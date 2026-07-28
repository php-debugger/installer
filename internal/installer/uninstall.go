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

// Uninstall removes the installed interpreter or extension. The two are never
// installed at once (installing the interpreter removes any extension — see
// InstallInterpreter), so the kind is unambiguous and need not be specified.
// version selects a specific interpreter variant; empty means the active one (and
// implies an interpreter). If removing the active interpreter leaves other
// variants, one is activated; otherwise a backed-up original interpreter is
// restored (if present).
func Uninstall(ctx context.Context, opts Options, version string, zts bool) error {
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

	if err := reconcileInvariant(layout, m, opts); err != nil {
		return err
	}

	// A version always refers to an interpreter variant. Otherwise uninstall
	// whichever kind is installed.
	switch {
	case version != "" || len(m.InterpreterKeys()) > 0:
		return uninstallInterpreter(opts, layout, m, env, version, zts)
	case m.Extension != nil:
		return uninstallExtension(opts, layout, m)
	default:
		return errors.New("nothing installed to uninstall")
	}
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

	// Restore the existing php's own ini files we sanitized in place at install
	// (e.g. bringing back a stripped xdebug). These are the user's real config files
	// — living in a directory the new interpreter happened to share — so they are
	// restored to their original contents, never deleted. This must precede the
	// commit: the ini backups live under the install root, which finalizeManifest
	// removes wholesale once nothing is installed. Restore by COPY so a failed commit
	// stays retryable (the backup survives), then let the post-commit cleanup below
	// drop the backup file along with the other consumed backups.
	for _, cb := range it.ConfigBackups {
		if err := copyBackup(cb.BackupPath, cb.OriginalPath); err != nil {
			return fmt.Errorf("restoring %s: %w", cb.OriginalPath, err)
		}
		consumedBackups = append(consumedBackups, cb.BackupPath)
		opts.logf("Restored %s.", cb.OriginalPath)
	}

	// Persist the consistent state (variant removed, active reassigned) before the
	// destructive cleanup, so a later removal failure can only leave orphan files —
	// never a manifest that points at an already-deleted interpreter directory.
	if err := finalizeManifest(layout, m); err != nil {
		return fmt.Errorf("saving manifest: %w", err)
	}

	// Only now that the manifest no longer references them, delete the backup files
	// the restores copied into place. Deferring until after the commit keeps a failed
	// save (or a partial restore) recoverable: the backups survive until the manifest
	// that would need them for a retry is gone. (finalizeManifest may already have
	// removed the whole root — and these with it — when nothing remains installed.)
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
	if err := revertExtension(opts, ext); err != nil {
		return err
	}
	m.ClearExtension()
	if err := finalizeManifest(layout, m); err != nil {
		return fmt.Errorf("saving manifest: %w", err)
	}
	opts.logf("Uninstalled the php-debugger extension for php %s.", ext.Series)
	return nil
}

// revertExtension undoes an extension install on disk: it removes the loader line,
// deletes the copied .so, and restores the ini files we modified in place (bringing
// back any xdebug we disabled). The restore runs last so it is authoritative over
// the loader removal for a file that held both. It does not touch the manifest —
// callers clear the record and persist.
func revertExtension(opts Options, ext *manifest.Extension) error {
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
	for _, cb := range ext.ConfigBackups {
		if err := restoreBackup(cb.BackupPath, cb.OriginalPath); err != nil {
			return fmt.Errorf("restoring %s: %w", cb.OriginalPath, err)
		}
		opts.logf("Restored %s.", cb.OriginalPath)
	}
	return nil
}

// reconcileInvariant repairs a manifest left in the impossible state where both an
// interpreter and the extension are recorded. Older versions installed the
// interpreter without removing a previously installed extension; the interpreter
// supersedes it (its loader is already disabled), so the stale extension record is
// dropped and the manifest re-saved. The orphaned .so is inert (never loaded) and
// left in place rather than risk touching the user's files during an unrelated
// command. No-op for a consistent manifest.
func reconcileInvariant(layout platform.Layout, m *manifest.Manifest, opts Options) error {
	if m.Extension == nil || len(m.InterpreterKeys()) == 0 {
		return nil
	}
	opts.logf("Note: found a stale extension record alongside an installed interpreter; " +
		"removing it (the interpreter includes the debugger and supersedes the extension).")
	m.ClearExtension()
	return m.Save(layout.ManifestPath())
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
