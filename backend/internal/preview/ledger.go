// Added for mument 007-C. See docs/development/007c/DESIGN.md.
package preview

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/gtsteffaniak/go-logger/logger"
)

type ledgerPreview struct {
	source             string
	path, key, variant string
	version            cacheledger.Version
	ref                cacheledger.Ref
	weakInfo           os.FileInfo
}

func (s *Service) startLedger() {
	repo, err := cacheledger.Open(filepath.Join(s.cacheDir, "image-preview-ledger.sqlite"))
	if err != nil {
		logger.Warningf("image preview ledger unavailable: %v", err)
		return
	}
	cutoff, err := repo.RecoveryCutoff(s.baseCtx)
	if err != nil {
		logger.Warningf("cannot initialize preview ledger recovery: %v", err)
		if closeErr := repo.Close(); closeErr != nil {
			logger.Warningf("cannot close preview ledger: %v", closeErr)
		}
		return
	}
	s.ledger = repo
	s.recoveryDone = make(chan struct{})
	go s.runLedgerRecovery(cutoff)
}

func (s *Service) runLedgerRecovery(cutoff int64) {
	defer close(s.recoveryDone)
	repo, ctx := s.ledger, s.baseCtx
	for {
		if err := s.recoverLedgerPages(ctx, cutoff); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-repo.RecoveryRequested():
		}
		delay := 10 * time.Millisecond
		for {
			// Capture the high water mark only after the active rename group exits.
			unlock, err := repo.LockLocations(ctx)
			if err != nil {
				return
			}
			next, err := repo.RecoveryCutoff(ctx)
			unlock()
			if err == nil {
				cutoff = next
				break
			}
			if ctx.Err() != nil {
				return
			}
			logger.Warningf("cannot read preview ledger recovery cutoff: %v", err)
			if !waitLedgerRetry(ctx, delay) {
				return
			}
			delay = min(2*delay, 5*time.Second)
		}
	}
}

func (s *Service) recoverLedgerPages(ctx context.Context, cutoff int64) error {
	delay := 10 * time.Millisecond
	for ctx.Err() == nil {
		n, err := s.ledger.RecoverBefore(ctx, cutoff, 64)
		if err == nil && n == 0 {
			return nil
		}
		if err != nil && ctx.Err() == nil {
			logger.Warningf("preview ledger recovery page failed: %v", err)
			delay = min(delay*2, 5*time.Second)
		} else {
			delay = 10 * time.Millisecond
		}
		if !waitLedgerRetry(ctx, delay) {
			break
		}
	}
	return ctx.Err()
}

func waitLedgerRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Static raster requests use the ledger. Embedded previews, animated formats,
// external converters, office documents and other media keep their old keys.
func ledgerVariant(file iteminfo.ExtendedFileInfo, size string) string {
	if file.OnlyOfficeId != "" || (file.Metadata != nil && len(file.Metadata.AlbumArt) > 0) ||
		!strings.HasPrefix(file.Type, "image/") || hasEmbeddedPreview(file.Type, file.Name) {
		return ""
	}
	var format string
	switch strings.ToLower(filepath.Ext(file.Name)) {
	case ".jpg", ".jpeg", ".pbm", ".pgm", ".ppm", ".pam":
		format = "jpeg"
	case ".png":
		format = "png"
	case ".tif", ".tiff":
		format = "tiff"
	case ".bmp":
		format = "bmp"
	default:
		return ""
	}
	opts, err := getPreviewOptions(size)
	if err != nil {
		return ""
	}
	fallback := file.Type == "image/jpeg" && settings.Config.Integrations.Media.Convert.ImagePreview[settings.JPEGImagePreview] != nil &&
		*settings.Config.Integrations.Media.Convert.ImagePreview[settings.JPEGImagePreview]
	// Revision covers EXIF orientation, resampling, embedded previews and
	// fallback behavior. The source extension selects JPEG or PNG encoding.
	return fmt.Sprintf("raster-v1/%s/%s/%dx%d/%d/%d/%d/fallback:%t", file.Type, format, opts.Width, opts.Height, opts.ResizeMode, opts.Quality, opts.Quality.jpegQuality(), fallback)
}

func (s *Service) planLedgerPreview(ctx context.Context, file iteminfo.ExtendedFileInfo, size string) (*ledgerPreview, error) {
	return s.planLedgerPreviewWithStat(ctx, file, size, nil, true)
}

func (s *Service) planLedgerPreviewWithStat(ctx context.Context, file iteminfo.ExtendedFileInfo, size string, guarded *warmupInput, resolve bool) (*ledgerPreview, error) {
	variant := ledgerVariant(file, size)
	if variant == "" || file.Source == "" {
		return nil, nil
	}
	idx := indexing.GetIndex(file.Source)
	if idx == nil {
		return nil, nil
	}
	path := filepath.Clean(file.RealPath)
	stat := func() (os.FileInfo, error) { return os.Stat(path) }
	if guarded != nil {
		stat = func() (os.FileInfo, error) { return guarded.stat(ctx) }
	}
	info, err := stat()
	if err != nil && guarded != nil {
		return nil, err
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrPreviewSourceChanged
	}
	v := cacheledger.FromFileInfo(info)
	plan := &ledgerPreview{source: file.Source, path: path, variant: variant, version: v}
	uncached := func() (*ledgerPreview, error) {
		// This identity lives only in the flight map. Weak stat fields can
		// coalesce overlapping requests, but cannot address durable bytes.
		plan.key = "c-uncached/" + cacheledger.Digest(fmt.Appendf(nil, "%q/%+v/%s", path, v, variant))
		return plan, nil
	}
	if !v.Strong {
		if err := verifyRequestedVersion(info, file); err != nil {
			return nil, err
		}
		plan.weakInfo = info
		return uncached()
	}
	if s.ledger == nil {
		return uncached()
	}

	namespace, name, ok := cacheledger.Location(idx.Path, path)
	if !ok {
		return uncached()
	}
	if !resolve {
		ref, err := s.ledger.FindRef(ctx, namespace, name, v)
		if err != nil {
			return nil, ErrWarmupStore
		}
		plan.ref = ref
		if ref.Asset != "" {
			plan.key = "c-raster/" + ref.Asset + "/" + variant
		}
		return plan, nil
	}

	// Serialize lookup/create with the short application rename/commit window.
	// Never hold this lock while reading originals or waiting for a flight.
	unlock, err := s.ledger.LockLocations(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	current, err := stat()
	if err != nil && guarded != nil {
		return nil, err
	}
	if err != nil || cacheledger.FromFileInfo(current) != v {
		return nil, ErrPreviewSourceChanged
	}
	ref, err := s.ledger.Resolve(ctx, namespace, name, v)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if guarded != nil {
			return nil, ErrWarmupStore
		}
		logger.Warningf("cannot resolve image preview cache reference: %v", err)
		return uncached()
	}
	plan.ref = ref
	plan.key = "c-raster/" + ref.Asset + "/" + variant
	return plan, nil
}

func (s *Service) loadPreviewCache(ctx context.Context, key string, plan *ledgerPreview) (previewResult, bool, error) {
	unlock, err := s.orphans.lockKey(ctx, key)
	if err != nil {
		return previewResult{}, false, err
	}
	defer unlock()
	if plan == nil {
		data, found, err := s.loadCached(ctx, key)
		return previewResult{data: data}, found, err
	}
	if plan.ref.Asset == "" {
		return previewResult{}, false, nil
	}
	result, found, err := s.ledger.Lookup(ctx, plan.ref, plan.variant)
	if err != nil {
		if ctx.Err() != nil {
			return previewResult{}, false, ctx.Err()
		}
		logger.Warningf("cannot look up image preview cache: %v", err)
		return previewResult{}, false, nil
	}
	if !found {
		return previewResult{}, false, nil
	}
	var data []byte
	// A generated raster thumbnail is bounded by the largest enabled dimensions.
	const maximumThumbnailBytes = 32 << 20
	if result.Size <= maximumThumbnailBytes {
		if sized, ok := s.fileCache.(interface {
			LoadSized(context.Context, string, int64) ([]byte, bool, error)
		}); ok {
			data, found, err = sized.LoadSized(ctx, result.Key, result.Size)
		} else {
			data, found, err = s.fileCache.Load(ctx, result.Key)
		}
	} else {
		found = false
	}
	if err != nil {
		logger.Warningf("cannot read recorded image preview: %v", err)
		return previewResult{}, false, nil
	}
	if !found || !result.Matches(data) {
		if s.capacity != nil {
			pending, err := s.ledger.PendingCapacityPublication(ctx, key)
			if err != nil || pending {
				return previewResult{}, false, nil
			}
		}
		if s.space != nil && s.space.cfg.Mode == "enforce" &&
			(s.space.tree == nil || s.space.tree.Check(false) != nil) {
			return previewResult{}, false, nil
		}
		rejected, err := s.ledger.Reject(ctx, plan.ref, plan.variant, result)
		if err != nil && ctx.Err() == nil {
			logger.Warningf("cannot invalidate corrupt image preview: %v", err)
		}
		if rejected {
			s.stats.cacheCorrupt.Add(1)
		}
		return previewResult{}, false, nil
	}
	if version, err := cacheledger.Observe(plan.path); err != nil || version != plan.version {
		return previewResult{}, false, ErrPreviewSourceChanged
	}
	return previewResult{data: data, ready: result}, true, nil
}

// previewSourceCheckError distinguishes a failed source recheck from a
// failed cache write. Only the latter can still deliver the generated bytes.
type previewSourceCheckError struct{ cause error }

func (e *previewSourceCheckError) Error() string { return e.cause.Error() }
func (e *previewSourceCheckError) Unwrap() error { return e.cause }

func (s *Service) publishPreviewCache(ctx context.Context, key string, plan *ledgerPreview, data []byte, original bool, warmup *warmupInput, background bool) (cacheledger.Result, error) {
	unlock, err := s.orphans.lockKey(ctx, key)
	if err != nil {
		return cacheledger.Result{}, err
	}
	defer unlock()
	if plan == nil {
		return cacheledger.Result{}, s.storePreview(ctx, key, data, background)
	}
	if original || plan.ref.Asset == "" {
		return cacheledger.Result{}, nil
	}
	if s.capacity != nil {
		pending, err := s.ledger.PendingCapacityPublication(ctx, key)
		if err != nil {
			return cacheledger.Result{}, err
		}
		if pending {
			return cacheledger.Result{}, &SpaceDeferred{"capacity_recovery"}
		}
	}
	if err := s.storePreview(ctx, key, data, background); err != nil {
		return cacheledger.Result{}, err
	}
	if warmup != nil {
		unlock, err := s.ledger.LockLocations(ctx)
		if err != nil {
			return cacheledger.Result{}, err
		}
		defer unlock()
		info, err := warmup.stat(ctx)
		if err != nil {
			return cacheledger.Result{}, &previewSourceCheckError{cause: err}
		}
		if cacheledger.FromFileInfo(info) != plan.version {
			return cacheledger.Result{}, &previewSourceCheckError{cause: ErrPreviewSourceChanged}
		}
	}
	stamp := s.orphans.publicationStamp(key)
	var proof string
	if s.capacity != nil && stamp != "" {
		proof = plan.capacityProof()
	}
	return s.ledger.PublishScoped(ctx, plan.ref, plan.variant, key, data, stamp, proof)
}

func (s *Service) deliverPreview(ctx context.Context, plan *ledgerPreview, result previewResult, foreground bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if plan != nil && plan.weakInfo != nil {
		// A replacement may have reused the same size and mtime. Each
		// waiter must match both the task's descriptor and the live path.
		if err := verifySourceUnchanged(plan.weakInfo, result.weakInfo); err != nil {
			s.stats.sourceChanged.Add(1)
			return nil, err
		}
		current, err := statSourceVersion(plan.path)
		if err != nil {
			s.stats.sourceChanged.Add(1)
			return nil, err
		}
		if err := verifySourceUnchanged(result.weakInfo, current); err != nil {
			s.stats.sourceChanged.Add(1)
			return nil, err
		}
	}
	var deferred *SpaceDeferred
	if foreground && errors.As(result.spaceErr, &deferred) && deferred.reason == "capacity_recovery" {
		if err := s.recordCapacityDemand(ctx, plan); err != nil {
			s.capacity.rememberUsageFailure(plan.key, time.Now())
		}
	}
	if foreground && result.ready.Key != "" {
		now := time.Now()
		if err := s.ledger.Touch(ctx, plan.ref, plan.variant, result.ready, now); err != nil && ctx.Err() == nil {
			if s.capacity != nil {
				s.capacity.rememberUsageFailure(result.ready.Key, now)
			}
			logger.Warningf("cannot record image preview use: %v", err)
		}
	}
	if foreground && result.spaceErr != nil && s.space != nil {
		s.space.mu.Lock()
		s.space.status.ForegroundUnstored++
		s.space.mu.Unlock()
	}
	return result.data, nil
}

// MoveLedger is available to the authorized filesystem adapter. Its lifetime
// is bounded by HTTP drain, followed by preview/recovery shutdown.
func MoveLedger() *cacheledger.Repository {
	if service == nil || service.closed.Load() {
		return nil
	}
	return service.ledger
}

// DiscardMovedLegacy preserves the historical invalidation for previews that
// still use the legacy keys. Ledger thumbnail bytes outlive their old path.
func DiscardMovedLegacy(ctx context.Context, file iteminfo.ExtendedFileInfo) {
	if ledgerVariant(file, "small") == "" {
		DelThumbs(ctx, file)
	}
}
