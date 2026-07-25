// Package installer orchestrates installing, updating and removing the PHP
// debugger — wiring together platform detection, release resolution, download,
// verification, symlinking and the on-disk manifest, with rollback on failure.
package installer

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

	// Client and Env are optional overrides for testing. When nil, real ones are
	// constructed.
	Client *release.Client
	Env    *platform.Env

	now func() time.Time // optional clock override for tests
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

// InstallInterpreter installs a self-contained PHP interpreter with the debugger
// compiled in, and activates it. This is the clean-host path: it does not yet
// detect or back up a pre-existing interpreter (that is layered on in a later
// step). On any failure after the first filesystem change, it rolls back.
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
	binDir, err := platform.SelectBinDir(layout.BinCandidates)
	if err != nil {
		return fmt.Errorf("%w\ntry --user for a per-user install, or re-run with elevated privileges", err)
	}

	client := opts.Client
	if client == nil {
		client = release.NewClient()
	}

	rel, err := client.LatestRelease(ctx)
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

	// --- place into the versioned directory ---
	rb := &rollback{}
	versionDir := layout.VersionDir(series, opts.ZTS)
	binTarget := filepath.Join(versionDir, "bin", phpBinaryName(p.OS))
	if err := installFile(dlPath, binTarget); err != nil {
		return fmt.Errorf("installing interpreter binary: %w", err)
	}
	rb.add(func() error { return os.RemoveAll(versionDir) })

	// --- activate (symlink/shim into the bin dir) ---
	prevTarget, _, hadPrev := platform.ReadActive(binDir, "php")
	activePath, kind, err := platform.Activate(binDir, "php", binTarget)
	if err != nil {
		rb.run()
		return fmt.Errorf("activating interpreter: %w", err)
	}
	rb.add(func() error {
		if hadPrev {
			_, _, e := platform.Activate(binDir, "php", prevTarget)
			return e
		}
		return platform.RemoveActive(binDir, "php")
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
	m.BinDir = binDir
	m.SetInterpreter(key, manifest.Interpreter{
		Series:      series,
		PHPVersion:  info.Version,
		ZTS:         opts.ZTS,
		ReleaseTag:  rel.TagName,
		Dir:         versionDir,
		InstalledAt: opts.clock(),
	})
	m.SetActive(key)
	if err := m.Save(layout.ManifestPath()); err != nil {
		rb.run()
		return fmt.Errorf("saving manifest; rolled back: %w", err)
	}

	opts.logf("Installed php %s (%s). Active php -> %s", info.Version, kind, activePath)
	warnIfNotOnPATH(opts, p.OS, binDir)
	return nil
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
