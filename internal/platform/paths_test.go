package platform

import (
	"path/filepath"
	"testing"
)

// fakeEnv builds an Env with a controllable environment map.
func fakeEnv(osID OS, arch Arch, home string, vars map[string]string) Env {
	return Env{
		OS:   osID,
		Arch: arch,
		Home: home,
		Getenv: func(k string) string {
			return vars[k]
		},
	}
}

func TestResolveRoot(t *testing.T) {
	const home = "/home/jane"
	tests := []struct {
		name  string
		env   Env
		scope Scope
		want  string
	}{
		{
			name:  "linux system uses /usr/local",
			env:   fakeEnv(Linux, X8664, home, nil),
			scope: System,
			want:  filepath.Join("/usr/local", AppName),
		},
		{
			name:  "linux arm64 system uses /usr/local",
			env:   fakeEnv(Linux, Arm64, home, nil),
			scope: System,
			want:  filepath.Join("/usr/local", AppName),
		},
		{
			name:  "macos arm64 system uses /opt",
			env:   fakeEnv(MacOS, Arm64, home, nil),
			scope: System,
			want:  filepath.Join("/opt", AppName),
		},
		{
			name:  "macos intel system uses /usr/local",
			env:   fakeEnv(MacOS, X8664, home, nil),
			scope: System,
			want:  filepath.Join("/usr/local", AppName),
		},
		{
			name:  "windows system default ProgramFiles",
			env:   fakeEnv(Windows, X8664, home, nil),
			scope: System,
			want:  filepath.Join(`C:\Program Files`, AppName),
		},
		{
			name:  "windows system custom ProgramFiles",
			env:   fakeEnv(Windows, X8664, home, map[string]string{"ProgramFiles": `D:\Apps`}),
			scope: System,
			want:  filepath.Join(`D:\Apps`, AppName),
		},
		{
			name:  "linux user default XDG",
			env:   fakeEnv(Linux, X8664, home, nil),
			scope: User,
			want:  filepath.Join(home, ".local", "share", AppName),
		},
		{
			name:  "linux user XDG override",
			env:   fakeEnv(Linux, X8664, home, map[string]string{"XDG_DATA_HOME": "/data/xdg"}),
			scope: User,
			want:  filepath.Join("/data/xdg", AppName),
		},
		{
			name:  "macos user Library",
			env:   fakeEnv(MacOS, Arm64, home, nil),
			scope: User,
			want:  filepath.Join(home, "Library", "Application Support", AppName),
		},
		{
			name:  "windows user LOCALAPPDATA",
			env:   fakeEnv(Windows, X8664, home, map[string]string{"LOCALAPPDATA": `C:\Users\jane\AppData\Local`}),
			scope: User,
			want:  filepath.Join(`C:\Users\jane\AppData\Local`, AppName),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, err := Resolve(tt.env, tt.scope)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if l.Root != tt.want {
				t.Errorf("Root = %q, want %q", l.Root, tt.want)
			}
		})
	}
}

func TestResolveBinCandidates(t *testing.T) {
	const home = "/home/jane"
	tests := []struct {
		name  string
		env   Env
		scope Scope
		want  []string
	}{
		{
			name:  "macos arm64 system prefers homebrew",
			env:   fakeEnv(MacOS, Arm64, home, nil),
			scope: System,
			want:  []string{"/opt/homebrew/bin", "/usr/local/bin"},
		},
		{
			name:  "macos intel system",
			env:   fakeEnv(MacOS, X8664, home, nil),
			scope: System,
			want:  []string{"/usr/local/bin"},
		},
		{
			name:  "linux system",
			env:   fakeEnv(Linux, Arm64, home, nil),
			scope: System,
			want:  []string{"/usr/local/bin"},
		},
		{
			name:  "linux user local bin",
			env:   fakeEnv(Linux, X8664, home, nil),
			scope: User,
			want:  []string{filepath.Join(home, ".local", "bin")},
		},
		{
			name:  "macos user local bin",
			env:   fakeEnv(MacOS, Arm64, home, nil),
			scope: User,
			want:  []string{filepath.Join(home, ".local", "bin")},
		},
		{
			name:  "windows system bin under root",
			env:   fakeEnv(Windows, X8664, home, nil),
			scope: System,
			want:  []string{filepath.Join(`C:\Program Files`, AppName, "bin")},
		},
		{
			name:  "windows user bin under root",
			env:   fakeEnv(Windows, X8664, home, map[string]string{"LOCALAPPDATA": `C:\LA`}),
			scope: User,
			want:  []string{filepath.Join(`C:\LA`, AppName, "bin")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, err := Resolve(tt.env, tt.scope)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !equalStrings(l.BinCandidates, tt.want) {
				t.Errorf("BinCandidates = %v, want %v", l.BinCandidates, tt.want)
			}
		})
	}
}

func TestResolveErrors(t *testing.T) {
	if _, err := Resolve(Env{OS: "", Arch: X8664}, System); err == nil {
		t.Error("Resolve with empty OS: expected error")
	}
	if _, err := Resolve(Env{OS: Linux, Arch: X8664, Home: ""}, User); err == nil {
		t.Error("Resolve user scope without home: expected error")
	}
	// System scope does not require a home directory.
	if _, err := Resolve(Env{OS: Linux, Arch: X8664, Home: ""}, System); err != nil {
		t.Errorf("Resolve system scope without home: unexpected error: %v", err)
	}
}

func TestLayoutDerivedPaths(t *testing.T) {
	l, err := Resolve(fakeEnv(Linux, X8664, "/home/jane", nil), User)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wantRoot := filepath.Join("/home/jane", ".local", "share", AppName)
	if got := l.ManifestPath(); got != filepath.Join(wantRoot, "manifest.json") {
		t.Errorf("ManifestPath = %q", got)
	}
	if got := l.BackupsDir(); got != filepath.Join(wantRoot, "backups") {
		t.Errorf("BackupsDir = %q", got)
	}
	if got := l.VersionDir("8.3", false); got != filepath.Join(wantRoot, "8.3") {
		t.Errorf("VersionDir NTS = %q", got)
	}
	if got := l.VersionDir("8.3", true); got != filepath.Join(wantRoot, "8.3-zts") {
		t.Errorf("VersionDir ZTS = %q", got)
	}
}

func TestVersionDirName(t *testing.T) {
	if got := VersionDirName("8.2", false); got != "8.2" {
		t.Errorf("NTS = %q, want 8.2", got)
	}
	if got := VersionDirName("8.2", true); got != "8.2-zts" {
		t.Errorf("ZTS = %q, want 8.2-zts", got)
	}
}

func TestScopeFromUserFlag(t *testing.T) {
	if ScopeFromUserFlag(true) != User {
		t.Error("ScopeFromUserFlag(true) should be User")
	}
	if ScopeFromUserFlag(false) != System {
		t.Error("ScopeFromUserFlag(false) should be System")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
