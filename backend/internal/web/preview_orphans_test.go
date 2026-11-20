//go:build linux && !386 && !arm && !cacheledger_portable

package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/files"
	"github.com/gtsteffaniak/filebrowser/backend/internal/database/share"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/webdav"
)

func TestPreviewOrphanSingleDeleteHTTP(t *testing.T) {
	t.Setenv("MUMENT_MOVE_HANDLER_TRANSPORT", "1")
	env := newMoveIndexEnv(t, "src")
	server := ledgerPreviewServer(t, env)
	const file = "delete.jpg"
	writeMoveImage(t, env.path("src", file), 7)
	env.index("src", "/", false)
	require.NotEmpty(t, readMovePreview(t, env, server, "/"+file, "small"))
	before := preview.PreviewOrphanStatus(t.Context(), preview.OrphanCursor{})
	require.True(t, before.Tracking)
	require.False(t, before.Enabled)
	require.Empty(t, before.RecentEvents)

	query := url.Values{"source": {"src"}, "path": {"/" + file}}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/resources?"+query.Encode(), nil)
	status, err := resourceDeleteHandler(httptest.NewRecorder(), req, &Context{User: env.user})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.NoFileExists(t, env.path("src", file))

	after := preview.PreviewOrphanStatus(t.Context(), preview.OrphanCursor{})
	require.Len(t, after.RecentEvents, 1)
	require.Equal(t, "delete", after.RecentEvents[0].Reason)
	require.Equal(t, "confirmed", after.RecentEvents[0].State)
	require.Equal(t, 1, after.Waiting)
	require.Zero(t, after.Unlinked)
}

func TestPreviewOrphanBulkAndDirectoryDeleteHTTP(t *testing.T) {
	t.Setenv("MUMENT_MOVE_HANDLER_TRANSPORT", "1")
	env := newMoveIndexEnv(t, "src")
	server := ledgerPreviewServer(t, env)
	env.mkdir("src", "album/sub")
	env.mkdir("src", "album-retained")
	for i, file := range []string{"single.jpg", "album/sub/photo.jpg", "album-retained/keep.jpg", "kept.jpg"} {
		writeMoveImage(t, env.path("src", file), int64(10+i))
	}
	env.index("src", "/", true)
	for _, file := range []string{"single.jpg", "album/sub/photo.jpg", "album-retained/keep.jpg", "kept.jpg"} {
		readMovePreview(t, env, server, "/"+file, "small")
	}

	body := `[{"source":"src","path":"/single.jpg"},{"source":"src","path":"/album"},{"source":"src","path":"/"}]`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/resources/bulk", strings.NewReader(body))
	rec := httptest.NewRecorder()
	status, err := ResourceBulkDeleteHandler(rec, req, &Context{User: env.user})
	require.NoError(t, err)
	require.Equal(t, http.StatusMultiStatus, status)
	var result BulkDeleteResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.Len(t, result.Succeeded, 2)
	require.Len(t, result.Failed, 1)
	require.NoFileExists(t, env.path("src", "single.jpg"))
	require.NoDirExists(t, env.path("src", "album"))
	require.FileExists(t, env.path("src", "album-retained/keep.jpg"))

	report := preview.PreviewOrphanStatus(t.Context(), preview.OrphanCursor{})
	require.Equal(t, 2, report.Waiting)
	require.Len(t, report.RecentEvents, 2)
	require.Equal(t, "directory_delete", report.RecentEvents[0].Reason)
	require.Equal(t, "delete", report.RecentEvents[1].Reason)

	denied := &Context{User: env.user, ShareUser: env.user, Share: share.Share{Hash: "synthetic", AllowDelete: false}}
	req = httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/public/api/resources/bulk",
		strings.NewReader(`[{"source":"src","path":"/kept.jpg"}]`))
	status, err = publicBulkDeleteHandler(httptest.NewRecorder(), req, denied)
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, status)
	require.FileExists(t, env.path("src", "kept.jpg"))
	require.Len(t, preview.PreviewOrphanStatus(t.Context(), preview.OrphanCursor{}).RecentEvents, 2)
}

func TestPreviewOrphanUploadCommitHTTP(t *testing.T) {
	t.Setenv("MUMENT_MOVE_HANDLER_TRANSPORT", "1")
	env := newMoveIndexEnv(t, "src")
	server := ledgerPreviewServer(t, env)
	for i, name := range []string{"plain.jpg", "failed.jpg", "direct.jpg", "chunk.jpg"} {
		writeMoveImage(t, env.path("src", name), int64(i+20))
	}
	env.index("src", "/", true)
	for _, name := range []string{"plain.jpg", "failed.jpg", "direct.jpg", "chunk.jpg"} {
		readMovePreview(t, env, server, "/"+name, "small")
	}
	post := func(name string, reader io.Reader, headers map[string]string) (int, error) {
		t.Helper()
		q := url.Values{"source": {"src"}, "path": {"/" + name}, "override": {"true"}}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/resources?"+q.Encode(), reader)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		return ResourcePostHandler(httptest.NewRecorder(), req, &Context{User: env.user})
	}
	status, err := post("failed.jpg", &errAfterReader{data: []byte("partial"), err: io.ErrUnexpectedEOF}, nil)
	require.Error(t, err)
	require.GreaterOrEqual(t, status, 400)
	report := preview.PreviewOrphanStatus(t.Context(), preview.OrphanCursor{})
	require.Zero(t, report.Waiting)
	require.Len(t, report.RecentEvents, 1)
	require.Equal(t, "unproven", report.RecentEvents[0].State)
	require.Equal(t, "overwrite", report.RecentEvents[0].Reason)

	status, err = post("plain.jpg", strings.NewReader("new plain"), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, 1, preview.PreviewOrphanStatus(t.Context(), preview.OrphanCursor{}).Waiting)
	require.FileExists(t, env.path("src", "plain.jpg"))

	status, err = post("direct.jpg", strings.NewReader("short"), map[string]string{
		"X-File-Total-Size": "6", "X-File-Upload-Session": "e2-direct",
	})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, 1, preview.PreviewOrphanStatus(t.Context(), preview.OrphanCursor{}).Waiting)
	status, err = post("direct.jpg", strings.NewReader("direct"), map[string]string{
		"X-File-Total-Size": "6", "X-File-Upload-Session": "e2-direct",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, 2, preview.PreviewOrphanStatus(t.Context(), preview.OrphanCursor{}).Waiting)

	chunk := func(offset, data string, readErr error) (int, error) {
		var body io.Reader = strings.NewReader(data)
		if readErr != nil {
			body = &errAfterReader{data: []byte(data), err: readErr}
		}
		return post("chunk.jpg", body, map[string]string{
			"X-File-Chunk-Offset": offset, "X-File-Total-Size": "8", "X-File-Upload-Session": "e2-chunk",
		})
	}
	status, err = chunk("0", "aaaa", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	status, err = chunk("4", "bb", io.ErrUnexpectedEOF)
	require.Error(t, err)
	require.GreaterOrEqual(t, status, 400)
	require.Equal(t, 2, preview.PreviewOrphanStatus(t.Context(), preview.OrphanCursor{}).Waiting)
	status, err = chunk("4", "bbbb", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	report = preview.PreviewOrphanStatus(t.Context(), preview.OrphanCursor{})
	require.Equal(t, 3, report.Waiting)
	require.Len(t, report.RecentEvents, 4)
	require.Equal(t, "replace_target", report.RecentEvents[0].Reason)
	require.Equal(t, "confirmed", report.RecentEvents[0].State)
	for name, expected := range map[string]string{"plain.jpg": "new plain", "direct.jpg": "direct", "chunk.jpg": "aaaabbbb"} {
		data, err := os.ReadFile(env.path("src", name))
		require.NoError(t, err)
		require.Equal(t, expected, string(data))
	}
}

func TestPreviewOrphanWebDAVDeleteRetainsUnprovenPreview(t *testing.T) {
	t.Setenv("MUMENT_MOVE_HANDLER_TRANSPORT", "1")
	env := newMoveIndexEnv(t, "src")
	server := ledgerPreviewServer(t, env)
	file := env.path("src", "dav.jpg")
	writeMoveImage(t, file, 30)
	env.index("src", "/", false)
	readMovePreview(t, env, server, "/dav.jpg", "small")
	ffs := &filteredFileSystem{fs: webdav.Dir(env.paths["src"]), source: "src", user: env.user,
		filePerms: env.user.BackendSourcePermissions[env.paths["src"]]}
	require.NoError(t, ffs.RemoveAll(t.Context(), "/dav.jpg"))
	require.NoFileExists(t, file)
	report := preview.PreviewOrphanStatus(t.Context(), preview.OrphanCursor{})
	require.Empty(t, report.RecentEvents)
	require.Zero(t, report.ConfirmedAssets)
}

func TestPreviewOrphanMoveOverwriteTransfersIncoming(t *testing.T) {
	t.Setenv("MUMENT_MOVE_HANDLER_TRANSPORT", "1")
	env := newMoveIndexEnv(t, "src")
	server := ledgerPreviewServer(t, env)
	incoming, target := env.path("src", "incoming.jpg"), env.path("src", "target.jpg")
	writeMoveImage(t, incoming, 31)
	writeMoveImage(t, target, 32)
	env.index("src", "/", true)
	data := readMovePreview(t, env, server, "/incoming.jpg", "small")
	require.NotEqual(t, data, readMovePreview(t, env, server, "/target.jpg", "small"))
	before := preview.GetService().Stats().FullDecodes
	require.NoError(t, files.MoveResource(false, "src", "src", incoming, target))
	require.Equal(t, data, readMovePreview(t, env, server, "/target.jpg", "small"))
	require.Equal(t, before, preview.GetService().Stats().FullDecodes)
	r := preview.PreviewOrphanStatus(t.Context(), preview.OrphanCursor{})
	require.Equal(t, 1, r.Waiting)
	require.Len(t, r.RecentEvents, 1)
	require.Equal(t, "replace_target", r.RecentEvents[0].Reason)
	require.Equal(t, "confirmed", r.RecentEvents[0].State)
}

func TestPreviewOrphanPartialDirectoryPreservesUnproven(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires filesystem permission denial")
	}
	t.Setenv("MUMENT_MOVE_HANDLER_TRANSPORT", "1")
	env := newMoveIndexEnv(t, "src")
	server := ledgerPreviewServer(t, env)
	env.mkdir("src", "partial/denied")
	writeMoveImage(t, env.path("src", "partial/photo.jpg"), 33)
	writeMoveImage(t, env.path("src", "partial/denied/keep.jpg"), 34)
	env.index("src", "/", true)
	readMovePreview(t, env, server, "/partial/photo.jpg", "small")
	readMovePreview(t, env, server, "/partial/denied/keep.jpg", "small")
	denied := env.path("src", "partial/denied")
	require.NoError(t, os.Chmod(denied, 0))
	t.Cleanup(func() { require.NoError(t, os.Chmod(denied, 0o700)) })
	require.Error(t, files.DeleteFiles("src", env.path("src", "partial"), true))
	require.NoFileExists(t, env.path("src", "partial/photo.jpg"))
	r := preview.PreviewOrphanStatus(t.Context(), preview.OrphanCursor{})
	require.Empty(t, r.RecentEvents)
	require.Zero(t, r.ConfirmedAssets)
}
