package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/php-debugger/installer/internal/ini"
	"github.com/php-debugger/installer/internal/manifest"
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
	// stripDebuggerLoader removes any existing php-debugger extension loader. Used
	// for the interpreter (it has the debugger compiled in, so the standalone
	// extension must not also load) regardless of ABI match.
	stripDebuggerLoader bool
	// skipUnchanged avoids writing (and registering undo for) files the rules
	// leave untouched. Used for in-place edits of an existing php's config.
	skipUnchanged bool
	// backupDir, when set, makes each in-place edit save the file's original
	// contents there first, returned as manifest.FileBackup entries so uninstall
	// can restore them (e.g. bringing back a stripped xdebug). Used only by the
	// extension flow, which mutates the user's real ini files.
	backupDir string
}

// rewriteIniFiles applies the shared ini rules to a set of (src -> dst) pairs:
// strip xdebug loaders, optionally comment other extension loaders, and — after
// a single confirmation covering all files — sanitize disallowed xdebug.mode
// tokens. It registers undo steps on rb and returns the destination files
// written plus any original-content backups made (see iniRewriteOptions.backupDir).
// This is the one place the xdebug/ini handling lives; the interpreter and
// extension flows differ only via iniRewriteOptions.
func rewriteIniFiles(pairs []configPair, cfg iniRewriteOptions, rb *rollback, opts Options) ([]string, []manifest.FileBackup, error) {
	stripModes, err := decideStripModes(pairs, opts)
	if err != nil {
		return nil, nil, err
	}

	var written []string
	var backups []manifest.FileBackup
	for _, pr := range pairs {
		data, err := os.ReadFile(pr.src)
		if err != nil {
			return written, backups, fmt.Errorf("reading ini %s: %w", pr.src, err)
		}
		content, removedXdebug := ini.StripXdebugLoaders(string(data))
		var removedDebugger, commentedOther []string
		if cfg.stripDebuggerLoader {
			content, removedDebugger = ini.StripPhpDebuggerLoaders(content)
		}
		if cfg.commentOtherLoaders {
			content, commentedOther = ini.CommentExtensionLoaders(content)
		}
		if stripModes {
			content, _, _ = ini.SanitizeXdebugMode(content)
		}
		if cfg.skipUnchanged && content == string(data) {
			continue
		}

		// Persist the original contents (for uninstall) before overwriting.
		if cfg.backupDir != "" {
			bpath, err := saveIniBackup(cfg.backupDir, pr.dst, data)
			if err != nil {
				return written, backups, err
			}
			rb.add(func() error { return removeIfExists(bpath) })
			backups = append(backups, manifest.FileBackup{OriginalPath: pr.dst, BackupPath: bpath})
		}

		if err := registerFileRestore(pr.dst, rb, 0o644); err != nil {
			return written, backups, err
		}
		if err := os.MkdirAll(filepath.Dir(pr.dst), 0o755); err != nil {
			return written, backups, fmt.Errorf("creating config dir: %w", err)
		}
		if err := os.WriteFile(pr.dst, []byte(content), 0o644); err != nil {
			return written, backups, fmt.Errorf("writing ini %s: %w", pr.dst, err)
		}
		written = append(written, pr.dst)
		opts.logf("  %s%s", pr.dst, loaderNote(len(removedXdebug), len(removedDebugger), len(commentedOther)))
	}
	return written, backups, nil
}

// saveIniBackup writes the original contents of originalPath into backupDir under
// a unique, traceable name, returning the backup path. The name is made unique
// (via CreateTemp) rather than deterministic so a fresh backup never collides with
// one from a previous run: otherwise a forced reinstall could overwrite — and its
// rollback delete — a backup file the persisted manifest still points at, breaking
// a later uninstall restore.
func saveIniBackup(backupDir, originalPath string, data []byte) (string, error) {
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", fmt.Errorf("creating backup dir: %w", err)
	}
	// Pattern keeps the origin file's name visible: "ini-<random>-php.ini".
	f, err := os.CreateTemp(backupDir, "ini-*-"+filepath.Base(originalPath))
	if err != nil {
		return "", fmt.Errorf("backing up %s: %w", originalPath, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("backing up %s: %w", originalPath, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("backing up %s: %w", originalPath, err)
	}
	// CreateTemp makes the backup 0600. restoreBackup reproduces the backup file's
	// own mode on uninstall (a rename carries it over verbatim), so align the backup
	// with the original ini's permissions — otherwise restoring would clamp a
	// world-readable php.ini down to owner-only and php running as another user
	// (e.g. php-fpm as www-data) could no longer read it.
	if fi, err := os.Stat(originalPath); err == nil {
		if err := os.Chmod(f.Name(), fi.Mode().Perm()); err != nil {
			os.Remove(f.Name())
			return "", fmt.Errorf("backing up %s: %w", originalPath, err)
		}
	}
	return f.Name(), nil
}

// copyConfig makes the new interpreter load the same (sanitized) configuration as
// the one it replaces. Config files whose destination differs from their source
// are copied into the new interpreter's compiled-in config path and returned as
// written files (uninstall deletes them). Files whose destination IS their source
// — which happens when the new interpreter's compiled-in config path is the
// existing php's own directory (e.g. both report /opt/homebrew/etc/php/8.5) — are
// instead sanitized in place with their originals backed up, and returned as
// FileBackups (uninstall restores them). Writing a "copy" over such a file would
// silently mutate the user's real config; deleting it on uninstall would destroy
// their configuration and prune their directory.
//
// Other (non-xdebug) extension loaders are commented out only when the new
// interpreter has a different ABI from the one it replaces — a foreign .so built
// for a different PHP version or thread-safety cannot load and would error on
// every run. When the replacement is the same PHP series and thread-safety, those
// extensions load fine, so their loaders are left intact.
func copyConfig(existing, target *php.Info, backupDir string, rb *rollback, opts Options) ([]string, []manifest.FileBackup, error) {
	copyPairs, sharedPairs := splitInPlacePairs(interpreterConfigPairs(existing, target))
	if len(copyPairs) == 0 && len(sharedPairs) == 0 {
		if target.Ini.ConfigPath == "" && target.Ini.ScanDir == "" {
			opts.logf("Note: the interpreter reports no config path; skipping ini copy.")
		}
		return nil, nil, nil
	}
	sameABI := existing.Series == target.Series && existing.ZTS == target.ZTS
	if sameABI {
		opts.logf("  (same PHP %s %s as before; keeping existing extension loaders)",
			target.Series, threading(target.ZTS))
	}

	// The debugger loader is always stripped: the interpreter has it built in, so
	// loading a standalone php-debugger extension on top would double-register it.
	written, _, err := rewriteIniFiles(copyPairs, iniRewriteOptions{
		commentOtherLoaders: !sameABI,
		stripDebuggerLoader: true,
	}, rb, opts)
	if err != nil {
		return written, nil, err
	}

	// In-place edits of the user's own config: sanitize (disabling the incompatible
	// xdebug) but back up the originals so uninstall can restore them. Only files the
	// rules actually change are touched and recorded.
	if len(sharedPairs) > 0 {
		opts.logf("The interpreter shares the existing PHP's config directory (%s); "+
			"disabling incompatible extensions there (restored on uninstall).", target.Ini.ConfigPath)
	}
	_, backups, err := rewriteIniFiles(sharedPairs, iniRewriteOptions{
		commentOtherLoaders: !sameABI,
		stripDebuggerLoader: true,
		skipUnchanged:       true,
		backupDir:           backupDir,
	}, rb, opts)
	if err != nil {
		return written, backups, err
	}
	return written, backups, nil
}

// preserveConfig combines the config bookkeeping captured this run with a prior
// interpreter record's, so a reinstall/update — which runs over our own active php
// and therefore re-copies nothing — keeps the entries uninstall needs. A freshly
// derived set of copied files wins; otherwise the prior list is kept. In-place ini
// backups are merged by file, freshly-captured ones winning.
func preserveConfig(prev manifest.Interpreter, files []string, backups []manifest.FileBackup) ([]string, []manifest.FileBackup) {
	if len(files) == 0 {
		files = prev.ConfigFiles
	}
	return files, mergeFileBackups(backups, prev.ConfigBackups)
}

// mergeFileBackups returns fresh plus any prior backup for a file fresh did not
// cover (matched by OriginalPath); a freshly-captured backup wins for its own file,
// since it holds that file's true pre-edit contents from this run.
func mergeFileBackups(fresh, prior []manifest.FileBackup) []manifest.FileBackup {
	if len(prior) == 0 {
		return fresh
	}
	freshByPath := make(map[string]bool, len(fresh))
	for _, b := range fresh {
		freshByPath[b.OriginalPath] = true
	}
	merged := append([]manifest.FileBackup(nil), fresh...)
	for _, b := range prior {
		if !freshByPath[b.OriginalPath] {
			merged = append(merged, b)
		}
	}
	return merged
}

// sanitizeInPlace applies the xdebug ini rules to the given existing ini files
// in place (used by the extension flow to disable a real xdebug). Only files the
// rules change are touched. Each modified file's original contents are saved
// under backupDir and returned so uninstall can restore them.
func sanitizeInPlace(files []string, backupDir string, rb *rollback, opts Options) ([]manifest.FileBackup, error) {
	_, backups, err := rewriteIniFiles(inPlacePairs(files),
		iniRewriteOptions{skipUnchanged: true, backupDir: backupDir}, rb, opts)
	return backups, err
}

// sanitizeOwnConfig disables extensions the freshly installed interpreter cannot
// load from its OWN compiled-in config path. A self-contained build cannot load
// foreign .so files, and these builds report a version-specific config path (e.g.
// /opt/homebrew/etc/php/8.5) that is often a Homebrew php's own directory, holding
// an xdebug loader the interpreter would choke on. It edits those files in place,
// backing up the originals so uninstall restores them.
//
// This runs when no foreign php was detected to copy configuration from — most
// importantly when switching to a new version while our own interpreter is active
// — because the interpreter still reads whatever lives at its config path. Loaders
// are always commented (the build cannot load any foreign extension); only files
// the rules actually change are touched and recorded.
func sanitizeOwnConfig(info *php.Info, backupDir string, rb *rollback, opts Options) ([]manifest.FileBackup, error) {
	var files []string
	if info.Ini.LoadedFile != "" {
		files = append(files, info.Ini.LoadedFile)
	}
	files = append(files, info.Ini.AdditionalFiles...)
	if len(files) == 0 {
		return nil, nil
	}
	_, backups, err := rewriteIniFiles(inPlacePairs(files), iniRewriteOptions{
		commentOtherLoaders: true,
		stripDebuggerLoader: true,
		skipUnchanged:       true,
		backupDir:           backupDir,
	}, rb, opts)
	return backups, err
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

// splitInPlacePairs partitions config pairs into those that copy to a new location
// (dst != src) and those whose destination resolves to the source file itself (the
// new interpreter shares the existing php's config directory). The two groups are
// handled differently: copies are written and deleted on uninstall; in-place edits
// are backed up and restored on uninstall.
func splitInPlacePairs(pairs []configPair) (copies, inPlace []configPair) {
	for _, pr := range pairs {
		if sameFile(pr.src, pr.dst) {
			inPlace = append(inPlace, pr)
		} else {
			copies = append(copies, pr)
		}
	}
	return copies, inPlace
}

// sameFile reports whether two paths refer to the same file. It compares cleaned
// paths and, when both exist, falls back to os.SameFile so symlinked or otherwise
// aliased paths still match.
func sameFile(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
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
// file, e.g. " (removed 1 xdebug loader(s), removed 1 php-debugger loader(s))".
func loaderNote(removedXdebug, removedDebugger, commentedOther int) string {
	var parts []string
	if removedXdebug > 0 {
		parts = append(parts, fmt.Sprintf("removed %d xdebug loader(s)", removedXdebug))
	}
	if removedDebugger > 0 {
		parts = append(parts, fmt.Sprintf("removed %d php-debugger loader(s)", removedDebugger))
	}
	if commentedOther > 0 {
		parts = append(parts, fmt.Sprintf("commented %d extension loader(s)", commentedOther))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
