package preview

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
)

type capacitySourceProof struct {
	orphanSourceProof
	Path string
}

func (plan *ledgerPreview) capacityProof() string {
	if plan == nil || plan.ref.Asset == "" || !plan.version.Strong {
		return ""
	}
	idx := indexing.GetIndex(plan.source)
	if idx == nil {
		return ""
	}
	source, err := diskcache.ObserveSource(idx.Path, plan.path)
	if err != nil {
		return ""
	}
	defer source.Close()
	if source.Proof.Check() != nil || cacheledger.FromFileInfo(source.Info) != plan.version {
		return ""
	}
	proof := capacitySourceProof{orphanSourceProof{plan.source, source.Proof}, plan.path}
	b, _ := json.Marshal(proof)
	return string(b)
}

func (p capacitySourceProof) matches(ref cacheledger.Ref) bool {
	if p.checkOnline() != nil {
		return false
	}
	current, err := diskcache.ObserveSource(p.Root, p.Path)
	if err != nil {
		return false
	}
	defer current.Close()
	ns, name, ok := cacheledger.Location(p.Root, p.Path)
	return ok && ns == ref.Source && name == ref.Path && current.Proof.RootID == p.RootID &&
		current.Proof.ParentID == p.ParentID && cacheledger.FromFileInfo(current.Info) == ref.Version
}

type CapacityCursor struct {
	Candidate cacheledger.CapacityCursor `json:"candidate"`
	Intent    int64                      `json:"intent"`
	Audit     int64                      `json:"audit"`
}

type CapacityReport struct {
	OrphanReport
	Examined    int            `json:"examined_candidates"`
	Next        CapacityCursor `json:"next"`
	Paused      bool           `json:"paused"`
	Pressure    string         `json:"pressure"`
	OrphanFirst string         `json:"orphan_first"`
}

type capacityManager struct {
	orphan         *orphanManager
	space          *spaceManager
	done           chan struct{}
	active         [64]atomic.Int32
	uncertainUsage [64]atomic.Int64
	cooldownSkips  atomic.Uint64
	paused         atomic.Bool
	last           atomic.Pointer[CapacityReport]
	priority       atomic.Pointer[string]
}

func newCapacityManager(orphan *orphanManager, space *spaceManager) *capacityManager {
	return &capacityManager{orphan: orphan, space: space, done: make(chan struct{})}
}

// Register before the first cache lookup. A capacity operation must hold the
// same key gate while testing registered deliveries and committing isolation.
func (m *capacityManager) claim(ctx context.Context, key string) (func(), error) {
	if m == nil {
		return func() {}, nil
	}
	unlock, err := m.orphan.lockKey(ctx, key)
	if err != nil {
		return nil, err
	}
	active := &m.active[keyStripe(key)]
	active.Add(1)
	unlock()
	return func() { active.Add(-1) }, nil
}

func (m *capacityManager) rememberUsageFailure(key string, now time.Time) {
	m.uncertainUsage[keyStripe(key)].Store(now.UnixNano())
}

func (m *capacityManager) policy(trial bool) reclaimPolicy {
	r := m.orphan.repo
	return reclaimPolicy{
		qualify: func(ctx context.Context, key string, pubs []cacheledger.OrphanPublication, now time.Time, sample *OrphanSample) string {
			return m.qualify(ctx, key, pubs, now, sample, trial)
		},
		intent: r.CapacityIntent, reserve: r.ReserveCapacity, phase: r.CapacityPhase, settle: r.SettleCapacity,
		restore: func(reason string) bool {
			return reason == "space_recovered" || reason == "in_use" || reason == "recent_use"
		},
		eviction: true,
	}
}

func (m *capacityManager) qualify(ctx context.Context, key string, pubs []cacheledger.OrphanPublication,
	now time.Time, sample *OrphanSample, trial bool) string {
	if len(pubs) == 0 || len(pubs) > cacheledger.OrphanPageSize {
		return "publication_coverage"
	}
	stripe := keyStripe(key)
	if m.active[stripe].Load() != 0 {
		return "in_use"
	}
	if last := m.uncertainUsage[stripe].Load(); last > now.Add(-cacheledger.CapacityUseProtection).UnixNano() && last != 0 {
		return "recent_use"
	}
	first := pubs[0]
	for _, p := range pubs {
		switch {
		case p.Moving:
			return "move_protected"
		case p.Unproven || !p.Referenced:
			return "reference_protected"
		case p.Epoch == "" || p.Stamp == "" || p.Key != "c-raster/"+p.Asset+"/"+p.Variant:
			return "untracked_publication"
		case p.Stamp != first.Stamp || p.Digest != first.Digest || p.Size != first.Size:
			return "shared_publication_mismatch"
		case p.Generated <= 0 || p.Generated > now.Add(-cacheledger.CapacityNewProtection).UnixNano():
			return "new_publication"
		case p.LastUsed != 0 && p.LastUsed > now.Add(-cacheledger.CapacityUseProtection).UnixNano():
			return "recent_use"
		}
		proof, ref, err := m.orphan.repo.CapacitySource(ctx, p)
		var pScope capacitySourceProof
		if err != nil || json.Unmarshal([]byte(proof), &pScope) != nil || !pScope.matches(ref) {
			return "source_identity_unavailable"
		}
	}
	sample.Logical = first.Size
	if first.Size <= 0 || first.Size > orphanMaxFile {
		return "file_byte_limit"
	}
	if !trial {
		if m.paused.Load() {
			return "administrator_paused"
		}
		m.space.poll(m.space.now())
		if !m.space.needsCapacity() {
			return "space_recovered"
		}
	}
	return ""
}

func (m *capacityManager) run(ctx context.Context) {
	defer close(m.done)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	var cursor CapacityCursor
	orphanOffered := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if m.space.cfg.Mode != "enforce" {
				return
			}
			m.step(ctx, &cursor, &orphanOffered)
		}
	}
}

func (m *capacityManager) step(ctx context.Context, cursor *CapacityCursor, orphanOffered *bool) {
	pressure := m.space.needsCapacity()
	if !pressure && m.space.snapshot().State == "healthy" {
		*orphanOffered = false
		m.priority.Store(nil)
	}
	if pressure && !*orphanOffered {
		if m.orphan.enabled && !m.orphan.executeWindow(ctx) {
			return
		}
		priority := "disabled"
		if m.orphan.enabled {
			priority = "offered"
		}
		m.priority.Store(&priority)
		*orphanOffered = true
		m.space.poll(m.space.now())
		return
	}
	if m.space.cfg.CapacityEviction && !m.paused.Load() {
		report := m.report(ctx, *cursor, true)
		*cursor = report.Next
		if report.Examined != 0 || len(report.Samples) != 0 || m.last.Load() == nil {
			m.last.Store(&report)
		}
	}
}

func (m *capacityManager) report(parent context.Context, cursor CapacityCursor, apply bool) CapacityReport {
	ctx, cancel := context.WithTimeout(parent, orphanWindow)
	defer cancel()
	r := CapacityReport{OrphanReport: OrphanReport{
		Enabled: m.space.cfg.CapacityEviction, Tracking: true, Reasons: make(map[string]int),
	}, Paused: m.paused.Load(), Pressure: m.space.snapshot().Reason, OrphanFirst: "pending"}
	if priority := m.priority.Load(); priority != nil {
		r.OrphanFirst = *priority
	} else if !m.orphan.enabled {
		r.OrphanFirst = "disabled"
	}
	apply = apply && r.Enabled && !r.Paused
	if err := m.orphan.tree.Check(apply); err != nil {
		r.Reasons[diskcache.ReclaimReason(err)]++
		r.Next = cursor
		return r
	}
	now := m.space.now()
	var bytes int64 = orphanByteLimit
	policy := m.policy(!apply)
	intents, err := m.orphan.repo.CapacityIntents(ctx, cursor.Intent)
	if err != nil {
		r.Reasons["ledger_error"]++
		return r
	}
	pressure := apply && m.space.needsCapacity()
	if apply {
		write := pressure
		for _, intent := range intents {
			if intent.State != "held" {
				write = true
				break
			}
		}
		if write && m.orphan.repo.AdvanceCapacityClock(ctx, now) != nil {
			r.Reasons["clock_or_ledger"]++
			r.Next = cursor
			return r
		}
	}
	matched := make(map[string]bool)
	for _, i := range intents {
		if ctx.Err() != nil || bytes < orphanMaxFile {
			r.Next.Intent = cursor.Intent
			break
		}
		matched[i.Key] = m.orphan.inspect(ctx, policy, &r.OrphanReport, i.Key, i, now, apply, &bytes) || matched[i.Key]
		cursor.Intent = i.Row
	}
	if len(intents) == cacheledger.OrphanPageSize {
		r.Next.Intent = cursor.Intent
	}
	if apply && (!pressure || !m.space.needsCapacity()) {
		r.Reasons["space_recovered"]++
		return r
	}
	candidates, err := m.orphan.repo.CapacityCandidates(ctx, cursor.Candidate, now)
	if err != nil {
		r.Reasons["ledger_error"]++
		return r
	}
	for _, c := range candidates {
		if ctx.Err() != nil || bytes < orphanMaxFile {
			r.Next.Candidate = cursor.Candidate
			return r
		}
		if !matched[c.Key] {
			r.Examined++
			m.orphan.inspect(ctx, policy, &r.OrphanReport, c.Key, cacheledger.OrphanIntent{}, now, apply, &bytes)
			matched[c.Key] = true
		}
		cursor.Candidate = c.Cursor
		if apply && !m.space.needsCapacity() {
			r.Next.Candidate = cursor.Candidate
			return r
		}
	}
	if len(candidates) == cacheledger.OrphanPageSize {
		r.Next.Candidate = cursor.Candidate
	} else if !cursor.Candidate.Used {
		r.Next.Candidate.Used = true
	}
	if !apply {
		r.RecentRemoval, r.Next.Audit, _ = m.orphan.repo.CapacityAuditPage(ctx, cursor.Audit)
	}
	return r
}

func PreviewCapacityStatus(ctx context.Context, cursor CapacityCursor) CapacityReport {
	if s := service; s != nil && !s.closed.Load() && s.capacity != nil {
		return s.capacity.report(ctx, cursor, false)
	}
	return CapacityReport{OrphanReport: OrphanReport{Reasons: map[string]int{"tracking_unavailable": 1}}}
}

func ControlPreviewCapacity(pause bool) error {
	if s := service; s != nil && !s.closed.Load() && s.capacity != nil && s.capacity.space.cfg.CapacityEviction {
		s.capacity.paused.Store(pause)
		return nil
	}
	return errors.New("capacity eviction is unavailable")
}
