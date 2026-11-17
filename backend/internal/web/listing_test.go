package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	indexsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/internal/database/users"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

func TestParseListingOptionsDefaults(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/resources/list?source=srv&path=/", nil)
	opts, err := parseListingOptions(req)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Sort != "name" || opts.Order != "asc" || opts.Limit != listingDefaultLimit {
		t.Fatalf("defaults = %#v", opts)
	}
}

func TestParseListingOptionsRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		query string
		code  string
	}{
		{"sort=duration", "invalid_sort"},
		{"order=sideways", "invalid_order"},
		{"limit=0", "invalid_limit"},
		{"limit=501", "invalid_limit"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/resources/list?"+tc.query, nil)
		_, err := parseListingOptions(req)
		if err == nil {
			t.Fatalf("%s: expected error", tc.query)
		}
		var protocolErr *listingProtocolError
		if !errors.As(err, &protocolErr) || protocolErr.code != tc.code || protocolErr.status != http.StatusBadRequest {
			t.Fatalf("%s: err = %#v", tc.query, err)
		}
	}
}

func TestParseListingAuthRequestRequiresSourceAndPath(t *testing.T) {
	cases := []struct {
		query string
		code  string
	}{
		{"path=/", "source_required"},
		{"source=srv", "path_required"},
		{"source=srv&path=", "invalid_path"},
		{"source=srv&path=../../etc", "invalid_path"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/resources/list?"+tc.query, nil)
		_, _, _, err := parseListingAuthRequest(req)
		if err == nil {
			t.Fatalf("%s: expected error", tc.query)
		}
		var protocolErr *listingProtocolError
		if !errors.As(err, &protocolErr) || protocolErr.code != tc.code {
			t.Fatalf("%s: err = %#v", tc.query, err)
		}
	}
}

func TestListingCursorRoundTrip(t *testing.T) {
	cursor := listingCursorToken{
		Version: listingCursorVersion, Source: "srv", Directory: "/music/",
		Sort: "name", Order: "asc", Kind: "dir", Key: "key", Path: "/music/Album/",
		ShowHidden: true, HideExt: ".tmp", Revision: "rev",
	}
	encoded, err := encodeListingCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeListingCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != cursor {
		t.Fatalf("decoded = %#v, want %#v", decoded, cursor)
	}
}

func TestListingCursorRejectsMalformedTokens(t *testing.T) {
	valid := listingCursorToken{
		Version: listingCursorVersion, Source: "srv", Directory: "/music/",
		Sort: "name", Order: "asc", Kind: "dir", Key: "key", Path: "/music/Album/", Revision: "rev",
	}
	encoded, err := encodeListingCursor(valid)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"not base64":     "!!!",
		"empty":          "",
		"truncated json": "e30", // {}
		"wrong version":  mustCursor(t, valid, func(c *listingCursorToken) { c.Version = 99 }),
		"wrong kind":     mustCursor(t, valid, func(c *listingCursorToken) { c.Kind = "symlink" }),
		"missing rev":    mustCursor(t, valid, func(c *listingCursorToken) { c.Revision = "" }),
	}
	for name, raw := range cases {
		if _, err := decodeListingCursor(raw); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
	if _, err := decodeListingCursor(encoded); err != nil {
		t.Fatalf("valid cursor rejected: %v", err)
	}
}

func mustCursor(t *testing.T, base listingCursorToken, mutate func(*listingCursorToken)) string {
	t.Helper()
	c := base
	mutate(&c)
	encoded, err := encodeListingCursor(c)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestListingCursorMatchesRequestIdentityAndFilters(t *testing.T) {
	req := listingRequest{
		source: "srv", showHidden: false, hideFileExt: ".tmp",
	}
	opts := listingOptions{Sort: "name", Order: "asc"}
	base := listingCursorToken{
		Version: listingCursorVersion, Source: "srv", Directory: "/music/",
		Sort: "name", Order: "asc", Kind: "dir", Key: "key", Path: "/music/Album/",
		ShowHidden: false, HideExt: ".tmp", Revision: "rev",
	}
	if err := listingCursorMatches(base, req, opts, "/music/"); err != nil {
		t.Fatalf("matching cursor rejected: %v", err)
	}
	for name, mutate := range map[string]func(*listingCursorToken){
		"source":   func(c *listingCursorToken) { c.Source = "other" },
		"dir":      func(c *listingCursorToken) { c.Directory = "/other/" },
		"sort":     func(c *listingCursorToken) { c.Sort = "size" },
		"order":    func(c *listingCursorToken) { c.Order = "desc" },
		"hidden":   func(c *listingCursorToken) { c.ShowHidden = true },
		"hide ext": func(c *listingCursorToken) { c.HideExt = ".bak" },
	} {
		c := base
		mutate(&c)
		err := listingCursorMatches(c, req, opts, "/music/")
		var protocolErr *listingProtocolError
		if !errors.As(err, &protocolErr) || protocolErr.code != "cursor_mismatch" {
			t.Fatalf("%s: err = %#v", name, err)
		}
	}
}

func TestListingRevisionChangesWithFingerprintAndDirectoryMtime(t *testing.T) {
	base := indexsql.DirectoryListingFingerprint{
		Count: 3, DirectoryUpdated: 50,
	}
	dirMod := time.Unix(1000, 0)
	rev := listingRevision("srv", "/music/", dirMod, base)
	if rev == "" {
		t.Fatal("revision is empty")
	}
	if again := listingRevision("srv", "/music/", dirMod, base); again != rev {
		t.Fatalf("revision is not deterministic: %q != %q", again, rev)
	}
	changedCount := base
	changedCount.Count++
	if got := listingRevision("srv", "/music/", dirMod, changedCount); got == rev {
		t.Fatal("revision did not change with child count")
	}
	if got := listingRevision("srv", "/music/", dirMod.Add(time.Second), base); got == rev {
		t.Fatal("revision did not change with directory mtime")
	}
	if err := listingRevisionMismatch(rev, rev); err != nil {
		t.Fatalf("same revision rejected: %v", err)
	}
	err := listingRevisionMismatch(rev, mustRevision(t, "srv", "/music/", dirMod, changedCount))
	var protocolErr *listingProtocolError
	if !errors.As(err, &protocolErr) || protocolErr.status != http.StatusConflict || protocolErr.code != "revision_mismatch" {
		t.Fatalf("mismatch error = %#v", err)
	}
	if err := listingRevisionMismatch("", rev); err != nil {
		t.Fatalf("first page revision must be optional: %v", err)
	}
}

func mustRevision(t *testing.T, source, dir string, mod time.Time, fp indexsql.DirectoryListingFingerprint) string {
	t.Helper()
	rev := listingRevision(source, dir, mod, fp)
	if rev == "" {
		t.Fatal("revision is empty")
	}
	return rev
}

func TestListingClientPathStripsScope(t *testing.T) {
	cases := []struct {
		full, prefix, want string
	}{
		{"/Pictures/Trips/a.jpg", "/Pictures/", "/Trips/a.jpg"},
		{"/Pictures/Trips/Sub/", "/Pictures/", "/Trips/Sub/"},
		{"/a.jpg", "/", "/a.jpg"},
		{"/Pictures/", "/Pictures/", "/"},
	}
	for _, tc := range cases {
		if got := listingClientPath(tc.full, tc.prefix); got != tc.want {
			t.Fatalf("listingClientPath(%q, %q) = %q, want %q", tc.full, tc.prefix, got, tc.want)
		}
	}
}

func TestWrapHandlerEmitsProtocolErrorCode(t *testing.T) {
	handler := wrapHandler(func(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
		return http.StatusConflict, newListingProtocolError(http.StatusConflict, "revision_mismatch", "changed")
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/resources/list", nil)
	handler(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "revision_mismatch" {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "changed") {
		t.Fatalf("message missing: %s", rec.Body.String())
	}
}

func TestResourceListingHandlerDetectsRevisionChange(t *testing.T) {
	sourceRoot := t.TempDir()
	album := filepath.Join(sourceRoot, "album")
	if err := os.MkdirAll(album, 0o755); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(album)
	if err != nil {
		t.Fatal(err)
	}

	prevCacheDir := settings.Config.Server.CacheDir
	prevIndexConfig := settings.Config.Server.IndexSqlConfig
	prevSourceMap := settings.Config.Server.SourceMap
	prevNameMap := settings.Config.Server.NameToSource
	cacheDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cacheDir, "sql"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings.Config.Server.CacheDir = cacheDir
	settings.Config.Server.IndexSqlConfig = settings.IndexSqlConfig{
		BatchSize: 1000, CacheSizeMB: 32, StartupIntegrityCheck: settings.IndexStartupIntegrityOff,
	}
	source := &settings.Source{Path: sourceRoot, Name: "srv"}
	settings.Config.Server.SourceMap = map[string]*settings.Source{sourceRoot: source}
	settings.Config.Server.NameToSource = map[string]*settings.Source{"srv": source}
	settings.InitializeUserResolvers()
	t.Cleanup(func() {
		settings.Config.Server.CacheDir = prevCacheDir
		settings.Config.Server.IndexSqlConfig = prevIndexConfig
		settings.Config.Server.SourceMap = prevSourceMap
		settings.Config.Server.NameToSource = prevNameMap
		settings.InitializeUserResolvers()
		indexing.ClearTestIndices()
		indexing.SetIndexDBForTesting(nil)
	})

	db, _, err := indexsql.NewIndexDB("web_listing_handler", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	indexing.SetIndexDBForTesting(db)
	indexing.SetTestIndexWithDB("srv", sourceRoot, db)

	dirItem := &iteminfo.FileInfo{
		Path: "/album/",
		ItemInfo: iteminfo.ItemInfo{
			Name: "album", Type: "directory", ModTime: stat.ModTime(),
		},
	}
	if err = db.InsertItem("srv", dirItem.Path, dirItem); err != nil {
		t.Fatal(err)
	}
	var children []*iteminfo.FileInfo
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		children = append(children, &iteminfo.FileInfo{
			Path: "/album/" + name,
			ItemInfo: iteminfo.ItemInfo{
				Name: name, Size: 1, ModTime: time.Unix(10, 0), Type: "text/plain",
			},
		})
	}
	if err = db.BulkInsertItems("srv", children); err != nil {
		t.Fatal(err)
	}

	user := &users.User{
		FrontendUser: users.FrontendUser{Username: "tester"},
		BackendScopes: []users.BackendScope{{
			Path: sourceRoot, Scope: "/", Permissions: users.SourceFilePermissions{View: true, Download: true},
		}},
	}
	d := &Context{User: user}

	req := httptest.NewRequest(http.MethodGet, "/api/resources/list?source=srv&path=/album&limit=1", nil)
	rec := httptest.NewRecorder()
	status, err := resourceListingHandler(rec, req, d)
	if err != nil || status != http.StatusOK {
		t.Fatalf("first page status=%d err=%v body=%s", status, err, rec.Body.String())
	}
	var first listingResponse
	if err = json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if !first.HasMore || first.NextCursor == "" || first.Revision == "" || len(first.Items) != 1 {
		t.Fatalf("first page = %#v", first)
	}
	if first.Items[0].Path != "/album/a.txt" {
		t.Fatalf("first item path = %q", first.Items[0].Path)
	}

	// A child insert changes the indexed child count while the live directory
	// mtime and the parent index row stay valid. The next page must be rejected.
	added := &iteminfo.FileInfo{
		Path: "/album/d.txt",
		ItemInfo: iteminfo.ItemInfo{
			Name: "d.txt", Size: 1, ModTime: time.Unix(11, 0), Type: "text/plain",
		},
	}
	if err = db.InsertItem("srv", added.Path, added); err != nil {
		t.Fatal(err)
	}

	next := "/api/resources/list?source=srv&path=/album&limit=1&cursor=" +
		first.NextCursor + "&revision=" + first.Revision
	req = httptest.NewRequest(http.MethodGet, next, nil)
	rec = httptest.NewRecorder()
	status, err = resourceListingHandler(rec, req, d)
	if status != http.StatusConflict {
		t.Fatalf("stale page status=%d body=%s", status, rec.Body.String())
	}
	var protocolErr *listingProtocolError
	if !errors.As(err, &protocolErr) || protocolErr.code != "revision_mismatch" {
		t.Fatalf("stale page err = %#v", err)
	}
}

func TestResourceListingHandlerRejectsMissingPathBeforeIndexAccess(t *testing.T) {
	d := &Context{User: &users.User{}}
	req := httptest.NewRequest(http.MethodGet, "/api/resources/list?source=srv", nil)
	status, err := resourceListingHandler(httptest.NewRecorder(), req, d)
	if status != http.StatusBadRequest || err == nil {
		t.Fatalf("status=%d err=%v", status, err)
	}
	var protocolErr *listingProtocolError
	if !errors.As(err, &protocolErr) || protocolErr.code != "path_required" {
		t.Fatalf("err = %#v", err)
	}
}

func TestResourceListingEnvelopeAndViewToken(t *testing.T) {
	sourceRoot := t.TempDir()
	album := filepath.Join(sourceRoot, "album")
	if err := os.MkdirAll(album, 0o755); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(album)
	if err != nil {
		t.Fatal(err)
	}

	prevCacheDir := settings.Config.Server.CacheDir
	prevIndexConfig := settings.Config.Server.IndexSqlConfig
	prevSourceMap := settings.Config.Server.SourceMap
	prevNameMap := settings.Config.Server.NameToSource
	cacheDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cacheDir, "sql"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings.Config.Server.CacheDir = cacheDir
	settings.Config.Server.IndexSqlConfig = settings.IndexSqlConfig{
		BatchSize: 1000, CacheSizeMB: 32, StartupIntegrityCheck: settings.IndexStartupIntegrityOff,
	}
	source := &settings.Source{Path: sourceRoot, Name: "srv"}
	settings.Config.Server.SourceMap = map[string]*settings.Source{sourceRoot: source}
	settings.Config.Server.NameToSource = map[string]*settings.Source{"srv": source}
	settings.InitializeUserResolvers()
	t.Cleanup(func() {
		settings.Config.Server.CacheDir = prevCacheDir
		settings.Config.Server.IndexSqlConfig = prevIndexConfig
		settings.Config.Server.SourceMap = prevSourceMap
		settings.Config.Server.NameToSource = prevNameMap
		settings.InitializeUserResolvers()
		indexing.ClearTestIndices()
		indexing.SetIndexDBForTesting(nil)
	})

	db, _, err := indexsql.NewIndexDB("web_listing_envelope", "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	indexing.SetIndexDBForTesting(db)
	indexing.SetTestIndexWithDB("srv", sourceRoot, db)

	dirItem := &iteminfo.FileInfo{
		Path: "/album/",
		ItemInfo: iteminfo.ItemInfo{
			Name: "album", Type: "directory", ModTime: stat.ModTime(),
		},
	}
	if err = db.InsertItem("srv", dirItem.Path, dirItem); err != nil {
		t.Fatal(err)
	}
	if err = db.BulkInsertItems("srv", []*iteminfo.FileInfo{{
		Path: "/album/a.txt",
		ItemInfo: iteminfo.ItemInfo{
			Name: "a.txt", Size: 1, ModTime: time.Unix(10, 0), Type: "text/plain",
		},
	}}); err != nil {
		t.Fatal(err)
	}

	user := &users.User{
		FrontendUser: users.FrontendUser{Username: "tester"},
		BackendScopes: []users.BackendScope{{
			Path: sourceRoot, Scope: "/", Permissions: users.SourceFilePermissions{View: true, Download: true},
		}},
	}
	d := &Context{User: user}

	req := httptest.NewRequest(http.MethodGet, "/api/resources/list?source=srv&path=/album&limit=10", nil)
	rec := httptest.NewRecorder()
	status, err := resourceListingHandler(rec, req, d)
	if err != nil || status != http.StatusOK {
		t.Fatalf("status=%d err=%v body=%s", status, err, rec.Body.String())
	}
	var resp listingResponse
	if err = json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Source != "srv" || resp.Name != "album" || resp.Type != "directory" {
		t.Fatalf("envelope = source:%q name:%q type:%q", resp.Source, resp.Name, resp.Type)
	}
	if resp.Filtered != 0 {
		t.Fatalf("filtered = %d, want 0", resp.Filtered)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items = %d", len(resp.Items))
	}
	if resp.Items[0].ViewToken == "" {
		t.Fatalf("file item is missing viewToken")
	}

	// Share-style scoping: serving the same directory with a client root must
	// emit share-relative paths while keeping the envelope and the view token.
	shareRec := httptest.NewRecorder()
	shareReq := httptest.NewRequest(http.MethodGet, "/public/api/resources/list?path=/&limit=10", nil)
	shareStatus, shareErr := serveDirectoryListing(shareReq.Context(), listingRequest{
		source:      "srv",
		viewToken:   "share-token",
		requestPath: "/album/",
		user:        user,
		username:    "tester",
		clientRoot:  "/album/",
		clientPath:  "/",
	}, listingOptions{Sort: "name", Order: "asc", Limit: 10}, shareRec, shareReq)
	if shareErr != nil || shareStatus != http.StatusOK {
		t.Fatalf("share status=%d err=%v body=%s", shareStatus, shareErr, shareRec.Body.String())
	}
	var shareResp listingResponse
	if err = json.Unmarshal(shareRec.Body.Bytes(), &shareResp); err != nil {
		t.Fatal(err)
	}
	if len(shareResp.Items) != 1 || shareResp.Items[0].Path != "/a.txt" {
		t.Fatalf("share items = %#v", shareResp.Items)
	}
	if shareResp.Items[0].ViewToken != "share-token" {
		t.Fatalf("share viewToken = %q", shareResp.Items[0].ViewToken)
	}
	if shareResp.Path != "/" || shareResp.Source != "srv" {
		t.Fatalf("share envelope path=%q source=%q", shareResp.Path, shareResp.Source)
	}
}

func listingPageItem(name string) *iteminfo.FileInfo {
	return &iteminfo.FileInfo{
		Path: "/d/" + name,
		ItemInfo: iteminfo.ItemInfo{Name: name, Type: "text/plain", Size: 1},
	}
}

func listingTestPage(items []*iteminfo.FileInfo, hasMore bool, next *indexsql.ListingCursorKey, fp indexsql.DirectoryListingFingerprint) *indexsql.DirectoryListingResult {
	return &indexsql.DirectoryListingResult{Items: items, HasMore: hasMore, Next: next, Fingerprint: fp}
}

func TestCollectVisibleListingPageSkipsDeniedRows(t *testing.T) {
	fp := indexsql.DirectoryListingFingerprint{Count: 3}
	first := listingTestPage([]*iteminfo.FileInfo{listingPageItem("a"), listingPageItem("b"), listingPageItem("c")}, false, nil, fp)
	items, filtered, hasMore, err := collectVisibleListingPage(first, 100, func(*indexsql.ListingCursorKey, int) (*indexsql.DirectoryListingResult, error) {
		t.Fatal("fetch must not be called when the first page is complete")
		return nil, nil
	}, func(item *iteminfo.FileInfo) bool { return item.Name != "b" })
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || filtered != 1 || hasMore {
		t.Fatalf("items=%d filtered=%d hasMore=%v", len(items), filtered, hasMore)
	}
}

func TestCollectVisibleListingPageContinuesPastAnAllDeniedPage(t *testing.T) {
	fp := indexsql.DirectoryListingFingerprint{Count: 3}
	first := listingTestPage([]*iteminfo.FileInfo{listingPageItem("a"), listingPageItem("b")}, true,
		&indexsql.ListingCursorKey{Kind: "file", Key: "b", Path: "/d/b"}, fp)
	fetchCalls := 0
	items, filtered, hasMore, err := collectVisibleListingPage(first, 10, func(after *indexsql.ListingCursorKey, limit int) (*indexsql.DirectoryListingResult, error) {
		fetchCalls++
		if after == nil || after.Path != "/d/b" || limit != 10 {
			t.Fatalf("unexpected fetch args after=%#v limit=%d", after, limit)
		}
		return listingTestPage([]*iteminfo.FileInfo{listingPageItem("c")}, false, nil, fp), nil
	}, func(item *iteminfo.FileInfo) bool { return item.Name == "c" })
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "c" || filtered != 2 || hasMore || fetchCalls != 1 {
		t.Fatalf("items=%d filtered=%d hasMore=%v calls=%d", len(items), filtered, hasMore, fetchCalls)
	}
}

func TestCollectVisibleListingPageStopsAtTheScanBudget(t *testing.T) {
	fp := indexsql.DirectoryListingFingerprint{Count: 1}
	first := listingTestPage([]*iteminfo.FileInfo{listingPageItem("hidden")}, true,
		&indexsql.ListingCursorKey{Kind: "file", Key: "hidden", Path: "/d/hidden"}, fp)
	fetchCalls := 0
	_, _, _, err := collectVisibleListingPage(first, 10, func(*indexsql.ListingCursorKey, int) (*indexsql.DirectoryListingResult, error) {
		fetchCalls++
		return listingTestPage([]*iteminfo.FileInfo{listingPageItem("hidden")}, true,
			&indexsql.ListingCursorKey{Kind: "file", Key: "hidden", Path: "/d/hidden"}, fp), nil
	}, func(*iteminfo.FileInfo) bool { return false })
	pe, ok := err.(*listingProtocolError)
	if !ok || pe.code != "listing_scan_budget" {
		t.Fatalf("err = %#v", err)
	}
	if fetchCalls != listingMaxFilteredPages {
		t.Fatalf("fetch calls = %d", fetchCalls)
	}
}

func TestCollectVisibleListingPageRejectsRevisionChange(t *testing.T) {
	first := listingTestPage([]*iteminfo.FileInfo{listingPageItem("hidden")}, true,
		&indexsql.ListingCursorKey{Kind: "file", Key: "hidden", Path: "/d/hidden"},
		indexsql.DirectoryListingFingerprint{Count: 1})
	_, _, _, err := collectVisibleListingPage(first, 10, func(*indexsql.ListingCursorKey, int) (*indexsql.DirectoryListingResult, error) {
		return listingTestPage(nil, false, nil, indexsql.DirectoryListingFingerprint{Count: 2}), nil
	}, func(*iteminfo.FileInfo) bool { return false })
	pe, ok := err.(*listingProtocolError)
	if !ok || pe.code != "revision_mismatch" {
		t.Fatalf("err = %#v", err)
	}
}
