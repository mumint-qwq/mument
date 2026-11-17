package files

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	indexsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

type queueEnv struct {
	t          *testing.T
	q          *refreshQueue
	idx        *indexing.Index
	db         *indexsql.IndexDB
	root       string
	sourcePath string
	sourceName string
}

func newQueueEnv(t *testing.T) *queueEnv {
	t.Helper()
	prevFilePerm := fileutils.PermFile
	prevDirPerm := fileutils.PermDir
	prevCacheDir := settings.Config.Server.CacheDir
	fileutils.SetFsPermissions(0o644, 0o755)
	root := t.TempDir()
	env := &queueEnv{
		t:          t,
		q:          newRefreshQueue(),
		root:       root,
		sourcePath: filepath.Join(root, "src"),
		sourceName: "queue-test",
	}
	cacheDir := filepath.Join(root, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(env.sourcePath, 0o755); err != nil {
		t.Fatal(err)
	}
	settings.Config.Server.CacheDir = cacheDir
	db, _, err := indexsql.NewIndexDB("queue", "OFF", 1000, 32, true)
	if err != nil {
		t.Fatalf("index db: %v", err)
	}
	env.db = db
	indexing.SetIndexDBForTesting(db)
	env.idx = indexing.SetTestIndexWithDB(env.sourceName, env.sourcePath, db)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := env.q.Shutdown(ctx); err != nil {
			t.Logf("queue shutdown: %v", err)
		}
		cancel()
		indexing.SetRefreshTestHookForTesting(nil)
		indexing.SetRefreshProbeEnabled(false)
		indexing.ClearTestIndices()
		indexing.SetIndexDBForTesting(nil)
		_ = db.Close()
		fileutils.SetFsPermissions(uint32(prevFilePerm), uint32(prevDirPerm))
		settings.Config.Server.CacheDir = prevCacheDir
	})
	return env
}

func (e *queueEnv) mkdir(rel string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Join(e.sourcePath, rel), 0o755); err != nil {
		e.t.Fatal(err)
	}
}

func (e *queueEnv) write(rel, content string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.sourcePath, rel), []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *queueEnv) remove(rel string) {
	e.t.Helper()
	if err := os.Remove(filepath.Join(e.sourcePath, rel)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *queueEnv) refresh(dir string) {
	e.t.Helper()
	if err := e.idx.RefreshDirectory(dir, false); err != nil {
		e.t.Fatalf("initial index %s: %v", dir, err)
	}
}

func (e *queueEnv) waitIdle() {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := e.q.WaitIdle(ctx); err != nil {
		e.t.Fatalf("queue did not become idle: %v", err)
	}
}

func (e *queueEnv) item(path string) *iteminfo.FileInfo {
	e.t.Helper()
	item, err := e.db.GetItem(e.sourceName, path)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		e.t.Fatalf("get item %s: %v", path, err)
	}
	return item
}

func (e *queueEnv) submit(impacts ...DirectoryRefresh) {
	e.t.Helper()
	if err := e.q.Submit(impacts); err != nil {
		e.t.Fatalf("submit: %v", err)
	}
}

// B08/B11: work submitted while a directory is scanning must not be lost, and
// the next round must remove the row that the first scan re-inserted.
func TestQueueMergesWorkSubmittedDuringScan(t *testing.T) {
	env := newQueueEnv(t)
	env.mkdir("A")
	env.write("A/x.jpg", "x")
	env.write("A/y.jpg", "y")
	env.refresh("/A/")

	scanned := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	env.q.roundGate = func(refreshKey) {
		once.Do(func() {
			scanned <- struct{}{}
			<-release
		})
	}
	env.submit(DirectoryRefresh{Source: env.sourceName, Dir: "/A/", Refresh: true})
	select {
	case <-scanned:
	case <-time.After(5 * time.Second):
		t.Fatal("first round did not start")
	}

	// The second batch removes x while the first scan is still open.
	env.remove("A/x.jpg")
	env.submit(DirectoryRefresh{
		Source:  env.sourceName,
		Dir:     "/A/",
		Refresh: true,
		Deletes: []PendingDelete{{Path: "/A/x.jpg"}},
	})
	close(release)
	env.waitIdle()

	if item := env.item("/A/x.jpg"); item != nil {
		t.Fatalf("late scan resurrected a moved-away row: %#v", item)
	}
	if item := env.item("/A/y.jpg"); item == nil {
		t.Fatal("unmoved row is missing")
	}
	stats := env.q.statsSnapshot()
	if stats.Merged != 1 {
		t.Fatalf("Merged = %d, want 1", stats.Merged)
	}
	if stats.DirtyReruns < 1 {
		t.Fatalf("DirtyReruns = %d, want at least 1", stats.DirtyReruns)
	}
	if stats.Rounds < 2 {
		t.Fatalf("Rounds = %d, want at least 2", stats.Rounds)
	}
	if stats.PeakActive != 1 || stats.Active != 0 {
		t.Fatalf("active tasks: peak=%d now=%d, want 1/0", stats.PeakActive, stats.Active)
	}
}

// B11: the exact late-snapshot race. A scan reads directory A (including x),
// another batch moves x out and deletes its row, then the old scan writes its
// snapshot back. The next round must delete the resurrected row again.
func TestLateScanSnapshotCannotResurrectMovedRow(t *testing.T) {
	env := newQueueEnv(t)
	env.mkdir("A")
	env.write("A/x.jpg", "x")
	env.write("A/y.jpg", "y")
	env.refresh("/A/")

	scanned := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	indexing.SetRefreshTestHookForTesting(func(stage, source, indexPath string) {
		if stage != "before_metadata" || source != env.sourceName || indexPath != "/A/" {
			return
		}
		once.Do(func() {
			scanned <- struct{}{}
			<-release
		})
	})
	env.submit(DirectoryRefresh{Source: env.sourceName, Dir: "/A/", Refresh: true})
	select {
	case <-scanned:
	case <-time.After(5 * time.Second):
		t.Fatal("scan did not reach the pre-write stage")
	}
	// The snapshot in memory still contains x; move it away and record the
	// deletion while the scan is paused.
	env.remove("A/x.jpg")
	env.submit(DirectoryRefresh{
		Source:  env.sourceName,
		Dir:     "/A/",
		Refresh: true,
		Deletes: []PendingDelete{{Path: "/A/x.jpg"}},
	})
	close(release)
	env.waitIdle()
	if item := env.item("/A/x.jpg"); item != nil {
		t.Fatalf("late snapshot left a moved-away row: %#v", item)
	}
	if stats := env.q.statsSnapshot(); stats.DirtyReruns < 1 {
		t.Fatalf("DirtyReruns = %d, want at least 1", stats.DirtyReruns)
	}
}

// B09: a quiet queue must not rescan on its own.
func TestQueueDoesNotRescanWhenQuiet(t *testing.T) {
	env := newQueueEnv(t)
	env.mkdir("A")
	env.write("A/x.jpg", "x")
	env.refresh("/A/")
	env.submit(DirectoryRefresh{Source: env.sourceName, Dir: "/A/", Refresh: true})
	env.waitIdle()
	first := env.q.statsSnapshot()
	time.Sleep(150 * time.Millisecond)
	second := env.q.statsSnapshot()
	if second.Rounds != first.Rounds {
		t.Fatalf("quiet queue ran extra rounds: %d -> %d", first.Rounds, second.Rounds)
	}
	if second.Rounds != 1 {
		t.Fatalf("Rounds = %d, want 1", second.Rounds)
	}
}

// B10: a burst of submissions racing with the worker must not lose deletions
// and must not overlap scans of the same directory.
func TestQueueBurstKeepsEveryDelete(t *testing.T) {
	env := newQueueEnv(t)
	env.mkdir("A")
	const files = 20
	for i := 0; i < files; i++ {
		env.write(fmt.Sprintf("A/f-%02d.jpg", i), "x")
	}
	env.refresh("/A/")

	env.submit(DirectoryRefresh{Source: env.sourceName, Dir: "/A/", Refresh: true})
	for i := 0; i < files; i++ {
		name := fmt.Sprintf("A/f-%02d.jpg", i)
		env.remove(name)
		env.submit(DirectoryRefresh{
			Source:  env.sourceName,
			Dir:     "/A/",
			Refresh: true,
			Deletes: []PendingDelete{{Path: "/" + name}},
		})
	}
	env.waitIdle()

	children, err := env.db.GetDirectoryChildren(env.sourceName, "/A/")
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 0 {
		t.Fatalf("stale rows after burst: %d", len(children))
	}
	stats := env.q.statsSnapshot()
	if stats.Merged != files {
		t.Fatalf("Merged = %d, want %d", stats.Merged, files)
	}
	if stats.PeakActive != 1 {
		t.Fatalf("PeakActive = %d, want 1", stats.PeakActive)
	}
}

// B14: the pending set is bounded, submissions block instead of being dropped,
// and every directory is still processed.
func TestQueueBackpressureKeepsEveryDirectory(t *testing.T) {
	env := newQueueEnv(t)
	env.q.capacity = 2
	release := make(chan struct{})
	var gateOnce sync.Once
	env.q.roundGate = func(refreshKey) {
		gateOnce.Do(func() {
			<-release
		})
	}
	for i := 0; i < 5; i++ {
		env.mkdir(fmt.Sprintf("D%d", i))
		env.write(fmt.Sprintf("D%d/f.jpg", i), "x")
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			_ = env.q.Submit([]DirectoryRefresh{{Source: env.sourceName, Dir: fmt.Sprintf("/D%d/", i), Refresh: true}})
		}
	}()
	time.Sleep(100 * time.Millisecond)
	if stats := env.q.statsSnapshot(); stats.PeakPending > 2 {
		t.Fatalf("PeakPending = %d exceeds capacity 2", stats.PeakPending)
	}
	close(release)
	submitted := make(chan struct{})
	go func() {
		wg.Wait()
		close(submitted)
	}()
	select {
	case <-submitted:
	case <-time.After(10 * time.Second):
		t.Fatal("submitters stayed blocked")
	}
	env.waitIdle()
	for i := 0; i < 5; i++ {
		if item := env.item(fmt.Sprintf("/D%d/", i)); item == nil {
			t.Fatalf("directory D%d was dropped", i)
		}
		if row := env.item(fmt.Sprintf("/D%d/f.jpg", i)); row == nil {
			t.Fatalf("file in D%d was not indexed", i)
		}
	}
	if stats := env.q.statsSnapshot(); stats.GivenUp != 0 || stats.Failures != 0 {
		t.Fatalf("unexpected failures: %+v", stats)
	}
}

// B15: a permanently failing directory retries a bounded number of times and
// records an error, while other directories keep being processed.
func TestQueueGivesUpWithoutBlockingOtherDirectories(t *testing.T) {
	env := newQueueEnv(t)
	env.mkdir("A")
	env.write("A/x.jpg", "x")
	env.refresh("/A/")
	indexing.SetRefreshProbeEnabled(true)
	env.submit(
		DirectoryRefresh{Source: "does-not-exist", Dir: "/A/", Refresh: true},
		DirectoryRefresh{Source: env.sourceName, Dir: "/A/", Refresh: true},
	)
	env.waitIdle()

	stats := env.q.statsSnapshot()
	if stats.GivenUp != 1 {
		t.Fatalf("GivenUp = %d, want 1", stats.GivenUp)
	}
	if stats.Failures != indexMaintenanceMaxAttempts {
		t.Fatalf("Failures = %d, want %d", stats.Failures, indexMaintenanceMaxAttempts)
	}
	if stats.Retries != indexMaintenanceMaxAttempts-1 {
		t.Fatalf("Retries = %d, want %d", stats.Retries, indexMaintenanceMaxAttempts-1)
	}
	probe := indexing.RefreshProbeSnapshot()
	if probe.ScanStarted != 1 {
		t.Fatalf("valid directory scans = %d, want 1", probe.ScanStarted)
	}
	if item := env.item("/A/x.jpg"); item == nil {
		t.Fatal("valid directory was not refreshed")
	}
}

// B15: a directory that no longer exists follows the existing refresh
// semantics: the round completes without error and is not retried forever.
// Removing the row itself is the job of the explicit source deletion recorded
// by a directory move, not of the refresh.
func TestQueueDeletedDirectoryFinishes(t *testing.T) {
	env := newQueueEnv(t)
	env.mkdir("Gone")
	env.write("Gone/x.jpg", "x")
	env.refresh("/Gone/")
	if err := os.RemoveAll(filepath.Join(env.sourcePath, "Gone")); err != nil {
		t.Fatal(err)
	}
	env.submit(DirectoryRefresh{Source: env.sourceName, Dir: "/Gone/", Refresh: true})
	env.waitIdle()
	stats := env.q.statsSnapshot()
	if stats.GivenUp != 0 || stats.Failures != 0 {
		t.Fatalf("deleted directory should finish cleanly: %+v", stats)
	}
	if stats.Rounds != 1 {
		t.Fatalf("deleted directory rounds = %d, want 1", stats.Rounds)
	}
}

// B13: a recursive requirement must not be downgraded by a later shallow one.
func TestQueueRecursiveRequirementIsNotDowngraded(t *testing.T) {
	env := newQueueEnv(t)
	env.mkdir("A/Sub/nested")
	env.write("A/Sub/nested/deep.jpg", "x")
	env.write("A/Sub/top.jpg", "x")
	env.refresh("/A/Sub/")
	// Remove the nested rows to prove the recursive scan is what adds them back.
	if err := env.db.DeleteItem(env.sourceName, "/A/Sub/nested/", true); err != nil {
		t.Fatal(err)
	}
	blocks := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	env.q.roundGate = func(refreshKey) {
		once.Do(func() {
			blocks <- struct{}{}
			<-release
		})
	}
	env.submit(DirectoryRefresh{Source: env.sourceName, Dir: "/A/Sub/", Refresh: true, Recursive: true})
	select {
	case <-blocks:
	case <-time.After(5 * time.Second):
		t.Fatal("round did not start")
	}
	env.submit(DirectoryRefresh{Source: env.sourceName, Dir: "/A/Sub", Refresh: true})
	close(release)
	env.waitIdle()
	if item := env.item("/A/Sub/nested/deep.jpg"); item == nil {
		t.Fatal("recursive requirement was downgraded; nested row missing")
	}
	if item := env.item("/A/Sub/top.jpg"); item == nil {
		t.Fatal("shallow file missing")
	}
}

// B17: shutdown drains, the queue can be restarted, and shutting down twice is
// safe.
func TestQueueShutdownDrainsAndRestarts(t *testing.T) {
	env := newQueueEnv(t)
	env.mkdir("A")
	env.write("A/x.jpg", "x")
	env.refresh("/A/")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := env.q.Shutdown(ctx); err != nil {
		t.Fatalf("idle shutdown: %v", err)
	}
	env.submit(DirectoryRefresh{Source: env.sourceName, Dir: "/A/", Refresh: true})
	env.waitIdle()
	if err := env.q.Shutdown(ctx); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
}

// B17: shutdown waits for a running round, and a closed database does not
// panic the worker.
func TestQueueShutdownWaitsAndClosedDBIsSafe(t *testing.T) {
	env := newQueueEnv(t)
	env.mkdir("A")
	env.write("A/x.jpg", "x")
	env.refresh("/A/")
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
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- env.q.Shutdown(ctx) }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	// The database is closed underneath a still-registered index; the worker
	// must record a failure instead of panicking or hanging.
	if err := env.db.Close(); err != nil {
		t.Fatal(err)
	}
	env.submit(DirectoryRefresh{
		Source:  env.sourceName,
		Dir:     "/A/",
		Refresh: true,
		Deletes: []PendingDelete{{Path: "/A/x.jpg"}},
	})
	env.waitIdle()
	stats := env.q.statsSnapshot()
	if stats.GivenUp == 0 && stats.Failures == 0 {
		t.Fatalf("closed database failure was not recorded: %+v", stats)
	}
}

// B12: A -> B -> C before background work catches up ends with the file only in
// C and no stale rows in A or B.
func TestMoveChainConvergesToDestination(t *testing.T) {
	env := newQueueEnv(t)
	env.mkdir("A")
	env.mkdir("B")
	env.mkdir("C")
	env.write("A/x.jpg", "x")
	env.refresh("/A/")

	batch1 := NewIndexImpactBatch()
	if err := moveResourceImpl(false, env.sourceName, env.sourceName,
		filepath.Join(env.sourcePath, "A/x.jpg"), filepath.Join(env.sourcePath, "B/x.jpg"), svc(), batch1); err != nil {
		t.Fatal(err)
	}
	batch1.Submit()
	batch2 := NewIndexImpactBatch()
	if err := moveResourceImpl(false, env.sourceName, env.sourceName,
		filepath.Join(env.sourcePath, "B/x.jpg"), filepath.Join(env.sourcePath, "C/x.jpg"), svc(), batch2); err != nil {
		t.Fatal(err)
	}
	batch2.Submit()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := WaitForIndexMaintenance(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(env.sourcePath, "C/x.jpg")); err != nil {
		t.Fatalf("file is not in C: %v", err)
	}
	for _, path := range []string{"/A/x.jpg", "/B/x.jpg"} {
		if item := env.item(path); item != nil {
			t.Fatalf("stale row at %s: %#v", path, item)
		}
	}
	if item := env.item("/C/x.jpg"); item == nil {
		t.Fatal("destination row missing")
	}
}
