//go:build linux && !386 && !arm && !cacheledger_portable

package web

import (
	"context"
	"encoding/json"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/internal/previewwarmup"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPreviewReuseAdminStatusAndPause(t *testing.T) {
	t.Setenv("MUMENT_MOVE_HANDLER_TRANSPORT", "1")
	env := newMoveIndexEnv(t, "src")
	env.mkdir("src", "Pictures")
	ledgerPreviewServer(t, env)
	ctx, cancel := context.WithCancel(t.Context())
	cfg := settings.Config.Server
	cfg.PreviewReuse = settings.PreviewWarmupConfig{Enabled: true, Scopes: []settings.PreviewWarmupScope{{Source: "src", Directory: "Pictures"}}}
	worker := previewwarmup.NewReuse(ctx, cfg, env.db, preview.GetService())
	previous := runtimeDeps.PreviewReuse
	runtimeDeps.PreviewReuse = worker
	t.Cleanup(func() {
		cancel()
		require.NoError(t, worker.Wait(context.Background()))
		runtimeDeps.PreviewReuse = previous
	})
	post := func(body string) (int, error) {
		w := httptest.NewRecorder()
		return previewReusePost(w, httptest.NewRequest(http.MethodPost, "/api/settings/preview-reuse", strings.NewReader(body)), &Context{User: env.user})
	}
	for _, bad := range []string{`{"action":"fill"}`, `{"action":"pause","path":"/other"}`, `{"action":"pause"} {}`} {
		code, err := post(bad)
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, code)
	}
	code, err := post(`{"action":"pause"}`)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	w := httptest.NewRecorder()
	code, err = previewReuseGet(w, httptest.NewRequest(http.MethodGet, "/api/settings/preview-reuse", nil), &Context{User: env.user})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	var status previewwarmup.ReuseStatus
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &status))
	require.True(t, status.Enabled)
	require.True(t, status.Paused)
	require.NotContains(t, w.Body.String(), env.root)
	require.NotContains(t, w.Body.String(), "digest")
	code, err = post(`{"action":"resume"}`)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
}
