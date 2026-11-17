// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package preview

// Queue admission is intentionally separate from the decode memory budget.
// Task slots limit how many distinct cache keys may have a goroutine, a bounded
// header read, and a pending decode. Same-key requests still coalesce without
// taking another slot.
const (
	previewTaskSlotFactor = 4
	minPreviewTaskSlots   = 8
	maxPreviewTaskSlots   = 64
	maxPreviewHTTPWaiters = 1024
)

type taskLimiter struct {
	slots chan struct{}
	stats *previewStats
}

func previewTaskSlots(maxConcurrent int) int {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	n := maxConcurrent * previewTaskSlotFactor
	if n < minPreviewTaskSlots {
		n = minPreviewTaskSlots
	}
	if n > maxPreviewTaskSlots {
		n = maxPreviewTaskSlots
	}
	return n
}

func newTaskLimiter(limit int, stats *previewStats) *taskLimiter {
	if limit < 1 {
		limit = 1
	}
	return &taskLimiter{slots: make(chan struct{}, limit), stats: stats}
}

func (l *taskLimiter) tryAcquire() bool {
	if l == nil {
		return true
	}
	select {
	case l.slots <- struct{}{}:
		if l.stats != nil {
			n := int64(len(l.slots))
			l.stats.taskSlotsInUse.Store(n)
			l.stats.peakTaskSlots.Store(maxInt64(l.stats.peakTaskSlots.Load(), n))
		}
		return true
	default:
		if l.stats != nil {
			l.stats.entryRejected.Add(1)
		}
		return false
	}
}

func (l *taskLimiter) release() {
	if l == nil {
		return
	}
	select {
	case <-l.slots:
		if l.stats != nil {
			l.stats.taskSlotsInUse.Store(int64(len(l.slots)))
		}
	default:
	}
}

func (l *taskLimiter) capacity() int {
	if l == nil {
		return 0
	}
	return cap(l.slots)
}
