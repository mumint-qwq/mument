package files

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
)

func (e *queueEnv) waitForStats(what string, fn func(MaintenanceStats) bool) MaintenanceStats {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		stats := e.q.statsSnapshot()
		if fn(stats) {
			return stats
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("timeout waiting for %s: %+v", what, stats)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A transient failure before the metadata write must be retried with the work
// that actually failed, not with an empty task. This is the RC1 review's
// one-time panic probe, inverted into a correctness assertion.
func TestQueueRetryReplaysFailedScan(t *testing.T) {
	env := newQueueEnv(t)
	env.mkdir("A")
	env.refresh("/A/")
	env.write("A/new.jpg", "synthetic")

	var mu sync.Mutex
	scans := 0
	indexing.SetRefreshTestHookForTesting(func(stage, source, dir string) {
		if stage != "before_metadata" || source != env.sourceName || dir != "/A/" {
			return
		}
		mu.Lock()
		scans++
		n := scans
		mu.Unlock()
		if n == 1 {
			panic("injected one-time scan failure")
		}
	})

	env.submit(DirectoryRefresh{Source: env.sourceName, Dir: "/A/", Refresh: true})
	env.waitIdle()

	if item := env.item("/A/new.jpg"); item == nil {
		t.Fatal("retry did not scan the directory; new.jpg is missing from the index")
	}
	mu.Lock()
	gotScans := scans
	mu.Unlock()
	if gotScans != 2 {
		t.Fatalf("scans = %d, want 2 (first attempt failed, retry must scan again)", gotScans)
	}
	stats := env.q.statsSnapshot()
	if stats.Failures != 1 || stats.Retries != 1 || stats.GivenUp != 0 || stats.Rounds != 2 {
		t.Fatalf("retry stats = %+v, want rounds=2 failures=1 retries=1 given_up=0", stats)
	}
}

// A real SQLite error in the delete phase must be retried with both the delete
// and the refresh (the RC1 review's real SQL failure probe, inverted).
func TestQueueRetryReplaysFailedDeletion(t *testing.T) {
	env := newQueueEnv(t)
	env.mkdir("A")
	env.write("A/moved.jpg", "old")
	env.refresh("/A/")
	env.remove("A/moved.jpg")
	env.write("A/new.jpg", "new")

	round := 0
	env.q.roundGate = func(refreshKey) {
		round++
		var err error
		switch round {
		case 1:
			_, err = env.db.Exec("ALTER TABLE index_items RENAME TO rc2_review_hidden_items")
		case 2:
			_, err = env.db.Exec("ALTER TABLE rc2_review_hidden_items RENAME TO index_items")
		}
		if err != nil {
			panic(fmt.Sprintf("round %d: %v", round, err))
		}
	}
	t.Cleanup(func() {
		_, _ = env.db.Exec("ALTER TABLE rc2_review_hidden_items RENAME TO index_items")
	})

	env.submit(DirectoryRefresh{
		Source:  env.sourceName,
		Dir:     "/A/",
		Refresh: true,
		Deletes: []PendingDelete{{Path: "/A/moved.jpg"}},
	})
	env.waitIdle()

	if item := env.item("/A/moved.jpg"); item != nil {
		t.Fatalf("failed deletion was not replayed; moved row still present: %#v", item)
	}
	if item := env.item("/A/new.jpg"); item == nil {
		t.Fatal("failed refresh was not replayed; new row is missing")
	}
	stats := env.q.statsSnapshot()
	if stats.Failures != 1 || stats.Retries != 1 || stats.GivenUp != 0 || stats.Rounds != 2 {
		t.Fatalf("retry stats = %+v, want rounds=2 failures=1 retries=1 given_up=0", stats)
	}
}

// New intents that arrive while a failed task waits for its retry must be
// merged with the failed work, including recursive and delete requirements.
func TestQueueRetryMergesNewIntent(t *testing.T) {
	env := newQueueEnv(t)
	env.mkdir("A")
	env.mkdir("A/Sub")
	env.write("A/old.jpg", "old")
	env.refresh("/A/")
	env.remove("A/old.jpg")
	env.write("A/Sub/deep.jpg", "deep")

	var once sync.Once
	failed := make(chan struct{})
	indexing.SetRefreshTestHookForTesting(func(stage, source, dir string) {
		if stage != "before_metadata" || source != env.sourceName || dir != "/A/" {
			return
		}
		once.Do(func() {
			close(failed)
			panic("injected one-time scan failure")
		})
	})

	env.submit(DirectoryRefresh{
		Source:  env.sourceName,
		Dir:     "/A/",
		Refresh: true,
		Deletes: []PendingDelete{{Path: "/A/old.jpg"}},
	})
	select {
	case <-failed:
	case <-time.After(5 * time.Second):
		t.Fatal("first scan did not fail")
	}
	env.waitForStats("first failure", func(s MaintenanceStats) bool { return s.Failures >= 1 })
	// Arrives during the retry backoff: it must join the failed task instead of
	// replacing its deletion/refresh work.
	env.submit(DirectoryRefresh{
		Source:    env.sourceName,
		Dir:       "/A/",
		Refresh:   true,
		Recursive: true,
		Deletes:   []PendingDelete{{Path: "/A/old.jpg"}},
	})
	env.waitIdle()

	if item := env.item("/A/old.jpg"); item != nil {
		t.Fatalf("merged retry lost the deletion: %#v", item)
	}
	if item := env.item("/A/Sub/deep.jpg"); item == nil {
		t.Fatal("merged recursive requirement was lost; deep row is missing")
	}
	stats := env.q.statsSnapshot()
	if stats.Failures != 1 || stats.Retries != 1 || stats.GivenUp != 0 {
		t.Fatalf("retry stats = %+v, want one failure, one retry, no give-up", stats)
	}
	if stats.Merged < 1 {
		t.Fatalf("Merged = %d, want at least 1", stats.Merged)
	}
}

// A new intent must not reset the retry budget of a permanently failing task.
// After the budget is exhausted the task is dropped visibly, and a later
// submission gets a fresh budget and can recover.
func TestQueueRetryBudgetIsNotResetByNewIntents(t *testing.T) {
	env := newQueueEnv(t)
	env.mkdir("A")
	env.write("A/x.jpg", "x")
	env.refresh("/A/")

	signals := make(chan struct{}, indexMaintenanceMaxAttempts+1)
	proceed := make(chan struct{})
	indexing.SetRefreshTestHookForTesting(func(stage, source, dir string) {
		if stage != "before_metadata" || source != env.sourceName || dir != "/A/" {
			return
		}
		signals <- struct{}{}
		<-proceed
		panic("injected permanent scan failure")
	})

	env.submit(DirectoryRefresh{Source: env.sourceName, Dir: "/A/", Refresh: true})
	for i := 0; i < indexMaintenanceMaxAttempts; i++ {
		select {
		case <-signals:
		case <-time.After(5 * time.Second):
			t.Fatalf("attempt %d did not run", i+1)
		}
		// Submit while the failing round is still open, so this intent is
		// merged into the failed work before the budget can be evaluated.
		env.submit(DirectoryRefresh{
			Source:  env.sourceName,
			Dir:     "/A/",
			Refresh: true,
			Deletes: []PendingDelete{{Path: "/A/never-existed.jpg"}},
		})
		proceed <- struct{}{}
	}
	env.waitIdle()

	stats := env.q.statsSnapshot()
	if stats.GivenUp != 1 {
		t.Fatalf("GivenUp = %d, want 1; stats=%+v", stats.GivenUp, stats)
	}
	if stats.Failures != indexMaintenanceMaxAttempts || stats.Rounds != indexMaintenanceMaxAttempts {
		t.Fatalf("retry budget was reset: failures=%d rounds=%d stats=%+v",
			stats.Failures, stats.Rounds, stats)
	}

	// A later submission starts a new task with a new budget.
	indexing.SetRefreshTestHookForTesting(nil)
	if err := env.db.DeleteItem(env.sourceName, "/A/x.jpg", false); err != nil {
		t.Fatal(err)
	}
	env.submit(DirectoryRefresh{Source: env.sourceName, Dir: "/A/", Refresh: true})
	env.waitIdle()
	if item := env.item("/A/x.jpg"); item == nil {
		t.Fatal("fresh task after give-up did not refresh the directory")
	}
	if stats := env.q.statsSnapshot(); stats.GivenUp != 1 {
		t.Fatalf("recovery changed GivenUp: %+v", stats)
	}
}

// A failed deep index must be replayed on retry, not silently skipped.
func TestQueueRetryReplaysDeepIndex(t *testing.T) {
	env := newQueueEnv(t)
	env.mkdir("A/Sub")
	env.write("A/Sub/deep.jpg", "deep")
	env.refresh("/A/")
	if err := env.db.DeleteItem(env.sourceName, "/A/Sub/deep.jpg", false); err != nil {
		t.Fatal(err)
	}

	var once sync.Once
	indexing.SetRefreshTestHookForTesting(func(stage, source, dir string) {
		if stage == "before_metadata" && source == env.sourceName && dir == "/A/Sub/" {
			once.Do(func() {
				panic("injected one-time deep index failure")
			})
		}
	})

	env.submit(DirectoryRefresh{
		Source: env.sourceName,
		Dir:    "/A/",
		Deep:   []string{"/A/Sub/"},
	})
	env.waitIdle()

	if item := env.item("/A/Sub/deep.jpg"); item == nil {
		t.Fatal("deep index was not replayed after a failure")
	}
	stats := env.q.statsSnapshot()
	if stats.Failures != 1 || stats.Retries != 1 || stats.GivenUp != 0 {
		t.Fatalf("deep retry stats = %+v", stats)
	}
}

// One Submit call with more directories than the queue capacity must be
// admitted incrementally with backpressure, never exceed the cap, and never
// drop a directory.
func TestQueueOneBatchRespectsCapacity(t *testing.T) {
	env := newQueueEnv(t)
	env.q.capacity = 2

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var once sync.Once
	env.q.roundGate = func(refreshKey) {
		once.Do(func() {
			started <- struct{}{}
			<-release
		})
	}

	impacts := make([]DirectoryRefresh, 0, 5)
	for i := 0; i < 5; i++ {
		dir := fmt.Sprintf("D%d", i)
		env.mkdir(dir)
		env.write(dir+"/f.jpg", "x")
		impacts = append(impacts, DirectoryRefresh{Source: env.sourceName, Dir: "/" + dir + "/", Refresh: true})
	}

	submitDone := make(chan error, 1)
	go func() { submitDone <- env.q.Submit(impacts) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	// Give the submitter time to reach the capacity wait, then inspect the
	// instantaneous bound. Capacity counts queued + running tasks.
	time.Sleep(100 * time.Millisecond)
	stats := env.q.statsSnapshot()
	if stats.Pending+stats.Active > env.q.capacity {
		t.Fatalf("live tasks = %d (pending=%d active=%d), capacity=%d",
			stats.Pending+stats.Active, stats.Pending, stats.Active, env.q.capacity)
	}
	if stats.PeakPending > env.q.capacity {
		t.Fatalf("PeakPending = %d exceeds capacity %d", stats.PeakPending, env.q.capacity)
	}

	close(release)
	select {
	case err := <-submitDone:
		if err != nil {
			t.Fatalf("batch submit: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("batch submit stayed blocked")
	}
	env.waitIdle()
	for i := 0; i < 5; i++ {
		dir := fmt.Sprintf("D%d", i)
		if item := env.item("/" + dir + "/"); item == nil {
			t.Fatalf("directory %s was dropped", dir)
		}
		if item := env.item("/" + dir + "/f.jpg"); item == nil {
			t.Fatalf("file in %s was not indexed", dir)
		}
	}
}

// When the queue is full, an intent for a directory that is already known must
// merge in place instead of blocking on a free slot.
func TestQueueFullQueueMergesSameKey(t *testing.T) {
	env := newQueueEnv(t)
	env.q.capacity = 1
	env.mkdir("A")
	env.write("A/f.jpg", "x")

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var once sync.Once
	env.q.roundGate = func(refreshKey) {
		once.Do(func() {
			started <- struct{}{}
			<-release
		})
	}

	env.submit(DirectoryRefresh{Source: env.sourceName, Dir: "/A/", Refresh: true})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}

	merged := make(chan error, 1)
	go func() {
		merged <- env.q.Submit([]DirectoryRefresh{{
			Source:  env.sourceName,
			Dir:     "/A/",
			Refresh: true,
			Deletes: []PendingDelete{{Path: "/A/ghost.jpg"}},
		}})
	}()
	select {
	case err := <-merged:
		if err != nil {
			t.Fatalf("same-key submit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("same-key submit blocked on capacity instead of merging")
	}
	if stats := env.q.statsSnapshot(); stats.Merged != 1 {
		t.Fatalf("Merged = %d, want 1", stats.Merged)
	}
	close(release)
	env.waitIdle()
}

// A shutdown deadline must not report a stopped queue while the worker is
// still inside a round, and the database must remain open for that worker.
func TestQueueShutdownDeadlineKeepsDatabaseOpen(t *testing.T) {
	env := newQueueEnv(t)
	env.q.abortGrace = 100 * time.Millisecond
	env.mkdir("A")
	env.write("A/x.jpg", "x")
	env.refresh("/A/")

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	indexing.SetRefreshTestHookForTesting(func(stage, source, dir string) {
		if stage == "before_metadata" && source == env.sourceName && dir == "/A/" {
			started <- struct{}{}
			<-release
		}
	})
	env.submit(DirectoryRefresh{Source: env.sourceName, Dir: "/A/", Refresh: true})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach the pre-write stage")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := env.q.Shutdown(ctx)
	if !errors.Is(err, ErrIndexMaintenanceBusy) {
		t.Fatalf("Shutdown error = %v, want ErrIndexMaintenanceBusy", err)
	}
	if stats := env.q.statsSnapshot(); stats.Active != 1 {
		t.Fatalf("Active = %d at shutdown return, want 1", stats.Active)
	}
	if err := env.db.DB().Ping(); err != nil {
		t.Fatalf("database was closed while the worker was alive: %v", err)
	}

	close(release)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	if err := env.q.Shutdown(waitCtx); err != nil {
		t.Fatalf("second shutdown after worker exit: %v", err)
	}
	if stats := env.q.statsSnapshot(); stats.Active != 0 {
		t.Fatalf("Active = %d after worker exit, want 0", stats.Active)
	}
	if err := env.db.DB().Ping(); err != nil {
		t.Fatalf("database was closed during a clean stop: %v", err)
	}
}

// A shutdown deadline with queued work must report the work it discarded.
func TestQueueShutdownDeadlineReportsDroppedWork(t *testing.T) {
	env := newQueueEnv(t)
	env.q.capacity = 2
	env.q.abortGrace = 100 * time.Millisecond
	env.mkdir("D0")
	env.mkdir("D1")
	env.write("D0/f.jpg", "x")
	env.write("D1/f.jpg", "x")

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var once sync.Once
	env.q.roundGate = func(refreshKey) {
		once.Do(func() {
			started <- struct{}{}
			<-release
		})
	}

	env.submit(
		DirectoryRefresh{Source: env.sourceName, Dir: "/D0/", Refresh: true},
		DirectoryRefresh{Source: env.sourceName, Dir: "/D1/", Refresh: true},
	)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	env.waitForStats("one pending directory", func(s MaintenanceStats) bool {
		return s.Pending == 1 && s.Active == 1
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := env.q.Shutdown(ctx)
	if !errors.Is(err, ErrIndexMaintenanceBusy) {
		t.Fatalf("Shutdown error = %v, want ErrIndexMaintenanceBusy", err)
	}
	stats := env.q.statsSnapshot()
	if stats.Dropped != 1 {
		t.Fatalf("Dropped = %d, want 1; stats=%+v", stats.Dropped, stats)
	}
	if stats.Pending != 0 {
		t.Fatalf("Pending = %d after abort, want 0", stats.Pending)
	}
	if err := env.q.Submit([]DirectoryRefresh{{Source: env.sourceName, Dir: "/D2/", Refresh: true}}); !errors.Is(err, errIndexMaintenanceClosed) {
		t.Fatalf("Submit after abort = %v, want errIndexMaintenanceClosed", err)
	}

	close(release)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	if err := env.q.Shutdown(waitCtx); err != nil {
		t.Fatalf("second shutdown after worker exit: %v", err)
	}
}

// A submitter blocked on capacity must be woken by shutdown and told that the
// queue is closing, instead of hanging until the process exits.
func TestQueueShutdownWakesBlockedSubmitters(t *testing.T) {
	env := newQueueEnv(t)
	env.q.capacity = 1
	env.q.abortGrace = 100 * time.Millisecond
	env.mkdir("D0")
	env.mkdir("D1")
	env.write("D0/f.jpg", "x")
	env.write("D1/f.jpg", "x")

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var once sync.Once
	env.q.roundGate = func(refreshKey) {
		once.Do(func() {
			started <- struct{}{}
			<-release
		})
	}

	env.submit(DirectoryRefresh{Source: env.sourceName, Dir: "/D0/", Refresh: true})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}

	blocked := make(chan error, 1)
	go func() {
		blocked <- env.q.Submit([]DirectoryRefresh{{Source: env.sourceName, Dir: "/D1/", Refresh: true}})
	}()
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := env.q.Shutdown(ctx); !errors.Is(err, ErrIndexMaintenanceBusy) {
		t.Fatalf("Shutdown error = %v, want ErrIndexMaintenanceBusy", err)
	}
	select {
	case err := <-blocked:
		if !errors.Is(err, errIndexMaintenanceClosed) {
			t.Fatalf("blocked Submit error = %v, want errIndexMaintenanceClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked submitter was not woken by shutdown")
	}

	close(release)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	if err := env.q.Shutdown(waitCtx); err != nil {
		t.Fatalf("second shutdown after worker exit: %v", err)
	}
}
