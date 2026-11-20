package preview

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
)

const orphanGrace = 7 * 24 * time.Hour
const orphanWindow = 2 * time.Second
const orphanByteLimit = 64 << 20
const orphanMaxFile = 32 << 20

type orphanManager struct {
	tree     *diskcache.ReclaimTree
	repo     *cacheledger.Repository
	enabled  bool
	grace    time.Duration
	keys     [64]chan struct{}
	done     chan struct{}
	now      func() time.Time
	last     atomic.Pointer[OrphanExecution]
	window   sync.Mutex
	cursor   OrphanCursor
	onUnlink func()
	// Tests inject a crash after a durable phase without cleanup.
	checkpoint func(string) error
}

func newOrphanManager(tree *diskcache.ReclaimTree, repo *cacheledger.Repository, enabled bool, graceHours int) *orphanManager {
	grace := orphanGrace
	if graceHours > 0 {
		grace = time.Duration(graceHours) * time.Hour
	}
	o := &orphanManager{tree: tree, repo: repo, enabled: enabled, grace: grace, done: make(chan struct{}), now: time.Now}
	for i := range o.keys {
		o.keys[i] = make(chan struct{}, 1)
	}
	return o
}

func (o *orphanManager) lockKey(ctx context.Context, key string) (func(), error) {
	if o == nil {
		return func() {}, nil
	}
	gate := o.keys[keyStripe(key)]
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	}
}

func keyStripe(key string) int {
	hash := sha256.Sum256([]byte(diskcache.RelativePath(key)))
	return int(hash[0]) % 64
}

func (o *orphanManager) publicationStamp(key string) string {
	if o == nil || o.tree.Check(false) != nil {
		return ""
	}
	object, err := o.tree.OpenKey(key)
	if err != nil {
		return ""
	}
	defer object.Close()
	stamp, err := object.Stamp()
	if err != nil {
		return ""
	}
	return stampJSON(stamp)
}

func stampJSON(stamp diskcache.ObjectStamp) string {
	raw, _ := json.Marshal(stamp)
	return string(raw)
}

func (o *orphanManager) run(ctx context.Context) {
	defer close(o.done)
	if !o.enabled {
		return
	}
	timer := time.NewTicker(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			o.executeWindow(ctx)
		}
	}
}

// executeWindow shares the applied cursor with pressure-triggered E2 work.
func (o *orphanManager) executeWindow(ctx context.Context) bool {
	if !o.enabled || !o.window.TryLock() {
		return false
	}
	defer o.window.Unlock()
	r := o.report(ctx, o.cursor, true)
	o.last.Store(&OrphanExecution{
		At: o.now().UTC(), Unlinked: r.Unlinked, Logical: r.UnlinkedBytes, Blocks: r.UnlinkedBlocks,
		Reasons: r.Reasons, Samples: r.Samples,
	})
	o.cursor = OrphanCursor{Candidate: r.NextCandidate, Intent: r.NextIntent, Object: r.NextObject}
	return true
}

type reclaimPolicy struct {
	qualify  func(context.Context, string, []cacheledger.OrphanPublication, time.Time, *OrphanSample) string
	intent   func(context.Context, string, string) (cacheledger.OrphanIntent, error)
	reserve  func(context.Context, cacheledger.OrphanIntent) (cacheledger.OrphanIntent, error)
	phase    func(context.Context, string, string, string) error
	settle   func(context.Context, cacheledger.OrphanIntent, []cacheledger.OrphanPublication, string, int64, int64, time.Time) error
	restore  func(string) bool
	restored func(context.Context, cacheledger.OrphanIntent, []cacheledger.OrphanPublication, string, time.Time) error
	eviction bool
}

func (o *orphanManager) orphanPolicy() reclaimPolicy {
	return reclaimPolicy{
		qualify: func(_ context.Context, _ string, pubs []cacheledger.OrphanPublication, now time.Time, sample *OrphanSample) string {
			return qualifyOrphan(pubs, now, sample)
		},
		intent: o.repo.OrphanIntent, reserve: o.repo.ReserveOrphan, phase: o.repo.OrphanPhase, settle: o.repo.SettleOrphan,
		restore: func(reason string) bool { return reason == "reference_present" },
	}
}

type OrphanReport struct {
	Enabled         bool                           `json:"enabled"`
	Tracking        bool                           `json:"tracking"`
	NextCandidate   int64                          `json:"next_candidate"`
	NextIntent      int64                          `json:"next_intent"`
	NextObject      string                         `json:"next_object,omitempty"`
	ConfirmedAssets int                            `json:"confirmed_assets_in_window"`
	Due             int                            `json:"due_objects"`
	Waiting         int                            `json:"waiting_objects"`
	EstimatedBytes  int64                          `json:"due_logical_bytes"`
	EstimatedBlocks int64                          `json:"due_allocated_bytes"`
	Unlinked        int                            `json:"unlinked_this_window"`
	UnlinkedBytes   int64                          `json:"unlinked_logical_bytes"`
	UnlinkedBlocks  int64                          `json:"unlinked_allocated_bytes"`
	Reasons         map[string]int                 `json:"reasons"`
	Samples         []OrphanSample                 `json:"samples,omitempty"`
	RecentEvents    []cacheledger.OrphanEventState `json:"recent_events,omitempty"`
	RecentRemoval   []cacheledger.OrphanAudit      `json:"recent_removal,omitempty"`
	LastExecution   *OrphanExecution               `json:"last_execution,omitempty"`
}

type OrphanExecution struct {
	At       time.Time      `json:"at"`
	Unlinked int            `json:"unlinked"`
	Logical  int64          `json:"logical_bytes"`
	Blocks   int64          `json:"allocated_bytes"`
	Reasons  map[string]int `json:"reasons"`
	Samples  []OrphanSample `json:"samples,omitempty"`
}

type OrphanSample struct {
	ID          string     `json:"id"`
	Reason      string     `json:"reason"`
	Evidence    string     `json:"evidence,omitempty"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	EligibleAt  *time.Time `json:"eligible_at,omitempty"`
	Logical     int64      `json:"logical_bytes"`
	Blocks      int64      `json:"allocated_bytes"`
}

type OrphanCursor struct {
	Candidate, Intent int64
	Object            string
}

func PreviewOrphanStatus(ctx context.Context, cursor OrphanCursor) OrphanReport {
	if s := service; s != nil && !s.closed.Load() && s.orphans != nil {
		r := s.orphans.report(ctx, cursor, false)
		r.LastExecution = s.orphans.last.Load()
		return r
	}
	return OrphanReport{Reasons: map[string]int{"tracking_unavailable": 1}}
}

func (r *OrphanReport) record(sample OrphanSample) {
	r.Reasons[sample.Reason]++
	if len(r.Samples) < cacheledger.OrphanPageSize {
		r.Samples = append(r.Samples, sample)
	}
}

func (o *orphanManager) report(parent context.Context, cursor OrphanCursor, apply bool) OrphanReport {
	r := OrphanReport{Enabled: o.enabled, Tracking: true, Reasons: make(map[string]int)}
	ctx, cancel := context.WithTimeout(parent, orphanWindow)
	defer cancel()
	now := o.now()
	assetsAfter, intentsAfter := cursor.Candidate, cursor.Intent
	apply = apply && o.enabled
	if err := o.tree.Check(apply); err != nil {
		r.NextCandidate, r.NextIntent, r.NextObject = assetsAfter, intentsAfter, cursor.Object
		r.Reasons[diskcache.ReclaimReason(err)]++
		return r
	}
	if !o.tree.HasDurableRoot() {
		r.NextCandidate, r.NextIntent, r.NextObject = assetsAfter, intentsAfter, cursor.Object
		r.Reasons["durable_identity_unavailable"]++
		return r
	}
	if apply {
		if err := o.repo.AdvanceOrphanClock(ctx, now); err != nil {
			r.NextCandidate, r.NextIntent, r.NextObject = assetsAfter, intentsAfter, cursor.Object
			r.Reasons["clock_or_ledger"]++
			return r
		}
	}
	policy := o.orphanPolicy()
	var budget int64 = orphanByteLimit
	visited := make(map[string]bool)
	intents, err := o.repo.OrphanIntents(ctx, intentsAfter)
	if err != nil {
		r.Reasons["ledger_error"]++
		return r
	}
	for _, intent := range intents {
		if ctx.Err() != nil || budget < orphanMaxFile {
			r.NextIntent = intentsAfter
			break
		}
		if o.inspect(ctx, policy, &r, intent.Key, intent, now, apply, &budget) {
			visited[intent.Key] = true
		}
		intentsAfter = intent.Row
	}
	if len(intents) == cacheledger.OrphanPageSize && r.NextIntent == 0 {
		r.NextIntent = intentsAfter
	}
	assets, err := o.repo.OrphanCandidates(ctx, assetsAfter)
	if err != nil {
		r.Reasons["ledger_error"]++
		return r
	}
	remaining := cacheledger.OrphanPageSize
	for _, candidate := range assets {
		if ctx.Err() != nil || remaining == 0 || budget < orphanMaxFile {
			r.NextCandidate, r.NextObject = assetsAfter, cursor.Object
			break
		}
		keys, err := o.repo.OrphanKeys(ctx, candidate.Asset)
		if err != nil {
			r.Reasons["ledger_error"]++
			r.NextCandidate, r.NextObject = assetsAfter, cursor.Object
			break
		}
		if len(keys) > cacheledger.OrphanPageSize {
			r.Reasons["variant_limit"]++
		} else {
			complete := true
			for i, key := range keys {
				if cacheledger.Digest([]byte(key)) == cursor.Object {
					keys = keys[i+1:]
					break
				}
			}
			for _, key := range keys {
				if ctx.Err() != nil || remaining == 0 || budget < orphanMaxFile {
					r.NextCandidate, r.NextObject = assetsAfter, cursor.Object
					complete = false
					break
				}
				if !visited[key] {
					o.inspect(ctx, policy, &r, key, cacheledger.OrphanIntent{}, now, apply, &budget)
					visited[key] = true
					remaining--
				}
				cursor.Object = cacheledger.Digest([]byte(key))
			}
			if !complete {
				break
			}
		}
		r.ConfirmedAssets++
		assetsAfter, cursor.Object = candidate.Row, ""
	}
	if len(assets) == cacheledger.OrphanPageSize && r.NextCandidate == 0 && r.NextObject == "" {
		r.NextCandidate = assetsAfter
	}
	if ctx.Err() != nil {
		r.Reasons["interrupted"]++
	}
	if !apply {
		r.RecentEvents, _ = o.repo.OrphanEvents(ctx)
		r.RecentRemoval, _ = o.repo.OrphanAudit(ctx)
	}
	return r
}

func (proof orphanSourceProof) checkOnline() error {
	idx := indexing.GetIndex(proof.Source)
	if idx == nil {
		return diskcache.ErrReclaimScope
	}
	root, err := filepath.Abs(idx.Path)
	if err != nil || root != proof.Root {
		return diskcache.ErrReclaimIdentity
	}
	return proof.Check()
}

func qualifyOrphan(pubs []cacheledger.OrphanPublication, now time.Time, sample *OrphanSample) string {
	if len(pubs) == 0 || len(pubs) > cacheledger.OrphanPageSize {
		return "publication_coverage"
	}
	first := pubs[0]
	var since, eligible int64
	for _, p := range pubs {
		switch {
		case p.Referenced:
			return "reference_present"
		case p.Moving:
			return "move_protected"
		case p.Unproven:
			return "unproven_ref_loss"
		case p.Since <= 0 || p.EligibleAt <= p.Since || p.Proof == "":
			return "unconfirmed_publication"
		case p.Epoch == "" || p.Stamp == "" || p.Key != "c-raster/"+p.Asset+"/"+p.Variant:
			return "untracked_publication"
		case p.Stamp != first.Stamp || p.Digest != first.Digest || p.Size != first.Size:
			return "shared_publication_mismatch"
		}
		var proof orphanSourceProof
		if json.Unmarshal([]byte(p.Proof), &proof) != nil {
			return "source_identity_unavailable"
		}
		if err := proof.checkOnline(); err != nil {
			if errors.Is(err, diskcache.ErrDurableIdentity) {
				return "source_durable_identity_unavailable"
			}
			return "source_identity_unavailable"
		}
		if p.Since > since {
			since = p.Since
			sample.Evidence = p.Reason
		}
		eligible = max(eligible, p.EligibleAt)
	}
	confirmedAt, eligibleAt := time.Unix(0, since).UTC(), time.Unix(0, eligible).UTC()
	sample.ConfirmedAt, sample.EligibleAt = &confirmedAt, &eligibleAt
	sample.Logical = first.Size
	if now.Before(confirmedAt) {
		return "clock_rollback"
	}
	if now.Before(eligibleAt) {
		return "waiting_grace"
	}
	if first.Size <= 0 || first.Size > orphanMaxFile {
		return "file_byte_limit"
	}
	return ""
}

func (o *orphanManager) inspect(ctx context.Context, policy reclaimPolicy, r *OrphanReport, key string, intent cacheledger.OrphanIntent, now time.Time, apply bool, budget *int64) (currentIntent bool) {
	sample := OrphanSample{ID: cacheledger.Digest([]byte(key))}
	defer func() { r.record(sample) }()
	if !strings.HasPrefix(key, "c-raster/") {
		sample.Reason = "untracked_publication"
		return
	}
	unlock, err := o.lockKey(ctx, key)
	if err != nil {
		sample.Reason = "interrupted"
		return
	}
	defer unlock()
	location, err := o.repo.LockLocations(ctx)
	if err != nil {
		sample.Reason = "interrupted"
		return
	}
	defer func() { location() }()
	withoutLocation := func(run func() error) error {
		location()
		location = func() {}
		if err := run(); err != nil {
			return err
		}
		again, err := o.repo.LockLocations(ctx)
		if err == nil {
			location = again
		}
		return err
	}
	pubs, err := o.repo.OrphanPublications(ctx, key)
	if err != nil {
		sample.Reason = "ledger_error"
		return
	}
	set := cacheledger.OrphanPublicationSet(pubs)
	if intent.ID == "" && set != "" {
		intent, err = policy.intent(ctx, key, set)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			sample.Reason = "ledger_error"
			return
		}
	}
	sample.Reason = policy.qualify(ctx, key, pubs, now, &sample)
	if intent.ID != "" {
		currentIntent = set != "" && intent.Publications == set
		if !apply {
			sample.Reason = "intent_" + intent.State
			return
		}
		sample.Reason = o.resume(ctx, policy, r, sample.Reason, pubs, intent, set, now, budget, false, withoutLocation)
		return
	}
	if sample.Reason == "waiting_grace" {
		r.Waiting++
	}
	if sample.Reason != "" {
		return
	}
	var expected diskcache.ObjectStamp
	if json.Unmarshal([]byte(pubs[0].Stamp), &expected) != nil || expected.Size != pubs[0].Size {
		sample.Reason = "untracked_publication"
		return
	}
	if !expected.HasDurableIdentity() {
		sample.Reason = "durable_identity_unavailable"
		return
	}
	object, err := o.tree.OpenKey(key)
	if err != nil {
		sample.Reason = diskcache.ReclaimReason(err)
		return
	}
	defer object.Close()
	*budget -= expected.Size
	// The location gate need not cover this bounded payload read. The key
	// gate protects the file while references and move intents can change.
	if err := withoutLocation(func() error { return object.Verify(ctx, expected, pubs[0].Digest) }); err != nil {
		sample.Reason = diskcache.ReclaimReason(err)
		return
	}
	rechecked, err := o.repo.OrphanPublications(ctx, key)
	if err != nil {
		sample.Reason = "ledger_error"
		return
	}
	if reason := policy.qualify(ctx, key, rechecked, now, &sample); reason != "" || cacheledger.OrphanPublicationSet(rechecked) != set {
		sample.Reason = "eligibility_changed"
		return
	}
	sample.Blocks = expected.Blocks
	r.Due++
	r.EstimatedBytes += expected.Size
	r.EstimatedBlocks += expected.Blocks
	sample.Reason = "due"
	if !apply {
		return
	}
	intent, err = policy.reserve(ctx, cacheledger.OrphanIntent{
		Key: key, Publications: set, Expected: stampJSON(expected), Logical: expected.Size, Blocks: expected.Blocks,
	})
	if err != nil {
		sample.Reason = "reservation_failed"
		return
	}
	if o.interrupted("reserved") {
		sample.Reason = "interrupted"
		return
	}
	sample.Reason = o.resume(ctx, policy, r, "", rechecked, intent, set, now, budget, true, withoutLocation)
	return
}

func (o *orphanManager) interrupted(phase string) bool {
	return o.checkpoint != nil && o.checkpoint(phase) != nil
}

func (o *orphanManager) resume(ctx context.Context, policy reclaimPolicy, r *OrphanReport, protect string, pubs []cacheledger.OrphanPublication,
	i cacheledger.OrphanIntent, set string, now time.Time, budget *int64, verified bool, withoutLocation func(func() error) error) string {
	if i.State == "held" {
		return "intent_held"
	}
	var old diskcache.ObjectStamp
	if json.Unmarshal([]byte(i.Expected), &old) != nil || old.Size <= 0 || old.Size > orphanMaxFile || old.Size != i.Logical {
		return "invalid_intent"
	}
	if !old.HasDurableIdentity() {
		return "durable_identity_unavailable"
	}
	if err := o.tree.CheckRecordedRoot(old.Root); err != nil {
		return diskcache.ReclaimReason(err)
	}
	if set != i.Publications || len(pubs) == 0 {
		if i.State == "reserved" && o.isolationAbsent(i.ID) {
			return policy.finish(ctx, i, nil, "superseded_reservation", 0, 0, now)
		}
		return policy.hold(ctx, i, "publication_changed")
	}
	restore := policy.restore(protect) || (policy.eviction && i.State == "restoring")
	restoreReason := "reference_restored"
	if policy.eviction {
		restoreReason = "capacity_restored"
	}
	if protect != "" && !restore && !(policy.eviction && i.State == "removed") {
		return protect
	}
	source, err := o.tree.OpenKey(i.Key)
	if err != nil {
		return diskcache.ReclaimReason(err)
	}
	defer source.Close()
	isolated, err := o.tree.OpenIsolated(i.ID, i.State == "reserved" && protect == "")
	if errors.Is(err, os.ErrNotExist) && restore && i.State == "reserved" {
		return policy.finish(ctx, i, nil, restoreReason, 0, 0, now)
	}
	if err != nil {
		return diskcache.ReclaimReason(err)
	}
	defer isolated.Close()

	quarantined, err := isolated.Stamp()
	absent := errors.Is(err, os.ErrNotExist)
	if err != nil && !absent {
		return diskcache.ReclaimReason(err)
	}
	if policy.eviction && absent && (i.State == "isolated" || i.State == "restoring") {
		current, err := source.Stamp()
		if err == nil {
			if !old.SameRenamed(current) {
				return policy.hold(ctx, i, "restoration_changed")
			}
			*budget -= current.Size
			if err := withoutLocation(func() error { return source.Verify(ctx, current, pubs[0].Digest) }); err != nil {
				return diskcache.ReclaimReason(err)
			}
			if err := policy.restored(ctx, i, pubs, stampJSON(current), now); err != nil {
				return "ledger_error"
			}
			return restoreReason
		}
		if !errors.Is(err, os.ErrNotExist) {
			return diskcache.ReclaimReason(err)
		}
	}
	if i.State == "removed" || ((i.State == "isolated" || i.State == "restoring") && absent) {
		if !absent {
			return policy.hold(ctx, i, "isolation_changed")
		}
		if restore && !policy.eviction {
			reason := "reference_restored_after_remove"
			if current, err := source.Stamp(); err == nil && old.SameRenamed(current) {
				reason = "reference_restored"
			}
			return policy.finish(ctx, i, nil, reason, 0, 0, now)
		}
		if i.State == "removed" {
			return policy.finish(ctx, i, pubs, "unlinked", i.Logical, i.Blocks, now)
		}
		return policy.finish(ctx, i, pubs, "missing_after_isolation", 0, 0, now)
	}
	if i.State == "reserved" && !absent {
		return policy.hold(ctx, i, "isolation_changed")
	}
	if absent && restore {
		if i.State == "moving" {
			if current, err := source.Stamp(); err != nil || current != old {
				return policy.hold(ctx, i, "isolation_changed")
			}
		}
		return policy.finish(ctx, i, nil, restoreReason, 0, 0, now)
	}
	if absent {
		if i.State == "reserved" {
			if err := policy.phase(ctx, i.ID, "moving", ""); err != nil {
				return "ledger_error"
			}
		}
		if o.interrupted("moving") || ctx.Err() != nil {
			return "interrupted"
		}
		if err := source.Move(isolated, old); err != nil {
			return diskcache.ReclaimReason(err)
		}
		if o.interrupted("renamed") {
			return "interrupted"
		}
		quarantined, err = isolated.Stamp()
		if err != nil {
			return diskcache.ReclaimReason(err)
		}
	}
	if !old.SameRenamed(quarantined) {
		return policy.hold(ctx, i, "isolation_changed")
	}
	if i.State == "isolated" || i.State == "restoring" {
		var recorded diskcache.ObjectStamp
		if json.Unmarshal([]byte(i.Isolated), &recorded) != nil || recorded != quarantined {
			return policy.hold(ctx, i, "isolation_changed")
		}
	}
	if !verified {
		*budget -= quarantined.Size
		if err := withoutLocation(func() error { return isolated.Verify(ctx, quarantined, pubs[0].Digest) }); err != nil {
			return diskcache.ReclaimReason(err)
		}
		current, err := o.repo.OrphanPublications(ctx, i.Key)
		if err != nil {
			return "ledger_error"
		}
		if cacheledger.OrphanPublicationSet(current) != set {
			return policy.hold(ctx, i, "publication_changed")
		}
		pubs = current
		protect = policy.qualify(ctx, i.Key, pubs, now, new(OrphanSample))
		restore = policy.restore(protect) || (policy.eviction && i.State == "restoring")
		if protect != "" && !restore {
			return protect
		}
	}
	restoreObject := func() string {
		if policy.eviction {
			if err := policy.phase(ctx, i.ID, "restoring", stampJSON(quarantined)); err != nil {
				return "ledger_error"
			}
			if o.interrupted("restoring") {
				return "interrupted"
			}
		}
		if err := isolated.Move(source, quarantined); err != nil {
			return diskcache.ReclaimReason(err)
		}
		if policy.eviction {
			if o.interrupted("restored") {
				return "interrupted"
			}
			current, err := source.Stamp()
			if err != nil {
				return diskcache.ReclaimReason(err)
			}
			if err := policy.restored(ctx, i, pubs, stampJSON(current), now); err != nil {
				return "ledger_error"
			}
			return restoreReason
		}
		return policy.finish(ctx, i, nil, restoreReason, 0, 0, now)
	}
	if restore {
		return restoreObject()
	}
	if err := policy.phase(ctx, i.ID, "isolated", stampJSON(quarantined)); err != nil {
		return "ledger_error"
	}
	if o.interrupted("isolated") || ctx.Err() != nil {
		return "interrupted"
	}
	if reason := policy.qualify(ctx, i.Key, pubs, now, new(OrphanSample)); reason != "" {
		if policy.restore(reason) {
			return restoreObject()
		}
		return reason
	}
	if err := isolated.Unlink(quarantined); err != nil {
		return diskcache.ReclaimReason(err)
	}
	if o.onUnlink != nil {
		o.onUnlink()
	}
	r.Unlinked++
	r.UnlinkedBytes += quarantined.Size
	r.UnlinkedBlocks += quarantined.Blocks
	if o.interrupted("unlinked") {
		return "interrupted"
	}
	if err := policy.phase(ctx, i.ID, "removed", ""); err != nil {
		return "ledger_error"
	}
	return policy.finish(ctx, i, pubs, "unlinked", quarantined.Size, quarantined.Blocks, now)
}

func (o *orphanManager) isolationAbsent(id string) bool {
	isolated, err := o.tree.OpenIsolated(id, false)
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	defer isolated.Close()
	_, err = isolated.Stamp()
	return errors.Is(err, os.ErrNotExist)
}

func (policy reclaimPolicy) hold(ctx context.Context, i cacheledger.OrphanIntent, reason string) string {
	if err := policy.phase(ctx, i.ID, "held", i.Isolated); err != nil {
		return "ledger_error"
	}
	return reason
}

func (policy reclaimPolicy) finish(ctx context.Context, i cacheledger.OrphanIntent, pubs []cacheledger.OrphanPublication,
	reason string, logical, blocks int64, now time.Time) string {
	if err := policy.settle(ctx, i, pubs, reason, logical, blocks, now); err != nil {
		return "ledger_error"
	}
	return reason
}
