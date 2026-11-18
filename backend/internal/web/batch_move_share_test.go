package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/files"
	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	"github.com/gtsteffaniak/filebrowser/backend/internal/app"
	"github.com/gtsteffaniak/filebrowser/backend/internal/database/share"
	indexsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/internal/database/users"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/internal/state"
	"github.com/gtsteffaniak/filebrowser/backend/internal/utils"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"golang.org/x/crypto/bcrypt"
)

// B19: a move inside a public share must go through the real share middleware
// (hash lookup, path scoping, response sanitisation) over real HTTP, and the
// index must still be maintained exactly like an authenticated move.
func TestPublicShareMoveOverRealHTTP(t *testing.T) {
	utils.BcryptCost = bcrypt.MinCost
	prevBaseURL := settings.Config.Http.BaseURL
	prevCacheDir := settings.Config.Server.CacheDir
	prevSourceMap := settings.Config.Server.SourceMap
	prevNameToSource := settings.Config.Server.NameToSource
	prevFilePerm := fileutils.PermFile
	prevDirPerm := fileutils.PermDir
	settings.Config.Http.BaseURL = "/"
	fileutils.SetFsPermissions(0o644, 0o755)
	measurePreviewOnce.Do(func() {
		previewCache, err := os.MkdirTemp("", "batch-move-share-preview")
		if err == nil {
			_ = preview.StartPreviewGenerator(1, previewCache)
		}
	})

	root := t.TempDir()
	shared := filepath.Join(root, "shared")
	for _, dir := range []string{filepath.Join(shared, "a"), filepath.Join(shared, "b")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeMoveImage(t, filepath.Join(shared, "a", "x.jpg"), 118)
	writeMoveImage(t, filepath.Join(root, "outside.jpg"), 119)
	if err := preview.StartPreviewGenerator(2, filepath.Join(root, "previews")); err != nil {
		t.Fatal(err)
	}

	statePath := filepath.Join(root, "state.sqlite")
	if _, err := state.Initialize(statePath); err != nil {
		t.Fatal(err)
	}
	app.MustWireServices(state.Default())

	cacheDir := filepath.Join(root, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings.Config.Server.CacheDir = cacheDir
	db, _, err := indexsql.NewIndexDB("sharemove", "OFF", 1000, 32, true)
	if err != nil {
		t.Fatal(err)
	}
	indexing.SetIndexDBForTesting(db)
	idx := indexing.SetTestIndexWithDB("srv", root, db)

	settings.Config.Server.SourceMap = map[string]*settings.Source{root: {Path: root, Name: "srv"}}
	settings.Config.Server.NameToSource = map[string]*settings.Source{"srv": settings.Config.Server.SourceMap[root]}
	settings.InitializeUserResolvers()

	fullPerms := users.SourceFilePermissions{View: true, Download: true, Modify: true, Create: true, Delete: true}
	owner := &users.User{
		FrontendUser: users.FrontendUser{
			Username:    "share_owner",
			Permissions: users.Permissions{Share: true},
		},
		BackendScopes:            []users.BackendScope{{Path: root, Scope: "/", Permissions: fullPerms}},
		BackendSourcePermissions: map[string]users.SourceFilePermissions{root: fullPerms},
		Version:                  users.SourcePermissionsMigrationVersion,
	}
	if err := state.CreateUser(owner, ""); err != nil {
		t.Fatal(err)
	}
	storedOwner, err := state.GetUserByUsername("share_owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.AllowUser(root, utils.IndexPathFromNormalized("/shared", true), "share_owner"); err != nil {
		t.Fatal(err)
	}
	const shareHash = "share_move_hash_01"
	link := &share.Share{
		ShareSettings: share.ShareSettings{
			FrontendShareInfo: share.FrontendShareInfo{ShareType: "normal", AllowModify: true},
			ShareLimits:       share.ShareLimits{SourceName: "srv"},
		},
		ShareColumns: share.ShareColumns{Hash: shareHash, Path: "/shared/"},
		SourcePath:   root,
		UserID:       storedOwner.ID,
		Version:      1,
	}
	if err := state.CreateShare(link); err != nil {
		t.Fatal(err)
	}
	if err := idx.RefreshDirectory("/shared/a/", false); err != nil {
		t.Fatal(err)
	}
	if err := idx.RefreshDirectory("/shared/b/", false); err != nil {
		t.Fatal(err)
	}

	router := http.NewServeMux()
	api := http.NewServeMux()
	publicRoutes := http.NewServeMux()
	publicApi := http.NewServeMux()
	configureHTTPRouter(router, api, publicRoutes, publicApi)
	srv := httptest.NewServer(router)
	client := &http.Client{Timeout: 30 * time.Second}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = files.WaitForIndexMaintenance(ctx)
		cancel()
		srv.Close()
		previewCtx, cancelPreview := context.WithTimeout(context.Background(), 5*time.Second)
		if err := preview.Shutdown(previewCtx); err != nil {
			t.Error(err)
		}
		cancelPreview()
		indexing.ClearTestIndices()
		indexing.SetIndexDBForTesting(nil)
		_ = db.Close()
		state.Close()
		fileutils.SetFsPermissions(uint32(prevFilePerm), uint32(prevDirPerm))
		settings.Config.Http.BaseURL = prevBaseURL
		settings.Config.Server.CacheDir = prevCacheDir
		settings.Config.Server.SourceMap = prevSourceMap
		settings.Config.Server.NameToSource = prevNameToSource
	})

	getPreview := func(path string) (int, []byte) {
		t.Helper()
		q := url.Values{"hash": {shareHash}, "path": {path}, "size": {"small"}}
		resp, err := client.Get(srv.URL + "/public/api/resources/preview?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, data
	}
	status, previewBefore := getPreview("a/x.jpg")
	if status != http.StatusOK {
		t.Fatalf("public source preview status %d: %s", status, previewBefore)
	}
	warm := preview.GetService().Stats().FullDecodes
	if warm != 1 {
		t.Fatalf("expected real preview decode, got %d", warm)
	}
	forbidden, detail := getPreview("../outside.jpg")
	if forbidden == http.StatusOK || bytes.Contains(detail, []byte(root)) {
		t.Fatalf("share preview outside scope: status=%d body=%s", forbidden, detail)
	}

	payload, err := json.Marshal(map[string]any{
		"action": "move",
		"items":  []map[string]string{{"fromPath": "a/x.jpg", "toPath": "b/x.jpg"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	patchURL := srv.URL + "/public/api/resources?hash=" + shareHash
	resp, err := client.Do(mustRequest(t, http.MethodPatch, patchURL, payload))
	if err != nil {
		t.Fatalf("public patch: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("public patch status = %d body=%s", resp.StatusCode, string(raw))
	}
	if bytes.Contains(raw, []byte("fromPath")) || bytes.Contains(raw, []byte("a/x.jpg")) || bytes.Contains(raw, []byte("srv")) {
		t.Fatalf("public response leaked paths or source names: %s", string(raw))
	}
	var body struct {
		Succeeded []map[string]any `json:"succeeded"`
		Failed    []map[string]any `json:"failed"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Succeeded) != 1 || len(body.Failed) != 0 {
		t.Fatalf("public move response = %s", string(raw))
	}
	status, previewAfter := getPreview("b/x.jpg")
	if status != http.StatusOK || !bytes.Equal(previewBefore, previewAfter) {
		t.Fatalf("public moved preview status=%d equal=%t", status, bytes.Equal(previewBefore, previewAfter))
	}
	if after := preview.GetService().Stats().FullDecodes; runtime.GOOS == "linux" && runtime.GOARCH != "386" && runtime.GOARCH != "arm" && after != warm {
		t.Fatalf("public move redecoded preview: %d -> %d", warm, after)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := files.WaitForIndexMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(shared, "b", "x.jpg")); err != nil {
		t.Fatalf("file was not moved inside the share: %v", err)
	}
	if _, err := os.Stat(filepath.Join(shared, "a", "x.jpg")); err == nil {
		t.Fatal("file still exists at the share source")
	}
	if item, _ := db.GetItem("srv", "/shared/a/x.jpg"); item != nil {
		t.Fatalf("stale index row after share move: %#v", item)
	}
	if item, _ := db.GetItem("srv", "/shared/b/x.jpg"); item == nil {
		t.Fatal("destination row was not indexed after share move")
	}

	// Path traversal must be rejected by the real share request path.
	escape, err := json.Marshal(map[string]any{
		"action": "move",
		"items":  []map[string]string{{"fromPath": "b/x.jpg", "toPath": "../../escape.jpg"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	escapeResp, err := client.Do(mustRequest(t, http.MethodPatch, patchURL, escape))
	if err != nil {
		t.Fatalf("escape patch: %v", err)
	}
	defer escapeResp.Body.Close()
	if escapeResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("path traversal status = %d, want 400", escapeResp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, "escape.jpg")); err == nil {
		t.Fatal("path traversal wrote outside the share")
	}
}
