package installer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/php-debugger/installer/internal/manifest"
	"github.com/php-debugger/installer/internal/platform"
	"github.com/php-debugger/installer/internal/release"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func baseNames(paths []string) []string {
	var out []string
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	sort.Strings(out)
	return out
}

func TestCopyExtensionFiles(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "ext") // does not exist yet

	writeFile(t, filepath.Join(src, "redis.so"), "REDIS")
	writeFile(t, filepath.Join(src, "php_intl.dll"), "INTL")
	writeFile(t, filepath.Join(src, "xdebug.so"), "XDEBUG")
	writeFile(t, filepath.Join(src, "php-debugger-php8.3-nts.so"), "DBG")
	writeFile(t, filepath.Join(src, "README.txt"), "not an extension")
	if err := os.MkdirAll(filepath.Join(src, "sub.so"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Already present at the destination: must not be overwritten.
	writeFile(t, filepath.Join(src, "opcache.so"), "FOREIGN-OPCACHE")
	writeFile(t, filepath.Join(dst, "opcache.so"), "OWN-OPCACHE")
	// A symlinked extension (as Homebrew does) is copied by content.
	if runtime.GOOS != "windows" {
		real := filepath.Join(t.TempDir(), "apcu.so")
		writeFile(t, real, "APCU")
		if err := os.Symlink(real, filepath.Join(src, "apcu.so")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(src, "missing"), filepath.Join(src, "dangling.so")); err != nil {
			t.Fatal(err)
		}
	}

	rb := &rollback{}
	copied, err := copyExtensionFiles(src, dst, rb, Options{})
	if err != nil {
		t.Fatalf("copyExtensionFiles: %v", err)
	}

	want := []string{"php_intl.dll", "redis.so"}
	if runtime.GOOS != "windows" {
		want = []string{"apcu.so", "php_intl.dll", "redis.so"}
	}
	if got := baseNames(copied); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("copied %v, want %v", got, want)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "redis.so")); string(b) != "REDIS" {
		t.Errorf("redis.so content = %q", b)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Lstat(filepath.Join(dst, "apcu.so"))
		if err != nil || !fi.Mode().IsRegular() {
			t.Errorf("symlinked extension should be copied as a regular file: %v %v", fi, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "opcache.so")); string(b) != "OWN-OPCACHE" {
		t.Errorf("existing destination file was overwritten: %q", b)
	}
	for _, skipped := range []string{"xdebug.so", "php-debugger-php8.3-nts.so", "README.txt", "dangling.so"} {
		if _, err := os.Lstat(filepath.Join(dst, skipped)); err == nil {
			t.Errorf("%s should not have been copied", skipped)
		}
	}

	// Rollback removes exactly the copies, leaving the pre-existing file.
	rb.run()
	for _, f := range copied {
		if _, err := os.Lstat(f); err == nil {
			t.Errorf("rollback left %s", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "opcache.so")); err != nil {
		t.Errorf("rollback removed a pre-existing file: %v", err)
	}
}

func TestCopyExtensionFilesNoOp(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "redis.so"), "REDIS")
	rb := &rollback{}
	for _, tc := range []struct{ name, src, dst string }{
		{"same dir", dir, dir},
		{"missing source", filepath.Join(dir, "nope"), t.TempDir()},
		{"empty source", "", t.TempDir()},
		{"empty destination", dir, ""},
	} {
		copied, err := copyExtensionFiles(tc.src, tc.dst, rb, Options{})
		if err != nil || len(copied) != 0 {
			t.Errorf("%s: copied %v, err %v; want no-op", tc.name, copied, err)
		}
	}
}

func TestMergePaths(t *testing.T) {
	got := mergePaths([]string{"a", "b"}, []string{"b", "c", "c"})
	if strings.Join(got, ",") != "a,b,c" {
		t.Errorf("mergePaths = %v", got)
	}
}

// withExtDir makes a fake php script report extDir as its extension_dir.
func withExtDir(script, extDir string) string {
	return strings.ReplaceAll(script, "extension_dir=/fake/ext", "extension_dir="+extDir)
}

// Replacing a same-ABI php copies its extension binaries into the new
// interpreter's extension_dir (so the kept loaders resolve), records them, and
// uninstall removes them again.
func TestInstallInterpreterSameABICopiesExtensions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()

	newCfg := filepath.Join(t.TempDir(), "newcfg")
	newExt := filepath.Join(t.TempDir(), "lib", "extensions", "no-debug-non-zts-20230831")
	srv := newFakeReleaseServer(t, withExtDir(fakePHP("8.3.12", true, newCfg, filepath.Join(newCfg, "conf.d")), newExt))

	existBin := filepath.Join(t.TempDir(), "bin")
	existIni := t.TempDir()
	existExt := t.TempDir()
	writeExec(t, filepath.Join(existBin, "php"), withExtDir(existingPHPScript("8.3.4",
		filepath.Join(existIni, "php.ini"), filepath.Join(existIni, "conf.d"), ""), existExt))
	writeFile(t, filepath.Join(existIni, "php.ini"), "zend_extension=xdebug\nextension=redis\n")
	writeFile(t, filepath.Join(existExt, "redis.so"), "REDIS")
	writeFile(t, filepath.Join(existExt, "xdebug.so"), "XDEBUG")
	t.Setenv("PATH", existBin)

	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)
	opts := Options{Scope: platform.User, AssumeYes: true, Client: client, Env: &env}

	var out bytes.Buffer
	opts.Out = &out
	if err := InstallInterpreter(context.Background(), opts); err != nil {
		t.Fatalf("InstallInterpreter: %v\n--- output ---\n%s", err, out.String())
	}

	if b, err := os.ReadFile(filepath.Join(newExt, "redis.so")); err != nil || string(b) != "REDIS" {
		t.Errorf("redis.so not copied to the new extension_dir: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(newExt, "xdebug.so")); err == nil {
		t.Error("xdebug.so must not be copied")
	}
	root := filepath.Join(home, ".local", "share", "php-debugger")
	m, err := manifest.Load(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	it, _ := m.Interpreter("8.3")
	if len(it.ExtensionFiles) != 1 || it.ExtensionFiles[0] != filepath.Join(newExt, "redis.so") {
		t.Errorf("ExtensionFiles = %v", it.ExtensionFiles)
	}

	// Uninstall removes the copied binary and prunes the now-empty dir; the
	// original php's extension stays untouched.
	if err := Uninstall(context.Background(), opts, "", false); err != nil {
		t.Fatalf("Uninstall: %v\n--- output ---\n%s", err, out.String())
	}
	if _, err := os.Stat(newExt); !os.IsNotExist(err) {
		t.Errorf("copied extension dir should be removed on uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(existExt, "redis.so")); err != nil {
		t.Errorf("original extension must be left alone: %v", err)
	}
}

// A different-ABI replacement comments the loaders out, so nothing is copied.
func TestInstallInterpreterDifferentABISkipsExtensions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()

	newCfg := filepath.Join(t.TempDir(), "newcfg")
	newExt := filepath.Join(t.TempDir(), "newext")
	srv := newFakeReleaseServer(t, withExtDir(fakePHP("8.3.12", true, newCfg, filepath.Join(newCfg, "conf.d")), newExt))

	existBin := filepath.Join(t.TempDir(), "bin")
	existIni := t.TempDir()
	existExt := t.TempDir()
	writeExec(t, filepath.Join(existBin, "php"), withExtDir(existingPHPScript("8.2.9",
		filepath.Join(existIni, "php.ini"), filepath.Join(existIni, "conf.d"), ""), existExt))
	writeFile(t, filepath.Join(existIni, "php.ini"), "extension=redis\n")
	writeFile(t, filepath.Join(existExt, "redis.so"), "REDIS")
	t.Setenv("PATH", existBin)

	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)
	var out bytes.Buffer
	if err := InstallInterpreter(context.Background(), Options{
		Scope: platform.User, AssumeYes: true, Out: &out, Client: client, Env: &env,
	}); err != nil {
		t.Fatalf("InstallInterpreter: %v\n--- output ---\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(newExt, "redis.so")); err == nil {
		t.Error("different ABI: extensions must not be copied")
	}
}
