package installer

import (
	"fmt"
	"os"
	"path/filepath"
)

// backupExisting moves the existing interpreter at srcPath into backupDir under a
// unique name, preserving whatever it is (a real binary or a symlink). It falls
// back to copy+remove if the move crosses filesystems. Returns the backup path.
func backupExisting(srcPath, backupDir, key string, nowNanos int64) (string, error) {
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", fmt.Errorf("creating backup dir: %w", err)
	}
	dst := filepath.Join(backupDir, fmt.Sprintf("php-%s-%d", key, nowNanos))

	if err := os.Rename(srcPath, dst); err == nil {
		return dst, nil
	}
	// Cross-device or other rename failure: copy the resolved binary, then remove
	// the original.
	if err := copyFile(srcPath, dst, 0o755); err != nil {
		return "", fmt.Errorf("backing up %s: %w", srcPath, err)
	}
	if err := os.Remove(srcPath); err != nil {
		os.Remove(dst)
		return "", fmt.Errorf("removing original %s after backup: %w", srcPath, err)
	}
	return dst, nil
}

// restoreBackup moves a backup back to its original path.
func restoreBackup(backupPath, originalPath string) error {
	if err := os.MkdirAll(filepath.Dir(originalPath), 0o755); err != nil {
		return err
	}
	if err := os.Rename(backupPath, originalPath); err == nil {
		return nil
	}
	if err := copyFile(backupPath, originalPath, 0o755); err != nil {
		return err
	}
	return os.Remove(backupPath)
}
