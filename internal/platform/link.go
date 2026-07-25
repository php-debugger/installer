package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ActiveKind describes how an active interpreter entry is materialized.
type ActiveKind int

const (
	// KindSymlink is a real symbolic link (Unix always; Windows when permitted).
	KindSymlink ActiveKind = iota
	// KindShim is a generated .cmd wrapper used on Windows when a symlink cannot
	// be created (symlinks there need Developer Mode or admin rights).
	KindShim
)

func (k ActiveKind) String() string {
	if k == KindShim {
		return "shim"
	}
	return "symlink"
}

// Activate makes the command `name` in binDir launch `target`, atomically
// replacing any existing entry. On Unix it creates a symlink; on Windows it
// tries a symlink first and falls back to a .cmd shim. It returns the path it
// created and how it was materialized.
func Activate(binDir, name, target string) (string, ActiveKind, error) {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", 0, fmt.Errorf("creating bin dir %s: %w", binDir, err)
	}
	if runtime.GOOS == "windows" {
		return activateWindows(binDir, name, target)
	}
	linkPath := filepath.Join(binDir, name)
	if err := replaceSymlink(target, linkPath); err != nil {
		return "", 0, err
	}
	return linkPath, KindSymlink, nil
}

func activateWindows(binDir, name, target string) (string, ActiveKind, error) {
	exePath := filepath.Join(binDir, name+".exe")
	cmdPath := filepath.Join(binDir, name+".cmd")

	// Prefer a real symlink; if it works, clear any stale shim.
	if err := replaceSymlink(target, exePath); err == nil {
		_ = os.Remove(cmdPath)
		return exePath, KindSymlink, nil
	}
	// Symlink not permitted: write a .cmd shim and clear any stale symlink.
	if err := writeShim(cmdPath, target); err != nil {
		return "", 0, err
	}
	_ = os.Remove(exePath)
	return cmdPath, KindShim, nil
}

// ReadActive returns the target the command `name` in binDir currently launches
// and how it is materialized. ok is false when there is no active entry.
func ReadActive(binDir, name string) (target string, kind ActiveKind, ok bool) {
	if runtime.GOOS == "windows" {
		if t, err := os.Readlink(filepath.Join(binDir, name+".exe")); err == nil {
			return t, KindSymlink, true
		}
		if t, err := readShimTarget(filepath.Join(binDir, name+".cmd")); err == nil {
			return t, KindShim, true
		}
		return "", 0, false
	}
	if t, err := os.Readlink(filepath.Join(binDir, name)); err == nil {
		return t, KindSymlink, true
	}
	return "", 0, false
}

// RemoveActive removes the active entry for `name` in binDir. It is idempotent:
// a missing entry is not an error.
func RemoveActive(binDir, name string) error {
	var paths []string
	if runtime.GOOS == "windows" {
		paths = []string{filepath.Join(binDir, name+".exe"), filepath.Join(binDir, name+".cmd")}
	} else {
		paths = []string{filepath.Join(binDir, name)}
	}
	var firstErr error
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// IsSymlink reports whether path is a symbolic link.
func IsSymlink(path string) (bool, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	return fi.Mode()&os.ModeSymlink != 0, nil
}

// replaceSymlink atomically creates (or replaces) a symlink at linkPath pointing
// to target, by creating a uniquely-named temp symlink and renaming it into
// place. The rename is atomic on Unix, so `php` is never momentarily absent.
func replaceSymlink(target, linkPath string) error {
	tmp := fmt.Sprintf("%s.tmp-%d", linkPath, time.Now().UnixNano())
	if err := os.Symlink(target, tmp); err != nil {
		return fmt.Errorf("creating symlink: %w", err)
	}
	if err := os.Rename(tmp, linkPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replacing %s: %w", linkPath, err)
	}
	return nil
}

func writeShim(shimPath, target string) error {
	if err := os.WriteFile(shimPath, []byte(windowsShimContent(target)), 0o755); err != nil {
		return fmt.Errorf("writing shim %s: %w", shimPath, err)
	}
	return nil
}

// windowsShimContent builds a .cmd wrapper that execs target, forwarding all
// arguments (%*). Uses CRLF line endings as is conventional for .cmd files.
func windowsShimContent(target string) string {
	return "@echo off\r\n\"" + target + "\" %*\r\n"
}

func readShimTarget(shimPath string) (string, error) {
	data, err := os.ReadFile(shimPath)
	if err != nil {
		return "", err
	}
	return parseShimTarget(string(data))
}

// parseShimTarget extracts the quoted target path from a generated shim.
func parseShimTarget(content string) (string, error) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "@") {
			continue
		}
		if strings.HasPrefix(line, "\"") {
			if end := strings.IndexByte(line[1:], '"'); end >= 0 {
				return line[1 : 1+end], nil
			}
		}
	}
	return "", fmt.Errorf("no target found in shim")
}
