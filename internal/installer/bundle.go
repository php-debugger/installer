package installer

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// isBundleAsset reports whether an interpreter asset is a zip bundle rather than
// a single self-contained binary. Windows interpreters are bundles: php.exe is
// only a launcher, and PHP itself lives in php8.dll / php8ts.dll beside it,
// together with the DLLs that one depends on and the ext\ directory.
func isBundleAsset(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".zip")
}

// extractBundle unpacks the zip at zipPath into dst. Entries that would land
// outside dst (absolute paths, "..") and symlink entries are rejected rather
// than skipped, since a bundle containing them is not one we published.
func extractBundle(zipPath, dst string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, f := range zr.File {
		// Zips written on Windows may use backslash separators.
		name := filepath.FromSlash(strings.ReplaceAll(f.Name, `\`, "/"))
		if !filepath.IsLocal(name) {
			return fmt.Errorf("refusing unsafe path %q in bundle", f.Name)
		}
		target := filepath.Join(dst, name)
		mode := f.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		case mode&fs.ModeSymlink != 0:
			return fmt.Errorf("refusing symlink %q in bundle", f.Name)
		}
		if err := extractFile(f, target); err != nil {
			return fmt.Errorf("extracting %s: %w", f.Name, err)
		}
	}
	return nil
}

func extractFile(f *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	in, err := f.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// installBundle copies the unpacked bundle tree at src into dst, overwriting
// files of the same name and leaving other files in dst alone (notably the
// php.ini the installer writes next to the binary, which a reinstall must keep).
// When rb is non-nil, each write is registered on it so a failed install can
// restore the previous contents; pass nil when dst is new and the caller
// removes it wholesale on rollback.
func installBundle(src, dst string, rb *rollback) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if rb != nil {
			if err := registerFileRestore(target, rb, 0o755); err != nil {
				return err
			}
		}
		return installFile(path, target)
	})
}
