package preview

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
)

const (
	warmupSize         = "xlarge"
	maxSizeForOriginal = 256 * 1024
)

var (
	ErrWarmupCached      = errors.New("warmup variant already cached")
	ErrWarmupOriginal    = errors.New("warmup would serve original")
	ErrWarmupUnsupported = errors.New("warmup requires a static JPEG or PNG")
	ErrWarmupStore       = errors.New("warmup cache unavailable")
	errWarmupFlight      = errors.New("retry preview through interactive admission")
)

type WarmupDirectory struct {
	Source, Path string
}

type warmupInput struct {
	open     func(context.Context) (*os.File, error)
	stat     func(context.Context) (os.FileInfo, error)
	check    func(int64) error
	estimate previewEstimate
}

func (s *Service) WarmupCacheRoot() string { return s.cacheDir }

func (s *Service) EnableWarmupHints() { s.warmupEnabled.Store(true) }

func (s *Service) WarmupDirectoryHint() *WarmupDirectory {
	if time.Since(time.Unix(0, s.lastInteractive.Load())) > 5*time.Minute {
		return nil
	}
	return s.recentDirectory.Load()
}

func (s *Service) IdleForWarmup() bool {
	return !s.closed.Load() && s.interactiveRequests.Load() == 0 &&
		s.stats.activeTasks.Load() == 0 && s.stats.waitingTasks.Load() == 0 &&
		time.Since(time.Unix(0, s.lastInteractive.Load())) >= time.Second
}

// WarmPreviewWhenIdle accepts only a worker-supplied constrained opener and
// stat. A normal waiter joining a deferred flight retries ordinary admission.
func (s *Service) WarmPreviewWhenIdle(ctx context.Context, file iteminfo.ExtendedFileInfo,
	open func(context.Context) (*os.File, error), stat func(context.Context) (os.FileInfo, error), check func(int64) error) error {
	if !s.IdleForWarmup() || !s.warmupRequest.CompareAndSwap(false, true) {
		return ErrPreviewBusy
	}
	defer s.warmupRequest.Store(false)
	if err := s.WarmupSpace(); err != nil {
		return err
	}
	if s.ledger == nil {
		return ErrWarmupStore
	}
	switch strings.ToLower(filepath.Ext(file.Name)) {
	case ".jpg", ".jpeg":
		if file.Type != "image/jpeg" {
			return ErrWarmupUnsupported
		}
	case ".png":
		if file.Type != "image/png" {
			return ErrWarmupUnsupported
		}
	default:
		return ErrWarmupUnsupported
	}

	input := &warmupInput{open: open, stat: stat, check: func(estimate int64) error {
		if err := s.WarmupSpace(); err != nil {
			return err
		}
		if check != nil {
			return check(estimate)
		}
		return nil
	}}
	plan, err := s.planLedgerPreviewWithStat(ctx, file, warmupSize, input, false)
	if err != nil {
		return err
	}
	if plan == nil || !plan.version.Strong {
		return ErrWarmupStore
	}
	if plan.ref.Asset != "" {
		if err := s.checkWarmupCache(ctx, plan); err != nil {
			return err
		}
	}
	if !s.IdleForWarmup() {
		return ErrPreviewBusy
	}

	// The sole warmup preflight uses its own bounded header read. Avoid
	// creating ledger assets for unsupported, original or oversized files.
	input.estimate, err = estimateWarmup(ctx, file, input)
	if err != nil {
		return err
	}
	if err := s.budget.checkEstimate(input.estimate.Bytes); err != nil {
		return err
	}
	if !s.IdleForWarmup() {
		return ErrPreviewBusy
	}
	if plan.ref.Asset == "" {
		plan, err = s.planLedgerPreviewWithStat(ctx, file, warmupSize, input, true)
		if err != nil {
			return err
		}
		if plan == nil || plan.ref.Asset == "" {
			return ErrWarmupStore
		}
	}
	result, err := s.flights.doWithAdmission(ctx, plan.key, true, func(taskCtx context.Context, markStarted func() bool) (previewResult, error) {
		result, err := s.generateAndPublish(taskCtx, file, warmupSize, "", 0, plan.key, plan.key, markStarted, plan, input, true)
		if err != nil {
			return result, errors.Join(errWarmupFlight, err)
		}
		return result, nil
	})
	if err == nil && result.ready.Key == "" {
		if result.spaceErr != nil {
			return result.spaceErr
		}
		return ErrWarmupStore
	}
	return err
}

func (s *Service) checkWarmupCache(ctx context.Context, plan *ledgerPreview) error {
	unlock, err := s.orphans.lockKey(ctx, plan.key)
	if err != nil {
		return err
	}
	defer unlock()
	cache, ok := s.fileCache.(interface {
		HasSized(string, int64) (bool, error)
	})
	if !ok {
		return ErrWarmupStore
	}
	result, found, err := s.ledger.Lookup(ctx, plan.ref, plan.variant)
	if err != nil {
		return ErrWarmupStore
	}
	if found {
		exists, err := cache.HasSized(result.Key, result.Size)
		if err != nil {
			return ErrWarmupStore
		}
		if exists {
			return ErrWarmupCached
		}
	}
	return s.warmupCooldown(ctx, plan)
}

func estimateWarmup(ctx context.Context, file iteminfo.ExtendedFileInfo, input *warmupInput) (previewEstimate, error) {
	f, err := input.open(ctx)
	if err != nil {
		return previewEstimate{}, err
	}
	defer f.Close()
	binding, err := newSourceBinding(f)
	if err != nil {
		return previewEstimate{}, err
	}
	if binding.size > iteminfo.LargeFileSizeThreshold {
		return previewEstimate{}, ErrWarmupUnsupported
	}
	header, err := readPreviewHeader(ctx, f)
	if err != nil {
		return previewEstimate{}, err
	}
	target := previewTargetBytes(warmupSize)
	var estimate previewEstimate
	if file.Type == "image/jpeg" {
		estimate, err = estimateJPEGHeader(header, target)
	} else if err = checkStaticPNG(header); err == nil {
		estimate, err = estimateNonJPEGHeader(ctx, header, file, target)
	}
	if err != nil {
		return previewEstimate{}, err
	}
	if !binding.matches(f) {
		return previewEstimate{}, ErrPreviewSourceChanged
	}
	// The low-cost rule matches the foreground size cutoff; large originals
	// whose dimensions fit are checked by the existing raster generator.
	if binding.size < maxSizeForOriginal {
		return previewEstimate{}, ErrWarmupOriginal
	}
	estimate.Source = &binding
	return estimate, nil
}

// acTL must precede IDAT in a conforming APNG. An incomplete prefix or a PNG
// with an oversized pre-IDAT chunk is outside the bounded warmup path.
func checkStaticPNG(header []byte) error {
	if !bytes.HasPrefix(header, []byte("\x89PNG\r\n\x1a\n")) {
		return ErrPreviewHeaderInvalid
	}
	for rest := header[8:]; len(rest) >= 8; {
		switch string(rest[4:8]) {
		case "IDAT":
			return nil
		case "acTL", "fcTL", "fdAT":
			return ErrWarmupUnsupported
		}
		size := uint64(binary.BigEndian.Uint32(rest[:4])) + 12
		if size > uint64(len(rest)) {
			break
		}
		rest = rest[size:]
	}
	return ErrPreviewHeaderInvalid
}

type WarmupMetrics struct {
	FullDecodes  uint64 `json:"fullDecodes"`
	DecodeWaitNS uint64 `json:"decodeWaitNanos"`
	WarmupHoldNS uint64 `json:"warmupHoldNanos"`
	WarmupMaxNS  uint64 `json:"warmupMaxHoldNanos"`
}

func (s *Service) WarmupMetrics() WarmupMetrics {
	return WarmupMetrics{
		FullDecodes:  s.stats.fullDecodes.Load(),
		DecodeWaitNS: s.stats.queueWaitNanos.Load(),
		WarmupHoldNS: s.stats.warmupHoldNanos.Load(),
		WarmupMaxNS:  s.stats.warmupMaxHoldNanos.Load(),
	}
}

func TrackWarmupBrowse() func() {
	s := service
	if s == nil || !s.warmupEnabled.Load() {
		return func() {}
	}
	s.interactiveRequests.Add(1)
	return func() {
		s.lastInteractive.Store(time.Now().UnixNano())
		s.interactiveRequests.Add(-1)
	}
}

func NoteWarmupDirectory(source, realPath string) {
	if service != nil && service.warmupEnabled.Load() && filepath.IsAbs(realPath) {
		service.noteWarmupDirectory(source, realPath)
	}
}

func (s *Service) noteWarmupDirectory(source, realPath string) {
	directory := WarmupDirectory{source, realPath}
	if previous := s.recentDirectory.Load(); previous == nil || *previous != directory {
		s.recentDirectory.Store(&directory)
	}
}
