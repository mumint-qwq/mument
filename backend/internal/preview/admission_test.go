// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package preview

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBudgetWeightedFIFO(t *testing.T) {
	b := newResourceBudget(4, 50, newPreviewStats())
	first, err := b.acquire(context.Background(), 40)
	require.NoError(t, err)

	order := make(chan string, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	// Enqueue the 30 MiB waiter first, then verify it is actually the head
	// before adding the 25 MiB waiter. Without this barrier the test would be
	// asserting goroutine scheduling order, not FIFO order.
	go func() {
		defer wg.Done()
		permit, err := b.acquire(context.Background(), 30)
		if err != nil {
			return
		}
		order <- "30"
		permit.release()
	}()
	require.Eventually(t, func() bool {
		active, used, waiting := b.snapshot()
		return active == 1 && used == 40 && waiting == 1
	}, time.Second, time.Millisecond)
	go func() {
		defer wg.Done()
		permit, err := b.acquire(context.Background(), 25)
		if err != nil {
			return
		}
		order <- "25"
		permit.release()
	}()

	require.Eventually(t, func() bool {
		active, used, waiting := b.snapshot()
		return active == 1 && used == 40 && waiting == 2
	}, time.Second, time.Millisecond)

	first.release()
	require.Equal(t, "30", <-order)
	require.Equal(t, "25", <-order)
	wg.Wait()
	active, used, _ := b.snapshot()
	require.Zero(t, active)
	require.Zero(t, used)
}

func TestBudgetConcurrencyCap(t *testing.T) {
	b := newResourceBudget(2, 1000, newPreviewStats())
	p1, err := b.acquire(context.Background(), 1)
	require.NoError(t, err)
	p2, err := b.acquire(context.Background(), 1)
	require.NoError(t, err)

	blocked := make(chan struct{})
	go func() {
		p3, err := b.acquire(context.Background(), 1)
		if err == nil {
			close(blocked)
			p3.release()
		}
	}()
	require.Eventually(t, func() bool {
		_, _, waiting := b.snapshot()
		return waiting == 1
	}, time.Second, time.Millisecond)
	select {
	case <-blocked:
		t.Fatal("third task was granted above the concurrency cap")
	default:
	}
	p1.release()
	require.Eventually(t, func() bool {
		select {
		case <-blocked:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	p2.release()
}

func TestBudgetOverBudgetImmediate(t *testing.T) {
	b := newResourceBudget(4, 100, newPreviewStats())
	_, err := b.acquire(context.Background(), 101)
	require.ErrorIs(t, err, ErrPreviewOverBudget)
	active, used, waiting := b.snapshot()
	require.Zero(t, active)
	require.Zero(t, used)
	require.Zero(t, waiting)
}

func TestBudgetCancelQueued(t *testing.T) {
	b := newResourceBudget(4, 100, newPreviewStats())
	hold, err := b.acquire(context.Background(), 100)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := b.acquire(ctx, 10)
		done <- err
	}()
	require.Eventually(t, func() bool {
		_, _, waiting := b.snapshot()
		return waiting == 1
	}, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	hold.release()
	active, used, waiting := b.snapshot()
	require.Zero(t, active)
	require.Zero(t, used)
	require.Zero(t, waiting)
}

func TestBudgetCloseWakesWaiters(t *testing.T) {
	b := newResourceBudget(4, 100, newPreviewStats())
	hold, err := b.acquire(context.Background(), 100)
	require.NoError(t, err)
	defer hold.release()

	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := b.acquire(context.Background(), 10)
			errs <- err
		}()
	}
	require.Eventually(t, func() bool {
		_, _, waiting := b.snapshot()
		return waiting == 2
	}, time.Second, time.Millisecond)
	b.close()
	require.ErrorIs(t, <-errs, ErrPreviewShuttingDown)
	require.ErrorIs(t, <-errs, ErrPreviewShuttingDown)
	_, err = b.acquire(context.Background(), 1)
	require.ErrorIs(t, err, ErrPreviewShuttingDown)
}

func TestBudgetPermitReleaseIdempotent(t *testing.T) {
	b := newResourceBudget(4, 100, newPreviewStats())
	p, err := b.acquire(context.Background(), 50)
	require.NoError(t, err)
	p.release()
	p.release()
	active, used, _ := b.snapshot()
	require.Zero(t, active)
	require.Zero(t, used)
}

func TestFlightCoalescesSameKey(t *testing.T) {
	stats := newPreviewStats()
	g := newFlightGroup(context.Background(), stats)
	defer g.close()

	var calls atomic.Int64
	entered := make(chan struct{})
	release := make(chan struct{})
	fn := func(ctx context.Context, markStarted func() bool) ([]byte, error) {
		if !markStarted() {
			return nil, context.Canceled
		}
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return []byte("shared-result"), nil
	}

	const waiters = 20
	results := make(chan []byte, waiters)
	errs := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		go func() {
			data, err := g.do(context.Background(), "same-key", fn)
			if err != nil {
				errs <- err
				return
			}
			results <- data
		}()
	}
	<-entered
	require.Eventually(t, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		call := g.calls["same-key"]
		return call != nil && call.waiters == waiters
	}, 2*time.Second, time.Millisecond)
	close(release)
	for i := 0; i < waiters; i++ {
		select {
		case data := <-results:
			require.Equal(t, "shared-result", string(data))
		case err := <-errs:
			t.Fatalf("unexpected waiter error: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatal("waiter did not receive the shared result")
		}
	}
	require.Equal(t, int64(1), calls.Load())
	require.Equal(t, uint64(1), stats.tasksCompleted.Load())
	require.Equal(t, uint64(waiters-1), stats.sharedWaits.Load())
}

func TestFlightDifferentKeysRunSeparately(t *testing.T) {
	g := newFlightGroup(context.Background(), newPreviewStats())
	defer g.close()
	var calls atomic.Int64
	fn := func(ctx context.Context, markStarted func() bool) ([]byte, error) {
		markStarted()
		calls.Add(1)
		return []byte("x"), nil
	}
	var wg sync.WaitGroup
	for _, key := range []string{"a", "b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, err := g.do(context.Background(), key, fn)
			require.NoError(t, err)
		}(key)
	}
	wg.Wait()
	require.Equal(t, int64(2), calls.Load())
}

func TestFlightPartialCancel(t *testing.T) {
	g := newFlightGroup(context.Background(), newPreviewStats())
	defer g.close()
	entered := make(chan struct{})
	release := make(chan struct{})
	fn := func(ctx context.Context, markStarted func() bool) ([]byte, error) {
		markStarted()
		close(entered)
		<-release
		return []byte("ok"), nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	canceled := make(chan error, 1)
	kept := make(chan []byte, 1)
	go func() {
		_, err := g.do(ctx, "key", fn)
		canceled <- err
	}()
	go func() {
		data, err := g.do(context.Background(), "key", fn)
		require.NoError(t, err)
		kept <- data
	}()
	<-entered
	require.Eventually(t, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		call := g.calls["key"]
		return call != nil && call.waiters == 2
	}, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-canceled, context.Canceled)
	close(release)
	require.Equal(t, "ok", string(<-kept))
}

func TestFlightLastWaiterQueuedCancelsTask(t *testing.T) {
	g := newFlightGroup(context.Background(), newPreviewStats())
	defer g.close()
	started := make(chan struct{})
	done := make(chan error, 1)
	fn := func(ctx context.Context, markStarted func() bool) ([]byte, error) {
		close(started)
		<-ctx.Done()
		done <- ctx.Err()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, _ = g.do(ctx, "queued", fn)
	}()
	<-started
	require.Eventually(t, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.calls["queued"] != nil
	}, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.Eventually(t, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.calls["queued"] == nil
	}, time.Second, time.Millisecond)
}

func TestFlightLastWaiterRunningContinues(t *testing.T) {
	stats := newPreviewStats()
	g := newFlightGroup(context.Background(), stats)
	defer g.close()
	started := make(chan struct{})
	release := make(chan struct{})
	fnDone := make(chan struct{})
	fn := func(ctx context.Context, markStarted func() bool) ([]byte, error) {
		if !markStarted() {
			return nil, context.Canceled
		}
		close(started)
		<-release
		close(fnDone)
		return []byte("finished"), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := g.do(ctx, "running", fn)
		waiterDone <- err
	}()
	<-started
	cancel()
	require.ErrorIs(t, <-waiterDone, context.Canceled)

	// The task has started, so cancellation removes only the waiter; generation
	// keeps its reservation and finishes before the flight disappears.
	select {
	case <-fnDone:
		t.Fatal("started task was canceled with its last waiter")
	default:
	}
	close(release)
	<-fnDone
	require.Eventually(t, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.calls["running"] == nil
	}, time.Second, time.Millisecond)
	require.Equal(t, uint64(1), stats.tasksCompleted.Load())
}

func TestFlightPanicAndRetry(t *testing.T) {
	stats := newPreviewStats()
	g := newFlightGroup(context.Background(), stats)
	defer g.close()
	_, err := g.do(context.Background(), "panic", func(ctx context.Context, markStarted func() bool) ([]byte, error) {
		markStarted()
		panic("boom")
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "panicked")
	require.Equal(t, uint64(1), stats.panics.Load())

	data, err := g.do(context.Background(), "panic", func(ctx context.Context, markStarted func() bool) ([]byte, error) {
		markStarted()
		return []byte("retry-ok"), nil
	})
	require.NoError(t, err)
	require.Equal(t, "retry-ok", string(data))
}

func TestFlightFailureDoesNotPersist(t *testing.T) {
	g := newFlightGroup(context.Background(), newPreviewStats())
	defer g.close()
	var calls atomic.Int64
	fn := func(ctx context.Context, markStarted func() bool) ([]byte, error) {
		markStarted()
		if calls.Add(1) == 1 {
			return nil, errors.New("transient")
		}
		return []byte("second"), nil
	}
	_, err := g.do(context.Background(), "retry", fn)
	require.Error(t, err)
	data, err := g.do(context.Background(), "retry", fn)
	require.NoError(t, err)
	require.Equal(t, "second", string(data))
}

func TestFlightShutdownWaitsForStartedTask(t *testing.T) {
	g := newFlightGroup(context.Background(), newPreviewStats())
	started := make(chan struct{})
	release := make(chan struct{})
	fn := func(ctx context.Context, markStarted func() bool) ([]byte, error) {
		markStarted()
		close(started)
		<-release
		return []byte("ok"), nil
	}
	waiterDone := make(chan error, 1)
	go func() {
		_, err := g.do(context.Background(), "shutdown", fn)
		waiterDone <- err
	}()
	<-started
	g.close()
	waitCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, g.wait(waitCtx), context.DeadlineExceeded)
	close(release)
	require.NoError(t, g.wait(context.Background()))
	require.NoError(t, <-waiterDone)
}
