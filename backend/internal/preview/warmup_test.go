//go:build linux && !386 && !arm

package preview

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/stretchr/testify/require"
)

func warmupTestSource(t *testing.T) (*Service, iteminfo.ExtendedFileInfo) {
	t.Helper()
	root := t.TempDir()
	indexing.SetTestIndex("warmup", root)
	t.Cleanup(indexing.ClearTestIndices)
	svc := newAdmissionService(t, 1, 128)
	svc.startLedger()
	file := newNoisyFile(t, root, "photo.jpg", 1600, 1200)
	file.Source = "warmup"
	return svc, file
}

func testWarm(t *testing.T, ctx context.Context, svc *Service, file iteminfo.ExtendedFileInfo, check func(int64) error) error {
	t.Helper()
	info, err := os.Stat(file.RealPath)
	if err != nil {
		return err
	}
	stat := func(context.Context) (os.FileInfo, error) {
		latest, err := os.Stat(file.RealPath)
		if err != nil || verifySourceUnchanged(info, latest) != nil {
			return nil, ErrPreviewSourceChanged
		}
		return latest, nil
	}
	open := func(ctx context.Context) (*os.File, error) {
		if _, err := stat(ctx); err != nil {
			return nil, err
		}
		return os.Open(file.RealPath)
	}
	return svc.WarmPreviewWhenIdle(ctx, file, open, stat, check)
}

func TestWarmupPublicationAndForegroundUse(t *testing.T) {
	svc, file := warmupTestSource(t)
	ctx := t.Context()
	check := func(int64) error { return nil }
	require.NoError(t, testWarm(t, ctx, svc, file, check))
	require.ErrorIs(t, testWarm(t, ctx, svc, file, check), ErrWarmupCached)
	require.EqualValues(t, 1, svc.stats.fullDecodes.Load())
	plan, err := svc.planLedgerPreview(ctx, file, "xlarge")
	require.NoError(t, err)
	result, found, err := svc.ledger.Lookup(ctx, plan.ref, plan.variant)
	require.NoError(t, err)
	require.True(t, found)
	require.Zero(t, result.LastUsedAt)
	data, err := svc.getPreviewForFile(ctx, file, "xlarge", "", 0)
	require.NoError(t, err)
	require.True(t, result.Matches(data))
	require.EqualValues(t, 1, svc.stats.fullDecodes.Load())
	result, found, err = svc.ledger.Lookup(ctx, plan.ref, plan.variant)
	require.NoError(t, err)
	require.True(t, found)
	require.GreaterOrEqual(t, result.LastUsedAt, result.GeneratedAt)
}

func TestWarmupJoinsAndCancellation(t *testing.T) {
	for _, scenario := range []string{"foreground_before_permit", "foreground_cancels", "warmup_cancels", "same_key"} {
		t.Run(scenario, func(t *testing.T) {
			svc, file := warmupTestSource(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			stop := func() { once.Do(func() { close(release) }) }
			t.Cleanup(stop)
			check := func(int64) error { return nil }
			if scenario == "foreground_before_permit" {
				check = func(int64) error {
					close(entered)
					<-release
					return nil
				}
			} else {
				svc.testHooks = &previewTestHooks{beforeGenerate: func() {
					close(entered)
					<-release
				}}
			}
			bgCtx, cancelWarmup := context.WithCancel(t.Context())
			defer cancelWarmup()
			warmDone := make(chan error, 1)
			go func() { warmDone <- testWarm(t, bgCtx, svc, file, check) }()
			select {
			case <-entered:
			case err := <-warmDone:
				t.Fatalf("warmup returned before admission hook: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("warmup did not reach admission hook")
			}
			require.Zero(t, svc.stats.taskSlotsInUse.Load())
			fgCtx, cancelForeground := context.WithCancel(t.Context())
			defer cancelForeground()
			type response struct {
				data []byte
				err  error
			}
			got := make(chan response, 1)
			go func() {
				data, err := svc.getPreviewForFile(fgCtx, file, "xlarge", "", 0)
				got <- response{data, err}
			}()
			require.Eventually(t, func() bool { return svc.stats.sharedWaits.Load() == 1 }, time.Second, time.Millisecond)
			switch scenario {
			case "warmup_cancels":
				cancelWarmup()
				select {
				case err := <-warmDone:
					t.Fatalf("cancel returned before in-flight decode drained: %v", err)
				case <-time.After(20 * time.Millisecond):
				}
			case "foreground_cancels":
				cancelForeground()
				require.ErrorIs(t, (<-got).err, context.Canceled)
			}
			stop()
			switch err := <-warmDone; scenario {
			case "warmup_cancels":
				require.ErrorIs(t, err, context.Canceled)
			case "foreground_before_permit":
				require.ErrorIs(t, err, ErrPreviewBusy)
			default:
				require.NoError(t, err)
			}
			if scenario != "foreground_cancels" {
				result := <-got
				require.NoError(t, result.err)
				require.NotEmpty(t, result.data)
			}
			require.EqualValues(t, 1, svc.stats.fullDecodes.Load())
		})
	}
}

func TestWarmupYieldsToQueuedForeground(t *testing.T) {
	svc, file := warmupTestSource(t)
	hold, err := svc.budget.acquire(t.Context(), 1)
	require.NoError(t, err)
	t.Cleanup(hold.release)
	waited := make(chan error, 1)
	go func() {
		_, err := svc.getPreviewForFile(t.Context(), file, "small", "", 0)
		waited <- err
	}()
	require.Eventually(t, func() bool { return svc.stats.waitingTasks.Load() == 1 }, time.Second, time.Millisecond)
	require.ErrorIs(t, testWarm(t, t.Context(), svc, file, func(int64) error { return nil }), ErrPreviewBusy)
	require.EqualValues(t, 1, svc.stats.waitingTasks.Load())
	hold.release()
	require.NoError(t, <-waited)
}

func TestWarmupRejectsReplacementDuringDecode(t *testing.T) {
	svc, file := warmupTestSource(t)
	replacement := newNoisyFile(t, filepath.Dir(file.RealPath), "new.jpg", 1599, 1200)
	var renameErr error
	svc.testHooks = &previewTestHooks{afterGenerate: func() {
		renameErr = os.Rename(replacement.RealPath, file.RealPath)
	}}
	require.ErrorIs(t, testWarm(t, t.Context(), svc, file, func(int64) error { return nil }), ErrPreviewSourceChanged)
	require.NoError(t, renameErr)
	plan, err := svc.planLedgerPreview(t.Context(), file, "xlarge")
	require.NoError(t, err)
	_, found, err := svc.ledger.Lookup(t.Context(), plan.ref, plan.variant)
	require.NoError(t, err)
	require.False(t, found)
}

func TestWarmupRejectsAnimatedAndHandlesOriginals(t *testing.T) {
	svc, sample := warmupTestSource(t)
	root := filepath.Dir(sample.RealPath)
	var source bytes.Buffer
	require.NoError(t, png.Encode(&source, image.NewRGBA(image.Rect(0, 0, 8, 8))))
	tiny := testFile(t, root, "tiny.png", source.Bytes(), "image/png")
	tiny.Source = sample.Source
	require.ErrorIs(t, testWarm(t, t.Context(), svc, tiny, func(int64) error { return nil }), ErrWarmupOriginal)
	version, err := cacheledger.Observe(tiny.RealPath)
	require.NoError(t, err)
	namespace, name, ok := cacheledger.Location(root, tiny.RealPath)
	require.True(t, ok)
	ref, err := svc.ledger.FindRef(t.Context(), namespace, name, version)
	require.NoError(t, err)
	require.Empty(t, ref.Asset, "original-only photos do not allocate ledger assets")
	tiny.Name = "invalid.png"
	tiny.RealPath = filepath.Join(root, tiny.Name)
	require.NoError(t, os.WriteFile(tiny.RealPath, []byte("invalid png"), 0o600))
	require.ErrorIs(t, testWarm(t, t.Context(), svc, tiny, func(int64) error { return nil }), ErrPreviewHeaderInvalid)
	require.ErrorIs(t, checkStaticPNG([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x08acTL")), ErrWarmupUnsupported)
	require.Zero(t, svc.stats.fullDecodes.Load())
}

func TestWarmupRejectsOverBudgetBeforeDecode(t *testing.T) {
	svc, sample := warmupTestSource(t)
	data := pngHeaderOnly(10000, 10000, 16, 6)
	data = append(data, make([]byte, 300*1024-len(data))...)
	file := testFile(t, filepath.Dir(sample.RealPath), "huge.png", data, "image/png")
	file.Source = sample.Source
	require.ErrorIs(t, testWarm(t, t.Context(), svc, file, func(int64) error { return nil }), ErrPreviewOverBudget)
	require.Zero(t, svc.stats.fullDecodes.Load())
}
