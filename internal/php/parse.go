package php

import "strings"

// IniPaths captures the ini file locations reported by `php --ini`.
type IniPaths struct {
	// ConfigPath is the directory php looks in for its main php.ini (the
	// "Configuration File (php.ini) Path"). This is compiled into the binary.
	ConfigPath string
	// LoadedFile is the main php.ini actually loaded ("" if none).
	LoadedFile string
	// ScanDir is the directory scanned for additional .ini files ("" if none).
	ScanDir string
	// AdditionalFiles are the extra .ini files parsed, in order.
	AdditionalFiles []string
}

// parseKeyVals parses simple "key=value" lines (as emitted by our -r info
// script) into a map. Lines without '=' are ignored.
func parseKeyVals(out string) map[string]string {
	m := map[string]string{}
	for _, ln := range strings.Split(out, "\n") {
		eq := strings.IndexByte(ln, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(ln[:eq])
		if key == "" {
			continue
		}
		m[key] = strings.TrimSpace(ln[eq+1:])
	}
	return m
}

// parseIniOutput parses the output of `php --ini`.
func parseIniOutput(out string) IniPaths {
	var p IniPaths
	lines := strings.Split(out, "\n")
	for i, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "Configuration File (php.ini) Path:"):
			p.ConfigPath = cleanIniValue(afterColon(ln))
		case strings.HasPrefix(ln, "Loaded Configuration File:"):
			p.LoadedFile = cleanIniValue(afterColon(ln))
		case strings.HasPrefix(ln, "Scan for additional .ini files in:"):
			p.ScanDir = cleanIniValue(afterColon(ln))
		case strings.HasPrefix(ln, "Additional .ini files parsed:"):
			// The value can wrap across subsequent indented lines; this is the
			// last labelled section, so absorb everything that follows.
			rest := afterColon(ln)
			for _, cont := range lines[i+1:] {
				rest += " " + cont
			}
			p.AdditionalFiles = parseFileList(rest)
		}
	}
	return p
}

// parseModules parses the output of `php -m` into a list of module names,
// skipping section headers ("[PHP Modules]", "[Zend Modules]") and blanks.
func parseModules(out string) []string {
	var mods []string
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "[") {
			continue
		}
		mods = append(mods, ln)
	}
	return mods
}

// parseVersionLine extracts the version and thread-safety from the first line of
// `php -v`, e.g. "PHP 8.3.10 (cli) (built: ...) (NTS)". It is a fallback used to
// verify a freshly downloaded binary before trusting it to run -r scripts.
func parseVersionLine(out string) (version string, zts bool, ok bool) {
	line := out
	if idx := strings.IndexByte(out, '\n'); idx >= 0 {
		line = out[:idx]
	}
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "PHP" {
		return "", false, false
	}
	return fields[1], strings.Contains(line, "ZTS"), true
}

func afterColon(s string) string {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// cleanIniValue trims a value, strips surrounding double quotes (newer PHP quotes
// these paths) and normalizes PHP's "(none)" placeholder to "".
func cleanIniValue(s string) string {
	s = unquotePath(strings.TrimSpace(s))
	if s == "(none)" {
		return ""
	}
	return s
}

// parseFileList splits a comma/whitespace separated list of file paths, dropping
// empties and "(none)", stripping surrounding quotes on each entry.
func parseFileList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = unquotePath(strings.TrimSpace(part))
		if part == "" || part == "(none)" {
			continue
		}
		out = append(out, part)
	}
	return out
}

// unquotePath removes a single pair of surrounding double quotes, then trims.
func unquotePath(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	return strings.TrimSpace(s)
}
