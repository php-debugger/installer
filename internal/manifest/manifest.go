// Package manifest defines the installer's on-disk state file and the
// bookkeeping operations over it. One manifest lives at the root of each install
// scope (see internal/platform) and records the installed interpreters, backups
// of any interpreters they replaced, the active interpreter, and the installed
// extension (if any).
//
// Variant keys (the map keys for interpreters/backups, and the value of
// ActiveInterpreter) are opaque strings supplied by the caller. The installer
// uses platform.VersionDirName (e.g. "8.3", "8.3-zts") so that each key maps 1:1
// to an on-disk version directory.
package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SchemaVersion is the current manifest schema version.
const SchemaVersion = 1

// Manifest is the root state object persisted as JSON.
type Manifest struct {
	SchemaVersion     int                    `json:"schemaVersion"`
	InstallRoot       string                 `json:"installRoot,omitempty"`
	BinDir            string                 `json:"binDir,omitempty"`
	ActiveInterpreter string                 `json:"activeInterpreter,omitempty"`
	Interpreters      map[string]Interpreter `json:"interpreters,omitempty"`
	Backups           map[string]Backup      `json:"backups,omitempty"`
	Extension         *Extension             `json:"extension,omitempty"`
}

// Interpreter records a single installed PHP interpreter variant.
type Interpreter struct {
	Series      string    `json:"series"`     // selected major.minor, e.g. "8.3"
	PHPVersion  string    `json:"phpVersion"` // full version reported, e.g. "8.3.10"
	ZTS         bool      `json:"zts"`
	ReleaseTag  string    `json:"releaseTag"`
	Dir         string    `json:"dir"` // absolute install directory
	InstalledAt time.Time `json:"installedAt"`
	// ConfigFiles are ini files this install wrote into the interpreter's
	// compiled-in config path (copied from a replaced interpreter), recorded so
	// uninstall can remove them.
	ConfigFiles []string `json:"configFiles,omitempty"`
}

// Backup records an interpreter that was replaced during an install, so it can
// be restored on uninstall.
type Backup struct {
	OriginalPath string    `json:"originalPath"` // where the replaced php lived
	BackupPath   string    `json:"backupPath"`   // where we saved a copy
	CreatedAt    time.Time `json:"createdAt"`
}

// Extension records an installed debugger extension (extension-only installs).
type Extension struct {
	Series      string    `json:"series"`     // php major.minor
	PHPVersion  string    `json:"phpVersion"` // full version of the target php
	ZTS         bool      `json:"zts"`
	ReleaseTag  string    `json:"releaseTag"`
	SoPath      string    `json:"soPath"`  // installed .so/.dll path
	IniPath     string    `json:"iniPath"` // ini file holding the loader line
	InstalledAt time.Time `json:"installedAt"`
}

// New returns an empty manifest for the given install root and bin directory.
func New(root, binDir string) *Manifest {
	m := newEmpty()
	m.InstallRoot = root
	m.BinDir = binDir
	return m
}

func newEmpty() *Manifest {
	return &Manifest{
		SchemaVersion: SchemaVersion,
		Interpreters:  map[string]Interpreter{},
		Backups:       map[string]Backup{},
	}
}

// Load reads a manifest from path. If the file does not exist, it returns a
// fresh empty manifest and no error (first run).
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newEmpty(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading manifest %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest %s: %w", path, err)
	}
	if m.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("manifest %s has schema version %d, newer than supported %d",
			path, m.SchemaVersion, SchemaVersion)
	}
	if m.SchemaVersion == 0 {
		m.SchemaVersion = SchemaVersion
	}
	m.ensureMaps()
	return &m, nil
}

// Save writes the manifest to path atomically (write temp, then rename),
// creating the parent directory if needed.
func (m *Manifest) Save(path string) error {
	m.SchemaVersion = SchemaVersion

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding manifest: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating manifest directory %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".manifest-*.json.tmp")
	if err != nil {
		return fmt.Errorf("creating temp manifest: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing temp manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("closing temp manifest: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("finalizing manifest %s: %w", path, err)
	}
	return nil
}

func (m *Manifest) ensureMaps() {
	if m.Interpreters == nil {
		m.Interpreters = map[string]Interpreter{}
	}
	if m.Backups == nil {
		m.Backups = map[string]Backup{}
	}
}

// --- Interpreter bookkeeping ---

// SetInterpreter records (or replaces) an interpreter variant under key.
func (m *Manifest) SetInterpreter(key string, it Interpreter) {
	m.ensureMaps()
	m.Interpreters[key] = it
}

// Interpreter returns the interpreter recorded under key, if any.
func (m *Manifest) Interpreter(key string) (Interpreter, bool) {
	it, ok := m.Interpreters[key]
	return it, ok
}

// RemoveInterpreter deletes the interpreter under key. If it was the active
// interpreter, the active pointer is cleared to avoid dangling references.
func (m *Manifest) RemoveInterpreter(key string) {
	delete(m.Interpreters, key)
	if m.ActiveInterpreter == key {
		m.ActiveInterpreter = ""
	}
}

// InterpreterKeys returns the recorded interpreter keys (unordered).
func (m *Manifest) InterpreterKeys() []string {
	keys := make([]string, 0, len(m.Interpreters))
	for k := range m.Interpreters {
		keys = append(keys, k)
	}
	return keys
}

// --- Active pointer ---

// SetActive sets the active interpreter key.
func (m *Manifest) SetActive(key string) { m.ActiveInterpreter = key }

// Active returns the active interpreter key ("" if none).
func (m *Manifest) Active() string { return m.ActiveInterpreter }

// --- Backup bookkeeping ---

// SetBackup records a backup of a replaced interpreter under key.
func (m *Manifest) SetBackup(key string, b Backup) {
	m.ensureMaps()
	m.Backups[key] = b
}

// Backup returns the backup recorded under key, if any.
func (m *Manifest) Backup(key string) (Backup, bool) {
	b, ok := m.Backups[key]
	return b, ok
}

// RemoveBackup deletes the backup record under key (e.g. after restoring it).
func (m *Manifest) RemoveBackup(key string) { delete(m.Backups, key) }

// --- Extension bookkeeping ---

// SetExtension records the installed debugger extension.
func (m *Manifest) SetExtension(e Extension) { m.Extension = &e }

// ClearExtension removes the recorded extension.
func (m *Manifest) ClearExtension() { m.Extension = nil }
