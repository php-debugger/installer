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

// fakePHP is a /bin/sh script that impersonates a php binary well enough for the
// installer's smoke test, info query and module check. hasDebugger controls
// whether `-m` lists the debugger module (to exercise the rollback path).
func fakePHP(hasDebugger bool) string {
	modules := "[PHP Modules]\\nCore\\ndate\\n"
	if hasDebugger {
		modules += "php_debugger\\n"
	}
	return `#!/bin/sh
case "$1" in
  -v) echo "PHP 8.3.7 (cli) (built: Jan 1 2026) (NTS)" ;;
  -m) printf '` + modules + `' ;;
  --ini) echo "Loaded Configuration File:         (none)" ;;
  -r) printf 'version=8.3.7\nseries=8.3\nzts=0\nextension_dir=/fake/ext\n' ;;
  *) : ;;
esac
exit 0
`
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

func TestInstallInterpreterCleanHost(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	srv := newFakeReleaseServer(t, fakePHP(true))

	client := release.NewClient()
	client.BaseURL = srv.URL

	env := linuxUserEnv(home)
	var out bytes.Buffer
	err := InstallInterpreter(context.Background(), Options{
		Scope:  platform.User,
		Out:    &out,
		Client: client,
		Env:    &env,
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
	if err != nil {
		t.Fatalf("active symlink missing: %v", err)
	}
	if got != binTarget {
		t.Errorf("symlink -> %q, want %q", got, binTarget)
	}

	m, err := manifest.Load(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Active() != "8.3" {
		t.Errorf("manifest active = %q, want 8.3", m.Active())
	}
	it, ok := m.Interpreter("8.3")
	if !ok {
		t.Fatal("interpreter 8.3 not recorded in manifest")
	}
	if it.PHPVersion != "8.3.7" || it.ReleaseTag != "9.9.9" {
		t.Errorf("interpreter record = %+v", it)
	}
	if it.Dir != filepath.Join(root, "8.3") {
		t.Errorf("interpreter dir = %q", it.Dir)
	}
}

func TestInstallInterpreterRollbackOnMissingModule(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	srv := newFakeReleaseServer(t, fakePHP(false)) // -m does NOT list php-debugger

	client := release.NewClient()
	client.BaseURL = srv.URL

	env := linuxUserEnv(home)
	var out bytes.Buffer
	err := InstallInterpreter(context.Background(), Options{
		Scope:  platform.User,
		Out:    &out,
		Client: client,
		Env:    &env,
	})
	if err == nil {
		t.Fatalf("expected install to fail when module is missing\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Errorf("error should mention rollback, got: %v", err)
	}

	root := filepath.Join(home, ".local", "share", "php-debugger")

	// version dir removed
	if _, err := os.Stat(filepath.Join(root, "8.3")); !os.IsNotExist(err) {
		t.Error("version directory should have been rolled back")
	}
	// symlink not left behind
	if _, err := os.Lstat(filepath.Join(home, ".local", "bin", "php")); !os.IsNotExist(err) {
		t.Error("active symlink should have been rolled back")
	}
	// manifest never written (save happens only after verification)
	if _, err := os.Stat(filepath.Join(root, "manifest.json")); !os.IsNotExist(err) {
		t.Error("manifest should not exist after a rolled-back install")
	}
}

func TestInstallInterpreterSmokeFailureNoChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	// a "binary" that exits non-zero on -v
	badPHP := "#!/bin/sh\nexit 3\n"
	srv := newFakeReleaseServer(t, badPHP)

	client := release.NewClient()
	client.BaseURL = srv.URL

	env := linuxUserEnv(home)
	var out bytes.Buffer
	err := InstallInterpreter(context.Background(), Options{
		Scope:  platform.User,
		Out:    &out,
		Client: client,
		Env:    &env,
	})
	if err == nil {
		t.Fatal("expected smoke-test failure")
	}
	if !strings.Contains(err.Error(), "--extension-only") {
		t.Errorf("smoke failure should suggest the extension, got: %v", err)
	}
	// nothing should have been created under the install root
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
