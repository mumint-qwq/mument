// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package indexing

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
)

func rc2InsertImageRow(t *testing.T, idx *Index, dir, name string) (string, os.FileInfo) {
	t.Helper()
	p := filepath.Join(dir, name)
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	it, _, _, _ := idx.processFileItem(fi, "/", Options{}, nil, nil)
	path := "/" + name
	if err := idx.db.InsertItem(idx.Name, path, &iteminfo.FileInfo{Path: path, ItemInfo: *it}); err != nil {
		t.Fatal(err)
	}
	return path, fi
}

func rc2ReadyCache(t *testing.T, idx *Index, path string, fi os.FileInfo, dir string) map[string]indexedImageAttributes {
	t.Helper()
	root := &iteminfo.FileInfo{Path: "/", ItemInfo: iteminfo.ItemInfo{Name: "/", Type: "directory", ModTime: time.Now()}}
	if err := idx.db.InsertItem(idx.Name, "/", root); err != nil {
		t.Fatal(err)
	}
	_, attrs := idx.fetchExtendedAttributes("/", Options{})
	if attrs[path].state != iteminfo.ImageMetaReady {
		t.Fatalf("ready cache missing: %+v", attrs[path])
	}
	return attrs
}

// A cached result is reusable metadata, not a reusable row identity. Deleting
// the row and reinserting the same file version must allocate this observation's
// birth guard so a worker captured from the old row lifetime is rejected.
func TestRC2R2CachedGuardCannotRecreateOldLifetime(t *testing.T) {
	idx, dir := newMetadataTestIndex(t)
	p := filepath.Join(dir, "photo.jpg")
	writeBytes(t, p, jpegBytes(t, 80, 40))
	path, fi := rc2InsertImageRow(t, idx, dir, "photo.jpg")
	old, err := idx.db.GetImageWorksByPaths(idx.Name, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if !idx.processImageWork(t.Context(), old[path], versionFromFileInfo(fi, p)) {
		t.Fatal("ready setup failed")
	}
	attrs := rc2ReadyCache(t, idx, path, fi, dir)

	// Ordinary same-version refresh: the cached result is applied, but the
	// existing row keeps its guard so an in-flight worker is not invalidated.
	refreshed, _, _, _ := idx.processFileItem(fi, "/", Options{}, nil, attrs)
	if err := idx.db.InsertItem(idx.Name, path, &iteminfo.FileInfo{Path: path, ItemInfo: *refreshed}); err != nil {
		t.Fatal(err)
	}
	same, _ := idx.db.GetImageWorksByPaths(idx.Name, []string{path})
	if same[path].Guard != old[path].Guard {
		t.Fatalf("same-version refresh changed guard: old=%d same=%d", old[path].Guard, same[path].Guard)
	}
	if same[path].State != iteminfo.ImageMetaReady {
		t.Fatalf("same-version refresh lost ready state: %#v", same[path])
	}
	if !idx.processImageWork(t.Context(), old[path], versionFromFileInfo(fi, p)) {
		t.Fatal("same-version worker was invalidated by a refresh")
	}

	if err := idx.db.DeleteItem(idx.Name, path, false); err != nil {
		t.Fatal(err)
	}
	rebuilt, _, _, _ := idx.processFileItem(fi, "/", Options{}, nil, attrs)
	if err := idx.db.InsertItem(idx.Name, path, &iteminfo.FileInfo{Path: path, ItemInfo: *rebuilt}); err != nil {
		t.Fatal(err)
	}
	fresh, _ := idx.db.GetImageWorksByPaths(idx.Name, []string{path})
	if fresh[path].Guard == old[path].Guard {
		t.Fatalf("recreated row reused cached guard %d (obs old=%d new=%d)", old[path].Guard, old[path].ObsSeq, fresh[path].ObsSeq)
	}
	if idx.processImageWork(t.Context(), old[path], versionFromFileInfo(fi, p)) {
		t.Fatal("old lifetime worker was accepted after cached-guard recreation")
	}
	if !idx.processImageWork(t.Context(), fresh[path], versionFromFileInfo(fi, p)) {
		t.Fatal("new lifetime worker was rejected")
	}
	row, _ := idx.db.GetItem(idx.Name, path)
	if row == nil || row.DisplayAspectRatio != 2 {
		t.Fatalf("new row ratio=%v", row)
	}
}

// A stat failure is retry information, not ownership of a completed result. A
// late Defer from before another task's same-version ready commit must not wipe
// that result. A real version change is left to the scanner and clears the old
// projection through the versioned INSERT.
func TestRC2R4LateDeferCannotClearSameVersionReady(t *testing.T) {
	idx, dir := newMetadataTestIndex(t)
	p := filepath.Join(dir, "photo.jpg")
	writeBytes(t, p, jpegBytes(t, 80, 40))
	path, fi := rc2InsertImageRow(t, idx, dir, "photo.jpg")
	work, _ := idx.db.GetImageWorksByPaths(idx.Name, []string{path})
	if !idx.processImageWork(t.Context(), work[path], versionFromFileInfo(fi, p)) {
		t.Fatal("ready commit failed")
	}
	before, _ := idx.db.GetItem(idx.Name, path)
	if before.DisplayAspectRatio != 2 {
		t.Fatalf("setup ratio=%v", before.DisplayAspectRatio)
	}
	ok, err := idx.db.DeferImageWork(idx.Name, path, work[path].Version, work[path].Guard, time.Now().Add(time.Minute).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("late Defer cleared a same-version ready result")
	}
	after, _ := idx.db.GetItem(idx.Name, path)
	state, _ := idx.db.GetImageWorksByPaths(idx.Name, []string{path})
	if after.DisplayAspectRatio != 2 || state[path].State != iteminfo.ImageMetaReady {
		t.Fatalf("ready result was lost: ratio=%v work=%#v", after.DisplayAspectRatio, state[path])
	}

	// A genuinely newer file version still owns a new row state and clears the
	// old public projection before its own worker runs.
	writeBytes(t, p, jpegBytes(t, 40, 80))
	newTime := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	newInfo, _ := os.Stat(p)
	changed, _, _, _ := idx.processFileItem(newInfo, "/", Options{}, nil, nil)
	if err := idx.db.InsertItem(idx.Name, path, &iteminfo.FileInfo{Path: path, ItemInfo: *changed}); err != nil {
		t.Fatal(err)
	}
	changedRow, _ := idx.db.GetItem(idx.Name, path)
	changedWork, _ := idx.db.GetImageWorksByPaths(idx.Name, []string{path})
	if changedRow.DisplayAspectRatio != 0 {
		t.Fatalf("new version kept old ratio: %v", changedRow.DisplayAspectRatio)
	}
	if changedWork[path].Version == work[path].Version {
		t.Fatal("version change was not observed")
	}
	ok, err = idx.db.DeferImageWork(idx.Name, path, work[path].Version, work[path].Guard, time.Now().Add(time.Minute).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("old-version Defer matched a changed row")
	}
}

func TestRC2R4RealPermissionFailureNotOwnReady(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission fixture requires an unprivileged UID")
	}
	idx, dir := newMetadataTestIndex(t)
	p := filepath.Join(dir, "photo.jpg")
	writeBytes(t, p, jpegBytes(t, 80, 40))
	path, fi := rc2InsertImageRow(t, idx, dir, "photo.jpg")
	work, _ := idx.db.GetImageWorksByPaths(idx.Name, []string{path})

	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	_, statErr := os.Stat(p)
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if !os.IsPermission(statErr) {
		t.Fatalf("expected EACCES, got %v", statErr)
	}
	now, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if versionFromFileInfo(now, p).String() != work[path].Version {
		t.Fatal("permission fixture changed file version")
	}
	// Another worker completes the same version before the old failure is written.
	if !idx.processImageWork(t.Context(), work[path], versionFromFileInfo(fi, p)) {
		t.Fatal("same-version worker failed")
	}
	before, _ := idx.db.GetItem(idx.Name, path)
	if before.DisplayAspectRatio != 2 {
		t.Fatalf("setup ratio=%v", before.DisplayAspectRatio)
	}
	ok, err := idx.db.DeferImageWork(idx.Name, path, work[path].Version, work[path].Guard, time.Now().Add(time.Minute).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("old permission failure cleared the newer ready result")
	}
	after, _ := idx.db.GetItem(idx.Name, path)
	state, _ := idx.db.GetImageWorksByPaths(idx.Name, []string{path})
	if after.DisplayAspectRatio != 2 || state[path].State != iteminfo.ImageMetaReady {
		t.Fatalf("final state ratio=%v work=%#v", after.DisplayAspectRatio, state[path])
	}
}
