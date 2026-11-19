// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package indexing

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
)

func TestB07PathReplacedBetweenDimensionsAndOrientation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.jpg")
	writeBytes(t, path, jpegWithOrientation(t, 80, 40, 1))
	info, _ := os.Stat(path)
	expected := versionFromFileInfo(info, path)

	replacement := filepath.Join(dir, "replacement.jpg")
	writeBytes(t, replacement, jpegWithOrientation(t, 40, 80, 6))
	var once atomic.Bool
	SetImageHeaderTestHookForTesting(func(stage, gotPath string) {
		if stage == "after_dimensions" && gotPath == path && once.CompareAndSwap(false, true) {
			if err := os.Rename(replacement, path); err != nil {
				t.Errorf("rename: %v", err)
			}
		}
	})
	defer SetImageHeaderTestHookForTesting(nil)

	got := readImageHeader(t.Context(), path, expected)
	if got.State != iteminfo.ImageMetaDeferred || got.Err != imageMetaErrStale {
		t.Fatalf("replacement result=%#v want deferred/stale", got)
	}
}

func TestB21ConcurrentHeaderReadsAreSerialized(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	var active, peak atomic.Int32
	SetImageHeaderTestHookForTesting(func(stage, path string) {
		if stage != "before_read" {
			return
		}
		now := active.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		active.Add(-1)
	})
	defer SetImageHeaderTestHookForTesting(nil)

	for i := 0; i < 4; i++ {
		path := filepath.Join(dir, "p"+string(rune('0'+i))+".jpg")
		writeBytes(t, path, jpegBytes(t, 80, 40))
		info, _ := os.Stat(path)
		v := versionFromFileInfo(info, path)
		wg.Add(1)
		go func(path string, v fileVersion) {
			defer wg.Done()
			_ = readImageHeader(t.Context(), path, v)
		}(path, v)
	}
	wg.Wait()
	if peak.Load() > 1 {
		t.Fatalf("peak concurrent header reads=%d want <=1", peak.Load())
	}
}

func TestB06SameSecondRewriteAndRestoredMtimeChangesVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.jpg")
	data := jpegBytes(t, 80, 40)
	writeBytes(t, path, data)
	info, _ := os.Stat(path)
	v1 := versionFromFileInfo(info, path)
	originalTime := info.ModTime()

	// Same logical size, same second, restored mtime; ctime/ino observation must
	// still distinguish the rewrite on Linux. tmpfs timestamps are coarse, so
	// wait for a real clock tick rather than assuming a sub-tick write is visible.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, originalTime, originalTime); err != nil {
		t.Fatal(err)
	}
	info2, _ := os.Stat(path)
	v2 := versionFromFileInfo(info2, path)
	if v1.String() == v2.String() {
		t.Fatalf("version did not change: %s", v1.String())
	}
}

func TestB05SameSecondDifferentNanosecondChangesVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.jpg")
	writeBytes(t, path, jpegBytes(t, 80, 40))
	info, _ := os.Stat(path)
	v1 := versionFromFileInfo(info, path)
	if info.ModTime().Nanosecond() > 999999000 {
		t.Skip("clock too close to the next second")
	}
	newTime := info.ModTime().Add(500 * time.Nanosecond)
	if err := os.Chtimes(path, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	info2, _ := os.Stat(path)
	v2 := versionFromFileInfo(info2, path)
	if v1.MtimeNS/1e9 != v2.MtimeNS/1e9 {
		t.Fatalf("test did not stay in the same second: %d vs %d", v1.MtimeNS, v2.MtimeNS)
	}
	if v1.String() == v2.String() {
		t.Fatal("nanosecond mtime change was not observed")
	}
}

func TestB20DiskDisplaySizeDoesNotChangeLogicalVersion(t *testing.T) {
	idx, dir := newMetadataTestIndex(t)
	path := filepath.Join(dir, "photo.jpg")
	data := jpegBytes(t, 80, 40)
	writeBytes(t, path, data)
	info, _ := os.Stat(path)

	idx.Config.UseLogicalSize = false
	scanner := &Scanner{idx: idx}
	item, _, _, _ := idx.processFileItem(info, "/", Options{IsRoutineScan: true}, scanner, nil)
	if item.Image == nil {
		t.Fatal("image metadata missing")
	}
	if item.Image.LogicalSize != info.Size() {
		t.Fatalf("logical size=%d want %d", item.Image.LogicalSize, info.Size())
	}
	if item.Size == info.Size() {
		t.Fatalf("display size should use disk blocks in this mode, got logical %d", item.Size)
	}
	if item.Size == 0 {
		t.Fatal("disk display size unexpectedly zero")
	}
}

func TestB12DeleteRecreateDoesNotAcceptOldCommit(t *testing.T) {
	idx, dir := newMetadataTestIndex(t)
	path := filepath.Join(dir, "photo.jpg")
	writeBytes(t, path, jpegBytes(t, 80, 40))
	info, _ := os.Stat(path)
	v1 := versionFromFileInfo(info, path)
	im := &iteminfo.ImageIndexMeta{Version: v1.String(), ObsSeq: idx.db.NextImageObsSeq(), LogicalSize: v1.Size, MTimeNS: v1.MtimeNS}
	row := &iteminfo.FileInfo{Path: "/photo.jpg", ItemInfo: iteminfo.ItemInfo{Name: "photo.jpg", Size: v1.Size, ModTime: info.ModTime(), Type: "image/jpeg", Image: im}}
	if err := idx.db.InsertItem("b", row.Path, row); err != nil {
		t.Fatal(err)
	}
	oldWork, _ := idx.db.GetImageWorksByPaths("b", []string{row.Path})
	if err := idx.db.DeleteItem("b", row.Path, false); err != nil {
		t.Fatal(err)
	}

	// A new file at the same path with the same size/mtime must still have a
	// different observed version (inode/ctime) and a different guard lifecycle.
	os.Remove(path)
	if err := os.WriteFile(path, jpegBytes(t, 40, 80), 0o600); err != nil {
		t.Fatal(err)
	}
	info2, _ := os.Stat(path)
	v2 := versionFromFileInfo(info2, path)
	im2 := &iteminfo.ImageIndexMeta{Version: v2.String(), ObsSeq: idx.db.NextImageObsSeq(), LogicalSize: v2.Size, MTimeNS: v2.MtimeNS}
	row2 := &iteminfo.FileInfo{Path: "/photo.jpg", ItemInfo: iteminfo.ItemInfo{Name: "photo.jpg", Size: v2.Size, ModTime: info2.ModTime(), Type: "image/jpeg", Image: im2}}
	if err := idx.db.InsertItem("b", row2.Path, row2); err != nil {
		t.Fatal(err)
	}
	if idx.processImageWork(t.Context(), oldWork["/photo.jpg"], v1) {
		t.Fatal("old commit accepted for recreated row")
	}
	newWork, _ := idx.db.GetImageWorksByPaths("b", []string{"/photo.jpg"})
	if !idx.processImageWork(t.Context(), newWork["/photo.jpg"], v2) {
		t.Fatal("new row commit rejected")
	}
}
