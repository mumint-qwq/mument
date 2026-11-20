//go:build linux && !386 && !arm && !cacheledger_portable

package preview

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	dbsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

type capacityRollbackProof struct {
	KeepOriginal, KeepPreview            string
	ColdOriginal, ColdAsset, ColdVariant string
	Isolation, IsolatedDigest            string
	Guard                                uint64
	SourceMount, CacheMount              uint64
}

func TestCapacityExportRollback(t *testing.T) {
	dir := os.Getenv("MUMENT_CAPACITY_ROLLBACK")
	if dir == "" {
		t.Skip("capacity rollback fixture exporter")
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
	kept, err := f.s.getPreviewForFile(t.Context(), keep, "xlarge", "", 0)
	require.NoError(t, err)
	keepOriginal, err := os.ReadFile(keep.RealPath)
	require.NoError(t, err)
	cold := newNoisyFile(t, f.root, "cold.jpg", 1600, 1200)
	cold.Source = keep.Source
	plan, _ := publishCapacityFile(t, f, cold, warmupSize)
	coldOriginal, err := os.ReadFile(cold.RealPath)
	require.NoError(t, err)
	when := time.Now().Add(2 * time.Hour)
	m := testCapacity(t, f, when, true)
	require.Equal(t, 1, m.report(t.Context(), CapacityCursor{}, true).Unlinked)
	require.NoFileExists(t, filepath.Join(f.cache, "diskcache", diskcache.RelativePath(plan.key)))
	_, pending := publishCapacityFixture(t, f, "pending.jpg")
	m.orphan.checkpoint = func(stage string) error {
		if stage == "isolated" {
			return errors.New("retain isolated capacity object for rollback")
		}
		return nil
	}
	require.Equal(t, 1, m.report(t.Context(), CapacityCursor{}, true).Reasons["interrupted"])
	m.orphan.checkpoint = nil
	intents, err := f.s.ledger.CapacityIntents(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, intents, 1)
	require.Equal(t, pending.Key, intents[0].Key)
	isolation := filepath.Join(f.cache, "diskcache", ".orphan-reclamation", intents[0].ID)
	isolated, err := os.ReadFile(isolation)
	require.NoError(t, err)
	db, _, err := dbsql.NewIndexDB("rollback", "WAL", 100, 32, false)
	require.NoError(t, err)
	defer db.Close()
	row := keep.FileInfo
	row.Path = "/keep.jpg"
	row.Image = &iteminfo.ImageIndexMeta{Version: "e3-fixture:keep", ObsSeq: 19, Guard: 19, State: iteminfo.ImageMetaUnknown,
		LogicalSize: row.Size, MTimeNS: row.ModTime.UnixNano()}
	require.NoError(t, db.InsertItem(keep.Source, row.Path, &row))
	work, err := db.GetImageWorksByPaths(keep.Source, []string{row.Path})
	require.NoError(t, err)
	guard := work[row.Path].Guard
	ready, err := db.CommitImageMetadata(keep.Source, row.Path, row.Image.Version, guard, dbsql.ImageMetadataCommit{
		State: iteminfo.ImageMetaReady, Width: 800, Height: 600, Orientation: 1, Ratio: 4.0 / 3.0,
	})
	require.NoError(t, err)
	require.True(t, ready)
	require.NoError(t, db.PreparePreviewWarmup(t.Context()))
	_, err = db.Exec("UPDATE index_items SET warmup_guard=image_guard,warmup_attempts=-1 WHERE path='/keep.jpg'")
	require.NoError(t, err)
	expected := capacityRollbackProof{
		KeepOriginal: cacheledger.Digest(keepOriginal), KeepPreview: cacheledger.Digest(kept),
		ColdOriginal: cacheledger.Digest(coldOriginal), ColdAsset: plan.ref.Asset, ColdVariant: plan.variant,
		Isolation: isolation, IsolatedDigest: cacheledger.Digest(isolated), Guard: guard,
	}
	if os.Getenv("MUMENT_SPACE_CONTAINER_TEST") == "1" {
		expected.SourceMount = capacityMountID(t, f.root)
		expected.CacheMount = capacityMountID(t, filepath.Join(f.cache, "diskcache"))
	}
	raw, err := json.Marshal(expected)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "capacity.json"), raw, 0o600))
}

func TestCapacityReturnAfterRollback(t *testing.T) {
	dir := os.Getenv("MUMENT_CAPACITY_ROLLBACK")
	if dir == "" {
		t.Skip("after a frozen older service has rebuilt the evicted preview")
	}
	data, err := os.ReadFile(filepath.Join(dir, "capacity.json"))
	require.NoError(t, err)
	var proof capacityRollbackProof
	require.NoError(t, json.Unmarshal(data, &proof))
	f := &orphanFixture{root: filepath.Join(dir, "source"), cache: filepath.Join(dir, "cache")}
	indexing.SetTestIndex("orphan-test", f.root)
	t.Cleanup(indexing.ClearTestIndices)
	cfg := settings.Config.Server.PreviewSpace
	settings.Config.Server.PreviewSpace = settings.PreviewSpaceConfig{Mode: "observe"}
	t.Cleanup(func() { settings.Config.Server.PreviewSpace = cfg })
	f.start(t)
	cold := filepath.Join(f.root, "cold.jpg")
	stat, err := os.Stat(cold)
	require.NoError(t, err)
	file := testFileInfoFromStatForReview(cold, stat)
	file.Source = "orphan-test"
	cached, err := f.s.getPreviewForFile(t.Context(), file, warmupSize, "", 0)
	require.NoError(t, err)
	require.NotEmpty(t, cached)
	require.Zero(t, f.s.WarmupMetrics().FullDecodes)
	cooling, err := f.s.ledger.CoolingUntil(t.Context(), proof.ColdAsset, proof.ColdVariant)
	require.NoError(t, err)
	require.False(t, cooling.IsZero())
	f.s.lastInteractive.Store(0)
	require.ErrorIs(t, testWarm(t, t.Context(), f.s, file, func(int64) error { return nil }), ErrWarmupCached)
	intents, err := f.s.ledger.CapacityIntents(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, intents, 1)
	actual, err := os.ReadFile(proof.Isolation)
	require.NoError(t, err)
	require.Equal(t, proof.IsolatedDigest, cacheledger.Digest(actual))
	t.Log("the older build's on-demand replacement was reused with zero decodes, and the capacity isolation remained intact")
}

func capacityMountID(t *testing.T, path string) uint64 {
	t.Helper()
	var stat unix.Statx_t
	require.NoError(t, unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_MNT_ID_UNIQUE, &stat))
	require.NotZero(t, stat.Mask&unix.STATX_MNT_ID_UNIQUE)
	return stat.Mnt_id
}

func TestCapacityContainerResume(t *testing.T) {
	if os.Getenv("MUMENT_SPACE_CONTAINER_TEST") != "1" || os.Getenv("MUMENT_CAPACITY_ROLLBACK") == "" {
		t.Skip("second container with the same synthetic data bind")
	}
	dir := os.Getenv("MUMENT_CAPACITY_ROLLBACK")
	raw, err := os.ReadFile(filepath.Join(dir, "capacity.json"))
	require.NoError(t, err)
	var proof capacityRollbackProof
	require.NoError(t, json.Unmarshal(raw, &proof))
	f := &orphanFixture{root: filepath.Join(dir, "source"), cache: filepath.Join(dir, "cache")}
	require.NotEqual(t, proof.SourceMount, capacityMountID(t, f.root))
	require.NotEqual(t, proof.CacheMount, capacityMountID(t, filepath.Join(f.cache, "diskcache")))
	indexing.SetTestIndex("orphan-test", f.root)
	t.Cleanup(indexing.ClearTestIndices)
	f.start(t)
	now := time.Now().Add(3 * time.Hour)
	m := testCapacity(t, f, now, true)
	resumed := m.report(t.Context(), CapacityCursor{}, true)
	require.Equal(t, 1, resumed.Unlinked, "%+v", resumed)
	require.NoFileExists(t, proof.Isolation)
	next := m.report(t.Context(), CapacityCursor{}, true)
	require.Zero(t, next.Unlinked)
	original, err := os.ReadFile(filepath.Join(f.root, "keep.jpg"))
	require.NoError(t, err)
	require.Equal(t, proof.KeepOriginal, cacheledger.Digest(original))
	t.Log("changed both unique mount IDs with persistent data, resumed the capacity intent exactly once")
}
