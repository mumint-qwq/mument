//go:build linux && !386 && !arm && !cacheledger_portable

package preview

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	dbsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
)

// The replay exports only deliberately synthetic data into a caller-selected
// empty directory, then reopens it using the frozen D source checkout.
func TestOrphanExportRollback(t *testing.T) {
	dir := os.Getenv("MUMENT_ORPHAN_ROLLBACK_FIXTURE")
	if dir == "" {
		t.Skip("rollback replay fixture exporter")
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
	f := &orphanFixture{root: filepath.Join(dir, "source"), cache: filepath.Join(dir, "cache")}
	require.NoError(t, os.Mkdir(f.root, 0o700))
	require.NoError(t, os.Mkdir(f.cache, 0o700))
	indexing.SetTestIndex("orphan-test", f.root)
	t.Cleanup(indexing.ClearTestIndices)
	previousCache := settings.Config.Server.CacheDir
	settings.Config.Server.CacheDir = f.cache
	t.Cleanup(func() { settings.Config.Server.CacheDir = previousCache })
	f.start(t)
	keep := newNoisyFile(t, f.root, "keep.jpg", 800, 600)
	keep.Source = "orphan-test"
	removed := newNoisyFile(t, f.root, "remove.jpg", 800, 600)
	removed.Source = keep.Source
	f.source = removed.RealPath
	ctx := t.Context()
	previewBytes, err := f.s.getPreviewForFile(ctx, keep, "xlarge", "", 0)
	require.NoError(t, err)
	_, err = f.s.getPreviewForFile(ctx, removed, "small", "", 0)
	require.NoError(t, err)
	keepData, err := os.ReadFile(keep.RealPath)
	require.NoError(t, err)

	db, _, err := dbsql.NewIndexDB("rollback", "WAL", 100, 32, false)
	require.NoError(t, err)
	defer db.Close()
	row := keep.FileInfo
	row.Path = "/keep.jpg"
	row.Image = &iteminfo.ImageIndexMeta{Version: "e2-fixture:keep", ObsSeq: 19, Guard: 19, State: iteminfo.ImageMetaUnknown,
		LogicalSize: row.Size, MTimeNS: row.ModTime.UnixNano()}
	require.NoError(t, db.InsertItem(keep.Source, row.Path, &row))
	work, err := db.GetImageWorksByPaths(keep.Source, []string{row.Path})
	require.NoError(t, err)
	ready, err := db.CommitImageMetadata(keep.Source, row.Path, row.Image.Version, work[row.Path].Guard, dbsql.ImageMetadataCommit{
		State: iteminfo.ImageMetaReady, Width: 800, Height: 600, Orientation: 1, Ratio: 4.0 / 3.0,
	})
	require.NoError(t, err)
	require.True(t, ready)
	require.NoError(t, db.PreparePreviewWarmup(ctx))
	_, err = db.Exec("UPDATE index_items SET warmup_guard=image_guard,warmup_attempts=-1 WHERE path='/keep.jpg'")
	require.NoError(t, err)
	expected := fmt.Sprintf("%s\n%s\n%d\n", cacheledger.Digest(keepData), cacheledger.Digest(previewBytes), work[row.Path].Guard)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "expected.txt"), []byte(expected), 0o600))
	due := f.remove(t).Add(orphanGrace)
	<-f.s.orphans.done
	f.s.orphans.enabled = true
	require.Equal(t, 1, f.step(t, due, true).Unlinked)
}
