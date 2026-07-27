package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	m.RemoveInterpreter(key) // in-memory; also clears active if it was active

	// Re-point the active `php` (or restore a backed-up original) BEFORE deleting
	// anything on disk. This is the step that can fail, so doing it first leaves the
	// interpreter and manifest untouched — and fully recoverable — if it does.
	// reassignActive works off the remaining variants (the key is already gone from
	// the map) and does not depend on the directory removed below.
	var consumedBackups []string
	if wasActive {
		binDir := m.BinDir
		if binDir == "" {
			if binDir, _ = platform.SelectBinDir(layout.BinCandidates); binDir == "" {
				binDir = filepath.Dir(it.Dir)
			}
		}
		cb, err := reassignActive(opts, m, env, binDir)
		if err != nil {
			return err
		}
		consumedBackups = cb
	}

	// Persist the consistent state (variant removed, active reassigned) before the
	// destructive cleanup, so a later removal failure can only leave orphan files —
	// never a manifest that points at an already-deleted interpreter directory.
	if err := finalizeManifest(layout, m); err != nil {
		return fmt.Errorf("saving manifest: %w", err)
	}

	// Only now that the manifest no longer references them, delete the backup files
	// the restore copied into place. Deferring until after the commit keeps a failed
	// save (or a partial restore) recoverable: the backups survive until the manifest
	// that would need them for a retry is gone.
	for _, p := range consumedBackups {
		_ = removeIfExists(p)
	}

	// Remove the copied config files (pruning now-empty dirs) and the versioned
	// directory. The active `php` no longer points into it.
	for _, f := range it.ConfigFiles {
		if err := removeIfExists(f); err != nil {
			return fmt.Errorf("removing config %s: %w", f, err)
		}
	}
	pruneEmptyConfigDirs(it.ConfigFiles)
	if err := os.RemoveAll(it.Dir); err != nil {
		return fmt.Errorf("removing %s: %w", it.Dir, err)
	}

	opts.logf("Uninstalled interpreter php %s (%s).", it.Series, threading(it.ZTS))
	return nil
}

// reassignActive decides what `php` points at after the active interpreter was
// removed: activate the highest remaining variant, or restore backed-up
// original(s), or remove the symlink. When it restores backups it returns their
// backup-file paths so the caller can delete them once the manifest is committed.
func reassignActive(opts Options, m *manifest.Manifest, env platform.Env, binDir string) ([]string, error) {
	if remaining := m.InterpreterKeys(); len(remaining) > 0 {
		newKey := highestKey(remaining)
		nit, _ := m.Interpreter(newKey)
		target := filepath.Join(nit.Dir, "bin", phpBinaryName(env.OS))
		if _, _, err := platform.Activate(binDir, "php", target); err != nil {
			return nil, fmt.Errorf("activating php %s: %w", newKey, err)
		}
		m.SetActive(newKey)
		opts.logf("Switched active php -> %s (%s).", nit.Series, threading(nit.ZTS))
		return nil, nil
	}

	// No variants left: restore every displaced original we backed up (a single
	// active `php` can have two materializations on Windows — php.exe and php.cmd).
	// Restores are COPIES that leave the backup files intact, so a partial failure is
	// safely retryable: the on-disk manifest still references every backup and every
	// backup file still exists, so a re-run restores them all again. The backup files
	// are consumed only after the manifest commit (by the caller). Removing the active
	// entry must precede the restores (a backup often targets that path); if a restore
	// then fails and nothing has been restored yet, re-activate the entry we removed
	// so the user is not left without a php. Once a restore has succeeded we do not
	// re-activate — that would clobber the restored file (and on Windows drop its
	// sibling), and the restored original already provides a working php.
	if keys := backupKeys(m); len(keys) > 0 {
		prevTarget, _, hadPrev := platform.ReadActive(binDir, "php")
		_ = platform.RemoveActive(binDir, "php")
		restoredAny := false
		for _, bkey := range keys {
			b, _ := m.Backup(bkey)
			if err := copyNode(b.BackupPath, b.OriginalPath, 0o755); err != nil {
				if !restoredAny && hadPrev {
					_, _, _ = platform.Activate(binDir, "php", prevTarget)
				}
				return nil, fmt.Errorf("restoring backup to %s: %w", b.OriginalPath, err)
			}
			restoredAny = true
		}
		// All restored: drop the backups from the manifest and hand the backup files
		// to the caller to delete after it persists this state.
		var consumed []string
		for _, bkey := range keys {
			b, _ := m.Backup(bkey)
			consumed = append(consumed, b.BackupPath)
			m.RemoveBackup(bkey)
			opts.logf("Restored the original interpreter at %s.", b.OriginalPath)
		}
		return consumed, nil
	}

	if err := platform.RemoveActive(binDir, "php"); err != nil {
		return nil, fmt.Errorf("removing active php: %w", err)
	}
	opts.logf("Removed the active php entry.")
	return nil, nil
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

// anyBackup returns any backup recorded in the manifest, if one exists.
func anyBackup(m *manifest.Manifest) (string, manifest.Backup, bool) {
	for k := range m.Backups {
		b, _ := m.Backup(k)
		return k, b, true
	}
	return "", manifest.Backup{}, false
}

// backupKeys returns the recorded backup keys in a stable order (the bare version
// key sorts before its "#N" extras), so multiple displaced files are restored
// deterministically, primary first.
func backupKeys(m *manifest.Manifest) []string {
	keys := make([]string, 0, len(m.Backups))
	for k := range m.Backups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
