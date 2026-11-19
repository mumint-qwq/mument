// Added for mument 007-C. Run with -tags cacheledger_portable to exercise
// the portable adapter on a Linux host.
package preview

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/stretchr/testify/require"
)

func TestLedgerUncachedFlightIdentity(t *testing.T) {
	root := t.TempDir()
	indexing.SetTestIndex("ledger-test", root)
	t.Cleanup(indexing.ClearTestIndices)
	svc := newAdmissionService(t, 2, 128)
	a := newNoisyFile(t, root, "a.jpg", 800, 600)
	b := newNoisyFile(t, root, "b.jpg", 800, 600)
	a.Source, b.Source = "ledger-test", "ledger-test"
	require.NoError(t, os.Chtimes(b.RealPath, a.ModTime, a.ModTime))
	b.ModTime = a.ModTime
	ctx := t.Context()
	first, err := svc.planLedgerPreview(ctx, a, "small")
	require.NoError(t, err)
	again, err := svc.planLedgerPreview(ctx, a, "small")
	require.NoError(t, err)
	require.Equal(t, first.key, again.key)
	require.Empty(t, first.ref.Asset)
	otherFile, err := svc.planLedgerPreview(ctx, b, "small")
	require.NoError(t, err)
	require.NotEqual(t, first.key, otherFile.key)
	otherVariant, err := svc.planLedgerPreview(ctx, a, "large")
	require.NoError(t, err)
	require.NotEqual(t, first.key, otherVariant.key)

	mtime := a.ModTime.Add(time.Second)
	require.NoError(t, os.Chtimes(a.RealPath, mtime, mtime))
	a.ModTime = mtime
	changed, err := svc.planLedgerPreview(ctx, a, "small")
	require.NoError(t, err)
	require.NotEqual(t, first.key, changed.key)
}

func TestLedgerUncachedFlightSharesWithCancellationAndLimits(t *testing.T) {
	root := t.TempDir()
	indexing.SetTestIndex("ledger-test", root)
	t.Cleanup(indexing.ClearTestIndices)

	for _, tc := range []struct {
		name       string
		withLedger bool
	}{
		{name: "without-ledger"},
		{name: "with-ledger", withLedger: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newAdmissionService(t, 2, 128)
			if tc.withLedger {
				svc.startLedger()
				require.NotNil(t, svc.ledger)
			}
			file := newNoisyFile(t, root, "same.jpg", 800, 600)
			file.Source = "ledger-test"
			plan, err := svc.planLedgerPreview(t.Context(), file, "small")
			require.NoError(t, err)
			require.Equal(t, tc.withLedger && plan.version.Strong, plan.ref.Asset != "")
			svc.flights.maxWaiters = 2
			entered, release := blockGeneration(svc)
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			canceled := make(chan error, 1)
			go func() { _, err := svc.getPreviewForFile(ctx, file, "small", "", 0); canceled <- err }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("first task did not enter generation")
			}

			// Fill spare entry slots to verify a same-key waiter joins even
			// when a new variant cannot start another task.
			held := svc.tasks.capacity() - 1
			for range held {
				require.True(t, svc.tasks.tryAcquire())
			}
			releaseSlots := sync.OnceFunc(func() {
				for range held {
					svc.tasks.release()
				}
			})
			defer releaseSlots()

			type response struct {
				data []byte
				err  error
			}
			kept := make(chan response, 1)
			go func() {
				data, err := svc.getPreviewForFile(t.Context(), file, "small", "", 0)
				kept <- response{data, err}
			}()
			require.Eventually(t, func() bool { return svc.Stats().SharedWaits == 1 }, 2*time.Second, time.Millisecond)
			_, err = svc.getPreviewForFile(t.Context(), file, "small", "", 0)
			require.ErrorIs(t, err, ErrPreviewSaturated, "the shared key still consumes waiter slots")
			cancel()
			require.ErrorIs(t, <-canceled, context.Canceled)
			require.Zero(t, svc.Stats().CanceledQueued)
			_, err = svc.getPreviewForFile(t.Context(), file, "large", "", 0)
			require.ErrorIs(t, err, ErrPreviewSaturated, "a distinct variant still needs an entry slot")
			require.Equal(t, uint64(1), svc.Stats().EntryRejected)
			releaseSlots()
			unblock()
			result := <-kept
			require.NoError(t, result.err)
			require.NotEmpty(t, result.data)
			require.Equal(t, uint64(1), svc.Stats().FullDecodes)
			require.NoError(t, svc.flights.wait(t.Context()))

			again, err := svc.getPreviewForFile(t.Context(), file, "small", "", 0)
			require.NoError(t, err)
			require.Equal(t, result.data, again)
			if plan.ref.Asset == "" {
				require.Equal(t, uint64(2), svc.Stats().FullDecodes, "a finished uncached task cannot serve a later request")
				_, found, err := svc.fileCache.Load(t.Context(), plan.key)
				require.NoError(t, err)
				require.False(t, found, "no persistent object uses the flight key")
			} else {
				require.Equal(t, uint64(1), svc.Stats().FullDecodes)
			}
		})
	}
}

func TestLedgerWeakFlightRejectsSameStampReplacement(t *testing.T) {
	root := t.TempDir()
	indexing.SetTestIndex("ledger-test", root)
	t.Cleanup(indexing.ClearTestIndices)
	svc := newAdmissionService(t, 2, 128)
	file := newNoisyFile(t, root, "same.jpg", 800, 600)
	file.Source = "ledger-test"
	plan, err := svc.planLedgerPreview(t.Context(), file, "small")
	require.NoError(t, err)
	if plan.version.Strong {
		t.Skip("run with -tags cacheledger_portable")
	}

	entered, release := blockGeneration(svc)
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	done := make(chan error, 2)
	request := func() { _, err := svc.getPreviewForFile(t.Context(), file, "small", "", 0); done <- err }
	go request()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first task did not enter generation")
	}
	replacement := newNoisyFile(t, root, "replacement.jpg", 800, 600)
	require.NoError(t, os.Chtimes(replacement.RealPath, file.ModTime, file.ModTime))
	require.NoError(t, os.Rename(replacement.RealPath, file.RealPath))
	current, err := svc.planLedgerPreview(t.Context(), file, "small")
	require.NoError(t, err)
	require.Equal(t, plan.version, current.version, "the portable stat stamp aliases")
	require.False(t, os.SameFile(plan.weakInfo, current.weakInfo))
	require.Equal(t, plan.key, current.key)

	// A replacement that occurs after the worker's final check must also
	// fail at the waiter, regardless of which inode the waiter observed.
	oldOutcome := previewResult{data: []byte("old"), weakInfo: plan.weakInfo}
	_, err = svc.deliverPreview(t.Context(), current, oldOutcome, true)
	require.ErrorIs(t, err, ErrPreviewSourceChanged)
	_, err = svc.deliverPreview(t.Context(), plan, oldOutcome, true)
	require.ErrorIs(t, err, ErrPreviewSourceChanged)

	go request()
	require.Eventually(t, func() bool { return svc.Stats().SharedWaits == 1 }, 2*time.Second, time.Millisecond)
	unblock()
	require.ErrorIs(t, <-done, ErrPreviewSourceChanged)
	require.ErrorIs(t, <-done, ErrPreviewSourceChanged)
	data, err := svc.getPreviewForFile(t.Context(), file, "small", "", 0)
	require.NoError(t, err)
	require.NotEmpty(t, data)
}
