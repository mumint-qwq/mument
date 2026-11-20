package web

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/files"
	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	indexsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/internal/database/users"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

// These tests drive the real PATCH and GET handlers over a real loopback HTTP
// server, against real temporary files and a real SQLite index. They cover the
// batch-move correctness matrix: success, partial success, failures, directory
// moves, index on/off, cancellations and the existing single-item calls.

type moveIndexEnv struct {
	t       *testing.T
	root    string
	db      *indexsql.IndexDB
	paths   map[string]string
	indices map[string]*indexing.Index
	user    *users.User
	srv     *moveHTTPServer
	client  *http.Client
}

func newMoveIndexEnv(t *testing.T, sourceNames ...string) *moveIndexEnv {
	t.Helper()
	prevFilePerm := fileutils.PermFile
	prevDirPerm := fileutils.PermDir
	prevCacheDir := settings.Config.Server.CacheDir
	prevSourceMap := settings.Config.Server.SourceMap
	prevNameToSource := settings.Config.Server.NameToSource
	prevSourceResolver := users.GetSourceNameResolver()
	prevSourceConfig := users.GetSourceConfig()

	fileutils.SetFsPermissions(0o644, 0o755)
	// patchAction deletes thumbnails; the preview service must exist even for
	// sources without previews.
	measurePreviewOnce.Do(func() {
		cacheDir, err := os.MkdirTemp("", "batch-move-web-preview")
		if err == nil {
			_ = preview.StartPreviewGenerator(1, cacheDir)
		}
	})
	root := t.TempDir()
	cacheDir := filepath.Join(root, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings.Config.Server.CacheDir = cacheDir
	db, _, err := indexsql.NewIndexDB("moveidx", "OFF", 1000, 32, true)
	if err != nil {
		t.Fatalf("index db: %v", err)
	}
	indexing.SetIndexDBForTesting(db)

	env := &moveIndexEnv{
		t:       t,
		root:    root,
		db:      db,
		paths:   make(map[string]string, len(sourceNames)),
		indices: make(map[string]*indexing.Index, len(sourceNames)),
		client:  &http.Client{Timeout: 60 * time.Second},
	}
	settings.Config.Server.SourceMap = make(map[string]*settings.Source, len(sourceNames))
	settings.Config.Server.NameToSource = make(map[string]*settings.Source, len(sourceNames))
	perms := users.SourceFilePermissions{View: true, Download: true, Modify: true, Create: true, Delete: true}
	scopes := make([]users.BackendScope, 0, len(sourceNames))
	backendPerms := make(map[string]users.SourceFilePermissions, len(sourceNames))
	for _, name := range sourceNames {
		sourcePath := filepath.Join(root, name)
		if err := os.MkdirAll(sourcePath, 0o755); err != nil {
			t.Fatal(err)
		}
		env.paths[name] = sourcePath
		env.indices[name] = indexing.SetTestIndexWithDB(name, sourcePath, db)
		settings.Config.Server.SourceMap[sourcePath] = &settings.Source{Path: sourcePath, Name: name}
		settings.Config.Server.NameToSource[name] = settings.Config.Server.SourceMap[sourcePath]
		scopes = append(scopes, users.BackendScope{Path: sourcePath, Scope: "/", Permissions: perms})
		backendPerms[sourcePath] = perms
	}
	env.user = &users.User{
		ID:                       1,
		FrontendUser:             users.FrontendUser{Username: "mover"},
		BackendScopes:            scopes,
		BackendSourcePermissions: backendPerms,
		Version:                  users.SourcePermissionsMigrationVersion,
	}
	users.SetSourceNameResolver(func(name string) (string, error) {
		if path, ok := env.paths[name]; ok {
			return path, nil
		}
		return "", fmt.Errorf("unknown source %q", name)
	})
	users.SetSourceConfig(&users.SourceConfigProvider{
		GetSourceByPath: func(path string) (users.SourceInfo, bool) {
			for name, sourcePath := range env.paths {
				if sourcePath == path {
					return users.SourceInfo{Path: sourcePath, Name: name}, true
				}
			}
			return users.SourceInfo{}, false
		},
		GetSourceByName: func(name string) (users.SourceInfo, bool) {
			if path, ok := env.paths[name]; ok {
				return users.SourceInfo{Path: path, Name: name}, true
			}
			return users.SourceInfo{}, false
		},
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d := &Context{User: env.user}
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/resources":
			_, _ = ResourcePatchHandler(w, r, d)
		case r.Method == http.MethodGet && r.URL.Path == "/api/resources":
			_, _ = resourceGetHandler(w, r, d)
		default:
			http.NotFound(w, r)
		}
	})
	env.srv = newMoveHTTPServer(handler)
	env.client.Transport = env.srv.client.Transport

	t.Cleanup(func() {
		env.waitIdle()
		env.srv.close()
		indexing.SetRefreshProbeEnabled(false)
		files.SetIndexWorkProbeEnabled(false)
		indexing.ClearTestIndices()
		indexing.SetIndexDBForTesting(nil)
		_ = db.Close()
		fileutils.SetFsPermissions(uint32(prevFilePerm), uint32(prevDirPerm))
		settings.Config.Server.CacheDir = prevCacheDir
		settings.Config.Server.SourceMap = prevSourceMap
		settings.Config.Server.NameToSource = prevNameToSource
		users.SetSourceNameResolver(prevSourceResolver)
		users.SetSourceConfig(prevSourceConfig)
	})
	return env
}

func (e *moveIndexEnv) path(source, rel string) string {
	return filepath.Join(e.paths[source], rel)
}

func (e *moveIndexEnv) mkdir(source, rel string) {
	e.t.Helper()
	if err := os.MkdirAll(e.path(source, rel), 0o755); err != nil {
		e.t.Fatal(err)
	}
}

func (e *moveIndexEnv) write(source, rel, content string) {
	e.t.Helper()
	if err := os.WriteFile(e.path(source, rel), []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *moveIndexEnv) exists(source, rel string) bool {
	_, err := os.Stat(e.path(source, rel))
	return err == nil
}

func (e *moveIndexEnv) index(source, dir string, recursive bool) {
	e.t.Helper()
	if err := e.indices[source].RefreshDirectory(dir, recursive); err != nil {
		e.t.Fatalf("initial index %s %s: %v", source, dir, err)
	}
}

func (e *moveIndexEnv) item(source, path string) *iteminfo.FileInfo {
	e.t.Helper()
	item, err := e.db.GetItem(source, path)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		e.t.Fatalf("get item %s %s: %v", source, path, err)
	}
	return item
}

func (e *moveIndexEnv) children(source, dir string) int {
	e.t.Helper()
	rows, err := e.db.GetDirectoryChildren(source, dir)
	if err != nil {
		e.t.Fatalf("children %s %s: %v", source, dir, err)
	}
	return len(rows)
}

func (e *moveIndexEnv) waitIdle() {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := files.WaitForIndexMaintenance(ctx); err != nil {
		e.t.Fatalf("index maintenance did not become idle: %v", err)
	}
}

func (e *moveIndexEnv) startProbes() {
	e.t.Helper()
	indexing.SetRefreshProbeEnabled(true)
	files.SetIndexWorkProbeEnabled(true)
}

type patchResult struct {
	Status    int
	Succeeded int
	Failed    int
	Messages  []string
	Ms        float64
}

func (e *moveIndexEnv) patch(action string, items []map[string]string) patchResult {
	e.t.Helper()
	payload, err := json.Marshal(map[string]any{"action": action, "items": items})
	if err != nil {
		e.t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPatch, e.srv.URL+"/api/resources", bytes.NewReader(payload))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatalf("patch: %v", err)
	}
	defer resp.Body.Close()
	result := patchResult{Status: resp.StatusCode}
	result.Ms = float64(time.Since(start).Microseconds()) / 1000
	var body struct {
		Succeeded []json.RawMessage `json:"succeeded"`
		Failed    []struct {
			Message string `json:"message"`
		} `json:"failed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		e.t.Fatalf("decode patch response: %v", err)
	}
	result.Succeeded = len(body.Succeeded)
	result.Failed = len(body.Failed)
	for _, failed := range body.Failed {
		result.Messages = append(result.Messages, failed.Message)
	}
	return result
}

func (e *moveIndexEnv) get(source, path string) (int, []string, []string, float64) {
	e.t.Helper()
	target := e.srv.URL + "/api/resources?source=" + url.QueryEscape(source) + "&path=" + url.QueryEscape(path)
	start := time.Now()
	resp, err := e.client.Get(target)
	if err != nil {
		e.t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	ms := float64(time.Since(start).Microseconds()) / 1000
	var body struct {
		Files    []struct{ Name string } `json:"files"`
		Folders  []struct{ Name string } `json:"folders"`
		Messages []string                `json:"messages"`
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &body)
	files := make([]string, 0, len(body.Files))
	for _, file := range body.Files {
		files = append(files, file.Name)
	}
	folders := make([]string, 0, len(body.Folders))
	for _, folder := range body.Folders {
		folders = append(folders, folder.Name)
	}
	return resp.StatusCode, files, folders, ms
}

func moveItem(fromSource, fromPath, toSource, toPath string) map[string]string {
	return map[string]string{
		"fromSource": fromSource, "fromPath": fromPath,
		"toSource": toSource, "toPath": toPath,
	}
}

// B01/B23: 267 ordinary files A -> B in one source. A quiet batch must produce
// one refresh per affected directory and leave a fully consistent index with
// the destination preview attributes intact.
func TestBatchMoveIndexTwoDirectoriesOnly(t *testing.T) {
	env := newMoveIndexEnv(t, "src")
	env.mkdir("src", "A")
	env.mkdir("src", "B")
	const moved = 267
	for i := 0; i < moved; i++ {
		env.write("src", fmt.Sprintf("A/f-%06d.jpg", i), "synthetic")
	}
	env.index("src", "/A/", false)
	env.index("src", "/B/", false)
	env.startProbes()
	before := files.IndexMaintenanceStats()

	items := make([]map[string]string, 0, moved)
	for i := 0; i < moved; i++ {
		name := fmt.Sprintf("f-%06d.jpg", i)
		items = append(items, moveItem("src", "/A/"+name, "src", "/B/"+name))
	}
	result := env.patch("move", items)
	if result.Status != http.StatusOK || result.Succeeded != moved || result.Failed != 0 {
		t.Fatalf("patch = %+v, want 200 with %d successes", result, moved)
	}

	env.waitIdle()
	after := files.IndexMaintenanceStats()
	if submitted := after.Submitted - before.Submitted; submitted != 2 {
		t.Fatalf("submitted refresh intents = %d, want 2 (one per directory)", submitted)
	}
	if merged := after.Merged - before.Merged; merged != 0 {
		t.Fatalf("merged intents = %d, want 0", merged)
	}
	work := files.IndexWorkProbeSnapshot()
	if work.RefreshRequests != 2 {
		t.Fatalf("RefreshIndex calls = %d, want 2", work.RefreshRequests)
	}
	probe := indexing.RefreshProbeSnapshot()
	if probe.ScanStarted != 2 || probe.DistinctDirs != 2 {
		t.Fatalf("scans=%d distinct=%d failed=%d skipped=%d work=%+v queue=%+v, want 2/2",
			probe.ScanStarted, probe.DistinctDirs, probe.ScanFailed, probe.ScanSkipped, work, after)
	}
	if probe.PeakActive != 1 || probe.Overlaps != 0 {
		t.Fatalf("scans overlapped: peak=%d overlaps=%d", probe.PeakActive, probe.Overlaps)
	}
	if probe.DeleteCalls != moved {
		t.Fatalf("deleted rows = %d, want %d", probe.DeleteCalls, moved)
	}
	if got := env.children("src", "/A/"); got != 0 {
		t.Fatalf("index children in A = %d, want 0", got)
	}
	if got := env.children("src", "/B/"); got != moved {
		t.Fatalf("index children in B = %d, want %d", got, moved)
	}
	for _, sample := range []int{0, 1, moved / 2, moved - 1} {
		name := fmt.Sprintf("f-%06d.jpg", sample)
		if item := env.item("src", "/A/"+name); item != nil {
			t.Fatalf("stale source row %s", name)
		}
		item := env.item("src", "/B/"+name)
		if item == nil {
			t.Fatalf("missing destination row %s", name)
		}
		if item.Type != "image/jpeg" || !item.HasPreview || item.Size == 0 || item.ModTime.IsZero() {
			t.Fatalf("destination row lost preview attributes: %+v", item.ItemInfo)
		}
	}
	if item := env.item("src", "/B/"); item == nil || item.Size == 0 {
		t.Fatalf("destination directory row was not refreshed: %#v", item)
	}
	if !env.exists("src", "B/f-000000.jpg") || env.exists("src", "A/f-000000.jpg") {
		t.Fatal("filesystem does not match the reported move")
	}
}

// B21: the listing must be correct immediately after the move and must not be
// held hostage by hundreds of background refreshes.
func TestBatchMoveListingIsFastImmediatelyAfterMove(t *testing.T) {
	env := newMoveIndexEnv(t, "src")
	env.mkdir("src", "A")
	env.mkdir("src", "B")
	const moved = 267
	for i := 0; i < moved; i++ {
		env.write("src", fmt.Sprintf("A/f-%06d.jpg", i), "synthetic")
	}
	env.index("src", "/A/", false)
	env.index("src", "/B/", false)

	items := make([]map[string]string, 0, moved)
	for i := 0; i < moved; i++ {
		name := fmt.Sprintf("f-%06d.jpg", i)
		items = append(items, moveItem("src", "/A/"+name, "src", "/B/"+name))
	}
	if result := env.patch("move", items); result.Status != http.StatusOK || result.Succeeded != moved {
		t.Fatalf("patch = %+v", result)
	}
	status, listingFiles, _, firstGetMs := env.get("src", "/A/")
	if status != http.StatusOK || len(listingFiles) != 0 {
		t.Fatalf("immediate GET /A/: status=%d files=%d", status, len(listingFiles))
	}
	if firstGetMs > 2000 {
		t.Fatalf("first listing after move took %.1fms", firstGetMs)
	}
	status, destFiles, _, destMs := env.get("src", "/B/")
	if status != http.StatusOK || len(destFiles) != moved {
		t.Fatalf("immediate GET /B/: status=%d files=%d", status, len(destFiles))
	}
	if destMs > 2000 {
		t.Fatalf("destination listing after move took %.1fms", destMs)
	}

	// Repeated listings while the index work settles must stay correct.
	slowest := destMs
	for i := 0; i < 10; i++ {
		status, files, _, ms := env.get("src", "/B/")
		if status != http.StatusOK || len(files) != moved {
			t.Fatalf("repeated GET /B/: status=%d files=%d", status, len(files))
		}
		if ms > slowest {
			slowest = ms
		}
	}
	if slowest > 2000 {
		t.Fatalf("slowest listing during background work: %.1fms", slowest)
	}
	env.waitIdle()
	if env.children("src", "/B/") != moved || env.children("src", "/A/") != 0 {
		t.Fatal("index did not converge after repeated listings")
	}
}

// B03: a single-file rename within one directory is one intent, not two.
func TestBatchRenameDeduplicatesParent(t *testing.T) {
	env := newMoveIndexEnv(t, "src")
	env.mkdir("src", "A")
	env.write("src", "A/x.jpg", "x")
	env.write("src", "A/y.jpg", "y")
	env.index("src", "/A/", false)
	env.startProbes()
	before := files.IndexMaintenanceStats()

	result := env.patch("rename", []map[string]string{moveItem("src", "/A/x.jpg", "src", "/A/x2.jpg")})
	if result.Status != http.StatusOK || result.Succeeded != 1 {
		t.Fatalf("rename = %+v", result)
	}
	env.waitIdle()
	if submitted := files.IndexMaintenanceStats().Submitted - before.Submitted; submitted != 1 {
		t.Fatalf("rename submitted %d intents, want 1", submitted)
	}
	if probe := indexing.RefreshProbeSnapshot(); probe.ScanStarted != 1 {
		t.Fatalf("rename scanned %d directories, want 1", probe.ScanStarted)
	}
	if env.item("src", "/A/x.jpg") != nil {
		t.Fatal("old row survived the rename")
	}
	if env.item("src", "/A/x2.jpg") == nil {
		t.Fatal("new row missing after the rename")
	}
	if !env.exists("src", "A/x2.jpg") || env.exists("src", "A/x.jpg") {
		t.Fatal("rename was not performed exactly once")
	}
}

// B04: the same directory path in two sources must not be merged.
func TestBatchMoveKeepsSourcesDistinct(t *testing.T) {
	env := newMoveIndexEnv(t, "s1", "s2")
	for _, source := range []string{"s1", "s2"} {
		env.mkdir(source, "A")
	}
	env.write("s1", "A/x.jpg", "x")
	env.write("s1", "A/y.jpg", "y")
	env.index("s1", "/A/", false)
	env.index("s2", "/A/", false)
	env.startProbes()
	before := files.IndexMaintenanceStats()

	result := env.patch("move", []map[string]string{
		moveItem("s1", "/A/x.jpg", "s2", "/A/x.jpg"),
		moveItem("s1", "/A/y.jpg", "s2", "/A/y.jpg"),
	})
	if result.Status != http.StatusOK || result.Succeeded != 2 {
		t.Fatalf("cross-source move = %+v", result)
	}
	env.waitIdle()
	if submitted := files.IndexMaintenanceStats().Submitted - before.Submitted; submitted != 2 {
		t.Fatalf("cross-source submitted %d intents, want 2", submitted)
	}
	if env.children("s1", "/A/") != 0 {
		t.Fatal("source rows were not deleted")
	}
	if env.children("s2", "/A/") != 2 {
		t.Fatal("destination rows were not indexed")
	}
	if env.item("s1", "/A/x.jpg") != nil || env.item("s2", "/A/x.jpg") == nil {
		t.Fatal("source identity was mixed up")
	}
	if !env.exists("s2", "A/x.jpg") || env.exists("s1", "A/x.jpg") {
		t.Fatal("cross-source filesystem move is wrong")
	}
}

// B05: partial success, all failure and an invalid item after a valid one.
func TestBatchMovePartialAndAllFailure(t *testing.T) {
	env := newMoveIndexEnv(t, "src")
	env.mkdir("src", "A")
	env.mkdir("src", "B")
	env.write("src", "A/x.jpg", "x")
	env.write("src", "A/y.jpg", "y")
	env.index("src", "/A/", false)
	env.startProbes()
	before := files.IndexMaintenanceStats()

	partial := env.patch("move", []map[string]string{
		moveItem("src", "/A/x.jpg", "src", "/B/x.jpg"),
		moveItem("src", "/A/missing.jpg", "src", "/B/missing.jpg"),
	})
	if partial.Status != http.StatusMultiStatus || partial.Succeeded != 1 || partial.Failed != 1 {
		t.Fatalf("partial move = %+v, want 207 with one success", partial)
	}
	env.waitIdle()
	if submitted := files.IndexMaintenanceStats().Submitted - before.Submitted; submitted != 2 {
		t.Fatalf("partial move submitted %d intents, want 2", submitted)
	}
	if env.item("src", "/A/x.jpg") != nil || env.item("src", "/B/x.jpg") == nil {
		t.Fatal("the successful item was not maintained")
	}
	if env.item("src", "/A/missing.jpg") != nil {
		t.Fatal("the failed item created an index row")
	}
	if !env.exists("src", "B/x.jpg") || !env.exists("src", "A/y.jpg") {
		t.Fatal("filesystem after partial move is wrong")
	}

	// A valid item followed by an invalid one still keeps the successful work.
	before = files.IndexMaintenanceStats()
	successThenError := env.patch("move", []map[string]string{
		moveItem("src", "/A/y.jpg", "src", "/B/y.jpg"),
		moveItem("src", "/A/missing2.jpg", "src", "/B/missing2.jpg"),
	})
	if successThenError.Status != http.StatusMultiStatus || successThenError.Succeeded != 1 {
		t.Fatalf("success-then-error = %+v", successThenError)
	}
	env.waitIdle()
	if env.item("src", "/A/y.jpg") != nil || env.item("src", "/B/y.jpg") == nil {
		t.Fatal("success before a later error was not maintained")
	}

	// All failed: nothing is collected and nothing is scanned.
	before = files.IndexMaintenanceStats()
	scanBefore := indexing.RefreshProbeSnapshot().ScanStarted
	allFailed := env.patch("move", []map[string]string{
		moveItem("src", "/A/nope1.jpg", "src", "/B/nope1.jpg"),
		moveItem("src", "/A/nope2.jpg", "src", "/B/nope2.jpg"),
	})
	if allFailed.Status != http.StatusInternalServerError || allFailed.Succeeded != 0 {
		t.Fatalf("all-failed move = %+v, want 500", allFailed)
	}
	env.waitIdle()
	if submitted := files.IndexMaintenanceStats().Submitted - before.Submitted; submitted != 0 {
		t.Fatalf("all-failed move submitted %d intents, want 0", submitted)
	}
	if scans := indexing.RefreshProbeSnapshot().ScanStarted - scanBefore; scans != 0 {
		t.Fatalf("all-failed move started %d scans, want 0", scans)
	}
}

// B06: moving a directory removes the old subtree and indexes the new one,
// including nested children and parent size.
func TestBatchMoveDirectoryKeepsSubtree(t *testing.T) {
	env := newMoveIndexEnv(t, "src")
	env.mkdir("src", "A/Sub/nested")
	env.mkdir("src", "B")
	env.write("src", "A/Sub/s1.jpg", "s1")
	env.write("src", "A/Sub/nested/deep.jpg", "deep")
	env.index("src", "/A/", true)
	env.index("src", "/B/", false)
	env.startProbes()
	before := files.IndexMaintenanceStats()

	result := env.patch("move", []map[string]string{moveItem("src", "/A/Sub", "src", "/B/Sub")})
	if result.Status != http.StatusOK || result.Succeeded != 1 {
		t.Fatalf("directory move = %+v", result)
	}
	env.waitIdle()

	for _, stale := range []string{"/A/Sub/", "/A/Sub/s1.jpg", "/A/Sub/nested/", "/A/Sub/nested/deep.jpg"} {
		if item := env.item("src", stale); item != nil {
			t.Fatalf("stale source row %s: %#v", stale, item)
		}
	}
	for _, want := range []string{"/B/Sub/", "/B/Sub/s1.jpg", "/B/Sub/nested/", "/B/Sub/nested/deep.jpg"} {
		if item := env.item("src", want); item == nil {
			t.Fatalf("missing destination row %s", want)
		}
	}
	if submitted := files.IndexMaintenanceStats().Submitted - before.Submitted; submitted != 2 {
		t.Fatalf("directory move submitted %d intents, want 2", submitted)
	}
	if probe := indexing.RefreshProbeSnapshot(); probe.ScanStarted != 3 {
		t.Fatalf("directory move scans = %d, want 3 (source parent, moved tree, destination parent)", probe.ScanStarted)
	}
	if !env.exists("src", "B/Sub/nested/deep.jpg") || env.exists("src", "A/Sub") {
		t.Fatal("directory move on disk is wrong")
	}
	parent := env.item("src", "/B/")
	if parent == nil || parent.Size <= 0 {
		t.Fatalf("destination parent size was not updated: %#v", parent)
	}
	movedDir := env.item("src", "/B/Sub/")
	if movedDir == nil || !movedDir.HasPreview {
		t.Fatalf("moved directory lost its preview attribute: %#v", movedDir)
	}
}

// B07: all four index on/off combinations keep the file operation working and
// only schedule work on enabled sides.
func TestBatchMoveIndexOnOffCombinations(t *testing.T) {
	cases := []struct {
		name     string
		srcOn    bool
		dstOn    bool
		expected int64
	}{
		{"both_on", true, true, 2},
		{"source_only", true, false, 1},
		{"destination_only", false, true, 1},
		{"both_off", false, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newMoveIndexEnv(t, "s1", "s2")
			env.mkdir("s1", "A")
			env.mkdir("s2", "B")
			env.write("s1", "A/x.jpg", "x")
			if tc.srcOn {
				env.index("s1", "/A/", false)
			}
			if tc.dstOn {
				env.index("s2", "/B/", false)
			}
			env.indices["s1"].Config.ResolvedRules.IndexingDisabled = !tc.srcOn
			env.indices["s2"].Config.ResolvedRules.IndexingDisabled = !tc.dstOn
			env.startProbes()
			before := files.IndexMaintenanceStats()

			result := env.patch("move", []map[string]string{moveItem("s1", "/A/x.jpg", "s2", "/B/x.jpg")})
			if result.Status != http.StatusOK || result.Succeeded != 1 {
				t.Fatalf("move = %+v", result)
			}
			env.waitIdle()
			if submitted := files.IndexMaintenanceStats().Submitted - before.Submitted; submitted != tc.expected {
				t.Fatalf("submitted %d intents, want %d", submitted, tc.expected)
			}
			if !env.exists("s2", "B/x.jpg") || env.exists("s1", "A/x.jpg") {
				t.Fatal("file move must work regardless of indexing")
			}
			if tc.srcOn && env.item("s1", "/A/x.jpg") != nil {
				t.Fatal("enabled source row was not deleted")
			}
			if tc.dstOn && env.item("s2", "/B/x.jpg") == nil {
				t.Fatal("enabled destination was not indexed")
			}
			if !tc.srcOn && env.item("s1", "/A/x.jpg") != nil {
				t.Fatal("disabled source must not be touched")
			}
		})
	}
}

// B16: a cancelled request context must not drop the maintenance of files that
// were already moved.
func TestBatchMoveCancelledContextStillMaintains(t *testing.T) {
	env := newMoveIndexEnv(t, "src")
	env.mkdir("src", "A")
	env.mkdir("src", "B")
	for _, name := range []string{"x", "y", "z"} {
		env.write("src", "A/"+name+".jpg", name)
	}
	env.index("src", "/A/", false)
	env.startProbes()
	before := files.IndexMaintenanceStats()

	items := make([]map[string]string, 0, 3)
	for _, name := range []string{"x", "y", "z"} {
		items = append(items, moveItem("src", "/A/"+name+".jpg", "src", "/B/"+name+".jpg"))
	}
	payload, err := json.Marshal(map[string]any{"action": "move", "items": items})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPatch, "/api/resources", bytes.NewReader(payload)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	status, handlerErr := ResourcePatchHandler(rec, req, &Context{User: env.user})
	if status != http.StatusOK || handlerErr != nil {
		t.Fatalf("cancelled patch: status=%d err=%v", status, handlerErr)
	}
	env.waitIdle()
	if submitted := files.IndexMaintenanceStats().Submitted - before.Submitted; submitted != 2 {
		t.Fatalf("cancelled request submitted %d intents, want 2", submitted)
	}
	for _, name := range []string{"x", "y", "z"} {
		if env.item("src", "/A/"+name+".jpg") != nil || env.item("src", "/B/"+name+".jpg") == nil {
			t.Fatalf("cancelled request lost maintenance for %s", name)
		}
		if !env.exists("src", "B/"+name+".jpg") {
			t.Fatalf("cancelled request did not move %s", name)
		}
	}
}

// B16: a client that disconnects immediately after sending the request must
// still leave the moved files and their index consistent.
func TestBatchMoveClientDisconnectStillMaintains(t *testing.T) {
	env := newMoveIndexEnv(t, "src")
	env.mkdir("src", "A")
	env.mkdir("src", "B")
	for _, name := range []string{"x", "y"} {
		env.write("src", "A/"+name+".jpg", name)
	}
	env.index("src", "/A/", false)

	items := []map[string]string{
		moveItem("src", "/A/x.jpg", "src", "/B/x.jpg"),
		moveItem("src", "/A/y.jpg", "src", "/B/y.jpg"),
	}
	payload, err := json.Marshal(map[string]any{"action": "move", "items": items})
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(env.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", target.Host, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request := fmt.Sprintf("PATCH /api/resources HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", target.Host, len(payload))
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if env.exists("src", "B/x.jpg") && env.exists("src", "B/y.jpg") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !env.exists("src", "B/x.jpg") || !env.exists("src", "B/y.jpg") {
		t.Fatal("disconnected request did not finish the physical moves")
	}
	env.waitIdle()
	if env.item("src", "/A/x.jpg") != nil || env.item("src", "/B/x.jpg") == nil {
		t.Fatal("disconnected request lost index maintenance")
	}
}

// B18: the existing single-item move and copy calls keep their maintenance.
func TestSingleMoveAndCopyKeepMaintenance(t *testing.T) {
	env := newMoveIndexEnv(t, "src")
	env.mkdir("src", "A")
	env.mkdir("src", "B")
	env.write("src", "A/x.jpg", "x")
	env.write("src", "A/y.jpg", "y")
	env.index("src", "/A/", false)
	env.startProbes()
	before := files.IndexMaintenanceStats()

	if err := files.MoveResource(false, "src", "src", env.path("src", "A/x.jpg"), env.path("src", "B/x.jpg")); err != nil {
		t.Fatalf("single move: %v", err)
	}
	env.waitIdle()
	if submitted := files.IndexMaintenanceStats().Submitted - before.Submitted; submitted != 2 {
		t.Fatalf("single move submitted %d intents, want 2", submitted)
	}
	if env.item("src", "/A/x.jpg") != nil || env.item("src", "/B/x.jpg") == nil {
		t.Fatal("single move lost its index maintenance")
	}

	if err := files.CopyResource(false, "src", "src", env.path("src", "A/y.jpg"), env.path("src", "B/y.jpg")); err != nil {
		t.Fatalf("copy: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if env.item("src", "/B/y.jpg") != nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if env.item("src", "/B/y.jpg") == nil {
		t.Fatal("copy lost its index maintenance")
	}
	if env.item("src", "/A/y.jpg") == nil {
		t.Fatal("copy removed the source row")
	}
}

// B05 reload: the response mapping for a share-like sanitised failure is
// covered by the existing share tests; this asserts the message plumbing for
// ordinary failures stays intact.
func TestBatchMoveFailureMessagesStayReadable(t *testing.T) {
	env := newMoveIndexEnv(t, "src")
	env.mkdir("src", "A")
	env.mkdir("src", "B")
	result := env.patch("move", []map[string]string{
		moveItem("src", "/A/missing.jpg", "src", "/B/missing.jpg"),
	})
	if result.Status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", result.Status)
	}
	if len(result.Messages) != 1 || strings.TrimSpace(result.Messages[0]) == "" {
		t.Fatalf("failure message missing: %+v", result)
	}
}
