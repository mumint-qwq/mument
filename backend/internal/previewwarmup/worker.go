package previewwarmup

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	indexsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/gtsteffaniak/go-logger/logger"
)

const (
	claimInterval = 500 * time.Millisecond
	quietFileAge  = 5 * time.Second
)

var (
	errScopeOffline        = errors.New("scope_offline")
	errIndexNotReady       = errors.New("index_not_ready")
	errFileUnavailable     = errors.New("file_unavailable")
	errLowSpace            = errors.New("low_cache_space")
	errMemoryPressure      = errors.New("memory_pressure")
	errCPUThrottled        = errors.New("cpu_throttled")
	errPressureUnavailable = errors.New("pressure_metrics_unavailable")
	ErrInvalidAction       = errors.New("invalid warmup operation")
	ErrInactive            = errors.New("warmup is unavailable")
)

type scope struct {
	config     settings.PreviewWarmupScope
	sourceRoot string
	root       string
	prefix     string
	cursor     string
	pinned     scopeID
	turn       uint64
}

func (s *scope) indexReady() bool {
	idx := indexing.GetIndex(s.config.Source)
	return idx != nil && idx.Path == s.sourceRoot && idx.GetStatus() == indexing.READY
}

type manualRun struct {
	scope  int
	cursor string
}

type ScopeStatus struct {
	Scope   int    `json:"scope"`
	Reason  string `json:"reason"`
	Checked uint64 `json:"checked"`
}

type Status struct {
	Enabled     bool   `json:"enabled"`
	Paused      bool   `json:"paused"`
	Active      bool   `json:"active"`
	Reason      string `json:"reason"`
	ManualScope int    `json:"manualScope,omitempty"`
	Discovered  uint64 `json:"discovered"`
	Cached      uint64 `json:"cached"`
	Generated   uint64 `json:"generated"`
	Original    uint64 `json:"original"`
	Changed     uint64 `json:"changed"`
	Retried     uint64 `json:"retried"`
	Terminal    uint64 `json:"terminal"`
	Deferred    uint64 `json:"deferred"`
	History     uint64 `json:"historyChecked"`
	Recent      uint64 `json:"recentChecked"`
	preview.WarmupMetrics
	Scopes []ScopeStatus `json:"scopes"`
}

type Worker struct {
	db     *indexsql.IndexDB
	svc    *preview.Service
	scopes []scope
	probe  resourceProbe
	done   chan struct{}

	mu     sync.Mutex
	status Status
	manual *manualRun
	cancel context.CancelFunc
}

func New(ctx context.Context, server settings.Server, db *indexsql.IndexDB, svc *preview.Service) *Worker {
	w := &Worker{db: db, svc: svc, done: make(chan struct{}), status: Status{Reason: "disabled"}}
	if !server.PreviewWarmup.Enabled {
		close(w.done)
		return w
	}
	w.status.Enabled = true
	if !platformSupported {
		w.status.Reason = "platform_unavailable"
		close(w.done)
		return w
	}
	if db == nil || svc == nil || db.PreparePreviewWarmup(ctx) != nil {
		w.status.Reason = "initialization_failed"
		logger.Warning("preview warmup could not initialize index state")
		close(w.done)
		return w
	}
	for i, cfg := range server.PreviewWarmup.Scopes {
		sourceRoot := server.NameToSource[cfg.Source].Path
		w.scopes = append(w.scopes, scope{
			config: cfg, root: filepath.Join(sourceRoot, cfg.Directory), sourceRoot: sourceRoot,
			prefix: indexPrefix(cfg.Directory),
		})
		w.status.Scopes = append(w.status.Scopes, ScopeStatus{Scope: i + 1, Reason: "pending"})
	}
	w.status.Reason = "starting"
	w.probe = newResourceProbe(svc.WarmupCacheRoot())
	svc.EnableWarmupHints()
	go w.run(ctx)
	return w
}

func indexPrefix(relative string) string {
	if relative == "." {
		return "/"
	}
	return "/" + filepath.ToSlash(relative) + "/"
}

func (w *Worker) Wait() { <-w.done }

func (w *Worker) Snapshot() Status {
	w.mu.Lock()
	s := w.status
	s.Scopes = append([]ScopeStatus(nil), s.Scopes...)
	if w.manual != nil {
		s.ManualScope = w.manual.scope + 1
	}
	w.mu.Unlock()
	if w.svc != nil {
		s.WarmupMetrics = w.svc.WarmupMetrics()
	}
	return s
}

// Control accepts only a configured scope number. Commands are process-local.
func (w *Worker) Control(action string, number int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.scopes) == 0 {
		return ErrInactive
	}
	if (action != "fill" && number != 0) || (action == "fill" && (number < 1 || number > len(w.scopes))) {
		return ErrInvalidAction
	}
	switch action {
	case "pause":
		w.status.Paused = true
		w.status.Reason = "paused"
	case "resume":
		w.status.Paused = false
		w.status.Reason = "starting"
		return nil
	case "fill":
		if w.manual != nil {
			return ErrInvalidAction
		}
		w.manual = &manualRun{scope: number - 1}
		return nil
	case "cancel":
		w.manual = nil
		w.status.Reason = "cancelled"
	default:
		return ErrInvalidAction
	}
	if w.cancel != nil {
		w.cancel()
	}
	return nil
}

func (w *Worker) run(ctx context.Context) {
	defer close(w.done)
	tick := time.NewTicker(claimInterval)
	defer tick.Stop()
	report := time.NewTicker(time.Minute)
	defer report.Stop()
	next := 0
	manualTurn := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-report.C:
			s := w.Snapshot()
			logger.Infof("preview warmup reason=%s discovered=%d cached=%d generated=%d original=%d changed=%d retried=%d terminal=%d deferred=%d",
				s.Reason, s.Discovered, s.Cached, s.Generated, s.Original, s.Changed, s.Retried, s.Terminal, s.Deferred)
		case <-tick.C:
			w.mu.Lock()
			if w.status.Paused {
				w.mu.Unlock()
				continue
			}
			var manual *manualRun
			if manualTurn {
				manual = w.manual
			}
			manualTurn = !manualTurn
			job, cancel := context.WithCancel(ctx)
			w.cancel = cancel
			w.mu.Unlock()

			number := next
			if manual != nil {
				number = manual.scope
			} else {
				next = (next + 1) % len(w.scopes)
			}
			reason := w.step(job, number, manual)
			w.mu.Lock()
			if job.Err() == nil {
				w.status.Reason = reason
			}
			cancel()
			w.cancel = nil
			w.mu.Unlock()
		}
	}
}

type lane uint8

const (
	historyLane lane = iota
	recentLane
	directoryLane
	manualLane
)

func (w *Worker) selectPage(ctx context.Context, scope *scope, manual *manualRun) ([]indexsql.PreviewWarmupCandidate, lane, error) {
	selected, prefix, cursor := historyLane, scope.prefix, scope.cursor
	if manual != nil {
		selected, cursor = manualLane, manual.cursor
	} else {
		scope.turn++
		switch scope.turn % 4 {
		case 1:
			selected = recentLane
		case 3:
			if hint := w.svc.WarmupDirectoryHint(); hint != nil && hint.Source == scope.config.Source {
				if rel, err := filepath.Rel(scope.sourceRoot, hint.Path); err == nil &&
					settings.WarmupContains(scope.config.Directory, rel) && rel != ".." && !strings.HasPrefix(rel, "../") {
					selected, prefix, cursor = directoryLane, indexPrefix(rel), ""
				}
			}
		}
	}
	rows, err := w.db.PreviewWarmupPage(ctx, scope.config.Source, prefix, cursor, selected == manualLane, selected == recentLane)
	return rows, selected, err
}

func (w *Worker) step(ctx context.Context, number int, manual *manualRun) string {
	if !w.svc.IdleForWarmup() {
		return "foreground_busy"
	}
	if err := w.probe.poll(); err != nil {
		return err.Error()
	}
	scope := &w.scopes[number]
	id, err := scope.online()
	if err != nil {
		w.record(number, err.Error(), 0)
		return err.Error()
	}
	page, selected, err := w.selectPage(ctx, scope, manual)
	if err != nil {
		return "index_query_failed"
	}
	w.record(number, "idle", len(page))
	if len(page) == 0 {
		if selected == historyLane {
			scope.cursor = ""
		}
		if manual != nil {
			w.mu.Lock()
			if w.manual == manual {
				w.manual = nil
			}
			w.mu.Unlock()
		}
		return "idle"
	}
	for _, candidate := range page {
		if ctx.Err() != nil || !w.svc.IdleForWarmup() {
			return "foreground_busy"
		}
		now := time.Now()
		result := "idle"
		didWork := false
		if strings.HasPrefix(candidate.Path, scope.prefix) && candidate.Guard > 0 &&
			(candidate.Type == "image/jpeg" || candidate.Type == "image/png") {
			switch {
			case candidate.RetryAt > now.Unix():
				result = "retry_backoff"
			case candidate.MTimeNS > now.Add(-quietFileAge).UnixNano():
				result = "file_stabilizing"
			default:
				result, didWork = w.process(ctx, number, selected, scope, id, candidate), true
			}
		}
		switch result {
		case "foreground_busy", "scope_offline", "index_not_ready", "cancelled", "index_write_failed",
			"cpu_throttled", "low_cache_space", "pressure_metrics_unavailable":
			return result
		}
		if selected == historyLane {
			scope.cursor = candidate.Path
		}
		if manual != nil {
			manual.cursor = candidate.Path
		}
		if didWork || candidate.Path == page[len(page)-1].Path {
			return result
		}
	}
	return "idle"
}

func (w *Worker) process(ctx context.Context, number int, selected lane, scope *scope, id scopeID, c indexsql.PreviewWarmupCandidate) string {
	relative := strings.TrimPrefix(c.Path, scope.prefix)
	full := filepath.Join(scope.root, filepath.FromSlash(relative))
	g := guardedFile{scope: scope, relative: relative, version: c.Version, claimedIdentity: id}
	var err error
	if relative == "" || !filepath.IsLocal(relative) || filepath.ToSlash(filepath.Clean(relative)) != relative ||
		iteminfo.PathWithinRoot(scope.root, full) != nil {
		// A vanished file is checked again against the mount before it can
		// be recorded as an obsolete version.
		if _, scopeErr := scope.online(); scopeErr != nil {
			err = scopeErr
		} else {
			err = preview.ErrPreviewSourceChanged
		}
	} else {
		w.mu.Lock()
		w.status.Active = true
		w.mu.Unlock()
		err = w.svc.WarmPreviewWhenIdle(ctx, iteminfo.ExtendedFileInfo{
			FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{
				Name: filepath.Base(full), Type: c.Type, HasPreview: true, ModTime: time.Unix(0, c.MTimeNS),
			}},
			Source: scope.config.Source, RealPath: full,
		}, g.open, g.stat, func(estimate int64) error {
			if err := w.probe.poll(); err != nil {
				return err
			}
			return w.probe.check(estimate)
		})
		w.mu.Lock()
		w.status.Active = false
		w.mu.Unlock()
	}
	if ctx.Err() != nil {
		return "cancelled"
	}

	outcome, attempts, retryAt := "generated", int64(-1), int64(0)
	switch {
	case err == nil:
	case errors.Is(err, preview.ErrWarmupCached):
		outcome = "cached"
	case errors.Is(err, preview.ErrWarmupOriginal):
		outcome = "original"
	case errors.Is(err, preview.ErrPreviewSourceChanged):
		outcome = "version_changed"
	case errors.Is(err, preview.ErrPreviewBusy), errors.Is(err, preview.ErrPreviewSaturated):
		w.record(number, "foreground_busy", 0)
		return "foreground_busy"
	case errors.Is(err, errScopeOffline):
		w.record(number, "scope_offline", 0)
		return "scope_offline"
	case errors.Is(err, errIndexNotReady):
		w.record(number, "index_not_ready", 0)
		return "index_not_ready"
	case errors.Is(err, errCPUThrottled), errors.Is(err, errLowSpace), errors.Is(err, errPressureUnavailable):
		return err.Error()
	case errors.Is(err, errMemoryPressure):
		outcome, attempts, retryAt = errMemoryPressure.Error(), c.Attempts, time.Now().Add(30*time.Second).Unix()
	case errors.Is(err, preview.ErrWarmupUnsupported), errors.Is(err, preview.ErrPreviewOverBudget),
		errors.Is(err, preview.ErrPreviewDimensionsTooLarge):
		outcome = "terminal"
	default:
		if c.Attempts >= 2 {
			outcome = "terminal"
		} else {
			outcome = "retry"
			attempts = c.Attempts + 1
			retryAt = time.Now().Add(time.Duration(attempts*attempts) * 15 * time.Second).Unix()
		}
	}
	// A manual pass may check completed versions. Avoid rewinding a failed
	// version's automatic retry state while doing so.
	if selected != manualLane || attempts == -1 {
		if err := w.db.MarkPreviewWarmup(ctx, scope.config.Source, c, attempts, retryAt); err != nil {
			return "index_write_failed"
		}
	}
	w.mu.Lock()
	if selected == historyLane {
		w.status.History++
	} else if selected != manualLane {
		w.status.Recent++
	}
	w.mu.Unlock()
	w.record(number, outcome, 0)
	return outcome
}

func (w *Worker) record(number int, reason string, discovered int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status.Scopes[number].Reason = reason
	w.status.Discovered += uint64(discovered)
	switch reason {
	case "generated":
		w.status.Generated++
	case "cached":
		w.status.Cached++
	case "original":
		w.status.Original++
	case "version_changed":
		w.status.Changed++
	case "retry":
		w.status.Retried++
	case "terminal":
		w.status.Terminal++
	case "foreground_busy", "scope_offline", "index_not_ready", "memory_pressure":
		w.status.Deferred++
	default:
		return
	}
	w.status.Scopes[number].Checked++
}
