// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package sql

import (
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

func bImageInfo(path, version string, observed uint64) *iteminfo.FileInfo {
	return &iteminfo.FileInfo{
		Path: path,
		ItemInfo: iteminfo.ItemInfo{
			Name:    "photo.jpg",
			Size:    100,
			ModTime: time.Unix(100, 0),
			Type:    "image/jpeg",
			Image: &iteminfo.ImageIndexMeta{
				Version: version, ObsSeq: observed, Guard: observed, State: iteminfo.ImageMetaUnknown,
				LogicalSize: 100, MTimeNS: 100 * 1e9,
			},
		},
	}
}

func TestB14LegacyMigrationClearsUnvalidatedRatio(t *testing.T) {
	dir := t.TempDir()
	pop := pushTestIndexConfig(t, dir, testIndexSQLConfig(settings.IndexStartupIntegrityOff))
	defer pop()

	legacy, err := createIndexDB("b_legacy", "OFF", "", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec("CREATE TABLE index_items (source TEXT NOT NULL, path TEXT NOT NULL, parent_path TEXT NOT NULL, name TEXT NOT NULL, size INTEGER NOT NULL, mod_time INTEGER NOT NULL, type TEXT NOT NULL, is_dir BOOLEAN NOT NULL, is_hidden BOOLEAN NOT NULL, has_preview BOOLEAN NOT NULL, display_aspect_ratio REAL NOT NULL DEFAULT 0, last_updated INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (source, path))")
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	_, err = legacy.Exec("INSERT INTO index_items (source,path,parent_path,name,size,mod_time,type,is_dir,is_hidden,has_preview,display_aspect_ratio,last_updated) VALUES ('b','/photo.jpg','/','photo.jpg',100,100,'image/jpeg',0,0,1,1.5,100)")
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	db, isNew, err := NewIndexDB("b_legacy", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !isNew {
		t.Fatal("schema addition must request a scan")
	}
	got, err := db.GetItem("b", "/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayAspectRatio != 0 {
		t.Fatalf("legacy ratio still served: %v", got.DisplayAspectRatio)
	}
	if got.Name != "photo.jpg" || got.Size != 100 {
		t.Fatalf("legacy row not preserved: %#v", got)
	}
	// The pre-existing sort_name backfill also requests a scan; satisfy it so
	// this test observes the image-column idempotence specifically.
	if _, err := db.Exec("UPDATE index_items SET sort_name='photo.jpg' WHERE path='/photo.jpg'"); err != nil {
		t.Fatal(err)
	}

	// Reopening must be idempotent and must not clear B rows or request another scan.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db2, isNew2, err := NewIndexDB("b_legacy", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if isNew2 {
		t.Fatal("second open should not request a schema backfill")
	}
}

func TestB14ImageMigrationDoesNotMarkFileIndexNew(t *testing.T) {
	dir := t.TempDir()
	pop := pushTestIndexConfig(t, dir, testIndexSQLConfig(settings.IndexStartupIntegrityOff))
	defer pop()
	legacy, err := createIndexDB("b_image_only_migration", "OFF", "", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec("CREATE TABLE index_items (source TEXT NOT NULL, path TEXT NOT NULL, parent_path TEXT NOT NULL, name TEXT NOT NULL, size INTEGER NOT NULL, mod_time INTEGER NOT NULL, type TEXT NOT NULL, is_dir BOOLEAN NOT NULL, is_hidden BOOLEAN NOT NULL, has_preview BOOLEAN NOT NULL, display_aspect_ratio REAL NOT NULL DEFAULT 0, sort_name TEXT NOT NULL DEFAULT '', last_updated INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (source, path))")
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	_, err = legacy.Exec("INSERT INTO index_items(source,path,parent_path,name,size,mod_time,type,is_dir,is_hidden,has_preview,display_aspect_ratio,sort_name,last_updated) VALUES('b','/a.jpg','/','a.jpg',1,1,'image/jpeg',0,0,1,1.5,'a.jpg',1)")
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	legacy.Close()
	db, isNew, err := NewIndexDB("b_image_only_migration", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if isNew {
		t.Fatal("image-only migration must not mark the file index as new")
	}
	if !db.ImageBackfillPending() {
		t.Fatal("image-only migration did not request a startup pass")
	}
}

func TestB16BToAToBAndProjectionRepair(t *testing.T) {
	dir := t.TempDir()
	pop := pushTestIndexConfig(t, dir, testIndexSQLConfig(settings.IndexStartupIntegrityOff))
	defer pop()

	db, _, err := NewIndexDB("b_reopen", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	row := bImageInfo("/photo.jpg", "v1:manual", 1)
	if err := db.InsertItem("b", row.Path, row); err != nil {
		t.Fatal(err)
	}
	work, _ := db.GetImageWorksByPaths("b", []string{row.Path})
	if ok, err := db.CommitImageMetadata("b", row.Path, "v1:manual", work[row.Path].Guard, ImageMetadataCommit{
		State: iteminfo.ImageMetaReady, Width: 80, Height: 40, Orientation: 1, Ratio: 2,
	}); err != nil || !ok {
		t.Fatalf("commit=%v err=%v", ok, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate old A: overwrite the public projection, leave private state.
	raw, err := createIndexDB("b_reopen", "OFF", "", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec("UPDATE index_items SET display_aspect_ratio=9.9 WHERE path='/photo.jpg'"); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	raw.Close()

	db2, _, err := NewIndexDB("b_reopen", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	got, _ := db2.GetItem("b", "/photo.jpg")
	if got.DisplayAspectRatio != 2 {
		t.Fatalf("projection repair=%v want 2", got.DisplayAspectRatio)
	}
}

func TestB12DeletedRowCommitIsNoop(t *testing.T) {
	dir := t.TempDir()
	pop := pushTestIndexConfig(t, dir, testIndexSQLConfig(settings.IndexStartupIntegrityOff))
	defer pop()
	db, _, err := NewIndexDB("b_delete", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	row := bImageInfo("/photo.jpg", "v1:delete", 1)
	if err := db.InsertItem("b", row.Path, row); err != nil {
		t.Fatal(err)
	}
	work, _ := db.GetImageWorksByPaths("b", []string{row.Path})
	if err := db.DeleteItem("b", row.Path, false); err != nil {
		t.Fatal(err)
	}
	ok, err := db.CommitImageMetadata("b", row.Path, work[row.Path].Version, work[row.Path].Guard, ImageMetadataCommit{
		State: iteminfo.ImageMetaReady, Width: 80, Height: 40, Orientation: 1, Ratio: 2,
	})
	if err != nil || ok {
		t.Fatalf("deleted row commit ok=%v err=%v", ok, err)
	}
	if got, _ := db.GetItem("b", row.Path); got != nil {
		t.Fatalf("commit resurrected deleted row: %#v", got)
	}
}

func TestB11LateObservationCannotRegress(t *testing.T) {
	dir := t.TempDir()
	pop := pushTestIndexConfig(t, dir, testIndexSQLConfig(settings.IndexStartupIntegrityOff))
	defer pop()
	db, _, err := NewIndexDB("b_late", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	newer := bImageInfo("/photo.jpg", "v1:new", 10)
	if err := db.InsertItem("b", newer.Path, newer); err != nil {
		t.Fatal(err)
	}
	newerWork, _ := db.GetImageWorksByPaths("b", []string{newer.Path})
	if ok, _ := db.CommitImageMetadata("b", newer.Path, "v1:new", newerWork[newer.Path].Guard, ImageMetadataCommit{State: iteminfo.ImageMetaReady, Width: 80, Height: 40, Orientation: 1, Ratio: 2}); !ok {
		t.Fatal("new commit failed")
	}
	newerGuard := newerWork[newer.Path].Guard
	older := bImageInfo("/photo.jpg", "v1:old", 5)
	older.ItemInfo.DisplayAspectRatio = 1
	if err := db.InsertItem("b", older.Path, older); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetItem("b", "/photo.jpg")
	work, _ := db.GetImageWorksByPaths("b", []string{"/photo.jpg"})
	if got.DisplayAspectRatio != 2 {
		t.Fatalf("ratio regressed: %v", got.DisplayAspectRatio)
	}
	if work["/photo.jpg"].Version != "v1:new" || work["/photo.jpg"].Guard != newerGuard {
		t.Fatalf("version/guard regressed: %#v", work["/photo.jpg"])
	}
}

func TestB23CandidateBackoffAndParserRevision(t *testing.T) {
	dir := t.TempDir()
	pop := pushTestIndexConfig(t, dir, testIndexSQLConfig(settings.IndexStartupIntegrityOff))
	defer pop()
	db, _, err := NewIndexDB("b_backoff", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	row := bImageInfo("/photo.jpg", "v1:backoff", 1)
	row.ItemInfo.Image.State = iteminfo.ImageMetaFailed
	row.ItemInfo.Image.RetryCount = 1
	row.ItemInfo.Image.RetryAt = 200
	row.ItemInfo.Image.ParserRev = iteminfo.ImageParserRevision
	if err := db.InsertItem("b", row.Path, row); err != nil {
		t.Fatal(err)
	}
	if works, _ := db.GetImageMetadataCandidates("b", "", 64, 100); len(works) != 0 {
		t.Fatalf("future retry was selected: %#v", works)
	}
	if works, _ := db.GetImageMetadataCandidates("b", "", 64, 200); len(works) != 1 {
		t.Fatalf("due retry not selected: %#v", works)
	}
	// Unsupported with the current parser must not hot-loop.
	if _, err := db.Exec("UPDATE index_items SET image_state=3 WHERE path='/photo.jpg'"); err != nil {
		t.Fatal(err)
	}
	if works, _ := db.GetImageMetadataCandidates("b", "", 64, 999999); len(works) != 0 {
		t.Fatalf("unsupported row hot-looped: %#v", works)
	}
	// A parser revision change makes it eligible again.
	if _, err := db.Exec("UPDATE index_items SET image_parser_rev=0 WHERE path='/photo.jpg'"); err != nil {
		t.Fatal(err)
	}
	if works, _ := db.GetImageMetadataCandidates("b", "", 64, 999999); len(works) != 1 {
		t.Fatalf("parser revision did not revalidate: %#v", works)
	}
}

func TestB13CommitOnClosedDBReturnsError(t *testing.T) {
	dir := t.TempDir()
	pop := pushTestIndexConfig(t, dir, testIndexSQLConfig(settings.IndexStartupIntegrityOff))
	defer pop()
	db, _, err := NewIndexDB("b_closed", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	_, err = db.CommitImageMetadata("b", "/photo.jpg", "v1:closed", 0, ImageMetadataCommit{State: iteminfo.ImageMetaReady, Ratio: 1})
	if err == nil {
		t.Fatal("closed database commit returned nil error")
	}
}

func TestB17DirectoryChildrenCarriesPrivateState(t *testing.T) {
	dir := t.TempDir()
	pop := pushTestIndexConfig(t, dir, testIndexSQLConfig(settings.IndexStartupIntegrityOff))
	defer pop()
	db, _, err := NewIndexDB("b_children", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := &iteminfo.FileInfo{Path: "/", ItemInfo: iteminfo.ItemInfo{Name: "/", Type: "directory", ModTime: time.Unix(1, 0)}}
	_ = db.InsertItem("b", "/", root)
	row := bImageInfo("/photo.jpg", "v1:children", 1)
	if err := db.InsertItem("b", row.Path, row); err != nil {
		t.Fatal(err)
	}
	work, _ := db.GetImageWorksByPaths("b", []string{row.Path})
	if ok, _ := db.CommitImageMetadata("b", row.Path, "v1:children", work[row.Path].Guard, ImageMetadataCommit{State: iteminfo.ImageMetaReady, Width: 80, Height: 40, Orientation: 1, Ratio: 2}); !ok {
		t.Fatal("commit failed")
	}
	children, err := db.GetDirectoryChildren("b", "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0].Image == nil || children[0].Image.State != iteminfo.ImageMetaReady || children[0].DisplayAspectRatio != 2 {
		t.Fatalf("children=%#v", children)
	}
}
