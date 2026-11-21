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
func (s *Service) ReuseContent(ctx context.Context, file iteminfo.ExtendedFileInfo, open func(context.Context) (*os.File, error), stat func(context.Context) (os.FileInfo, error), pace func(context.Context, int) error, preferred ...string) (ContentOutcome, error) {
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
	candidates, err = s.ledger.ContentMatches(ctx, proof, preferred...)
	if err != nil {
		return out, err
	}
	// Serialize the short binding window with D admission. Foreground requests
	// remain free to start; their location planning and active count win below.
	if !s.warmupRequest.CompareAndSwap(false, true) {
		return out, ErrPreviewBusy
	}
	defer s.warmupRequest.Store(false)
	// Reserve half the existing integrity budget for the final gated witness
	// check; ranking must not consume the bytes needed to attach its winner.
	selectionBytes := int64(16 << 20)
	type choice struct {
		proof   cacheledger.ContentProof
		results []cacheledger.ContentResult
		witness cacheledger.ContentResult
		score   [4]int
	}
	var choices []choice
	// Validate coverage, not just the first usable small result. Each candidate's
	// physical gates are released before another candidate's gates are acquired.
	for _, candidate := range candidates {
		out.CandidateChecks++
		results, e := s.ledger.ContentResults(ctx, candidate.Asset)
		if e != nil {
			return out, e
		}
		upper := [4]int{0, 0, 0, len(results)}
		if len(preferred) > 0 && candidate.Asset == preferred[0] {
			upper[0] = 1
		}
		for _, p := range results {
			if p.Variant == ledgerVariant(file, "xlarge") {
				upper[1] = 1
			}
			if p.Variant == ledgerVariant(file, "large") {
				upper[2] = 1
			}
		}
		var best [4]int
		for _, previous := range choices {
			if contentCoverageGreater(previous.score, best) {
				best = previous.score
			}
		}
		if len(choices) > 0 && !contentCoverageGreater(upper, best) {
			continue
		}
		unlock, e := s.lockContentKeys(ctx, results)
		if e != nil {
			return out, e
		}
		c := choice{proof: candidate, results: results}
		for _, p := range results {
			valid, e := s.validateContentResult(ctx, p, &selectionBytes, &out)
			if e != nil {
				unlock()
				return out, e
			}
			if !valid {
				continue
			}
			c.score[3]++
			if len(preferred) > 0 && preferred[0] == candidate.Asset {
				c.score[0] = 1
			}
			if p.Variant == ledgerVariant(file, "xlarge") {
				c.score[1] = 1
				c.witness = p
			} else if c.witness.Key == "" {
				c.witness = p
			}
			if p.Variant == ledgerVariant(file, "large") {
				c.score[2] = 1
			}
		}
		unlock()
		if c.witness.Key != "" {
			choices = append(choices, c)
			if c.score[0] == 1 {
				break
			}
		}
	}
	sort.SliceStable(choices, func(i, j int) bool {
		for k := range choices[i].score {
			if choices[i].score[k] != choices[j].score[k] {
				return choices[i].score[k] > choices[j].score[k]
			}
		}
		return choices[i].proof.Asset < choices[j].proof.Asset
	})
	remainingPreviewBytes := int64(32<<20) - int64(out.PreviewBytes)
	for _, c := range choices {
		unlock, e := s.lockContentKeys(ctx, c.results)
		if e != nil {
			return out, e
		}
		// Revalidate the chosen witness after reacquiring its gates. E may have
		// reclaimed it while other candidates were ranked. Both reads share 32 MiB.
		valid, e := s.validateContentResult(ctx, c.witness, &remainingPreviewBytes, &out)
		if e != nil {
			unlock()
			return out, e
		}
		if !valid {
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
			e = s.ledger.BindContent(ctx, plan.ref, c.proof, c.witness)
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

// HintCopiedContent observes a successful copy and tries a bounded memory queue.
// It performs no SQL, waits for no receiver, and starts no goroutine. Losing a
// hint (including its preferred asset) falls back to ordinary index discovery.
func HintCopiedContent(source, path string, exdev bool, preferred ...string) {
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
	h := cacheledger.ContentHint{Source: source, Path: path, Version: cacheledger.FromFileInfo(info), EXDEV: exdev}
	if !h.Version.Strong {
		return
	}
	if len(preferred) > 0 {
		h.PreferredAsset = preferred[0]
	}
	select {
	case service.contentHints <- h:
	default:
	}
}

// FlushContentHints is called only by the independent F worker. At most sixteen
// hints share one transaction and one deadline; contention drops this batch.
// Shutdown never needs to drain disposable hints before closing the ledger.
func (s *Service) FlushContentHints(ctx context.Context) (int, error) {
	var batch []cacheledger.ContentHint
	for len(batch) < 16 {
		select {
		case h := <-s.contentHints:
			batch = append(batch, h)
		default:
			goto persist
		}
	}
persist:
	if len(batch) == 0 {
		return 0, nil
	}
	bounded, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	return len(batch), s.ledger.HintContentBatch(bounded, batch)
}

func (s *Service) validateContentResult(ctx context.Context, p cacheledger.ContentResult, remaining *int64, out *ContentOutcome) (bool, error) {
	if p.Size <= 0 || p.Size > *remaining {
		return false, nil
	}
	pending, err := s.ledger.ContentPending(ctx, p.Key)
	if err != nil {
		return false, err
	}
	if pending {
		out.ReclamationDeferrals++
		return false, nil
	}
	*remaining -= p.Size
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
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err == nil && found && p.Matches(data) {
		out.PhysicalHits++
		return true, nil
	}
	return false, nil
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

// Lexicographic: known original, current xlarge, current large, variant count.
func contentCoverageGreater(a, b [4]int) bool {
	for k := range a {
		if a[k] != b[k] {
			return a[k] > b[k]
		}
	}
	return false
}
