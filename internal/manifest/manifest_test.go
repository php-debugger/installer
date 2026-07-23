package manifest

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// fixed UTC times so JSON round-trips compare equal with reflect.DeepEqual.
var (
	t1 = time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	t2 = time.Date(2026, 7, 20, 8, 30, 0, 0, time.UTC)
)

func sampleManifest() *Manifest {
	m := New("/opt/php-debugger", "/usr/local/bin")
	m.SetInterpreter("8.3", Interpreter{
		Series: "8.3", PHPVersion: "8.3.10", ZTS: false,
		ReleaseTag: "0.1.0", Dir: "/opt/php-debugger/8.3", InstalledAt: t1,
	})
	m.SetInterpreter("8.2-zts", Interpreter{
		Series: "8.2", PHPVersion: "8.2.20", ZTS: true,
		ReleaseTag: "0.1.0", Dir: "/opt/php-debugger/8.2-zts", InstalledAt: t2,
	})
	m.SetActive("8.3")
	m.SetBackup("8.3", Backup{
		OriginalPath: "/usr/bin/php",
		BackupPath:   "/opt/php-debugger/backups/php-8.3",
		CreatedAt:    t1,
	})
	m.SetExtension(Extension{
		Series: "8.1", PHPVersion: "8.1.29", ZTS: false, ReleaseTag: "0.1.0",
		SoPath: "/usr/lib/php/ext/php-debugger.so", IniPath: "/etc/php/8.1/conf.d/99-debugger.ini",
		InstalledAt: t2,
	})
	return m
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	want := sampleManifest()

	if err := want.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got = %+v\nwant = %+v", got, want)
	}
}

func TestLoadMissingReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	m, err := Load(path)
	if err != nil {
		t.Fatalf("Load missing: unexpected error: %v", err)
	}
	if m.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", m.SchemaVersion, SchemaVersion)
	}
	if m.Interpreters == nil || m.Backups == nil {
		t.Error("maps should be initialized on empty manifest")
	}
	if len(m.Interpreters) != 0 || m.Active() != "" || m.Extension != nil {
		t.Error("empty manifest should have no entries")
	}
}

func TestSaveCreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "manifest.json")
	if err := New("/root", "/bin").Save(path); err != nil {
		t.Fatalf("Save into nested dir: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("manifest not written: %v", err)
	}
}

func TestSaveOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")

	m := New("/root", "/bin")
	m.SetInterpreter("8.3", Interpreter{Series: "8.3", InstalledAt: t1})
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}

	m.RemoveInterpreter("8.3")
	m.SetInterpreter("8.4", Interpreter{Series: "8.4", InstalledAt: t2})
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Interpreter("8.3"); ok {
		t.Error("8.3 should have been overwritten away")
	}
	if _, ok := got.Interpreter("8.4"); !ok {
		t.Error("8.4 should be present after overwrite")
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("Load of invalid JSON: expected error")
	}
}

func TestLoadRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion": 999}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("Load of newer schema: expected error")
	}
}

func TestInterpreterBookkeeping(t *testing.T) {
	m := New("/root", "/bin")

	if _, ok := m.Interpreter("8.3"); ok {
		t.Error("no interpreter expected yet")
	}
	m.SetInterpreter("8.3", Interpreter{Series: "8.3", PHPVersion: "8.3.10"})
	got, ok := m.Interpreter("8.3")
	if !ok || got.PHPVersion != "8.3.10" {
		t.Errorf("Interpreter(8.3) = %+v, %v", got, ok)
	}

	m.SetActive("8.3")
	if m.Active() != "8.3" {
		t.Errorf("Active = %q, want 8.3", m.Active())
	}

	// Removing the active interpreter clears the active pointer.
	m.RemoveInterpreter("8.3")
	if _, ok := m.Interpreter("8.3"); ok {
		t.Error("8.3 should be removed")
	}
	if m.Active() != "" {
		t.Errorf("Active should be cleared after removing active interpreter, got %q", m.Active())
	}
}

func TestRemoveNonActiveKeepsActive(t *testing.T) {
	m := New("/root", "/bin")
	m.SetInterpreter("8.3", Interpreter{Series: "8.3"})
	m.SetInterpreter("8.2", Interpreter{Series: "8.2"})
	m.SetActive("8.3")

	m.RemoveInterpreter("8.2")
	if m.Active() != "8.3" {
		t.Errorf("Active should remain 8.3, got %q", m.Active())
	}
}

func TestBackupBookkeeping(t *testing.T) {
	m := New("/root", "/bin")
	if _, ok := m.Backup("8.3"); ok {
		t.Error("no backup expected yet")
	}
	m.SetBackup("8.3", Backup{OriginalPath: "/usr/bin/php", BackupPath: "/b", CreatedAt: t1})
	b, ok := m.Backup("8.3")
	if !ok || b.OriginalPath != "/usr/bin/php" {
		t.Errorf("Backup(8.3) = %+v, %v", b, ok)
	}
	m.RemoveBackup("8.3")
	if _, ok := m.Backup("8.3"); ok {
		t.Error("backup should be removed")
	}
}

func TestExtensionBookkeeping(t *testing.T) {
	m := New("/root", "/bin")
	if m.Extension != nil {
		t.Error("no extension expected yet")
	}
	m.SetExtension(Extension{Series: "8.1", PHPVersion: "8.1.29"})
	if m.Extension == nil || m.Extension.Series != "8.1" {
		t.Errorf("Extension = %+v", m.Extension)
	}
	m.ClearExtension()
	if m.Extension != nil {
		t.Error("extension should be cleared")
	}
}

func TestInterpreterKeys(t *testing.T) {
	m := New("/root", "/bin")
	m.SetInterpreter("8.3", Interpreter{Series: "8.3"})
	m.SetInterpreter("8.2-zts", Interpreter{Series: "8.2", ZTS: true})
	keys := m.InterpreterKeys()
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %v", keys)
	}
	set := map[string]bool{keys[0]: true, keys[1]: true}
	if !set["8.3"] || !set["8.2-zts"] {
		t.Errorf("unexpected keys: %v", keys)
	}
}
