package installer

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/php-debugger/installer/internal/platform"
	"github.com/php-debugger/installer/internal/release"
)

// makeZip builds a zip in memory from name -> content entries.
func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bundle.zip")
	if err := os.WriteFile(p, makeZip(t, files), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestIsBundleAsset(t *testing.T) {
	for name, want := range map[string]bool{
		"php-php8.5-nts-windows-x64.zip": true,
		"php-php8.5-nts-windows-x64.ZIP": true,
		"php-php8.5-nts-windows-x64.exe": false,
		"php-php8.5-nts-linux-x86_64":    false,
	} {
		if got := isBundleAsset(name); got != want {
			t.Errorf("isBundleAsset(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestExtractBundle(t *testing.T) {
	zipPath := writeZip(t, map[string]string{
		"php.exe":           "EXE",
		"php8.dll":          "DLL",
		"ext/php_curl.dll":  "CURL",
		`ext\php_zip.dll`:   "ZIP", // backslash separator, as some Windows tools write
		"dev/php8.lib":      "LIB",
		"php.ini-developme": "INI",
	})
	dst := t.TempDir()
	if err := extractBundle(zipPath, dst); err != nil {
		t.Fatalf("extractBundle: %v", err)
	}
	for rel, want := range map[string]string{
		"php.exe":                            "EXE",
		"php8.dll":                           "DLL",
		filepath.Join("ext", "php_curl.dll"): "CURL",
		filepath.Join("ext", "php_zip.dll"):  "ZIP",
		filepath.Join("dev", "php8.lib"):     "LIB",
	} {
		got, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q (err %v), want %q", rel, got, err, want)
		}
	}
}

func TestExtractBundleRejectsUnsafePaths(t *testing.T) {
	for _, name := range []string{"../evil.dll", "ext/../../evil.dll", `..\evil.dll`, "/abs/evil.dll"} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dst := filepath.Join(parent, "out")
			zipPath := writeZip(t, map[string]string{"php.exe": "EXE", name: "EVIL"})
			if err := extractBundle(zipPath, dst); err == nil {
				t.Fatalf("extractBundle accepted %q", name)
			}
			if _, err := os.Stat(filepath.Join(parent, "evil.dll")); err == nil {
				t.Errorf("%q was written outside the destination", name)
			}
		})
	}
}

func TestInstallBundleKeepsOtherFilesAndRollsBack(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "php.exe"), "NEW-EXE")
	writeFile(t, filepath.Join(src, "ext", "php_curl.dll"), "NEW-CURL")

	dst := t.TempDir()
	writeFile(t, filepath.Join(dst, "php.exe"), "OLD-EXE")
	writeFile(t, filepath.Join(dst, "php.ini"), "memory_limit=1G\n") // written by an earlier install

	rb := &rollback{}
	if err := installBundle(src, dst, rb); err != nil {
		t.Fatalf("installBundle: %v", err)
	}
	check := func(rel, want string) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q (err %v), want %q", rel, got, err, want)
		}
	}
	check("php.exe", "NEW-EXE")
	check(filepath.Join("ext", "php_curl.dll"), "NEW-CURL")
	check("php.ini", "memory_limit=1G\n")

	rb.run()
	check("php.exe", "OLD-EXE")
	check("php.ini", "memory_limit=1G\n")
	if _, err := os.Stat(filepath.Join(dst, "ext", "php_curl.dll")); !os.IsNotExist(err) {
		t.Errorf("rollback should remove files the bundle added (err %v)", err)
	}
}

// newFakeBundleServer serves a release whose only interpreter is a Windows zip
// bundle, counting downloads so tests can assert nothing was fetched.
func newFakeBundleServer(t *testing.T, bundle []byte, downloads *atomic.Int32) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			fmt.Fprintf(w, `{"tag_name":"9.9.9","assets":[
				{"name":"php-php8.3-nts-windows-x64.zip","browser_download_url":%q,"size":%d}
			]}`, srv.URL+"/dl/bundle", len(bundle))
		case r.URL.Path == "/dl/bundle":
			downloads.Add(1)
			w.Write(bundle)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func windowsUserEnv(home string) platform.Env {
	return platform.Env{
		OS:     platform.Windows,
		Arch:   platform.X8664,
		Home:   home,
		Getenv: func(string) string { return "" },
	}
}

// A Windows interpreter is a zip bundle: the whole tree must be installed next
// to php.exe, and the existing php's config copied beside it (where php.exe
// reads it), not into the compiled-in config path, which on Windows is
// C:\Windows.
func TestInstallInterpreterWindowsBundle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()

	// The bundled php reports a compiled-in config path standing in for C:\Windows.
	sharedWinDir := filepath.Join(t.TempDir(), "Windows")
	bundle := makeZip(t, map[string]string{
		"php.exe":          fakePHP("8.3.7", true, sharedWinDir, ""),
		"php8.dll":         "PHP-CORE",
		"libcrypto.dll":    "CRYPTO",
		"ext/php_curl.dll": "CURL",
	})
	var downloads atomic.Int32
	srv := newFakeBundleServer(t, bundle, &downloads)

	// A pre-existing php on PATH whose php.ini loads xdebug.
	existBin := filepath.Join(t.TempDir(), "bin")
	existIni := filepath.Join(t.TempDir(), "php.ini")
	writeExec(t, filepath.Join(existBin, "php"), existingPHPScript("8.3.1", existIni, "", ""))
	if err := os.WriteFile(existIni, []byte("zend_extension=xdebug\nmemory_limit=256M\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", existBin)

	client := release.NewClient()
	client.BaseURL = srv.URL
	env := windowsUserEnv(home)
	var out bytes.Buffer
	err := InstallInterpreter(context.Background(), Options{
		Scope: platform.User, AssumeYes: true, Out: &out, Client: client, Env: &env,
	})
	if err != nil {
		t.Fatalf("InstallInterpreter: %v\n--- output ---\n%s", err, out.String())
	}

	binDir := filepath.Join(home, "AppData", "Local", "php-debugger", "8.3", "bin")
	for rel, want := range map[string]string{
		"php8.dll":                           "PHP-CORE",
		"libcrypto.dll":                      "CRYPTO",
		filepath.Join("ext", "php_curl.dll"): "CURL",
	} {
		got, err := os.ReadFile(filepath.Join(binDir, rel))
		if err != nil || string(got) != want {
			t.Errorf("bundle file %s = %q (err %v), want %q", rel, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(binDir, "php.exe")); err != nil {
		t.Errorf("php.exe not installed: %v", err)
	}

	ini, err := os.ReadFile(filepath.Join(binDir, "php.ini"))
	if err != nil {
		t.Fatalf("config not copied next to php.exe: %v\n%s", err, out.String())
	}
	if strings.Contains(string(ini), "xdebug") || !strings.Contains(string(ini), "memory_limit=256M") {
		t.Errorf("copied php.ini not sanitized as expected:\n%s", ini)
	}
	if _, err := os.Stat(filepath.Join(sharedWinDir, "php.ini")); !os.IsNotExist(err) {
		t.Errorf("config must not be written to the compiled-in (Windows) dir (err %v)", err)
	}
}

func TestInstallInterpreterBundleMissingBinary(t *testing.T) {
	isolatePATH(t)
	bundle := makeZip(t, map[string]string{"php8.dll": "PHP-CORE"})
	var downloads atomic.Int32
	srv := newFakeBundleServer(t, bundle, &downloads)
	client := release.NewClient()
	client.BaseURL = srv.URL
	env := windowsUserEnv(t.TempDir())

	err := InstallInterpreter(context.Background(), Options{
		Scope: platform.User, Out: &bytes.Buffer{}, Client: client, Env: &env,
	})
	if err == nil || !strings.Contains(err.Error(), "has no php.exe") {
		t.Fatalf("expected a missing php.exe error, got %v", err)
	}
}

// readOnlyRoot makes the user-scope install root of env unwritable by creating
// its parent directory read-only.
func readOnlyRoot(t *testing.T, parent string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions are not POSIX modes on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write to read-only directories")
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o755) })
}

// Without write access to the install root (e.g. the default Program Files scope
// without elevation), the install must stop before downloading or changing
// anything, and say how to proceed.
func TestInstallInterpreterUnwritableRootFailsFast(t *testing.T) {
	isolatePATH(t)
	home := t.TempDir()
	readOnlyRoot(t, filepath.Join(home, "AppData", "Local"))

	bundle := makeZip(t, map[string]string{"php.exe": "unused"})
	var downloads atomic.Int32
	srv := newFakeBundleServer(t, bundle, &downloads)
	client := release.NewClient()
	client.BaseURL = srv.URL
	env := windowsUserEnv(home)

	err := InstallInterpreter(context.Background(), Options{
		Scope: platform.User, Out: &bytes.Buffer{}, Client: client, Env: &env,
	})
	if err == nil || !strings.Contains(err.Error(), "--user") || !strings.Contains(err.Error(), "Run as administrator") {
		t.Fatalf("expected an unwritable-root error with guidance, got %v", err)
	}
	if n := downloads.Load(); n != 0 {
		t.Errorf("downloaded %d asset(s) before failing", n)
	}
}

func TestInstallExtensionUnwritableRootFailsFast(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	readOnlyRoot(t, filepath.Join(home, ".local", "share"))

	extDir := t.TempDir()
	iniDir := t.TempDir()
	scanDir := filepath.Join(iniDir, "conf.d")
	loadedFile := filepath.Join(iniDir, "php.ini")
	const iniContent = "zend_extension=xdebug.so\n"
	if err := os.WriteFile(loadedFile, []byte(iniContent), 0o644); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(t.TempDir(), "bin")
	writeExec(t, filepath.Join(binDir, "php"),
		fakeExistingPHPForExt("8.3.7", extDir, loadedFile, scanDir, true))
	t.Setenv("PATH", binDir)

	srv := newFakeReleaseServer(t, "unused")
	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)

	err := InstallExtension(context.Background(), Options{
		Scope: platform.User, AssumeYes: true, Out: &bytes.Buffer{}, Client: client, Env: &env,
	})
	if err == nil || !strings.Contains(err.Error(), "--user") || !strings.Contains(err.Error(), "sudo") {
		t.Fatalf("expected an unwritable-root error with guidance, got %v", err)
	}
	if entries, _ := os.ReadDir(extDir); len(entries) != 0 {
		t.Errorf("extension_dir was modified before failing: %v", entries)
	}
	if b, _ := os.ReadFile(loadedFile); string(b) != iniContent {
		t.Errorf("php.ini was modified before failing:\n%s", b)
	}
}
