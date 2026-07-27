package installer

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// copyNode backs the cross-filesystem move fallback. A symlink must survive as a
// symlink (target not dereferenced), matching the rename path it stands in for.
func TestCopyNodePreservesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	realTarget := filepath.Join(dir, "real-php")
	if err := os.WriteFile(realTarget, []byte("BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "php") // symlink -> real-php
	if err := os.Symlink(realTarget, src); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "backup", "php")
	if err := copyNode(src, dst, 0o755); err != nil {
		t.Fatalf("copyNode: %v", err)
	}

	fi, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("lstat dst: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("dst should be a symlink, not a dereferenced regular file")
	}
	got, err := os.Readlink(dst)
	if err != nil || got != realTarget {
		t.Errorf("symlink target = %q (err %v), want %q", got, err, realTarget)
	}
}

func TestCopyNodeCopiesRegularFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "bin")
	if err := os.WriteFile(src, []byte("CONTENTS"), 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "sub", "bin")
	if err := copyNode(src, dst, 0o755); err != nil {
		t.Fatalf("copyNode: %v", err)
	}
	if isLink, _ := isSymlinkNode(dst); isLink {
		t.Error("regular file should not become a symlink")
	}
	if b, err := os.ReadFile(dst); err != nil || string(b) != "CONTENTS" {
		t.Errorf("dst contents = %q (err %v), want CONTENTS", b, err)
	}
}

// Two files backed up under the same key in one install (e.g. a Windows php.exe
// and php.cmd) must get distinct backup paths — even sharing a basename — so one
// never overwrites the other.
func TestBackupExistingUniquePaths(t *testing.T) {
	dir := t.TempDir()
	backups := filepath.Join(dir, "backups")

	// Two distinct sources sharing a basename ("php"), backed up under one key.
	srcA := filepath.Join(dir, "a", "php")
	srcB := filepath.Join(dir, "b", "php")
	if err := os.MkdirAll(filepath.Dir(srcA), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(srcB), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcA, []byte("AAA"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcB, []byte("BBB"), 0o755); err != nil {
		t.Fatal(err)
	}

	pa, err := backupExisting(srcA, backups, "8.3")
	if err != nil {
		t.Fatal(err)
	}
	pb, err := backupExisting(srcB, backups, "8.3")
	if err != nil {
		t.Fatal(err)
	}

	if pa == pb {
		t.Fatalf("backup paths collided: %q", pa)
	}
	if data, _ := os.ReadFile(pa); string(data) != "AAA" {
		t.Errorf("backup A content = %q, want AAA", data)
	}
	if data, _ := os.ReadFile(pb); string(data) != "BBB" {
		t.Errorf("backup B content = %q, want BBB", data)
	}
	// Both sources were moved into their backups.
	if _, err := os.Stat(srcA); !os.IsNotExist(err) {
		t.Error("srcA should have been moved")
	}
	if _, err := os.Stat(srcB); !os.IsNotExist(err) {
		t.Error("srcB should have been moved")
	}
}

func isSymlinkNode(path string) (bool, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	return fi.Mode()&os.ModeSymlink != 0, nil
}
