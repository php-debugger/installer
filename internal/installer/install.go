// Package installer orchestrates installing, updating and removing the PHP
// debugger — wiring together platform detection, release resolution, download,
// verification, symlinking and the on-disk manifest, with rollback on failure.
package installer

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/php-debugger/installer/internal/manifest"
	"github.com/php-debugger/installer/internal/php"
	"github.com/php-debugger/installer/internal/platform"
	"github.com/php-debugger/installer/internal/release"
)

// Options configures an install.
type Options struct {
	Scope      platform.Scope
	PHPVersion string // PHP series (e.g. "8.3"); empty means latest available
	ZTS        bool
	AssumeYes  bool

	// Out receives human-readable progress output (may be nil).
	Out io.Writer
	// In is read for interactive confirmations (defaults to os.Stdin via the CLI;
	// may be nil, in which case prompts default to "no" unless AssumeYes).
	In io.Reader

	// BinDir, when set, forces the active `php` to be placed in this directory
	// instead of auto-choosing (replace-existing or scope bin dir). Used by
	// `switch` so a newly installed variant activates where the current one lives.
	BinDir string

	// Force skips "nothing to do" short-circuits (used by `update` to reinstall
	// even when the debugger is already present).
	Force bool

	// Client and Env are optional overrides for testing. When nil, real ones are
	// constructed.
	Client *release.Client
	Env    *platform.Env

	// preloadedRelease, when set, is used instead of fetching the latest release
	// (so `update` can fetch once to compare versions, then reuse it).
	preloadedRelease *release.Release

	now func() time.Time // optional clock override for tests
}

// latestRelease returns the preloaded release if set, otherwise fetches it.
func (o Options) latestRelease(ctx context.Context, client *release.Client) (*release.Release, error) {
	if o.preloadedRelease != nil {
		return o.preloadedRelease, nil
	}
	return client.LatestRelease(ctx)
}

func (o Options) logf(format string, args ...any) {
	if o.Out != nil {
		fmt.Fprintf(o.Out, format+"\n", args...)
	}
}

func (o Options) env() (platform.Env, error) {
	if o.Env != nil {
		return *o.Env, nil
	}
	return platform.CurrentEnv()
}

func (o Options) clock() time.Time {
	if o.now != nil {
		return o.now()
	}
	return time.Now().UTC()
}

// confirm asks the user a yes/no question. Returns true under --yes; otherwise
// reads a line from In (defaulting to "no" if there is no input).
func (o Options) confirm(question string) bool {
	if o.AssumeYes {
		return true
	}
	if o.In == nil {
		return false
	}
	if o.Out != nil {
		fmt.Fprintf(o.Out, "%s [y/N]: ", question)
	}
	line, _ := bufio.NewReader(o.In).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

// InstallInterpreter installs a self-contained PHP interpreter with the debugger
// compiled in, and activates it. If an existing interpreter is found it is backed
// up and replaced at its location, and its ini configuration is copied (minus
// xdebug) into the new interpreter's config path. On any failure after the first
// filesystem change, it rolls back.
func InstallInterpreter(ctx context.Context, opts Options) error {
	env, err := opts.env()
	if err != nil {
		return err
	}
	p := platform.Platform{OS: env.OS, Arch: env.Arch}

	layout, err := platform.Resolve(env, opts.Scope)
	if err != nil {
		return err
	}

	client := opts.Client
	if client == nil {
		client = release.NewClient()
	}
	rel, err := opts.latestRelease(ctx, client)
	if err != nil {
		return err
	}

	series := opts.PHPVersion
	if series == "" {
		series, err = release.LatestSeries(rel.Assets, release.Interpreter, opts.ZTS, p.OS, p.Arch)
		if err != nil {
			return err
		}
	}
	asset, err := release.SelectAsset(rel.Assets, release.Selector{
		Kind:   release.Interpreter,
		Series: series,
		ZTS:    opts.ZTS,
		OS:     p.OS,
		Arch:   p.Arch,
	})
	if err != nil {
		return err
	}

	// If our own interpreter for this exact version (series + threading) is
	// already active with the debugger, there is nothing to do. `update` sets
	// Force to reinstall against the latest release regardless.
	if !opts.Force && alreadyProvided(ctx, opts, layout.Root, series) {
		return nil
	}

	// Detect a pre-existing interpreter before we change anything. Ignore one
	// that is our own previous install (avoid backing up our own symlink).
	existing := detectExisting(ctx, opts, layout.Root)

	// Decide where the active `php` goes: replace the existing interpreter at its
	// location when possible, else our scope's bin dir.
	linkDir, replaceExisting, err := chooseLinkDir(existing, layout, opts.BinDir)
	if err != nil {
		return err
	}

	opts.logf("Installing php-debugger interpreter: php %s (%s) %s, release %s",
		series, threading(opts.ZTS), p, rel.TagName)

	// --- download to a temp dir ---
	tmpDir, err := os.MkdirTemp("", "php-debugger-dl-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	opts.logf("Downloading %s ...", asset.Name)
	dlPath, err := client.Download(ctx, asset, tmpDir)
	if err != nil {
		return err
	}
	if err := os.Chmod(dlPath, 0o755); err != nil {
		return err
	}

	// --- smoke test before touching anything on the system ---
	opts.logf("Verifying the interpreter runs on this system ...")
	if err := php.SmokeTest(ctx, dlPath); err != nil {
		return smokeFailureError(err)
	}
	info, err := php.Query(ctx, dlPath)
	if err != nil {
		return fmt.Errorf("querying downloaded interpreter: %w", err)
	}

	rb := &rollback{}

	// --- place the binary into the versioned directory ---
	versionDir := layout.VersionDir(series, opts.ZTS)
	binTarget := filepath.Join(versionDir, "bin", phpBinaryName(p.OS))
	// If the version dir already exists (an update/reinstall), preserve the old
	// binary so a failed install can be rolled back to a working state; else the
	// whole fresh dir is removed on rollback.
	versionExisted := isDir(versionDir)
	if versionExisted {
		if err := registerFileRestore(binTarget, rb, 0o755); err != nil {
			return err
		}
	}
	if err := installFile(dlPath, binTarget); err != nil {
		if !versionExisted {
			os.RemoveAll(versionDir)
		}
		return fmt.Errorf("installing interpreter binary: %w", err)
	}
	if !versionExisted {
		rb.add(func() error { return os.RemoveAll(versionDir) })
	}

	// --- copy the existing interpreter's ini config into the new one ---
	var configFiles []string
	if existing != nil {
		opts.logf("Copying existing PHP configuration (removing xdebug) ...")
		configFiles, err = copyConfig(existing, info, rb, opts)
		if err != nil {
			rb.run()
			return fmt.Errorf("copying ini configuration; rolled back: %w", err)
		}
	}

	// --- back up and replace an existing interpreter at its location ---
	var backup *manifest.Backup
	if replaceExisting {
		opts.logf("Backing up existing interpreter at %s ...", existing.Path)
		backupPath, err := backupExisting(existing.Path, layout.BackupsDir(),
			platform.VersionDirName(series, opts.ZTS), time.Now().UnixNano())
		if err != nil {
			rb.run()
			return fmt.Errorf("backing up existing interpreter; rolled back: %w", err)
		}
		origPath := existing.Path
		rb.add(func() error { return restoreBackup(backupPath, origPath) })
		backup = &manifest.Backup{OriginalPath: origPath, BackupPath: backupPath, CreatedAt: opts.clock()}
		maybeWarnManaged(opts, existing)
	}

	// --- activate (symlink into the chosen bin dir) ---
	prevTarget, _, hadPrev := platform.ReadActive(linkDir, "php")
	activePath, kind, err := platform.Activate(linkDir, "php", binTarget)
	if err != nil {
		rb.run()
		return fmt.Errorf("activating interpreter; rolled back: %w", err)
	}
	rb.add(func() error {
		if hadPrev {
			_, _, e := platform.Activate(linkDir, "php", prevTarget)
			return e
		}
		return platform.RemoveActive(linkDir, "php")
	})

	// --- post-verify via the activated entry ---
	opts.logf("Confirming the installed interpreter works and has the debugger ...")
	if err := php.SmokeTest(ctx, activePath); err != nil {
		rb.run()
		return fmt.Errorf("installed interpreter failed to run; rolled back: %w", err)
	}
	hasDebugger, err := php.HasModule(ctx, activePath, php.DebuggerModule)
	if err != nil {
		rb.run()
		return fmt.Errorf("checking for debugger module; rolled back: %w", err)
	}
	if !hasDebugger {
		rb.run()
		return fmt.Errorf("installed interpreter does not report the %q module; rolled back",
			php.DebuggerModule)
	}

	// --- record in the manifest ---
	m, err := manifest.Load(layout.ManifestPath())
	if err != nil {
		rb.run()
		return err
	}
	key := platform.VersionDirName(series, opts.ZTS)
	m.InstallRoot = layout.Root
	m.BinDir = linkDir
	m.SetInterpreter(key, manifest.Interpreter{
		Series:      series,
		PHPVersion:  info.Version,
		ZTS:         opts.ZTS,
		ReleaseTag:  rel.TagName,
		Dir:         versionDir,
		InstalledAt: opts.clock(),
		ConfigFiles: configFiles,
	})
	m.SetActive(key)
	if backup != nil {
		m.SetBackup(key, *backup)
	}
	if err := m.Save(layout.ManifestPath()); err != nil {
		rb.run()
		return fmt.Errorf("saving manifest; rolled back: %w", err)
	}

	opts.logf("Installed php %s (%s). Active php -> %s", info.Version, kind, activePath)
	if !replaceExisting {
		warnIfNotOnPATH(opts, p.OS, linkDir)
	}
	return nil
}

// alreadyProvided reports whether the php currently on PATH is our own
// interpreter for this exact version (series + threading) with the debugger — in
// which case installing again would be a no-op. It logs an informative message
// when so. A normal php that merely loads our extension does NOT count: it
// returns false so the interpreter install proceeds (and disables that
// extension). Callers gate this on !Force.
func alreadyProvided(ctx context.Context, opts Options, root, series string) bool {
	path, err := php.Detect()
	if err != nil || !isOurInterpreter(path, root) {
		return false
	}
	info, err := php.Query(ctx, path)
	if err != nil || info.Series != series || info.ZTS != opts.ZTS {
		return false
	}
	if has, _ := php.HasModule(ctx, path, php.DebuggerModule); !has {
		return false
	}
	opts.logf("php %s (%s) with the debugger is already installed and active at %s; nothing to do.",
		series, threading(opts.ZTS), path)
	return true
}

// detectExisting finds a pre-existing php on PATH and queries it. It returns nil
// if none is found, if it cannot be queried, or if it is our own previous
// install under root (which must not be treated as a foreign interpreter).
func detectExisting(ctx context.Context, opts Options, root string) *php.Info {
	path, err := php.Detect()
	if err != nil {
		return nil
	}
	if isOurInterpreter(path, root) {
		return nil // our own previously-installed interpreter
	}
	info, err := php.Query(ctx, path)
	if err != nil {
		opts.logf("Note: found php at %s but could not query it (%v); treating as clean host.", path, err)
		return nil
	}
	return info
}

// chooseLinkDir decides where the active `php` entry goes. An explicit override
// (from `switch`) wins. Otherwise, when an existing interpreter is found and its
// directory is writable, we replace it in place; else we use the scope's first
// writable bin dir.
func chooseLinkDir(existing *php.Info, layout platform.Layout, override string) (dir string, replace bool, err error) {
	if override != "" {
		return override, false, nil
	}
	if existing != nil {
		d := filepath.Dir(existing.Path)
		if platform.IsDirWritable(d) {
			return d, true, nil
		}
	}
	binDir, err := platform.SelectBinDir(layout.BinCandidates)
	if err != nil {
		return "", false, fmt.Errorf("%w\ntry --user for a per-user install, or re-run with elevated privileges", err)
	}
	return binDir, false, nil
}

// isOurInterpreter reports whether the php at path is one we installed (its real
// path, after resolving symlinks, lives under our install root). Both sides are
// canonicalized so symlinked path prefixes (e.g. macOS /var -> /private/var) do
// not cause a false negative.
func isOurInterpreter(path, root string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	return isUnderRoot(resolved, root)
}

func isUnderRoot(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// maybeWarnManaged warns when the replaced interpreter appears to be managed by a
// package manager (e.g. Homebrew), since a future upgrade may recreate it.
func maybeWarnManaged(opts Options, existing *php.Info) {
	target, _ := filepath.EvalSymlinks(existing.Path)
	low := strings.ToLower(existing.Path + " " + target)
	if strings.Contains(low, "cellar") || strings.Contains(low, "homebrew") {
		opts.logf("Note: %s looks package-manager-managed; a future upgrade (e.g. `brew upgrade`) may recreate it and shadow this install.", existing.Path)
	}
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func threading(zts bool) string {
	if zts {
		return "zts"
	}
	return "nts"
}

func phpBinaryName(osID platform.OS) string {
	if osID == platform.Windows {
		return "php.exe"
	}
	return "php"
}

func smokeFailureError(err error) error {
	return fmt.Errorf(`the downloaded interpreter failed to run on this system:

%w

You can instead install only the debugger extension:
    php-debugger install --extension-only
or build from source following the instructions at
    https://github.com/php-debugger/php-debugger`, err)
}

func warnIfNotOnPATH(opts Options, osID platform.OS, binDir string) {
	if platform.IsOnPATH(osID, binDir, os.Getenv("PATH")) {
		return
	}
	opts.logf("")
	opts.logf("Note: %s is not on your PATH.", binDir)
	if osID == platform.Windows {
		opts.logf("Add it via System Properties > Environment Variables, or:")
		opts.logf(`    setx PATH "%s;%%PATH%%"`, binDir)
	} else {
		opts.logf("Add it to your shell profile, e.g.:")
		opts.logf(`    export PATH="%s:$PATH"`, binDir)
	}
}
