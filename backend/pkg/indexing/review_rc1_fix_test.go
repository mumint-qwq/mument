// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package indexing

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
)

func readDirInfoForTest(t *testing.T, idx *Index, dir string, scanner *Scanner) *iteminfo.FileInfo {
	t.Helper()
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	info, err := idx.GetDirInfoCore(f, stat, "/", Options{Recursive: false, IsRoutineScan: scanner != nil}, scanner)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestR1OldDirectorySnapshotCannotRegressNewReady(t *testing.T) {
	idx, dir := newMetadataTestIndex(t)
	path := filepath.Join(dir, "photo.jpg")
	writeBytes(t, path, jpegBytes(t, 80, 40))

	var first atomic.Bool
	started := make(chan struct{})
	release := make(chan struct{})
	SetDirectoryReadTestHookForTesting(func(stage, source, indexPath string) {
		if stage != "after_readdir" || source != "b" || indexPath != "/" {
			return
		}
		if !first.CompareAndSwap(false, true) {
			return
		}
		close(started)
		<-release
	})
	defer SetDirectoryReadTestHookForTesting(nil)

	oldInfo := make(chan *iteminfo.FileInfo, 1)
	go func() {
		oldInfo <- readDirInfoForTest(t, idx, dir, nil)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("old directory read did not block")
	}

	// New observation and ready commit while the old Readdir snapshot is held.
	writeBytes(t, path, jpegBytes(t, 40, 80))
	newTime := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	scanner := &Scanner{idx: idx}
	newInfo := readDirInfoForTest(t, idx, dir, scanner)
	if !idx.UpdateMetadata(newInfo, scanner, true) {
		t.Fatal("new metadata write failed")
	}
	scanner.flushImageCandidates()
	newWork, _ := idx.db.GetImageWorksByPaths("b", []string{"/photo.jpg"})
	if newWork["/photo.jpg"].State != iteminfo.ImageMetaReady {
		t.Fatalf("new row not ready: %#v", newWork["/photo.jpg"])
	}
	newRow, _ := idx.db.GetItem("b", "/photo.jpg")
	if newRow.DisplayAspectRatio != 0.5 {
		t.Fatalf("new ratio=%v", newRow.DisplayAspectRatio)
	}

	close(release)
	old := <-oldInfo
	oldSeq := uint64(0)
	for _, f := range old.Files {
		if f.Name == "photo.jpg" && f.Image != nil {
			oldSeq = f.Image.ObsSeq
		}
	}
	if oldSeq == 0 || oldSeq >= newWork["/photo.jpg"].ObsSeq {
		t.Fatalf("old seq=%d new seq=%d", oldSeq, newWork["/photo.jpg"].ObsSeq)
	}
	if !idx.UpdateMetadata(old, nil, true) {
		t.Fatal("old metadata write failed")
	}
	after, _ := idx.db.GetImageWorksByPaths("b", []string{"/photo.jpg"})
	row, _ := idx.db.GetItem("b", "/photo.jpg")
	if after["/photo.jpg"].Version != newWork["/photo.jpg"].Version || after["/photo.jpg"].Guard != newWork["/photo.jpg"].Guard {
		t.Fatalf("old snapshot regressed version/guard: after=%#v new=%#v", after["/photo.jpg"], newWork["/photo.jpg"])
	}
	if row.DisplayAspectRatio != 0.5 {
		t.Fatalf("old snapshot regressed ratio: %v", row.DisplayAspectRatio)
	}
	// The single-row InsertItem path must also reject the older observation.
	for _, f := range old.Files {
		if f.Name == "photo.jpg" {
			if err := idx.db.InsertItem("b", "/photo.jpg", &iteminfo.FileInfo{Path: "/photo.jpg", ItemInfo: f.ItemInfo}); err != nil {
				t.Fatal(err)
			}
		}
	}
	single, _ := idx.db.GetItem("b", "/photo.jpg")
	if single.DisplayAspectRatio != 0.5 {
		t.Fatalf("single old upsert regressed ratio: %v", single.DisplayAspectRatio)
	}
}
