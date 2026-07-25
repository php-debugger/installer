package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeExecutable creates a file at path with the given content, marked
// executable.
func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestActivateSymlinkUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix symlink semantics")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	target := filepath.Join(dir, "8.3", "php")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, target, "#!/bin/sh\necho hi\n")

	path, kind, err := Activate(binDir, "php", target)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if kind != KindSymlink {
		t.Errorf("kind = %v, want symlink", kind)
	}
	if path != filepath.Join(binDir, "php") {
		t.Errorf("path = %q", path)
	}
	if isLink, _ := IsSymlink(path); !isLink {
		t.Error("activated path should be a symlink")
	}
	gotTarget, gotKind, ok := ReadActive(binDir, "php")
	if !ok || gotTarget != target || gotKind != KindSymlink {
		t.Errorf("ReadActive = (%q, %v, %v), want (%q, symlink, true)", gotTarget, gotKind, ok, target)
	}
}

func TestActivateReplacesExisting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix symlink semantics")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	t1 := filepath.Join(dir, "t1")
	t2 := filepath.Join(dir, "t2")
	writeExecutable(t, t1, "#!/bin/sh\necho one\n")
	writeExecutable(t, t2, "#!/bin/sh\necho two\n")

	if _, _, err := Activate(binDir, "php", t1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Activate(binDir, "php", t2); err != nil {
		t.Fatalf("re-activate: %v", err)
	}
	got, _, ok := ReadActive(binDir, "php")
	if !ok || got != t2 {
		t.Errorf("after replace, target = %q, want %q", got, t2)
	}
}

func TestActivateOverRegularFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix symlink semantics")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A pre-existing real file where the active link will go.
	existing := filepath.Join(binDir, "php")
	writeExecutable(t, existing, "#!/bin/sh\necho old\n")

	target := filepath.Join(dir, "php-new")
	writeExecutable(t, target, "#!/bin/sh\necho new\n")

	if _, _, err := Activate(binDir, "php", target); err != nil {
		t.Fatalf("Activate over regular file: %v", err)
	}
	if isLink, _ := IsSymlink(existing); !isLink {
		t.Error("regular file should have been replaced by a symlink")
	}
}

func TestActivatedSymlinkRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a /bin/sh target")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	target := filepath.Join(dir, "php")
	writeExecutable(t, target, "#!/bin/sh\necho ACTIVATED\n")

	path, _, err := Activate(binDir, "php", target)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(path).Output()
	if err != nil {
		t.Fatalf("running activated symlink: %v", err)
	}
	if got := string(out); got != "ACTIVATED\n" {
		t.Errorf("output = %q, want ACTIVATED", got)
	}
}

func TestRemoveActive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix symlink semantics")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	target := filepath.Join(dir, "php")
	writeExecutable(t, target, "#!/bin/sh\n")

	if _, _, err := Activate(binDir, "php", target); err != nil {
		t.Fatal(err)
	}
	if err := RemoveActive(binDir, "php"); err != nil {
		t.Fatalf("RemoveActive: %v", err)
	}
	if _, _, ok := ReadActive(binDir, "php"); ok {
		t.Error("active entry should be gone after RemoveActive")
	}
	// idempotent
	if err := RemoveActive(binDir, "php"); err != nil {
		t.Errorf("RemoveActive on missing entry should be nil, got %v", err)
	}
}

func TestReadActiveMissing(t *testing.T) {
	dir := t.TempDir()
	if _, _, ok := ReadActive(dir, "php"); ok {
		t.Error("ReadActive on empty dir should be ok=false")
	}
}

// --- pure shim tests (run on all platforms) ---

func TestWindowsShimContent(t *testing.T) {
	c := windowsShimContent(`C:\opt\php-debugger\8.3\bin\php.exe`)
	if !strings.Contains(c, `"C:\opt\php-debugger\8.3\bin\php.exe" %*`) {
		t.Errorf("shim content missing quoted target + args:\n%s", c)
	}
	if !strings.Contains(c, "@echo off") {
		t.Errorf("shim content missing @echo off:\n%s", c)
	}
}

func TestParseShimTargetRoundTrip(t *testing.T) {
	target := `C:\Program Files\php-debugger\8.3\bin\php.exe`
	got, err := parseShimTarget(windowsShimContent(target))
	if err != nil {
		t.Fatalf("parseShimTarget: %v", err)
	}
	if got != target {
		t.Errorf("parsed target = %q, want %q", got, target)
	}
}

func TestWriteAndReadShim(t *testing.T) {
	shimPath := filepath.Join(t.TempDir(), "php.cmd")
	target := `C:\opt\php-debugger\8.4-zts\bin\php.exe`
	if err := writeShim(shimPath, target); err != nil {
		t.Fatalf("writeShim: %v", err)
	}
	got, err := readShimTarget(shimPath)
	if err != nil {
		t.Fatalf("readShimTarget: %v", err)
	}
	if got != target {
		t.Errorf("read target = %q, want %q", got, target)
	}
}

func TestParseShimTargetError(t *testing.T) {
	if _, err := parseShimTarget("@echo off\r\nnot a quoted target\r\n"); err == nil {
		t.Error("expected error parsing shim with no quoted target")
	}
}
