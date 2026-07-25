// Package php inspects PHP interpreters: it finds an existing php on PATH, runs a
// binary to smoke-test that it executes on this host, and queries its facts
// (version, thread safety, ini file locations, extension_dir, loaded modules).
//
// Output parsing lives in parse.go and is pure/unit-tested; this file holds the
// thin exec wrappers.
package php

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrNotFound is returned by Detect when no php interpreter is on PATH.
var ErrNotFound = errors.New("no php interpreter found on PATH")

// DebuggerModule is the module name the php-debugger extension registers, as
// reported by `php -m`. Confirmed against release 0.1.0: the interpreter lists
// "php_debugger" under [PHP Modules] (and "PHP Debugger" under [Zend Modules]).
//
// The extension also exposes a *simulated* "xdebug" module for compatibility
// with tooling that probes for xdebug. We therefore verify against
// "php_debugger" specifically — checking for "xdebug" would be ambiguous, since
// a host with real xdebug (but not our debugger) would match it too.
//
// Install verification checks for it via HasModule (case-insensitive).
const DebuggerModule = "php_debugger"

// Info describes a PHP interpreter.
type Info struct {
	Path         string // path to the php binary
	Version      string // full version, e.g. "8.3.10"
	Series       string // major.minor, e.g. "8.3"
	ZTS          bool   // thread-safe build
	ExtensionDir string // ini_get("extension_dir")
	Ini          IniPaths
}

// infoScript prints the interpreter facts as key=value lines. Using -r keeps the
// output locale-independent (unlike parsing -v / -i prose).
const infoScript = `printf("version=%s\nseries=%s\nzts=%d\nextension_dir=%s\n",` +
	` PHP_VERSION, PHP_MAJOR_VERSION.".".PHP_MINOR_VERSION, PHP_ZTS, ini_get("extension_dir"));`

// Detect returns the path to the php interpreter on PATH, or ErrNotFound.
func Detect() (string, error) {
	path, err := exec.LookPath("php")
	if err != nil {
		return "", ErrNotFound
	}
	return path, nil
}

// SmokeError reports that a binary failed to run `php -v`. It carries the
// combined output so the caller can show it to the user.
type SmokeError struct {
	Binary string
	Output string
	Err    error
}

func (e *SmokeError) Error() string {
	out := strings.TrimSpace(e.Output)
	if out == "" {
		return fmt.Sprintf("%s failed to run: %v", e.Binary, e.Err)
	}
	return fmt.Sprintf("%s failed to run: %v\n%s", e.Binary, e.Err, out)
}

func (e *SmokeError) Unwrap() error { return e.Err }

// SmokeTest runs `binary -v` to confirm the interpreter executes on this host.
// On failure it returns a *SmokeError with the captured output.
func SmokeTest(ctx context.Context, binary string) error {
	cmd := exec.CommandContext(ctx, binary, "-v")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &SmokeError{Binary: binary, Output: string(out), Err: err}
	}
	return nil
}

// Query runs the interpreter to gather its Info (version, thread safety,
// extension_dir and ini paths).
func Query(ctx context.Context, binary string) (*Info, error) {
	info := &Info{Path: binary}

	out, err := run(ctx, binary, "-r", infoScript)
	if err != nil {
		return nil, fmt.Errorf("querying php info: %w", err)
	}
	kv := parseKeyVals(out)
	info.Version = kv["version"]
	info.Series = kv["series"]
	info.ZTS = kv["zts"] == "1"
	info.ExtensionDir = kv["extension_dir"]

	iniOut, err := run(ctx, binary, "--ini")
	if err != nil {
		return nil, fmt.Errorf("querying php ini paths: %w", err)
	}
	info.Ini = parseIniOutput(iniOut)

	return info, nil
}

// Modules lists the modules reported by `binary -m`.
func Modules(ctx context.Context, binary string) ([]string, error) {
	out, err := run(ctx, binary, "-m")
	if err != nil {
		return nil, fmt.Errorf("listing php modules: %w", err)
	}
	return parseModules(out), nil
}

// HasModule reports whether `binary -m` lists a module named name
// (case-insensitively).
func HasModule(ctx context.Context, binary, name string) (bool, error) {
	mods, err := Modules(ctx, binary)
	if err != nil {
		return false, err
	}
	for _, m := range mods {
		if strings.EqualFold(m, name) {
			return true, nil
		}
	}
	return false, nil
}

// run executes a php command, returning stdout on success. On failure the error
// includes stderr.
func run(ctx context.Context, binary string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return stdout.String(), nil
}
