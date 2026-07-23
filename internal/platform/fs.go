package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// IsDirWritable reports whether the installer could create files in dir. If dir
// does not exist yet, it checks whether the nearest existing ancestor is
// writable (so dir could be created). It performs filesystem access.
func IsDirWritable(dir string) bool {
	if dir == "" {
		return false
	}
	d := dir
	for {
		info, err := os.Stat(d)
		if err == nil {
			return info.IsDir() && probeWritable(d)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return false // reached the root without finding an existing dir
		}
		d = parent
	}
}

// probeWritable checks writability by creating and removing a temp file.
func probeWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".php-debugger-write-test-")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// SelectBinDir returns the first writable directory from candidates. It is used
// to choose where the active `php` symlink is placed.
func SelectBinDir(candidates []string) (string, error) {
	for _, c := range candidates {
		if IsDirWritable(c) {
			return c, nil
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no bin directory candidates")
	}
	return "", fmt.Errorf("no writable bin directory (tried: %s)", strings.Join(candidates, ", "))
}

// IsOnPATH reports whether dir appears in the given PATH-style string for the
// target OS. Comparison uses ";" on Windows (case-insensitive) and ":" elsewhere
// (case-sensitive). It is pure — it does not read the process environment.
func IsOnPATH(osID OS, dir, pathEnv string) bool {
	if dir == "" {
		return false
	}
	sep := ":"
	if osID == Windows {
		sep = ";"
	}
	want := normalizePathEntry(osID, dir)
	for _, entry := range strings.Split(pathEnv, sep) {
		if entry == "" {
			continue
		}
		if normalizePathEntry(osID, entry) == want {
			return true
		}
	}
	return false
}

// normalizePathEntry canonicalizes a PATH entry for comparison without relying
// on the host's path separator (so Windows entries compare correctly on Unix
// test hosts).
func normalizePathEntry(osID OS, p string) string {
	p = strings.TrimSpace(p)
	if osID == Windows {
		p = strings.ReplaceAll(p, "\\", "/")
	}
	p = strings.TrimRight(p, "/")
	if osID == Windows {
		p = strings.ToLower(p)
	}
	return p
}
