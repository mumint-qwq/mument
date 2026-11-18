// Added for mument 007-C.
package preview

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/stretchr/testify/require"
)

func TestLedgerCorruptionLegacyAndRestart(t *testing.T) {
	ctx := t.Context()
	root, cache := t.TempDir(), t.TempDir()
	indexing.SetTestIndex("ledger-test", root)
	t.Cleanup(indexing.ClearTestIndices)
	file := newNoisyFile(t, root, "source.jpg", 800, 600)
	file.Source = "ledger-test"
	version, err := cacheledger.Observe(file.RealPath)
	require.NoError(t, err)
	if !version.Strong {
		t.Skip("strong observations unavailable")
	}
	legacy := NewPreviewGenerator(1, cache)
	legacyFile := file
	legacyFile.Source = ""
	expected, err := legacy.getPreviewForFile(ctx, legacyFile, "small", "", 0)
	require.NoError(t, err)
	require.NoError(t, legacy.Shutdown(ctx))

	svc := NewPreviewGenerator(1, cache)
	svc.startLedger()
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(context.Background())) })
	first, err := svc.getPreviewForFile(ctx, file, "small", "", 0)
	require.NoError(t, err)
	require.Equal(t, expected, first)
	require.Equal(t, uint64(1), svc.Stats().FullDecodes, "unproved old keys must miss")
	plan, err := svc.planLedgerPreview(ctx, file, "small")
	require.NoError(t, err)
	require.NoError(t, svc.fileCache.Store(ctx, plan.key, bytes.Repeat([]byte{1}, len(first))))
	repaired, err := svc.getPreviewForFile(ctx, file, "small", "", 0)
	require.NoError(t, err)
	require.Equal(t, first, repaired)
	require.Equal(t, uint64(2), svc.Stats().FullDecodes)
	require.Equal(t, uint64(1), svc.Stats().CacheCorrupt)

	// A physical old key is usable if its exact bytes and active source
	// generation have independent publication evidence.
	legacyKey := CacheKey(previewCacheHash(file), "small", 0)
	require.NoError(t, svc.ledger.Publish(ctx, plan.ref, plan.variant, legacyKey, first))
	adopted, err := svc.getPreviewForFile(ctx, file, "small", "", 0)
	require.NoError(t, err)
	require.Equal(t, first, adopted)
	require.Equal(t, uint64(2), svc.Stats().FullDecodes)

	require.NoError(t, svc.Shutdown(ctx))
	oldAgain := NewPreviewGenerator(1, cache)
	require.NoError(t, oldAgain.fileCache.Store(ctx, legacyKey, bytes.Repeat([]byte{2}, len(first))))
	require.NoError(t, oldAgain.Shutdown(ctx))
	resumed := NewPreviewGenerator(1, cache)
	resumed.startLedger()
	t.Cleanup(func() { require.NoError(t, resumed.Shutdown(context.Background())) })
	actual, err := resumed.getPreviewForFile(ctx, file, "small", "", 0)
	require.NoError(t, err)
	require.Equal(t, first, actual)
	require.Equal(t, uint64(1), resumed.Stats().FullDecodes, "untrusted old-writer replacement must miss")
	require.Equal(t, uint64(1), resumed.Stats().CacheCorrupt)
}

func TestLedgerOriginalDoesNotBecomeReady(t *testing.T) {
	root := t.TempDir()
	indexing.SetTestIndex("ledger-test", root)
	t.Cleanup(indexing.ClearTestIndices)
	svc := newAdmissionService(t, 1, 128)
	svc.startLedger()
	var source bytes.Buffer
	require.NoError(t, jpeg.Encode(&source, image.NewGray(image.Rect(0, 0, 48, 24)), nil))
	file := testFile(t, root, "tiny.jpg", source.Bytes(), "image/jpeg")
	file.Source = "ledger-test"
	first, err := svc.getPreviewForFile(t.Context(), file, "small", "", 0)
	require.NoError(t, err)
	require.Equal(t, source.Bytes(), first)
	plan, err := svc.planLedgerPreview(t.Context(), file, "small")
	require.NoError(t, err)
	_, found, err := svc.ledger.Lookup(t.Context(), plan.ref, plan.variant)
	require.NoError(t, err)
	require.False(t, found)
	require.Equal(t, uint64(1), svc.Stats().OriginalServed)
	require.Zero(t, svc.Stats().FullDecodes)
}

func TestLedgerGenerationInterruptedByRename(t *testing.T) {
	root := t.TempDir()
	indexing.SetTestIndex("ledger-test", root)
	t.Cleanup(indexing.ClearTestIndices)
	svc := newAdmissionService(t, 1, 128)
	svc.startLedger()
	file := newNoisyFile(t, root, "source.jpg", 800, 600)
	file.Source = "ledger-test"
	version, err := cacheledger.Observe(file.RealPath)
	require.NoError(t, err)
	if !version.Strong {
		t.Skip("strong observations unavailable")
	}
	entered, release := blockGeneration(svc)
	done := make(chan error, 1)
	go func() { _, err := svc.getPreviewForFile(context.Background(), file, "small", "", 0); done <- err }()
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	<-entered
	plan, err := svc.planLedgerPreview(t.Context(), file, "small")
	require.NoError(t, err)
	target := filepath.Join(root, "target.jpg")
	ns, name, ok := cacheledger.Location(root, target)
	require.True(t, ok)
	intents, err := svc.ledger.Prepare(t.Context(), []cacheledger.Move{{Ref: plan.ref, TargetSource: ns, TargetPath: name}})
	require.NoError(t, err)
	require.NoError(t, os.Rename(file.RealPath, target))
	after, err := cacheledger.Observe(target)
	require.NoError(t, err)
	require.NoError(t, svc.ledger.CommitRename(t.Context(), intents[0].ID, after))
	unblock()
	require.ErrorIs(t, <-done, ErrPreviewSourceChanged)
	ref, ok, err := svc.ledger.Existing(t.Context(), ns, name, after)
	require.NoError(t, err)
	require.True(t, ok)
	_, found, err := svc.ledger.Lookup(t.Context(), ref, plan.variant)
	require.NoError(t, err)
	require.False(t, found)
	file.RealPath, file.Name = target, "target.jpg"
	bytes, err := svc.getPreviewForFile(t.Context(), file, "small", "", 0)
	require.NoError(t, err)
	require.NotEmpty(t, bytes)
}

func TestLedgerShutdownWhileRecoveryWaitsForLocationGate(t *testing.T) {
	svc := newAdmissionService(t, 1, 128)
	svc.startLedger()
	require.NotNil(t, svc.ledger)
	unlock, err := svc.ledger.LockLocations(t.Context())
	require.NoError(t, err)
	defer unlock()
	svc.ledger.RequestRecovery()
	require.Eventually(t, func() bool { return len(svc.ledger.RecoveryRequested()) == 0 }, time.Second, time.Millisecond)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.NoError(t, svc.Shutdown(ctx))
	_, err = svc.ledger.RecoveryCutoff(ctx)
	require.Error(t, err)
}
