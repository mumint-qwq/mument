package web

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
)

// A directory GET refreshes its directory on the request path. If a move
// finishes while that refresh is between reading the directory and writing the
// index, the older snapshot must be discarded; otherwise the moved-away row is
// resurrected after the maintenance queue already deleted it. This is the RC1
// review's real-HTTP probe, inverted into a correctness assertion.
func TestDirectoryGetCannotRestoreMovedAwayRow(t *testing.T) {
	env := newMoveIndexEnv(t, "src")
	env.mkdir("src", "A")
	env.mkdir("src", "B")
	env.write("src", "A/x.jpg", "x")
	env.write("src", "A/y.jpg", "y")
	env.index("src", "/A/", false)
	env.index("src", "/B/", false)

	scanned := make(chan struct{}, 1)
	release := make(chan struct{})
	var paused atomic.Bool
	indexing.SetRefreshTestHookForTesting(func(stage, source, dir string) {
		if stage == "before_metadata" && source == "src" && dir == "/A/" && paused.CompareAndSwap(false, true) {
			scanned <- struct{}{}
			<-release
		}
	})
	defer indexing.SetRefreshTestHookForTesting(nil)

	getDone := make(chan struct{})
	go func() {
		defer close(getDone)
		env.get("src", "/A/")
	}()
	select {
	case <-scanned:
	case <-time.After(5 * time.Second):
		t.Fatal("GET scan did not pause before the metadata write")
	}

	moved := env.patch("move", []map[string]string{moveItem("src", "/A/x.jpg", "src", "/B/x.jpg")})
	if moved.Succeeded != 1 {
		close(release)
		t.Fatalf("move failed: %+v", moved)
	}
	env.waitIdle()
	if before := env.item("src", "/A/x.jpg"); before != nil {
		close(release)
		t.Fatalf("maintenance queue already deleted the row, but it is back before the GET is released: %#v", before)
	}

	close(release)
	select {
	case <-getDone:
	case <-time.After(5 * time.Second):
		t.Fatal("GET did not finish after release")
	}
	env.waitIdle()

	if after := env.item("src", "/A/x.jpg"); after != nil {
		t.Fatalf("old GET snapshot resurrected the moved-away row: %#v", after)
	}
	if env.exists("src", "A/x.jpg") {
		t.Fatal("source file still exists on disk")
	}
	if !env.exists("src", "B/x.jpg") {
		t.Fatal("destination file is missing on disk")
	}
	if item := env.item("src", "/B/x.jpg"); item == nil {
		t.Fatal("destination index row is missing")
	}
}

// The guard must not disable the normal GET refresh: without a competing move
// intent the refresh still writes newly discovered files.
func TestDirectoryGetRefreshStillWritesMetadata(t *testing.T) {
	env := newMoveIndexEnv(t, "src")
	env.mkdir("src", "A")
	env.write("src", "A/y.jpg", "y")
	env.index("src", "/A/", false)
	env.write("src", "A/new.jpg", "new")

	status, _, _, _ := env.get("src", "/A/")
	if status != 200 {
		t.Fatalf("GET status = %d, want 200", status)
	}
	if item := env.item("src", "/A/new.jpg"); item == nil {
		t.Fatal("GET refresh did not write the newly discovered file")
	}
}

// A directory move deletes the moved subtree recursively. An old GET of a
// descendant must be invalidated too, not just a GET of the source parent.
func TestDirectoryGetOfMovedSubtreeCannotRestoreRows(t *testing.T) {
	env := newMoveIndexEnv(t, "src")
	env.mkdir("src", "A/Sub/nested")
	env.mkdir("src", "B")
	env.write("src", "A/Sub/nested/x.jpg", "x")
	env.index("src", "/A/", true)
	env.index("src", "/B/", false)

	scanned := make(chan struct{}, 1)
	release := make(chan struct{})
	var paused atomic.Bool
	indexing.SetRefreshTestHookForTesting(func(stage, source, dir string) {
		if stage == "before_metadata" && source == "src" && dir == "/A/Sub/nested/" && paused.CompareAndSwap(false, true) {
			scanned <- struct{}{}
			<-release
		}
	})
	defer indexing.SetRefreshTestHookForTesting(nil)

	getDone := make(chan struct{})
	go func() {
		defer close(getDone)
		env.get("src", "/A/Sub/nested/")
	}()
	select {
	case <-scanned:
	case <-time.After(5 * time.Second):
		t.Fatal("descendant GET did not pause")
	}

	moved := env.patch("move", []map[string]string{moveItem("src", "/A/Sub/", "src", "/B/Sub/")})
	if moved.Succeeded != 1 {
		close(release)
		t.Fatalf("directory move failed: %+v", moved)
	}
	env.waitIdle()
	if before := env.item("src", "/A/Sub/nested/x.jpg"); before != nil {
		close(release)
		t.Fatalf("stale row present before releasing old GET: %#v", before)
	}

	close(release)
	select {
	case <-getDone:
	case <-time.After(5 * time.Second):
		t.Fatal("descendant GET did not finish")
	}
	env.waitIdle()

	if after := env.item("src", "/A/Sub/nested/x.jpg"); after != nil {
		t.Fatalf("old descendant GET resurrected the moved row: %#v", after)
	}
	if item := env.item("src", "/B/Sub/nested/x.jpg"); item == nil {
		t.Fatal("destination subtree row is missing")
	}
	if env.exists("src", "A/Sub/nested/x.jpg") {
		t.Fatal("old source file still exists on disk")
	}
	if !env.exists("src", "B/Sub/nested/x.jpg") {
		t.Fatal("destination file is missing on disk")
	}
}

// Moving /A/Sub/ must not discard an in-flight GET of the same-prefix sibling
// /A/Submarine/. Exact parent bumps also must not discard unrelated children.
func TestDirectoryGetSiblingStillWritesAfterSubtreeMove(t *testing.T) {
	env := newMoveIndexEnv(t, "src")
	env.mkdir("src", "A/Sub")
	env.mkdir("src", "A/Submarine")
	env.mkdir("src", "B")
	env.write("src", "A/Sub/x.jpg", "x")
	env.write("src", "A/Submarine/y.jpg", "y")
	env.index("src", "/A/", true)
	env.index("src", "/B/", false)
	if err := env.db.DeleteItem("src", "/A/Submarine/y.jpg", false); err != nil {
		t.Fatal(err)
	}

	scanned := make(chan struct{}, 1)
	release := make(chan struct{})
	var paused atomic.Bool
	indexing.SetRefreshTestHookForTesting(func(stage, source, dir string) {
		if stage == "before_metadata" && source == "src" && dir == "/A/Submarine/" && paused.CompareAndSwap(false, true) {
			scanned <- struct{}{}
			<-release
		}
	})
	defer indexing.SetRefreshTestHookForTesting(nil)

	getDone := make(chan struct{})
	go func() {
		defer close(getDone)
		env.get("src", "/A/Submarine/")
	}()
	select {
	case <-scanned:
	case <-time.After(5 * time.Second):
		t.Fatal("sibling GET did not pause")
	}

	moved := env.patch("move", []map[string]string{moveItem("src", "/A/Sub/", "src", "/B/Sub/")})
	if moved.Succeeded != 1 {
		close(release)
		t.Fatalf("subtree move failed: %+v", moved)
	}
	env.waitIdle()
	close(release)
	select {
	case <-getDone:
	case <-time.After(5 * time.Second):
		t.Fatal("sibling GET did not finish")
	}
	env.waitIdle()

	if item := env.item("src", "/A/Submarine/y.jpg"); item == nil {
		t.Fatal("sibling GET was wrongly invalidated by an exact parent bump or subtree prefix collision")
	}
}
