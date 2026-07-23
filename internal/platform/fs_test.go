package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsDirWritable(t *testing.T) {
	dir := t.TempDir()

	if !IsDirWritable(dir) {
		t.Errorf("existing temp dir %q should be writable", dir)
	}

	// A not-yet-existing subdirectory whose parent is writable should count as
	// writable (we can create it).
	nested := filepath.Join(dir, "a", "b", "c")
	if !IsDirWritable(nested) {
		t.Errorf("nested path under writable parent %q should be writable", nested)
	}

	// A path whose ancestor is a file (not a directory) is not writable.
	fileParent := filepath.Join(dir, "afile")
	if err := os.WriteFile(fileParent, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if IsDirWritable(filepath.Join(fileParent, "sub")) {
		t.Error("path under a regular file should not be writable")
	}

	if IsDirWritable("") {
		t.Error("empty dir should not be writable")
	}
}

func TestSelectBinDir(t *testing.T) {
	writable := t.TempDir()

	// A non-writable candidate: under a regular file.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	badCandidate := filepath.Join(blocker, "bin")

	got, err := SelectBinDir([]string{badCandidate, writable})
	if err != nil {
		t.Fatalf("SelectBinDir: %v", err)
	}
	if got != writable {
		t.Errorf("SelectBinDir picked %q, want %q", got, writable)
	}

	if _, err := SelectBinDir([]string{badCandidate}); err == nil {
		t.Error("SelectBinDir with only bad candidates: expected error")
	}
	if _, err := SelectBinDir(nil); err == nil {
		t.Error("SelectBinDir with no candidates: expected error")
	}
}

func TestIsOnPATH(t *testing.T) {
	tests := []struct {
		name    string
		osID    OS
		dir     string
		pathEnv string
		want    bool
	}{
		{
			name:    "unix present",
			osID:    Linux,
			dir:     "/home/jane/.local/bin",
			pathEnv: "/usr/bin:/home/jane/.local/bin:/bin",
			want:    true,
		},
		{
			name:    "unix trailing slash normalized",
			osID:    Linux,
			dir:     "/home/jane/.local/bin/",
			pathEnv: "/usr/bin:/home/jane/.local/bin",
			want:    true,
		},
		{
			name:    "unix absent",
			osID:    MacOS,
			dir:     "/opt/homebrew/bin",
			pathEnv: "/usr/bin:/bin",
			want:    false,
		},
		{
			name:    "unix case sensitive",
			osID:    Linux,
			dir:     "/Home/Jane/bin",
			pathEnv: "/home/jane/bin",
			want:    false,
		},
		{
			name:    "windows case-insensitive and slash-agnostic",
			osID:    Windows,
			dir:     `C:\Program Files\php-debugger\bin`,
			pathEnv: `C:\Windows;c:/program files/PHP-DEBUGGER/BIN`,
			want:    true,
		},
		{
			name:    "windows absent",
			osID:    Windows,
			dir:     `C:\a\bin`,
			pathEnv: `C:\b\bin;C:\c\bin`,
			want:    false,
		},
		{
			name:    "empty dir",
			osID:    Linux,
			dir:     "",
			pathEnv: "/usr/bin",
			want:    false,
		},
		{
			name:    "empty path",
			osID:    Linux,
			dir:     "/usr/bin",
			pathEnv: "",
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsOnPATH(tt.osID, tt.dir, tt.pathEnv); got != tt.want {
				t.Errorf("IsOnPATH(%s, %q, %q) = %v, want %v", tt.osID, tt.dir, tt.pathEnv, got, tt.want)
			}
		})
	}
}
