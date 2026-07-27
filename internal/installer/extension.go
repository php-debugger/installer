package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/php-debugger/installer/internal/manifest"
	"github.com/php-debugger/installer/internal/php"
	"github.com/php-debugger/installer/internal/platform"
	"github.com/php-debugger/installer/internal/release"
)

// ErrNoInterpreter is returned by InstallExtension when no php is found on PATH.
var ErrNoInterpreter = errors.New(
	"no PHP interpreter found on PATH.\n" +
		"The extension needs an existing PHP to install into. Install the debugger\n" +
		"interpreter instead (php-debugger install), or install PHP and retry.")

// InstallExtension installs just the debugger extension into the current PHP:
// it downloads the extension matching that php's version/threading, copies it
// into the php's extension_dir, disables any real xdebug in the existing ini
// files, enables the extension, and verifies it loads — reverting everything on
// failure.
func InstallExtension(ctx context.Context, opts Options) error {
	env, err := opts.env()
	if err != nil {
		return err
	}
	p := platform.Platform{OS: env.OS, Arch: env.Arch}

	layout, err := platform.Resolve(env, opts.Scope)
	if err != nil {
		return err
	}

	// Require an existing interpreter.
	path, err := php.Detect()
	if err != nil {
		return ErrNoInterpreter
	}
	existing, err := php.Query(ctx, path)
	if err != nil {
		return fmt.Errorf("querying php at %s: %w", path, err)
	}

	// Nothing to do if the debugger is already available, unless forced (as by
	// `update`). This covers two cases: the php on PATH already loads the
	// extension, or it is our own interpreter with the debugger compiled in
	// (both report the module via `php -m`).
	if !opts.Force {
		if has, _ := php.HasModule(ctx, path, php.DebuggerModule); has {
			if isOurInterpreter(path, layout.Root) {
				opts.logf("php at %s is the php-debugger interpreter, which already includes the debugger built in; nothing to do.", path)
			} else {
				opts.logf("php at %s already loads the %s extension; nothing to do.", path, php.DebuggerModule)
			}
			return nil
		}
	}

	if existing.ExtensionDir == "" {
		return fmt.Errorf("php at %s reports no extension_dir; cannot install the extension", path)
	}

	client := opts.Client
	if client == nil {
		client = release.NewClient()
	}
	rel, err := opts.latestRelease(ctx, client)
	if err != nil {
		return err
	}

	// The extension must match the architecture the existing php runs as, which
	// can differ from the host (e.g. an Intel php under Rosetta on Apple Silicon).
	// Fall back to the host arch only if php did not report its machine.
	arch := p.Arch
	if existing.Machine != "" {
		a, err := platform.ArchFromMachine(existing.Machine)
		if err != nil {
			return fmt.Errorf("determining architecture of php at %s: %w", path, err)
		}
		arch = a
	}
	target := platform.Platform{OS: p.OS, Arch: arch}

	asset, err := release.SelectAsset(rel.Assets, release.Selector{
		Kind:   release.Extension,
		Series: existing.Series,
		ZTS:    existing.ZTS,
		OS:     target.OS,
		Arch:   target.Arch,
	})
	if err != nil {
		return err
	}

	opts.logf("Installing php-debugger extension for php %s (%s) %s, release %s",
		existing.Series, threading(existing.ZTS), target, rel.TagName)

	tmpDir, err := os.MkdirTemp("", "php-debugger-ext-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	opts.logf("Downloading %s ...", asset.Name)
	dlPath, err := client.Download(ctx, asset, tmpDir)
	if err != nil {
		return err
	}

	rb := &rollback{}

	// --- copy the extension into extension_dir ---
	// Resolve a relative extension_dir to an absolute path. PHP may report one
	// (notably Windows configs default to "ext"), which it resolves against the PHP
	// installation directory — not the installer's working directory. Installing to
	// the raw relative value would drop the .so under the current directory and
	// record a CWD-relative loader path that only loads when php runs from here.
	extDir := resolveExtensionDir(existing.ExtensionDir, path)
	if extDir != existing.ExtensionDir {
		opts.logf("Note: php reports a relative extension_dir (%q); installing into %s.",
			existing.ExtensionDir, extDir)
	}
	soDst := filepath.Join(extDir, asset.Name)
	if err := registerFileRestore(soDst, rb, 0o755); err != nil {
		return err
	}
	if err := installFile(dlPath, soDst); err != nil {
		return fmt.Errorf("installing extension into %s: %w", extDir, err)
	}
	opts.logf("  copied %s", soDst)

	// --- disable any real xdebug in the existing ini files ---
	// Back the originals up (under the install root) so uninstall can restore the
	// user's xdebug config, since these are their live files, not copies.
	iniBackups, err := stripXdebugFromExisting(existing, layout.BackupsDir(), rb, opts)
	if err != nil {
		rb.run()
		return fmt.Errorf("updating existing ini files; reverted: %w", err)
	}

	// --- enable the extension ---
	iniPath, err := enableExtension(existing, soDst, rb)
	if err != nil {
		rb.run()
		return fmt.Errorf("enabling extension; reverted: %w", err)
	}
	opts.logf("  enabled via %s", iniPath)

	// --- verify it loads ---
	opts.logf("Confirming the extension loads ...")
	has, err := php.HasModule(ctx, path, php.DebuggerModule)
	if err != nil {
		rb.run()
		return fmt.Errorf("checking for the debugger module; reverted: %w", err)
	}
	if !has {
		rb.run()
		return fmt.Errorf("the extension did not load in %s (its build may not match this php); reverted", path)
	}

	// --- record in the manifest ---
	m, err := manifest.Load(layout.ManifestPath())
	if err != nil {
		rb.run()
		return err
	}
	m.InstallRoot = layout.Root
	// Preserve the user's original ini backups across reinstalls/updates. A re-run
	// (e.g. `update`, which forces past the already-installed guard) typically finds
	// xdebug already stripped, so stripXdebugFromExisting produces no new backups;
	// the prior extension record still holds the true pre-strip originals, so carry
	// those forward for any file we did not freshly back up this run — otherwise
	// uninstall could no longer restore the user's xdebug configuration.
	iniBackups = preserveBackups(m.Extension, iniBackups)
	m.SetExtension(manifest.Extension{
		Series:        existing.Series,
		PHPVersion:    existing.Version,
		ZTS:           existing.ZTS,
		ReleaseTag:    rel.TagName,
		SoPath:        soDst,
		IniPath:       iniPath,
		InstalledAt:   opts.clock(),
		ConfigBackups: iniBackups,
	})
	if err := m.Save(layout.ManifestPath()); err != nil {
		rb.run()
		return fmt.Errorf("saving manifest; reverted: %w", err)
	}

	opts.logf("Installed the php-debugger extension into %s.", path)
	return nil
}

// stripXdebugFromExisting disables a real xdebug in the existing php's ini files
// (in place) so it does not conflict with the debugger's simulated one. It reuses
// the shared ini rules (see rewriteIniFiles) without commenting other loaders —
// the existing php can load its own extensions. Modified files are backed up
// under backupDir and returned so uninstall can restore the user's xdebug config.
func stripXdebugFromExisting(existing *php.Info, backupDir string, rb *rollback, opts Options) ([]manifest.FileBackup, error) {
	var files []string
	if existing.Ini.LoadedFile != "" {
		files = append(files, existing.Ini.LoadedFile)
	}
	files = append(files, existing.Ini.AdditionalFiles...)
	return sanitizeInPlace(files, backupDir, rb, opts)
}

// preserveBackups merges the ini backups created this run (fresh) with those from
// a prior extension record (if any). A freshly-captured backup wins for its own
// file — it holds that file's pre-strip contents from this run. For files not
// touched this run (the common update case, where xdebug was already stripped at
// first install), the prior backup is kept, since it holds the user's true
// original contents. Returns fresh unchanged when there is nothing to preserve.
func preserveBackups(prior *manifest.Extension, fresh []manifest.FileBackup) []manifest.FileBackup {
	if prior == nil || len(prior.ConfigBackups) == 0 {
		return fresh
	}
	freshByPath := make(map[string]bool, len(fresh))
	for _, b := range fresh {
		freshByPath[b.OriginalPath] = true
	}
	merged := append([]manifest.FileBackup(nil), fresh...)
	for _, b := range prior.ConfigBackups {
		if !freshByPath[b.OriginalPath] {
			merged = append(merged, b)
		}
	}
	return merged
}

// resolveExtensionDir returns an absolute extension directory. PHP sometimes
// reports a relative extension_dir (notably Windows' default "ext"), which it
// resolves against the PHP installation directory rather than the current working
// directory. We mirror that by resolving it against the php binary's directory
// (following symlinks to the real install location), so the .so is installed
// alongside PHP's other extensions and the loader references it by an absolute
// path. An already-absolute dir is returned unchanged.
func resolveExtensionDir(extDir, phpPath string) string {
	if filepath.IsAbs(extDir) {
		return extDir
	}
	base := filepath.Dir(phpPath)
	if resolved, err := filepath.EvalSymlinks(phpPath); err == nil {
		base = filepath.Dir(resolved)
	}
	return filepath.Join(base, extDir)
}

// enableExtension writes a loader that enables the debugger extension: a
// dedicated ini in the scan dir if there is one, otherwise appended to the main
// php.ini. Returns the ini file path. Registers undo on rb.
func enableExtension(existing *php.Info, soPath string, rb *rollback) (string, error) {
	line := "; Enables the php-debugger extension (added by php-debugger)\nzend_extension=" + soPath + "\n"

	if existing.Ini.ScanDir != "" {
		iniPath := filepath.Join(existing.Ini.ScanDir, "99-php-debugger.ini")
		if err := registerFileRestore(iniPath, rb, 0o644); err != nil {
			return "", err
		}
		if err := os.MkdirAll(existing.Ini.ScanDir, 0o755); err != nil {
			return "", fmt.Errorf("creating scan dir: %w", err)
		}
		if err := os.WriteFile(iniPath, []byte(line), 0o644); err != nil {
			return "", err
		}
		return iniPath, nil
	}

	if existing.Ini.LoadedFile != "" {
		iniPath := existing.Ini.LoadedFile
		prev, err := os.ReadFile(iniPath)
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", iniPath, err)
		}
		if err := registerFileRestore(iniPath, rb, 0o644); err != nil {
			return "", err
		}
		if err := os.WriteFile(iniPath, append(prev, []byte("\n"+line)...), 0o644); err != nil {
			return "", err
		}
		return iniPath, nil
	}

	return "", errors.New("no ini file available to enable the extension (php reports no scan dir or loaded php.ini)")
}
