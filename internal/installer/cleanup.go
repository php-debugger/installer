package installer

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/php-debugger/installer/internal/manifest"
	"github.com/php-debugger/installer/internal/platform"
)

// finalizeManifest saves the manifest, or removes the whole install root when
// nothing is installed any more (so a full uninstall leaves no empty leftovers).
func finalizeManifest(layout platform.Layout, m *manifest.Manifest) error {
	if manifestEmpty(m) {
		return os.RemoveAll(layout.Root)
	}
	return m.Save(layout.ManifestPath())
}

func manifestEmpty(m *manifest.Manifest) bool {
	return len(m.Interpreters) == 0 && len(m.Backups) == 0 &&
		m.Extension == nil && m.Active() == ""
}

// pruneEmptyConfigDirs removes now-empty directories that held copied config
// files (e.g. the compiled-config path and its conf.d), deepest first. os.Remove
// only succeeds on empty dirs, so shared directories with other content are left
// intact.
func pruneEmptyConfigDirs(files []string) {
	seen := map[string]bool{}
	var dirs []string
	for _, f := range files {
		d := filepath.Dir(f)
		for i := 0; i < 2; i++ { // the file's dir and one parent
			if d == "" || d == "/" || d == "." {
				break
			}
			if !seen[d] {
				seen[d] = true
				dirs = append(dirs, d)
			}
			d = filepath.Dir(d)
		}
	}
	// Deeper paths (longer strings under a common root) first.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		_ = os.Remove(d)
	}
}
