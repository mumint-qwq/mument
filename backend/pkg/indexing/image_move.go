// Added for mument 007-C.
package indexing

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/gtsteffaniak/filebrowser/backend/internal/utils"
	"github.com/gtsteffaniak/go-logger/logger"
)

// TransferMovedImage attaches B metadata only after the filesystem adapter has
// observed a proven rename. Reobserve the target before installing any result.
func TransferMovedImage(src, dst *Index, from, to string, before, after os.FileInfo) {
	if src.db == nil || src.db != dst.db || src.Config.ResolvedRules.IndexingDisabled || dst.Config.ResolvedRules.IndexingDisabled {
		return
	}
	if !isKnownImageExtension(from) && !isKnownImageExtension(to) {
		return
	}
	a, b := versionFromFileInfo(before, ""), versionFromFileInfo(after, "")
	seq := dst.db.NextImageObsSeq()
	current, err := os.Lstat(to)
	if err != nil || versionFromFileInfo(current, "") != b {
		return
	}
	oldExt, newExt := strings.ToLower(filepath.Ext(from)), strings.ToLower(filepath.Ext(to))
	sameFormat := oldExt == newExt || (oldExt == ".jpg" && newExt == ".jpeg") || (oldExt == ".jpeg" && newExt == ".jpg")
	err = dst.db.TransferImageMetadata(src.Name, src.MakeIndexPath(from, false).String(),
		dst.Name, dst.MakeIndexPath(to, false).String(), filepath.Base(to), a.String(), b.String(), b.Size, b.MtimeNS, seq, sameFormat)
	if err != nil {
		logger.Warningf("could not transfer image metadata after rename: %v", err)
	}
}

// TransferMovedDirectoryImages joins the move queue's existing recursive
// index work. Only ready source rows are paged, and each target is matched by
// its unchanged full B version before metadata is attached. A descendant
// edited after the rename is left to B's regular scanner.
func TransferMovedDirectoryImages(src, dst *Index, sourceDir, targetDir string, stillCurrent func() bool) error {
	if src.db == nil || src.db != dst.db || dst.Config.ResolvedRules.IndexingDisabled {
		return nil
	}
	const pageSize = 64
	for after := ""; stillCurrent(); {
		rows, err := src.db.ReadyImagesAfter(src.Name, sourceDir, after, pageSize)
		if err != nil {
			return err
		}
		for _, row := range rows {
			to := targetDir + strings.TrimPrefix(row.Path, sourceDir)
			if err := transferDirectoryImage(src, dst, row.Path, to, row.Version); err != nil {
				return err
			}
		}
		if len(rows) < pageSize {
			return nil
		}
		after = rows[len(rows)-1].Path
	}
	return nil
}

func transferDirectoryImage(src, dst *Index, from, to, expectedVersion string) error {
	guard := NewDirectoryWriteGuard(dst.Name, filepath.ToSlash(filepath.Dir(to)))
	defer guard.Close()
	real := utils.JoinUnderSourceRoot(dst.Path, to)
	info, err := os.Lstat(real)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	version := versionFromFileInfo(info, "")
	if version.Known&versionKnownCtimeNS == 0 || version.String() != expectedVersion {
		return nil
	}
	if !guard.acquire() {
		return nil
	}
	defer guard.release()
	seq := dst.db.NextImageObsSeq()
	return dst.db.TransferImageMetadata(src.Name, from, dst.Name, to, filepath.Base(real), expectedVersion,
		expectedVersion, version.Size, version.MtimeNS, seq, true)
}
