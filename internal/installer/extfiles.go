package installer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/php-debugger/installer/internal/php"
)

// sameABI reports whether extensions built for existing can load in target: same
// PHP series and same thread-safety. When true, the existing php's extension
// loaders are kept active in the copied config and its extension binaries are
// copied over (see copyExtensionFiles).
func sameABI(existing, target *php.Info) bool {
	return existing.Series == target.Series && existing.ZTS == target.ZTS
}

// copyExtensions copies the existing php's extension binaries into the new
// interpreter's extension_dir. It must run after the config was copied: the new
// interpreter (already placed at binTarget) is re-queried so its effective
// extension_dir reflects the copied ini — if that sets extension_dir explicitly
// (e.g. to the old php's dir), the loaders already resolve and nothing is copied.
// Relative dirs (Windows' "ext") are resolved against each php's own binary.
func copyExtensions(ctx context.Context, existing, target *php.Info, binTarget string, rb *rollback, opts Options) ([]string, error) {
	targetDir := target.ExtensionDir
	if q, err := php.Query(ctx, binTarget); err == nil && q.ExtensionDir != "" {
		targetDir = q.ExtensionDir
	}
	if existing.ExtensionDir == "" || targetDir == "" {
		return nil, nil
	}
	return copyExtensionFiles(
		resolveExtensionDir(existing.ExtensionDir, existing.Path),
		resolveExtensionDir(targetDir, binTarget),
		rb, opts)
}

// copyExtensionFiles copies the shared extension binaries (.so / .dll) from the
// replaced php's extension_dir into the new interpreter's, so the loaders kept in
// the copied config (e.g. `extension=redis`, resolved against extension_dir) find
// their files. It is only meaningful for a same-ABI replacement; otherwise the
// loaders are commented out and the binaries could not load anyway.
//
// xdebug and the standalone php-debugger extension are skipped (their loaders are
// always stripped). A file already present at the destination is never
// overwritten: it may belong to the new interpreter or to the user. Symlinks in the
// source (e.g. Homebrew's opcache.so) are followed and their target copied. It
// registers removal of each copy on rb and returns the files written, which
// uninstall deletes.
func copyExtensionFiles(srcDir, dstDir string, rb *rollback, opts Options) ([]string, error) {
	if srcDir == "" || dstDir == "" || sameFile(srcDir, dstDir) {
		return nil, nil
	}
	entries, err := os.ReadDir(srcDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading extension dir %s: %w", srcDir, err)
	}

	var copied []string
	for _, e := range entries {
		name := e.Name()
		if !isExtensionBinary(name) || isDebuggerBinary(name) {
			continue
		}
		src := filepath.Join(srcDir, name)
		fi, err := os.Stat(src) // follows symlinks
		if err != nil || !fi.Mode().IsRegular() {
			continue // dangling symlink or not a file
		}
		dst := filepath.Join(dstDir, name)
		if _, err := os.Lstat(dst); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return copied, fmt.Errorf("inspecting %s: %w", dst, err)
		}
		if err := copyFile(src, dst, fi.Mode().Perm()); err != nil {
			_ = removeIfExists(dst)
			return copied, fmt.Errorf("copying extension %s to %s: %w", src, dstDir, err)
		}
		d := dst
		rb.add(func() error { return removeIfExists(d) })
		copied = append(copied, dst)
	}
	if len(copied) > 0 {
		opts.logf("  copied %d extension file(s) from %s to %s", len(copied), srcDir, dstDir)
	}
	return copied, nil
}

// isExtensionBinary reports whether name looks like a PHP shared extension.
func isExtensionBinary(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".so" || ext == ".dll"
}

// isDebuggerBinary reports whether name is an xdebug or php-debugger extension
// binary. Both are superseded by the interpreter's built-in debugger, and their
// loaders are stripped from the copied config, so they are never copied.
func isDebuggerBinary(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "xdebug") || strings.Contains(n, "php-debugger") ||
		strings.Contains(n, "php_debugger")
}

// mergePaths returns fresh plus any path from prior not already in fresh.
func mergePaths(fresh, prior []string) []string {
	seen := make(map[string]bool, len(fresh))
	for _, p := range fresh {
		seen[p] = true
	}
	merged := append([]string(nil), fresh...)
	for _, p := range prior {
		if !seen[p] {
			seen[p] = true
			merged = append(merged, p)
		}
	}
	return merged
}
