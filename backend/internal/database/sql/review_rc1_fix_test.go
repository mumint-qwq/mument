// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package sql

import (
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

func TestR2SameVersionRecreateRejectsOldLifetime(t *testing.T) {
	pop := pushTestIndexConfig(t, t.TempDir(), testIndexSQLConfig(settings.IndexStartupIntegrityOff))
	defer pop()
	db, _, err := NewIndexDB("r2_aba", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	a := bImageInfo("/photo.jpg", "same-file-version", db.NextImageObsSeq())
	if err := db.InsertItem("b", a.Path, a); err != nil {
		t.Fatal(err)
	}
	old, _ := db.GetImageWorksByPaths("b", []string{a.Path})
	if old[a.Path].Guard == 0 || old[a.Path].Guard <= a.Image.ObsSeq {
		t.Fatalf("birth guard=%d obs=%d", old[a.Path].Guard, a.Image.ObsSeq)
	}
	if err := db.DeleteItem("b", a.Path, false); err != nil {
		t.Fatal(err)
	}
	b := bImageInfo(a.Path, "same-file-version", db.NextImageObsSeq())
	if err := db.InsertItem("b", b.Path, b); err != nil {
		t.Fatal(err)
	}
	newWork, _ := db.GetImageWorksByPaths("b", []string{a.Path})
	if newWork[a.Path].Guard == old[a.Path].Guard {
		t.Fatalf("recreated row reused guard %d", newWork[a.Path].Guard)
	}
	ok, err := db.CommitImageMetadata("b", a.Path, old[a.Path].Version, old[a.Path].Guard, ImageMetadataCommit{
		State: iteminfo.ImageMetaReady, Width: 80, Height: 40, Orientation: 1, Ratio: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("old lifetime commit was accepted")
	}
	ok, err = db.CommitImageMetadata("b", a.Path, newWork[a.Path].Version, newWork[a.Path].Guard, ImageMetadataCommit{
		State: iteminfo.ImageMetaReady, Width: 80, Height: 40, Orientation: 1, Ratio: 2,
	})
	if err != nil || !ok {
		t.Fatalf("new lifetime commit ok=%v err=%v", ok, err)
	}
}
