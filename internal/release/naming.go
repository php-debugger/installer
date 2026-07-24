package release

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/php-debugger/installer/internal/platform"
)

// Kind distinguishes the two sorts of release asset the installer cares about.
type Kind int

const (
	// Interpreter is a self-contained php binary with the debugger compiled in.
	Interpreter Kind = iota
	// Extension is the debugger loadable extension (.so/.dll).
	Extension
)

func (k Kind) String() string {
	if k == Extension {
		return "extension"
	}
	return "interpreter"
}

// ParsedAsset is the structured interpretation of a release asset filename.
type ParsedAsset struct {
	Kind   Kind
	Series string // PHP series as it appears in the name, e.g. "8.3"
	ZTS    bool
	OS     platform.OS
	Arch   platform.Arch
}

// Selector describes the asset the caller wants. An empty Series matches any and
// selects the highest available.
type Selector struct {
	Kind   Kind
	Series string
	ZTS    bool
	OS     platform.OS
	Arch   platform.Arch
}

var seriesRe = regexp.MustCompile(`^\d+\.\d+(?:\.\d+)*$`)

// ParseAssetName interprets a release asset filename. It is deliberately
// tolerant: rather than matching an exact template, it detects the asset kind
// from the file suffix (.so/.dll = extension; .exe or none = interpreter) and
// extracts the PHP series, threading model, OS and architecture from the
// hyphen-separated tokens. This absorbs the fact that the two kinds use
// different prefixes. It returns ok=false for anything it does not recognize
// (checksums, source archives, …).
//
// Confirmed naming in release 0.1.0:
//   - interpreter: php-php{ver}-{nts|ts}-{os}-{arch}[.exe]
//   - extension:   php-debugger-php{ver}-{nts|ts}-{os}-{arch}.{so|dll}
//
// This function is the single place that encodes the release naming convention;
// adjust it here if the published names change.
func ParseAssetName(name string) (ParsedAsset, bool) {
	lower := strings.ToLower(name)

	kind := Interpreter
	base := name
	switch {
	case strings.HasSuffix(lower, ".so"):
		kind, base = Extension, name[:len(name)-len(".so")]
	case strings.HasSuffix(lower, ".dll"):
		kind, base = Extension, name[:len(name)-len(".dll")]
	case strings.HasSuffix(lower, ".exe"):
		kind, base = Interpreter, name[:len(name)-len(".exe")]
	}

	var (
		p                                     ParsedAsset
		haveVer, haveThread, haveOS, haveArch bool
	)
	p.Kind = kind

	for _, tok := range strings.Split(strings.ToLower(base), "-") {
		switch {
		case strings.HasPrefix(tok, "php") && seriesRe.MatchString(tok[len("php"):]):
			p.Series, haveVer = tok[len("php"):], true
		case tok == "ts":
			p.ZTS, haveThread = true, true
		case tok == "nts":
			p.ZTS, haveThread = false, true
		case tok == "linux":
			p.OS, haveOS = platform.Linux, true
		case tok == "macos":
			p.OS, haveOS = platform.MacOS, true
		case tok == "windows":
			p.OS, haveOS = platform.Windows, true
		case tok == "x86_64":
			p.Arch, haveArch = platform.X8664, true
		case tok == "arm64":
			p.Arch, haveArch = platform.Arm64, true
		}
	}

	if !(haveVer && haveThread && haveOS && haveArch) {
		return ParsedAsset{}, false
	}
	return p, true
}

// SelectAsset returns the asset matching sel. When sel.Series is empty it selects
// the highest available series among the matches. It returns a *NoMatchError
// (listing what is available) if nothing matches.
func SelectAsset(assets []Asset, sel Selector) (Asset, error) {
	type candidate struct {
		asset  Asset
		parsed ParsedAsset
	}
	var matches []candidate
	for _, a := range assets {
		p, ok := ParseAssetName(a.Name)
		if !ok || p.Kind != sel.Kind || p.OS != sel.OS || p.Arch != sel.Arch || p.ZTS != sel.ZTS {
			continue
		}
		if sel.Series != "" && p.Series != sel.Series {
			continue
		}
		matches = append(matches, candidate{a, p})
	}
	if len(matches) == 0 {
		return Asset{}, &NoMatchError{Selector: sel, Available: availableVariants(assets, sel.Kind)}
	}
	best := matches[0]
	for _, m := range matches[1:] {
		if compareSeries(m.parsed.Series, best.parsed.Series) > 0 {
			best = m
		}
	}
	return best.asset, nil
}

// LatestSeries returns the highest PHP series among assets matching the given
// kind, threading, OS and architecture. It errors if none match.
func LatestSeries(assets []Asset, kind Kind, zts bool, osID platform.OS, arch platform.Arch) (string, error) {
	var best string
	for _, a := range assets {
		p, ok := ParseAssetName(a.Name)
		if !ok || p.Kind != kind || p.OS != osID || p.Arch != arch || p.ZTS != zts {
			continue
		}
		if best == "" || compareSeries(p.Series, best) > 0 {
			best = p.Series
		}
	}
	if best == "" {
		return "", &NoMatchError{
			Selector:  Selector{Kind: kind, ZTS: zts, OS: osID, Arch: arch},
			Available: availableVariants(assets, kind),
		}
	}
	return best, nil
}

// compareSeries compares two dotted version strings numerically. Returns -1, 0
// or 1. Non-numeric fields sort as 0.
func compareSeries(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var ai, bi int
		if i < len(as) {
			ai, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bi, _ = strconv.Atoi(bs[i])
		}
		switch {
		case ai < bi:
			return -1
		case ai > bi:
			return 1
		}
	}
	return 0
}

// NoMatchError is returned when no asset satisfies a selector. It lists the
// available variants of the requested kind to help the user.
type NoMatchError struct {
	Selector  Selector
	Available []string
}

func (e *NoMatchError) Error() string {
	sel := e.Selector
	threading := "nts"
	if sel.ZTS {
		threading = "ts"
	}
	want := fmt.Sprintf("%s php%s %s %s/%s", sel.Kind, orAny(sel.Series), threading, sel.OS, sel.Arch)
	if len(e.Available) == 0 {
		return fmt.Sprintf("no matching %s asset (wanted %s); none available in this release", sel.Kind, want)
	}
	return fmt.Sprintf("no matching %s asset (wanted %s); available: %s",
		sel.Kind, want, strings.Join(e.Available, ", "))
}

func orAny(s string) string {
	if s == "" {
		return "(latest)"
	}
	return s
}

// availableVariants lists the parseable assets of a kind as human-readable
// variant strings, sorted and de-duplicated.
func availableVariants(assets []Asset, kind Kind) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range assets {
		p, ok := ParseAssetName(a.Name)
		if !ok || p.Kind != kind {
			continue
		}
		threading := "nts"
		if p.ZTS {
			threading = "ts"
		}
		v := fmt.Sprintf("php%s %s %s/%s", p.Series, threading, p.OS, p.Arch)
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
