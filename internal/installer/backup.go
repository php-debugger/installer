package installer

import (
	"fmt"
	"os"
	"path/filepath"
)

// backupExisting moves the existing interpreter at srcPath into backupDir under a
// unique name, preserving whatever it is (a real binary or a symlink). It falls
// back to copy+remove if the move crosses filesystems. Returns the backup path.
//
// The destination is reserved with CreateTemp so its name is guaranteed unique:
// one install can back up several files under the same key (e.g. a Windows php.exe
// and php.cmd sharing an active slot), and a deterministic or timestamp-based name
// could collide and let one backup silently overwrite another. The original's
// basename is kept in the name for traceability.
func backupExisting(srcPath, backupDir, key string) (string, error) {
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", fmt.Errorf("creating backup dir: %w", err)
	}
	f, err := os.CreateTemp(backupDir, "php-"+key+"-*-"+filepath.Base(srcPath))
	if err != nil {
		return "", fmt.Errorf("reserving backup path for %s: %w", srcPath, err)
	}
	dst := f.Name()
	f.Close()

	// Move the original into the reserved path (os.Rename atomically replaces the
	// empty placeholder on both Unix and Windows).
	if err := os.Rename(srcPath, dst); err == nil {
		return dst, nil
	}
	// Cross-device or other rename failure: copy into the reserved path, then remove
	// the original. copyNode preserves a symlink as a symlink (matching rename).
	if err := copyNode(srcPath, dst, 0o755); err != nil {
		os.Remove(dst)
		return "", fmt.Errorf("backing up %s: %w", srcPath, err)
	}
	if err := os.Remove(srcPath); err != nil {
		os.Remove(dst)
		return "", fmt.Errorf("removing original %s after backup: %w", srcPath, err)
	}
	return dst, nil
}

// restoreBackup moves a backup back to its original path, preserving a symlink as
// a symlink whether it moves (rename) or falls back to copy across filesystems.
// The restored file keeps the backup's own permissions (which mirror the original
// file's mode — see backupExisting/saveIniBackup), so a rename and the copy
// fallback yield the same mode.
func restoreBackup(backupPath, originalPath string) error {
	if err := os.MkdirAll(filepath.Dir(originalPath), 0o755); err != nil {
		return err
	}
	if err := os.Rename(backupPath, originalPath); err == nil {
		return nil
	}
	fi, err := os.Lstat(backupPath)
	if err != nil {
		return err
	}
	if err := copyNode(backupPath, originalPath, fi.Mode().Perm()); err != nil {
		return err
	}
	return os.Remove(backupPath)
}

// copyBackup restores a backup to its original path by COPYING it (the backup file
// is left in place, so the operation is retryable until a caller deletes it),
// preserving the backup's own permissions (which mirror the original file's mode —
// see saveIniBackup). A symlink is recreated as a symlink.
func copyBackup(backupPath, originalPath string) error {
	fi, err := os.Lstat(backupPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(originalPath), 0o755); err != nil {
		return err
	}
	return copyNode(backupPath, originalPath, fi.Mode().Perm())
}

// copyNode copies src to dst for the cross-filesystem move fallbacks. A symlink is
// recreated as a symlink (its target is not dereferenced); a regular file has its
// contents copied with perm.
func copyNode(src, dst string, perm os.FileMode) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", filepath.Dir(dst), err)
		}
		_ = os.Remove(dst) // os.Symlink fails if dst already exists
		return os.Symlink(target, dst)
	}
	return copyFile(src, dst, perm)
}
