package indexing

import "testing"

func TestDirectoryWriteGuardRefusesSupersededSnapshot(t *testing.T) {
	const source = "guard-unit-test"
	guard := NewDirectoryWriteGuard(source, "/A/")
	defer guard.Close()
	if guard == nil {
		t.Fatal("guard is nil")
	}
	if !guard.acquire() {
		t.Fatal("fresh guard refused the write")
	}
	guard.release()

	stale := NewDirectoryWriteGuard(source, "/A/")
	defer stale.Close()
	BumpDirectoryWriteEpoch(source, "/A")
	if stale.acquire() {
		stale.release()
		t.Fatal("guard accepted a snapshot after a newer intent")
	}
	stale.release()

	current := NewDirectoryWriteGuard(source, "/A/")
	defer current.Close()
	if !current.acquire() {
		t.Fatal("guard created after the bump refused the write")
	}
	current.release()
}

// A directory move must invalidate GETs of the moved directory itself and its
// descendants, but not a same-prefix sibling, an unrelated sibling, an
// ancestor, or the root.
func TestDirectoryWriteGuardSubtreeBoundaries(t *testing.T) {
	const source = "guard-subtree-test"
	child := NewDirectoryWriteGuard(source, "/A/Sub/nested/")
	defer child.Close()
	moved := NewDirectoryWriteGuard(source, "/A/Sub/")
	defer moved.Close()
	sibling := NewDirectoryWriteGuard(source, "/A/Submarine/")
	defer sibling.Close()
	other := NewDirectoryWriteGuard(source, "/A/Other/")
	defer other.Close()
	parent := NewDirectoryWriteGuard(source, "/A/")
	defer parent.Close()
	root := NewDirectoryWriteGuard(source, "/")
	defer root.Close()

	BumpDirectorySubtreeWriteEpoch(source, "/A/Sub")

	for name, guard := range map[string]*DirectoryWriteGuard{"child": child, "moved": moved} {
		if guard.acquire() {
			guard.release()
			t.Fatalf("%s guard accepted a snapshot below the moved subtree", name)
		}
	}
	for name, guard := range map[string]*DirectoryWriteGuard{
		"sibling": sibling, "other": other, "parent": parent, "root": root,
	} {
		if !guard.acquire() {
			t.Fatalf("%s guard was invalidated outside the moved subtree", name)
		}
		guard.release()
	}
}

// The source parent's exact bump changes its own rows but must not discard a
// child directory GET that is scanning a different subtree.
func TestDirectoryWriteGuardExactParentDoesNotInvalidateChildren(t *testing.T) {
	const source = "guard-exact-test"
	parent := NewDirectoryWriteGuard(source, "/P/")
	defer parent.Close()
	child := NewDirectoryWriteGuard(source, "/P/Child/")
	defer child.Close()

	BumpDirectoryWriteEpoch(source, "/P")

	if parent.acquire() {
		parent.release()
		t.Fatal("parent guard accepted a snapshot after its exact bump")
	}
	if !child.acquire() {
		t.Fatal("child guard was invalidated by an exact parent bump")
	}
	child.release()
}

func TestDirectoryWriteGuardSourceAndEmptyIdentity(t *testing.T) {
	guard := NewDirectoryWriteGuard("source-a", "/A/")
	defer guard.Close()
	BumpDirectoryWriteEpoch("source-b", "/A/")
	BumpDirectorySubtreeWriteEpoch("source-b", "/A/")
	if !guard.acquire() {
		t.Fatal("guard was invalidated by another source")
	}
	guard.release()

	if NewDirectoryWriteGuard("", "/A/") != nil {
		t.Fatal("guard for an empty source should be nil")
	}
	if NewDirectoryWriteGuard("src", "") != nil {
		t.Fatal("guard for an empty directory should be nil")
	}
}
