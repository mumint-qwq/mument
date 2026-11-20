//go:build linux && !386 && !arm && !cacheledger_portable

package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
)

func TestPreviewSpaceHTTPHandlers(t *testing.T) {
	t.Setenv("MUMENT_MOVE_HANDLER_TRANSPORT", "1")
	env := newMoveIndexEnv(t, "src")
	previous := settings.Config.Server.PreviewSpace
	t.Cleanup(func() { settings.Config.Server.PreviewSpace = previous })
	settings.Config.Server.PreviewSpace = settings.PreviewSpaceConfig{
		Mode: "observe", BudgetMiB: 10, MinimumFreeMiB: 1, ResumeFreeMiB: 2, StableSeconds: 1,
	}
	ledgerPreviewServer(t, env)

	get := func(handler handleFunc, path string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		code, err := handler(w, httptest.NewRequest(http.MethodGet, path, nil), &Context{User: env.user})
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, code)
		return w
	}
	post := func(action string) (*httptest.ResponseRecorder, int, error) {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/settings/preview-capacity", strings.NewReader(action))
		code, err := previewCapacityPost(w, r, &Context{User: env.user})
		return w, code, err
	}
	var status preview.SpaceStatus
	require.NoError(t, json.Unmarshal(get(previewSpaceGet, "/api/settings/preview-space").Body.Bytes(), &status))
	require.Equal(t, "observe", status.Mode)
	require.False(t, status.E3Enabled)
	var trial preview.CapacityReport
	require.NoError(t, json.Unmarshal(get(previewCapacityGet, "/api/settings/preview-capacity?time=0&row=0&used=false&intent=0&audit=0").Body.Bytes(), &trial))
	require.True(t, trial.Tracking)
	require.False(t, trial.Enabled)
	require.Zero(t, trial.Unlinked)
	_, code, err := post(`{"action":"pause"}`)
	require.Error(t, err)
	require.Equal(t, http.StatusConflict, code)

	settings.Config.Server.PreviewSpace.Mode = "enforce"
	settings.Config.Server.PreviewSpace.CapacityEviction = true
	require.NoError(t, preview.Shutdown(context.Background()))
	require.NoError(t, preview.StartPreviewGeneratorWithBudget(1, 128, filepath.Join(env.root, "previews")))
	w, code, err := post(`{"action":"pause"}`)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &status))
	require.True(t, status.CapacityPaused)
	w, code, err = post(`{"action":"resume"}`)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &status))
	require.False(t, status.CapacityPaused)

	w = httptest.NewRecorder()
	code, err = previewCapacityGet(w, httptest.NewRequest(http.MethodGet, "/api/settings/preview-capacity?row=-1", nil), &Context{User: env.user})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, code)
}
