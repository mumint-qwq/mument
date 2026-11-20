// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package preview

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/filebrowser/backend/internal/ffmpeg"
	"github.com/gtsteffaniak/filebrowser/backend/internal/imagemeta"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/gtsteffaniak/go-logger/logger"
)

const minPreviewSize = 100

var (
	ErrUnsupportedFormat    = errors.New("preview is not available for provided file format")
	ErrUnsupportedMedia     = errors.New("unsupported media type")
	ErrPreviewTooSmall      = errors.New("generated image is too small, likely an error occurred")
	ErrPreviewUnavailable   = errors.New("preview service is not available")
	ErrPreviewShuttingDown  = errors.New("preview service is shutting down")
	ErrPreviewSourceChanged = errors.New("preview source changed during generation")
	ErrPreviewSaturated     = errors.New("preview generation queue is full")
	ErrPreviewBusy          = errors.New("preview service is busy")
	service                 *Service
)

type Service struct {
	fileCache      diskcache.Interface
	ledger         *cacheledger.Repository
	orphans        *orphanManager
	space          *spaceManager
	recoveryDone   chan struct{}
	ledgerClose    sync.Once
	ledgerCloseErr error
	cacheDir       string // Cache directory used for thumbnails and temp files
	debug          bool
	docGenMutex    sync.Mutex            // Mutex to serialize access to doc generation (required for CGO thread safety with go-fitz)
	ffmpegService  *ffmpeg.FFmpegService // Shared FFmpeg service for video and HEIC/JPEG fallback

	budget              *resourceBudget
	flights             *flightGroup[previewResult]
	tasks               *taskLimiter
	headerSem           chan struct{}
	stats               *previewStats
	baseCtx             context.Context
	baseCancel          context.CancelFunc
	closeOnce           sync.Once
	closed              atomic.Bool
	interactiveRequests atomic.Int64
	lastInteractive     atomic.Int64
	recentDirectory     atomic.Pointer[WarmupDirectory]
	warmupRequest       atomic.Bool
	warmupEnabled       atomic.Bool

	// testHooks is nil in production. Tests use it to hold a generation at a
	// defined stage instead of relying on sleeps.
	testHooks *previewTestHooks
}

type previewTestHooks struct {
	beforeGenerate func()
	afterGenerate  func()
}

// NewPreviewGenerator keeps the historical two-argument constructor and uses
// the default preview memory budget. Callers that know the configured budget
// should use NewPreviewGeneratorWithBudget.
func NewPreviewGenerator(concurrencyLimit int, cacheDir string) *Service {
	return NewPreviewGeneratorWithBudget(concurrencyLimit, settings.DefaultPreviewMemoryBudgetMB, cacheDir)
}

// NewPreviewGeneratorWithBudget creates the single admission point for preview
// work. concurrencyLimit is the configured numImageProcessors maximum; the byte
// budget is shared by every preview task. There is no second independent
// four-task or eight-megabyte semaphore.
func NewPreviewGeneratorWithBudget(concurrencyLimit, memoryBudgetMB int, cacheDir string) *Service {
	return newPreviewGenerator(concurrencyLimit, memoryBudgetMB, cacheDir, false)
}

func newPreviewGenerator(concurrencyLimit, memoryBudgetMB int, cacheDir string, spaceConfigured bool) *Service {
	configureImagingParallelism()

	if concurrencyLimit < 1 {
		concurrencyLimit = 1
	}
	if memoryBudgetMB < 1 {
		memoryBudgetMB = settings.DefaultPreviewMemoryBudgetMB
	}

	actualCacheDir := cacheDir
	if actualCacheDir == "" {
		actualCacheDir = os.TempDir()
	}

	openCache := diskcache.NewFileCache
	if spaceConfigured {
		openCache = diskcache.NewRetainedFileCache
	}
	fileCache, err := openCache(actualCacheDir)
	if err != nil {
		logger.Error("The cache dir could not be created. Make sure the user that you executed the program with has access to create directories in the local path. See  ")
		logger.Fatalf("failed to create file cache path, which is now require to run the server: %v", err)
	}
	// Create directories recursively using the determined cache directory
	err = os.MkdirAll(filepath.Join(actualCacheDir, "thumbnails", "docs"), fileutils.PermDir)
	if err != nil {
		logger.Error(err)
	}
	err = os.MkdirAll(filepath.Join(actualCacheDir, "thumbnails", "videos"), fileutils.PermDir)
	if err != nil {
		logger.Error(err)
	}
	err = os.MkdirAll(filepath.Join(actualCacheDir, "heic"), fileutils.PermDir)
	if err != nil {
		logger.Error(err)
	}

	// Single shared ffmpeg service initialized during config setup
	ffmpegService := ffmpeg.Get()

	stats := &previewStats{}
	baseCtx, baseCancel := context.WithCancel(context.Background())
	budgetBytes := int64(memoryBudgetMB) * 1024 * 1024
	budget := newResourceBudget(concurrencyLimit, budgetBytes, stats)
	taskLimit := newTaskLimiter(previewTaskSlots(concurrencyLimit), stats)
	svc := &Service{
		fileCache:     fileCache,
		cacheDir:      actualCacheDir,
		debug:         settings.Config.Integrations.Media.Debug,
		ffmpegService: ffmpegService,
		budget:        budget,
		tasks:         taskLimit,
		headerSem:     make(chan struct{}, concurrencyLimit),
		stats:         stats,
		baseCtx:       baseCtx,
		baseCancel:    baseCancel,
	}
	svc.flights = newFlightGroup[previewResult](baseCtx, stats, taskLimit)
	go svc.sampleLoop(baseCtx)

	settings.Env.MuPdfAvailable = docEnabled()
	logger.Infof("Preview generation budget: %d MiB, max concurrent tasks: %d", memoryBudgetMB, concurrencyLimit)
	return svc
}

// StartPreviewGenerator keeps the historical entry point for tests and callers
// that do not configure a budget.
func StartPreviewGenerator(concurrencyLimit int, cacheDir string) error {
	return StartPreviewGeneratorWithBudget(concurrencyLimit, settings.DefaultPreviewMemoryBudgetMB, cacheDir)
}

// StartPreviewGeneratorWithBudget initializes the global preview service with an
// explicit memory budget in MiB.
func StartPreviewGeneratorWithBudget(concurrencyLimit, memoryBudgetMB int, cacheDir string) error {
	if service != nil {
		logger.Errorf("WARNING: StartPreviewGenerator called multiple times! Shutting down the previous preview service.")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := service.Shutdown(shutdownCtx)
		cancel()
		if err != nil {
			return err
		}
	}
	if cacheDir == "" {
		cacheDir = os.TempDir()
	}
	// Prepare only the dedicated directory before taking the writer lease.
	// Cache initialization may migrate old keys and runs after acquisition.
	enabled := settings.Config.Server.PreviewOrphanReclamation
	if err := os.MkdirAll(filepath.Join(cacheDir, "diskcache"), fileutils.PermDir); err != nil {
		return err
	}
	tree, err := diskcache.OpenReclaimTree(cacheDir)
	if err != nil && (enabled || errors.Is(err, diskcache.ErrReclaimBusy)) {
		return err
	}
	if enabled {
		if err := tree.Check(true); err != nil {
			_ = tree.Close()
			return err
		}
	}
	spaceConfig := settings.Config.Server.PreviewSpace
	next := newPreviewGenerator(concurrencyLimit, memoryBudgetMB, cacheDir, spaceConfig.Mode != "")
	next.startLedger()
	if tree != nil && next.ledger != nil {
		next.orphans = newOrphanManager(tree, next.ledger, enabled, settings.Config.Server.PreviewOrphanGraceHours)
		go next.orphans.run(next.baseCtx)
	} else if tree != nil {
		_ = tree.Close()
	}
	if enabled && next.orphans == nil {
		_ = next.Shutdown(context.Background())
		return ErrPreviewUnavailable
	}
	if spaceConfig.Mode != "" {
		var spaceTree *diskcache.ReclaimTree
		if next.orphans != nil {
			spaceTree = next.orphans.tree
		}
		next.space = newSpaceManager(spaceTree, spaceConfig)
		go next.space.run(next.baseCtx)
	}
	service = next
	return nil
}

// GetService returns the preview service instance (can be nil if not started)
func GetService() *Service {
	return service
}

// Shutdown stops accepting preview work, cancels queued waiters, and waits for
// already-started generation to leave the admission budget. It is idempotent.
func Shutdown(ctx context.Context) error {
	if service == nil {
		return nil
	}
	return service.Shutdown(ctx)
}

func (s *Service) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.baseCancel()
		s.budget.close()
		s.flights.close()
	})
	if err := s.flights.wait(ctx); err != nil {
		return err
	}
	if s.recoveryDone != nil {
		select {
		case <-s.recoveryDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.space != nil {
		select {
		case <-s.space.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.orphans != nil {
		select {
		case <-s.orphans.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.ledgerClose.Do(func() {
		if s.ledger != nil {
			s.ledgerCloseErr = s.ledger.Close()
		}
		if s.orphans != nil {
			s.ledgerCloseErr = errors.Join(s.ledgerCloseErr, s.orphans.tree.Close())
		}
	})
	return s.ledgerCloseErr
}

func previewCacheHash(file iteminfo.ExtendedFileInfo) string {
	if file.Metadata != nil && len(file.Metadata.AlbumArt) > 0 {
		hasher := md5.New()
		_, _ = hasher.Write(file.Metadata.AlbumArt)
		return hex.EncodeToString(hasher.Sum(nil))
	}
	hasher := md5.New()
	cacheString := fmt.Sprintf("%s:%d:%s", file.RealPath, file.Size, file.ModTime.Format(time.RFC3339Nano))
	_, _ = hasher.Write([]byte(cacheString))
	return hex.EncodeToString(hasher.Sum(nil))
}

// GetPreviewForFile serves a cached preview without touching the decode
// admission path. On a miss the request joins (or starts) one same-key task.
func GetPreviewForFile(ctx context.Context, file iteminfo.ExtendedFileInfo, previewSize, url string, seekPercentage int) ([]byte, error) {
	if service == nil {
		return nil, ErrPreviewUnavailable
	}
	return service.getPreviewForFile(ctx, file, previewSize, url, seekPercentage)
}

// WarmPreviewForFile checks or generates a preview without marking it as used.
func WarmPreviewForFile(ctx context.Context, file iteminfo.ExtendedFileInfo, previewSize, url string, seekPercentage int) error {
	if service == nil {
		return ErrPreviewUnavailable
	}
	_, err := service.requestPreview(ctx, file, previewSize, url, seekPercentage, false)
	return err
}

type previewResult struct {
	spaceErr error
	data     []byte
	ready    cacheledger.Result
	weakInfo os.FileInfo
}

func (s *Service) getPreviewForFile(ctx context.Context, file iteminfo.ExtendedFileInfo, previewSize, url string, seekPercentage int) ([]byte, error) {
	return s.requestPreview(ctx, file, previewSize, url, seekPercentage, true)
}

func (s *Service) requestPreview(ctx context.Context, file iteminfo.ExtendedFileInfo, previewSize, url string, seekPercentage int, foreground bool) ([]byte, error) {
	s.interactiveRequests.Add(1)
	defer func() {
		s.lastInteractive.Store(time.Now().UnixNano())
		s.interactiveRequests.Add(-1)
	}()
	if foreground && s.warmupEnabled.Load() && file.Source != "" {
		s.noteWarmupDirectory(file.Source, filepath.Dir(file.RealPath))
	}
	result, err := s.requestInteractivePreview(ctx, file, previewSize, url, seekPercentage, foreground)
	if errors.Is(err, errWarmupFlight) {
		return s.requestInteractivePreview(ctx, file, previewSize, url, seekPercentage, foreground)
	}
	return result, err
}

func (s *Service) requestInteractivePreview(ctx context.Context, file iteminfo.ExtendedFileInfo, previewSize, url string, seekPercentage int, foreground bool) ([]byte, error) {
	if !file.HasPreview {
		return nil, ErrUnsupportedMedia
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cacheHash := previewCacheHash(file)
	cacheKey := CacheKey(cacheHash, previewSize, seekPercentage)
	publication, err := s.planLedgerPreview(ctx, file, previewSize)
	if err != nil {
		return nil, err
	}
	if publication != nil {
		cacheKey = publication.key
	}

	if result, found, err := s.loadPreviewCache(ctx, cacheKey, publication); err != nil {
		return nil, fmt.Errorf("failed to load from cache: %w", err)
	} else if found {
		s.stats.cacheHits.Add(1)
		return s.deliverPreview(ctx, publication, result, foreground)
	}
	s.stats.cacheMisses.Add(1)

	result, err := s.flights.do(ctx, cacheKey, func(taskCtx context.Context, markStarted func() bool) (previewResult, error) {
		// Double-check after becoming the leader: a previous task may have
		// published between the hit check and the flight registration.
		if result, found, err := s.loadPreviewCache(taskCtx, cacheKey, publication); err != nil {
			return previewResult{}, err
		} else if found {
			s.stats.cacheHits.Add(1)
			return result, nil
		}
		return s.generateAndPublish(taskCtx, file, previewSize, url, seekPercentage, cacheHash, cacheKey, markStarted, publication, nil, !foreground)
	})
	if err != nil {
		return nil, err
	}
	return s.deliverPreview(ctx, publication, result, foreground)
}

func (s *Service) loadCached(ctx context.Context, cacheKey string) ([]byte, bool, error) {
	data, found, err := s.fileCache.Load(ctx, cacheKey)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	if len(data) < minPreviewSize {
		// Older builds stored an empty marker after a failed generation. That
		// turned a transient failure into a permanent one; delete it and let the
		// request retry under normal admission.
		_ = s.fileCache.Delete(ctx, cacheKey)
		s.stats.cacheCorrupt.Add(1)
		return nil, false, nil
	}
	return data, true, nil
}

// generateAndPublish is the only path that opens the source for preview work.
// It reserves memory, verifies the source version, generates, verifies again,
// and atomically publishes the result.
func (s *Service) generateAndPublish(ctx context.Context, file iteminfo.ExtendedFileInfo, previewSize, officeUrl string, seekPercentage int, fileMD5, cacheKey string, markStarted func() bool, publication *ledgerPreview, warmup *warmupInput, background bool) (previewResult, error) {
	if fileMD5 == "" {
		return previewResult{}, fmt.Errorf("preview cache hash is empty")
	}
	if err := ctx.Err(); err != nil {
		return previewResult{}, err
	}
	if s.closed.Load() {
		return previewResult{}, ErrPreviewShuttingDown
	}

	if warmup != nil && s.interactiveRequests.Load() != 0 {
		return previewResult{}, ErrPreviewBusy
	}
	var estimate previewEstimate
	var err error
	if warmup != nil {
		estimate = warmup.estimate
	} else {
		estimate, err = s.estimateGeneration(ctx, file, previewSize)
	}
	if err != nil {
		if errors.Is(err, ErrPreviewHeaderInvalid) || errors.Is(err, ErrPreviewDimensionsTooLarge) {
			s.stats.headerRejected.Add(1)
		}
		return previewResult{}, err
	}

	var permit *budgetPermit
	if warmup != nil {
		if err := warmup.check(estimate.Bytes); err != nil {
			return previewResult{}, err
		}
		permit, err = s.budget.acquireIdle(ctx, estimate.Bytes, &s.interactiveRequests)
	} else {
		permit, err = s.budget.acquire(ctx, estimate.Bytes)
	}
	if err != nil {
		return previewResult{}, err
	}
	defer permit.release()
	if warmup != nil {
		start := time.Now()
		defer func() {
			duration := uint64(time.Since(start).Nanoseconds())
			s.stats.warmupHoldNanos.Add(duration)
			s.stats.warmupMaxHoldNanos.Store(max(s.stats.warmupMaxHoldNanos.Load(), duration))
		}()
	}
	if s.closed.Load() {
		return previewResult{}, ErrPreviewShuttingDown
	}

	workStarted := false
	defer func() {
		if workStarted {
			// A task's estimate must not be treated as returned to the OS just
			// because it left the accounting. Large decodes force a collection
			// and return pages before the reservation is released; smaller tasks
			// only force GC when the live heap is high.
			s.settleMemory(estimate.Bytes)
		}
	}()

	if markStarted != nil && !markStarted() {
		return previewResult{}, context.Canceled
	}

	// A concurrent flight for the same key cannot exist, but a direct
	// GeneratePreviewWithMD5 caller or a just-finished task may have published
	// while this task waited for admission.
	if warmup != nil {
		if err := s.checkWarmupCache(ctx, publication); err != nil {
			return previewResult{}, err
		}
	} else if result, found, err := s.loadPreviewCache(ctx, cacheKey, publication); err != nil {
		return previewResult{}, err
	} else if found {
		s.stats.cacheHits.Add(1)
		return result, nil
	}

	// Reopen after admission. Decode the same descriptor that passes the
	// header binding and, for warmup, the live directory and index checks.
	var sourceFile *os.File
	if warmup != nil {
		sourceFile, err = warmup.open(ctx)
		if err == nil && !sourceBindingFromEstimate(estimate).matches(sourceFile) {
			sourceFile.Close()
			err = ErrPreviewSourceChanged
		}
	} else {
		sourceFile, err = openBoundSource(ctx, file.RealPath, sourceBindingFromEstimate(estimate))
	}
	if err != nil {
		s.stats.sourceChanged.Add(1)
		return previewResult{}, err
	}
	defer sourceFile.Close()
	before, err := sourceFile.Stat()
	if err != nil {
		s.stats.sourceChanged.Add(1)
		return previewResult{}, fmt.Errorf("%w: %v", ErrPreviewSourceChanged, err)
	}
	if err := verifyRequestedVersion(before, file); err != nil {
		return previewResult{}, err
	}

	if publication != nil && cacheledger.FromFileInfo(before) != publication.version {
		return previewResult{}, ErrPreviewSourceChanged
	}
	if publication != nil && publication.weakInfo != nil {
		if err := verifySourceUnchanged(publication.weakInfo, before); err != nil {
			s.stats.sourceChanged.Add(1)
			return previewResult{}, err
		}
	}
	workStarted = true
	start := time.Now()
	if s.testHooks != nil && s.testHooks.beforeGenerate != nil {
		s.testHooks.beforeGenerate()
	}
	var original bool
	var data []byte
	if warmup != nil {
		data, err = s.generateImagePreviewFromFile(ctx, file, previewSize, sourceFile, &original, true)
		if err == nil && !original && len(data) < minPreviewSize {
			err = ErrPreviewTooSmall
		}
	} else {
		data, err = s.generatePreviewLocked(ctx, file, previewSize, officeUrl, seekPercentage, fileMD5, cacheKey, sourceFile, &original)
	}
	if err != nil {
		return previewResult{}, err
	}
	if s.testHooks != nil && s.testHooks.afterGenerate != nil {
		s.testHooks.afterGenerate()
	}
	after, err := sourceFile.Stat()
	if err != nil {
		s.stats.sourceChanged.Add(1)
		return previewResult{}, fmt.Errorf("%w: %v", ErrPreviewSourceChanged, err)
	}
	if err := verifySourceUnchanged(before, after); err != nil {
		s.stats.sourceChanged.Add(1)
		return previewResult{}, err
	}
	// A rename can replace the pathname while the already-open descriptor still
	// points at the old inode. Reject publication in that case too.
	var pathAfter os.FileInfo
	if warmup != nil {
		pathAfter, err = warmup.stat(ctx)
		if err == nil && original {
			err = ErrWarmupOriginal
		}
	} else {
		pathAfter, err = statSourceVersion(file.RealPath)
	}
	if err != nil {
		s.stats.sourceChanged.Add(1)
		return previewResult{}, err
	}
	if err := verifySourceUnchanged(before, pathAfter); err != nil {
		s.stats.sourceChanged.Add(1)
		return previewResult{}, err
	}

	publishStart := time.Now()
	ready, err := s.publishPreviewCache(ctx, cacheKey, publication, data, original, warmup, background)
	var sourceCheck *previewSourceCheckError
	if errors.As(err, &sourceCheck) {
		if errors.Is(sourceCheck, ErrPreviewSourceChanged) {
			s.stats.sourceChanged.Add(1)
		}
		return previewResult{}, err
	}
	if err != nil {
		// The generated bytes are valid and are still returned; the miss is
		// visible in logs/counters and the next request retries the store. A
		// cache failure must not pretend the result was published.
		s.stats.storeFailures.Add(1)
		if warmup == nil {
			logger.Warningf("failed to publish preview cache entry: %v", err)
		}
	}
	s.stats.generatedBytes.Add(uint64(len(data)))
	s.stats.publishNanos.Add(uint64(time.Since(publishStart).Nanoseconds()))
	s.stats.generateNanos.Add(uint64(time.Since(start).Nanoseconds()))
	result := previewResult{data: data, ready: ready}
	if IsSpaceDeferred(err) {
		result.spaceErr = err
	}
	if publication != nil && publication.weakInfo != nil {
		result.weakInfo = after
	}
	return result, nil
}

func statSourceVersion(path string) (os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPreviewSourceChanged, err)
	}
	return info, nil
}

// verifyRequestedVersion checks the stat available before generation against the
// request metadata. Size is deliberately not compared: with the default disk
// usage mode FileInfo.Size is allocated blocks, not the logical file length.
func verifyRequestedVersion(info os.FileInfo, file iteminfo.ExtendedFileInfo) error {
	if file.ModTime.IsZero() {
		return nil
	}
	// Index-derived times may have second precision. Compare nanoseconds only
	// when the request actually carried them, so a second-precision index value
	// does not make every preview look stale. Same-second replacement with an
	// identical inode and timestamp remains undetectable here; 007-B's versioned
	// metadata is the place to close that gap.
	if info.ModTime().Unix() != file.ModTime.Unix() {
		return fmt.Errorf("%w: modification time changed", ErrPreviewSourceChanged)
	}
	if file.ModTime.Nanosecond() != 0 && info.ModTime().Nanosecond() != file.ModTime.Nanosecond() {
		return fmt.Errorf("%w: modification time changed", ErrPreviewSourceChanged)
	}
	return nil
}

// verifySourceUnchanged compares the stat captured immediately before the
// decode with the one captured after it. os.SameFile catches replacement even
// when size and timestamp are reused; an in-place rewrite that preserves both
// is outside what stat can prove and is documented as a known limit.
func verifySourceUnchanged(before, after os.FileInfo) error {
	if !os.SameFile(before, after) {
		return fmt.Errorf("%w: source inode changed", ErrPreviewSourceChanged)
	}
	if before.Size() != after.Size() {
		return fmt.Errorf("%w: size changed from %d to %d", ErrPreviewSourceChanged, before.Size(), after.Size())
	}
	if !before.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("%w: modification time changed", ErrPreviewSourceChanged)
	}
	if b, a := cacheledger.FromFileInfo(before), cacheledger.FromFileInfo(after); b.Strong && b != a {
		return fmt.Errorf("%w: change time changed", ErrPreviewSourceChanged)
	}
	return nil
}

const heavyPreviewBytes = 192 << 20

func (s *Service) settleMemory(estimateBytes int64) {
	if estimateBytes >= heavyPreviewBytes {
		s.stats.memoryReclaims.Add(1)
		debug.FreeOSMemory()
		return
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	if int64(m.HeapAlloc) >= s.budget.maxBytes/2 {
		s.stats.memoryReclaims.Add(1)
		runtime.GC()
	}
}

// filePreviewType represents the type of preview generation needed
type filePreviewType int

const (
	previewTypeDocument filePreviewType = iota
	previewTypeOffice
	previewTypeHEIC
	previewTypeImage
	previewTypeVideo
	previewTypeAudio
	previewTypeUnsupported
)

// determinePreviewType determines what type of preview generation is needed
func determinePreviewType(file iteminfo.ExtendedFileInfo) filePreviewType {
	// Check document conversion first
	if iteminfo.HasDocConvertableExtension(file.Name, file.Type) {
		return previewTypeDocument
	}

	// Check office files
	if file.OnlyOfficeId != "" {
		return previewTypeOffice
	}

	// Check HEIC with conversion enabled
	if strings.HasPrefix(file.Type, "image/heic") &&
		*settings.Config.Integrations.Media.Convert.ImagePreview[settings.HEICImagePreview] {
		return previewTypeHEIC
	}

	// Check by MIME type prefix
	switch {
	case strings.HasPrefix(file.Type, "image"):
		return previewTypeImage
	case strings.HasPrefix(file.Type, "video"):
		return previewTypeVideo
	case strings.HasPrefix(file.Type, "audio") && file.Metadata != nil && len(file.Metadata.AlbumArt) > 0:
		return previewTypeAudio
	default:
		return previewTypeUnsupported
	}
}

// generateRawPreview generates the initial preview image bytes based on file type.
func (s *Service) generateRawPreview(ctx context.Context, file iteminfo.ExtendedFileInfo, previewSize, officeUrl string, seekPercentage int, hash string) ([]byte, error) {
	f, err := os.Open(file.RealPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open preview source: %w", err)
	}
	defer f.Close()
	return s.generateRawPreviewFromFile(ctx, file, previewSize, officeUrl, seekPercentage, hash, f, nil)
}

func (s *Service) generateRawPreviewFromFile(ctx context.Context, file iteminfo.ExtendedFileInfo, previewSize, officeUrl string, seekPercentage int, hash string, source *os.File, original *bool) ([]byte, error) {
	previewType := determinePreviewType(file)

	switch previewType {
	case previewTypeDocument:
		return s.generateDocumentPreview(ctx, file, previewSize, hash)

	case previewTypeOffice:
		return s.generateOfficeFilePreview(ctx, file, officeUrl)

	case previewTypeHEIC:
		return s.generateHEICPreview(ctx, file, previewSize)

	case previewTypeImage:
		ext := strings.ToLower(filepath.Ext(file.Name))
		if iteminfo.IsRawImage(ext) {
			return nil, ErrUnsupportedFormat
		}
		if source == nil {
			return s.generateImagePreview(ctx, file, previewSize)
		}
		return s.generateImagePreviewFromFile(ctx, file, previewSize, source, original, false)

	case previewTypeVideo:
		return s.generateVideoPreviewBytes(ctx, file, seekPercentage)

	case previewTypeAudio:
		if file.Metadata != nil && len(file.Metadata.AlbumArt) > 0 {
			return file.Metadata.AlbumArt, nil
		}
		return nil, nil
	case previewTypeUnsupported:
		ext := strings.ToLower(filepath.Ext(file.Name))
		return nil, fmt.Errorf("unsupported media type: %s", ext)

	default:
		return nil, errors.New("unknown preview type")
	}
}

// generateDocumentPreview generates preview for PDF and document files
func (s *Service) generateDocumentPreview(ctx context.Context, file iteminfo.ExtendedFileInfo, previewSize, hash string) ([]byte, error) {
	tempFilePath := filepath.Join(s.cacheDir, "thumbnails", "docs", hash) + ".txt"
	imageBytes, err := s.GenerateImageFromDoc(ctx, file, tempFilePath, 0, previewSize) // 0 for the first page
	if err != nil {
		return nil, fmt.Errorf("failed to create image for PDF file: %w", err)
	}
	return imageBytes, nil
}

// generateOfficeFilePreview generates preview for Office files via OnlyOffice
func (s *Service) generateOfficeFilePreview(ctx context.Context, file iteminfo.ExtendedFileInfo, officeUrl string) ([]byte, error) {
	imageBytes, err := s.GenerateOfficePreview(ctx, filepath.Ext(file.Name), file.OnlyOfficeId, file.Name, officeUrl)
	if err != nil {
		return nil, fmt.Errorf("failed to create image for office file: %w", err)
	}
	return imageBytes, nil
}

// generateHEICPreview generates preview for HEIC files using FFmpeg
func (s *Service) generateHEICPreview(ctx context.Context, file iteminfo.ExtendedFileInfo, previewSize string) ([]byte, error) {
	s.stats.subprocesses.Add(1)
	imageBytes, err := s.convertHEICToJPEGWithFFmpeg(ctx, file.RealPath, previewSize)
	if err != nil {
		return nil, fmt.Errorf("failed to process HEIC image file: %w", err)
	}
	return imageBytes, nil
}

// generateVideoPreviewBytes generates preview frame from video file
func (s *Service) generateVideoPreviewBytes(ctx context.Context, file iteminfo.ExtendedFileInfo, seekPercentage int) ([]byte, error) {
	// Check if this video format is enabled for preview generation
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(file.Name)), ".")
	if !settings.CanConvertVideo(ext) {
		return nil, fmt.Errorf("video preview generation is disabled for .%s files in settings", ext)
	}

	videoSeekPercentage := seekPercentage
	if videoSeekPercentage == 0 {
		videoSeekPercentage = 10
	}

	s.stats.subprocesses.Add(1)
	imageBytes, err := s.GenerateVideoPreview(ctx, file.RealPath, videoSeekPercentage)
	if err != nil {
		// Don't log client cancellations as errors
		if ctx.Err() != context.Canceled {
			logger.Errorf("Video preview generation failed for '%s' (path: %s, seek: %d%%): %v",
				file.Name, file.RealPath, videoSeekPercentage, err)
		}
		return nil, fmt.Errorf("failed to create image for video file: %w", err)
	}
	return imageBytes, nil
}

// generateImagePreview generates preview for regular image files.
func (s *Service) generateImagePreview(ctx context.Context, file iteminfo.ExtendedFileInfo, previewSize string) ([]byte, error) {
	f, err := os.Open(file.RealPath)
	if err != nil {
		logger.Errorf("Failed to open image file '%s' (path: %s): %v", file.Name, file.RealPath, err)
		return nil, fmt.Errorf("failed to open image file: %w", err)
	}
	defer f.Close()
	return s.generateImagePreviewFromFile(ctx, file, previewSize, f, nil, false)
}

// generateImagePreviewFromFile is the admitted path: f is the descriptor whose
// identity and logical size were checked against the header estimate.
func (s *Service) generateImagePreviewFromFile(ctx context.Context, file iteminfo.ExtendedFileInfo, previewSize string, f *os.File, servedOriginal *bool, warmup bool) ([]byte, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPreviewSourceChanged, err)
	}
	if previewSize != "original" {
		fits := false
		if st.Size() < maxSizeForOriginal {
			fits = true
		} else if _, err := f.Seek(0, io.SeekStart); err == nil {
			if cfg, _, cfgErr := image.DecodeConfig(f); cfgErr == nil {
				fits = ImageFitsPreviewSize(cfg.Width, cfg.Height, previewSize)
			}
		}
		if fits {
			if warmup {
				*servedOriginal = true
				return nil, nil
			}
			if _, err := f.Seek(0, io.SeekStart); err == nil {
				original, readErr := io.ReadAll(io.LimitReader(f, st.Size()+1))
				if readErr == nil && int64(len(original)) == st.Size() {
					s.stats.originalServed.Add(1)
					if servedOriginal != nil {
						*servedOriginal = true
					}
					return original, nil
				}
			}
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrPreviewSourceChanged, err)
		}
	}

	options, err := getPreviewOptions(previewSize)
	if err != nil {
		return nil, err
	}

	ext := strings.ToLower(filepath.Ext(file.Name))
	format, err := s.FormatFromExtension(ext)
	if err != nil {
		return nil, err
	}
	options.Format = format

	// Count only the full-source decode path, not cached hits or the bounded
	// original-bytes shortcut above.
	s.stats.fullDecodes.Add(1)
	imageBytes, err := s.CreatePreview(ctx, f, st.Size(), options)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// For JPEG files, try FFmpeg fallback if initial decode failed
		if !warmup && strings.HasPrefix(file.Type, "image/jpeg") {
			return handleJPEGFallback(ctx, s, file, previewSize, err)
		}
		return nil, fmt.Errorf("failed to create image preview: %w", err)
	}

	return imageBytes, nil
}

// handleJPEGFallback attempts to use FFmpeg for problematic JPEG files
// This is preview-specific logic for user files, not used for icon generation
func handleJPEGFallback(ctx context.Context, s *Service, file iteminfo.ExtendedFileInfo, previewSize string, originalErr error) ([]byte, error) {
	enableJPEGFallback := *settings.Config.Integrations.Media.Convert.ImagePreview[settings.JPEGImagePreview]

	if !enableJPEGFallback {
		return nil, fmt.Errorf("failed to resize preview image (unsupported JPEG format, FFmpeg conversion disabled in settings): %w", originalErr)
	}

	if s.ffmpegService == nil {
		return nil, fmt.Errorf("failed to resize preview image (unsupported JPEG format, FFmpeg not available): %w", originalErr)
	}

	logger.Debugf("JPEG decode failed for '%s', falling back to FFmpeg: %v", file.Name, originalErr)
	s.stats.subprocesses.Add(1)
	imageBytes, err := s.convertImageWithFFmpeg(ctx, file.RealPath, previewSize)
	if err != nil {
		return nil, fmt.Errorf("failed to resize preview image with FFmpeg fallback: %w", err)
	}

	return imageBytes, nil
}

func (s *Service) generatePreviewLocked(ctx context.Context, file iteminfo.ExtendedFileInfo, previewSize, officeUrl string, seekPercentage int, fileMD5, cacheKey string, source *os.File, original *bool) ([]byte, error) {
	// Enforce file size limit for image preview generation to prevent memory exhaustion
	if strings.HasPrefix(file.Type, "image") && file.Size > iteminfo.LargeFileSizeThreshold {
		message := fmt.Sprintf("Image file too large for preview: %s (size: %d bytes, limit: %d bytes)",
			file.Name, file.Size, iteminfo.LargeFileSizeThreshold)
		logger.Warning(message)
		return nil, errors.New(message)
	}

	// Generate hash for temp file paths
	hasher := md5.New()
	_, _ = hasher.Write([]byte(cacheKey))
	hash := hex.EncodeToString(hasher.Sum(nil))

	// If this file type might have an embedded preview, try extracting it before any type-specific path.
	var imageBytes []byte
	fromEmbeddedPreview := false
	if hasEmbeddedPreview(file.Type, file.Name) {
		if previewBytes, _ := imagemeta.ExtractEmbeddedPreview(ctx, file.RealPath); len(previewBytes) >= minPreviewSize && isJPEG(previewBytes) {
			imageBytes = previewBytes
			fromEmbeddedPreview = true
			s.stats.embeddedPreview.Add(1)
			if orient := imagemeta.GetOrientation(ctx, file.RealPath); orient != "" {
				if corrected := applyOrientationToPreviewBytes(imageBytes, orient); len(corrected) >= minPreviewSize {
					imageBytes = corrected
				}
			}
		}
	}

	if !fromEmbeddedPreview {
		var err error
		imageBytes, err = s.generateRawPreviewFromFile(ctx, file, previewSize, officeUrl, seekPercentage, hash, source, original)
		if err != nil {
			return nil, err
		}
	}

	if len(imageBytes) < minPreviewSize {
		logger.Errorf("Generated image too small for '%s' (type: %s): %d bytes - likely an error occurred",
			file.Name, file.Type, len(imageBytes))
		return nil, ErrPreviewTooSmall
	}

	// When we got bytes from an embedded preview, we still need to resize to small/large/xlarge (original is never converted).
	// When we got bytes from type-specific path, regular images are already resized; others need resize below.
	previewType := determinePreviewType(file)
	if !fromEmbeddedPreview && (previewType == previewTypeImage ||
		(previewType == previewTypeDocument && settings.Env.MuPdfAvailable)) {
		return imageBytes, nil
	}

	// Check if context was cancelled during processing
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Resize if needed (embedded preview bytes, or videos, audio, documents). Original is never converted.
	if previewSize != "original" {
		options, err := getPreviewOptions(previewSize)
		if err != nil {
			return nil, err
		}

		if cfg, _, cfgErr := image.DecodeConfig(bytes.NewReader(imageBytes)); cfgErr == nil && ImageFitsPreviewSize(cfg.Width, cfg.Height, previewSize) {
			return imageBytes, nil
		}

		resizedBytes, err := s.CreatePreview(ctx, bytes.NewReader(imageBytes), 0, options)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// For JPEG files, try FFmpeg fallback if resize failed
			if strings.HasPrefix(file.Type, "image/jpeg") {
				resizedBytes, err = handleJPEGFallback(ctx, s, file, previewSize, err)
				if err != nil {
					return nil, err
				}
			} else {
				return nil, fmt.Errorf("failed to resize preview image: %w", err)
			}
		}

		return resizedBytes, nil
	}

	// Return the original-size bytes; the caller publishes them once the source
	// version has been verified.
	return imageBytes, nil
}

// GeneratePreviewWithMD5 generates one preview without joining a same-key
// flight. GetPreviewForFile is the normal entry point; this function is kept
// for callers that already own the request and for tests.
func GeneratePreviewWithMD5(ctx context.Context, file iteminfo.ExtendedFileInfo, previewSize, officeUrl string, seekPercentage int, fileMD5 string) ([]byte, error) {
	if service == nil {
		return nil, ErrPreviewUnavailable
	}
	if fileMD5 == "" {
		return nil, fmt.Errorf("preview generation failed: cache hash is empty for file %s", file.Name)
	}
	// This entry point does not join a flight, but it still must not create
	// unbounded direct decode tasks.
	if !service.tasks.tryAcquire() {
		return nil, ErrPreviewSaturated
	}
	defer service.tasks.release()
	result, err := service.generateAndPublish(ctx, file, previewSize, officeUrl, seekPercentage, fileMD5, CacheKey(fileMD5, previewSize, seekPercentage), nil, nil, nil, false)
	return result.data, err
}

func GeneratePreview(ctx context.Context, file iteminfo.ExtendedFileInfo, previewSize, officeUrl string, seekPercentage int) ([]byte, error) {
	cacheHash := previewCacheHash(file)
	return GeneratePreviewWithMD5(ctx, file, previewSize, officeUrl, seekPercentage, cacheHash)
}

// ImageFitsPreviewSize reports whether both image dimensions are within the preview bounds.
func ImageFitsPreviewSize(imgWidth, imgHeight int, previewSize string) bool {
	if previewSize == "original" {
		return true
	}
	opts, err := getPreviewOptions(previewSize)
	if err != nil {
		return false
	}
	return imgWidth <= opts.Width && imgHeight <= opts.Height
}

// ReadImageFileDimensions reads image width and height without decoding the full image.
func ReadImageFileDimensions(path string) (int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

// ShouldServeOriginalImage reports whether an on-disk image is already within preview bounds.
func ShouldServeOriginalImage(path, previewSize string) bool {
	width, height, err := ReadImageFileDimensions(path)
	if err != nil {
		return false
	}
	return ImageFitsPreviewSize(width, height, previewSize)
}

// getPreviewOptions returns resize options for the given preview size
func getPreviewOptions(previewSize string) (ResizeOptions, error) {
	switch previewSize {
	case "xlarge":
		return ResizeOptions{
			Width:      1024,
			Height:     1024,
			ResizeMode: ResizeModeFit,
			Quality:    QualityHigh,
			Format:     FormatJpeg,
		}, nil
	case "large":
		return ResizeOptions{
			Width:      640,
			Height:     640,
			ResizeMode: ResizeModeFit,
			Quality:    QualityMedium,
			Format:     FormatJpeg,
		}, nil
	case "small":
		return ResizeOptions{
			Width:      256,
			Height:     256,
			ResizeMode: ResizeModeFill,
			Quality:    QualityLow,
			Format:     FormatJpeg,
		}, nil
	default:
		return ResizeOptions{}, ErrUnsupportedFormat
	}
}

// CreatePreview resizes an image from a reader with the given options
func (s *Service) CreatePreview(ctx context.Context, reader io.Reader, fileSize int64, options ResizeOptions) ([]byte, error) {
	output := &bytes.Buffer{}
	if err := s.ResizeWithSize(ctx, reader, output, fileSize, options); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func CacheKey(md5, previewSize string, percentage int) string {
	key := fmt.Sprintf("%x%x%x", md5, previewSize, percentage)
	return key
}

func DelThumbs(ctx context.Context, file iteminfo.ExtendedFileInfo) {
	if service == nil {
		return
	}
	// Generate metadata-based cache hash for deletion
	hasher := md5.New()
	cacheString := fmt.Sprintf("%s:%d:%s", file.RealPath, file.Size, file.ModTime.Format(time.RFC3339Nano))
	_, _ = hasher.Write([]byte(cacheString))
	cacheHash := hex.EncodeToString(hasher.Sum(nil))

	small := CacheKey(cacheHash, "small", 0)
	unlock, err := service.orphans.lockKey(ctx, small)
	if err != nil {
		return
	}
	errSmall := service.fileCache.Delete(ctx, small)
	unlock()
	if errSmall != nil {
		large := CacheKey(cacheHash, "large", 0)
		unlock, err := service.orphans.lockKey(ctx, large)
		if err != nil {
			return
		}
		errLarge := service.fileCache.Delete(ctx, large)
		unlock()
		if errLarge != nil {
			logger.Debugf("Could not delete thumbnail: %v", file.Name)
		}
	}
}
