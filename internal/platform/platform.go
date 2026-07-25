// Package platform handles OS/architecture detection and resolves the
// filesystem locations the installer uses, for both the system-wide and
// per-user install scopes.
//
// The core logic is pure: OS/arch mapping and path resolution take their inputs
// (GOOS, GOARCH, home dir, environment) as parameters so they can be unit tested
// for any target platform from any host. Thin wrappers (Detect, CurrentEnv)
// supply the real runtime values.
package platform

import (
	"fmt"
	"runtime"
)

// OS is the operating-system token used in release asset names.
type OS string

const (
	Linux   OS = "linux"
	MacOS   OS = "macos"
	Windows OS = "windows"
)

// Arch is the CPU-architecture token used in release asset names.
type Arch string

const (
	X8664 Arch = "x86_64"
	Arm64 Arch = "arm64"
)

// AppName is the directory/binary name used throughout the installer.
const AppName = "php-debugger"

// Platform is a resolved OS/architecture pair.
type Platform struct {
	OS   OS
	Arch Arch
}

func (p Platform) String() string { return string(p.OS) + "/" + string(p.Arch) }

// DetectOS maps a Go GOOS value to a release OS token.
func DetectOS(goos string) (OS, error) {
	switch goos {
	case "linux":
		return Linux, nil
	case "darwin":
		return MacOS, nil
	case "windows":
		return Windows, nil
	default:
		return "", fmt.Errorf("unsupported operating system %q", goos)
	}
}

// DetectArch maps a Go GOARCH value to a release architecture token.
func DetectArch(goarch string) (Arch, error) {
	switch goarch {
	case "amd64":
		return X8664, nil
	case "arm64":
		return Arm64, nil
	default:
		return "", fmt.Errorf("unsupported architecture %q", goarch)
	}
}

// DetectFor resolves a Platform from explicit GOOS/GOARCH values (for testing).
func DetectFor(goos, goarch string) (Platform, error) {
	osID, err := DetectOS(goos)
	if err != nil {
		return Platform{}, err
	}
	arch, err := DetectArch(goarch)
	if err != nil {
		return Platform{}, err
	}
	return Platform{OS: osID, Arch: arch}, nil
}

// Detect resolves the Platform of the host the installer is running on. It
// corrects the architecture to the machine's native one when the tool is running
// emulated (an amd64 build on Apple Silicon), so we install native binaries.
func Detect() (Platform, error) {
	p, err := DetectFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return Platform{}, err
	}
	p.Arch = detectNativeArch(p.OS, p.Arch, sysctlInt)
	return p, nil
}
