package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/php-debugger/installer/internal/ini"
	"github.com/php-debugger/installer/internal/php"
)

// configPair is a source ini file and where its (sanitized) copy is written.
// For in-place edits src == dst.
type configPair struct{ src, dst string }

// iniRewriteOptions captures the two ways the interpreter and extension flows
// differ when applying the ini rules.
type iniRewriteOptions struct {
	// commentOtherLoaders comments out non-xdebug extension= / zend_extension=
	// loaders. Used for the interpreter (a self-contained build cannot load
	// foreign .so files); not for the extension (the existing php loads its own).
	commentOtherLoaders bool
	// skipUnchanged avoids writing (and registering undo for) files the rules
	// leave untouched. Used for in-place edits of an existing php's config.
	skipUnchanged bool
}

// rewriteIniFiles applies the shared ini rules to a set of (src -> dst) pairs:
// strip xdebug loaders, optionally comment other extension loaders, and — after
// a single confirmation covering all files — sanitize disallowed xdebug.mode
// tokens. It registers undo steps on rb and returns the destination files
// written. This is the one place the xdebug/ini handling lives; the interpreter
// and extension flows differ only via iniRewriteOptions.
func rewriteIniFiles(pairs []configPair, cfg iniRewriteOptions, rb *rollback, opts Options) ([]string, error) {
	stripModes, err := decideStripModes(pairs, opts)
	if err != nil {
		return nil, err
	}

	var written []string
	for _, pr := range pairs {
		data, err := os.ReadFile(pr.src)
		if err != nil {
			return written, fmt.Errorf("reading ini %s: %w", pr.src, err)
		}
		content, removedLoaders := ini.StripXdebugLoaders(string(data))
		var commentedLoaders []string
		if cfg.commentOtherLoaders {
			content, commentedLoaders = ini.CommentExtensionLoaders(content)
		}
		if stripModes {
			content, _, _ = ini.SanitizeXdebugMode(content)
		}
		if cfg.skipUnchanged && content == string(data) {
			continue
		}

		if err := registerFileRestore(pr.dst, rb, 0o644); err != nil {
			return written, err
		}
		if err := os.MkdirAll(filepath.Dir(pr.dst), 0o755); err != nil {
			return written, fmt.Errorf("creating config dir: %w", err)
		}
		if err := os.WriteFile(pr.dst, []byte(content), 0o644); err != nil {
			return written, fmt.Errorf("writing ini %s: %w", pr.dst, err)
		}
		written = append(written, pr.dst)
		opts.logf("  %s%s", pr.dst, loaderNote(len(removedLoaders), len(commentedLoaders)))
	}
	return written, nil
}

// copyConfig copies the existing interpreter's ini files into the new
// interpreter's compiled-in config path (so the new php loads the same
// configuration), sanitizing each on the way. Returns the destination files.
func copyConfig(existing, target *php.Info, rb *rollback, opts Options) ([]string, error) {
	pairs := interpreterConfigPairs(existing, target)
	if len(pairs) == 0 {
		if target.Ini.ConfigPath == "" && target.Ini.ScanDir == "" {
			opts.logf("Note: the interpreter reports no config path; skipping ini copy.")
		}
		return nil, nil
	}
	return rewriteIniFiles(pairs, iniRewriteOptions{commentOtherLoaders: true}, rb, opts)
}

// sanitizeInPlace applies the xdebug ini rules to the given existing ini files
// in place (used by the extension flow to disable a real xdebug). Only files the
// rules change are touched.
func sanitizeInPlace(files []string, rb *rollback, opts Options) error {
	_, err := rewriteIniFiles(inPlacePairs(files), iniRewriteOptions{skipUnchanged: true}, rb, opts)
	return err
}

// interpreterConfigPairs maps the existing main php.ini to the new interpreter's
// ConfigPath, and each additional .ini to its ScanDir (by base name).
func interpreterConfigPairs(existing, target *php.Info) []configPair {
	var pairs []configPair
	if existing.Ini.LoadedFile != "" && target.Ini.ConfigPath != "" {
		pairs = append(pairs, configPair{
			src: existing.Ini.LoadedFile,
			dst: filepath.Join(target.Ini.ConfigPath, "php.ini"),
		})
	}
	if target.Ini.ScanDir != "" {
		for _, f := range existing.Ini.AdditionalFiles {
			pairs = append(pairs, configPair{
				src: f,
				dst: filepath.Join(target.Ini.ScanDir, filepath.Base(f)),
			})
		}
	}
	return pairs
}

// inPlacePairs turns a list of files into src==dst pairs for in-place rewriting.
func inPlacePairs(files []string) []configPair {
	pairs := make([]configPair, 0, len(files))
	for _, f := range files {
		pairs = append(pairs, configPair{src: f, dst: f})
	}
	return pairs
}

// decideStripModes scans the source ini files for disallowed xdebug.mode tokens
// and, if any are found, asks the user whether to remove them (auto-yes under
// --yes). Returns true if the caller should sanitize xdebug.mode.
func decideStripModes(pairs []configPair, opts Options) (bool, error) {
	seen := map[string]bool{}
	var disallowed []string
	for _, pr := range pairs {
		data, err := os.ReadFile(pr.src)
		if err != nil {
			return false, fmt.Errorf("reading ini %s: %w", pr.src, err)
		}
		for _, m := range ini.DisallowedModes(string(data)) {
			if !seen[m] {
				seen[m] = true
				disallowed = append(disallowed, m)
			}
		}
	}
	if len(disallowed) == 0 {
		return false, nil
	}
	sort.Strings(disallowed)
	return opts.confirm(fmt.Sprintf(
		"xdebug.mode lists disallowed mode(s): %s. Remove them (keeping only off/debug)?",
		strings.Join(disallowed, ", "))), nil
}

// registerFileRestore records how to undo writing path: restore its prior
// contents (with perm) if it existed, otherwise remove it on rollback.
func registerFileRestore(path string, rb *rollback, perm os.FileMode) error {
	if prev, err := os.ReadFile(path); err == nil {
		rb.add(func() error { return os.WriteFile(path, prev, perm) })
	} else if os.IsNotExist(err) {
		rb.add(func() error { return removeIfExists(path) })
	} else {
		return fmt.Errorf("inspecting %s: %w", path, err)
	}
	return nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// loaderNote summarizes what happened to extension loaders in a rewritten ini
// file, e.g. " (removed 1 xdebug loader(s), commented 2 extension loader(s))".
func loaderNote(removed, commented int) string {
	var parts []string
	if removed > 0 {
		parts = append(parts, fmt.Sprintf("removed %d xdebug loader(s)", removed))
	}
	if commented > 0 {
		parts = append(parts, fmt.Sprintf("commented %d extension loader(s)", commented))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
