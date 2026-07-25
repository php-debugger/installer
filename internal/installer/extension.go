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
	if existing.ExtensionDir == "" {
		return fmt.Errorf("php at %s reports no extension_dir; cannot install the extension", path)
	}

	// Nothing to do if the debugger is already present (e.g. our own
	// interpreter), unless forced (as by `update`).
	if !opts.Force {
		if has, _ := php.HasModule(ctx, path, php.DebuggerModule); has {
			opts.logf("%s already has the %s module; nothing to do.", path, php.DebuggerModule)
			return nil
		}
	}

	client := opts.Client
	if client == nil {
		client = release.NewClient()
	}
	rel, err := opts.latestRelease(ctx, client)
	if err != nil {
		return err
	}
	asset, err := release.SelectAsset(rel.Assets, release.Selector{
		Kind:   release.Extension,
		Series: existing.Series,
		ZTS:    existing.ZTS,
		OS:     p.OS,
		Arch:   p.Arch,
	})
	if err != nil {
		return err
	}

	opts.logf("Installing php-debugger extension for php %s (%s) %s, release %s",
		existing.Series, threading(existing.ZTS), p, rel.TagName)

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
	soDst := filepath.Join(existing.ExtensionDir, asset.Name)
	if err := registerFileRestore(soDst, rb, 0o755); err != nil {
		return err
	}
	if err := installFile(dlPath, soDst); err != nil {
		return fmt.Errorf("installing extension into %s: %w", existing.ExtensionDir, err)
	}
	opts.logf("  copied %s", soDst)

	// --- disable any real xdebug in the existing ini files ---
	if err := stripXdebugFromExisting(existing, rb, opts); err != nil {
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
	m.SetExtension(manifest.Extension{
		Series:      existing.Series,
		PHPVersion:  existing.Version,
		ZTS:         existing.ZTS,
		ReleaseTag:  rel.TagName,
		SoPath:      soDst,
		IniPath:     iniPath,
		InstalledAt: opts.clock(),
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
// the existing php can load its own extensions.
func stripXdebugFromExisting(existing *php.Info, rb *rollback, opts Options) error {
	var files []string
	if existing.Ini.LoadedFile != "" {
		files = append(files, existing.Ini.LoadedFile)
	}
	files = append(files, existing.Ini.AdditionalFiles...)
	return sanitizeInPlace(files, rb, opts)
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
