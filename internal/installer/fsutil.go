package installer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// installFile copies src to dst (creating parent directories) and marks it
// executable.
func installFile(src, dst string) error {
	return copyFile(src, dst, 0o755)
}

// copyFile copies src to dst with the given permissions, creating parent
// directories as needed.
func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(dst), err)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// Ensure perms even if the file pre-existed with different mode.
	return os.Chmod(dst, perm)
}
