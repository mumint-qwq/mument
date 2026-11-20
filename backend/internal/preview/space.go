package preview

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

const (
	spaceMiB          = 1 << 20
	spaceSnapshotLife = 2 * time.Minute
	spaceRescanEvery  = 30 * time.Second
	spaceScanPage     = 512
	spaceInodeStop    = 32
	spaceInodeResume  = 64
)

type SpaceDeferred struct{ reason string }

func (e *SpaceDeferred) Error() string { return "space_" + e.reason }

func IsSpaceDeferred(err error) bool {
	var deferred *SpaceDeferred
	return errors.As(err, &deferred)
}

type SpaceStatus struct {
	Mode               string                `json:"mode"`
	State              string                `json:"state"`
	Reason             string                `json:"reason"`
	Usage              diskcache.Usage       `json:"cache_usage"`
	UsageAdjusted      bool                  `json:"includes_write_adjustments"`
	Complete           bool                  `json:"inventory_complete"`
	InventoryAt        time.Time             `json:"inventory_at"`
	InventoryReason    string                `json:"inventory_reason,omitempty"`
	Filesystem         diskcache.SpaceSample `json:"filesystem"`
	FilesystemAt       time.Time             `json:"filesystem_at"`
	RecoveringSince    time.Time             `json:"recovering_since"`
	ReservedBytes      int64                 `json:"reserved_bytes"`
	ReservedInodes     int64                 `json:"reserved_inodes"`
	BudgetBytes        int64                 `json:"budget_bytes"`
	MinimumFreeBytes   int64                 `json:"minimum_free_bytes"`
	ResumeFreeBytes    int64                 `json:"resume_free_bytes"`
	ForegroundUnstored uint64                `json:"foreground_uncached"`
	DEnabled           bool                  `json:"warmup_enabled"`
	E2Enabled          bool                  `json:"orphan_reclamation_enabled"`
	E3Enabled          bool                  `json:"capacity_eviction_enabled"`
}

type spaceManager struct {
	tree *diskcache.ReclaimTree
	cfg  settings.PreviewSpaceConfig
	done chan struct{}
	now  func() time.Time

	mu               sync.Mutex
	status           SpaceStatus
	revision         uint64
	fsReason         string
	writeError       string
	observersWriting int
	rescan           chan struct{}
}

func newSpaceManager(tree *diskcache.ReclaimTree, cfg settings.PreviewSpaceConfig) *spaceManager {
	cfg = cfg.Defaults()
	return &spaceManager{
		tree: tree, cfg: cfg, done: make(chan struct{}), now: time.Now, rescan: make(chan struct{}, 1),
		status: SpaceStatus{Mode: cfg.Mode, State: "observing", Reason: "inventory_pending",
			BudgetBytes: cfg.BudgetMiB * spaceMiB, MinimumFreeBytes: cfg.MinimumFreeMiB * spaceMiB,
			ResumeFreeBytes: cfg.ResumeFreeMiB * spaceMiB},
	}
}

func (m *spaceManager) snapshot() SpaceStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func PreviewSpaceStatus() SpaceStatus {
	result := SpaceStatus{Mode: "off", State: "disabled", Reason: "disabled"}
	if s := service; s != nil && s.space != nil {
		result = s.space.snapshot()
	}
	result.DEnabled = settings.Config.Server.PreviewWarmup.Enabled
	result.E2Enabled = settings.Config.Server.PreviewOrphanReclamation
	result.E3Enabled = settings.Config.Server.PreviewSpace.CapacityEviction
	return result
}

func (s *Service) WarmupSpace() error {
	if s.space == nil || s.space.cfg.Mode != "enforce" {
		return nil
	}
	m := s.space
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.State != "healthy" {
		return &SpaceDeferred{m.status.Reason}
	}
	return nil
}

func (m *spaceManager) poll(now time.Time) {
	var sample diskcache.SpaceSample
	var err error
	if m.tree == nil {
		err = diskcache.ErrReclaimScope
	} else {
		sample, err = m.tree.SpaceSample()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.fsReason = diskcache.ReclaimReason(err)
		m.status.Complete = false
		m.status.InventoryReason = m.fsReason
		m.requestInventory()
	} else {
		m.status.Filesystem, m.status.FilesystemAt = sample, now.UTC()
		m.fsReason = ""
		m.writeError = ""
	}
	m.evaluate(now)
}

// evaluate is called under mu. Pressure is immediate, while every recovery
// must pass both low watermarks for an uninterrupted interval.
func (m *spaceManager) evaluate(now time.Time) {
	s := &m.status
	p := s.Filesystem
	if !s.InventoryAt.IsZero() && (now.Before(s.InventoryAt) || now.Sub(s.InventoryAt) > spaceSnapshotLife) {
		s.Complete = false
		s.InventoryReason = "inventory_stale"
	}
	used := saturatingAdd(s.Usage.Allocated, s.ReservedBytes)
	free := p.Available - s.ReservedBytes
	inodesOK := !p.HasInodes || p.Inodes >= uint64(spaceInodeStop+s.ReservedInodes)
	var reason string
	switch {
	case m.fsReason != "":
		reason = m.fsReason
	case p.ReadOnly:
		reason = "read_only"
	case m.writeError != "":
		reason = m.writeError
	case p.Block <= 0:
		reason = "filesystem_pending"
	case free < s.MinimumFreeBytes:
		reason = "low_free"
	case !inodesOK:
		reason = "low_inodes"
	case !s.Complete:
		reason = "inventory_pending"
	case s.BudgetBytes > 0 && used >= s.BudgetBytes*9/10:
		reason = "high_usage"
	}
	if reason != "" {
		s.State, s.Reason, s.RecoveringSince = "pressured", reason, time.Time{}
		return
	}
	if s.State == "healthy" {
		return
	}
	haveRoom := (s.BudgetBytes == 0 || used < s.BudgetBytes*3/4) &&
		free >= s.ResumeFreeBytes && (!p.HasInodes || p.Inodes >= uint64(spaceInodeResume+s.ReservedInodes))
	if !haveRoom {
		s.State, s.Reason, s.RecoveringSince = "pressured", "waiting_for_watermarks", time.Time{}
		return
	}
	if s.RecoveringSince.IsZero() || now.Before(s.RecoveringSince) {
		s.RecoveringSince = now.UTC()
	}
	if now.Sub(s.RecoveringSince) < time.Duration(m.cfg.StableSeconds)*time.Second {
		s.State, s.Reason = "recovering", "stabilizing"
		return
	}
	s.State, s.Reason, s.RecoveringSince = "healthy", "ready", time.Time{}
}

func (m *spaceManager) run(ctx context.Context) {
	defer close(m.done)
	m.poll(m.now())
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	pages := time.NewTicker(50 * time.Millisecond)
	defer pages.Stop()
	var inventory *diskcache.Inventory
	defer func() {
		if inventory != nil {
			inventory.Close()
		}
	}()
	var generation uint64
	var next time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			m.poll(m.now())
		case <-m.rescan:
			next = time.Time{}
		case <-pages.C:
			now := m.now()
			if m.tree == nil || now.Before(next) {
				continue
			}
			if inventory == nil {
				m.mu.Lock()
				generation = m.revision
				busy := m.status.ReservedInodes != 0 || m.observersWriting != 0
				m.mu.Unlock()
				if busy {
					continue
				}
				var err error
				inventory, err = m.tree.NewInventory()
				if err != nil {
					m.endInventory(now, generation, diskcache.Usage{}, err)
					next = now.Add(time.Second)
					continue
				}
			}
			page, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
			usage, complete, err := inventory.Step(page, spaceScanPage)
			cancel()
			if errors.Is(err, context.DeadlineExceeded) || (err == nil && !complete) {
				continue
			}
			inventory.Close()
			inventory = nil
			if err == nil {
				_, err = m.tree.SpaceSample()
			}
			if m.endInventory(now, generation, usage, err) {
				next = now.Add(spaceRescanEvery)
			} else {
				next = now.Add(time.Second)
			}
		}
	}
}

func (m *spaceManager) endInventory(now time.Time, revision uint64, usage diskcache.Usage, err error) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if revision != m.revision {
		m.status.InventoryReason = "concurrent_write"
		return false
	}
	if err != nil {
		m.status.Complete = false
		m.status.InventoryReason = diskcache.ReclaimReason(err)
		m.evaluate(now)
		return false
	}
	m.status.Usage, m.status.InventoryAt = usage, now.UTC()
	m.status.Complete, m.status.InventoryReason = usage.Excluded == 0, ""
	m.status.UsageAdjusted = false
	if !m.status.Complete {
		m.status.InventoryReason = "excluded_entries"
	}
	m.evaluate(now)
	return true
}

func saturatingAdd(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

type spaceReservation struct{ bytes int64 }

func (m *spaceManager) reserve(sample diskcache.SpaceSample, payload int64, background bool) (spaceReservation, error) {
	const inodes = 3 // temporary file and up to two new shard directories
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &m.status
	s.Filesystem, s.FilesystemAt = sample, now.UTC()
	m.fsReason = ""
	m.evaluate(now)
	deny := func(reason string) (spaceReservation, error) {
		return spaceReservation{}, &SpaceDeferred{reason}
	}
	if sample.ReadOnly {
		return deny("read_only")
	}
	if !s.Complete {
		return deny("inventory_pending")
	}
	if background && s.State != "healthy" {
		return deny(s.Reason)
	}
	if payload < 0 || sample.Block <= 0 || payload > math.MaxInt64-3*sample.Block {
		return deny("allocation_unknown")
	}
	allocated := (payload + sample.Block - 1) / sample.Block * sample.Block
	// Charge only payload blocks to the diskcache inventory budget. Reserve
	// two additional directory blocks against actual free space.
	reserved := allocated + 2*sample.Block
	freeFloor := s.MinimumFreeBytes
	cap := s.BudgetBytes
	if background {
		freeFloor = s.ResumeFreeBytes
		cap = cap * 9 / 10
	}
	if sample.Available-s.ReservedBytes-freeFloor < reserved {
		return deny("low_free")
	}
	if sample.HasInodes && sample.Inodes < uint64(spaceInodeStop+s.ReservedInodes+inodes) {
		return deny("low_inodes")
	}
	if cap > 0 && cap-s.Usage.Allocated-s.ReservedBytes < allocated {
		return deny("budget_limit")
	}
	s.ReservedBytes += reserved
	s.ReservedInodes += inodes
	m.revision++
	m.evaluate(now)
	return spaceReservation{reserved}, nil
}

func (m *spaceManager) finish(res spaceReservation, payload int64, allocated int64, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revision++
	s := &m.status
	s.ReservedBytes -= res.bytes
	s.ReservedInodes -= 3
	if err == nil {
		// Replacements retain their old charge until the next full inventory.
		// This covers externally introduced or still-open old inodes too.
		s.Usage.Allocated = saturatingAdd(s.Usage.Allocated, allocated)
		s.Usage.Logical = saturatingAdd(s.Usage.Logical, payload)
		s.UsageAdjusted = true
	} else if reason := diskcache.SpaceWriteReason(err); reason != "" {
		m.writeError = reason
		s.Complete = false
		s.InventoryReason = reason
		m.requestInventory()
	}
	m.evaluate(m.now())
}

func (m *spaceManager) requestInventory() {
	select {
	case m.rescan <- struct{}{}:
	default:
	}
}

func (s *Service) storePreview(ctx context.Context, key string, bytes []byte, background bool) error {
	m := s.space
	if m == nil {
		return s.fileCache.Store(ctx, key, bytes)
	}
	if m.cfg.Mode == "observe" {
		m.mu.Lock()
		m.revision++
		m.observersWriting++
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			m.revision++
			m.observersWriting--
			m.mu.Unlock()
		}()
		return s.fileCache.Store(ctx, key, bytes)
	}
	if m.tree == nil {
		return &SpaceDeferred{"scope_unavailable"}
	}
	sample, err := m.tree.SpaceSample()
	if err != nil {
		m.mu.Lock()
		m.status.Complete = false
		m.fsReason = diskcache.ReclaimReason(err)
		m.status.InventoryReason = m.fsReason
		m.evaluate(m.now())
		m.requestInventory()
		m.mu.Unlock()
		return &SpaceDeferred{diskcache.ReclaimReason(err)}
	}
	reservation, err := m.reserve(sample, int64(len(bytes)), background)
	if err != nil {
		return err
	}
	err = m.tree.Store(ctx, key, bytes)
	allocated := reservation.bytes - 2*sample.Block
	if err == nil {
		if object, err := m.tree.OpenKey(key); err == nil {
			if stamp, err := object.Stamp(); err == nil {
				allocated = max(allocated, stamp.Blocks)
			}
			object.Close()
		}
	}
	m.finish(reservation, int64(len(bytes)), allocated, err)
	if reason := diskcache.SpaceWriteReason(err); reason != "" {
		return &SpaceDeferred{reason}
	}
	return err
}
