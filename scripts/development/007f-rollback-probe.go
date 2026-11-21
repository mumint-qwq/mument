//go:build linux && !386 && !arm && !cacheledger_portable

package preview

import (
	"context"
	"encoding/json"
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	indexsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

var fRollbackIndex *indexsql.IndexDB

type fRollbackState struct {
	First, Rewritten         map[string]string
	FirstIndex, CurrentIndex string
}

func fRollbackStart(t *testing.T) (string, *Service, iteminfo.ExtendedFileInfo) {
	t.Helper()
	dir := os.Getenv("MUMENT_REUSE_ROLLBACK")
	if dir == "" {
		t.Skip("isolated rollback fixture")
	}
	root := filepath.Join(dir, "source")
	require.NoError(t, os.MkdirAll(root, 0700))
	oldCache := settings.Config.Server.CacheDir
	settings.Config.Server.CacheDir = filepath.Join(dir, "index-cache")
	require.NoError(t, os.MkdirAll(settings.Config.Server.CacheDir, 0700))
	t.Cleanup(func() { settings.Config.Server.CacheDir = oldCache })
	db, fresh, err := indexsql.NewIndexDB("f-rollback", "WAL", 100, 8, false)
	require.NoError(t, err)
	fRollbackIndex = db
	t.Cleanup(func() { require.NoError(t, db.Close()); fRollbackIndex = nil })
	indexing.SetTestIndexWithDB("reuse-rollback", root, db)
	t.Cleanup(indexing.ClearTestIndices)
	phase := os.Getenv("MUMENT_REUSE_ROLLBACK_PHASE")
	if phase == "start" {
		_, err = db.Exec(`CREATE TABLE f_rollback_sentinel(value TEXT PRIMARY KEY);INSERT INTO f_rollback_sentinel VALUES('kept')`)
		require.NoError(t, err)
	} else {
		require.False(t, fresh, "persistent index must not be recreated")
		var sentinel, version string
		require.NoError(t, db.QueryRow(`SELECT value FROM f_rollback_sentinel`).Scan(&sentinel))
		require.Equal(t, "kept", sentinel)
		require.NoError(t, db.QueryRow(`SELECT image_version FROM index_items WHERE source='reuse-rollback' AND path='/source.jpg'`).Scan(&version))
		require.Equal(t, fRollbackRead(t, dir).CurrentIndex, version)
		t.Logf("persistent index retained; current source observation verified")
	}
	require.NoError(t, StartPreviewGeneratorWithBudget(1, 128, filepath.Join(dir, "cache")))
	s := service
	t.Cleanup(func() { require.NoError(t, s.Shutdown(context.Background())) })
	var rootStat, cacheStat unix.Statx_t
	require.NoError(t, unix.Statx(unix.AT_FDCWD, root, 0, unix.STATX_MNT_ID, &rootStat))
	require.NoError(t, unix.Statx(unix.AT_FDCWD, filepath.Join(dir, "cache"), 0, unix.STATX_MNT_ID, &cacheStat))
	t.Logf("source mount=%d cache mount=%d", rootStat.Mnt_id, cacheStat.Mnt_id)
	return dir, s, iteminfo.ExtendedFileInfo{Source: "reuse-rollback", RealPath: filepath.Join(root, "source.jpg"), FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: "source.jpg", Type: "image/jpeg", HasPreview: true}}}
}
func fRollbackRead(t *testing.T, dir string) fRollbackState {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(dir, "state.json"))
	require.NoError(t, e)
	var s fRollbackState
	require.NoError(t, json.Unmarshal(b, &s))
	return s
}
func fRollbackWrite(t *testing.T, dir string, s fRollbackState) {
	t.Helper()
	idx := indexing.GetIndex("reuse-rollback")
	require.NotNil(t, idx)
	require.NoError(t, idx.RefreshDirectory("/", false))
	require.NoError(t, fRollbackIndex.QueryRow(`SELECT image_version FROM index_items WHERE source='reuse-rollback' AND path='/source.jpg'`).Scan(&s.CurrentIndex))
	if s.FirstIndex == "" {
		s.FirstIndex = s.CurrentIndex
	}
	b, e := json.Marshal(s)
	require.NoError(t, e)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.json"), b, 0600))
}

// This exact probe is compiled into aaaefeb, without F APIs or schema knowledge.
func TestFCompatibilityBaseline(t *testing.T) {
	dir, s, source := fRollbackStart(t)
	phase := os.Getenv("MUMENT_REUSE_ROLLBACK_PHASE")
	if phase == "start" {
		source = newNoisyFile(t, filepath.Dir(source.RealPath), "source.jpg", 1600, 1200)
		source.Source = "reuse-rollback"
		state := fRollbackState{First: map[string]string{}}
		for _, size := range []string{"small", "xlarge"} {
			data, e := s.getPreviewForFile(t.Context(), source, size, "", 0)
			require.NoError(t, e)
			state.First[size] = cacheledger.Digest(data)
		}
		fRollbackWrite(t, dir, state)
		return
	}
	require.Equal(t, "old-return", phase)
	state := fRollbackRead(t, dir)
	copy := source
	copy.Name = "copy.jpg"
	copy.RealPath = filepath.Join(filepath.Dir(source.RealPath), copy.Name)
	before := s.Stats().FullDecodes
	for _, file := range []iteminfo.ExtendedFileInfo{source, copy} {
		for _, size := range []string{"small", "xlarge"} {
			data, e := s.getPreviewForFile(t.Context(), file, size, "", 0)
			require.NoError(t, e)
			require.Equal(t, state.First[size], cacheledger.Digest(data))
		}
	}
	require.Equal(t, before, s.Stats().FullDecodes)
	// Old E3 Resolve and naked INSERT writers must still create a fresh version.
	source = newNoisyFile(t, filepath.Dir(source.RealPath), "source.jpg", 1300, 1500)
	source.Source = "reuse-rollback"
	state.Rewritten = map[string]string{}
	for _, size := range []string{"small", "xlarge"} {
		data, e := s.getPreviewForFile(t.Context(), source, size, "", 0)
		require.NoError(t, e)
		state.Rewritten[size] = cacheledger.Digest(data)
		require.NotEqual(t, state.First[size], state.Rewritten[size])
	}
	fRollbackWrite(t, dir, state)
}
