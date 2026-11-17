package sql

import (
	"context"
	"os"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
)

func migrationItems(n int) []*iteminfo.FileInfo {
	items := make([]*iteminfo.FileInfo, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, &iteminfo.FileInfo{
			Path: "/rehearsal/f" + padded(i) + ".jpg",
			ItemInfo: iteminfo.ItemInfo{
				Name: "f" + padded(i) + ".jpg", Size: int64(i + 1), Type: "image/jpeg", HasPreview: true,
			},
		})
	}
	return items
}

func padded(i int) string {
	s := "00000" + itoa(i)
	return s[len(s)-6:]
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	out := ""
	for i > 0 {
		out = string(rune('0'+i%10)) + out
		i /= 10
	}
	return out
}

// TestListingMigrationBackfillAndOldWriteCompatibility rehearses the reusable
// index upgrade on a synthetic database: rows written by the old binary carry
// an empty sort_name, the paged query refuses to paginate, and an old-style
// INSERT (which omits sort_name) must still succeed so a rollback to the older
// image keeps working.
func TestListingMigrationBackfillAndOldWriteCompatibility(t *testing.T) {
	db := listingTestDB(t, "migration_rehearsal")
	items := migrationItems(200)
	if err := db.BulkInsertItems("srv", items); err != nil {
		t.Fatal(err)
	}
	// Simulate the state right after ALTER TABLE added the column: existing rows
	// keep the empty default until the scanner rewrites them.
	if _, err := db.Exec("UPDATE index_items SET sort_name = ''"); err != nil {
		t.Fatal(err)
	}
	needs, err := db.listingSortNameNeedsBackfill()
	if err != nil {
		t.Fatal(err)
	}
	if !needs {
		t.Fatal("expected backfill to be required")
	}
	page, err := db.GetDirectoryListingPage(context.Background(), "srv", "/rehearsal/", DirectoryListingQuery{
		Sort: ListingSortName, Order: ListingOrderAsc, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Fingerprint.SortKeysMissing {
		t.Fatal("paged query should refuse to paginate until the rescan completes")
	}

	// Old binary write path: no sort_name column in the INSERT.
	if _, err = db.Exec("INSERT INTO index_items (source, path, parent_path, name, size, mod_time, type, is_dir, is_hidden, has_preview, display_aspect_ratio, last_updated) "+
		"VALUES ('srv','/rehearsal/old-writer.txt','/rehearsal/','old-writer.txt',1,1,'text/plain',0,0,0,0,1)"); err != nil {
		t.Fatalf("old writer insert failed: %v", err)
	}
	// Old binary read path: explicit column list, no sort_name.
	rows, err := db.Query("SELECT path, name, size, mod_time, type, is_dir, is_hidden, has_preview, display_aspect_ratio, last_updated " +
		"FROM index_items WHERE source = 'srv' AND parent_path = '/rehearsal/'")
	if err != nil {
		t.Fatalf("old reader query failed: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count < 200 {
		t.Fatalf("old reader saw %d rows, want at least 200", count)
	}
}

// BenchmarkListingRescanWrite20k estimates the DB-side cost of the rescan that
// backfills sort_name for a 20,000-entry directory. It does not include the
// filesystem walk, which depends on the host and the media.
func BenchmarkListingRescanWrite20k(b *testing.B) {
	db := listingTestDB(b, "migration_bench")
	items := migrationItems(20000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := db.BulkInsertItems("srv", items); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if info, err := os.Stat(db.Path()); err == nil {
		b.ReportMetric(float64(info.Size())/1048576.0, "db_MB")
	}
}

// BenchmarkListingIndexBuild20k measures the startup migration cost that is not
// visible in a row-write benchmark: building the six listing indexes over an
// existing 20,000-row table.
func BenchmarkListingIndexBuild20k(b *testing.B) {
	db := listingTestDB(b, "migration_index_bench")
	if err := db.BulkInsertItems("srv", migrationItems(20000)); err != nil {
		b.Fatal(err)
	}
	names := []string{
		"idx_source_parent_listing_name", "idx_source_parent_listing_size",
		"idx_source_parent_listing_mtime", "idx_source_parent_listing_type",
		"idx_source_parent_listing_updated", "idx_listing_sort_name_empty",
	}
	for _, name := range names {
		if _, err := db.Exec("DROP INDEX IF EXISTS " + name); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := db.Exec(`
			CREATE INDEX IF NOT EXISTS idx_source_parent_listing_name ON index_items(source, parent_path, is_dir, sort_name, path);
			CREATE INDEX IF NOT EXISTS idx_source_parent_listing_size ON index_items(source, parent_path, is_dir, size, path);
			CREATE INDEX IF NOT EXISTS idx_source_parent_listing_mtime ON index_items(source, parent_path, is_dir, mod_time, path);
			CREATE INDEX IF NOT EXISTS idx_source_parent_listing_type ON index_items(source, parent_path, is_dir, type, path);
			CREATE INDEX IF NOT EXISTS idx_source_parent_listing_updated ON index_items(source, parent_path, last_updated);
			CREATE INDEX IF NOT EXISTS idx_listing_sort_name_empty ON index_items(sort_name) WHERE sort_name = '';
		`); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if info, err := os.Stat(db.Path()); err == nil {
		b.ReportMetric(float64(info.Size())/1048576.0, "db_MB")
	}
}
