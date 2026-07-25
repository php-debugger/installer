package installer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/php-debugger/installer/internal/platform"
	"github.com/php-debugger/installer/internal/release"
)

func TestPruneEmptyConfigDirs(t *testing.T) {
	root := t.TempDir()
	php85 := filepath.Join(root, "php", "8.5")
	confd := filepath.Join(php85, "conf.d")
	if err := os.MkdirAll(confd, 0o755); err != nil {
		t.Fatal(err)
	}
	// Another version dir with content that must survive.
	php82 := filepath.Join(root, "php", "8.2")
	if err := os.MkdirAll(php82, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(php82, "php.ini"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Config files we "removed" already (they no longer exist); prune their dirs.
	files := []string{
		filepath.Join(php85, "php.ini"),
		filepath.Join(confd, "20-xdebug.ini"),
	}
	pruneEmptyConfigDirs(files)

	if _, err := os.Stat(confd); !os.IsNotExist(err) {
		t.Error("empty conf.d should be pruned")
	}
	if _, err := os.Stat(php85); !os.IsNotExist(err) {
		t.Error("empty 8.5 dir should be pruned")
	}
	// Shared parent and the other version must remain.
	if _, err := os.Stat(php82); err != nil {
		t.Error("non-empty 8.2 dir must be kept")
	}
	if _, err := os.Stat(filepath.Join(root, "php")); err != nil {
		t.Error("shared php dir with other content must be kept")
	}
}

func TestUninstallRemovesEmptyRoot(t *testing.T) {
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
	root := filepath.Join(home, ".local", "share", "php-debugger")
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("root should exist after install: %v", err)
	}

	if err := Uninstall(context.Background(), Options{Scope: platform.User, Out: &bytes.Buffer{}, Env: &env},
		false, false, "", false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Error("install root should be removed after full uninstall")
	}
}
