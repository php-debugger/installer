package installer

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/php-debugger/installer/internal/manifest"
	"github.com/php-debugger/installer/internal/platform"
	"github.com/php-debugger/installer/internal/release"
)

// mutableServer serves a release whose tag and interpreter bytes can be changed
// between requests, to simulate a new release becoming available.
type mutableServer struct {
	mu     sync.Mutex
	tag    string
	script string
	URL    string
}

func newMutableServer(t *testing.T, tag, script string) *mutableServer {
	t.Helper()
	ms := &mutableServer{tag: tag, script: script}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ms.mu.Lock()
		tag, script := ms.tag, ms.script
		ms.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			fmt.Fprintf(w, `{"tag_name":%q,"assets":[
				{"name":"php-php8.3-nts-linux-x86_64","browser_download_url":%q}
			]}`, tag, ms.URL+"/dl/php")
		case r.URL.Path == "/dl/php":
			w.Write([]byte(script))
		default:
			http.NotFound(w, r)
		}
	}))
	ms.URL = srv.URL
	t.Cleanup(srv.Close)
	return ms
}

func (ms *mutableServer) set(tag, script string) {
	ms.mu.Lock()
	ms.tag, ms.script = tag, script
	ms.mu.Unlock()
}

func installBaseInterpreter(t *testing.T, home string, ms *mutableServer) (Options, string) {
	t.Helper()
	client := release.NewClient()
	client.BaseURL = ms.URL
	env := linuxUserEnv(home)
	opts := Options{Scope: platform.User, Client: client, Env: &env, Out: &bytes.Buffer{}, PHPVersion: "8.3"}
	if err := InstallInterpreter(context.Background(), opts); err != nil {
		t.Fatalf("base install: %v", err)
	}
	return opts, filepath.Join(home, ".local", "share", "php-debugger", "manifest.json")
}

func TestUpdateInterpreter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	isolatePATH(t)
	home := t.TempDir()
	ms := newMutableServer(t, "1.0.0", fakePHP("8.3.7", true, "", ""))
	opts, manifestPath := installBaseInterpreter(t, home, ms)

	// A newer release appears.
	ms.set("2.0.0", fakePHP("8.3.9", true, "", ""))

	var out bytes.Buffer
	uo := opts
	uo.Out = &out
	uo.PHPVersion = "" // update figures out the target from the manifest
	if err := Update(context.Background(), uo, false, false); err != nil {
		t.Fatalf("Update: %v\n%s", err, out.String())
	}

	m, _ := manifest.Load(manifestPath)
	it, _ := m.Interpreter("8.3")
	if it.ReleaseTag != "2.0.0" {
		t.Errorf("ReleaseTag = %q, want 2.0.0", it.ReleaseTag)
	}
	if it.PHPVersion != "8.3.9" {
		t.Errorf("PHPVersion = %q, want 8.3.9", it.PHPVersion)
	}
}

// Regression: when the installer's own interpreter is active and on PATH, update
// must still reinstall against a newer release. The "already provided" check in
// InstallInterpreter matches on series+threading (not release tag), so update has
// to force past it — otherwise it silently no-ops and leaves the old binary/tag.
func TestUpdateInterpreterActiveOnPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	// Put the install's bin dir on PATH so the active php is discoverable, the way
	// it is after a real install (unlike the isolated-PATH tests above).
	binDir := filepath.Join(home, ".local", "bin")
	t.Setenv("PATH", binDir)

	ms := newMutableServer(t, "1.0.0", fakePHP("8.3.7", true, "", ""))
	opts, manifestPath := installBaseInterpreter(t, home, ms)

	// Sanity: the active php is now on PATH and reports the debugger.
	if has, err := phpHasDebugger(filepath.Join(binDir, "php")); err != nil || !has {
		t.Fatalf("active php not usable on PATH: has=%v err=%v", has, err)
	}

	// A newer release appears.
	ms.set("2.0.0", fakePHP("8.3.9", true, "", ""))

	var out bytes.Buffer
	uo := opts
	uo.Out = &out
	uo.PHPVersion = ""
	if err := Update(context.Background(), uo, false, false); err != nil {
		t.Fatalf("Update: %v\n%s", err, out.String())
	}

	m, _ := manifest.Load(manifestPath)
	it, _ := m.Interpreter("8.3")
	if it.ReleaseTag != "2.0.0" {
		t.Errorf("ReleaseTag = %q, want 2.0.0 (update no-opped past the already-provided check)", it.ReleaseTag)
	}
	if it.PHPVersion != "8.3.9" {
		t.Errorf("PHPVersion = %q, want 8.3.9", it.PHPVersion)
	}
}

func TestUpdateAlreadyLatest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	isolatePATH(t)
	home := t.TempDir()
	ms := newMutableServer(t, "1.0.0", fakePHP("8.3.7", true, "", ""))
	opts, _ := installBaseInterpreter(t, home, ms)

	var out bytes.Buffer
	uo := opts
	uo.Out = &out
	if err := Update(context.Background(), uo, false, false); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !strings.Contains(out.String(), "up to date") {
		t.Errorf("expected 'up to date', got: %s", out.String())
	}
}

func TestUpdateRollbackKeepsWorkingInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	isolatePATH(t)
	home := t.TempDir()
	ms := newMutableServer(t, "1.0.0", fakePHP("8.3.7", true, "", ""))
	opts, manifestPath := installBaseInterpreter(t, home, ms)

	// New release is broken: the interpreter lacks the debugger module.
	ms.set("2.0.0", fakePHP("8.3.9", false, "", ""))

	var out bytes.Buffer
	uo := opts
	uo.Out = &out
	err := Update(context.Background(), uo, false, false)
	if err == nil {
		t.Fatalf("expected update to fail on broken release\n%s", out.String())
	}

	// Manifest must still point at the old, working release.
	m, _ := manifest.Load(manifestPath)
	it, _ := m.Interpreter("8.3")
	if it.ReleaseTag != "1.0.0" || it.PHPVersion != "8.3.7" {
		t.Errorf("manifest changed after failed update: %+v", it)
	}
	// The active php must still run and have the debugger (old binary restored).
	link := filepath.Join(home, ".local", "bin", "php")
	has, err := phpHasDebugger(link)
	if err != nil {
		t.Fatalf("running restored php: %v", err)
	}
	if !has {
		t.Error("restored interpreter should still report the debugger module")
	}
}

func TestUpdateNothingInstalled(t *testing.T) {
	env := linuxUserEnv(t.TempDir())
	err := Update(context.Background(), Options{Scope: platform.User, Env: &env}, false, false)
	if err == nil || !strings.Contains(err.Error(), "nothing installed") {
		t.Errorf("expected 'nothing installed' error, got: %v", err)
	}
}

func TestUpdateAmbiguous(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	isolatePATH(t)
	home := t.TempDir()
	ms := newMutableServer(t, "1.0.0", fakePHP("8.3.7", true, "", ""))
	_, manifestPath := installBaseInterpreter(t, home, ms)

	// Also record an extension so both are present.
	m, _ := manifest.Load(manifestPath)
	m.SetExtension(manifest.Extension{Series: "8.3", ReleaseTag: "1.0.0"})
	if err := m.Save(manifestPath); err != nil {
		t.Fatal(err)
	}

	env := linuxUserEnv(home)
	err := Update(context.Background(), Options{Scope: platform.User, Env: &env}, false, false)
	if err == nil || !strings.Contains(err.Error(), "specify") {
		t.Errorf("expected ambiguity error, got: %v", err)
	}
}

// phpHasDebugger runs `path -m` and reports whether php_debugger is listed.
func phpHasDebugger(path string) (bool, error) {
	out, err := exec.Command(path, "-m").Output()
	if err != nil {
		return false, err
	}
	return strings.Contains(string(out), "php_debugger"), nil
}
