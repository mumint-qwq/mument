//go:build linux && !386 && !arm && !cacheledger_portable

package previewwarmup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
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
	live, err := w.Retention(t.Context(), "")
	require.NoError(t, err)
	require.EqualValues(t, 1, live.Reasons["live_verified_observation"])
	require.Equal(t, "/Pictures/source.jpg", live.Samples[0].Path)
	moved := filepath.Join(root, "Pictures", "moved.jpg")
	require.NoError(t, os.Rename(path, moved))
	require.NoError(t, idx.RefreshDirectory("/Pictures/", false))
	c, err := db.PreviewContentExact(t.Context(), "library", "/Pictures/moved.jpg")
	require.NoError(t, err)
	w = makeWorker()
	require.True(t, w.processReuse(t.Context(), &w.scopes[0], c))
	require.Equal(t, uint64(1), w.Snapshot().Counts["reused"])
	require.EqualValues(t, 1, w.Snapshot().CandidateChecks)
	require.EqualValues(t, 1, w.Snapshot().PhysicalHits)
	require.Positive(t, w.Snapshot().PreviewBytes)
	retained, err := w.Retention(t.Context(), "")
	require.NoError(t, err)
	require.EqualValues(t, 1, retained.Reasons["missing_or_unreadable_unconfirmed"])
	require.Equal(t, 2, retained.Samples[0].References)
	require.False(t, retained.Samples[0].Capped)
	read := w.Snapshot().ReadBytes
	w = makeWorker()
	require.False(t, w.processReuse(t.Context(), &w.scopes[0], c))
	require.Zero(t, w.Snapshot().ReadBytes)
	require.Positive(t, read)
	require.NoError(t, w.Control("pause"))
	require.Error(t, w.allow(t.Context()))
	require.NoError(t, w.Control("resume"))
	// Replacing a reference through old C/E3 APIs without a new B file version
	// must invalidate F completion, while keeping historical results separate.
	ns, name, ok := cacheledger.Location(root, moved)
	require.True(t, ok)
	v, err := cacheledger.Observe(moved)
	require.NoError(t, err)
	ref, err := w.repo.FindRef(t.Context(), ns, name, v)
	require.NoError(t, err)
	require.NoError(t, w.repo.Detach(t.Context(), ref))
	info.Name = "moved.jpg"
	info.RealPath = moved
	_, err = preview.GetPreviewForFile(t.Context(), info, "small", "", 0)
	require.NoError(t, err)
	time.Sleep(1100 * time.Millisecond)
	w = makeWorker()
	require.True(t, w.processReuse(t.Context(), &w.scopes[0], c))
	require.EqualValues(t, 1, w.Snapshot().Counts["proven_own"])
	require.Positive(t, w.Snapshot().ReadBytes)
	// The administrator audit never treats replacement of a scope root as loss.
	gallery := filepath.Join(root, "Pictures")
	saved := filepath.Join(root, "saved")
	require.NoError(t, os.Rename(gallery, saved))
	require.NoError(t, os.Mkdir(gallery, 0700))
	audit, err := w.Retention(t.Context(), "")
	require.NoError(t, err)
	require.EqualValues(t, len(audit.Samples), audit.Reasons["scope_identity_unavailable"])
	require.NoError(t, os.Remove(gallery))
	require.NoError(t, os.Rename(saved, gallery))
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

func TestContentHintsCannotStarveValidWork(t *testing.T) {
	db, idx, root := warmupTestIndex(t)
	cache := t.TempDir()
	require.NoError(t, preview.StartPreviewGeneratorWithBudget(1, 128, cache))
	svc := preview.GetService()
	t.Cleanup(func() { require.NoError(t, preview.Shutdown(context.Background())) })
	source := filepath.Join(root, "Pictures", "z-valid.jpg")
	makePhoto(t, source, 1600, 1200)
	require.NoError(t, idx.RefreshDirectory("/Pictures/", false))
	info := iteminfo.ExtendedFileInfo{Source: "library", RealPath: source, FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: "z-valid.jpg", Type: "image/jpeg", HasPreview: true}}}
	_, err := preview.GetPreviewForFile(t.Context(), info, "small", "", 0)
	require.NoError(t, err)
	time.Sleep(1100 * time.Millisecond)
	w := &ReuseWorker{db: db, svc: svc, repo: svc.ContentLedger(), probe: fakePressure(t, cache), done: make(chan struct{}), scopes: []scope{{config: settings.PreviewWarmupScope{Source: "library", Directory: "Pictures"}, sourceRoot: root, root: filepath.Join(root, "Pictures"), prefix: "/Pictures/"}}, status: ReuseStatus{Enabled: true, Counts: map[string]uint64{}}}
	v, err := cacheledger.Observe(source)
	require.NoError(t, err)
	for i := 0; i < 17; i++ {
		require.NoError(t, w.repo.HintContent(t.Context(), "aaa-removed", filepath.Join(root, fmt.Sprintf("removed-%02d.jpg", i)), v, false))
	}
	require.NoError(t, w.repo.HintContent(t.Context(), "library", source, v, true))
	w.hints(t.Context())
	require.EqualValues(t, 16, w.Snapshot().Counts["out_of_scope_hint"])
	w.hints(t.Context())
	require.EqualValues(t, 1, w.Snapshot().Counts["proven_own"])
	future := filepath.Join(root, "Pictures", "not-indexed.jpg")
	require.NoError(t, w.repo.HintContent(t.Context(), "library", future, v, true))
	w.hints(t.Context())
	require.EqualValues(t, 1, w.Snapshot().Counts["hint_rediscovery"])
	hints, err := w.repo.ContentHints(t.Context())
	require.NoError(t, err)
	require.Empty(t, hints)
	bytes, err := os.ReadFile(source)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(future, bytes, 0600))
	past := time.Now().Add(-time.Minute)
	require.NoError(t, os.Chtimes(future, past, past))
	require.NoError(t, idx.RefreshDirectory("/Pictures/", false))
	before := svc.Stats().FullDecodes
	ctx, cancel := context.WithCancel(t.Context())
	go w.run(ctx)
	t.Cleanup(func() { cancel(); require.NoError(t, w.Wait(context.Background())) })
	require.Eventually(t, func() bool { return w.Snapshot().Counts["reused"] == 1 }, 5*time.Second, 50*time.Millisecond)
	require.Equal(t, before, svc.Stats().FullDecodes)
}
