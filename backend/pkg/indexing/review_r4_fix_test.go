// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package indexing

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
)

func TestR4MissingRowsCannotStarveLaterImage(t *testing.T) {
	idx, dir := newMetadataTestIndex(t)
	for i := 0; i < 64; i++ {
		name := fmt.Sprintf("/a%03d.jpg", i)
		row := &iteminfo.FileInfo{Path: name, ItemInfo: iteminfo.ItemInfo{
			Name: name, Type: "image/jpeg",
			Image: &iteminfo.ImageIndexMeta{Version: "missing", ObsSeq: idx.db.NextImageObsSeq(), Guard: idx.db.NextImageObsSeq()},
		}}
		if err := idx.db.InsertItem("b", name, row); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(dir, "z.jpg")
	writeBytes(t, p, jpegBytes(t, 80, 40))
	fi, _ := os.Stat(p)
	item, _, _, _ := idx.processFileItem(fi, "/", Options{}, nil, nil)
	if err := idx.db.InsertItem("b", "/z.jpg", &iteminfo.FileInfo{Path: "/z.jpg", ItemInfo: *item}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = idx.backfillImageMetadataBatch(context.Background())
		w, _ := idx.db.GetImageWorksByPaths("b", []string{"/z.jpg"})
		if w["/z.jpg"].State == iteminfo.ImageMetaReady {
			missing, _ := idx.db.GetImageWorksByPaths("b", []string{"/a000.jpg"})
			if missing["/a000.jpg"].State != iteminfo.ImageMetaDeferred {
				t.Fatalf("missing row state=%v want deferred", missing["/a000.jpg"].State)
			}
			// A stale DB version for an existing file must also defer, not parse
			// the old version or block later paths.
			bp := filepath.Join(dir, "b.jpg")
			writeBytes(t, bp, jpegBytes(t, 80, 40))
			stale := &iteminfo.FileInfo{Path: "/b.jpg", ItemInfo: iteminfo.ItemInfo{Name: "b.jpg", Type: "image/jpeg", Image: &iteminfo.ImageIndexMeta{Version: "stale", ObsSeq: idx.db.NextImageObsSeq(), Guard: idx.db.NextImageObsSeq()}}}
			if err := idx.db.InsertItem("b", "/b.jpg", stale); err != nil {
				t.Fatal(err)
			}
			_ = idx.backfillImageMetadataBatch(context.Background())
			bw, _ := idx.db.GetImageWorksByPaths("b", []string{"/b.jpg"})
			if bw["/b.jpg"].State != iteminfo.ImageMetaDeferred {
				t.Fatalf("stale row state=%v want deferred", bw["/b.jpg"].State)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("z.jpg remained unknown behind 64 missing rows")
}
