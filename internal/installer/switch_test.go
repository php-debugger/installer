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

// newMultiVersionServer serves a release with 8.3 and 8.4 interpreter assets,
// each a fake php reporting its own version.
func newMultiVersionServer(t *testing.T) *httptest.Server {
	t.Helper()
	php83 := fakePHP("8.3.7", true, "", "")
	php84 := fakePHP("8.4.2", true, "", "")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/php-debugger/php-debugger/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"9.9.9","assets":[
				{"name":"php-php8.3-nts-linux-x86_64","browser_download_url":%q},
				{"name":"php-php8.4-nts-linux-x86_64","browser_download_url":%q}
			]}`, srv.URL+"/dl/83", srv.URL+"/dl/84")
		case "/dl/83":
			w.Write([]byte(php83))
		case "/dl/84":
			w.Write([]byte(php84))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSwitchInstallsAndFlips(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	isolatePATH(t)
	home := t.TempDir()
	srv := newMultiVersionServer(t)
	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)

	baseOpts := func() Options {
		return Options{Scope: platform.User, Client: client, Env: &env, Out: &bytes.Buffer{}}
	}

	root := filepath.Join(home, ".local", "share", "php-debugger")
	link := filepath.Join(home, ".local", "bin", "php")
	manifestPath := filepath.Join(root, "manifest.json")

	assertActive := func(wantKey, wantDir string) {
		t.Helper()
		tgt, err := os.Readlink(link)
		if err != nil {
			t.Fatalf("readlink: %v", err)
		}
		want := filepath.Join(root, wantDir, "bin", "php")
		if tgt != want {
			t.Errorf("symlink -> %q, want %q", tgt, want)
		}
		m, err := manifest.Load(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		if m.Active() != wantKey {
			t.Errorf("manifest active = %q, want %q", m.Active(), wantKey)
		}
	}

	// 1. switch to 8.3 (not installed) -> installs + activates
	o := baseOpts()
	o.PHPVersion = "8.3"
	if err := Switch(context.Background(), o); err != nil {
		t.Fatalf("switch 8.3: %v", err)
	}
	assertActive("8.3", "8.3")

	// 2. switch to 8.4 (not installed) -> installs + activates at same bin dir
	o = baseOpts()
	o.PHPVersion = "8.4"
	if err := Switch(context.Background(), o); err != nil {
		t.Fatalf("switch 8.4: %v", err)
	}
	assertActive("8.4", "8.4")

	// both variants coexist on disk
	for _, v := range []string{"8.3", "8.4"} {
		if _, err := os.Stat(filepath.Join(root, v, "bin", "php")); err != nil {
			t.Errorf("variant %s should still be installed: %v", v, err)
		}
	}

	// 3. switch back to 8.3 (installed) -> just re-points, no re-download
	o = baseOpts()
	o.PHPVersion = "8.3"
	if err := Switch(context.Background(), o); err != nil {
		t.Fatalf("switch back to 8.3: %v", err)
	}
	assertActive("8.3", "8.3")

	// 4. switch to already-active 8.3 -> no-op message
	var out bytes.Buffer
	o = baseOpts()
	o.PHPVersion = "8.3"
	o.Out = &out
	if err := Switch(context.Background(), o); err != nil {
		t.Fatalf("switch to active: %v", err)
	}
	if !strings.Contains(out.String(), "already active") {
		t.Errorf("expected 'already active' message, got: %s", out.String())
	}
}

func TestSwitchRequiresVersion(t *testing.T) {
	env := linuxUserEnv(t.TempDir())
	if err := Switch(context.Background(), Options{Scope: platform.User, Env: &env}); err == nil {
		t.Error("switch with no version should error")
	}
}
