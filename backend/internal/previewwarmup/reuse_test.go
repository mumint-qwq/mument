//go:build linux && !386 && !arm && !cacheledger_portable

package previewwarmup

import (
	"context"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestContentWorkerIndependentDiscoveryAndRestart(t *testing.T) {
	db, idx, root := warmupTestIndex(t)
	cache := t.TempDir()
	require.NoError(t, preview.StartPreviewGeneratorWithBudget(1, 128, cache))
	svc := preview.GetService()
	t.Cleanup(func() { require.NoError(t, preview.Shutdown(context.Background())) })
	require.NoError(t, svc.ContentCacheReady(t.Context()))
	path := filepath.Join(root, "Pictures", "source.jpg")
	makePhoto(t, path, 1600, 1200)
	require.NoError(t, idx.RefreshDirectory("/Pictures/", false))
	info := iteminfo.ExtendedFileInfo{Source: "library", RealPath: path, FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: "source.jpg", Type: "image/jpeg", HasPreview: true}}}
	for _, size := range []string{"small", "xlarge"} {
		_, err := preview.GetPreviewForFile(t.Context(), info, size, "", 0)
		require.NoError(t, err)
	}
	makeWorker := func() *ReuseWorker {
		return &ReuseWorker{db: db, svc: svc, repo: svc.ContentLedger(), probe: fakePressure(t, cache), scopes: []scope{{config: settings.PreviewWarmupScope{Source: "library", Directory: "Pictures"}, sourceRoot: root, root: filepath.Join(root, "Pictures"), prefix: "/Pictures/"}}, status: ReuseStatus{Enabled: true, Counts: map[string]uint64{}}}
	}
	w := makeWorker()
	time.Sleep(1100 * time.Millisecond)
	page, err := db.PreviewContentExact(t.Context(), "library", "/Pictures/source.jpg")
	require.NoError(t, err)
	require.True(t, w.processReuse(t.Context(), &w.scopes[0], page))
	require.Equal(t, uint64(1), w.Snapshot().Counts["proven_own"])
	moved := filepath.Join(root, "Pictures", "moved.jpg")
	require.NoError(t, os.Rename(path, moved))
	require.NoError(t, idx.RefreshDirectory("/Pictures/", false))
	c, err := db.PreviewContentExact(t.Context(), "library", "/Pictures/moved.jpg")
	require.NoError(t, err)
	w = makeWorker()
	require.True(t, w.processReuse(t.Context(), &w.scopes[0], c))
	require.Equal(t, uint64(1), w.Snapshot().Counts["reused"])
	read := w.Snapshot().ReadBytes
	w = makeWorker()
	require.False(t, w.processReuse(t.Context(), &w.scopes[0], c))
	require.Zero(t, w.Snapshot().ReadBytes)
	require.Positive(t, read)
	require.NoError(t, w.Control("pause"))
	require.Error(t, w.allow(t.Context()))
	require.NoError(t, w.Control("resume"))
	require.NoError(t, db.PreparePreviewWarmup(t.Context())) // D can initialize later, independently.
}
func TestContentWorkerDisabledHasNoWork(t *testing.T) {
	w := NewReuse(t.Context(), settings.Server{}, nil, nil)
	require.NoError(t, w.Wait(t.Context()))
	require.False(t, w.Snapshot().Enabled)
	require.Zero(t, w.Snapshot().ReadBytes)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	require.NoError(t, w.Wait(cancelled))
}
