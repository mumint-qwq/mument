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

func TestB06ScannerQueuesAndCommitsWithoutFullDecode(t *testing.T) {
	idx, dir := newMetadataTestIndex(t)
	path := filepath.Join(dir, "photo.jpg")
	writeBytes(t, path, jpegBytes(t, 80, 40))

	dirFile, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	stat, _ := dirFile.Stat()
	scanner := &Scanner{idx: idx}
	before := ImageMetadataStatsSnapshot()
	info, err := idx.GetDirInfo(dirFile, stat, "/", Options{Recursive: false, IsRoutineScan: true}, scanner)
	dirFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(scanner.imageCandidates) != 1 {
		t.Fatalf("candidates=%d want 1", len(scanner.imageCandidates))
	}
	if !idx.UpdateMetadata(info, scanner, true) {
		t.Fatal("metadata write failed")
	}
	scanner.flushImageCandidates()

	got, err := idx.db.GetItem("b", "/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	works, _ := idx.db.GetImageWorksByPaths("b", []string{"/photo.jpg"})
	if got.DisplayAspectRatio != 2 {
		t.Fatalf("projection=%v want 2 stats=%#v work=%#v", got.DisplayAspectRatio, ImageMetadataStatsSnapshot(), works["/photo.jpg"])
	}
	w := works["/photo.jpg"]
	if w.State != iteminfo.ImageMetaReady {
		t.Fatalf("state=%v want ready stats=%#v work=%#v", w.State, ImageMetadataStatsSnapshot(), w)
	}
	after := ImageMetadataStatsSnapshot()
	if after.Ready != before.Ready+1 {
		t.Fatalf("ready counter=%d want %d", after.Ready, before.Ready+1)
	}
	if after.HeaderBytes <= 0 {
		t.Fatal("header bytes were not counted")
	}
}

func TestB09RefreshPreservesReadyAndVersionChangeInvalidates(t *testing.T) {
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
	work, _ := idx.db.GetImageWorksByPaths("b", []string{row.Path})
	if !idx.processImageWork(t.Context(), work[row.Path], v1) {
		t.Fatal("initial commit failed")
	}
	oldGuard := work[row.Path].Guard

	// Ordinary refresh with no image observation must preserve the projection.
	plain := *row
	plain.ItemInfo.Image = nil
	plain.ItemInfo.DisplayAspectRatio = 0
	if err := idx.db.InsertItem("b", "/photo.jpg", &plain); err != nil {
		t.Fatal(err)
	}
	got, _ := idx.db.GetItem("b", "/photo.jpg")
	if got.DisplayAspectRatio != 2 {
		t.Fatalf("plain refresh cleared ratio: %v", got.DisplayAspectRatio)
	}

	// A newer observation of a different version invalidates immediately.
	writeBytes(t, path, jpegBytes(t, 40, 80))
	changedTime := info.ModTime().Add(time.Second)
	if err := os.Chtimes(path, changedTime, changedTime); err != nil {
		t.Fatal(err)
	}
	info2, _ := os.Stat(path)
	v2 := versionFromFileInfo(info2, path)
	im2 := &iteminfo.ImageIndexMeta{Version: v2.String(), ObsSeq: idx.db.NextImageObsSeq(), LogicalSize: v2.Size, MTimeNS: v2.MtimeNS}
	changed := *row
	changed.ItemInfo.Size = v2.Size
	changed.ItemInfo.ModTime = info2.ModTime()
	changed.ItemInfo.Image = im2
	if err := idx.db.InsertItem("b", "/photo.jpg", &changed); err != nil {
		t.Fatal(err)
	}
	got, _ = idx.db.GetItem("b", "/photo.jpg")
	if got.DisplayAspectRatio != 0 {
		t.Fatalf("changed version still serves old ratio: %v", got.DisplayAspectRatio)
	}
	works, _ := idx.db.GetImageWorksByPaths("b", []string{"/photo.jpg"})
	if works["/photo.jpg"].Guard <= oldGuard {
		t.Fatalf("guard=%d did not advance from %d", works["/photo.jpg"].Guard, oldGuard)
	}
	newGuard := works["/photo.jpg"].Guard

	// A late older observation must not move the version or guard backwards.
	late := *row
	late.ItemInfo.Image = im
	late.ItemInfo.Size = v1.Size
	late.ItemInfo.ModTime = info.ModTime()
	if err := idx.db.InsertItem("b", "/photo.jpg", &late); err != nil {
		t.Fatal(err)
	}
	after, _ := idx.db.GetImageWorksByPaths("b", []string{"/photo.jpg"})
	if after["/photo.jpg"].Version != v2.String() || after["/photo.jpg"].Guard != newGuard {
		t.Fatalf("late observation regressed: %#v", after["/photo.jpg"])
	}
}
