// Added for mument 007-C.
package cacheledger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Location accepts paths inside an already-authorized source root and
// addresses their parent directories on the observed filesystem. A rename of
// any ancestor preserves these locators. Full file version equality still
// gates every read, including through aliases and after directory recreation.
func Location(root, path string) (namespace, name string, ok bool) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", false
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", "", false
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", "", false
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	parent, err := os.Stat(dir)
	if err != nil || !parent.IsDir() {
		return "", "", false
	}
	pv := FromFileInfo(parent)
	if !pv.Strong {
		return "", "", false
	}
	fs, ok := filesystemID(dir)
	if !ok {
		return "", "", false
	}
	return fmt.Sprintf("%s/%d:%d", fs, pv.Dev, pv.Ino), "/" + filepath.Base(path), true
}
