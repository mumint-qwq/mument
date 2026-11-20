// This probe is copied into frozen E2 and D source checkouts by verify-007e3.sh.
package preview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	dbsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
)

func TestOldReadsE3CapacityFixture(t *testing.T) {
	dir := os.Getenv("MUMENT_CAPACITY_ROLLBACK")
	require.NotEmpty(t, dir)
	raw, err := os.ReadFile(filepath.Join(dir, "capacity.json"))
	require.NoError(t, err)
	var proof struct {
		KeepOriginal, KeepPreview, ColdOriginal, Isolation, IsolatedDigest string
		Guard                                                              uint64
	}
	require.NoError(t, json.Unmarshal(raw, &proof))
	root, cache := filepath.Join(dir, "source"), filepath.Join(dir, "cache")
	settings.Config.Server.CacheDir = cache
	indexing.SetTestIndex("orphan-test", root)
	require.NoError(t, StartPreviewGeneratorWithBudget(1, 128, cache))
	t.Cleanup(func() { require.NoError(t, Shutdown(context.Background())) })
	for _, name := range []string{"keep.jpg", "cold.jpg"} {
		path := filepath.Join(root, name)
		original, err := os.ReadFile(path)
		require.NoError(t, err)
		stat, err := os.Stat(path)
		require.NoError(t, err)
		file := testFileInfoFromStatForReview(path, stat)
		file.Source = "orphan-test"
		data, err := GetPreviewForFile(t.Context(), file, "xlarge", "", 0)
		require.NoError(t, err)
		require.NotEmpty(t, data)
		if name == "keep.jpg" {
			require.Equal(t, proof.KeepOriginal, cacheledger.Digest(original))
			require.Equal(t, proof.KeepPreview, cacheledger.Digest(data))
			require.Zero(t, GetService().Stats().FullDecodes)
		} else {
			require.Equal(t, proof.ColdOriginal, cacheledger.Digest(original))
			require.EqualValues(t, 1, GetService().Stats().FullDecodes)
		}
	}
	isolated, err := os.ReadFile(proof.Isolation)
	require.NoError(t, err)
	require.Equal(t, proof.IsolatedDigest, cacheledger.Digest(isolated))
	db, newDB, err := dbsql.NewIndexDB("rollback", "WAL", 100, 32, false)
	require.NoError(t, err)
	defer db.Close()
	require.False(t, newDB)
	rows, err := db.GetDirectoryChildren("orphan-test", "/")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 4.0/3.0, rows[0].DisplayAspectRatio)
	require.NotNil(t, rows[0].Image)
	require.Equal(t, 800, rows[0].Image.Width)
	require.Equal(t, 600, rows[0].Image.Height)
	require.Equal(t, uint8(1), rows[0].Image.Orientation)
	var guard uint64
	var attempts int64
	require.NoError(t, db.DB().QueryRow("SELECT warmup_guard,warmup_attempts FROM index_items WHERE path='/keep.jpg'").Scan(&guard, &attempts))
	require.Equal(t, proof.Guard, guard)
	require.EqualValues(t, -1, attempts)
	t.Log("valid preview reused, evicted preview rebuilt, both originals and B metadata/D guard intact, E3 isolation preserved")
}
