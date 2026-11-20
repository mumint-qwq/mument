package previewwarmup

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	indexsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/gtsteffaniak/go-logger/logger"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ReuseWorker struct {
	paused atomic.Bool
	db     *indexsql.IndexDB
	svc    *preview.Service
	repo   *cacheledger.Repository
	scopes []scope
	probe  resourceProbe
	done   chan struct{}
	mu     sync.Mutex
	status ReuseStatus
}
type ReuseStatus struct {
	Paused    bool              `json:"paused"`
	Enabled   bool              `json:"enabled"`
	Reason    string            `json:"reason"`
	ReadBytes int64             `json:"readBytes"`
	Counts    map[string]uint64 `json:"counts"`
}

func NewReuse(ctx context.Context, server settings.Server, db *indexsql.IndexDB, svc *preview.Service) *ReuseWorker {
	w := &ReuseWorker{db: db, svc: svc, done: make(chan struct{}), status: ReuseStatus{Reason: "disabled", Counts: map[string]uint64{}}}
	if !server.PreviewReuse.Enabled {
		close(w.done)
		return w
	}
	w.status.Enabled = true
	if !platformSupported || db == nil || svc == nil || svc.ContentLedger() == nil {
		w.status.Reason = "dependencies_unavailable"
		close(w.done)
		return w
	}
	w.repo = svc.ContentLedger()
	if err := svc.ContentCacheReady(ctx); err != nil {
		w.status.Reason = "cache_identity_unavailable"
		close(w.done)
		return w
	}
	for _, cfg := range server.PreviewReuse.Scopes {
		source := server.NameToSource[cfg.Source]
		if source == nil {
			continue
		}
		w.scopes = append(w.scopes, scope{config: cfg, sourceRoot: source.Path, root: filepath.Join(source.Path, cfg.Directory), prefix: indexPrefix(cfg.Directory)})
	}
	if len(w.scopes) == 0 {
		w.status.Reason = "no_scopes"
		close(w.done)
		return w
	}
	svc.EnableWarmupHints() // Directory activity must preempt F even when D is off.
	w.probe = newResourceProbe(svc.WarmupCacheRoot())
	w.status.Reason = "starting"
	go w.run(ctx)
	return w
}
func (w *ReuseWorker) Wait(ctx context.Context) error {
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (w *ReuseWorker) Snapshot() ReuseStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.status
	s.Paused = w.paused.Load()
	s.Counts = map[string]uint64{}
	for k, v := range w.status.Counts {
		s.Counts[k] = v
	}
	return s
}
func (w *ReuseWorker) record(reason string, bytes int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status.Reason = reason
	w.status.ReadBytes += bytes
	w.status.Counts[reason]++
}
func (w *ReuseWorker) allow(ctx context.Context) error {
	if w.paused.Load() {
		return preview.ErrPreviewBusy
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !w.svc.IdleForWarmup() {
		return preview.ErrPreviewBusy
	}
	if err := w.svc.ContentCacheReady(ctx); err != nil {
		return err
	}
	if err := w.svc.WarmupSpace(); err != nil {
		return err
	}
	return w.probe.poll()
}
func (w *ReuseWorker) run(ctx context.Context) {
	defer close(w.done)
	tick := time.NewTicker(claimInterval)
	defer tick.Stop()
	report := time.NewTicker(time.Minute)
	defer report.Stop()
	turn := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-report.C:
			s := w.Snapshot()
			logger.Infof("preview reuse reason=%s bytes=%d counts=%v", s.Reason, s.ReadBytes, s.Counts)
			_ = w.repo.PruneContentWork(ctx)
		case <-tick.C:
			if err := w.allow(ctx); err != nil {
				w.record("resource_deferred", 0)
				continue
			}
			if turn%2 == 0 {
				w.hints(ctx)
			}
			number := (turn / 2) % len(w.scopes)
			turn++
			s := &w.scopes[number]
			page, err := w.db.PreviewWarmupPage(ctx, s.config.Source, s.prefix, s.cursor, true, false)
			if err != nil {
				w.record("index_query_failed", 0)
				continue
			}
			if len(page) == 0 {
				s.cursor = ""
				continue
			}
			for _, c := range page {
				if ctx.Err() != nil {
					return
				}
				if c.Type == "image/jpeg" || c.Type == "image/png" {
					if w.processReuse(ctx, s, c) {
						s.cursor = c.Path
						break
					}
				}
				s.cursor = c.Path
			}
		}
	}
}
func (w *ReuseWorker) scopeKey(s *scope) string {
	data, _ := json.Marshal([]string{s.config.Source, s.sourceRoot, s.config.Directory, s.config.MountPoint})
	return cacheledger.Digest(data)
}
func (w *ReuseWorker) processReuse(ctx context.Context, s *scope, c indexsql.PreviewWarmupCandidate) bool {
	if c.Guard <= 0 || c.MTimeNS > time.Now().Add(-quietFileAge).UnixNano() {
		return false
	}
	id, err := s.online()
	if err != nil {
		w.record(err.Error(), 0)
		return true
	}
	rel := strings.TrimPrefix(c.Path, s.prefix)
	if !strings.HasPrefix(c.Path, s.prefix) || !filepath.IsLocal(rel) || filepath.ToSlash(filepath.Clean(rel)) != rel {
		w.record("out_of_scope", 0)
		return false
	}
	full := filepath.Join(s.root, rel)
	key := w.scopeKey(s)
	source, err := diskcache.ObserveSource(s.root, full)
	if err != nil {
		w.record("source_unavailable", 0)
		return false
	}
	proof := source.Proof
	source.Close()
	if err = proof.Check(); err != nil {
		w.record("source_identity_unavailable", 0)
		return true
	}
	raw, _ := json.Marshal(proof.RootID)
	if err = w.repo.BindContentScope(ctx, key, string(raw)); err != nil {
		w.record("scope_identity_changed", 0)
		return true
	}
	claim, err := w.repo.ContentWork(ctx, key, c.Path, c.Version)
	if err != nil {
		w.record("work_sql_failed", 0)
		return true
	}
	if !claim {
		return false
	}
	g := guardedFile{scope: s, relative: rel, version: c.Version, claimedIdentity: id}
	stat := func(ctx context.Context) (os.FileInfo, error) {
		if err := proof.Check(); err != nil {
			return nil, err
		}
		return g.stat(ctx)
	}
	pace := func(ctx context.Context, n int) error {
		if err := w.allow(ctx); err != nil {
			return err
		}
		if _, err := s.online(); err != nil {
			return err
		}
		if err := proof.Check(); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		timer := time.NewTimer(time.Duration(n) * time.Second / (8 << 20))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	out, err := w.svc.ReuseContent(ctx, iteminfo.ExtendedFileInfo{Source: s.config.Source, RealPath: full, FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: filepath.Base(full), Type: c.Type, HasPreview: true}}}, g.open, stat, pace)
	if err != nil {
		switch {
		case ctx.Err() != nil, errors.Is(err, preview.ErrPreviewBusy), errors.Is(err, errScopeOffline), errors.Is(err, errIndexNotReady), errors.Is(err, errCPUThrottled), errors.Is(err, errMemoryPressure), errors.Is(err, errPressureUnavailable), errors.Is(err, errLowSpace), errors.Is(err, diskcache.ErrReclaimScope), preview.IsSpaceDeferred(err):
			_ = w.repo.DeferContentWork(ctx, key, c.Path, c.Version)
			w.record("resource_deferred", out.Bytes)
		default:
			_ = w.repo.FinishContentWork(ctx, key, c.Path, c.Version, false)
			w.record("retry", out.Bytes)
		}
		return true
	}
	switch out.Reason {
	case "no_old_proof", "candidate_mismatch":
		_ = w.repo.DeferContentWork(ctx, key, c.Path, c.Version)
	default:
		_ = w.repo.FinishContentWork(ctx, key, c.Path, c.Version, true)
	}
	w.record(out.Reason, out.Bytes)
	return out.Bytes > 0 || out.Reason == "reused"
}
func (w *ReuseWorker) hints(ctx context.Context) {
	hints, err := w.repo.ContentHints(ctx)
	if err != nil {
		return
	}
	for _, h := range hints {
		for i := range w.scopes {
			s := &w.scopes[i]
			if s.config.Source != h.Source {
				continue
			}
			rel, err := filepath.Rel(s.sourceRoot, h.Path)
			if err != nil || !filepath.IsLocal(rel) || !settings.WarmupContains(s.config.Directory, rel) {
				continue
			}
			c, err := w.db.PreviewContentExact(ctx, h.Source, "/"+filepath.ToSlash(rel))
			if err != nil {
				continue
			}
			info, err := os.Lstat(h.Path)
			if err == nil && cacheledger.FromFileInfo(info) == h.Version {
				w.processReuse(ctx, s, c)
			}
			_ = w.repo.DropContentHint(ctx, h)
			return
		}
	}
}

func (w *ReuseWorker) Control(action string) error {
	if !w.status.Enabled || w.repo == nil || len(w.scopes) == 0 {
		return ErrInactive
	}
	switch action {
	case "pause":
		w.paused.Store(true)
	case "resume":
		w.paused.Store(false)
	default:
		return ErrInvalidAction
	}
	return nil
}
