package files

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
)

// A real SQLite metadata write failure must reach the queue, so the round is a
// failure and the work is retried. RC2 ignored UpdateMetadata's false result
// and counted the round as a success.
func TestQueueRetryReplaysMetadataWriteFailure(t *testing.T) {
	env := newQueueEnv(t)
	env.mkdir("A")
	env.refresh("/A/")
	env.write("A/new.jpg", "synthetic")

	renamed := false
	indexing.SetRefreshTestHookForTesting(func(stage, source, dir string) {
		if stage == "before_metadata" && source == env.sourceName && dir == "/A/" && !renamed {
			renamed = true
			if _, err := env.db.Exec("ALTER TABLE index_items RENAME TO rc3_hidden_items"); err != nil {
				panic(err)
			}
		}
	})
	t.Cleanup(func() {
		_, _ = env.db.Exec("ALTER TABLE rc3_hidden_items RENAME TO index_items")
	})

	round := 0
	env.q.roundGate = func(refreshKey) {
		round++
		if round == 2 {
			if _, err := env.db.Exec("ALTER TABLE rc3_hidden_items RENAME TO index_items"); err != nil {
				panic(fmt.Sprintf("restore metadata table: %v", err))
			}
		}
	}

	env.submit(DirectoryRefresh{Source: env.sourceName, Dir: "/A/", Refresh: true})
	env.waitIdle()

	if item := env.item("/A/new.jpg"); item == nil {
		t.Fatal("metadata write failure was not retried; new row is missing")
	}
	stats := env.q.statsSnapshot()
	if stats.Failures != 1 || stats.Retries != 1 || stats.GivenUp != 0 || stats.Rounds != 2 {
		t.Fatalf("retry stats = %+v, want rounds=2 failures=1 retries=1 given_up=0", stats)
	}
}

// A persistent metadata write failure must be bounded and visible, then a later
// submission must be able to recover.
func TestQueueMetadataWriteFailureGivesUpAndRecovers(t *testing.T) {
	env := newQueueEnv(t)
	env.mkdir("A")
	env.refresh("/A/")
	env.write("A/new.jpg", "synthetic")

	indexing.SetRefreshTestHookForTesting(func(stage, source, dir string) {
		if stage == "before_metadata" && source == env.sourceName && dir == "/A/" {
			_, _ = env.db.Exec("ALTER TABLE index_items RENAME TO rc3_hidden_items_persistent")
		}
	})
	t.Cleanup(func() {
		_, _ = env.db.Exec("ALTER TABLE rc3_hidden_items_persistent RENAME TO index_items")
	})

	env.submit(DirectoryRefresh{Source: env.sourceName, Dir: "/A/", Refresh: true})
	env.waitIdle()
	stats := env.q.statsSnapshot()
	if stats.GivenUp != 1 {
		t.Fatalf("GivenUp = %d, want 1; stats=%+v", stats.GivenUp, stats)
	}
	if stats.Failures != indexMaintenanceMaxAttempts || stats.Rounds != indexMaintenanceMaxAttempts {
		t.Fatalf("persistent failure stats = %+v, want %d failures/rounds", stats, indexMaintenanceMaxAttempts)
	}
	// Restore the table and confirm the failed writes left no row behind.
	if _, err := env.db.Exec("ALTER TABLE rc3_hidden_items_persistent RENAME TO index_items"); err != nil {
		t.Fatal(err)
	}
	if item := env.item("/A/new.jpg"); item != nil {
		t.Fatalf("row exists despite failed writes: %#v", item)
	}

	// Submit a new task; it must get a fresh budget.
	indexing.SetRefreshTestHookForTesting(nil)
	env.submit(DirectoryRefresh{Source: env.sourceName, Dir: "/A/", Refresh: true})
	env.waitIdle()
	if item := env.item("/A/new.jpg"); item == nil {
		t.Fatal("fresh task after give-up did not write the recovered row")
	}
}

// A failure while indexing a deep subdirectory must propagate out of the
// recursive scan, not be logged and skipped as a successful parent round.
func TestQueueDeepSubdirMetadataFailureIsRetried(t *testing.T) {
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
				if _, err := env.db.Exec("ALTER TABLE index_items RENAME TO rc3_hidden_deep_items"); err != nil {
					panic(err)
				}
			})
		}
	})
	t.Cleanup(func() {
		_, _ = env.db.Exec("ALTER TABLE rc3_hidden_deep_items RENAME TO index_items")
	})

	round := 0
	env.q.roundGate = func(refreshKey) {
		round++
		if round == 2 {
			if _, err := env.db.Exec("ALTER TABLE rc3_hidden_deep_items RENAME TO index_items"); err != nil {
				panic(fmt.Sprintf("restore deep metadata table: %v", err))
			}
		}
	}

	env.submit(DirectoryRefresh{Source: env.sourceName, Dir: "/A/", Deep: []string{"/A/Sub/"}})
	env.waitIdle()

	if item := env.item("/A/Sub/deep.jpg"); item == nil {
		t.Fatal("deep subdirectory write failure was swallowed; row is missing after retry")
	}
	stats := env.q.statsSnapshot()
	if stats.Failures != 1 || stats.Retries != 1 || stats.GivenUp != 0 {
		t.Fatalf("deep retry stats = %+v", stats)
	}
}

// A metadata failure must merge intents that arrive during the retry window,
// including a new deletion and a recursive requirement.
func TestQueueMetadataFailureMergesNewIntent(t *testing.T) {
	env := newQueueEnv(t)
	env.mkdir("A")
	env.mkdir("A/Sub")
	env.write("A/old.jpg", "old")
	env.refresh("/A/")
	env.remove("A/old.jpg")
	env.write("A/Sub/deep.jpg", "deep")

	failed := make(chan struct{})
	var once sync.Once
	indexing.SetRefreshTestHookForTesting(func(stage, source, dir string) {
		if stage == "before_metadata" && source == env.sourceName && dir == "/A/" {
			once.Do(func() {
				close(failed)
				if _, err := env.db.Exec("ALTER TABLE index_items RENAME TO rc3_hidden_merge_items"); err != nil {
					panic(err)
				}
			})
		}
	})
	t.Cleanup(func() {
		_, _ = env.db.Exec("ALTER TABLE rc3_hidden_merge_items RENAME TO index_items")
	})

	round := 0
	env.q.roundGate = func(refreshKey) {
		round++
		if round == 2 {
			if _, err := env.db.Exec("ALTER TABLE rc3_hidden_merge_items RENAME TO index_items"); err != nil {
				panic(fmt.Sprintf("restore merge metadata table: %v", err))
			}
		}
	}

	env.submit(DirectoryRefresh{
		Source:  env.sourceName,
		Dir:     "/A/",
		Refresh: true,
		Deletes: []PendingDelete{{Path: "/A/old.jpg"}},
	})
	select {
	case <-failed:
	case <-time.After(5 * time.Second):
		t.Fatal("metadata failure was not injected")
	}
	env.waitForStats("first metadata failure", func(s MaintenanceStats) bool { return s.Failures >= 1 })
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
		t.Fatal("merged recursive intent was lost; deep row is missing")
	}
	stats := env.q.statsSnapshot()
	if stats.Failures != 1 || stats.GivenUp != 0 || stats.Merged < 1 {
		t.Fatalf("merge retry stats = %+v", stats)
	}
}
