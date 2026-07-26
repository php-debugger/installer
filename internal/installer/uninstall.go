package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/php-debugger/installer/internal/manifest"
	"github.com/php-debugger/installer/internal/platform"
	"github.com/php-debugger/installer/internal/release"
)

// Uninstall removes an installed interpreter variant or the extension. For an
// interpreter, version selects a specific variant; empty means the active one.
// If removing the active interpreter leaves other variants, one is activated;
// otherwise a backed-up original interpreter is restored (if present).
func Uninstall(ctx context.Context, opts Options, wantInterp, wantExt bool, version string, zts bool) error {
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

	hasInterp := len(m.InterpreterKeys()) > 0
	hasExt := m.Extension != nil

	if !wantInterp && !wantExt {
		switch {
		case version != "":
			wantInterp = true
		case hasInterp && hasExt:
			return errors.New("both an interpreter and an extension are installed; " +
				"specify --interpreter or --extension")
		case hasInterp:
			wantInterp = true
		case hasExt:
			wantExt = true
		default:
			return errors.New("nothing installed to uninstall")
		}
	}

	if wantExt {
		return uninstallExtension(opts, layout, m)
	}
	return uninstallInterpreter(opts, layout, m, env, version, zts)
}

func uninstallInterpreter(opts Options, layout platform.Layout, m *manifest.Manifest, env platform.Env, version string, zts bool) error {
	key := version
	if key != "" {
		key = platform.VersionDirName(version, zts)
	} else {
		key = m.Active()
		if key == "" {
			return errors.New("no active interpreter to uninstall; specify a version")
		}
	}

	it, ok := m.Interpreter(key)
	if !ok {
		return fmt.Errorf("interpreter %q is not installed", key)
	}
	wasActive := m.Active() == key

	// Remove the copied config files (and prune the dirs they lived in if now
	// empty), then the versioned directory.
	for _, f := range it.ConfigFiles {
		if err := removeIfExists(f); err != nil {
			return fmt.Errorf("removing config %s: %w", f, err)
		}
	}
	pruneEmptyConfigDirs(it.ConfigFiles)
	if err := os.RemoveAll(it.Dir); err != nil {
		return fmt.Errorf("removing %s: %w", it.Dir, err)
	}
	m.RemoveInterpreter(key) // also clears active if it was active

	if wasActive {
		binDir := m.BinDir
		if binDir == "" {
			if binDir, _ = platform.SelectBinDir(layout.BinCandidates); binDir == "" {
				binDir = filepath.Dir(it.Dir)
			}
		}
		if err := reassignActive(opts, m, env, binDir); err != nil {
			return err
		}
	}

	if err := finalizeManifest(layout, m); err != nil {
		return fmt.Errorf("saving manifest: %w", err)
	}
	opts.logf("Uninstalled interpreter php %s (%s).", it.Series, threading(it.ZTS))
	return nil
}

// reassignActive decides what `php` points at after the active interpreter was
// removed: activate the highest remaining variant, or restore a backed-up
// original, or remove the symlink.
func reassignActive(opts Options, m *manifest.Manifest, env platform.Env, binDir string) error {
	if remaining := m.InterpreterKeys(); len(remaining) > 0 {
		newKey := highestKey(remaining)
		nit, _ := m.Interpreter(newKey)
		target := filepath.Join(nit.Dir, "bin", phpBinaryName(env.OS))
		if _, _, err := platform.Activate(binDir, "php", target); err != nil {
			return fmt.Errorf("activating php %s: %w", newKey, err)
		}
		m.SetActive(newKey)
		opts.logf("Switched active php -> %s (%s).", nit.Series, threading(nit.ZTS))
		return nil
	}

	// No variants left: restore the displaced original if we have one.
	if bkey, b, ok := anyBackup(m); ok {
		_ = platform.RemoveActive(binDir, "php")
		if err := restoreBackup(b.BackupPath, b.OriginalPath); err != nil {
			return fmt.Errorf("restoring backup to %s: %w", b.OriginalPath, err)
		}
		m.RemoveBackup(bkey)
		opts.logf("Restored the original interpreter at %s.", b.OriginalPath)
		return nil
	}

	if err := platform.RemoveActive(binDir, "php"); err != nil {
		return fmt.Errorf("removing active php: %w", err)
	}
	opts.logf("Removed the active php entry.")
	return nil
}

func uninstallExtension(opts Options, layout platform.Layout, m *manifest.Manifest) error {
	ext := m.Extension
	if ext == nil {
		return errors.New("no extension installed")
	}

	if ext.IniPath != "" {
		if err := removeExtensionLoader(ext.IniPath, ext.SoPath); err != nil {
			return fmt.Errorf("removing loader from %s: %w", ext.IniPath, err)
		}
	}
	if ext.SoPath != "" {
		if err := removeIfExists(ext.SoPath); err != nil {
			return fmt.Errorf("removing %s: %w", ext.SoPath, err)
		}
	}
	// Restore the ini files we modified in place at install (bringing back any
	// xdebug we disabled). This runs last so it is authoritative over the loader
	// removal above for a file that held both.
	for _, cb := range ext.ConfigBackups {
		if err := restoreBackup(cb.BackupPath, cb.OriginalPath); err != nil {
			return fmt.Errorf("restoring %s: %w", cb.OriginalPath, err)
		}
		opts.logf("Restored %s.", cb.OriginalPath)
	}
	m.ClearExtension()
	if err := finalizeManifest(layout, m); err != nil {
		return fmt.Errorf("saving manifest: %w", err)
	}
	opts.logf("Uninstalled the php-debugger extension for php %s.", ext.Series)
	return nil
}

// removeExtensionLoader removes the debugger loader from an ini file: it drops
// the zend_extension line pointing at soPath and our comment line. If the file
// becomes effectively empty (it was a dedicated loader file) it is removed;
// otherwise it is rewritten (the loader was appended to a shared php.ini).
func removeExtensionLoader(iniPath, soPath string) error {
	data, err := os.ReadFile(iniPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var kept []string
	for _, ln := range strings.Split(string(data), "\n") {
		if soPath != "" && strings.Contains(ln, soPath) {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(ln), "; Enables the php-debugger extension") {
			continue
		}
		kept = append(kept, ln)
	}
	result := strings.Join(kept, "\n")
	if strings.TrimSpace(result) == "" {
		return os.Remove(iniPath)
	}
	return os.WriteFile(iniPath, []byte(result), 0o644)
}

// anyBackup returns any backup recorded in the manifest (there is at most one
// meaningful one: the interpreter displaced when we first took over a location).
func anyBackup(m *manifest.Manifest) (string, manifest.Backup, bool) {
	for k := range m.Backups {
		b, _ := m.Backup(k)
		return k, b, true
	}
	return "", manifest.Backup{}, false
}

// highestKey returns the variant key with the highest PHP version.
func highestKey(keys []string) string {
	best := keys[0]
	for _, k := range keys[1:] {
		if release.CompareSeries(seriesOf(k), seriesOf(best)) > 0 {
			best = k
		}
	}
	return best
}

func seriesOf(key string) string { return strings.TrimSuffix(key, "-zts") }
