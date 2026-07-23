package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Scope selects where the installer places files.
type Scope int

const (
	// System is the default, system-wide scope (/opt on Linux/macOS,
	// Program Files on Windows). Writing to it generally requires elevation.
	System Scope = iota
	// User is the per-user scope under the user's home directory (no elevation).
	User
)

func (s Scope) String() string {
	switch s {
	case System:
		return "system"
	case User:
		return "user"
	default:
		return "unknown"
	}
}

// ScopeFromUserFlag maps the CLI --user flag to a Scope.
func ScopeFromUserFlag(user bool) Scope {
	if user {
		return User
	}
	return System
}

// Env captures every external input path resolution depends on, so that it can
// be faked in tests. Use CurrentEnv to populate it from the real host.
type Env struct {
	OS   OS
	Arch Arch
	// Home is the user's home directory.
	Home string
	// Getenv resolves environment variables. If nil, os.Getenv is used.
	Getenv func(string) string
}

func (e Env) getenv(key string) string {
	if e.Getenv != nil {
		return e.Getenv(key)
	}
	return os.Getenv(key)
}

// CurrentEnv builds an Env from the host's runtime, home directory and
// environment.
func CurrentEnv() (Env, error) {
	p, err := Detect()
	if err != nil {
		return Env{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Env{}, fmt.Errorf("determining home directory: %w", err)
	}
	return Env{OS: p.OS, Arch: p.Arch, Home: home, Getenv: os.Getenv}, nil
}

// Layout is the set of resolved locations for a given scope. BinCandidates is an
// ordered preference list of directories for the active `php` symlink; the first
// writable one is chosen (see SelectBinDir).
type Layout struct {
	Scope         Scope
	Root          string
	BinCandidates []string
}

// ManifestPath is the location of the installer's state file for this layout.
func (l Layout) ManifestPath() string {
	return filepath.Join(l.Root, "manifest.json")
}

// BackupsDir is where replaced interpreters are backed up for this layout.
func (l Layout) BackupsDir() string {
	return filepath.Join(l.Root, "backups")
}

// VersionDirName is the folder name for a given PHP version and threading model:
// "8.3" for NTS, "8.3-zts" for ZTS. The suffix lets both variants coexist.
func VersionDirName(version string, zts bool) string {
	if zts {
		return version + "-zts"
	}
	return version
}

// VersionDir is the absolute install directory for a PHP version/variant.
func (l Layout) VersionDir(version string, zts bool) string {
	return filepath.Join(l.Root, VersionDirName(version, zts))
}

// Resolve computes the Layout for the given environment and scope. It is pure:
// it performs no filesystem access and never fails on missing directories.
func Resolve(env Env, scope Scope) (Layout, error) {
	if env.OS == "" {
		return Layout{}, errors.New("platform.Resolve: empty OS")
	}
	if scope == User && env.Home == "" {
		return Layout{}, errors.New("platform.Resolve: user scope requires a home directory")
	}

	root := resolveRoot(env, scope)
	bins := resolveBinCandidates(env, scope, root)
	return Layout{Scope: scope, Root: root, BinCandidates: bins}, nil
}

func resolveRoot(env Env, scope Scope) string {
	if scope == System {
		switch env.OS {
		case Windows:
			return filepath.Join(programFiles(env), AppName)
		case MacOS:
			// Apple Silicon follows Homebrew's /opt prefix; Intel Macs kept
			// /usr/local, like Linux.
			if env.Arch == Arm64 {
				return filepath.Join("/opt", AppName)
			}
			return filepath.Join("/usr/local", AppName)
		default: // Linux and other Unix
			return filepath.Join("/usr/local", AppName)
		}
	}

	// User scope.
	switch env.OS {
	case MacOS:
		return filepath.Join(env.Home, "Library", "Application Support", AppName)
	case Windows:
		return filepath.Join(localAppData(env), AppName)
	default: // Linux and other Unix
		return filepath.Join(xdgDataHome(env), AppName)
	}
}

func resolveBinCandidates(env Env, scope Scope, root string) []string {
	if scope == System {
		switch env.OS {
		case MacOS:
			if env.Arch == Arm64 {
				// Homebrew's arm64 prefix first, then the Intel/standard location.
				return []string{"/opt/homebrew/bin", "/usr/local/bin"}
			}
			return []string{"/usr/local/bin"}
		case Linux:
			return []string{"/usr/local/bin"}
		case Windows:
			return []string{filepath.Join(root, "bin")}
		}
	}

	// User scope.
	switch env.OS {
	case Windows:
		return []string{filepath.Join(root, "bin")}
	default: // Linux, macOS
		return []string{filepath.Join(env.Home, ".local", "bin")}
	}
}

// xdgDataHome returns $XDG_DATA_HOME or the ~/.local/share default.
func xdgDataHome(env Env) string {
	if v := env.getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	return filepath.Join(env.Home, ".local", "share")
}

// localAppData returns %LOCALAPPDATA% or a sensible default under the home dir.
func localAppData(env Env) string {
	if v := env.getenv("LOCALAPPDATA"); v != "" {
		return v
	}
	return filepath.Join(env.Home, "AppData", "Local")
}

// programFiles returns %ProgramFiles% or the conventional default.
func programFiles(env Env) string {
	if v := env.getenv("ProgramFiles"); v != "" {
		return v
	}
	return `C:\Program Files`
}
