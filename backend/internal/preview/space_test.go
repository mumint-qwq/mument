//go:build linux && !386 && !arm && !cacheledger_portable

package preview

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
)

func TestSpaceWatermarksAndReservations(t *testing.T) {
	m := newSpaceManager(nil, settings.PreviewSpaceConfig{
		Mode: "enforce", BudgetMiB: 4, MinimumFreeMiB: 1, ResumeFreeMiB: 2, StableSeconds: 1,
	})
	now := time.Unix(100, 0)
	m.now = func() time.Time { return now }
	highFree := diskcache.SpaceSample{Available: 20 * spaceMiB, Block: 4096, Inodes: 100, HasInodes: true}
	m.status.Filesystem = highFree
	m.endInventory(now, 0, diskcache.Usage{Allocated: spaceMiB}, nil)
	require.Equal(t, "recovering", m.status.State)
	now = now.Add(time.Second)
	m.evaluate(now)
	require.Equal(t, "healthy", m.status.State)

	first, err := m.reserve(highFree, 2*spaceMiB, true)
	require.NoError(t, err)
	_, err = m.reserve(highFree, 2*spaceMiB, false)
	require.ErrorContains(t, err, "budget_limit")
	m.finish(first, 2*spaceMiB, 2*spaceMiB, nil)
	require.Zero(t, m.status.ReservedBytes)

	m.status.Filesystem.Inodes = 31
	m.evaluate(now)
	require.Equal(t, "low_inodes", m.status.Reason)
	m.status.Filesystem = highFree
	m.evaluate(now)
	require.Equal(t, "waiting_for_watermarks", m.status.Reason)
	m.status.Usage.Allocated -= 4096
	m.evaluate(now)
	require.Equal(t, "recovering", m.status.State)
	m.status.Filesystem.Available = 100
	m.evaluate(now)
	require.Equal(t, "low_free", m.status.Reason)
	m.status.Filesystem = highFree
	m.evaluate(now.Add(time.Second))
	require.Equal(t, "recovering", m.status.State)
	m.evaluate(now.Add(2 * time.Second))
	require.Equal(t, "healthy", m.status.State)
	m.status.Filesystem.ReadOnly = true
	m.evaluate(now.Add(2 * time.Second))
	require.Equal(t, "read_only", m.status.Reason)
}

func TestSpaceForegroundHitAndUnstoredMiss(t *testing.T) {
	f := startOrphanFixture(t)
	require.NoError(t, f.s.Shutdown(t.Context()))
	previous := settings.Config.Server.PreviewSpace
	t.Cleanup(func() { settings.Config.Server.PreviewSpace = previous })
	settings.Config.Server.PreviewSpace = settings.PreviewSpaceConfig{
		Mode: "enforce", BudgetMiB: 1, MinimumFreeMiB: 1, ResumeFreeMiB: 2, StableSeconds: 1,
	}
	root := filepath.Join(f.cache, "diskcache")
	require.NoError(t, os.WriteFile(filepath.Join(root, "protected-old"), make([]byte, spaceMiB), 0o600))
	f.start(t)
	require.Eventually(t, func() bool { return f.s.space.snapshot().Complete }, 5*time.Second, 25*time.Millisecond)
	require.Equal(t, "high_usage", f.s.space.snapshot().Reason)
	require.ErrorContains(t, f.s.WarmupSpace(), "space_high_usage")

	file := newNoisyFile(t, f.root, "hit.jpg", 800, 600)
	require.NoError(t, os.Remove(file.RealPath))
	file.RealPath, file.Source, file.Name = f.source, "orphan-test", filepath.Base(f.source)
	file.ModTime = time.Time{}
	before := f.s.WarmupMetrics().FullDecodes
	data, err := f.s.getPreviewForFile(t.Context(), file, "small", "", 0)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	require.Equal(t, before, f.s.WarmupMetrics().FullDecodes)

	miss := newNoisyFile(t, f.root, "new.jpg", 1000, 800)
	miss.Source = "orphan-test"
	data, err = f.s.getPreviewForFile(t.Context(), miss, "small", "", 0)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	plan, err := f.s.planLedgerPreview(t.Context(), miss, "small")
	require.NoError(t, err)
	_, found, err := f.s.ledger.Lookup(t.Context(), plan.ref, plan.variant)
	require.NoError(t, err)
	require.False(t, found)
	require.NoFileExists(t, filepath.Join(root, diskcache.RelativePath(plan.key)))
	require.Equal(t, uint64(1), f.s.space.snapshot().ForegroundUnstored)
	require.Zero(t, f.s.space.snapshot().ReservedBytes)
}

func TestSpaceChangedRootAcrossRestartKeepsPublication(t *testing.T) {
	f := startOrphanFixture(t)
	require.NoError(t, f.s.Shutdown(t.Context()))
	previous := settings.Config.Server.PreviewSpace
	t.Cleanup(func() { settings.Config.Server.PreviewSpace = previous })
	settings.Config.Server.PreviewSpace = settings.PreviewSpaceConfig{
		Mode: "enforce", BudgetMiB: 4, MinimumFreeMiB: 1, ResumeFreeMiB: 2, StableSeconds: 1,
	}
	f.start(t)
	require.Eventually(t, func() bool { return f.s.space.snapshot().Complete }, 5*time.Second, 25*time.Millisecond)
	require.NoError(t, f.s.Shutdown(t.Context()))
	root := filepath.Join(f.cache, "diskcache")
	require.NoError(t, os.Rename(root, root+".saved"))
	require.NoError(t, os.Mkdir(root, 0o700))
	f.start(t)
	require.ErrorContains(t, f.s.WarmupSpace(), "identity_changed")
	require.Nil(t, f.s.space.tree)
	original := filepath.Join(root+".saved", diskcache.RelativePath(f.key))
	require.FileExists(t, original)
	refBefore, found, err := f.s.ledger.Lookup(t.Context(), f.ref, f.variant)
	require.NoError(t, err)
	require.True(t, found)

	st, err := os.Stat(f.source)
	require.NoError(t, err)
	file := testFileInfoFromStatForReview(f.source, st)
	file.Source, file.Type = "orphan-test", "image/jpeg"
	data, err := f.s.getPreviewForFile(t.Context(), file, "small", "", 0)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	refAfter, found, err := f.s.ledger.Lookup(t.Context(), f.ref, f.variant)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, refBefore, refAfter)
	require.NoFileExists(t, f.physical)
	require.FileExists(t, original)
}
