package preview

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

// ContentLedger is owned by the preview service and must be drained before Stop.
func (s *Service) ContentLedger() *cacheledger.Repository { return s.ledger }

type ContentOutcome struct {
	Reason                                                                                      string
	Bytes                                                                                       int64
	CandidateRows, CandidateChecks, ProofHits, ReclamationDeferrals, PhysicalHits, PreviewBytes uint64
	PublicationWon                                                                              bool
}

// ReuseContent is background-only. It never decodes or records foreground use.
// The caller supplies D's descriptor guards plus durable scope revalidation.
func (s *Service) ReuseContent(ctx context.Context, file iteminfo.ExtendedFileInfo, open func(context.Context) (*os.File, error), stat func(context.Context) (os.FileInfo, error), pace func(context.Context, int) error) (ContentOutcome, error) {
	out := ContentOutcome{Reason: "no_old_proof"}
	if s.ledger == nil {
		return out, errors.New("ledger_unavailable")
	}
	guarded := &warmupInput{open: open, stat: stat}
	plan, err := s.planLedgerPreviewWithStat(ctx, file, "small", guarded, false)
	if err != nil {
		return out, err
	}
	if plan == nil {
		return out, nil
	}
	if plan.version.Size <= 0 || plan.version.Size > 64<<20 {
		out.Reason = "over_limit"
		return out, nil
	}
	candidates, err := s.ledger.ContentCandidates(ctx, plan.version.Size)
	if err != nil {
		return out, err
	}
	out.CandidateRows = uint64(len(candidates))
	own, err := s.ledger.ContentResults(ctx, plan.ref.Asset)
	if err != nil {
		return out, err
	}
	hadOwn := len(own) > 0
	if len(own) == 0 && len(candidates) == 0 {
		return out, nil
	}
	if len(own) > 0 {
		yes, err := s.ledger.ContentProof(ctx, plan.ref)
		if err != nil {
			return out, err
		}
		if yes {
			out.Reason = "already_proven"
			out.ProofHits = 1
			return out, s.ledger.NoteContentOrigin(ctx, plan.ref, file.Source, plan.path)
		}
	}
	// Only useful versions get a C token. A proof is never assigned to a token
	// captured from a different source/version.
	plan, err = s.planLedgerPreviewWithStat(ctx, file, "small", guarded, true)
	if err != nil {
		return out, err
	}
	proof, known, err := s.ledger.KnownContent(ctx, plan.ref)
	if err != nil {
		return out, err
	}
	if known {
		out.ProofHits = 1
	}
	if !known {
		f, e := open(ctx)
		if e != nil {
			return out, e
		}
		defer f.Close()
		var bytes int64
		proof, bytes, err = cacheledger.HashContent(ctx, f, plan.version, 64<<20, pace)
		out.Bytes = bytes
		if err != nil {
			return out, err
		}
	}
	check := func() error {
		info, err := stat(ctx)
		if err != nil {
			return err
		}
		if cacheledger.FromFileInfo(info) != plan.version {
			return cacheledger.ErrStale
		}
		return nil
	}
	if err = check(); err != nil {
		return out, err
	}
	// Persist only this current token's evidence, even when there is no match.
	// A later pass can compare new candidates without re-reading the original.
	release, err := s.ledger.LockLocations(ctx)
	if err != nil {
		return out, err
	}
	err = check()
	if err == nil {
		proof.Asset = plan.ref.Asset
		err = s.ledger.ProveContent(ctx, plan.ref, proof)
		if err == nil {
			err = s.ledger.NoteContentOrigin(ctx, plan.ref, file.Source, plan.path)
		}
	}
	release()
	if err != nil {
		return out, err
	}
	// Recheck actual foreground results after hashing; a winning publication
	// retains its asset and may now acquire its own exact source proof.
	own, err = s.ledger.ContentResults(ctx, plan.ref.Asset)
	if err != nil {
		return out, err
	}
	if len(own) > 0 {
		unlock, err := s.ledger.LockLocations(ctx)
		if err != nil {
			return out, err
		}
		defer unlock()
		if err = check(); err != nil {
			return out, err
		}
		proof.Asset = plan.ref.Asset
		err = s.ledger.ProveContent(ctx, plan.ref, proof)
		out.Reason = "proven_own"
		out.PublicationWon = !hadOwn
		return out, err
	}
	// Query exact digest after the one target read; unrelated same-size files
	// cannot hide a match behind the cheap candidate limit.
	candidates, err = s.ledger.ContentMatches(ctx, proof)
	if err != nil {
		return out, err
	}
	// Serialize the short binding window with D admission. Foreground requests
	// remain free to start; their location planning and active count win below.
	if !s.warmupRequest.CompareAndSwap(false, true) {
		return out, ErrPreviewBusy
	}
	defer s.warmupRequest.Store(false)
	remainingPreviewBytes := int64(32 << 20)
	for _, candidate := range candidates {
		out.CandidateChecks++
		results, err := s.ledger.ContentResults(ctx, candidate.Asset)
		if err != nil {
			return out, err
		}
		unlock, err := s.lockContentKeys(ctx, results)
		if err != nil {
			return out, err
		}
		var witness *cacheledger.ContentResult
		for i := range results {
			p := &results[i]
			if p.Size <= 0 || p.Size > remainingPreviewBytes {
				continue
			}
			// Isolated/held publications are protected; F never reads their paths.
			pending, e := s.ledger.ContentPending(ctx, p.Key)
			if e != nil {
				unlock()
				return out, e
			}
			if pending {
				out.ReclamationDeferrals++
				continue
			}
			remainingPreviewBytes -= p.Size
			out.PreviewBytes += uint64(p.Size)
			var data []byte
			var found bool
			if sized, ok := s.fileCache.(interface {
				LoadSized(context.Context, string, int64) ([]byte, bool, error)
			}); ok {
				data, found, err = sized.LoadSized(ctx, p.Key, p.Size)
			} else {
				data, found, err = s.fileCache.Load(ctx, p.Key)
			}
			if err == nil && found && p.Matches(data) {
				witness = p
				out.PhysicalHits++
				break
			}
		}
		if witness == nil {
			unlock()
			continue
		}
		release, e := s.ledger.LockLocations(ctx)
		if e != nil {
			unlock()
			return out, e
		}
		e = check()
		s.flights.mu.Lock()
		busy := s.interactiveRequests.Load() != 0 || len(s.flights.calls) != 0
		s.flights.mu.Unlock()
		if busy {
			e = ErrPreviewBusy
		}
		if e == nil {
			e = s.ledger.BindContent(ctx, plan.ref, candidate, *witness)
		}
		release()
		unlock()
		if e == nil {
			out.Reason = "reused"
			return out, nil
		}
		if !errors.Is(e, cacheledger.ErrStale) {
			return out, e
		}
	}
	out.Reason = "candidate_mismatch"
	return out, nil
}

func (s *Service) lockContentKeys(ctx context.Context, results []cacheledger.ContentResult) (func(), error) {
	if s.orphans == nil {
		return func() {}, nil
	}
	stripes := make(map[int]bool)
	for _, r := range results {
		stripes[keyStripe(r.Key)] = true
	}
	order := make([]int, 0, len(stripes))
	for stripe := range stripes {
		order = append(order, stripe)
	}
	sort.Ints(order)
	acquired := []int{}
	release := func() {
		for i := len(acquired) - 1; i >= 0; i-- {
			<-s.orphans.keys[acquired[i]]
		}
	}
	for _, stripe := range order {
		select {
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		case s.orphans.keys[stripe] <- struct{}{}:
			acquired = append(acquired, stripe)
		}
	}
	return release, nil
}

// HintCopiedContent performs only bounded observation/SQL after a successful
// copy. A lost hint is recovered by index pagination; source deletion is owned
// exclusively by existing MoveByCopy semantics.
func HintCopiedContent(source, path string, exdev bool) {
	if service == nil || service.closed.Load() || service.ledger == nil {
		return
	}
	cfg := settings.Config.Server.PreviewReuse
	if !cfg.Enabled {
		return
	}
	allowed := false
	for _, scope := range cfg.Scopes {
		if scope.Source != source {
			continue
		}
		src := settings.Config.Server.NameToSource[source]
		if src == nil {
			continue
		}
		rel, err := filepath.Rel(src.Path, path)
		if err == nil && filepath.IsLocal(rel) && settings.WarmupContains(scope.Directory, rel) {
			allowed = true
			break
		}
	}
	if !allowed {
		return
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = service.ledger.HintContent(ctx, source, path, cacheledger.FromFileInfo(info), exdev)
}

func (s *Service) ContentCacheReady(ctx context.Context) error {
	if s.orphans == nil || s.orphans.tree == nil || !s.orphans.tree.HasDurableRoot() {
		return errors.New("cache_identity_unavailable")
	}
	id, err := s.orphans.tree.RecordedRoot()
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(id)
	return s.ledger.BindContentScope(ctx, "cache-root", string(raw))
}
