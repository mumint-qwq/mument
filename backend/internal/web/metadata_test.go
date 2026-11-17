package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/files"
	"github.com/gtsteffaniak/filebrowser/backend/internal/database/share"
	"github.com/gtsteffaniak/filebrowser/backend/internal/database/users"
	apperrors "github.com/gtsteffaniak/filebrowser/backend/internal/errors"
	"github.com/gtsteffaniak/filebrowser/backend/internal/utils"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

func metadataRequest(t *testing.T, body string) (metadataSubsetRequest, error) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/resources/metadata", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	return decodeMetadataSubsetRequest(w, r)
}

func TestDecodeMetadataSubsetRequest(t *testing.T) {
	req, err := metadataRequest(t, "{\"source\":\" pictures \",\"paths\":[\" /videos/a.mp4 \",\"\",\"/videos/a.mp4\",\"/music/b.mp3\"],\"albumArt\":true}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Source != "pictures" {
		t.Fatalf("source = %q", req.Source)
	}
	if len(req.Paths) != 2 || req.Paths[0] != "/videos/a.mp4" || req.Paths[1] != "/music/b.mp3" {
		t.Fatalf("paths = %#v", req.Paths)
	}
	if !req.AlbumArt {
		t.Fatal("albumArt = false, want true")
	}
}

func TestDecodeMetadataSubsetRequestRequiresPaths(t *testing.T) {
	_, err := metadataRequest(t, "{\"source\":\"pictures\",\"paths\":[]}")
	if !errors.Is(err, apperrors.ErrInvalidRequestParams) {
		t.Fatalf("error = %v, want ErrInvalidRequestParams", err)
	}
}

func TestDecodeMetadataSubsetRequestCapsPaths(t *testing.T) {
	paths := make([]string, files.MetadataSubsetMaxPaths+1)
	for i := range paths {
		paths[i] = fmt.Sprintf("/videos/%d.mp4", i)
	}
	body, err := json.Marshal(metadataSubsetRequest{Source: "pictures", Paths: paths})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = metadataRequest(t, string(body))
	if !errors.Is(err, apperrors.ErrInvalidRequestParams) {
		t.Fatalf("error = %v, want ErrInvalidRequestParams", err)
	}
}

func TestDecodeMetadataSubsetRequestRejectsTraversal(t *testing.T) {
	_, err := metadataRequest(t, "{\"source\":\"pictures\",\"paths\":[\"../secrets.mp4\"]}")
	if err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}

func TestDecodeMetadataSubsetRequestRejectsInvalidJSON(t *testing.T) {
	if _, err := metadataRequest(t, "{\"source\":"); err == nil {
		t.Fatal("expected invalid JSON to be rejected")
	}
}

func TestShareRelativeMetadataPaths(t *testing.T) {
	response := files.MetadataResponse{Items: []files.MetadataItem{
		{Path: "/share/videos/a.mp4"},
		{Path: "/share"},
		{Path: "/other/b.mp4"},
	}}
	shareRelativeMetadataPaths(response, "/share/")
	if response.Items[0].Path != "/videos/a.mp4" {
		t.Fatalf("item 0 path = %q, want /videos/a.mp4", response.Items[0].Path)
	}
	if response.Items[1].Path != "/" {
		t.Fatalf("item 1 path = %q, want /", response.Items[1].Path)
	}
	if response.Items[2].Path != "/other/b.mp4" {
		t.Fatalf("item 2 path = %q, want unchanged", response.Items[2].Path)
	}
}

func TestPublicMetadataSubsetHandlerBuildsShareRelativePaths(t *testing.T) {
	originalMap := settings.Config.Server.SourceMap
	settings.Config.Server.SourceMap = map[string]*settings.Source{
		"/share": {Name: "src"},
	}
	t.Cleanup(func() { settings.Config.Server.SourceMap = originalMap })

	origSubset := MetadataSubsetFunc
	t.Cleanup(func() { MetadataSubsetFunc = origSubset })
	var gotPaths []string
	var gotOpts utils.FileOptions
	MetadataSubsetFunc = func(_ context.Context, opts utils.FileOptions, _ *users.User, paths []string, _ bool) (files.MetadataResponse, error) {
		gotOpts = opts
		gotPaths = append([]string(nil), paths...)
		return files.MetadataResponse{Items: []files.MetadataItem{
			{Path: paths[0], Name: "a.mp4", Metadata: &iteminfo.MediaMetadata{Duration: 5}},
		}}, nil
	}

	d := &Context{
		Share: share.Share{
			ShareColumns: share.ShareColumns{Hash: "sharehash"},
			SourcePath:   "/share",
		},
		ShareUser: &users.User{},
		IndexPath: "/share/",
	}
	body := strings.NewReader("{\"paths\":[\"/videos/a.mp4\"]}")
	req := httptest.NewRequest(http.MethodPost, "/public/api/resources/metadata?hash=sharehash", body)
	rec := httptest.NewRecorder()

	status, err := publicMetadataSubsetHandler(rec, req, d)
	if err != nil || status != http.StatusOK {
		t.Fatalf("status = %d, err = %v", status, err)
	}
	if len(gotPaths) != 1 || gotPaths[0] != "/share/videos/a.mp4" {
		t.Fatalf("resolved paths = %#v, want share-prefixed path", gotPaths)
	}
	if gotOpts.Source != "src" || gotOpts.FollowSymlinks {
		t.Fatalf("opts = %+v, want source src without symlink following", gotOpts)
	}
	var response files.MetadataResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].Path != "/videos/a.mp4" {
		t.Fatalf("response items = %+v, want share-relative path", response.Items)
	}
}

func TestDecodeMetadataSubsetRequestCanOmitSource(t *testing.T) {
	req, err := metadataRequest(t, "{\"paths\":[\"/a.mp4\"]}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Source != "" {
		t.Fatalf("source = %q, want empty for handler-level validation", req.Source)
	}
}
