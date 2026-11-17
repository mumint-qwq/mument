package files

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/database/users"
	apperrors "github.com/gtsteffaniak/filebrowser/backend/internal/errors"
	"github.com/gtsteffaniak/filebrowser/backend/internal/ffmpeg"
	"github.com/gtsteffaniak/filebrowser/backend/internal/utils"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
)

func makeMetadataTargets(count int, prefix string) []metadataTarget {
	targets := make([]metadataTarget, count)
	modTime := time.Unix(1700000000, 0)
	for i := range targets {
		targets[i] = metadataTarget{
			resultIndex: i,
			slot:        i,
			path:        fmt.Sprintf("/videos/%s-%d.mp4", prefix, i),
			name:        fmt.Sprintf("%s-%d.mp4", prefix, i),
			typ:         "video/mp4",
			realPath:    fmt.Sprintf("/tmp/%s-%d.mp4", prefix, i),
			modTime:     modTime,
		}
	}
	return targets
}

func TestRunMetadataExtractionBoundsWorkers(t *testing.T) {
	const targetCount = 50
	targets := makeMetadataTargets(targetCount, t.Name())
	gate := make(chan struct{})
	started := make(chan struct{}, targetCount)
	var active, peak int32
	var calls int32

	extract := func(_ context.Context, item *iteminfo.ExtendedItemInfo, _ string, _ bool, _ *ffmpeg.FFmpegService) error {
		atomic.AddInt32(&calls, 1)
		current := atomic.AddInt32(&active, 1)
		for {
			previous := atomic.LoadInt32(&peak)
			if current <= previous || atomic.CompareAndSwapInt32(&peak, previous, current) {
				break
			}
		}
		started <- struct{}{}
		<-gate
		atomic.AddInt32(&active, -1)
		item.Metadata = &iteminfo.MediaMetadata{Duration: 1}
		return nil
	}

	done := make(chan []metadataResult, 1)
	go func() {
		done <- runMetadataExtraction(context.Background(), targets, false, 2, time.Second, extract, nil)
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for worker %d to start", i+1)
		}
	}
	select {
	case <-started:
		t.Fatal("more than the configured 2 workers started")
	case <-time.After(100 * time.Millisecond):
	}
	close(gate)
	results := <-done

	if len(results) != targetCount {
		t.Fatalf("results = %d, want %d", len(results), targetCount)
	}
	for i, result := range results {
		if result.err != "" {
			t.Fatalf("result %d error = %q", i, result.err)
		}
		if result.metadata == nil || result.metadata.Duration != 1 {
			t.Fatalf("result %d metadata = %+v, want duration 1", i, result.metadata)
		}
	}
	if peak > 2 {
		t.Fatalf("peak concurrency = %d, want <= 2", peak)
	}
	if calls != targetCount {
		t.Fatalf("extractor calls = %d, want %d", calls, targetCount)
	}
}

func TestRunMetadataExtractionCacheHitSkipsExtractor(t *testing.T) {
	mediaMetadataCache.ClearAll()
	t.Cleanup(mediaMetadataCache.ClearAll)
	targets := makeMetadataTargets(3, t.Name())
	var calls int32
	extract := func(_ context.Context, item *iteminfo.ExtendedItemInfo, _ string, _ bool, _ *ffmpeg.FFmpegService) error {
		atomic.AddInt32(&calls, 1)
		item.Metadata = &iteminfo.MediaMetadata{Title: "cached title", Duration: 42}
		return nil
	}
	first := runMetadataExtraction(context.Background(), targets, false, 2, time.Second, extract, nil)
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("first extraction calls = %d, want 3", got)
	}
	for i, result := range first {
		if result.metadata == nil || result.metadata.Duration != 42 {
			t.Fatalf("first result %d metadata = %+v", i, result.metadata)
		}
	}
	second := runMetadataExtraction(context.Background(), targets, false, 2, time.Second, extract, nil)
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("second extraction calls = %d, want cached 3", got)
	}
	for i, result := range second {
		if result.metadata == nil || result.metadata.Title != "cached title" {
			t.Fatalf("second result %d metadata = %+v", i, result.metadata)
		}
	}

	// A changed modtime must miss the cache instead of serving stale metadata.
	targets[0].modTime = targets[0].modTime.Add(time.Second)
	runMetadataExtraction(context.Background(), targets[:1], false, 1, time.Second, extract, nil)
	if got := atomic.LoadInt32(&calls); got != 4 {
		t.Fatalf("modtime change calls = %d, want 4", got)
	}
}

func TestFiftyVideoDirectoryUsesCacheWithoutProbing(t *testing.T) {
	mediaMetadataCache.ClearAll()
	t.Cleanup(mediaMetadataCache.ClearAll)
	targets := makeMetadataTargets(50, t.Name())
	for _, target := range targets {
		storeCachedMediaMetadata(target, false, &iteminfo.MediaMetadata{Duration: 7})
	}
	var calls int32
	results := runMetadataExtraction(context.Background(), targets, false, 2, time.Second, func(context.Context, *iteminfo.ExtendedItemInfo, string, bool, *ffmpeg.FFmpegService) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}, nil)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("extractor calls = %d, want 0 for an all-cached directory", got)
	}
	for i, result := range results {
		if result.metadata == nil || result.metadata.Duration != 7 {
			t.Fatalf("result %d metadata = %+v, want cached duration 7", i, result.metadata)
		}
	}
}

func TestRunMetadataExtractionParentCancelMarksUnstarted(t *testing.T) {
	targets := makeMetadataTargets(20, t.Name())
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	var calls int32
	extract := func(extractCtx context.Context, _ *iteminfo.ExtendedItemInfo, _ string, _ bool, _ *ffmpeg.FFmpegService) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			cancel()
		}
		<-release
		return extractCtx.Err()
	}
	done := make(chan []metadataResult, 1)
	go func() {
		done <- runMetadataExtraction(ctx, targets, false, 2, time.Second, extract, nil)
	}()
	// Both workers are now blocked in the extractor, so the scheduler must
	// observe cancellation instead of queueing more work.
	time.Sleep(50 * time.Millisecond)
	close(release)
	results := <-done

	var canceled int
	for _, result := range results {
		if result.err == metadataErrorCanceled || result.err == metadataErrorTimedOut {
			canceled++
		}
	}
	if canceled == 0 {
		t.Fatalf("expected canceled results, got %+v", results)
	}
	if got := atomic.LoadInt32(&calls); got >= int32(len(targets)) {
		t.Fatalf("extractor calls = %d, want fewer than %d after cancellation", got, len(targets))
	}
}

func TestRunMetadataExtractionPerItemTimeout(t *testing.T) {
	targets := makeMetadataTargets(1, t.Name())
	extract := func(ctx context.Context, _ *iteminfo.ExtendedItemInfo, _ string, _ bool, _ *ffmpeg.FFmpegService) error {
		<-ctx.Done()
		return ctx.Err()
	}
	start := time.Now()
	results := runMetadataExtraction(context.Background(), targets, false, 1, 20*time.Millisecond, extract, nil)
	elapsed := time.Since(start)
	if len(results) != 1 || results[0].err != metadataErrorTimedOut {
		t.Fatalf("results = %+v, want one timed out item", results)
	}
	if elapsed > time.Second {
		t.Fatalf("timeout took %s, want under 1s", elapsed)
	}
}

func TestRunMetadataExtractionAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := runMetadataExtraction(ctx, makeMetadataTargets(5, t.Name()), false, 2, time.Second, func(context.Context, *iteminfo.ExtendedItemInfo, string, bool, *ffmpeg.FFmpegService) error {
		t.Fatal("extractor must not run for an already canceled request")
		return nil
	}, nil)
	for i, result := range results {
		if result.err != metadataErrorCanceled {
			t.Fatalf("result %d error = %q, want %q", i, result.err, metadataErrorCanceled)
		}
	}
}

func TestNormalizeMetadataPaths(t *testing.T) {
	got, err := NormalizeMetadataPaths([]string{" /a.mp4 ", "", "/a.mp4", "/b.mp4"}, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != "/a.mp4" || got[1] != "/b.mp4" {
		t.Fatalf("normalized = %#v", got)
	}

	if _, err := NormalizeMetadataPaths(nil, 10); !errors.Is(err, apperrors.ErrInvalidRequestParams) {
		t.Fatalf("empty paths error = %v, want ErrInvalidRequestParams", err)
	}
	if _, err := NormalizeMetadataPaths([]string{"/a", "/b", "/c"}, 2); !errors.Is(err, apperrors.ErrInvalidRequestParams) {
		t.Fatalf("too many paths error = %v, want ErrInvalidRequestParams", err)
	}
}

func TestMetadataResponseIncludesPerItemErrors(t *testing.T) {
	response := MetadataResponse{Items: []MetadataItem{
		{Path: "/missing.mp4", Error: metadataErrorUnavailable},
		{Path: "/notes.txt", Error: metadataErrorUnsupported},
	}}
	if len(response.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(response.Items))
	}
}

func TestGetMetadataSubsetRejectsInvalidRequests(t *testing.T) {
	opts := utils.FileOptions{Source: "src"}

	if _, err := GetMetadataSubset(context.Background(), opts, nil, []string{"/a.mp4"}, false); !errors.Is(err, apperrors.ErrInvalidRequestParams) {
		t.Fatalf("nil user error = %v, want ErrInvalidRequestParams", err)
	}
	if _, err := GetMetadataSubset(context.Background(), opts, &users.User{}, nil, false); !errors.Is(err, apperrors.ErrInvalidRequestParams) {
		t.Fatalf("empty paths error = %v, want ErrInvalidRequestParams", err)
	}
}

func TestLoadCachedMediaMetadataRequiresRealPath(t *testing.T) {
	mediaMetadataCache.ClearAll()
	if _, ok := loadCachedMediaMetadata(metadataTarget{}); ok {
		t.Fatal("empty real path must not hit the cache")
	}
}
