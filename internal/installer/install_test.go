package installer

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/php-debugger/installer/internal/manifest"
	"github.com/php-debugger/installer/internal/platform"
	"github.com/php-debugger/installer/internal/release"
)

// isolatePATH points PATH at an empty dir so php.Detect() finds no pre-existing
// interpreter (unless a test adds one). Prevents tests from touching the real
// system php.
func isolatePATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// fakePHP is a /bin/sh script that impersonates a php binary well enough for the
// installer's smoke test, info query and module check. hasDebugger controls
// whether `-m` lists the debugger module. cfgDir/scanDir, when non-empty, are
// reported by `--ini` as the compiled config paths.
func fakePHP(version string, hasDebugger bool, cfgDir, scanDir string) string {
	modules := "[PHP Modules]\\nCore\\ndate\\n"
	if hasDebugger {
		modules += "php_debugger\\n"
	}
	series := version
	if i := strings.LastIndex(version, "."); i >= 0 {
		series = version[:i]
	}
	return fmt.Sprintf(`#!/bin/sh
case "$1" in
  -v) echo "PHP %s (cli) (built: Jan 1 2026) (NTS)" ;;
  -m) printf '%s' ;;
  --ini)
    echo "Configuration File (php.ini) Path: \"%s\""
    echo "Loaded Configuration File:         (none)"
    echo "Scan for additional .ini files in: \"%s\"" ;;
  -r) printf 'version=%s\nseries=%s\nzts=0\nextension_dir=/fake/ext\n' ;;
  *) : ;;
esac
exit 0
`, version, modules, cfgDir, scanDir, version, series)
}

// newFakeReleaseServer serves a latest-release payload pointing at a single
// interpreter asset whose bytes are the given fake php script.
func newFakeReleaseServer(t *testing.T, phpScript string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			fmt.Fprintf(w, `{"tag_name":"9.9.9","assets":[
				{"name":"php-php8.3-nts-linux-x86_64","browser_download_url":%q,"size":%d}
			]}`, srv.URL+"/dl/php", len(phpScript))
		case r.URL.Path == "/dl/php":
			w.Write([]byte(phpScript))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func linuxUserEnv(home string) platform.Env {
	return platform.Env{
		OS:     platform.Linux,
		Arch:   platform.X8664,
		Home:   home,
		Getenv: func(string) string { return "" },
	}
}

func writeExec(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestInstallInterpreterCleanHost(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	isolatePATH(t)
	home := t.TempDir()
	srv := newFakeReleaseServer(t, fakePHP("8.3.7", true, "", ""))

	client := release.NewClient()
	client.BaseURL = srv.URL

	env := linuxUserEnv(home)
	var out bytes.Buffer
	err := InstallInterpreter(context.Background(), Options{
		Scope: platform.User, Out: &out, Client: client, Env: &env,
	})
	if err != nil {
		t.Fatalf("InstallInterpreter: %v\n--- output ---\n%s", err, out.String())
	}

	root := filepath.Join(home, ".local", "share", "php-debugger")
	binTarget := filepath.Join(root, "8.3", "bin", "php")
	if _, err := os.Stat(binTarget); err != nil {
		t.Errorf("interpreter binary not placed at %s: %v", binTarget, err)
	}
	link := filepath.Join(home, ".local", "bin", "php")
	got, err := os.Readlink(link)
	if err != nil || got != binTarget {
		t.Fatalf("active symlink -> %q (err %v), want %q", got, err, binTarget)
	}
	m, err := manifest.Load(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Active() != "8.3" {
		t.Errorf("manifest active = %q, want 8.3", m.Active())
	}
	if _, ok := m.Backup("8.3"); ok {
		t.Error("clean host should not record a backup")
	}
}

func TestInstallInterpreterReplacesExisting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()

	// The new interpreter reports a config path/scan dir we control.
	newCfg := filepath.Join(t.TempDir(), "newcfg")
	newScan := filepath.Join(newCfg, "conf.d")
	srv := newFakeReleaseServer(t, fakePHP("8.3.7", true, newCfg, newScan))

	// A pre-existing php on PATH, with ini files that load xdebug and set a
	// disallowed xdebug.mode.
	existBin := filepath.Join(t.TempDir(), "bin")
	existIni := t.TempDir()
	existConfd := filepath.Join(existIni, "conf.d")
	writeExec(t, filepath.Join(existBin, "php"), existingPHPScript("8.2.9",
		filepath.Join(existIni, "php.ini"), existConfd,
		filepath.Join(existConfd, "20-xdebug.ini")))
	if err := os.WriteFile(filepath.Join(existIni, "php.ini"),
		[]byte("zend_extension=xdebug.so\nextension=mysqli.so\nmemory_limit=128M\nxdebug.mode=develop,debug\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(existConfd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existConfd, "20-xdebug.ini"),
		[]byte("zend_extension=/opt/xdebug.so\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", existBin) // php.Detect() finds our fake existing php

	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)

	var out bytes.Buffer
	err := InstallInterpreter(context.Background(), Options{
		Scope: platform.User, AssumeYes: true, Out: &out, Client: client, Env: &env,
	})
	if err != nil {
		t.Fatalf("InstallInterpreter: %v\n--- output ---\n%s", err, out.String())
	}

	root := filepath.Join(home, ".local", "share", "php-debugger")
	binTarget := filepath.Join(root, "8.3", "bin", "php")

	// The active php is placed at the existing interpreter's location.
	link := filepath.Join(existBin, "php")
	if tgt, err := os.Readlink(link); err != nil || tgt != binTarget {
		t.Errorf("existing php not replaced by symlink: -> %q (err %v), want %q", tgt, err, binTarget)
	}

	// A backup of the original was recorded and exists on disk.
	m, err := manifest.Load(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	b, ok := m.Backup("8.3")
	if !ok {
		t.Fatal("no backup recorded for replaced interpreter")
	}
	if b.OriginalPath != link {
		t.Errorf("backup OriginalPath = %q, want %q", b.OriginalPath, link)
	}
	if _, err := os.Stat(b.BackupPath); err != nil {
		t.Errorf("backup file missing: %v", err)
	}

	// Config was copied to the new interpreter's config path and sanitized.
	mainIni, err := os.ReadFile(filepath.Join(newCfg, "php.ini"))
	if err != nil {
		t.Fatalf("main php.ini not copied: %v", err)
	}
	s := string(mainIni)
	if strings.Contains(s, "xdebug.so") || strings.Contains(strings.ToLower(s), "zend_extension") {
		t.Errorf("xdebug loader not stripped from php.ini:\n%s", s)
	}
	if !strings.Contains(s, "memory_limit=128M") {
		t.Errorf("non-xdebug settings should be preserved:\n%s", s)
	}
	// non-xdebug extension loaders are commented out, not removed or left active.
	if !strings.Contains(s, "; extension=mysqli.so") {
		t.Errorf("mysqli loader should be commented out:\n%s", s)
	}
	if strings.Contains(s, "develop") || !strings.Contains(s, "xdebug.mode=debug") {
		t.Errorf("xdebug.mode not sanitized to debug:\n%s", s)
	}
	if _, err := os.Stat(filepath.Join(newScan, "20-xdebug.ini")); err != nil {
		t.Errorf("additional ini not copied: %v", err)
	}
	it, _ := m.Interpreter("8.3")
	if len(it.ConfigFiles) != 2 {
		t.Errorf("expected 2 config files recorded, got %v", it.ConfigFiles)
	}
}

func TestInstallInterpreterRollbackOnMissingModule(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	isolatePATH(t)
	home := t.TempDir()
	srv := newFakeReleaseServer(t, fakePHP("8.3.7", false, "", "")) // no debugger module

	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)
	var out bytes.Buffer
	err := InstallInterpreter(context.Background(), Options{
		Scope: platform.User, Out: &out, Client: client, Env: &env,
	})
	if err == nil {
		t.Fatalf("expected install to fail when module is missing\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Errorf("error should mention rollback, got: %v", err)
	}

	root := filepath.Join(home, ".local", "share", "php-debugger")
	if _, err := os.Stat(filepath.Join(root, "8.3")); !os.IsNotExist(err) {
		t.Error("version directory should have been rolled back")
	}
	if _, err := os.Lstat(filepath.Join(home, ".local", "bin", "php")); !os.IsNotExist(err) {
		t.Error("active symlink should have been rolled back")
	}
	if _, err := os.Stat(filepath.Join(root, "manifest.json")); !os.IsNotExist(err) {
		t.Error("manifest should not exist after a rolled-back install")
	}
}

func TestInstallRollbackRestoresReplacedInterpreter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	// New interpreter LACKS the debugger module -> post-verify fails -> rollback.
	newCfg := filepath.Join(t.TempDir(), "newcfg")
	srv := newFakeReleaseServer(t, fakePHP("8.3.7", false, newCfg, filepath.Join(newCfg, "conf.d")))

	existBin := filepath.Join(t.TempDir(), "bin")
	existIni := t.TempDir()
	existPhp := filepath.Join(existBin, "php")
	writeExec(t, existPhp, existingPHPScript("8.2.9",
		filepath.Join(existIni, "php.ini"), filepath.Join(existIni, "conf.d"), ""))
	if err := os.WriteFile(filepath.Join(existIni, "php.ini"), []byte("memory_limit=99M\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	origContent, _ := os.ReadFile(existPhp)
	t.Setenv("PATH", existBin)

	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)
	var out bytes.Buffer
	err := InstallInterpreter(context.Background(), Options{
		Scope: platform.User, AssumeYes: true, Out: &out, Client: client, Env: &env,
	})
	if err == nil {
		t.Fatal("expected failure due to missing debugger module")
	}

	// The original interpreter must be restored at its location.
	restored, rerr := os.ReadFile(existPhp)
	if rerr != nil {
		t.Fatalf("original interpreter not restored: %v", rerr)
	}
	if !bytes.Equal(restored, origContent) {
		t.Error("restored interpreter content differs from original")
	}
	// It must be a real file again, not our symlink.
	if isLink, _ := platform.IsSymlink(existPhp); isLink {
		t.Error("restored path should not be a symlink")
	}
}

func TestInstallInterpreterSmokeFailureNoChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	isolatePATH(t)
	home := t.TempDir()
	srv := newFakeReleaseServer(t, "#!/bin/sh\nexit 3\n")

	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)
	var out bytes.Buffer
	err := InstallInterpreter(context.Background(), Options{
		Scope: platform.User, Out: &out, Client: client, Env: &env,
	})
	if err == nil {
		t.Fatal("expected smoke-test failure")
	}
	if !strings.Contains(err.Error(), "--extension-only") {
		t.Errorf("smoke failure should suggest the extension, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "php-debugger", "8.3")); !os.IsNotExist(err) {
		t.Error("no version dir should be created when the smoke test fails")
	}
}

func TestThreadingAndBinaryName(t *testing.T) {
	if threading(true) != "zts" || threading(false) != "nts" {
		t.Error("threading mapping wrong")
	}
	if phpBinaryName(platform.Windows) != "php.exe" {
		t.Error("windows binary should be php.exe")
	}
	if phpBinaryName(platform.Linux) != "php" {
		t.Error("unix binary should be php")
	}
}

// existingPHPScript builds a fake pre-existing php that reports the given ini
// file locations via --ini (so the installer copies them).
func existingPHPScript(version, loadedFile, scanDir, additional string) string {
	add := "(none)"
	if additional != "" {
		add = `\"` + additional + `\"`
	}
	series := version
	if i := strings.LastIndex(version, "."); i >= 0 {
		series = version[:i]
	}
	return fmt.Sprintf(`#!/bin/sh
case "$1" in
  -v) echo "PHP %s (cli) (built: Jan 1 2026) (NTS)" ;;
  -m) printf '[PHP Modules]\nCore\nxdebug\n' ;;
  --ini)
    echo "Loaded Configuration File:         \"%s\""
    echo "Scan for additional .ini files in: \"%s\""
    echo "Additional .ini files parsed:      %s" ;;
  -r) printf 'version=%s\nseries=%s\nzts=0\nextension_dir=/fake/ext\n' ;;
  *) : ;;
esac
exit 0
`, version, loadedFile, scanDir, add, version, series)
}
