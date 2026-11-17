package sql

import (
	"fmt"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

func previewTestItem(path, name, mime string, hidden, hasPreview bool) *iteminfo.FileInfo {
	return &iteminfo.FileInfo{
		Path: path,
		ItemInfo: iteminfo.ItemInfo{
			Name: name, Type: mime, ModTime: time.Unix(1, 0), Hidden: hidden, HasPreview: hasPreview,
		},
	}
}

func TestGetDirectoryPreviewCandidatesIsBoundedAndFiltered(t *testing.T) {
	cacheDir := t.TempDir()
	pop := pushTestIndexConfig(t, cacheDir, testIndexSQLConfig(settings.IndexStartupIntegrityOff))
	defer pop()

	db, _, err := NewIndexDB("preview_candidates", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	items := []*iteminfo.FileInfo{
		previewTestItem("/album/a.jpg", "a.jpg", "image/jpeg", false, true),
		previewTestItem("/album/b.mp4", "b.mp4", "video/mp4", false, true),
		previewTestItem("/album/c.mp3", "c.mp3", "audio/mpeg", false, true),
		previewTestItem("/album/.hidden.jpg", ".hidden.jpg", "image/jpeg", true, true),
		previewTestItem("/album/no-preview.jpg", "no-preview.jpg", "image/jpeg", false, false),
		previewTestItem("/album/readme.txt", "readme.txt", "text/plain", false, true),
		previewTestItem("/other/z.jpg", "z.jpg", "image/jpeg", false, true),
		previewTestItem("/album/nested/", "nested", "directory", false, true),
	}
	if err = db.BulkInsertItems("pictures", items); err != nil {
		t.Fatal(err)
	}

	first, err := db.GetDirectoryPreviewCandidates("pictures", "/album/", false, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].Name != "a.jpg" || first[1].Name != "b.mp4" {
		t.Fatalf("first page = %#v", first)
	}

	second, err := db.GetDirectoryPreviewCandidates("pictures", "/album/", false, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].Name != "c.mp3" {
		t.Fatalf("second page = %#v", second)
	}

	withHidden, err := db.GetDirectoryPreviewCandidates("pictures", "/album/", true, 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(withHidden) != 4 || withHidden[0].Name != ".hidden.jpg" {
		t.Fatalf("hidden-inclusive page = %#v", withHidden)
	}
}

func BenchmarkDirectoryPreviewCandidateWindow(b *testing.B) {
	for _, count := range []int{100, 4000, 20000} {
		b.Run(fmt.Sprintf("children_%d", count), func(b *testing.B) {
			previousCacheDir := settings.Config.Server.CacheDir
			previousIndexConfig := settings.Config.Server.IndexSqlConfig
			settings.Config.Server.CacheDir = b.TempDir()
			settings.Config.Server.IndexSqlConfig = testIndexSQLConfig(settings.IndexStartupIntegrityOff)
			defer func() {
				settings.Config.Server.CacheDir = previousCacheDir
				settings.Config.Server.IndexSqlConfig = previousIndexConfig
			}()

			db, _, err := NewIndexDB(fmt.Sprintf("preview_bench_%d", count), "OFF", 1000, 32, false)
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()

			items := make([]*iteminfo.FileInfo, 0, count)
			for i := 0; i < count; i++ {
				name := fmt.Sprintf("%08d.jpg", i)
				items = append(items, previewTestItem("/album/"+name, name, "image/jpeg", false, true))
			}
			if err = db.BulkInsertItems("pictures", items); err != nil {
				b.Fatal(err)
			}

			b.Run("full_children", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := db.GetDirectoryChildren("pictures", "/album/"); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("cover_window", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := db.GetDirectoryPreviewCandidates("pictures", "/album/", false, 4, 0); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
