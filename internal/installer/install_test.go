package installer

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

// fakePHPLoadingConfig is like fakePHP but reports a real loaded php.ini and an
// additional parsed .ini file (via `--ini`), so tests can exercise sanitizing the
// interpreter's OWN compiled-in config. It always reports the debugger module.
func fakePHPLoadingConfig(version, loadedFile, scanDir, additional string) string {
	series := version
	if i := strings.LastIndex(version, "."); i >= 0 {
		series = version[:i]
	}
	return fmt.Sprintf(`#!/bin/sh
case "$1" in
  -v) echo "PHP %s (cli) (built: Jan 1 2026) (NTS)" ;;
  -m) printf '[PHP Modules]\nCore\ndate\nphp_debugger\n' ;;
  --ini)
    echo "Configuration File (php.ini) Path: \"%s\""
    echo "Loaded Configuration File:         \"%s\""
    echo "Scan for additional .ini files in: \"%s\""
    echo "Additional .ini files parsed:      \"%s\"" ;;
  -r) printf 'version=%s\nseries=%s\nzts=0\nextension_dir=/fake/ext\n' ;;
  *) : ;;
esac
exit 0
`, version, filepath.Dir(loadedFile), loadedFile, scanDir, additional, version, series)
}

const fakeSO = "FAKE-DEBUGGER-SO-BYTES"

// newFakeReleaseServer serves a latest-release payload with both an interpreter
// asset (bytes = phpScript) and an extension asset (bytes = fakeSO).
func newFakeReleaseServer(t *testing.T, phpScript string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			fmt.Fprintf(w, `{"tag_name":"9.9.9","assets":[
				{"name":"php-php8.3-nts-linux-x86_64","browser_download_url":%q,"size":%d},
				{"name":"php-debugger-php8.3-nts-linux-x86_64.so","browser_download_url":%q,"size":%d}
			]}`, srv.URL+"/dl/php", len(phpScript), srv.URL+"/dl/ext", len(fakeSO))
		case r.URL.Path == "/dl/php":
			w.Write([]byte(phpScript))
		case r.URL.Path == "/dl/ext":
			w.Write([]byte(fakeSO))
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

// Regression: a real php sitting in the selected bin dir but not on PATH (so it
// is never detected) must be backed up before Activate overwrites it — otherwise
// installing would destroy a foreign interpreter with no way to restore it.
func TestInstallInterpreterBacksUpUndetectedInterpreterAtDest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	isolatePATH(t) // nothing is detectable on PATH -> treated as a clean host
	home := t.TempDir()

	// A real (non-symlink) php already lives in the scope bin dir, off PATH.
	foreign := filepath.Join(home, ".local", "bin", "php")
	const sentinel = "#!/bin/sh\necho REAL-FOREIGN-PHP\n"
	writeExec(t, foreign, sentinel)

	srv := newFakeReleaseServer(t, fakePHP("8.3.7", true, "", ""))
	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)

	if err := InstallInterpreter(context.Background(), Options{
		Scope: platform.User, Out: &bytes.Buffer{}, Client: client, Env: &env, PHPVersion: "8.3",
	}); err != nil {
		t.Fatalf("install: %v", err)
	}

	// The destination is now our symlink, and the foreign binary is backed up.
	if isLink, _ := platform.IsSymlink(foreign); !isLink {
		t.Error("active php should be our symlink after install")
	}
	root := filepath.Join(home, ".local", "share", "php-debugger")
	m, _ := manifest.Load(filepath.Join(root, "manifest.json"))
	_, b, ok := anyBackup(m)
	if !ok {
		t.Fatal("the overwritten foreign php should have been backed up")
	}
	if b.OriginalPath != foreign {
		t.Errorf("backup OriginalPath = %q, want %q", b.OriginalPath, foreign)
	}
	if data, err := os.ReadFile(b.BackupPath); err != nil || string(data) != sentinel {
		t.Errorf("backup does not hold the original foreign php: %q err=%v", data, err)
	}

	// Uninstall restores the foreign php byte-for-byte.
	if err := Uninstall(context.Background(), Options{Scope: platform.User, Out: &bytes.Buffer{}, Env: &env},
		"", false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if isLink, _ := platform.IsSymlink(foreign); isLink {
		t.Error("restored foreign php should be a real file, not a symlink")
	}
	if data, err := os.ReadFile(foreign); err != nil || string(data) != sentinel {
		t.Errorf("foreign php not restored: %q err=%v", data, err)
	}
}

// End-to-end: a user-created symlink at the activation path pointing at another
// php (not detected on PATH) must be backed up on install and restored on
// uninstall — not silently replaced and lost.
func TestInstallInterpreterBacksUpForeignSymlinkAtDest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	isolatePATH(t) // the foreign symlink is off PATH -> not detected
	home := t.TempDir()

	// A user symlink in the scope bin dir pointing at some other php.
	foreignPhp := filepath.Join(t.TempDir(), "other-php")
	writeExec(t, foreignPhp, "#!/bin/sh\necho OTHER\n")
	link := filepath.Join(home, ".local", "bin", "php")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreignPhp, link); err != nil {
		t.Fatal(err)
	}

	srv := newFakeReleaseServer(t, fakePHP("8.3.7", true, "", ""))
	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)

	if err := InstallInterpreter(context.Background(), Options{
		Scope: platform.User, Out: &bytes.Buffer{}, Client: client, Env: &env, PHPVersion: "8.3",
	}); err != nil {
		t.Fatalf("install: %v", err)
	}

	// Our interpreter is now active, and the foreign symlink was backed up.
	root := filepath.Join(home, ".local", "share", "php-debugger")
	binTarget := filepath.Join(root, "8.3", "bin", "php")
	if tgt, _ := os.Readlink(link); tgt != binTarget {
		t.Errorf("active php should point at our interpreter, got %q", tgt)
	}
	m, _ := manifest.Load(filepath.Join(root, "manifest.json"))
	_, b, ok := anyBackup(m)
	if !ok {
		t.Fatal("the foreign symlink should have been backed up")
	}
	if b.OriginalPath != link {
		t.Errorf("backup OriginalPath = %q, want %q", b.OriginalPath, link)
	}

	// Uninstall restores the user's foreign symlink, pointing at their php.
	if err := Uninstall(context.Background(), Options{Scope: platform.User, Out: &bytes.Buffer{}, Env: &env},
		"", false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if isLink, _ := isSymlinkNode(link); !isLink {
		t.Error("restored path should be a symlink, not our removed entry")
	}
	if tgt, err := os.Readlink(link); err != nil || tgt != foreignPhp {
		t.Errorf("foreign symlink not restored: %q err=%v", tgt, err)
	}
}

// Windows activation may write php.cmd (shim) instead of php.exe, so both must be
// candidates for the destination backup — otherwise a real php.cmd is clobbered.
func TestActiveCandidatePaths(t *testing.T) {
	unix := activeCandidatePaths(platform.Linux, "/bin")
	if len(unix) != 1 || filepath.Base(unix[0]) != "php" {
		t.Errorf("unix candidates = %v, want [/bin/php]", unix)
	}
	win := activeCandidatePaths(platform.Windows, `C:\bin`)
	hasExe, hasCmd := false, false
	for _, p := range win {
		switch filepath.Base(p) {
		case "php.exe":
			hasExe = true
		case "php.cmd":
			hasCmd = true
		}
	}
	if !hasExe || !hasCmd {
		t.Errorf("windows candidates must include php.exe and php.cmd, got %v", win)
	}
}

// A real php.cmd shim at the destination (the case the Unix test cannot exercise)
// must be backed up before activation and restored on rollback.
func TestPreserveDestinationBacksUpShim(t *testing.T) {
	dir := t.TempDir()
	backups := filepath.Join(dir, "backups")
	exe := filepath.Join(dir, "php.exe") // absent
	cmd := filepath.Join(dir, "php.cmd") // a real, foreign shim
	const shim = "@echo off\r\n\"C:\\real\\php.exe\" %*\r\n"
	if err := os.WriteFile(cmd, []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}

	rb := &rollback{}
	got, err := preserveDestination([]string{exe, cmd}, backups, "8.3", filepath.Join(dir, "root"), rb, Options{Out: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("preserveDestination: %v", err)
	}
	if len(got) != 1 || got[0].OriginalPath != cmd {
		t.Fatalf("expected only php.cmd to be backed up, got %+v", got)
	}
	b := got[0]
	// The original is moved aside so activation can write freely.
	if _, err := os.Stat(cmd); !os.IsNotExist(err) {
		t.Error("php.cmd should have been moved to the backup")
	}
	if data, err := os.ReadFile(b.BackupPath); err != nil || string(data) != shim {
		t.Errorf("backup does not hold the original shim: %q err=%v", data, err)
	}
	// Rollback restores it byte-for-byte.
	rb.run()
	if data, err := os.ReadFile(cmd); err != nil || string(data) != shim {
		t.Errorf("php.cmd not restored on rollback: %q err=%v", data, err)
	}
}

// If both Windows activation forms (php.exe and php.cmd) are real files, BOTH must
// be backed up — activation clobbers one and removes the other, so backing up only
// the first would still lose one without recovery.
func TestPreserveDestinationBacksUpBothWindowsForms(t *testing.T) {
	dir := t.TempDir()
	backups := filepath.Join(dir, "backups")
	exe := filepath.Join(dir, "php.exe")
	cmd := filepath.Join(dir, "php.cmd")
	if err := os.WriteFile(exe, []byte("EXE"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cmd, []byte("CMD"), 0o755); err != nil {
		t.Fatal(err)
	}

	rb := &rollback{}
	got, err := preserveDestination([]string{exe, cmd}, backups, "8.3", filepath.Join(dir, "root"), rb, Options{Out: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("preserveDestination: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("both real files should be backed up, got %d: %+v", len(got), got)
	}

	// Each original was moved aside into a backup holding its own contents.
	want := map[string]string{"php.exe": "EXE", "php.cmd": "CMD"}
	for _, b := range got {
		name := filepath.Base(b.OriginalPath)
		if data, err := os.ReadFile(b.BackupPath); err != nil || string(data) != want[name] {
			t.Errorf("%s backup content = %q (err %v), want %q", name, data, err, want[name])
		}
		if _, err := os.Stat(b.OriginalPath); !os.IsNotExist(err) {
			t.Errorf("%s should have been moved aside", name)
		}
	}

	// Rollback restores both, byte-for-byte.
	rb.run()
	if data, _ := os.ReadFile(exe); string(data) != "EXE" {
		t.Error("php.exe not restored on rollback")
	}
	if data, _ := os.ReadFile(cmd); string(data) != "CMD" {
		t.Error("php.cmd not restored on rollback")
	}
}

// Our own active entry — a symlink resolving into the install root — is skipped:
// uninstall reconstructs it from the manifest, and it holds no user data.
func TestPreserveDestinationSkipsOwnSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "share", "php-debugger")
	target := filepath.Join(root, "8.3", "bin", "php") // under our install root
	writeExec(t, target, fakePHP("8.3.7", true, "", ""))
	link := filepath.Join(dir, "bin", "php")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	rb := &rollback{}
	got, err := preserveDestination([]string{link}, filepath.Join(dir, "backups"), "8.3", root, rb, Options{Out: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("preserveDestination: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("our own active symlink should not be backed up, got %+v", got)
	}
	if isLink, _ := isSymlinkNode(link); !isLink {
		t.Error("our symlink should be left in place")
	}
}

// A foreign symlink (to a php outside our install root) that the user created at
// the activation path MUST be backed up — otherwise activation replaces it and
// uninstall cannot restore the user's original.
func TestPreserveDestinationBacksUpForeignSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "share", "php-debugger") // our install root (unrelated)
	foreignTarget := filepath.Join(dir, "opt", "other-php")
	writeExec(t, foreignTarget, "#!/bin/sh\necho OTHER\n")
	link := filepath.Join(dir, "bin", "php") // user symlink -> foreign php
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreignTarget, link); err != nil {
		t.Fatal(err)
	}

	rb := &rollback{}
	got, err := preserveDestination([]string{link}, filepath.Join(dir, "backups"), "8.3", root, rb, Options{Out: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("preserveDestination: %v", err)
	}
	if len(got) != 1 || got[0].OriginalPath != link {
		t.Fatalf("foreign symlink should be backed up, got %+v", got)
	}
	// It was moved aside (so activation can write) and the backup is still a symlink
	// to the user's original target.
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Error("foreign symlink should have been moved to the backup")
	}
	if tgt, err := os.Readlink(got[0].BackupPath); err != nil || tgt != foreignTarget {
		t.Errorf("backup should preserve the symlink target: %q err=%v", tgt, err)
	}
	// Rollback restores the user's symlink pointing at their php.
	rb.run()
	if tgt, err := os.Readlink(link); err != nil || tgt != foreignTarget {
		t.Errorf("foreign symlink not restored on rollback: %q err=%v", tgt, err)
	}
}

// Regression: these self-contained builds report the existing PHP's own config
// directory as their compiled-in config path (e.g. /opt/homebrew/etc/php/8.5).
// The interpreter install must disable the incompatible xdebug there IN PLACE but
// back the file up — so the self-contained php stops erroring on the foreign
// zend_extension — and uninstall must RESTORE the user's original config (never
// delete it or prune their directory, as an earlier version did).
func TestInstallInterpreterSharedConfigDirInPlace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()

	// The existing php's config lives here; the NEW interpreter reports the very
	// same directory as its config path/scan dir (the real-world collision).
	sharedCfg := t.TempDir()
	sharedScan := filepath.Join(sharedCfg, "conf.d")
	srv := newFakeReleaseServer(t, fakePHP("8.3.7", true, sharedCfg, sharedScan))

	existBin := filepath.Join(t.TempDir(), "bin")
	loadedFile := filepath.Join(sharedCfg, "php.ini")
	xdebugIni := filepath.Join(sharedScan, "xdebug.ini")
	writeExec(t, filepath.Join(existBin, "php"),
		existingPHPScript("8.3.4", loadedFile, sharedScan, xdebugIni))
	if err := os.MkdirAll(sharedScan, 0o755); err != nil {
		t.Fatal(err)
	}
	const phpIni = "memory_limit=128M\n"
	const xdebugContent = "zend_extension=/opt/homebrew/lib/php/xdebug.so\n"
	if err := os.WriteFile(loadedFile, []byte(phpIni), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xdebugIni, []byte(xdebugContent), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", existBin)

	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)

	var out bytes.Buffer
	if err := InstallInterpreter(context.Background(), Options{
		Scope: platform.User, AssumeYes: true, Out: &out, Client: client, Env: &env,
	}); err != nil {
		t.Fatalf("InstallInterpreter: %v\n%s", err, out.String())
	}

	// Install disabled the incompatible xdebug loader in the shared file...
	if got, _ := os.ReadFile(xdebugIni); strings.Contains(string(got), "zend_extension") {
		t.Errorf("shared xdebug.ini should have its loader stripped, got %q", got)
	}
	// ...backing it up (recorded as ConfigBackups, not deletable ConfigFiles).
	root := filepath.Join(home, ".local", "share", "php-debugger")
	m, err := manifest.Load(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	it, _ := m.Interpreter("8.3")
	if len(it.ConfigFiles) != 0 {
		t.Errorf("user's own files must not be recorded as copied ConfigFiles, got %v", it.ConfigFiles)
	}
	if len(it.ConfigBackups) == 0 {
		t.Fatal("in-place sanitized file must be recorded as a ConfigBackup for restore")
	}

	// Uninstall must restore the user's original config, not delete it or prune the
	// directory.
	if err := Uninstall(context.Background(), Options{Scope: platform.User, Out: &bytes.Buffer{}, Env: &env},
		"", false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	got, err := os.ReadFile(xdebugIni)
	if err != nil {
		t.Fatalf("uninstall deleted the user's xdebug.ini: %v", err)
	}
	if string(got) != xdebugContent {
		t.Errorf("uninstall did not restore xdebug.ini: got %q, want %q", got, xdebugContent)
	}
	if fi, err := os.Stat(xdebugIni); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("restored xdebug.ini mode = %v (err %v), want 0644", fi.Mode().Perm(), err)
	}
}

// Regression: `switch <ver>` (and any install with no foreign php on PATH, e.g.
// while our own interpreter is active) still installs an interpreter whose
// compiled-in config path may hold a same-version Homebrew xdebug it cannot load.
// The install must sanitize that own config in place (backed up), or the new
// interpreter errors trying to load the foreign zend_extension on every run.
func TestInstallInterpreterSanitizesOwnConfigNoForeignPHP(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	isolatePATH(t) // no foreign php on PATH -> existing == nil
	home := t.TempDir()

	// The new interpreter's own compiled-in config path holds a real xdebug loader.
	ownCfg := t.TempDir()
	ownScan := filepath.Join(ownCfg, "conf.d")
	loadedFile := filepath.Join(ownCfg, "php.ini")
	xdebugIni := filepath.Join(ownScan, "xdebug.ini")
	if err := os.MkdirAll(ownScan, 0o755); err != nil {
		t.Fatal(err)
	}
	const xdebugContent = "zend_extension=/opt/homebrew/lib/php/xdebug.so\n"
	if err := os.WriteFile(loadedFile, []byte("memory_limit=128M\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xdebugIni, []byte(xdebugContent), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newFakeReleaseServer(t, fakePHPLoadingConfig("8.3.7", loadedFile, ownScan, xdebugIni))
	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)

	var out bytes.Buffer
	if err := InstallInterpreter(context.Background(), Options{
		Scope: platform.User, AssumeYes: true, Out: &out, Client: client, Env: &env,
	}); err != nil {
		t.Fatalf("InstallInterpreter: %v\n%s", err, out.String())
	}

	// The incompatible xdebug loader must be disabled in the interpreter's own config.
	if got, _ := os.ReadFile(xdebugIni); strings.Contains(string(got), "zend_extension") {
		t.Errorf("own xdebug.ini loader should be stripped, got %q", got)
	}
	// ...and recorded as a restorable backup, not a deletable copied file.
	root := filepath.Join(home, ".local", "share", "php-debugger")
	m, err := manifest.Load(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	it, _ := m.Interpreter("8.3")
	if len(it.ConfigFiles) != 0 {
		t.Errorf("own config must not be recorded as copied ConfigFiles, got %v", it.ConfigFiles)
	}
	if len(it.ConfigBackups) == 0 {
		t.Fatal("sanitized own config must be recorded as a ConfigBackup for restore")
	}

	// Uninstall restores the original config, leaving the directory intact.
	if err := Uninstall(context.Background(), Options{Scope: platform.User, Out: &bytes.Buffer{}, Env: &env},
		"", false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if got, _ := os.ReadFile(xdebugIni); string(got) != xdebugContent {
		t.Errorf("uninstall did not restore xdebug.ini: got %q, want %q", got, xdebugContent)
	}
}

// End-to-end for the reported flow: a Homebrew php 8.5 with xdebug is on PATH;
// `install -p 8.3` replaces it, then `switch 8.5` installs 8.5 whose compiled-in
// config path is the Homebrew 8.5 config (xdebug there gets disabled in place).
// Uninstalling must then restore everything: uninstalling active 8.5 brings its
// xdebug config back and falls to 8.3; uninstalling 8.3 restores the original
// Homebrew php. This is the specific concern — does the new own-config handling
// uninstall correctly across multiple interpreters.
func TestInstallSwitchUninstallLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()

	// Homebrew 8.5 config (shared by the real php and, later, our 8.5 interpreter).
	hb85 := t.TempDir()
	hb85Scan := filepath.Join(hb85, "conf.d")
	hb85Php := filepath.Join(hb85, "php.ini")
	hb85Xdebug := filepath.Join(hb85Scan, "xdebug.ini")
	if err := os.MkdirAll(hb85Scan, 0o755); err != nil {
		t.Fatal(err)
	}
	const xdebugContent = "zend_extension=/opt/homebrew/lib/php/xdebug.so\n"
	if err := os.WriteFile(hb85Php, []byte("memory_limit=256M\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hb85Xdebug, []byte(xdebugContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// The 8.3 interpreter reports its own (separate) config path.
	new83 := filepath.Join(t.TempDir(), "etc83")
	new83Scan := filepath.Join(new83, "conf.d")

	// Release serves both an 8.3 and an 8.5 interpreter asset. The 8.5 fake reports
	// the Homebrew 8.5 config as its own compiled-in config (the real collision).
	php83 := fakePHP("8.3.7", true, new83, new83Scan)
	php85 := fakePHPLoadingConfig("8.5.1", hb85Php, hb85Scan, hb85Xdebug)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/php-debugger/php-debugger/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"9.9.9","assets":[
				{"name":"php-php8.3-nts-linux-x86_64","browser_download_url":%q},
				{"name":"php-php8.5-nts-linux-x86_64","browser_download_url":%q}
			]}`, srv.URL+"/dl/83", srv.URL+"/dl/85")
		case "/dl/83":
			w.Write([]byte(php83))
		case "/dl/85":
			w.Write([]byte(php85))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	// The Homebrew php 8.5 on PATH (with xdebug), to be replaced.
	existBin := filepath.Join(t.TempDir(), "bin")
	existPhp := filepath.Join(existBin, "php")
	writeExec(t, existPhp, existingPHPScript("8.5.1", hb85Php, hb85Scan, hb85Xdebug))
	t.Setenv("PATH", existBin)

	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)
	base := func() Options {
		return Options{Scope: platform.User, AssumeYes: true, Out: &bytes.Buffer{}, Client: client, Env: &env}
	}
	root := filepath.Join(home, ".local", "share", "php-debugger")
	manifestPath := filepath.Join(root, "manifest.json")

	// 1. install -p 8.3 (replaces Homebrew 8.5). 8.5's own config is untouched here.
	o := base()
	o.PHPVersion = "8.3"
	if err := InstallInterpreter(context.Background(), o); err != nil {
		t.Fatalf("install 8.3: %v", err)
	}
	if got, _ := os.ReadFile(hb85Xdebug); string(got) != xdebugContent {
		t.Fatalf("installing 8.3 must not touch the 8.5 config, got %q", got)
	}

	// 2. switch 8.5 (installs it; our 8.3 is active, so existing == nil). This must
	//    disable the incompatible xdebug in the shared 8.5 config, backed up.
	o = base()
	o.PHPVersion = "8.5"
	if err := Switch(context.Background(), o); err != nil {
		t.Fatalf("switch 8.5: %v", err)
	}
	if got, _ := os.ReadFile(hb85Xdebug); strings.Contains(string(got), "zend_extension") {
		t.Fatalf("switch 8.5 should disable xdebug in the shared config, got %q", got)
	}
	m, _ := manifest.Load(manifestPath)
	if m.Active() != "8.5" {
		t.Fatalf("active = %q, want 8.5", m.Active())
	}
	if it, _ := m.Interpreter("8.5"); len(it.ConfigBackups) == 0 {
		t.Fatalf("8.5 must record a ConfigBackup for its in-place edit")
	}

	// 3. uninstall active 8.5 -> restores its xdebug config, falls back to 8.3.
	if err := Uninstall(context.Background(), base(), "8.5", false); err != nil {
		t.Fatalf("uninstall 8.5: %v", err)
	}
	if got, _ := os.ReadFile(hb85Xdebug); string(got) != xdebugContent {
		t.Errorf("uninstall 8.5 did not restore its xdebug config: got %q", got)
	}
	m, _ = manifest.Load(manifestPath)
	if m.Active() != "8.3" {
		t.Errorf("after removing 8.5, active = %q, want 8.3", m.Active())
	}
	if _, err := os.Stat(filepath.Join(root, "8.3", "bin", "php")); err != nil {
		t.Errorf("8.3 should still be installed: %v", err)
	}

	// 4. uninstall 8.3 (last one) -> restores the original Homebrew php.
	if err := Uninstall(context.Background(), base(), "8.3", false); err != nil {
		t.Fatalf("uninstall 8.3: %v", err)
	}
	// The original Homebrew php is back at its path and reports its version (i.e.
	// the real interpreter, not a dangling symlink into our removed root).
	if v, err := exec.Command(existPhp, "-v").CombinedOutput(); err != nil || !strings.Contains(string(v), "PHP 8.5.1") {
		t.Errorf("original Homebrew php not restored/runnable: out=%q err=%v", v, err)
	}
	// The Homebrew 8.5 config remains intact (xdebug still enabled for it).
	if got, _ := os.ReadFile(hb85Xdebug); string(got) != xdebugContent {
		t.Errorf("Homebrew 8.5 config should still have xdebug, got %q", got)
	}
	// Full uninstall removed our install root.
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("install root should be gone after full uninstall, err=%v", err)
	}
}

// Regression: an update reinstalls over our own already-active interpreter, so no
// foreign php is detected and copyConfig is skipped. The in-place config backup
// captured at first install must be carried forward (preserveConfig), or a later
// uninstall could no longer restore the user's xdebug.
func TestUpdateInterpreterPreservesSharedConfigBackup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	sharedCfg := t.TempDir()
	sharedScan := filepath.Join(sharedCfg, "conf.d")
	loadedFile := filepath.Join(sharedCfg, "php.ini")
	xdebugIni := filepath.Join(sharedScan, "xdebug.ini")

	existBin := filepath.Join(t.TempDir(), "bin")
	writeExec(t, filepath.Join(existBin, "php"),
		existingPHPScript("8.3.4", loadedFile, sharedScan, xdebugIni))
	if err := os.MkdirAll(sharedScan, 0o755); err != nil {
		t.Fatal(err)
	}
	const xdebugContent = "zend_extension=/opt/homebrew/lib/php/xdebug.so\n"
	if err := os.WriteFile(loadedFile, []byte("memory_limit=128M\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xdebugIni, []byte(xdebugContent), 0o644); err != nil {
		t.Fatal(err)
	}
	// After install our symlink replaces the existing php here, so update finds it.
	t.Setenv("PATH", existBin)

	ms := newMutableServer(t, "1.0.0", fakePHP("8.3.7", true, sharedCfg, sharedScan))
	client := release.NewClient()
	client.BaseURL = ms.URL
	env := linuxUserEnv(home)
	opts := Options{Scope: platform.User, AssumeYes: true, Out: &bytes.Buffer{}, Client: client, Env: &env, PHPVersion: "8.3"}

	if err := InstallInterpreter(context.Background(), opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	manifestPath := filepath.Join(home, ".local", "share", "php-debugger", "manifest.json")
	m, _ := manifest.Load(manifestPath)
	if it, _ := m.Interpreter("8.3"); len(it.ConfigBackups) == 0 {
		t.Fatalf("install should record a ConfigBackup, got %+v", it)
	}

	// A newer release appears; update the interpreter.
	ms.set("2.0.0", fakePHP("8.3.9", true, sharedCfg, sharedScan))
	var out bytes.Buffer
	uo := opts
	uo.PHPVersion = ""
	uo.Out = &out
	if err := Update(context.Background(), uo); err != nil {
		t.Fatalf("update: %v\n%s", err, out.String())
	}

	m, _ = manifest.Load(manifestPath)
	it, _ := m.Interpreter("8.3")
	if it.ReleaseTag != "2.0.0" {
		t.Fatalf("interpreter not updated: %+v", it)
	}
	if len(it.ConfigBackups) == 0 {
		t.Fatal("update lost the shared-config backup; uninstall could no longer restore xdebug")
	}

	// Uninstall after update must still restore the user's original xdebug config.
	if err := Uninstall(context.Background(), Options{Scope: platform.User, Out: &bytes.Buffer{}, Env: &env},
		"", false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if got, _ := os.ReadFile(xdebugIni); string(got) != xdebugContent {
		t.Errorf("xdebug not restored after update+uninstall: got %q, want %q", got, xdebugContent)
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
	// Exactly one backup: preserveDestination runs after the replace but must skip
	// the already-moved detected interpreter rather than double-recording it.
	if len(m.Backups) != 1 {
		t.Errorf("expected exactly one backup, got %d: %v", len(m.Backups), m.Backups)
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

// When the new interpreter has the same PHP series and thread-safety as the one
// it replaces, foreign extensions built for that ABI still load, so their loaders
// are left intact (only xdebug is stripped).
func TestInstallInterpreterSameABIKeepsLoaders(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()

	// New interpreter is 8.3 NTS (see newFakeReleaseServer's asset name).
	newCfg := filepath.Join(t.TempDir(), "newcfg")
	newScan := filepath.Join(newCfg, "conf.d")
	srv := newFakeReleaseServer(t, fakePHP("8.3.12", true, newCfg, newScan))

	// Pre-existing php is the SAME series (8.3) and NTS.
	existBin := filepath.Join(t.TempDir(), "bin")
	existIni := t.TempDir()
	existConfd := filepath.Join(existIni, "conf.d")
	writeExec(t, filepath.Join(existBin, "php"), existingPHPScript("8.3.4",
		filepath.Join(existIni, "php.ini"), existConfd, ""))
	if err := os.WriteFile(filepath.Join(existIni, "php.ini"),
		[]byte("zend_extension=xdebug.so\nextension=mysqli.so\nmemory_limit=128M\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(existConfd, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", existBin)

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

	mainIni, err := os.ReadFile(filepath.Join(newCfg, "php.ini"))
	if err != nil {
		t.Fatalf("main php.ini not copied: %v", err)
	}
	s := string(mainIni)
	// xdebug is always stripped.
	if strings.Contains(s, "xdebug.so") {
		t.Errorf("xdebug loader not stripped:\n%s", s)
	}
	// The mysqli loader is left active (not commented), because it loads fine.
	if !strings.Contains(s, "\nextension=mysqli.so") && !strings.HasPrefix(s, "extension=mysqli.so") {
		t.Errorf("same-ABI: mysqli loader should stay active, not be commented:\n%s", s)
	}
	if strings.Contains(s, "; extension=mysqli.so") {
		t.Errorf("same-ABI: mysqli loader should not be commented:\n%s", s)
	}
}

// When our own interpreter for the same version (series + threading) is already
// active with the debugger, installing again is a no-op.
func TestInstallInterpreterAlreadyProvided(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	root := filepath.Join(home, ".local", "share", "php-debugger")

	// A fake php that reports the debugger, installed *under our root* (8.3 NTS,
	// matching what newFakeReleaseServer offers).
	ownBin := filepath.Join(root, "8.3", "bin")
	writeExec(t, filepath.Join(ownBin, "php"), fakePHP("8.3.7", true, "", ""))
	t.Setenv("PATH", ownBin)

	srv := newFakeReleaseServer(t, fakePHP("8.3.7", true, "", ""))
	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)

	var out bytes.Buffer
	err := InstallInterpreter(context.Background(), Options{
		Scope: platform.User, Out: &out, Client: client, Env: &env,
	})
	if err != nil {
		t.Fatalf("expected a no-op, got: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "nothing to do") {
		t.Errorf("expected 'nothing to do' message, got: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(root, "manifest.json")); !os.IsNotExist(err) {
		t.Error("no manifest should be written for a no-op install")
	}
}

// When a normal php already loads our standalone extension, installing the
// interpreter proceeds but the copied config disables that extension loader — the
// interpreter has the debugger built in, so the .so must not also load. Other
// same-ABI loaders stay active.
func TestInstallInterpreterDisablesExistingDebuggerExtension(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()

	newCfg := filepath.Join(t.TempDir(), "newcfg")
	newScan := filepath.Join(newCfg, "conf.d")
	srv := newFakeReleaseServer(t, fakePHP("8.3.12", true, newCfg, newScan))

	// Pre-existing php is the SAME series (8.3 NTS) and its ini loads our
	// extension plus an unrelated one.
	existBin := filepath.Join(t.TempDir(), "bin")
	existIni := t.TempDir()
	existConfd := filepath.Join(existIni, "conf.d")
	writeExec(t, filepath.Join(existBin, "php"), existingPHPScript("8.3.4",
		filepath.Join(existIni, "php.ini"), existConfd, ""))
	soLoader := "zend_extension=/usr/lib/php/ext/php-debugger-php8.3-nts-linux-x86_64.so"
	if err := os.WriteFile(filepath.Join(existIni, "php.ini"),
		[]byte(soLoader+"\nextension=mysqli.so\nmemory_limit=128M\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(existConfd, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", existBin)

	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)

	var out bytes.Buffer
	err := InstallInterpreter(context.Background(), Options{
		Scope: platform.User, AssumeYes: true, Out: &out, Client: client, Env: &env,
	})
	if err != nil {
		t.Fatalf("InstallInterpreter: %v\n%s", err, out.String())
	}

	mainIni, err := os.ReadFile(filepath.Join(newCfg, "php.ini"))
	if err != nil {
		t.Fatalf("main php.ini not copied: %v", err)
	}
	s := string(mainIni)
	// The php-debugger extension loader must be stripped entirely.
	if strings.Contains(s, soLoader) {
		t.Errorf("debugger extension loader should be removed:\n%s", s)
	}
	// Same ABI: the unrelated mysqli loader stays active.
	if strings.Contains(s, "; extension=mysqli.so") {
		t.Errorf("same-ABI: mysqli loader should stay active:\n%s", s)
	}
	if !strings.Contains(s, "\nextension=mysqli.so") && !strings.HasPrefix(s, "extension=mysqli.so") {
		t.Errorf("same-ABI: mysqli loader should stay present and active:\n%s", s)
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
