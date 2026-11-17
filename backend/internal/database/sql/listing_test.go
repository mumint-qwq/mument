package sql

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

func listingTestDB(tb testing.TB, name string) *IndexDB {
	tb.Helper()
	prevDir := settings.Config.Server.CacheDir
	prevIdx := settings.Config.Server.IndexSqlConfig
	settings.Config.Server.CacheDir = tb.TempDir()
	settings.Config.Server.IndexSqlConfig = testIndexSQLConfig(settings.IndexStartupIntegrityOff)
	tb.Cleanup(func() {
		settings.Config.Server.CacheDir = prevDir
		settings.Config.Server.IndexSqlConfig = prevIdx
	})
	db, _, err := NewIndexDB(name, "OFF", 1000, 32, false)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = db.Close() })
	parent := listingItemAt("/music/", "music", true, 0, 1, "directory", false)
	if err = db.InsertItem("music", parent.Path, parent); err != nil {
		tb.Fatal(err)
	}
	return db
}

func listingItemAt(path, name string, isDir bool, size, modUnix int64, mime string, hidden bool) *iteminfo.FileInfo {
	item := &iteminfo.FileInfo{
		Path: path,
		ItemInfo: iteminfo.ItemInfo{
			Name: name, Size: size, ModTime: time.Unix(modUnix, 0), Type: mime, Hidden: hidden,
		},
		IsDir: isDir,
	}
	return item
}

func insertListingItems(tb testing.TB, db *IndexDB, source string, items []*iteminfo.FileInfo) {
	tb.Helper()
	const batch = 2000
	for start := 0; start < len(items); start += batch {
		end := start + batch
		if end > len(items) {
			end = len(items)
		}
		if err := db.BulkInsertItems(source, items[start:end]); err != nil {
			tb.Fatalf("bulk insert [%d:%d]: %v", start, end, err)
		}
	}
}

func syntheticListingItems(tb testing.TB, count int) []*iteminfo.FileInfo {
	tb.Helper()
	items := make([]*iteminfo.FileInfo, 0, count+8)
	// Mixed-case names and a natural-numeric pair exercise the path tie-break.
	items = append(items,
		listingItemAt("/music/Readme.txt", "Readme.txt", false, 10, 100, "text/plain", false),
		listingItemAt("/music/readme.txt", "readme.txt", false, 10, 100, "text/plain", false),
		listingItemAt("/music/2.flac", "2.flac", false, 20, 200, "audio/flac", false),
		listingItemAt("/music/10.flac", "10.flac", false, 20, 200, "audio/flac", false),
		listingItemAt("/music/.hidden.jpg", ".hidden.jpg", false, 30, 300, "image/jpeg", true),
		listingItemAt("/music/notes.tmp", "notes.tmp", false, 40, 400, "text/plain", false),
		listingItemAt("/music/Album/", "Album", true, 0, 500, "directory", false),
		listingItemAt("/music/B-sides/", "B-sides", true, 0, 500, "directory", false),
	)
	for i := 0; len(items) < count; i++ {
		if i%5 == 0 {
			name := fmt.Sprintf("dir-%05d", i)
			items = append(items, listingItemAt("/music/"+name+"/", name, true, 0, int64(1000+i), "directory", false))
			continue
		}
		name := fmt.Sprintf("track-%05d.mp3", i)
		items = append(items, listingItemAt("/music/"+name, name, false, int64(1000+i*7), int64(1000+i), "audio/mpeg", i%37 == 0))
	}
	return items[:count]
}

func listingReferenceKey(sortBy ListingSort, item *iteminfo.FileInfo) string {
	switch sortBy {
	case ListingSortName:
		return listingNameSortKey(item.Name)
	case ListingSortSize:
		return fmt.Sprintf("%020d", item.Size)
	case ListingSortModTime:
		return fmt.Sprintf("%020d", item.ModTime.Unix())
	default:
		return item.Type
	}
}

func referenceListingOrder(items []*iteminfo.FileInfo, sortBy ListingSort, order ListingOrder, showHidden bool, hideExt string) []*iteminfo.FileInfo {
	out := make([]*iteminfo.FileInfo, 0, len(items))
	for _, item := range items {
		if !showHidden && item.Hidden {
			continue
		}
		if !item.IsDir && hideExt != "" && strings.HasSuffix(strings.ToLower(item.Name), hideExt) {
			continue
		}
		out = append(out, item)
	}
	less := func(a, b *iteminfo.FileInfo) bool {
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		ka, kb := listingReferenceKey(sortBy, a), listingReferenceKey(sortBy, b)
		if ka != kb {
			if order == ListingOrderDesc {
				return ka > kb
			}
			return ka < kb
		}
		if order == ListingOrderDesc {
			return a.Path > b.Path
		}
		return a.Path < b.Path
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

func collectListingPages(t *testing.T, db *IndexDB, source string, q DirectoryListingQuery, want int) []*iteminfo.FileInfo {
	t.Helper()
	var after *ListingCursorKey
	var all []*iteminfo.FileInfo
	seen := make(map[string]bool)
	for page := 0; page < want+10; page++ {
		query := q
		query.After = after
		result, err := db.GetDirectoryListingPage(context.Background(), source, "/music/", query)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(result.Items) > q.Limit {
			t.Fatalf("page %d returned %d items, limit %d", page, len(result.Items), q.Limit)
		}
		for _, item := range result.Items {
			if seen[item.Path] {
				t.Fatalf("duplicate item %q on page %d", item.Path, page)
			}
			seen[item.Path] = true
			all = append(all, item)
		}
		if !result.HasMore {
			if result.Next != nil {
				t.Fatalf("last page should not expose a next cursor")
			}
			return all
		}
		if result.Next == nil {
			t.Fatalf("page %d reports hasMore without a cursor", page)
		}
		after = result.Next
	}
	t.Fatalf("pagination did not terminate after %d pages", want+10)
	return nil
}

func TestDirectoryListingPaginationAllSorts(t *testing.T) {
	db := listingTestDB(t, "listing_sorts")
	items := syntheticListingItems(t, 100)
	insertListingItems(t, db, "music", items)

	for _, sort := range []ListingSort{ListingSortName, ListingSortSize, ListingSortModTime, ListingSortType} {
		for _, order := range []ListingOrder{ListingOrderAsc, ListingOrderDesc} {
			t.Run(string(sort)+"_"+string(order), func(t *testing.T) {
				wantItems := referenceListingOrder(items, sort, order, false, ".tmp")
				got := collectListingPages(t, db, "music", DirectoryListingQuery{
					Sort: sort, Order: order, Limit: 13, HideFileExt: ".tmp",
				}, len(wantItems))
				if len(got) != len(wantItems) {
					t.Fatalf("got %d items, want %d", len(got), len(wantItems))
				}
				for i := range wantItems {
					if got[i].Path != wantItems[i].Path {
						t.Fatalf("item %d = %q, want %q", i, got[i].Path, wantItems[i].Path)
					}
				}
			})
		}
	}
}

func TestDirectoryListingPaginationLarge(t *testing.T) {
	for _, count := range []int{4000, 20000} {
		t.Run(fmt.Sprintf("children_%d", count), func(t *testing.T) {
			db := listingTestDB(t, fmt.Sprintf("listing_large_%d", count))
			items := syntheticListingItems(t, count)
			insertListingItems(t, db, "music", items)
			wantItems := referenceListingOrder(items, ListingSortName, ListingOrderAsc, false, "")
			got := collectListingPages(t, db, "music", DirectoryListingQuery{
				Sort: ListingSortName, Order: ListingOrderAsc, Limit: 100,
			}, len(wantItems))
			if len(got) != len(wantItems) {
				t.Fatalf("got %d items, want %d", len(got), len(wantItems))
			}
			for i := range wantItems {
				if got[i].Path != wantItems[i].Path {
					t.Fatalf("item %d = %q, want %q", i, got[i].Path, wantItems[i].Path)
				}
			}
		})
	}
}

// Hard-coded order for the edge cases the protocol calls out: hidden files,
// case-only duplicates, numeric names, and mixed folders/files.
func TestDirectoryListingEdgeCaseOrder(t *testing.T) {
	db := listingTestDB(t, "listing_edge_order")
	items := []*iteminfo.FileInfo{
		listingItemAt("/music/10/", "10", true, 0, 1, "directory", false),
		listingItemAt("/music/2/", "2", true, 0, 1, "directory", false),
		listingItemAt("/music/.hidden/", ".hidden", true, 0, 1, "directory", true),
		listingItemAt("/music/10.flac", "10.flac", false, 1, 1, "audio/flac", false),
		listingItemAt("/music/2.flac", "2.flac", false, 1, 1, "audio/flac", false),
		listingItemAt("/music/Readme.txt", "Readme.txt", false, 1, 1, "text/plain", false),
		listingItemAt("/music/readme.txt", "readme.txt", false, 1, 1, "text/plain", false),
		listingItemAt("/music/.hidden.txt", ".hidden.txt", false, 1, 1, "text/plain", true),
	}
	insertListingItems(t, db, "music", items)

	want := []string{
		"/music/2/", "/music/10/",
		"/music/2.flac", "/music/10.flac",
		"/music/Readme.txt", "/music/readme.txt",
	}
	got := collectListingPages(t, db, "music", DirectoryListingQuery{
		Sort: ListingSortName, Order: ListingOrderAsc, Limit: 1,
	}, len(want))
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Path != want[i] {
			t.Fatalf("item %d = %q, want %q", i, got[i].Path, want[i])
		}
	}

	wantDesc := []string{
		"/music/10/", "/music/2/",
		"/music/readme.txt", "/music/Readme.txt",
		"/music/10.flac", "/music/2.flac",
	}
	got = collectListingPages(t, db, "music", DirectoryListingQuery{
		Sort: ListingSortName, Order: ListingOrderDesc, Limit: 2,
	}, len(wantDesc))
	for i := range wantDesc {
		if got[i].Path != wantDesc[i] {
			t.Fatalf("desc item %d = %q, want %q", i, got[i].Path, wantDesc[i])
		}
	}
}

// The legacy expanded endpoint returns Folders then Files, each sorted by
// SortItems. Zero-padded names and pure numeric names are within that
// comparator's transitive domain, so paged name order must match it exactly.
func TestDirectoryListingMatchesLegacySortOrder(t *testing.T) {
	db := listingTestDB(t, "listing_legacy_order")
	items := []*iteminfo.FileInfo{
		listingItemAt("/music/10/", "10", true, 0, 1, "directory", false),
		listingItemAt("/music/2/", "2", true, 0, 1, "directory", false),
		listingItemAt("/music/album/", "album", true, 0, 1, "directory", false),
		listingItemAt("/music/10.txt", "10.txt", false, 1, 1, "text/plain", false),
		listingItemAt("/music/2.txt", "2.txt", false, 1, 1, "text/plain", false),
		listingItemAt("/music/B.txt", "B.txt", false, 1, 1, "text/plain", false),
		listingItemAt("/music/a.txt", "a.txt", false, 1, 1, "text/plain", false),
	}
	insertListingItems(t, db, "music", items)

	legacy := iteminfo.FileInfo{}
	for _, item := range items {
		if item.IsDir {
			legacy.Folders = append(legacy.Folders, item.ItemInfo)
		} else {
			legacy.Files = append(legacy.Files, iteminfo.ExtendedItemInfo{ItemInfo: item.ItemInfo})
		}
	}
	legacy.SortItems()
	want := make([]string, 0, len(items))
	for _, folder := range legacy.Folders {
		want = append(want, "/music/"+folder.Name+"/")
	}
	for _, file := range legacy.Files {
		want = append(want, "/music/"+file.Name)
	}

	got := collectListingPages(t, db, "music", DirectoryListingQuery{
		Sort: ListingSortName, Order: ListingOrderAsc, Limit: 2,
	}, len(items))
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Path != want[i] {
			t.Fatalf("item %d = %q, want legacy %q", i, got[i].Path, want[i])
		}
	}
}

func TestDirectoryListingFingerprintTracksMutations(t *testing.T) {
	db := listingTestDB(t, "listing_fingerprint")
	items := syntheticListingItems(t, 20)
	insertListingItems(t, db, "music", items)

	first, err := db.GetDirectoryListingPage(context.Background(), "music", "/music/", DirectoryListingQuery{
		Sort: ListingSortName, Order: ListingOrderAsc, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := first.Fingerprint
	if base.Count != 20 || base.SortKeysMissing {
		t.Fatalf("base fingerprint = %#v", base)
	}

	added := listingItemAt("/music/zz-new.txt", "zz-new.txt", false, 7, 777, "text/plain", false)
	if err = db.InsertItem("music", added.Path, added); err != nil {
		t.Fatal(err)
	}
	afterInsert, err := db.GetDirectoryListingPage(context.Background(), "music", "/music/", DirectoryListingQuery{
		Sort: ListingSortName, Order: ListingOrderAsc, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if afterInsert.Fingerprint == base || afterInsert.Fingerprint.Count != base.Count+1 {
		t.Fatalf("insert did not change fingerprint: before=%#v after=%#v", base, afterInsert.Fingerprint)
	}
	// A real scanner refresh rewrites the parent directory row, which is the
	// index-version component of the revision.
	if _, err = db.Exec(`UPDATE index_items SET last_updated = last_updated + 1 WHERE source = ? AND path = ?`, "music", "/music/"); err != nil {
		t.Fatal(err)
	}
	afterRefresh, err := db.GetDirectoryListingPage(context.Background(), "music", "/music/", DirectoryListingQuery{
		Sort: ListingSortName, Order: ListingOrderAsc, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if afterRefresh.Fingerprint.DirectoryUpdated != base.DirectoryUpdated+1 {
		t.Fatalf("directory update not reflected: before=%#v after=%#v", base, afterRefresh.Fingerprint)
	}

	if err = db.DeleteItem("music", added.Path, false); err != nil {
		t.Fatal(err)
	}
	afterDelete, err := db.GetDirectoryListingPage(context.Background(), "music", "/music/", DirectoryListingQuery{
		Sort: ListingSortName, Order: ListingOrderAsc, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if afterDelete.Fingerprint.Count != base.Count {
		t.Fatalf("delete fingerprint = %#v, want base %#v", afterDelete.Fingerprint, base)
	}
}

func TestDirectoryListingFingerprintFlagsMissingSortKeys(t *testing.T) {
	db := listingTestDB(t, "listing_missing_keys")
	insertListingItems(t, db, "music", syntheticListingItems(t, 10))
	// Simulate a row written by the pre-pagination backend (sort_name default).
	if _, err := db.Exec(`INSERT INTO index_items
		(source, path, parent_path, name, size, mod_time, type, is_dir, is_hidden, has_preview, last_updated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"music", "/music/legacy.txt", "/music/", "legacy.txt", 1, 1, "text/plain", false, false, false, 1); err != nil {
		t.Fatal(err)
	}
	result, err := db.GetDirectoryListingPage(context.Background(), "music", "/music/", DirectoryListingQuery{
		Sort: ListingSortName, Order: ListingOrderAsc, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Fingerprint.SortKeysMissing || len(result.Items) != 0 {
		t.Fatalf("missing sort keys not reported: %#v items=%d", result.Fingerprint, len(result.Items))
	}
}

func TestListingQueriesUseCompoundIndexes(t *testing.T) {
	db := listingTestDB(t, "listing_plans")
	insertListingItems(t, db, "music", syntheticListingItems(t, 100))

	for _, sort := range []ListingSort{ListingSortName, ListingSortSize, ListingSortModTime, ListingSortType} {
		for _, order := range []string{"ASC", "DESC"} {
			column := listingColumn(sort)
			query := fmt.Sprintf(`SELECT path FROM index_items
				WHERE source = ? AND parent_path = ? AND is_dir = ? AND is_hidden = 0
				ORDER BY %s %s, path %s LIMIT 100`, column, order, order)
			plan, err := db.ExplainQueryPlan(query, "music", "/music/", false)
			if err != nil {
				t.Fatalf("%s %s plan: %v", sort, order, err)
			}
			if !strings.Contains(plan, "idx_source_parent_listing_") {
				t.Fatalf("%s %s did not use a listing index:\n%s", sort, order, plan)
			}
			if strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
				t.Fatalf("%s %s required a temporary sort:\n%s", sort, order, plan)
			}
		}
	}
}

func benchmarkListingFixture(b *testing.B, count int) *IndexDB {
	b.Helper()
	db := listingTestDB(b, fmt.Sprintf("listing_bench_%d", count))
	items := syntheticListingItems(b, count)
	insertListingItems(b, db, "music", items)
	return db
}

func BenchmarkDirectoryListingPage(b *testing.B) {
	for _, count := range []int{100, 4000, 20000} {
		b.Run(fmt.Sprintf("children_%d", count), func(b *testing.B) {
			db := benchmarkListingFixture(b, count)
			ctx := context.Background()
			b.ResetTimer()
			b.Run("paged_first_100", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := db.GetDirectoryListingPage(ctx, "music", "/music/", DirectoryListingQuery{
						Sort: ListingSortName, Order: ListingOrderAsc, Limit: 100,
					}); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("legacy_full_children", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := db.GetDirectoryChildren("music", "/music/"); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
