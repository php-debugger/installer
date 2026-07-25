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
type configPair struct{ src, dst string }

// copyConfig copies the existing interpreter's ini files into the new
// interpreter's compiled-in config path (so the new php loads the same
// configuration), sanitizing each on the way: xdebug loader lines are stripped,
// and disallowed xdebug.mode tokens are removed after confirmation.
//
// It registers undo steps on rb (restoring overwritten files / removing created
// ones) and returns the list of destination files written.
func copyConfig(existing, target *php.Info, rb *rollback, opts Options) ([]string, error) {
	pairs := configPairs(existing, target)
	if len(pairs) == 0 {
		if target.Ini.ConfigPath == "" && target.Ini.ScanDir == "" {
			opts.logf("Note: the interpreter reports no config path; skipping ini copy.")
		}
		return nil, nil
	}

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
		content, commentedLoaders := ini.CommentExtensionLoaders(content)
		if stripModes {
			content, _, _ = ini.SanitizeXdebugMode(content)
		}

		if err := registerConfigUndo(pr.dst, rb); err != nil {
			return written, err
		}
		if err := os.MkdirAll(filepath.Dir(pr.dst), 0o755); err != nil {
			return written, fmt.Errorf("creating config dir: %w", err)
		}
		if err := os.WriteFile(pr.dst, []byte(content), 0o644); err != nil {
			return written, fmt.Errorf("writing ini %s: %w", pr.dst, err)
		}
		written = append(written, pr.dst)
		opts.logf("  wrote %s%s", pr.dst, loaderNote(len(removedLoaders), len(commentedLoaders)))
	}
	return written, nil
}

// configPairs builds the (source, destination) list: the existing main php.ini
// goes to the new interpreter's ConfigPath, and each additional .ini goes to its
// ScanDir (by base name).
func configPairs(existing, target *php.Info) []configPair {
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

// registerConfigUndo records how to undo writing dst: restore its prior contents
// if it existed, otherwise remove it on rollback.
func registerConfigUndo(dst string, rb *rollback) error {
	if prev, err := os.ReadFile(dst); err == nil {
		rb.add(func() error { return os.WriteFile(dst, prev, 0o644) })
	} else if os.IsNotExist(err) {
		rb.add(func() error { return removeIfExists(dst) })
	} else {
		return fmt.Errorf("inspecting %s: %w", dst, err)
	}
	return nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// loaderNote summarizes what happened to extension loaders in a copied ini file.
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
