//go:build linux && !386 && !arm

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/internal/previewwarmup"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
)

func TestPreviewWarmupHTTPHandlers(t *testing.T) {
	t.Setenv("MUMENT_MOVE_HANDLER_TRANSPORT", "1")
	env := newMoveIndexEnv(t, "src")
	server := ledgerPreviewServer(t, env)
	for _, dir := range []string{"Pictures", "Pictures-old"} {
		env.mkdir("src", dir)
		img := image.NewGray(image.Rect(0, 1100, 1110, 2000))
		_, err := rand.New(rand.NewSource(162)).Read(img.Pix)
		require.NoError(t, err)
		var b bytes.Buffer
		require.NoError(t, png.Encode(&b, img))
		path := env.path("src", dir+"/image.png")
		require.NoError(t, os.WriteFile(path, b.Bytes(), 0o644))
		past := time.Now().Add(-time.Minute)
		require.NoError(t, os.Chtimes(path, past, past))
		env.index("src", "/"+dir+"/", false)
	}

	svc := preview.GetService()
	ctx, cancel := context.WithCancel(t.Context())
	cfg := settings.Config.Server
	cfg.PreviewWarmup = settings.PreviewWarmupConfig{Enabled: true,
		Scopes: []settings.PreviewWarmupScope{{Source: "src", Directory: "Pictures"}}}
	worker := previewwarmup.New(ctx, cfg, env.db, svc)
	previous := runtimeDeps.PreviewWarmup
	runtimeDeps.PreviewWarmup = worker
	t.Cleanup(func() {
		cancel()
		worker.Wait()
		runtimeDeps.PreviewWarmup = previous
	})
	getStatus := func() previewwarmup.Status {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/settings/preview-warmup", nil)
		w := httptest.NewRecorder()
		code, err := previewWarmupGet(w, r, &Context{})
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, code)
		var status previewwarmup.Status
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &status))
		return status
	}
	require.Eventually(t, func() bool { return worker.Snapshot().Generated == 1 }, 12*time.Second, 100*time.Millisecond,
		"worker status: %+v", worker.Snapshot())
	before := getStatus()
	require.EqualValues(t, 1, before.FullDecodes)
	data := readMovePreview(t, env, server, "/Pictures/image.png", "xlarge")
	meta, err := png.DecodeConfig(bytes.NewReader(data))
	require.NoError(t, err)
	require.Equal(t, 1024, meta.Width)
	require.EqualValues(t, before.FullDecodes, getStatus().FullDecodes)
	require.NotEmpty(t, readMovePreview(t, env, server, "/Pictures/image.png", "large"))
	require.EqualValues(t, 2, getStatus().FullDecodes, "large remains on demand")
	var siblingDone int
	require.NoError(t, env.db.QueryRow("SELECT warmup_guard FROM index_items WHERE path='/Pictures-old/image.png'").Scan(&siblingDone))
	require.Zero(t, siblingDone)

	r := httptest.NewRequest(http.MethodPost, "/api/settings/preview-warmup", bytes.NewBufferString(`{"action":"pause"}`))
	w := httptest.NewRecorder()
	code, err := previewWarmupPost(w, r, &Context{})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	require.True(t, getStatus().Paused)

	r = httptest.NewRequest(http.MethodPost, "/api/settings/preview-warmup", bytes.NewBufferString(`{"action":"fill","scope":2}`))
	w = httptest.NewRecorder()
	code, err = previewWarmupPost(w, r, &Context{})
	require.ErrorIs(t, err, previewwarmup.ErrInvalidAction)
	require.Equal(t, http.StatusBadRequest, code)
}
