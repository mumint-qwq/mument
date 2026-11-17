// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package indexing

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	dbsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

func TestB18GetDoesNotWaitForBlockedHeaderRead(t *testing.T) {
	idx, dir := newMetadataTestIndex(t)
	base := jpegBytes(t, 8, 4)
	rows := make([]*iteminfo.FileInfo, 0, 4000)
	for i := 0; i < 4000; i++ {
		name := fmt.Sprintf("p%04d.jpg", i)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, base, 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		v := versionFromFileInfo(info, path)
		rows = append(rows, &iteminfo.FileInfo{
			Path: "/" + name,
			ItemInfo: iteminfo.ItemInfo{
				Name: name, Size: info.Size(), ModTime: info.ModTime(), Type: "image/jpeg",
				Image: &iteminfo.ImageIndexMeta{Version: v.String(), ObsSeq: idx.db.NextImageObsSeq(), State: iteminfo.ImageMetaUnknown, LogicalSize: v.Size, MTimeNS: v.MtimeNS},
			},
		})
	}
	if err := idx.db.BulkInsertItems("b", rows); err != nil {
		t.Fatal(err)
	}

	blockedPath := "/p0000.jpg"
	work, _ := idx.db.GetImageWorksByPaths("b", []string{blockedPath})
	info, _ := os.Stat(filepath.Join(dir, "p0000.jpg"))
	expected := versionFromFileInfo(info, filepath.Join(dir, "p0000.jpg"))
	started := make(chan struct{})
	release := make(chan struct{})
	SetImageHeaderTestHookForTesting(func(stage, path string) {
		if stage == "before_read" && filepath.Base(path) == "p0000.jpg" {
			close(started)
			<-release
		}
	})
	defer SetImageHeaderTestHookForTesting(nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = idx.processImageWork(t.Context(), work[blockedPath], expected)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked reader did not start")
	}

	dirFile, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	stat, _ := dirFile.Stat()
	beforeBytes := ImageMetadataStatsSnapshot().HeaderBytes
	start := time.Now()
	listing, err := idx.GetDirInfo(dirFile, stat, "/", Options{Recursive: false, IsRoutineScan: false}, nil)
	elapsed := time.Since(start)
	dirFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("GET waited for blocked reader: %v", elapsed)
	}
	if len(listing.Files) != 4000 {
		t.Fatalf("files=%d want 4000", len(listing.Files))
	}
	if got := ImageMetadataStatsSnapshot().HeaderBytes; got != beforeBytes {
		t.Fatalf("GET read headers: %d -> %d", beforeBytes, got)
	}
	close(release)
	<-done
}

func TestB14IndexDirectoryWiresMetadataBackfill(t *testing.T) {
	idx, dir := newMetadataTestIndex(t)
	writeBytes(t, filepath.Join(dir, "photo.jpg"), jpegBytes(t, 80, 40))
	scanner := &Scanner{idx: idx}
	if _, _, err := idx.indexDirectory("/", Options{Recursive: true, IsRoutineScan: true}, scanner, nil); err != nil {
		t.Fatal(err)
	}
	scanner.flushBatch()
	scanner.flushImageCandidates()
	got, err := idx.db.GetItem("b", "/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayAspectRatio != 2 {
		t.Fatalf("indexDirectory ratio=%v want 2", got.DisplayAspectRatio)
	}
}

func TestB27BackfillLoopStopsOnSchedulerSignal(t *testing.T) {
	idx, _ := newMetadataTestIndex(t)
	idx.schedulerStop = make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		idx.runImageMetadataBackfill()
	}()
	time.Sleep(50 * time.Millisecond)
	close(idx.schedulerStop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("backfill loop did not stop")
	}
}

func TestB15ReadyVersionSurvivesRestartWithoutHeaderRead(t *testing.T) {
	dir := t.TempDir()
	prevCache := settings.Config.Server.CacheDir
	settings.Config.Server.CacheDir = t.TempDir()
	defer func() { settings.Config.Server.CacheDir = prevCache }()
	path := filepath.Join(dir, "photo.jpg")
	writeBytes(t, path, jpegBytes(t, 80, 40))
	info, _ := os.Stat(path)
	v := versionFromFileInfo(info, path)

	open := func() (*Index, *dbsql.IndexDB) {
		db, _, err := dbsql.NewIndexDB("b_restart", "OFF", 1000, 32, false)
		if err != nil {
			t.Fatal(err)
		}
		return &Index{
			Source: settings.Source{Name: "b", Path: dir, Config: settings.SourceConfig{UseLogicalSize: true}},
			db:     db, scanUpdatedPaths: map[string]bool{}, folderSizes: map[string]uint64{}, folderSizesUnsynced: map[string]struct{}{},
		}, db
	}
	idx, db := open()
	root := &iteminfo.FileInfo{Path: "/", ItemInfo: iteminfo.ItemInfo{Name: "/", Type: "directory", ModTime: info.ModTime()}}
	if err := db.InsertItem("b", "/", root); err != nil {
		t.Fatal(err)
	}
	im := &iteminfo.ImageIndexMeta{Version: v.String(), ObsSeq: db.NextImageObsSeq(), LogicalSize: v.Size, MTimeNS: v.MtimeNS}
	row := &iteminfo.FileInfo{Path: "/photo.jpg", ItemInfo: iteminfo.ItemInfo{Name: "photo.jpg", Size: v.Size, ModTime: info.ModTime(), Type: "image/jpeg", Image: im}}
	if err := db.InsertItem("b", row.Path, row); err != nil {
		t.Fatal(err)
	}
	work, _ := db.GetImageWorksByPaths("b", []string{row.Path})
	if !idx.processImageWork(t.Context(), work[row.Path], v) {
		t.Fatal("ready commit failed")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	idx2, db2 := open()
	defer db2.Close()
	children, err := db2.GetDirectoryChildren("b", "/")
	if err != nil || len(children) != 1 || children[0].Image == nil || children[0].Image.State != iteminfo.ImageMetaReady {
		t.Fatalf("restart row=%#v err=%v", children, err)
	}
	before := ImageMetadataStatsSnapshot()
	_, attrs := idx2.fetchExtendedAttributes("/", Options{})
	item, _, _, _ := idx2.processFileItem(info, "/", Options{IsRoutineScan: false}, nil, attrs)
	if item.DisplayAspectRatio != 2 {
		t.Fatalf("restart ratio=%v want 2", item.DisplayAspectRatio)
	}
	if after := ImageMetadataStatsSnapshot(); after.HeaderBytes != before.HeaderBytes {
		t.Fatalf("restart read header: %d -> %d", before.HeaderBytes, after.HeaderBytes)
	}
}

func TestB19RepeatedScanDoesNotReadReadyVersion(t *testing.T) {
	idx, dir := newMetadataTestIndex(t)
	path := filepath.Join(dir, "photo.jpg")
	writeBytes(t, path, jpegBytes(t, 80, 40))

	scan := func() *Scanner {
		dirFile, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		stat, _ := dirFile.Stat()
		scanner := &Scanner{idx: idx}
		info, err := idx.GetDirInfo(dirFile, stat, "/", Options{Recursive: false, IsRoutineScan: true}, scanner)
		dirFile.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !idx.UpdateMetadata(info, scanner, true) {
			t.Fatal("metadata write failed")
		}
		scanner.flushImageCandidates()
		return scanner
	}
	beforeFirst := ImageMetadataStatsSnapshot()
	first := scan()
	firstStats := ImageMetadataStatsSnapshot()
	if len(first.imageCandidates) != 0 {
		t.Fatalf("flush should clear candidates, got %d", len(first.imageCandidates))
	}
	if firstStats.Ready != beforeFirst.Ready+1 {
		t.Fatalf("first scan ready=%d want %d", firstStats.Ready, beforeFirst.Ready+1)
	}
	before := ImageMetadataStatsSnapshot()
	second := scan()
	after := ImageMetadataStatsSnapshot()
	if len(second.imageCandidates) != 0 {
		t.Fatalf("ready version queued again: %d", len(second.imageCandidates))
	}
	if after.HeaderBytes != before.HeaderBytes {
		t.Fatalf("repeated scan read headers: %d -> %d", before.HeaderBytes, after.HeaderBytes)
	}
	got, _ := idx.db.GetItem("b", "/photo.jpg")
	if got.DisplayAspectRatio != 2 {
		t.Fatalf("ratio after repeated scan=%v", got.DisplayAspectRatio)
	}
}
