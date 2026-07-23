// Package ini provides line-based editing of PHP ini files. It is deliberately
// not a general ini parser: it preserves the original file's formatting,
// comments and line endings, and only rewrites the specific lines the installer
// cares about — xdebug loader directives and the xdebug.mode setting.
//
// All functions are pure (string in, string out) so they can be unit tested
// without touching the filesystem.
package ini

import "strings"

// AllowedModes are the only xdebug.mode tokens the installer permits.
var AllowedModes = []string{"off", "debug"}

// StripXdebugLoaders removes every line that loads the xdebug extension — i.e. a
// `zend_extension=` directive whose value references "xdebug" — whether the line
// is active or commented out. (Xdebug is a Zend extension and only loads via
// zend_extension, so plain `extension=` lines are left alone.) It returns the
// rewritten content and the list of removed lines (trimmed of any trailing CR)
// for reporting.
func StripXdebugLoaders(content string) (string, []string) {
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, len(lines))
	var removed []string
	for _, ln := range lines {
		_, key, value, ok := parseDirective(ln)
		if ok && key == "zend_extension" && referencesXdebug(value) {
			removed = append(removed, strings.TrimRight(ln, "\r"))
			continue
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n"), removed
}

// DisallowedModes returns the de-duplicated set of xdebug.mode tokens present in
// xdebug.mode directives (active or commented out) that are not in AllowedModes,
// preserving first-seen order. It is empty if xdebug.mode is absent or already
// valid, letting the caller decide whether to prompt the user.
func DisallowedModes(content string) []string {
	var out []string
	seen := map[string]bool{}
	for _, ln := range strings.Split(content, "\n") {
		_, key, value, ok := parseDirective(ln)
		if !ok || key != "xdebug.mode" {
			continue
		}
		for _, m := range parseModeList(value) {
			if !isAllowedMode(m) && !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out
}

// SanitizeXdebugMode rewrites every xdebug.mode directive that contains a
// disallowed token so that only allowed tokens remain, preserving their order
// and de-duplicating. If no allowed token survives, the value becomes "off".
// Commented-out xdebug.mode lines are sanitized too, keeping their leading `;`.
// Lines that already contain only allowed tokens are left byte-for-byte
// untouched.
//
// It returns the new content, the disallowed tokens that were removed
// (de-duplicated, first-seen order) and whether anything changed.
func SanitizeXdebugMode(content string) (string, []string, bool) {
	lines := strings.Split(content, "\n")
	var removed []string
	seenRemoved := map[string]bool{}
	changed := false

	for i, ln := range lines {
		_, key, value, ok := parseDirective(ln)
		if !ok || key != "xdebug.mode" {
			continue
		}

		var allowed []string
		seenAllowed := map[string]bool{}
		hasDisallowed := false
		for _, m := range parseModeList(value) {
			if isAllowedMode(m) {
				if !seenAllowed[m] {
					seenAllowed[m] = true
					allowed = append(allowed, m)
				}
				continue
			}
			hasDisallowed = true
			if !seenRemoved[m] {
				seenRemoved[m] = true
				removed = append(removed, m)
			}
		}
		if !hasDisallowed {
			continue
		}
		if len(allowed) == 0 {
			allowed = []string{"off"}
		}
		lines[i] = rewriteValue(ln, strings.Join(allowed, ","))
		changed = true
	}

	return strings.Join(lines, "\n"), removed, changed
}

// parseDirective inspects a single line. It reports whether the line is a
// directive (has a `key=value` shape), the lowercased key, the trimmed value,
// and whether the directive is commented out (leading `;`). Non-directive lines
// (blank, pure comments, section headers) return ok=false.
func parseDirective(line string) (commented bool, key, value string, ok bool) {
	s := strings.TrimRight(line, "\r")
	trimmed := strings.TrimLeft(s, " \t")
	if strings.HasPrefix(trimmed, ";") {
		commented = true
		trimmed = strings.TrimLeft(trimmed[1:], " \t")
	}
	eq := strings.IndexByte(trimmed, '=')
	if eq < 0 {
		return commented, "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(trimmed[:eq]))
	if key == "" {
		return commented, "", "", false
	}
	value = strings.TrimSpace(trimmed[eq+1:])
	return commented, key, value, true
}

// rewriteValue replaces the value portion of a directive line with newValue,
// preserving the key, the exact spacing up to and including `=`, the leading
// whitespace of the original value, and any trailing CR.
func rewriteValue(line, newValue string) string {
	cr := ""
	body := line
	if strings.HasSuffix(body, "\r") {
		cr = "\r"
		body = body[:len(body)-1]
	}
	eq := strings.IndexByte(body, '=')
	if eq < 0 {
		return line
	}
	head := body[:eq+1]
	rest := body[eq+1:]
	ws := rest[:len(rest)-len(strings.TrimLeft(rest, " \t"))]
	return head + ws + newValue + cr
}

func referencesXdebug(value string) bool {
	return strings.Contains(strings.ToLower(unquote(value)), "xdebug")
}

func parseModeList(value string) []string {
	var out []string
	for _, p := range strings.Split(unquote(value), ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func isAllowedMode(m string) bool {
	for _, a := range AllowedModes {
		if m == a {
			return true
		}
	}
	return false
}

// unquote strips a single pair of matching surrounding quotes, if present.
func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}
