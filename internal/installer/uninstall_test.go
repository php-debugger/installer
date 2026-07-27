package installer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/php-debugger/installer/internal/manifest"
	"github.com/php-debugger/installer/internal/platform"
	"github.com/php-debugger/installer/internal/release"
)

func TestUninstallInterpreterRestoresBackup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	newCfg := filepath.Join(t.TempDir(), "newcfg")
	srv := newFakeReleaseServer(t, fakePHP("8.3.7", true, newCfg, filepath.Join(newCfg, "conf.d")))

	// A pre-existing php that will be replaced (and backed up).
	existBin := filepath.Join(t.TempDir(), "bin")
	existPhp := filepath.Join(existBin, "php")
	existIni := t.TempDir()
	writeExec(t, existPhp, existingPHPScript("8.2.9", filepath.Join(existIni, "php.ini"),
		filepath.Join(existIni, "conf.d"), ""))
	if err := os.WriteFile(filepath.Join(existIni, "php.ini"), []byte("memory_limit=64M\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	origContent, _ := os.ReadFile(existPhp)
	t.Setenv("PATH", existBin)

	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)

	// Install (replaces existing, records a backup).
	if err := InstallInterpreter(context.Background(), Options{
		Scope: platform.User, AssumeYes: true, Out: &bytes.Buffer{}, Client: client, Env: &env,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	root := filepath.Join(home, ".local", "share", "php-debugger")

	// Sanity: our symlink took over.
	if isLink, _ := platform.IsSymlink(existPhp); !isLink {
		t.Fatal("expected our symlink at the existing php location")
	}

	// Uninstall the active interpreter.
	var out bytes.Buffer
	if err := Uninstall(context.Background(), Options{
		Scope: platform.User, Out: &out, Env: &env,
	}, false, false, "", false); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out.String())
	}

	// Original interpreter restored at its location.
	restored, err := os.ReadFile(existPhp)
	if err != nil {
		t.Fatalf("original not restored: %v", err)
	}
	if !bytes.Equal(restored, origContent) {
		t.Error("restored interpreter differs from original")
	}
	if isLink, _ := platform.IsSymlink(existPhp); isLink {
		t.Error("restored path should be a real file, not a symlink")
	}
	// Versioned dir and copied config removed.
	if _, err := os.Stat(filepath.Join(root, "8.3")); !os.IsNotExist(err) {
		t.Error("version dir should be removed")
	}
	if _, err := os.Stat(filepath.Join(newCfg, "php.ini")); !os.IsNotExist(err) {
		t.Error("copied config should be removed")
	}
	// Manifest cleared.
	m, _ := manifest.Load(filepath.Join(root, "manifest.json"))
	if _, ok := m.Interpreter("8.3"); ok {
		t.Error("interpreter should be gone from manifest")
	}
	if _, _, ok := anyBackup(m); ok {
		t.Error("backup should be consumed after restore")
	}
}

// Regression: if reassigning the active php (here, restoring a backup) fails, the
// uninstall must not have already deleted the interpreter's files — the manifest
// must still point at an interpreter directory that still exists, so the state is
// recoverable rather than dangling.
func TestUninstallInterpreterKeepsFilesWhenReassignFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	srv := newFakeReleaseServer(t, fakePHP("8.3.7", true, "", ""))

	// A pre-existing php so the install records a restorable backup.
	existBin := filepath.Join(t.TempDir(), "bin")
	existPhp := filepath.Join(existBin, "php")
	existIni := t.TempDir()
	writeExec(t, existPhp, existingPHPScript("8.2.9", filepath.Join(existIni, "php.ini"),
		filepath.Join(existIni, "conf.d"), ""))
	if err := os.WriteFile(filepath.Join(existIni, "php.ini"), []byte("memory_limit=64M\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", existBin)

	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)

	if err := InstallInterpreter(context.Background(), Options{
		Scope: platform.User, AssumeYes: true, Out: &bytes.Buffer{}, Client: client, Env: &env,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	root := filepath.Join(home, ".local", "share", "php-debugger")
	manifestPath := filepath.Join(root, "manifest.json")
	versionDir := filepath.Join(root, "8.3")

	// Sabotage the recorded backup so restoreBackup fails: point OriginalPath under
	// a regular file, so MkdirAll of its parent errors out.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ := manifest.Load(manifestPath)
	b, _ := m.Backup("8.3")
	b.OriginalPath = filepath.Join(blocker, "php") // parent is a file -> unrestoreable
	m.SetBackup("8.3", b)
	if err := m.Save(manifestPath); err != nil {
		t.Fatal(err)
	}

	// Uninstalling the active (only) variant must attempt the backup restore, fail,
	// and leave everything intact.
	err := Uninstall(context.Background(), Options{Scope: platform.User, Out: &bytes.Buffer{}, Env: &env},
		false, false, "", false)
	if err == nil {
		t.Fatal("expected uninstall to fail when backup restore fails")
	}

	// The version dir must NOT have been deleted...
	if _, statErr := os.Stat(versionDir); statErr != nil {
		t.Errorf("version dir should survive a failed reassignment: %v", statErr)
	}
	// ...and the on-disk manifest must still reference the interpreter.
	m2, _ := manifest.Load(manifestPath)
	if _, ok := m2.Interpreter("8.3"); !ok {
		t.Error("manifest should still list the interpreter after a failed uninstall")
	}
	// ...and, crucially, the active `php` must still exist and point at the
	// still-installed interpreter (a failed restore must not leave the user with no php).
	link := filepath.Join(existBin, "php")
	if _, statErr := os.Lstat(link); statErr != nil {
		t.Errorf("active php should still exist after a failed uninstall: %v", statErr)
	}
	binTarget := filepath.Join(versionDir, "bin", "php")
	if tgt, _ := os.Readlink(link); tgt != binTarget {
		t.Errorf("active php -> %q, want %q", tgt, binTarget)
	}
	// And it should still run and report the debugger.
	if has, err := phpHasDebugger(link); err != nil || !has {
		t.Errorf("restored active php should run and report the debugger: has=%v err=%v", has, err)
	}
}

// Uninstalling the active interpreter must restore every displaced backup, not
// just one — a Windows active slot can have both a php.exe and a php.cmd backed up.
func TestUninstallRestoresMultipleBackups(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	root := filepath.Join(home, ".local", "share", "php-debugger")
	versionDir := filepath.Join(root, "8.3")
	writeExec(t, filepath.Join(versionDir, "bin", "php"), fakePHP("8.3.7", true, "", ""))

	backupsDir := filepath.Join(root, "backups")
	if err := os.MkdirAll(backupsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	b1 := filepath.Join(backupsDir, "php-8.3-exe")
	b2 := filepath.Join(backupsDir, "php-8.3-cmd")
	if err := os.WriteFile(b1, []byte("EXE-ORIG"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b2, []byte("CMD-ORIG"), 0o755); err != nil {
		t.Fatal(err)
	}

	origDir := filepath.Join(t.TempDir(), "bin")
	orig1 := filepath.Join(origDir, "php.exe")
	orig2 := filepath.Join(origDir, "php.cmd")

	m := manifest.New(root, origDir)
	m.SetInterpreter("8.3", manifest.Interpreter{Series: "8.3", PHPVersion: "8.3.7", Dir: versionDir})
	m.SetActive("8.3")
	m.SetBackup("8.3", manifest.Backup{OriginalPath: orig1, BackupPath: b1})
	m.SetBackup("8.3#1", manifest.Backup{OriginalPath: orig2, BackupPath: b2})
	if err := m.Save(filepath.Join(root, "manifest.json")); err != nil {
		t.Fatal(err)
	}

	env := linuxUserEnv(home)
	if err := Uninstall(context.Background(), Options{Scope: platform.User, Out: &bytes.Buffer{}, Env: &env},
		true, false, "", false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	// Both displaced originals restored to their contents.
	if data, err := os.ReadFile(orig1); err != nil || string(data) != "EXE-ORIG" {
		t.Errorf("php.exe not restored: %q err=%v", data, err)
	}
	if data, err := os.ReadFile(orig2); err != nil || string(data) != "CMD-ORIG" {
		t.Errorf("php.cmd not restored: %q err=%v", data, err)
	}
	// Backups consumed and version dir removed.
	m2, _ := manifest.Load(filepath.Join(root, "manifest.json"))
	if len(m2.Backups) != 0 {
		t.Errorf("backups should be cleared after restore, got %v", m2.Backups)
	}
	if _, err := os.Stat(versionDir); !os.IsNotExist(err) {
		t.Error("version dir should be removed")
	}
}

// Regression: with two backups (Windows php.exe + php.cmd), if the first restore
// succeeds and a later one fails, the uninstall must stay recoverable — no backup
// file consumed, the on-disk manifest unchanged — so a retry completes cleanly.
func TestUninstallMultiBackupPartialFailureIsRetryable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	home := t.TempDir()
	root := filepath.Join(home, ".local", "share", "php-debugger")
	versionDir := filepath.Join(root, "8.3")
	writeExec(t, filepath.Join(versionDir, "bin", "php"), fakePHP("8.3.7", true, "", ""))

	backupsDir := filepath.Join(root, "backups")
	if err := os.MkdirAll(backupsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	b1 := filepath.Join(backupsDir, "php-8.3-exe")
	b2 := filepath.Join(backupsDir, "php-8.3-cmd")
	if err := os.WriteFile(b1, []byte("EXE-ORIG"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b2, []byte("CMD-ORIG"), 0o755); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	orig1 := filepath.Join(binDir, "php.exe") // 8.3   -> restoreable
	// 8.3#1's parent is a regular file, so restoring it fails until we fix it.
	blocker := filepath.Join(t.TempDir(), "blk")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig2 := filepath.Join(blocker, "php.cmd") // 8.3#1 -> unrestoreable (for now)

	m := manifest.New(root, binDir)
	m.SetInterpreter("8.3", manifest.Interpreter{Series: "8.3", PHPVersion: "8.3.7", Dir: versionDir})
	m.SetActive("8.3")
	m.SetBackup("8.3", manifest.Backup{OriginalPath: orig1, BackupPath: b1})
	m.SetBackup("8.3#1", manifest.Backup{OriginalPath: orig2, BackupPath: b2})
	if err := m.Save(filepath.Join(root, "manifest.json")); err != nil {
		t.Fatal(err)
	}

	env := linuxUserEnv(home)
	uOpts := Options{Scope: platform.User, Out: &bytes.Buffer{}, Env: &env}

	// First attempt: 8.3 restores, 8.3#1 fails.
	if err := Uninstall(context.Background(), uOpts, true, false, "", false); err == nil {
		t.Fatal("expected uninstall to fail on the sabotaged second restore")
	}

	// The first restore did happen (copy), proving we got past 8.3...
	if data, err := os.ReadFile(orig1); err != nil || string(data) != "EXE-ORIG" {
		t.Errorf("first backup should have been restored: %q err=%v", data, err)
	}
	// ...yet nothing was consumed: both backup files survive and the on-disk manifest
	// still references both backups and the interpreter, so a retry can complete.
	if _, err := os.Stat(b1); err != nil {
		t.Errorf("backup b1 must not be consumed on partial failure: %v", err)
	}
	if _, err := os.Stat(b2); err != nil {
		t.Errorf("backup b2 must survive: %v", err)
	}
	m2, _ := manifest.Load(filepath.Join(root, "manifest.json"))
	if _, ok := m2.Interpreter("8.3"); !ok {
		t.Error("manifest should still list the interpreter after a failed uninstall")
	}
	if len(m2.Backups) != 2 {
		t.Errorf("manifest should still reference both backups, got %v", m2.Backups)
	}
	if _, err := os.Stat(versionDir); err != nil {
		t.Errorf("version dir should survive a failed uninstall: %v", err)
	}

	// Remove the obstruction and retry: now it completes.
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(context.Background(), uOpts, true, false, "", false); err != nil {
		t.Fatalf("retry uninstall: %v", err)
	}
	if data, err := os.ReadFile(orig1); err != nil || string(data) != "EXE-ORIG" {
		t.Errorf("php.exe not restored on retry: %q err=%v", data, err)
	}
	if data, err := os.ReadFile(orig2); err != nil || string(data) != "CMD-ORIG" {
		t.Errorf("php.cmd not restored on retry: %q err=%v", data, err)
	}
	m3, _ := manifest.Load(filepath.Join(root, "manifest.json"))
	if len(m3.Backups) != 0 {
		t.Errorf("backups should be cleared after retry, got %v", m3.Backups)
	}
	if _, ok := m3.Interpreter("8.3"); ok {
		t.Error("interpreter should be gone after a successful retry")
	}
	if _, err := os.Stat(versionDir); !os.IsNotExist(err) {
		t.Error("version dir should be removed after a successful retry")
	}
	// Backup files are consumed only after the successful commit.
	if _, err := os.Stat(b1); !os.IsNotExist(err) {
		t.Error("b1 should be deleted after a successful uninstall")
	}
	if _, err := os.Stat(b2); !os.IsNotExist(err) {
		t.Error("b2 should be deleted after a successful uninstall")
	}
}

func TestUninstallActiveReassignsToOtherVariant(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	isolatePATH(t)
	home := t.TempDir()
	srv := newMultiVersionServer(t)
	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)

	// Install 8.3 then 8.4 (8.4 active) via switch.
	for _, v := range []string{"8.3", "8.4"} {
		o := Options{Scope: platform.User, Client: client, Env: &env, Out: &bytes.Buffer{}, PHPVersion: v}
		if err := Switch(context.Background(), o); err != nil {
			t.Fatalf("switch %s: %v", v, err)
		}
	}
	root := filepath.Join(home, ".local", "share", "php-debugger")
	link := filepath.Join(home, ".local", "bin", "php")

	// Uninstall the active 8.4 -> 8.3 should become active.
	var out bytes.Buffer
	if err := Uninstall(context.Background(), Options{Scope: platform.User, Out: &out, Env: &env},
		false, false, "", false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	m, _ := manifest.Load(filepath.Join(root, "manifest.json"))
	if m.Active() != "8.3" {
		t.Errorf("active = %q, want 8.3", m.Active())
	}
	tgt, _ := os.Readlink(link)
	if tgt != filepath.Join(root, "8.3", "bin", "php") {
		t.Errorf("symlink -> %q, want 8.3", tgt)
	}
	if _, err := os.Stat(filepath.Join(root, "8.4")); !os.IsNotExist(err) {
		t.Error("8.4 dir should be removed")
	}
}

func TestUninstallCleanHostRemovesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake php is a /bin/sh script")
	}
	isolatePATH(t)
	home := t.TempDir()
	srv := newFakeReleaseServer(t, fakePHP("8.3.7", true, "", ""))
	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)

	if err := InstallInterpreter(context.Background(), Options{
		Scope: platform.User, Out: &bytes.Buffer{}, Client: client, Env: &env, PHPVersion: "8.3",
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	link := filepath.Join(home, ".local", "bin", "php")

	if err := Uninstall(context.Background(), Options{Scope: platform.User, Out: &bytes.Buffer{}, Env: &env},
		false, false, "", false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Error("active symlink should be removed on clean-host uninstall")
	}
}

func TestUninstallExtension(t *testing.T) {
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
	writeExec(t, filepath.Join(binDir, "php"),
		fakeExistingPHPForExt("8.3.7", extDir, loadedFile, scanDir, true))
	t.Setenv("PATH", binDir)

	srv := newFakeReleaseServer(t, "unused")
	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)

	if err := InstallExtension(context.Background(), Options{
		Scope: platform.User, AssumeYes: true, Out: &bytes.Buffer{}, Client: client, Env: &env,
	}); err != nil {
		t.Fatalf("install extension: %v", err)
	}
	soDst := filepath.Join(extDir, "php-debugger-php8.3-nts-linux-x86_64.so")
	loader := filepath.Join(scanDir, "99-php-debugger.ini")

	// Uninstall the extension.
	if err := Uninstall(context.Background(), Options{Scope: platform.User, Out: &bytes.Buffer{}, Env: &env},
		false, false, "", false); err != nil {
		t.Fatalf("uninstall extension: %v", err)
	}
	if _, err := os.Stat(soDst); !os.IsNotExist(err) {
		t.Error(".so should be removed")
	}
	if _, err := os.Stat(loader); !os.IsNotExist(err) {
		t.Error("dedicated loader ini should be removed")
	}
	root := filepath.Join(home, ".local", "share", "php-debugger")
	m, _ := manifest.Load(filepath.Join(root, "manifest.json"))
	if m.Extension != nil {
		t.Error("extension should be cleared from manifest")
	}
}

// Installing the extension strips xdebug from the existing php's ini; uninstalling
// must restore it (the ini is the user's live file, not a copy).
func TestUninstallExtensionRestoresXdebug(t *testing.T) {
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

	srv := newFakeReleaseServer(t, "unused")
	client := release.NewClient()
	client.BaseURL = srv.URL
	env := linuxUserEnv(home)

	if err := InstallExtension(context.Background(), Options{
		Scope: platform.User, AssumeYes: true, Out: &bytes.Buffer{}, Client: client, Env: &env,
	}); err != nil {
		t.Fatalf("install extension: %v", err)
	}
	// After install, xdebug is gone and the mode is sanitized.
	if b, _ := os.ReadFile(loadedFile); strings.Contains(string(b), "xdebug.so") || strings.Contains(string(b), "develop") {
		t.Fatalf("install should have stripped xdebug:\n%s", b)
	}

	if err := Uninstall(context.Background(), Options{Scope: platform.User, Out: &bytes.Buffer{}, Env: &env},
		false, false, "", false); err != nil {
		t.Fatalf("uninstall extension: %v", err)
	}
	// After uninstall, the ini is byte-for-byte the user's original (xdebug back).
	got, err := os.ReadFile(loadedFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("ini not restored to original.\n got: %q\nwant: %q", got, original)
	}
}

func TestUninstallNothing(t *testing.T) {
	env := linuxUserEnv(t.TempDir())
	err := Uninstall(context.Background(), Options{Scope: platform.User, Env: &env},
		false, false, "", false)
	if err == nil || !strings.Contains(err.Error(), "nothing installed") {
		t.Errorf("expected 'nothing installed', got %v", err)
	}
}

func TestRemoveExtensionLoaderKeepsSharedIni(t *testing.T) {
	dir := t.TempDir()
	ini := filepath.Join(dir, "php.ini")
	so := "/ext/php-debugger.so"
	content := "memory_limit=256M\n; Enables the php-debugger extension (added by php-debugger)\nzend_extension=" + so + "\ndisplay_errors=On\n"
	if err := os.WriteFile(ini, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeExtensionLoader(ini, so); err != nil {
		t.Fatal(err)
	}
	// Shared ini kept, loader lines gone, other settings preserved.
	got, err := os.ReadFile(ini)
	if err != nil {
		t.Fatalf("shared ini should be kept: %v", err)
	}
	s := string(got)
	if strings.Contains(s, so) || strings.Contains(s, "Enables the php-debugger") {
		t.Errorf("loader lines not removed:\n%s", s)
	}
	if !strings.Contains(s, "memory_limit=256M") || !strings.Contains(s, "display_errors=On") {
		t.Errorf("other settings should be preserved:\n%s", s)
	}
}
