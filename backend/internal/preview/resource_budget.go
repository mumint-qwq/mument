// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package preview

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// resourceBudget is the single admission point for preview work. It replaces
// the old eight-megabyte small/large semaphore split: numImageProcessors is the
// maximum number of concurrent preview tasks, and every task must also reserve
// its conservative decode estimate against the shared byte budget.
//
// Invariants:
//   - a reservation is taken atomically with the concurrency check;
//   - a task whose estimate exceeds the whole budget is rejected immediately,
//     never queued forever;
//   - waiters are granted in FIFO order, so a large task cannot be starved by a
//     stream of small requests, and small requests are not blocked by a task
//     that does not fit;
//   - release is idempotent and wakes the next eligible waiters;
//   - closing the budget wakes every waiter with ErrPreviewShuttingDown.
type resourceBudget struct {
	mu            sync.Mutex
	maxConcurrent int
	maxBytes      int64
	active        int
	usedBytes     int64
	waiters       []*budgetWaiter
	closed        bool

	stats *previewStats
}

type budgetWaiter struct {
	bytes    int64
	ready    chan struct{}
	granted  bool
	err      error
	queuedAt time.Time
}

type budgetPermit struct {
	budget *resourceBudget
	bytes  int64
	once   sync.Once
}

func newResourceBudget(maxConcurrent int, maxBytes int64, stats *previewStats) *resourceBudget {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	if maxBytes < 1 {
		maxBytes = 1
	}
	return &resourceBudget{
		maxConcurrent: maxConcurrent,
		maxBytes:      maxBytes,
		stats:         stats,
	}
}

func (p *budgetPermit) release() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		p.budget.release(p.bytes)
	})
}

func (b *resourceBudget) acquire(ctx context.Context, bytes int64) (*budgetPermit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if bytes < 1 {
		bytes = 1
	}
	if bytes > b.maxBytes {
		if b.stats != nil {
			b.stats.overBudget.Add(1)
		}
		return nil, fmt.Errorf("%w: estimated %d MiB exceeds the %d MiB budget", ErrPreviewOverBudget, bytes>>20, b.maxBytes>>20)
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrPreviewShuttingDown
	}
	if len(b.waiters) == 0 && b.active < b.maxConcurrent && b.usedBytes+bytes <= b.maxBytes {
		b.grantLocked(bytes)
		b.mu.Unlock()
		return &budgetPermit{budget: b, bytes: bytes}, nil
	}
	w := &budgetWaiter{bytes: bytes, ready: make(chan struct{}), queuedAt: time.Now()}
	b.waiters = append(b.waiters, w)
	if b.stats != nil {
		b.stats.waitingTasks.Add(1)
		b.stats.peakWaiting.Store(maxInt64(b.stats.peakWaiting.Load(), int64(len(b.waiters))))
	}
	b.mu.Unlock()

	select {
	case <-w.ready:
		if b.stats != nil {
			b.stats.waitingTasks.Add(-1)
			b.stats.queueWaitNanos.Add(uint64(time.Since(w.queuedAt).Nanoseconds()))
		}
		b.mu.Lock()
		granted := w.granted
		err := w.err
		b.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if !granted {
			return nil, ErrPreviewShuttingDown
		}
		if err := ctx.Err(); err != nil {
			// The caller was canceled at the same time the permit was granted.
			b.release(bytes)
			return nil, err
		}
		return &budgetPermit{budget: b, bytes: bytes}, nil
	case <-ctx.Done():
		b.mu.Lock()
		if w.granted {
			// A grant raced with cancellation. Return the reservation rather
			// than leaking it; the caller is already gone.
			w.granted = false
			b.active--
			b.usedBytes -= bytes
			b.grantWaitersLocked()
		} else {
			w.err = ctx.Err()
			b.removeWaiterLocked(w)
		}
		if b.stats != nil {
			b.stats.waitingTasks.Add(-1)
		}
		b.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (b *resourceBudget) release(bytes int64) {
	b.mu.Lock()
	b.active--
	b.usedBytes -= bytes
	if b.active < 0 {
		b.active = 0
	}
	if b.usedBytes < 0 {
		b.usedBytes = 0
	}
	if b.stats != nil {
		b.stats.activeTasks.Store(int64(b.active))
		b.stats.reservedBytes.Store(b.usedBytes)
	}
	b.grantWaitersLocked()
	b.mu.Unlock()
}

func (b *resourceBudget) close() {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		for _, w := range b.waiters {
			if !w.granted {
				w.err = ErrPreviewShuttingDown
				close(w.ready)
			}
		}
		b.waiters = nil
	}
	b.mu.Unlock()
}

func (b *resourceBudget) snapshot() (active int, used int64, waiting int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active, b.usedBytes, len(b.waiters)
}

func (b *resourceBudget) removeWaiterLocked(target *budgetWaiter) {
	for i, w := range b.waiters {
		if w == target {
			b.waiters = append(b.waiters[:i], b.waiters[i+1:]...)
			// Removing the head can make a later waiter fit immediately. Wake
			// eligible waiters now instead of waiting for an unrelated release.
			b.grantWaitersLocked()
			return
		}
	}
}

func (b *resourceBudget) grantWaitersLocked() {
	for len(b.waiters) > 0 {
		w := b.waiters[0]
		if b.active >= b.maxConcurrent || b.usedBytes+w.bytes > b.maxBytes {
			return
		}
		b.waiters = b.waiters[1:]
		w.granted = true
		b.grantLocked(w.bytes)
		close(w.ready)
	}
}

func (b *resourceBudget) grantLocked(bytes int64) {
	b.active++
	b.usedBytes += bytes
	if b.stats != nil {
		b.stats.activeTasks.Store(int64(b.active))
		b.stats.reservedBytes.Store(b.usedBytes)
		b.stats.peakActive.Store(maxInt64(b.stats.peakActive.Load(), int64(b.active)))
		b.stats.peakReservedBytes.Store(maxInt64(b.stats.peakReservedBytes.Load(), b.usedBytes))
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
