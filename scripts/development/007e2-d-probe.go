// This probe is copied into the frozen 007-D backend/internal/preview package
// by verify-007e2.sh. It uses the old service to reopen an E2-created fixture.
package preview

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	dbsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
)

func TestDReadsE2Fixture(t *testing.T) {
	fixture := os.Getenv("MUMENT_ORPHAN_ROLLBACK_FIXTURE")
	require.NotEmpty(t, fixture)
	root, cache := filepath.Join(fixture, "source"), filepath.Join(fixture, "cache")
	want, err := os.ReadFile(filepath.Join(fixture, "expected.txt"))
	require.NoError(t, err)
	hashes := strings.Fields(string(want))
	require.Len(t, hashes, 3)
	wantGuard, err := strconv.Atoi(hashes[2])
	require.NoError(t, err)
	require.Positive(t, wantGuard)
	original, err := os.ReadFile(filepath.Join(root, "keep.jpg"))
	require.NoError(t, err)
	require.Equal(t, hashes[0], cacheledger.Digest(original))
	require.NoFileExists(t, filepath.Join(root, "remove.jpg"))

	settings.Config.Server.CacheDir = cache
	indexing.SetTestIndex("orphan-test", root)
	require.NoError(t, StartPreviewGeneratorWithBudget(1, 128, cache))
	t.Cleanup(func() { require.NoError(t, Shutdown(context.Background())) })
	st, err := os.Stat(filepath.Join(root, "keep.jpg"))
	require.NoError(t, err)
	file := testFileInfoFromStatForReview(filepath.Join(root, "keep.jpg"), st)
	file.Source = "orphan-test"
	data, err := GetPreviewForFile(t.Context(), file, "xlarge", "", 0)
	require.NoError(t, err)
	require.Equal(t, hashes[1], cacheledger.Digest(data))
	require.Zero(t, GetService().Stats().FullDecodes)

	db, newDB, err := dbsql.NewIndexDB("rollback", "WAL", 100, 32, false)
	require.NoError(t, err)
	defer db.Close()
	require.False(t, newDB)
	rows, err := db.GetDirectoryChildren("orphan-test", "/")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	row := rows[0]
	require.Equal(t, 4.0/3.0, row.DisplayAspectRatio)
	require.NotNil(t, row.Image)
	require.Equal(t, 800, row.Image.Width)
	require.Equal(t, 600, row.Image.Height)
	require.Equal(t, uint8(1), row.Image.Orientation)
	var guard, attempts int
	require.NoError(t, db.DB().QueryRow("SELECT warmup_guard,warmup_attempts FROM index_items WHERE path='/keep.jpg'").Scan(&guard, &attempts))
	require.Equal(t, wantGuard, guard)
	require.Equal(t, -1, attempts)
	t.Log("D reused the exact preview with zero decodes and retained the original, dimensions, ratio, orientation and completed guard")
}
