// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package preview

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"math/rand"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/stretchr/testify/require"
)

func newNoisyFile(t *testing.T, dir, name string, width, height int) iteminfo.ExtendedFileInfo {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	rng := rand.New(rand.NewSource(42))
	_, _ = rng.Read(img.Pix)
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}))
	require.Greater(t, buf.Len(), 256*1024)
	file := testFile(t, dir, name, buf.Bytes(), "image/jpeg")
	return file
}

func newAdmissionService(t *testing.T, workers, budgetMB int) *Service {
	t.Helper()
	svc := NewPreviewGeneratorWithBudget(workers, budgetMB, t.TempDir())
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Shutdown(shutdownCtx)
	})
	return svc
}

func blockGeneration(svc *Service) (entered <-chan struct{}, release chan<- struct{}) {
	enteredCh := make(chan struct{})
	releaseCh := make(chan struct{})
	var once sync.Once
	svc.testHooks = &previewTestHooks{
		beforeGenerate: func() {
			once.Do(func() { close(enteredCh) })
			<-releaseCh
		},
	}
	return enteredCh, releaseCh
}

func TestPreviewCacheHitBypassesAdmission(t *testing.T) {
	dir := t.TempDir()
	svc := newAdmissionService(t, 4, 768)
	file := newNoisyFile(t, dir, "cached.jpg", 1600, 1200)

	first, err := svc.getPreviewForFile(context.Background(), file, "xlarge", "", 0)
	require.NoError(t, err)
	require.NotEmpty(t, first)
	require.Equal(t, uint64(1), svc.stats.fullDecodes.Load())

	hold, err := svc.budget.acquire(context.Background(), svc.budget.maxBytes)
	require.NoError(t, err)
	defer hold.release()

	start := time.Now()
	second, err := svc.getPreviewForFile(context.Background(), file, "xlarge", "", 0)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Less(t, time.Since(start), 200*time.Millisecond)
	require.Equal(t, uint64(1), svc.stats.fullDecodes.Load())
	require.Equal(t, uint64(1), svc.stats.cacheHits.Load())
}

func TestSameKeyTwentyWaitersGenerateOnce(t *testing.T) {
	dir := t.TempDir()
	svc := newAdmissionService(t, 4, 768)
	file := newNoisyFile(t, dir, "same-key.jpg", 1600, 1200)
	entered, release := blockGeneration(svc)

	const waiters = 20
	results := make([][]byte, waiters)
	errs := make([]error, waiters)
	var wg sync.WaitGroup
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = svc.getPreviewForFile(context.Background(), file, "xlarge", "", 0)
		}(i)
	}
	<-entered
	require.Eventually(t, func() bool {
		svc.flights.mu.Lock()
		defer svc.flights.mu.Unlock()
		call := svc.flights.calls[CacheKey(previewCacheHash(file), "xlarge", 0)]
		return call != nil && call.waiters == waiters
	}, 2*time.Second, time.Millisecond)
	close(release)
	wg.Wait()

	require.Equal(t, uint64(1), svc.stats.fullDecodes.Load())
	require.Equal(t, uint64(waiters-1), svc.stats.sharedWaits.Load())
	for i := 0; i < waiters; i++ {
		require.NoError(t, errs[i])
		require.Equal(t, len(results[0]), len(results[i]))
		require.True(t, bytes.Equal(results[0], results[i]))
	}
}

func TestPartialWaiterCancelKeepsSharedTask(t *testing.T) {
	dir := t.TempDir()
	svc := newAdmissionService(t, 4, 768)
	file := newNoisyFile(t, dir, "partial.jpg", 1600, 1200)
	entered, release := blockGeneration(svc)

	ctx, cancel := context.WithCancel(context.Background())
	canceled := make(chan error, 1)
	kept := make(chan []byte, 2)
	go func() { _, err := svc.getPreviewForFile(ctx, file, "large", "", 0); canceled <- err }()
	for i := 0; i < 2; i++ {
		go func() {
			data, err := svc.getPreviewForFile(context.Background(), file, "large", "", 0)
			require.NoError(t, err)
			kept <- data
		}()
	}
	<-entered
	require.Eventually(t, func() bool {
		svc.flights.mu.Lock()
		defer svc.flights.mu.Unlock()
		call := svc.flights.calls[CacheKey(previewCacheHash(file), "large", 0)]
		return call != nil && call.waiters == 3
	}, 2*time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-canceled, context.Canceled)
	close(release)
	require.NotEmpty(t, <-kept)
	require.NotEmpty(t, <-kept)
	require.Equal(t, uint64(1), svc.stats.canceledWaiters.Load())
	active, used, waiting := svc.budget.snapshot()
	require.Zero(t, active)
	require.Zero(t, used)
	require.Zero(t, waiting)
}

func TestLastWaiterCancelsQueuedTask(t *testing.T) {
	dir := t.TempDir()
	svc := newAdmissionService(t, 4, 768)
	file := newNoisyFile(t, dir, "queued.jpg", 1600, 1200)
	hold, err := svc.budget.acquire(context.Background(), svc.budget.maxBytes)
	require.NoError(t, err)
	defer hold.release()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := svc.getPreviewForFile(ctx, file, "small", "", 0); done <- err }()
	require.Eventually(t, func() bool {
		svc.flights.mu.Lock()
		defer svc.flights.mu.Unlock()
		call := svc.flights.calls[CacheKey(previewCacheHash(file), "small", 0)]
		return call != nil && call.waiters == 1
	}, 2*time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.Eventually(t, func() bool {
		svc.flights.mu.Lock()
		defer svc.flights.mu.Unlock()
		return len(svc.flights.calls) == 0
	}, 2*time.Second, time.Millisecond)
	require.Equal(t, uint64(1), svc.stats.canceledQueued.Load())
	require.Zero(t, svc.stats.fullDecodes.Load())
}

func TestLastWaiterCancelDoesNotAbortRunningDecode(t *testing.T) {
	dir := t.TempDir()
	svc := newAdmissionService(t, 4, 768)
	file := newNoisyFile(t, dir, "running.jpg", 1600, 1200)
	entered, release := blockGeneration(svc)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := svc.getPreviewForFile(ctx, file, "small", "", 0); done <- err }()
	<-entered
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	close(release)

	require.Eventually(t, func() bool {
		svc.flights.mu.Lock()
		defer svc.flights.mu.Unlock()
		return len(svc.flights.calls) == 0
	}, 2*time.Second, time.Millisecond)
	require.Equal(t, uint64(1), svc.stats.fullDecodes.Load())
	require.Zero(t, svc.stats.canceledQueued.Load())

	// The completed result was published even though its only waiter left.
	svc.testHooks = nil
	data, err := svc.getPreviewForFile(context.Background(), file, "small", "", 0)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	require.Equal(t, uint64(1), svc.stats.fullDecodes.Load())
}

func TestGenerationPanicRecoversAndRetries(t *testing.T) {
	dir := t.TempDir()
	svc := newAdmissionService(t, 4, 768)
	file := newNoisyFile(t, dir, "panic.jpg", 1600, 1200)
	svc.testHooks = &previewTestHooks{beforeGenerate: func() { panic("test panic") }}
	_, err := svc.getPreviewForFile(context.Background(), file, "xlarge", "", 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "panicked")
	require.Equal(t, uint64(1), svc.stats.panics.Load())

	svc.testHooks = nil
	data, err := svc.getPreviewForFile(context.Background(), file, "xlarge", "", 0)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	require.Equal(t, uint64(1), svc.stats.fullDecodes.Load())
	active, used, _ := svc.budget.snapshot()
	require.Zero(t, active)
	require.Zero(t, used)
}

type failingStoreCache struct {
	inner interface {
		Store(ctx context.Context, key string, value []byte) error
		Load(ctx context.Context, key string) ([]byte, bool, error)
		Delete(ctx context.Context, key string) error
	}
}

func (c failingStoreCache) Store(ctx context.Context, key string, value []byte) error {
	return errors.New("simulated storage failure")
}
func (c failingStoreCache) Load(ctx context.Context, key string) ([]byte, bool, error) {
	return c.inner.Load(ctx, key)
}
func (c failingStoreCache) Delete(ctx context.Context, key string) error {
	return c.inner.Delete(ctx, key)
}

func TestStorageFailureReturnsGeneratedBytesAndRetries(t *testing.T) {
	dir := t.TempDir()
	svc := newAdmissionService(t, 4, 768)
	file := newNoisyFile(t, dir, "store.jpg", 1600, 1200)
	original := svc.fileCache
	svc.fileCache = failingStoreCache{inner: original}
	data, err := svc.getPreviewForFile(context.Background(), file, "xlarge", "", 0)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	require.Equal(t, uint64(1), svc.stats.storeFailures.Load())

	svc.fileCache = original
	data2, err := svc.getPreviewForFile(context.Background(), file, "xlarge", "", 0)
	require.NoError(t, err)
	require.Equal(t, data, data2)
	require.Equal(t, uint64(2), svc.stats.fullDecodes.Load())
}

func TestSourceChangeDoesNotPublishStaleResult(t *testing.T) {
	dir := t.TempDir()
	svc := newAdmissionService(t, 4, 768)
	file := newNoisyFile(t, dir, "changing.jpg", 1600, 1200)
	svc.testHooks = &previewTestHooks{afterGenerate: func() {
		replacement := make([]byte, file.Size+1024)
		require.NoError(t, os.WriteFile(file.RealPath, replacement, 0o600))
	}}
	_, err := svc.getPreviewForFile(context.Background(), file, "xlarge", "", 0)
	require.ErrorIs(t, err, ErrPreviewSourceChanged)
	require.Equal(t, uint64(1), svc.stats.sourceChanged.Load())

	svc.testHooks = nil
	cacheHash := previewCacheHash(file)
	// The request's cache key was based on the old size/mtime, so the failing
	// task must not have published under it.
	_, found, err := svc.loadCached(context.Background(), CacheKey(cacheHash, "xlarge", 0))
	require.NoError(t, err)
	require.False(t, found)
}

func TestShutdownWaitsForRunningDecodeAndRejectsNewMisses(t *testing.T) {
	dir := t.TempDir()
	svc := newAdmissionService(t, 4, 768)
	file := newNoisyFile(t, dir, "shutdown.jpg", 1600, 1200)
	entered, release := blockGeneration(svc)
	done := make(chan error, 1)
	go func() { _, err := svc.getPreviewForFile(context.Background(), file, "xlarge", "", 0); done <- err }()
	<-entered

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		shutdownDone <- svc.Shutdown(ctx)
	}()
	require.ErrorIs(t, <-shutdownDone, context.DeadlineExceeded)
	close(release)
	requestErr := <-done
	require.True(t, requestErr == nil || errors.Is(requestErr, context.Canceled), "request ended with %v", requestErr)
	require.NoError(t, svc.Shutdown(context.Background()))
	require.NoError(t, svc.Shutdown(context.Background()))

	other := newNoisyFile(t, dir, "after-shutdown.jpg", 1600, 1200)
	_, err := svc.getPreviewForFile(context.Background(), other, "xlarge", "", 0)
	require.ErrorIs(t, err, ErrPreviewShuttingDown)
}

func TestPreviewVariantsProduceExpectedShape(t *testing.T) {
	dir := t.TempDir()
	svc := newAdmissionService(t, 4, 768)
	file := newNoisyFile(t, dir, "variants.jpg", 1600, 900)

	cases := []struct {
		size         string
		maxDimension int
		fill         bool
	}{
		{"small", 256, true},
		{"large", 640, false},
		{"xlarge", 1024, false},
	}
	for _, tc := range cases {
		data, err := svc.getPreviewForFile(context.Background(), file, tc.size, "", 0)
		require.NoError(t, err)
		cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
		require.NoError(t, err)
		require.Equal(t, "jpeg", format)
		if tc.fill {
			require.Equal(t, 256, cfg.Width)
			require.Equal(t, 256, cfg.Height)
			continue
		}
		require.LessOrEqual(t, cfg.Width, tc.maxDimension)
		require.LessOrEqual(t, cfg.Height, tc.maxDimension)
		require.Equal(t, tc.maxDimension, maxInt(cfg.Width, cfg.Height))
	}
}

func TestPreviewAppliesEXIFOrientation(t *testing.T) {
	dir := t.TempDir()
	svc := newAdmissionService(t, 4, 768)
	img := image.NewNRGBA(image.Rect(0, 0, 1200, 600))
	rng := rand.New(rand.NewSource(7))
	_, _ = rng.Read(img.Pix)
	var encoded bytes.Buffer
	require.NoError(t, jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 90}))
	exif := makeExifOrientation(6) // Rotate 90 CW: display dimensions become 600x1200.
	app1 := []byte{0xff, 0xe1, byte((len(exif) + 2) >> 8), byte(len(exif) + 2)}
	data := append([]byte{0xff, 0xd8}, app1...)
	data = append(data, exif...)
	data = append(data, encoded.Bytes()[2:]...)
	file := testFile(t, dir, "rotated.jpg", data, "image/jpeg")

	preview, err := svc.getPreviewForFile(context.Background(), file, "xlarge", "", 0)
	require.NoError(t, err)
	cfg, format, err := image.DecodeConfig(bytes.NewReader(preview))
	require.NoError(t, err)
	require.Equal(t, "jpeg", format)
	require.Greater(t, cfg.Height, cfg.Width, "EXIF Rotate 90 CW must swap display dimensions")
	require.Equal(t, 1024, maxInt(cfg.Width, cfg.Height))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func TestOverBudgetRejectsBeforeDecode(t *testing.T) {
	dir := t.TempDir()
	svc := newAdmissionService(t, 4, 64)
	// A synthetic progressive JPEG header large enough to exceed a 64 MiB budget.
	data := testJPEGHeader(12000, 9000, true, []jpegComponent{{1, 1}, {1, 1}, {1, 1}}, 0)
	if len(data) < 300*1024 {
		data = append(data, make([]byte, 300*1024-len(data))...)
	}
	file := testFile(t, dir, "huge.jpg", data, "image/jpeg")
	_, err := svc.getPreviewForFile(context.Background(), file, "xlarge", "", 0)
	require.ErrorIs(t, err, ErrPreviewOverBudget)
	require.Equal(t, uint64(1), svc.stats.overBudget.Load())
	require.Zero(t, svc.stats.fullDecodes.Load())
	active, used, _ := svc.budget.snapshot()
	require.Zero(t, active)
	require.Zero(t, used)
}
