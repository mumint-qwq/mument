package indexing

import (
	"errors"
	"strings"
	"sync"

	"github.com/gtsteffaniak/filebrowser/backend/internal/utils"
)

// Direct directory index writes
//
// The move maintenance queue deletes moved-away rows and then refreshes the
// affected directories. A directory GET also refreshes its own directory on the
// request path, so it can still hold a directory snapshot read before a move.
// Without coordination that older snapshot can be written after the queue
// deleted the moved-away row, resurrecting it.
//
// Instead of giving every directory a permanent version, each direct refresh
// registers itself as an active writer before it scans. A move intent bumps
// (invalidates):
//
//   - exactly the intent's directory, for the parent rows that change when a
//     file is moved; and
//   - the moved directory subtree (and the destination subtree for directory
//     moves), so every active GET of that directory or a descendant is marked
//     stale.
//
// Invalidating an active guard marks it stale. Acquiring a guard happens under
// the guard's own mutex, which is held through the metadata write; a bump takes
// that same mutex. Therefore either the bump wins and the write is skipped, or
// the write finishes before the bump and the queued delete/refresh runs after
// it. No global lock is held during scans or writes, and only active GETs are
// registered, so the registry does not grow with the number of browsed
// directories.

// ErrDirectoryWriteStale means a direct directory scan was superseded by a
// newer move-maintenance intent for the same directory or an ancestor; the
// caller must not write the scan result.
var ErrDirectoryWriteStale = errors.New("directory index write superseded by a newer move intent")

// DirectoryWriteGuard represents one in-flight direct directory refresh.
type DirectoryWriteGuard struct {
	source string
	dir    string

	mu     sync.Mutex
	stale  bool
	closed bool
	held   bool
}

var activeDirectoryWrites = struct {
	sync.Mutex
	guards map[*DirectoryWriteGuard]struct{}
}{guards: make(map[*DirectoryWriteGuard]struct{})}

func normalizeWriteEpochDir(dir string) string {
	if dir == "" {
		return ""
	}
	return utils.AddTrailingSlashIfNotExists(dir)
}

// isDirWithin reports whether child is root or a path-component descendant of
// root. Both paths are normalized with trailing slashes, so "/A/Sub/" does not
// match "/A/Submarine/".
func isDirWithin(child, root string) bool {
	child = normalizeWriteEpochDir(child)
	root = normalizeWriteEpochDir(root)
	if root == "" || child == "" {
		return false
	}
	if root == "/" {
		return true
	}
	return child == root || strings.HasPrefix(child, root)
}

// NewDirectoryWriteGuard registers a refresh before its scan starts. The caller
// must call Close when the refresh is done, even if the directory does not
// exist or the write is skipped.
func NewDirectoryWriteGuard(source, dir string) *DirectoryWriteGuard {
	if source == "" || dir == "" {
		return nil
	}
	guard := &DirectoryWriteGuard{
		source: source,
		dir:    normalizeWriteEpochDir(dir),
	}
	activeDirectoryWrites.Lock()
	activeDirectoryWrites.guards[guard] = struct{}{}
	activeDirectoryWrites.Unlock()
	return guard
}

// Close removes the guard from the active set. It is safe on a nil guard.
func (g *DirectoryWriteGuard) Close() {
	if g == nil {
		return
	}
	activeDirectoryWrites.Lock()
	delete(activeDirectoryWrites.guards, g)
	activeDirectoryWrites.Unlock()

	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
}

// BumpDirectoryWriteEpoch invalidates in-flight refreshes of exactly this
// directory. It is used for the parent directory whose own rows change.
func BumpDirectoryWriteEpoch(source, dir string) {
	bumpDirectoryWrites(source, dir, false)
}

// BumpDirectorySubtreeWriteEpoch invalidates in-flight refreshes of this
// directory and all its descendants. It is used when a directory (or directory
// subtree) is moved or removed.
func BumpDirectorySubtreeWriteEpoch(source, dir string) {
	bumpDirectoryWrites(source, dir, true)
}

func bumpDirectoryWrites(source, dir string, subtree bool) {
	if source == "" || dir == "" {
		return
	}
	dir = normalizeWriteEpochDir(dir)
	activeDirectoryWrites.Lock()
	defer activeDirectoryWrites.Unlock()
	for guard := range activeDirectoryWrites.guards {
		if guard.source != source {
			continue
		}
		if subtree {
			if !isDirWithin(guard.dir, dir) {
				continue
			}
		} else if guard.dir != dir {
			continue
		}
		guard.mu.Lock()
		guard.stale = true
		guard.mu.Unlock()
	}
}

// acquire locks the guard and reports whether the scan is still current. When
// it returns true the caller must call release after the write. When it returns
// false the lock is already released.
//
// The guard mutex is held across the metadata write, and a bump takes the same
// mutex, which is the ordering point between an old snapshot and a move.
func (g *DirectoryWriteGuard) acquire() bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	if g.stale || g.closed {
		g.mu.Unlock()
		return false
	}
	g.held = true
	return true
}

func (g *DirectoryWriteGuard) release() {
	if g == nil || !g.held {
		return
	}
	g.held = false
	g.mu.Unlock()
}
