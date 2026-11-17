// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package preview

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/gtsteffaniak/go-logger/logger"
)

// flightGroup coalesces concurrent preview requests for the same cache key.
// Only the leader executes fn; every waiter receives the same result.
//
// Bounds:
//   - a new cache key consumes one task slot before its goroutine or header
//     read exists; saturation is rejected immediately with ErrPreviewSaturated;
//   - requests for an existing key join without consuming another slot;
//   - total HTTP waiters are capped separately because a same-key join still
//     costs one request goroutine even though it shares one decode.
//
// Cancellation ownership:
//   - a waiter's context only cancels that waiter;
//   - while no waiter remains and the task has not started decoding, the queued
//     task is removed and canceled;
//   - once decoding has started it cannot be preempted. The task keeps its
//     admission reservation until the decode really returns, even if every
//     waiter has gone. This prevents an immediate duplicate decode while the
//     first one still holds memory.
type flightGroup struct {
	mu           sync.Mutex
	calls        map[string]*flightCall
	stats        *previewStats
	baseCtx      context.Context
	wg           sync.WaitGroup
	tasks        *taskLimiter
	totalWaiters int
	maxWaiters   int
	closed       bool
}

type flightCall struct {
	key       string
	done      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	waiters   int
	started   bool
	finished  bool
	entryHeld bool
	data      []byte
	err       error
}

func newFlightGroup(baseCtx context.Context, stats *previewStats, tasks ...*taskLimiter) *flightGroup {
	limiter := newTaskLimiter(maxPreviewTaskSlots, stats)
	if len(tasks) > 0 && tasks[0] != nil {
		limiter = tasks[0]
	}
	return &flightGroup{
		calls:      make(map[string]*flightCall),
		stats:      stats,
		baseCtx:    baseCtx,
		tasks:      limiter,
		maxWaiters: maxPreviewHTTPWaiters,
	}
}

func (g *flightGroup) do(ctx context.Context, key string, fn func(context.Context, func() bool) ([]byte, error)) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil, ErrPreviewShuttingDown
	}
	if g.maxWaiters > 0 && g.totalWaiters >= g.maxWaiters {
		g.mu.Unlock()
		g.stats.waiterRejected.Add(1)
		return nil, ErrPreviewSaturated
	}
	call := g.calls[key]
	if call == nil {
		if !g.tasks.tryAcquire() {
			g.mu.Unlock()
			return nil, ErrPreviewSaturated
		}
		taskCtx, cancel := context.WithCancel(g.baseCtx)
		call = &flightCall{
			key:       key,
			done:      make(chan struct{}),
			ctx:       taskCtx,
			cancel:    cancel,
			entryHeld: true,
		}
		g.calls[key] = call
		g.stats.tasksStarted.Add(1)
		g.wg.Add(1)
		go g.run(call, fn)
	} else {
		g.stats.sharedWaits.Add(1)
	}
	call.waiters++
	g.totalWaiters++
	g.stats.httpWaiters.Add(1)
	g.mu.Unlock()

	select {
	case <-call.done:
		g.mu.Lock()
		data, err := call.data, call.err
		g.totalWaiters--
		g.stats.httpWaiters.Add(-1)
		g.mu.Unlock()
		return data, err
	case <-ctx.Done():
		g.mu.Lock()
		g.totalWaiters--
		g.stats.httpWaiters.Add(-1)
		g.mu.Unlock()
		g.stats.canceledWaiters.Add(1)
		g.leave(call)
		return nil, ctx.Err()
	}
}

func (g *flightGroup) run(call *flightCall, fn func(context.Context, func() bool) ([]byte, error)) {
	defer g.wg.Done()
	defer func() {
		if call.entryHeld {
			g.tasks.release()
		}
	}()
	// Always cancel the task context after the task really ends. Without this,
	// every completed flight leaves a child context referenced by baseCtx until
	// the whole service shuts down.
	defer call.cancel()

	markStarted := func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		if call.waiters == 0 {
			return false
		}
		call.started = true
		return true
	}

	var data []byte
	var err error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				g.stats.panics.Add(1)
				err = fmt.Errorf("preview generation panicked: %v", recovered)
				logger.Errorf("preview generation panic: %v\n%s", recovered, debug.Stack())
			}
		}()
		data, err = fn(call.ctx, markStarted)
	}()

	g.mu.Lock()
	call.data = data
	call.err = err
	call.finished = true
	if g.calls[call.key] == call {
		delete(g.calls, call.key)
	}
	close(call.done)
	g.mu.Unlock()

	if err != nil {
		g.stats.tasksFailed.Add(1)
	} else {
		g.stats.tasksCompleted.Add(1)
	}
}

func (g *flightGroup) leave(call *flightCall) {
	g.mu.Lock()
	defer g.mu.Unlock()
	call.waiters--
	if call.waiters > 0 || call.started || call.finished {
		return
	}
	// Nobody is waiting and the task has not started decoding. Remove it before
	// canceling so a new request creates a fresh flight instead of joining a
	// canceled one.
	if g.calls[call.key] == call {
		delete(g.calls, call.key)
	}
	g.stats.canceledQueued.Add(1)
	call.cancel()
}

func (g *flightGroup) close() {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.closed = true
	for _, call := range g.calls {
		call.cancel()
	}
	g.mu.Unlock()
}

func (g *flightGroup) wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
