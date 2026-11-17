package files

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/utils"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/go-logger/logger"
)

// Index maintenance queue
//
// A successful move needs follow-up index work: the moved-away rows must be
// deleted, the destination tree of a moved directory must be indexed, and the
// affected parent directories must be re-scanned. Doing that once per file
// means hundreds of identical scans and SQLite write transactions for one
// batch. Instead, callers hand the work to this queue, which keeps at most one
// task per (source, directory) identity, runs a bounded number of tasks at a
// time, and re-runs a directory when new work arrives while it is scanning.
//
// Failure handling: a round owns the task's work until it succeeds. A failed
// round keeps that work (and merges work that arrived while the round was
// running) for the next attempt, so a retry never runs an empty task. One
// directory identity has one retry budget; new intents merged into a failing
// task do not reset it, which prevents a chatty directory from retrying a
// permanently broken write forever. When the budget is exhausted the whole
// task is dropped with an error log and a counter, including intents that
// arrived too late to get their own attempt. A later submission creates a new
// task with a fresh budget.

const (
	// indexMaintenanceWorkers is how many directories are maintained at once.
	// One worker is deliberate: a round is "read one directory + one SQLite
	// write transaction", and the index database serialises writers behind a
	// single mutex. Adding workers raises contention before it raises
	// throughput. Measurements decide whether a second worker is justified.
	indexMaintenanceWorkers = 1

	// indexMaintenancePendingCapacity bounds distinct live directory tasks:
	// queued, waiting for a retry, or currently running. New directory
	// identities are admitted one at a time and submitters block until a slot
	// frees up, so one wide batch cannot temporarily exceed the limit. An
	// intent for an already-known directory merges without taking a new slot.
	// A 267-file move touches a handful of directories, so this only matters
	// for pathologically wide batches.
	indexMaintenancePendingCapacity = 4096

	// indexMaintenanceMaxAttempts bounds retries of one directory task before
	// the queue gives up on it and records a visible error.
	indexMaintenanceMaxAttempts = 3

	// indexMaintenanceAbortGrace is how long shutdown waits for an aborted
	// worker to leave its current round before telling the caller that the
	// worker is still alive. Callers must not close the index database while
	// that is the case.
	indexMaintenanceAbortGrace = 2 * time.Second
)

var (
	errIndexMaintenanceClosed = errors.New("index maintenance is shutting down")

	// ErrIndexMaintenanceBusy reports that shutdown timed out and the worker is
	// still inside a round. The worker may still write to the index database,
	// so the caller must not close it, clear caches that the worker uses, or
	// treat the dropped work as applied.
	ErrIndexMaintenanceBusy = errors.New("index maintenance worker is still running")

	errIndexMaintenanceAborted = errors.New("index maintenance was aborted before all work completed")
	errIndexingDisabled        = errors.New("indexing is disabled for this source")
)

// PendingDelete is an index row (or subtree) that must be removed because the
// filesystem operation already moved it away.
type PendingDelete struct {
	Path      string
	IsDir     bool
	Recursive bool
}

// DirectoryRefresh is the index maintenance a successful file operation still
// needs for one directory identity: the source name plus the normalized index
// directory path. Directory paths always end with "/".
type DirectoryRefresh struct {
	Source    string
	Dir       string
	Refresh   bool
	Recursive bool
	Deep      []string
	Deletes   []PendingDelete
}

type refreshKey struct {
	source string
	dir    string
}

// directoryWork is the impact accumulated for one directory identity over one
// or more requests. The queue keeps it until a round succeeds.
type directoryWork struct {
	refresh   bool
	recursive bool
	deep      []string
	deletes   []PendingDelete
}

func (w *directoryWork) empty() bool {
	return !w.refresh && !w.recursive && len(w.deep) == 0 && len(w.deletes) == 0
}

func (w *directoryWork) clone() directoryWork {
	out := *w
	out.deep = append([]string(nil), w.deep...)
	out.deletes = append([]PendingDelete(nil), w.deletes...)
	return out
}

func (w *directoryWork) merge(impact DirectoryRefresh) {
	if impact.Refresh {
		w.refresh = true
	}
	if impact.Recursive {
		w.recursive = true
	}
	for _, deep := range impact.Deep {
		if deep == "" || containsString(w.deep, deep) {
			continue
		}
		w.deep = append(w.deep, deep)
	}
	for _, deletion := range impact.Deletes {
		if deletion.Path == "" {
			continue
		}
		merged := false
		for i := range w.deletes {
			if w.deletes[i].Path == deletion.Path && w.deletes[i].IsDir == deletion.IsDir {
				w.deletes[i].Recursive = w.deletes[i].Recursive || deletion.Recursive
				merged = true
				break
			}
		}
		if !merged {
			w.deletes = append(w.deletes, deletion)
		}
	}
}

func (w *directoryWork) mergeWork(other directoryWork) {
	w.merge(DirectoryRefresh{
		Refresh:   other.refresh,
		Recursive: other.recursive,
		Deep:      other.deep,
		Deletes:   other.deletes,
	})
}

// refreshTask is one directory identity. work is the outstanding impact. It is
// copied into the running round but stays on the task until that round
// succeeds; intents that arrive while a round runs are collected in incoming
// and either become the next round (on success) or are merged into the retry.
type refreshTask struct {
	key      refreshKey
	work     directoryWork
	incoming *directoryWork
	running  bool
	attempts int
	readyAt  time.Time
}

// MaintenanceStats is an aggregate snapshot of index maintenance work. It never
// contains paths or file names.
type MaintenanceStats struct {
	Submitted         int64
	Merged            int64
	TasksStarted      int64
	Rounds            int64
	DirtyReruns       int64
	Deletes           int64
	DeepIndexes       int64
	Refreshes         int64
	Failures          int64
	Retries           int64
	GivenUp           int64
	Dropped           int64
	SkippedDisabled   int64
	PeakPending       int
	PeakActive        int
	Active            int
	Pending           int
	AccessRuleUpdates int64
}

type refreshQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	pending map[refreshKey]*refreshTask
	order   []refreshKey
	running map[refreshKey]*refreshTask

	wake     chan struct{}
	idleWait chan struct{}

	started     bool
	closing     bool
	aborted     bool
	workersDone sync.WaitGroup

	// capacity bounds distinct live tasks (pending + running); tests may lower
	// it. The zero value is replaced with the default on construction.
	capacity int
	// abortGrace is the shutdown grace period after the deadline; tests may
	// shorten it.
	abortGrace time.Duration
	// roundGate, when set, runs before a round executes. It exists so tests can
	// hold a round open and submit new work at an exact moment.
	roundGate func(refreshKey)

	stats MaintenanceStats
}

var defaultRefreshQueue = newRefreshQueue()

func newRefreshQueue() *refreshQueue {
	q := &refreshQueue{
		pending:    make(map[refreshKey]*refreshTask),
		running:    make(map[refreshKey]*refreshTask),
		wake:       make(chan struct{}, 1),
		idleWait:   make(chan struct{}),
		capacity:   indexMaintenancePendingCapacity,
		abortGrace: indexMaintenanceAbortGrace,
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// normalizeRefreshDirKey canonicalises a directory identity. It preserves case
// and the source name, so "/A/" in two different sources stays distinct.
func normalizeRefreshDirKey(dir string) string {
	if dir == "" {
		return ""
	}
	return utils.AddTrailingSlashIfNotExists(dir)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// mergeLocked records an intent in the task. The task's retry budget is not
// reset: new intents are part of the same directory task, and letting every new
// event reset the budget would retry a permanently broken directory forever.
func (q *refreshQueue) mergeLocked(task *refreshTask, impact DirectoryRefresh) {
	if task.running {
		if task.incoming == nil {
			task.incoming = &directoryWork{}
		}
		task.incoming.merge(impact)
		return
	}
	task.work.merge(impact)
}

func (q *refreshQueue) liveLocked() int {
	return len(q.pending) + len(q.running)
}

// markBusyLocked resets the idle notification before a task is added so that
// WaitIdle cannot observe the queue as idle in the middle of a submission.
func (q *refreshQueue) markBusyLocked() {
	if q.liveLocked() == 0 {
		q.idleWait = make(chan struct{})
	}
}

// Submit accepts directory maintenance intents. Duplicate intents for the same
// directory are merged; a directory that is running is marked dirty so it gets
// another round. Capacity is checked per new directory identity while the lock
// is released during the wait, so a batch larger than the queue can be
// admitted without holding the worker back or silently dropping its tail.
func (q *refreshQueue) Submit(impacts []DirectoryRefresh) error {
	if len(impacts) == 0 {
		return nil
	}
	for _, impact := range impacts {
		dir := normalizeRefreshDirKey(impact.Dir)
		if impact.Source == "" || dir == "" {
			continue
		}
		key := refreshKey{source: impact.Source, dir: dir}
		// Invalidate direct directory refreshes before the deletion can run.
		// The parent's own rows are keyed exactly; a moved directory subtree
		// must also invalidate active GETs of its descendants, and a directory
		// move's destination subtree must invalidate the rows it replaces.
		indexing.BumpDirectoryWriteEpoch(impact.Source, dir)
		for _, deep := range impact.Deep {
			indexing.BumpDirectorySubtreeWriteEpoch(impact.Source, deep)
		}
		for _, deletion := range impact.Deletes {
			if deletion.IsDir {
				indexing.BumpDirectorySubtreeWriteEpoch(impact.Source, deletion.Path)
			}
		}

		q.mu.Lock()
		for {
			if q.closing {
				q.mu.Unlock()
				return errIndexMaintenanceClosed
			}
			if task, ok := q.running[key]; ok {
				q.markBusyLocked()
				q.mergeLocked(task, impact)
				q.stats.Submitted++
				q.stats.Merged++
				q.mu.Unlock()
				break
			}
			if task, ok := q.pending[key]; ok {
				q.markBusyLocked()
				q.mergeLocked(task, impact)
				q.stats.Submitted++
				q.stats.Merged++
				q.mu.Unlock()
				break
			}
			if q.liveLocked() < q.capacity {
				q.markBusyLocked()
				task := &refreshTask{key: key}
				task.work.merge(impact)
				q.pending[key] = task
				q.order = append(q.order, key)
				q.stats.Submitted++
				if len(q.pending) > q.stats.PeakPending {
					q.stats.PeakPending = len(q.pending)
				}
				q.ensureStartedLocked()
				q.mu.Unlock()
				break
			}
			// Backpressure: release the lock while waiting. The worker (or a
			// shutdown) broadcasts when a slot is free or the queue closes.
			q.cond.Wait()
		}
		q.signalWake()
	}
	return nil
}

func (q *refreshQueue) ensureStartedLocked() {
	if q.started || q.closing {
		return
	}
	q.started = true
	q.workersDone.Add(indexMaintenanceWorkers)
	for i := 0; i < indexMaintenanceWorkers; i++ {
		go q.worker()
	}
}

func (q *refreshQueue) signalWake() {
	q.mu.Lock()
	wake := q.wake
	q.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

type refreshSnapshot struct {
	task *refreshTask
	work directoryWork
}

// next hands the worker the first ready directory task. The task retains its
// work until finish reports success; incoming intents go to task.incoming. It
// returns done=true once the queue is closed and no pending work remains, or
// once shutdown has aborted the queue.
func (q *refreshQueue) next() (snapshot *refreshSnapshot, wait time.Duration, done bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.aborted {
		return nil, 0, true
	}
	if len(q.order) == 0 {
		if q.closing {
			return nil, 0, true
		}
		return nil, 0, false
	}
	now := time.Now()
	bestIndex := -1
	var earliest time.Time
	for i, key := range q.order {
		task := q.pending[key]
		if task == nil {
			continue
		}
		if task.readyAt.IsZero() || !task.readyAt.After(now) {
			bestIndex = i
			break
		}
		if earliest.IsZero() || task.readyAt.Before(earliest) {
			earliest = task.readyAt
		}
	}
	if bestIndex < 0 {
		return nil, time.Until(earliest), false
	}
	key := q.order[bestIndex]
	q.order = append(q.order[:bestIndex], q.order[bestIndex+1:]...)
	task := q.pending[key]
	delete(q.pending, key)

	task.running = true
	task.incoming = &directoryWork{}
	snapshot = &refreshSnapshot{task: task, work: task.work.clone()}
	q.running[key] = task
	q.stats.TasksStarted++
	q.stats.Active++
	if q.stats.Active > q.stats.PeakActive {
		q.stats.PeakActive = q.stats.Active
	}
	return snapshot, 0, false
}

func (q *refreshQueue) worker() {
	defer q.workersDone.Done()
	for {
		snapshot, wait, done := q.next()
		if done {
			return
		}
		if snapshot == nil {
			if wait <= 0 {
				<-q.wake
				continue
			}
			timer := time.NewTimer(wait)
			select {
			case <-q.wake:
			case <-timer.C:
			}
			timer.Stop()
			continue
		}
		if q.roundGate != nil {
			q.roundGate(snapshot.task.key)
		}
		err := runDirectoryRefresh(snapshot)
		q.finish(snapshot, err)
	}
}

func (q *refreshQueue) finish(snapshot *refreshSnapshot, err error) {
	task := snapshot.task
	q.mu.Lock()
	delete(q.running, task.key)
	task.running = false
	incoming := task.incoming
	task.incoming = nil
	if q.stats.Active > 0 {
		q.stats.Active--
	}
	q.stats.Rounds++
	if snapshot.work.refresh {
		q.stats.Refreshes++
	}
	q.stats.Deletes += int64(len(snapshot.work.deletes))
	q.stats.DeepIndexes += int64(len(snapshot.work.deep))

	switch {
	case errors.Is(err, errIndexingDisabled):
		q.stats.SkippedDisabled++
		task.attempts = 0
		task.work = directoryWork{}
		if incoming != nil && !incoming.empty() {
			task.work = *incoming
			q.stats.DirtyReruns++
			q.requeueLocked(task)
		}
	case err != nil:
		q.stats.Failures++
		task.attempts++
		// Work that arrived during the failed round is part of the retry, not
		// a replacement for the work that failed.
		if incoming != nil {
			task.work.mergeWork(*incoming)
		}
		if task.attempts >= indexMaintenanceMaxAttempts {
			q.stats.GivenUp++
			logger.Errorf("index maintenance gave up on source %q directory %q after %d attempts: %v",
				task.key.source, task.key.dir, task.attempts, err)
			task.work = directoryWork{}
		} else {
			q.stats.Retries++
			task.readyAt = time.Now().Add(retryBackoff(task.attempts))
			logger.Warningf("index maintenance retry %d/%d for source %q directory %q: %v",
				task.attempts, indexMaintenanceMaxAttempts, task.key.source, task.key.dir, err)
			q.requeueLocked(task)
		}
	default:
		// The round applied the whole task. Only work that arrived while it was
		// running remains, and it deserves an immediate second round.
		task.attempts = 0
		task.work = directoryWork{}
		if incoming != nil && !incoming.empty() {
			task.work = *incoming
			q.stats.DirtyReruns++
			q.requeueLocked(task)
		}
	}
	if task.work.empty() {
		task.attempts = 0
		task.readyAt = time.Time{}
	}
	q.maybeIdleLocked()
	q.cond.Broadcast()
	q.mu.Unlock()
	q.signalWake()
}

func retryBackoff(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 250 * time.Millisecond
	case 2:
		return time.Second
	default:
		return 2 * time.Second
	}
}

func (q *refreshQueue) requeueLocked(task *refreshTask) {
	if q.aborted {
		// Shutdown already discarded the queue while this round was running.
		// Re-adding the task would leave work that no worker will ever pick up.
		q.stats.Dropped++
		logger.Warningf("discarding index maintenance for source %q directory %q after shutdown abort",
			task.key.source, task.key.dir)
		return
	}
	if _, queued := q.pending[task.key]; queued {
		return
	}
	q.pending[task.key] = task
	q.order = append(q.order, task.key)
	if len(q.pending) > q.stats.PeakPending {
		q.stats.PeakPending = len(q.pending)
	}
}

func (q *refreshQueue) maybeIdleLocked() {
	if len(q.pending) != 0 || len(q.running) != 0 {
		return
	}
	close(q.idleWait)
	q.idleWait = make(chan struct{})
}

func (q *refreshQueue) isIdleLocked() bool {
	return len(q.pending) == 0 && len(q.running) == 0
}

// WaitIdle blocks until no maintenance task is queued or running.
func (q *refreshQueue) WaitIdle(ctx context.Context) error {
	for {
		q.mu.Lock()
		if q.isIdleLocked() {
			q.mu.Unlock()
			return nil
		}
		wait := q.idleWait
		q.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Shutdown stops accepting work and drains queued work. When ctx expires it
// aborts the queue: queued work is discarded with a log, the worker is asked to
// stop after its current round, and the result distinguishes "worker stopped"
// (safe to close the database) from ErrIndexMaintenanceBusy (the worker is
// still alive and must not have its database closed underneath it). A graceful
// shutdown leaves the queue restartable; an aborted one stays closed.
func (q *refreshQueue) Shutdown(ctx context.Context) error {
	q.mu.Lock()
	wasAborted := q.aborted
	if !q.closing {
		q.closing = true
	}
	q.cond.Broadcast()
	q.mu.Unlock()
	q.signalWake()

	done := make(chan struct{})
	go func() {
		q.workersDone.Wait()
		close(done)
	}()

	if !wasAborted {
		select {
		case <-done:
			q.mu.Lock()
			q.started = false
			q.closing = false
			q.aborted = false
			q.pending = make(map[refreshKey]*refreshTask)
			q.order = nil
			q.running = make(map[refreshKey]*refreshTask)
			q.wake = make(chan struct{}, 1)
			q.idleWait = make(chan struct{})
			q.mu.Unlock()
			return nil
		case <-ctx.Done():
		}
	} else {
		// A previous shutdown already aborted the queue. Just wait for the
		// worker to leave its round; the database may only be closed then.
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return fmt.Errorf("%w: worker still running after an earlier abort", ErrIndexMaintenanceBusy)
		}
	}

	q.mu.Lock()
	q.aborted = true
	dropped := len(q.pending)
	for _, key := range q.order {
		if task := q.pending[key]; task != nil {
			logger.Warningf("dropping queued index maintenance for source %q directory %q during shutdown",
				task.key.source, task.key.dir)
		}
	}
	q.pending = make(map[refreshKey]*refreshTask)
	q.order = nil
	q.stats.Dropped += int64(dropped)
	q.cond.Broadcast()
	q.mu.Unlock()
	q.signalWake()

	timer := time.NewTimer(q.abortGrace)
	defer timer.Stop()
	select {
	case <-done:
		return fmt.Errorf("%w: %d queued directories discarded", errIndexMaintenanceAborted, dropped)
	case <-timer.C:
		return fmt.Errorf("%w: %d queued directories discarded, worker still inside a round", ErrIndexMaintenanceBusy, dropped)
	}
}

func (q *refreshQueue) statsSnapshot() MaintenanceStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	stats := q.stats
	stats.Pending = len(q.pending)
	stats.Active = len(q.running)
	return stats
}

// runDirectoryRefresh performs one round for one directory. Deletions run
// before the scan: a scan started after a move must not resurrect the row it
// moved away, and a scan that started earlier is corrected by the next round
// (the task stays dirty).
func runDirectoryRefresh(snapshot *refreshSnapshot) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("index maintenance panic: %v", recovered)
		}
	}()
	idx := indexing.GetIndex(snapshot.task.key.source)
	if idx == nil {
		return fmt.Errorf("index for source %q is not registered", snapshot.task.key.source)
	}
	if idx.Config.ResolvedRules.IndexingDisabled {
		return errIndexingDisabled
	}
	for _, deletion := range snapshot.work.deletes {
		if !idx.DeleteMetadata(deletion.Path, deletion.IsDir, deletion.Recursive) {
			return fmt.Errorf("could not delete index metadata for %s", deletion.Path)
		}
	}
	for _, deep := range snapshot.work.deep {
		if err := RefreshIndex(snapshot.task.key.source, deep, true, true); err != nil {
			return fmt.Errorf("could not index moved directory %s: %w", deep, err)
		}
	}
	if snapshot.work.refresh {
		if err := RefreshIndex(snapshot.task.key.source, snapshot.task.key.dir, true, snapshot.work.recursive); err != nil {
			return fmt.Errorf("could not refresh directory %s: %w", snapshot.task.key.dir, err)
		}
	}
	return nil
}

// SubmitIndexMaintenance queues index work from a successful file operation.
// It never uses the HTTP request context.
func SubmitIndexMaintenance(impacts []DirectoryRefresh) error {
	return defaultRefreshQueue.Submit(impacts)
}

// WaitForIndexMaintenance blocks until the queue has no queued or running work.
func WaitForIndexMaintenance(ctx context.Context) error {
	return defaultRefreshQueue.WaitIdle(ctx)
}

// StopIndexMaintenance drains and stops the queue. The application calls it
// after the HTTP server has finished its handlers and before the index database
// is closed. A returned error other than ErrIndexMaintenanceBusy means the
// worker has stopped; ErrIndexMaintenanceBusy means it has not, and the caller
// must leave the database open.
func StopIndexMaintenance(ctx context.Context) error {
	return defaultRefreshQueue.Shutdown(ctx)
}

// IndexMaintenanceStats returns aggregate counters for the maintenance queue.
func IndexMaintenanceStats() MaintenanceStats {
	stats := defaultRefreshQueue.statsSnapshot()
	stats.AccessRuleUpdates = IndexWorkProbeSnapshot().AccessRuleUpdates
	return stats
}
