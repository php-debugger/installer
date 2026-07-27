package installer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/php-debugger/installer/internal/manifest"
	"github.com/php-debugger/installer/internal/platform"
	"github.com/php-debugger/installer/internal/release"
)

// fakeExistingPHPForExt builds a fake php whose `-m` lists php_debugger only when
// the loader ini exists AND simulateLoad is true — mimicking dynamic loading via
// the enabled extension. It reports the given extension_dir and ini locations.
func fakeExistingPHPForExt(version, extDir, loadedFile, scanDir string, simulateLoad bool) string {
	return fakeExistingPHPForExtArch(version, "x86_64", extDir, loadedFile, scanDir, simulateLoad)
}

// fakeExistingPHPForExtArch is fakeExistingPHPForExt with an explicit machine
// architecture reported by php_uname("m") (used to exercise Rosetta-style hosts
// where the php process arch differs from the host's native arch).
func fakeExistingPHPForExtArch(version, machine, extDir, loadedFile, scanDir string, simulateLoad bool) string {
	series := version
	if i := strings.LastIndex(version, "."); i >= 0 {
		series = version[:i]
	}
	sim := "0"
	if simulateLoad {
		sim = "1"
	}
	loader := filepath.Join(scanDir, "99-php-debugger.ini")
	return fmt.Sprintf(`#!/bin/sh
case "$1" in
  -v) echo "PHP %s (cli) (built: Jan 1 2026) (NTS)" ;;
  -m)
    printf '[PHP Modules]\nCore\ndate\nxdebug\n'
    if [ "%s" = "1" ] && [ -f "%s" ]; then printf 'php_debugger\n'; fi
    ;;
  --ini)
    echo "Loaded Configuration File:         \"%s\""
    echo "Scan for additional .ini files in: \"%s\"" ;;
  -r) printf 'version=%s\nseries=%s\nzts=0\nmachine=%s\nextension_dir=%s\n' ;;
  *) : ;;
esac
exit 0
`, version, sim, loader, loadedFile, scanDir, version, series, machine, extDir)
}

func TestInstallExtensionNoPHP(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	isolatePATH(t)
	env := linuxUserEnv(t.TempDir())
	err := InstallExtension(context.Background(), Options{Scope: platform.User, Env: &env})
	if !errors.Is(err, ErrNoInterpreter) {
		t.Fatalf("expected ErrNoInterpreter, got %v", err)
	}
}

func TestInstallExtensionSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	extDir := t.TempDir()
	iniDir := t.TempDir()
	scanDir := filepath.Join(iniDir, "conf.d")
	loadedFile := filepath.Join(iniDir, "php.ini")
	if err := os.WriteFile(loadedFile, []byte("zend_extension=xdebug.so\nmemory_limit=100M\n"), 0o644); err != nil {
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

	var out bytes.Buffer
	err := InstallExtension(context.Background(), Options{
		Scope: platform.User, AssumeYes: true, Out: &out, Client: client, Env: &env,
	})
	if err != nil {
		t.Fatalf("InstallExtension: %v\n%s", err, out.String())
	}

	// .so copied into extension_dir
	soDst := filepath.Join(extDir, "php-debugger-php8.3-nts-linux-x86_64.so")
	if b, err := os.ReadFile(soDst); err != nil || string(b) != fakeSO {
		t.Errorf("extension .so not copied correctly: %v", err)
	}
	// loader ini created and points at the .so
	loader := filepath.Join(scanDir, "99-php-debugger.ini")
	lb, err := os.ReadFile(loader)
	if err != nil || !strings.Contains(string(lb), "zend_extension="+soDst) {
		t.Errorf("loader ini wrong: %q (err %v)", string(lb), err)
	}
	// xdebug loader stripped from the existing php.ini
	mainIni, _ := os.ReadFile(loadedFile)
	if strings.Contains(string(mainIni), "xdebug.so") {
		t.Errorf("xdebug loader not stripped:\n%s", mainIni)
	}
	if !strings.Contains(string(mainIni), "memory_limit=100M") {
		t.Errorf("unrelated settings should be preserved:\n%s", mainIni)
	}
	// manifest records the extension
	m, err := manifest.Load(filepath.Join(home, ".local", "share", "php-debugger", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Extension == nil || m.Extension.SoPath != soDst || m.Extension.IniPath != loader {
		t.Errorf("extension not recorded correctly: %+v", m.Extension)
	}
}

func TestInstallExtensionRevertsOnLoadFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	extDir := t.TempDir()
	iniDir := t.TempDir()
	scanDir := filepath.Join(iniDir, "conf.d")
	loadedFile := filepath.Join(iniDir, "php.ini")
	if err := os.WriteFile(loadedFile, []byte("memory_limit=100M\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(t.TempDir(), "bin")
	// simulateLoad=false: even after enabling, -m never lists php_debugger.
	writeExec(t, filepath.Join(binDir, "php"),
		fakeExistingPHPForExt("8.3.7", extDir, loadedFile, scanDir, false))
	t.Setenv("PATH", binDir)

	srv := newFakeReleaseServer(t, "unused")
	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)

	var out bytes.Buffer
	err := InstallExtension(context.Background(), Options{
		Scope: platform.User, AssumeYes: true, Out: &out, Client: client, Env: &env,
	})
	if err == nil {
		t.Fatalf("expected load-failure error\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "reverted") {
		t.Errorf("error should mention revert, got: %v", err)
	}

	// .so and loader ini must be gone; manifest must not exist.
	if _, err := os.Stat(filepath.Join(extDir, "php-debugger-php8.3-nts-linux-x86_64.so")); !os.IsNotExist(err) {
		t.Error("extension .so should have been reverted")
	}
	if _, err := os.Stat(filepath.Join(scanDir, "99-php-debugger.ini")); !os.IsNotExist(err) {
		t.Error("loader ini should have been reverted")
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "php-debugger", "manifest.json")); !os.IsNotExist(err) {
		t.Error("manifest should not exist after revert")
	}
}

func TestInstallExtensionAlreadyPresent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	extDir := t.TempDir()
	scanDir := filepath.Join(t.TempDir(), "conf.d")
	if err := os.MkdirAll(scanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-create the loader so the fake reports php_debugger from the start.
	if err := os.WriteFile(filepath.Join(scanDir, "99-php-debugger.ini"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(t.TempDir(), "bin")
	writeExec(t, filepath.Join(binDir, "php"),
		fakeExistingPHPForExt("8.3.7", extDir, "", scanDir, true))
	t.Setenv("PATH", binDir)

	env := linuxUserEnv(home)
	var out bytes.Buffer
	err := InstallExtension(context.Background(), Options{Scope: platform.User, Out: &out, Env: &env})
	if err != nil {
		t.Fatalf("already-present should be a no-op, got: %v", err)
	}
	if !strings.Contains(out.String(), "nothing to do") {
		t.Errorf("expected 'nothing to do' message, got: %s", out.String())
	}
}

// When the php on PATH is our own interpreter (with the debugger compiled in),
// installing the extension is a no-op with a message naming the interpreter.
func TestInstallExtensionSkipsOwnInterpreter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	root := filepath.Join(home, ".local", "share", "php-debugger")

	// Place a fake php that reports the debugger module *under our install root*.
	ownBin := filepath.Join(root, "8.3", "bin")
	writeExec(t, filepath.Join(ownBin, "php"), fakePHP("8.3.7", true, "", ""))
	t.Setenv("PATH", ownBin) // php.Detect() finds our interpreter

	env := linuxUserEnv(home)
	var out bytes.Buffer
	// No release client: if the code tried to download, it would fail — proving
	// we returned before touching the network.
	err := InstallExtension(context.Background(), Options{Scope: platform.User, Out: &out, Env: &env})
	if err != nil {
		t.Fatalf("expected a no-op, got: %v", err)
	}
	if !strings.Contains(out.String(), "interpreter") || !strings.Contains(out.String(), "nothing to do") {
		t.Errorf("expected an interpreter-specific message, got: %s", out.String())
	}
	// No extension should be recorded.
	if _, err := os.Stat(filepath.Join(root, "manifest.json")); !os.IsNotExist(err) {
		t.Error("no manifest should be written when nothing is installed")
	}
}

// mutableExtServer serves a release with a single extension asset whose tag can
// change between requests, to simulate a newer release becoming available.
type mutableExtServer struct {
	mu  sync.Mutex
	tag string
	URL string
}

func newMutableExtServer(t *testing.T, tag string) *mutableExtServer {
	t.Helper()
	ms := &mutableExtServer{tag: tag}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ms.mu.Lock()
		tag := ms.tag
		ms.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			fmt.Fprintf(w, `{"tag_name":%q,"assets":[
				{"name":"php-debugger-php8.3-nts-linux-x86_64.so","browser_download_url":%q,"size":%d}
			]}`, tag, ms.URL+"/dl/ext", len(fakeSO))
		case r.URL.Path == "/dl/ext":
			w.Write([]byte(fakeSO))
		default:
			http.NotFound(w, r)
		}
	}))
	ms.URL = srv.URL
	t.Cleanup(srv.Close)
	return ms
}

func (ms *mutableExtServer) set(tag string) {
	ms.mu.Lock()
	ms.tag = tag
	ms.mu.Unlock()
}

// Regression: updating the extension must not lose the original ini backups. The
// update re-runs InstallExtension (forced), which finds xdebug already stripped
// and so produces no new backups; the manifest must retain the first install's
// backups so a later uninstall can still restore the user's xdebug config.
func TestUpdateExtensionPreservesXdebugBackup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	extDir := t.TempDir()
	iniDir := t.TempDir()
	scanDir := filepath.Join(iniDir, "conf.d")
	loadedFile := filepath.Join(iniDir, "php.ini")
	original := "zend_extension=xdebug.so\nxdebug.mode=develop,debug\nmemory_limit=100M\n"
	if err := os.WriteFile(loadedFile, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(t.TempDir(), "bin")
	writeExec(t, filepath.Join(binDir, "php"),
		fakeExistingPHPForExt("8.3.7", extDir, loadedFile, scanDir, true))
	t.Setenv("PATH", binDir)

	ms := newMutableExtServer(t, "1.0.0")
	client := release.NewClient()
	client.BaseURL = ms.URL
	env := linuxUserEnv(home)
	opts := Options{Scope: platform.User, AssumeYes: true, Out: &bytes.Buffer{}, Client: client, Env: &env}

	if err := InstallExtension(context.Background(), opts); err != nil {
		t.Fatalf("install extension: %v", err)
	}
	manifestPath := filepath.Join(home, ".local", "share", "php-debugger", "manifest.json")
	m, _ := manifest.Load(manifestPath)
	if m.Extension == nil || len(m.Extension.ConfigBackups) == 0 {
		t.Fatalf("first install should record ini backups, got %+v", m.Extension)
	}

	// A newer release appears; update the extension.
	ms.set("2.0.0")
	var out bytes.Buffer
	uo := opts
	uo.Out = &out
	if err := Update(context.Background(), uo, false, true); err != nil {
		t.Fatalf("Update: %v\n%s", err, out.String())
	}

	// The backups must survive the update (bug: they were nil'd out).
	m, _ = manifest.Load(manifestPath)
	if m.Extension == nil || m.Extension.ReleaseTag != "2.0.0" {
		t.Fatalf("extension not updated: %+v", m.Extension)
	}
	if len(m.Extension.ConfigBackups) == 0 {
		t.Fatal("update lost the ini backups; uninstall could no longer restore xdebug")
	}

	// End-to-end: uninstall after update must restore the user's original ini.
	if err := Uninstall(context.Background(), Options{Scope: platform.User, Out: &bytes.Buffer{}, Env: &env},
		false, false, "", false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	got, err := os.ReadFile(loadedFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("ini not restored after update+uninstall.\n got: %q\nwant: %q", got, original)
	}
}

// Regression: a php that reports a relative extension_dir (e.g. Windows' "ext")
// must have the .so installed under the PHP install dir, with an absolute loader
// path — not relative to the installer's working directory.
func TestInstallExtensionResolvesRelativeExtensionDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	t.Chdir(t.TempDir()) // contain (and make detectable) any CWD-relative write
	home := t.TempDir()
	scanDir := filepath.Join(t.TempDir(), "conf.d")

	binDir := filepath.Join(t.TempDir(), "bin")
	// php reports a RELATIVE extension_dir ("ext").
	writeExec(t, filepath.Join(binDir, "php"),
		fakeExistingPHPForExtArch("8.3.7", "x86_64", "ext", "", scanDir, true))
	t.Setenv("PATH", binDir)

	srv := newFakeReleaseServer(t, "unused")
	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)

	var out bytes.Buffer
	if err := InstallExtension(context.Background(), Options{
		Scope: platform.User, AssumeYes: true, Out: &out, Client: client, Env: &env,
	}); err != nil {
		t.Fatalf("InstallExtension: %v\n%s", err, out.String())
	}

	// The .so lands under the php install dir's ext/, addressed absolutely.
	phpReal, err := filepath.EvalSymlinks(filepath.Join(binDir, "php"))
	if err != nil {
		t.Fatal(err)
	}
	wantSo := filepath.Join(filepath.Dir(phpReal), "ext", "php-debugger-php8.3-nts-linux-x86_64.so")
	if _, err := os.Stat(wantSo); err != nil {
		t.Errorf("extension not installed at resolved absolute path %s: %v", wantSo, err)
	}
	// Nothing should have been written relative to the working directory.
	if _, err := os.Stat("ext"); err == nil {
		t.Error("extension dir was created relative to CWD")
	}
	// The loader references the .so by that absolute path.
	loader := filepath.Join(scanDir, "99-php-debugger.ini")
	if lb, _ := os.ReadFile(loader); !strings.Contains(string(lb), "zend_extension="+wantSo) {
		t.Errorf("loader should reference absolute .so path %s:\n%s", wantSo, lb)
	}
	// The manifest records the absolute .so path (so uninstall removes the right file).
	m, _ := manifest.Load(filepath.Join(home, ".local", "share", "php-debugger", "manifest.json"))
	if m.Extension == nil || m.Extension.SoPath != wantSo {
		t.Errorf("manifest SoPath = %+v, want %s", m.Extension, wantSo)
	}
}

// Regression: a forced extension reinstall that creates a fresh ini backup and
// then rolls back (verification fails) must NOT delete the prior run's backup,
// which the persisted manifest still references. Unique backup paths guarantee the
// fresh backup can't collide with the old one.
func TestForcedReinstallRollbackKeepsPriorIniBackup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	extDir := t.TempDir()
	iniDir := t.TempDir()
	scanDir := filepath.Join(iniDir, "conf.d")
	loadedFile := filepath.Join(iniDir, "php.ini")
	original := "zend_extension=xdebug.so\nxdebug.mode=develop,debug\nmemory_limit=100M\n"
	if err := os.WriteFile(loadedFile, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	phpPath := filepath.Join(t.TempDir(), "bin", "php")
	writeExec(t, phpPath, fakeExistingPHPForExt("8.3.7", extDir, loadedFile, scanDir, true))
	t.Setenv("PATH", filepath.Dir(phpPath))

	srv := newFakeReleaseServer(t, "unused")
	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)
	opts := Options{Scope: platform.User, AssumeYes: true, Out: &bytes.Buffer{}, Client: client, Env: &env}

	// First install: strips xdebug and records backup B1 holding the original.
	if err := InstallExtension(context.Background(), opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	manifestPath := filepath.Join(home, ".local", "share", "php-debugger", "manifest.json")
	m, _ := manifest.Load(manifestPath)
	if m.Extension == nil || len(m.Extension.ConfigBackups) == 0 {
		t.Fatalf("expected a config backup, got %+v", m.Extension)
	}
	b1 := m.Extension.ConfigBackups[0].BackupPath
	if data, err := os.ReadFile(b1); err != nil || !strings.Contains(string(data), "xdebug.so") {
		t.Fatalf("B1 should hold the original xdebug ini: %q err=%v", data, err)
	}

	// The user re-adds xdebug (so a forced reinstall strips again → a fresh backup),
	// and the reinstall is made to FAIL at verification so it rolls back.
	if err := os.WriteFile(loadedFile, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExec(t, phpPath, fakeExistingPHPForExt("8.3.7", extDir, loadedFile, scanDir, false))

	fo := opts
	fo.Force = true
	if err := InstallExtension(context.Background(), fo); err == nil {
		t.Fatal("expected forced reinstall to fail at verification (so it rolls back)")
	}

	// The prior backup B1 must survive the rollback (unique paths ⇒ no collision).
	if _, err := os.Stat(b1); err != nil {
		t.Errorf("prior backup B1 was deleted by the rollback: %v", err)
	}
	// The persisted (unchanged) manifest still references B1 and can restore xdebug.
	m2, _ := manifest.Load(manifestPath)
	if m2.Extension == nil || len(m2.Extension.ConfigBackups) == 0 ||
		m2.Extension.ConfigBackups[0].BackupPath != b1 {
		t.Fatalf("manifest should still reference B1: %+v", m2.Extension)
	}
	if err := restoreBackup(b1, loadedFile); err != nil {
		t.Fatalf("restore from B1 failed: %v", err)
	}
	if got, _ := os.ReadFile(loadedFile); string(got) != original {
		t.Errorf("restore did not reproduce the original ini:\n%s", got)
	}
}

// newFakeExtReleaseServer serves a release whose extension assets exist for both
// architectures of the given os token, so a test can assert which one is chosen.
func newFakeExtReleaseServer(t *testing.T, osTok string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			fmt.Fprintf(w, `{"tag_name":"9.9.9","assets":[
				{"name":"php-debugger-php8.3-nts-%s-x86_64.so","browser_download_url":%q,"size":%d},
				{"name":"php-debugger-php8.3-nts-%s-arm64.so","browser_download_url":%q,"size":%d}
			]}`, osTok, srv.URL+"/dl/x86_64", len(fakeSO), osTok, srv.URL+"/dl/arm64", len(fakeSO))
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			w.Write([]byte(fakeSO))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// On an Apple-Silicon host running an Intel php under Rosetta, the extension must
// match the php process's architecture (x86_64), not the host's native arm64.
func TestInstallExtensionMatchesPHPArch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	extDir := t.TempDir()
	scanDir := filepath.Join(t.TempDir(), "conf.d")

	binDir := filepath.Join(t.TempDir(), "bin")
	// php reports itself as x86_64 even though the host env below is arm64.
	writeExec(t, filepath.Join(binDir, "php"),
		fakeExistingPHPForExtArch("8.3.7", "x86_64", extDir, "", scanDir, true))
	t.Setenv("PATH", binDir)

	srv := newFakeExtReleaseServer(t, "macos")
	client := release.NewClient()
	client.BaseURL = srv.URL

	// Host is Apple Silicon (macos/arm64).
	env := platform.Env{
		OS: platform.MacOS, Arch: platform.Arm64, Home: home,
		Getenv: func(string) string { return "" },
	}

	var out bytes.Buffer
	err := InstallExtension(context.Background(), Options{
		Scope: platform.User, AssumeYes: true, Out: &out, Client: client, Env: &env,
	})
	if err != nil {
		t.Fatalf("InstallExtension: %v\n%s", err, out.String())
	}

	// The x86_64 extension (matching php) must be chosen, not the host's arm64.
	if _, err := os.Stat(filepath.Join(extDir, "php-debugger-php8.3-nts-macos-x86_64.so")); err != nil {
		t.Errorf("expected x86_64 extension to be installed (matching php arch): %v", err)
	}
	if _, err := os.Stat(filepath.Join(extDir, "php-debugger-php8.3-nts-macos-arm64.so")); !os.IsNotExist(err) {
		t.Error("arm64 extension should not have been installed on a Rosetta php")
	}
}
