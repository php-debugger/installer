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
)

// Switch makes the given interpreter variant the active `php`. If the variant is
// already installed it just re-points the active symlink; otherwise it installs
// it first (activating at the current bin dir so all variants share one entry).
// opts.PHPVersion and opts.ZTS select the target.
func Switch(ctx context.Context, opts Options) error {
	if opts.PHPVersion == "" {
		return errors.New("switch requires a PHP version")
	}
	env, err := opts.env()
	if err != nil {
		return err
	}
	layout, err := platform.Resolve(env, opts.Scope)
	if err != nil {
		return err
	}
	m, err := manifest.Load(layout.ManifestPath())
	if err != nil {
		return err
	}

	key := platform.VersionDirName(opts.PHPVersion, opts.ZTS)
	it, installed := m.Interpreter(key)

	if !installed {
		opts.logf("php %s (%s) is not installed; installing it now.",
			opts.PHPVersion, threading(opts.ZTS))
		io := opts
		io.BinDir = m.BinDir // keep activation consistent with the current install
		return InstallInterpreter(ctx, io)
	}

	if m.Active() == key {
		opts.logf("php %s (%s) is already active.", opts.PHPVersion, threading(opts.ZTS))
		return nil
	}

	binDir := m.BinDir
	if binDir == "" {
		if binDir, err = platform.SelectBinDir(layout.BinCandidates); err != nil {
			return err
		}
	}
	target := filepath.Join(it.Dir, "bin", phpBinaryName(env.OS))
	if _, statErr := os.Stat(target); statErr != nil {
		return fmt.Errorf("installed interpreter binary missing at %s: %w", target, statErr)
	}

	// Re-point the active symlink, restoring the previous target if the new one
	// fails to run.
	prevTarget, _, hadPrev := platform.ReadActive(binDir, "php")
	activePath, kind, err := platform.Activate(binDir, "php", target)
	if err != nil {
		return fmt.Errorf("activating php %s: %w", key, err)
	}
	if err := php.SmokeTest(ctx, activePath); err != nil {
		if hadPrev {
			_, _, _ = platform.Activate(binDir, "php", prevTarget)
		}
		return fmt.Errorf("activated php %s failed to run; reverted: %w", key, err)
	}

	m.SetActive(key)
	if err := m.Save(layout.ManifestPath()); err != nil {
		return fmt.Errorf("saving manifest: %w", err)
	}
	opts.logf("Switched active php -> %s (%s) via %s. php -> %s",
		opts.PHPVersion, threading(opts.ZTS), kind, activePath)
	return nil
}
