package platform

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ArchFromMachine maps a `uname -m`-style machine name — as reported by
// php_uname("m") — to a release architecture token. This is the architecture the
// PHP *process* runs as, which is what its matching extension must be built for:
// an Intel php running under Rosetta 2 on Apple Silicon reports "x86_64", so it
// needs the x86_64 extension, not the host's native arm64 one.
func ArchFromMachine(machine string) (Arch, error) {
	switch strings.ToLower(strings.TrimSpace(machine)) {
	case "x86_64", "amd64", "x64":
		return X8664, nil
	case "arm64", "aarch64":
		return Arm64, nil
	default:
		return "", fmt.Errorf("unrecognized machine architecture %q", machine)
	}
}

// detectNativeArch upgrades a detected architecture to the host's *native*
// architecture when they differ due to emulation. Concretely: an x86_64 build of
// this tool running on Apple Silicon (under Rosetta 2) reports GOARCH=amd64, but
// the machine is arm64 and should get arm64 PHP. sysctlInt is injected so the
// logic is unit-testable without a real macOS host.
func detectNativeArch(osID OS, arch Arch, sysctlInt func(name string) (int, bool)) Arch {
	if osID == MacOS && arch == X8664 {
		// hw.optional.arm64 == 1 means the hardware is Apple Silicon; combined
		// with an amd64 build this means we're running translated.
		if v, ok := sysctlInt("hw.optional.arm64"); ok && v == 1 {
			return Arm64
		}
	}
	return arch
}

// sysctlInt reads an integer sysctl value by name, returning ok=false if the key
// is absent or non-integer (e.g. on Intel Macs hw.optional.arm64 does not exist).
func sysctlInt(name string) (int, bool) {
	out, err := exec.Command("sysctl", "-n", name).Output()
	if err != nil {
		return 0, false
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, false
	}
	return v, true
}
