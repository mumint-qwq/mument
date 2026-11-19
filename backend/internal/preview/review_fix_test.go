// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package preview

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/kovidgoyal/imaging"
	"github.com/stretchr/testify/require"
)

func TestQueuedReplacementIsRejectedBeforeDecode(t *testing.T) {
	dir := t.TempDir()
	svc := newAdmissionService(t, 1, 64)
	file := newNoisyFile(t, dir, "source.jpg", 800, 600)

	hold, err := svc.budget.acquire(context.Background(), svc.budget.maxBytes)
	require.NoError(t, err)
	deferred := false
	defer func() {
		if !deferred {
			hold.release()
		}
	}()

	entered := make(chan struct{}, 1)
	svc.testHooks = &previewTestHooks{beforeGenerate: func() { entered <- struct{}{} }}

	result := make(chan error, 1)
	go func() {
		_, err := svc.getPreviewForFile(context.Background(), file, "xlarge", "", 0)
		result <- err
	}()
	require.Eventually(t, func() bool {
		_, _, waiting := svc.budget.snapshot()
		return waiting == 1
	}, time.Second, time.Millisecond)

	next := filepath.Join(dir, "replacement.jpg")
	require.NoError(t, os.WriteFile(next, padHeaderForParse(testJPEGHeader(4609, 8375, true, []jpegComponent{{1, 1}, {1, 1}, {1, 1}}, 0)), 0o600))
	require.NoError(t, os.Chtimes(next, file.ModTime, file.ModTime))
	require.NoError(t, os.Rename(next, file.RealPath))

	hold.release()
	deferred = true
	require.ErrorIs(t, <-result, ErrPreviewSourceChanged)
	select {
	case <-entered:
		t.Fatal("replacement was admitted using the old small-file estimate")
	default:
	}
}

func TestPathReplacementDuringGenerationIsNotPublished(t *testing.T) {
	dir := t.TempDir()
	svc := newAdmissionService(t, 4, 768)
	file := newNoisyFile(t, dir, "source.jpg", 800, 600)
	replacement := newNoisyFile(t, dir, "replacement.jpg", 900, 700)

	var once sync.Once
	svc.testHooks = &previewTestHooks{beforeGenerate: func() {
		once.Do(func() {
			if err := os.Rename(replacement.RealPath, file.RealPath); err != nil {
				t.Errorf("replace source: %v", err)
			}
		})
	}}

	_, err := svc.getPreviewForFile(context.Background(), file, "xlarge", "", 0)
	require.ErrorIs(t, err, ErrPreviewSourceChanged)

	cacheKey := CacheKey(previewCacheHash(file), "xlarge", 0)
	_, found, loadErr := svc.loadCached(context.Background(), cacheKey)
	require.NoError(t, loadErr)
	require.False(t, found, "a preview generated from the old descriptor must not be published")
}

func TestPNG16BitEstimateCoversDecodedPixelSurface(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rgba16.png")
	img := image.NewNRGBA64(image.Rect(0, 0, 1000, 1000))
	rng := rand.New(rand.NewSource(9))
	_, err := rng.Read(img.Pix)
	require.NoError(t, err)
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, png.Encode(f, img))
	require.NoError(t, f.Close())
	img = nil

	st, err := os.Stat(path)
	require.NoError(t, err)
	file := testFileInfoFromStatForReview(path, st)
	svc := newAdmissionService(t, 4, 768)
	estimate, err := svc.estimateGeneration(context.Background(), file, "xlarge")
	require.NoError(t, err)

	input, err := os.Open(path)
	require.NoError(t, err)
	defer input.Close()
	decoded, err := imaging.Decode(input, previewDecodeOpts(true)...)
	require.NoError(t, err)
	v, ok := decoded.(*image.NRGBA64)
	require.True(t, ok, "unexpected decoded type %T", decoded)
	require.GreaterOrEqual(t, estimate.Bytes, int64(len(v.Pix)))
	require.Equal(t, "raster-png", estimate.Kind)
}

func TestPNG16BitOverBudgetIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge16.png")
	require.NoError(t, os.WriteFile(path, pngHeaderOnly(10000, 10000, 16, 6), 0o600))
	st, err := os.Stat(path)
	require.NoError(t, err)
	file := testFileInfoFromStatForReview(path, st)
	svc := newAdmissionService(t, 4, 768)
	_, err = svc.getPreviewForFile(context.Background(), file, "xlarge", "", 0)
	require.ErrorIs(t, err, ErrPreviewOverBudget)
}

func pngHeaderOnly(width, height int, bitDepth, colorType byte) []byte {
	chunk := func(typ string, data []byte) []byte {
		var b []byte
		b = binary.BigEndian.AppendUint32(b, uint32(len(data)))
		b = append(b, typ...)
		b = append(b, data...)
		b = binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(append([]byte(typ), data...)))
		return b
	}
	var ihdr []byte
	ihdr = binary.BigEndian.AppendUint32(ihdr, uint32(width))
	ihdr = binary.BigEndian.AppendUint32(ihdr, uint32(height))
	ihdr = append(ihdr, bitDepth, colorType, 0, 0, 0)
	out := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	out = append(out, chunk("IHDR", ihdr)...)
	out = append(out, chunk("IDAT", nil)...)
	out = append(out, chunk("IEND", nil)...)
	return out
}

func TestGrayPNGWithTransparencyReservesNRGBA(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 4000, 4000))
	rng := rand.New(rand.NewSource(906))
	_, err := rng.Read(img.Pix)
	require.NoError(t, err)
	var raw bytes.Buffer
	require.NoError(t, png.Encode(&raw, img))
	img = nil
	original := raw.Bytes()
	require.Equal(t, byte(0), original[25])
	payload := []byte{'t', 'R', 'N', 'S', 0, 128}
	chunk := binary.BigEndian.AppendUint32(nil, 2)
	chunk = append(chunk, payload...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(payload))
	data := append(append(append([]byte{}, original[:33]...), chunk...), original[33:]...)
	file := testFile(t, t.TempDir(), "gray-transparent.png", data, "image/png")
	require.Greater(t, file.Size, int64(256<<10))
	svc := newAdmissionService(t, 4, 768)
	estimate, err := svc.estimateGeneration(context.Background(), file, "xlarge")
	require.NoError(t, err)

	input, err := os.Open(file.RealPath)
	require.NoError(t, err)
	defer input.Close()
	decoded, err := imaging.Decode(input, previewDecodeOpts(true)...)
	require.NoError(t, err)
	pixel, ok := decoded.(*image.NRGBA)
	require.True(t, ok, "actual decoded type %T", decoded)
	require.GreaterOrEqual(t, estimate.Bytes, int64(len(pixel.Pix)),
		"reservation smaller than actual decoded NRGBA surface")
}

func TestPNGDecoderBytesPerPixel(t *testing.T) {
	base := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 13}
	base = append(base, []byte("IHDR")...)
	base = append(base, 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0)
	got, err := pngDecoderBytesPerPixel(base)
	require.NoError(t, err)
	require.Equal(t, int64(4), got)

	base[24] = 16
	got, err = pngDecoderBytesPerPixel(base)
	require.NoError(t, err)
	require.Equal(t, int64(8), got)

	base[25] = 3
	got, err = pngDecoderBytesPerPixel(base)
	require.NoError(t, err)
	require.Equal(t, int64(1), got)
}

func TestDistinctKeyQueueIsBounded(t *testing.T) {
	svc := newAdmissionService(t, 1, 64)
	file := newNoisyFile(t, t.TempDir(), "queued.jpg", 800, 600)
	slots := svc.flights.tasks.capacity()
	require.Greater(t, slots, 1)

	hold, err := svc.budget.acquire(context.Background(), svc.budget.maxBytes)
	require.NoError(t, err)
	defer hold.release()

	const n = 96
	errs := make(chan error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(k int) {
			defer wg.Done()
			_, err := svc.getPreviewForFile(context.Background(), file, "xlarge", "", k)
			errs <- err
		}(i)
	}
	require.Eventually(t, func() bool {
		return svc.stats.entryRejected.Load() >= uint64(n-slots)
	}, 5*time.Second, time.Millisecond)

	_, _, waiting := svc.budget.snapshot()
	require.LessOrEqual(t, waiting, slots)
	svc.flights.mu.Lock()
	flights := len(svc.flights.calls)
	svc.flights.mu.Unlock()
	require.LessOrEqual(t, flights, slots)
	require.LessOrEqual(t, svc.stats.peakTaskSlots.Load(), int64(slots))

	hold.release()
	wg.Wait()
	close(errs)
	saturated := 0
	for err := range errs {
		if errors.Is(err, ErrPreviewSaturated) {
			saturated++
			continue
		}
		require.NoError(t, err)
	}
	require.Equal(t, n-slots, saturated)
}

func TestCanceledHeadWakesEligibleTail(t *testing.T) {
	b := newResourceBudget(3, 100, newPreviewStats())
	hold, err := b.acquire(context.Background(), 80)
	require.NoError(t, err)
	defer hold.release()

	headCtx, cancelHead := context.WithCancel(context.Background())
	defer cancelHead()
	headDone := make(chan error, 1)
	go func() {
		p, err := b.acquire(headCtx, 40)
		if p != nil {
			p.release()
		}
		headDone <- err
	}()
	require.Eventually(t, func() bool {
		_, _, waiting := b.snapshot()
		return waiting == 1
	}, time.Second, time.Millisecond)

	tailDone := make(chan error, 1)
	go func() {
		p, err := b.acquire(context.Background(), 10)
		if p != nil {
			p.release()
		}
		tailDone <- err
	}()
	require.Eventually(t, func() bool {
		_, _, waiting := b.snapshot()
		return waiting == 2
	}, time.Second, time.Millisecond)

	cancelHead()
	require.ErrorIs(t, <-headDone, context.Canceled)
	select {
	case err := <-tailDone:
		require.NoError(t, err)
	case <-time.After(200 * time.Millisecond):
		active, reserved, waiting := b.snapshot()
		t.Fatalf("tail not granted after head cancellation: active=%d reserved=%d waiting=%d", active, reserved, waiting)
	}
}

func TestCompletedFlightCancelsTaskContext(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	group := newFlightGroup[[]byte](base, newPreviewStats())
	var taskCtx context.Context
	_, err := group.do(context.Background(), "ctx-key", func(ctx context.Context, _ func() bool) ([]byte, error) {
		taskCtx = ctx
		return []byte("ok"), nil
	})
	require.NoError(t, err)
	require.NoError(t, group.wait(context.Background()))
	require.NotNil(t, taskCtx)
	select {
	case <-taskCtx.Done():
	default:
		t.Fatal("completed flight retained a live child context")
	}
}

func testFileInfoFromStatForReview(path string, st os.FileInfo) iteminfo.ExtendedFileInfo {
	return iteminfo.ExtendedFileInfo{RealPath: path, FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: st.Name(), Size: st.Size(), ModTime: st.ModTime(), Type: "image/png", HasPreview: true}}}
}
