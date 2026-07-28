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

	// Loaded up front so we can enforce the interpreter/extension invariant below
	// and reused for recording at the end.
	m, err := manifest.Load(layout.ManifestPath())
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

	// Enforce the invariant that the interpreter and the extension are never both
	// installed: the interpreter has the debugger compiled in, so a standalone
	// extension is redundant and its loader would double-register. Now that the
	// interpreter is proven to run on this host (smoke test passed) but before any
	// system change, remove any installed extension — reverting its ini edits so the
	// user's original config (e.g. xdebug) is restored and then re-derived cleanly by
	// the interpreter's own config handling below. Committed immediately so that if
	// the install later rolls back, disk and manifest still agree the extension is
	// gone. If existing is the php the extension modified, re-query it so its ini
	// paths reflect the reverted state (the extension's loader file is now gone).
	if m.Extension != nil {
		opts.logf("Removing the previously installed debugger extension (the interpreter includes it).")
		if err := revertExtension(opts, m.Extension); err != nil {
			return fmt.Errorf("removing the previously installed extension: %w", err)
		}
		m.ClearExtension()
		if err := m.Save(layout.ManifestPath()); err != nil {
			return fmt.Errorf("saving manifest after removing extension: %w", err)
		}
		if existing != nil {
			if refreshed, qErr := php.Query(ctx, existing.Path); qErr == nil {
				existing = refreshed
			}
		}
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

	// --- sanitize the ini config the new interpreter will load ---
	var configFiles []string
	var configBackups []manifest.FileBackup
	if existing != nil {
		// Replacing a foreign php: copy its configuration into the new interpreter's
		// config path (or sanitize it in place when they share a directory).
		opts.logf("Copying existing PHP configuration (removing xdebug) ...")
		configFiles, configBackups, err = copyConfig(existing, info, layout.BackupsDir(), rb, opts)
		if err != nil {
			rb.run()
			return fmt.Errorf("copying ini configuration; rolled back: %w", err)
		}
	} else {
		// No foreign php to copy from (e.g. switching versions while our own
		// interpreter is active, or a clean host). The new interpreter still reads its
		// compiled-in config path, which may hold a same-version Homebrew xdebug it
		// cannot load. Disable such loaders in place, backed up for restore.
		configBackups, err = sanitizeOwnConfig(info, layout.BackupsDir(), rb, opts)
		if err != nil {
			rb.run()
			return fmt.Errorf("sanitizing interpreter config; rolled back: %w", err)
		}
	}

	// --- back up and replace an existing interpreter at its location ---
	var backups []manifest.Backup
	if replaceExisting {
		opts.logf("Backing up existing interpreter at %s ...", existing.Path)
		backupPath, err := backupExisting(existing.Path, layout.BackupsDir(),
			platform.VersionDirName(series, opts.ZTS))
		if err != nil {
			rb.run()
			return fmt.Errorf("backing up existing interpreter; rolled back: %w", err)
		}
		origPath := existing.Path
		rb.add(func() error { return restoreBackup(backupPath, origPath) })
		backups = append(backups, manifest.Backup{OriginalPath: origPath, BackupPath: backupPath, CreatedAt: opts.clock()})
		maybeWarnManaged(opts, existing)
	}

	// --- preserve any un-backed-up file at the activation path(s) ---
	// Activate replaces whatever occupies the active `php` path, and on Windows it
	// materializes one of php.exe / php.cmd and removes the other. Back up anything
	// there we have not already preserved, so activation cannot destroy it
	// irrecoverably — a foreign php off PATH, one whose query failed (clean-host
	// fallback), a real sibling next to a replaced interpreter (a php.cmd beside the
	// detected php.exe), or a user-created symlink pointing at some other php. This
	// runs even after replacing a detected interpreter: that file is already moved
	// aside, so it is skipped here. Only a symlink that is our own active entry
	// (resolves into our install root) is skipped — everything else is preserved.
	bs, err := preserveDestination(activeCandidatePaths(p.OS, linkDir), layout.BackupsDir(),
		platform.VersionDirName(series, opts.ZTS), layout.Root, rb, opts)
	if err != nil {
		rb.run()
		return fmt.Errorf("backing up interpreter at the activation path; rolled back: %w", err)
	}
	backups = append(backups, bs...)

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
	key := platform.VersionDirName(series, opts.ZTS)
	m.InstallRoot = layout.Root
	m.BinDir = linkDir
	// Carry the config bookkeeping forward when this run did not re-derive it. A
	// reinstall/update runs over our own already-active interpreter, so no foreign
	// php is detected and copyConfig is skipped — but the prior record still holds
	// the copied files and the in-place ini backups uninstall needs. preserveConfig
	// keeps freshly-captured entries and fills the rest from the previous record.
	if prev, ok := m.Interpreter(key); ok {
		configFiles, configBackups = preserveConfig(prev, configFiles, configBackups)
	}
	m.SetInterpreter(key, manifest.Interpreter{
		Series:        series,
		PHPVersion:    info.Version,
		ZTS:           opts.ZTS,
		ReleaseTag:    rel.TagName,
		Dir:           versionDir,
		InstalledAt:   opts.clock(),
		ConfigFiles:   configFiles,
		ConfigBackups: configBackups,
	})
	m.SetActive(key)
	for i, b := range backups {
		m.SetBackup(backupKey(key, i), b)
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

// shouldPreserve reports whether the file at an activation candidate path must be
// backed up before activation overwrites it. A regular file always is (a foreign
// interpreter or shim). A symlink is preserved too — unless it is our own active
// entry, proven by resolving into our install root — because a user-created symlink
// to another php would otherwise be replaced with no way to restore it on
// uninstall. Missing paths and other node types (dirs, sockets) are left alone.
func shouldPreserve(path, root string) bool {
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if fi.Mode().IsRegular() {
		return true
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return !isOurInterpreter(path, root)
	}
	return false
}

// activeCandidatePaths lists the paths where activation may materialize the active
// `php`, so the installer can preserve any real file it is about to replace. On
// Windows activation writes either a php.exe symlink or a php.cmd shim (removing
// the other), so both are candidates; elsewhere it is just php. It mirrors
// platform.Activate's naming but keys off the target OS (not runtime.GOOS) so the
// backup decision is made for the platform being installed to.
func activeCandidatePaths(osID platform.OS, binDir string) []string {
	if osID == platform.Windows {
		return []string{
			filepath.Join(binDir, "php.exe"),
			filepath.Join(binDir, "php.cmd"),
		}
	}
	return []string{filepath.Join(binDir, "php")}
}

// preserveDestination backs up every candidate file that must not be lost to
// activation (see shouldPreserve), registering a rollback restore for each and
// returning the recorded backups. On Windows both php.exe and php.cmd can exist and
// activation clobbers/removes both forms, so both are preserved — not just the first
// found. root identifies our install so our own active symlink is not backed up.
func preserveDestination(candidates []string, backupDir, key, root string, rb *rollback, opts Options) ([]manifest.Backup, error) {
	var backups []manifest.Backup
	for _, p := range candidates {
		if !shouldPreserve(p, root) {
			continue
		}
		opts.logf("Backing up existing interpreter at %s ...", p)
		backupPath, err := backupExisting(p, backupDir, key)
		if err != nil {
			return backups, err
		}
		orig := p
		rb.add(func() error { return restoreBackup(backupPath, orig) })
		backups = append(backups, manifest.Backup{OriginalPath: orig, BackupPath: backupPath, CreatedAt: opts.clock()})
	}
	return backups, nil
}

// backupKey names a manifest backup entry. The first keeps the bare version key
// (the common single-backup case); extras get a suffix so multiple displaced files
// sharing one active slot (e.g. a Windows php.exe and php.cmd) coexist in the map.
func backupKey(versionKey string, index int) string {
	if index == 0 {
		return versionKey
	}
	return fmt.Sprintf("%s#%d", versionKey, index)
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
