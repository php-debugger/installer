package installer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/php-debugger/installer/internal/manifest"
	"github.com/php-debugger/installer/internal/platform"
	"github.com/php-debugger/installer/internal/release"
)

// fakeExistingPHPForExt builds a fake php whose `-m` lists php_debugger only when
// the loader ini exists AND simulateLoad is true — mimicking dynamic loading via
// the enabled extension. It reports the given extension_dir and ini locations.
func fakeExistingPHPForExt(version, extDir, loadedFile, scanDir string, simulateLoad bool) string {
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
  -r) printf 'version=%s\nseries=%s\nzts=0\nextension_dir=%s\n' ;;
  *) : ;;
esac
exit 0
`, version, sim, loader, loadedFile, scanDir, version, series, extDir)
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
