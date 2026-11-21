//go:build linux && !386 && !arm && !cacheledger_portable

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/internal/previewwarmup"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
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

func TestPreviewReuseFourThousandListingDoesNotHash(t *testing.T) {
	t.Setenv("MUMENT_MOVE_HANDLER_TRANSPORT", "1")
	env := newMoveIndexEnv(t, "src")
	env.mkdir("src", "Pictures")
	for i := 0; i < 3999; i++ {
		env.write("src", fmt.Sprintf("Pictures/text-%04d.txt", i), "synthetic")
	}
	path := env.path("src", "Pictures/000-image.jpg")
	writeMoveImage(t, path, 42)
	past := time.Now().Add(-time.Minute)
	require.NoError(t, os.Chtimes(path, past, past))
	env.index("src", "/Pictures/", false)
	server := ledgerPreviewServer(t, env)
	first := time.Now()
	_ = readMovePreview(t, env, server, "/Pictures/000-image.jpg", "small")
	firstMS := time.Since(first).Milliseconds()
	ctx, cancel := context.WithCancel(t.Context())
	cfg := settings.Config.Server
	cfg.PreviewReuse = settings.PreviewWarmupConfig{Enabled: true, Scopes: []settings.PreviewWarmupScope{{Source: "src", Directory: "Pictures"}}}
	worker := previewwarmup.NewReuse(ctx, cfg, env.db, preview.GetService())
	t.Cleanup(func() { cancel(); require.NoError(t, worker.Wait(context.Background())) })
	// Hold the same signal used by both real directory handlers across a complete
	// scheduler tick, proving that F alone enables browse activity tracking.
	rawRelease := preview.TrackWarmupBrowse()
	var once sync.Once
	release := func() { once.Do(rawRelease) }
	t.Cleanup(release)
	time.Sleep(1100 * time.Millisecond)
	require.False(t, preview.GetService().IdleForWarmup())
	require.Zero(t, worker.Snapshot().ReadBytes)
	status, listed, _, ordinaryMS := env.get("src", "/Pictures/")
	require.Equal(t, http.StatusOK, status)
	require.Len(t, listed, 4000)
	count, pages := 0, 0
	cursor, revision := "", ""
	start := time.Now()
	for {
		req := httptest.NewRequest(http.MethodGet, "/api/resources/list?source=src&path=/Pictures/&limit=500&cursor="+url.QueryEscape(cursor)+"&revision="+url.QueryEscape(revision), nil)
		rec := httptest.NewRecorder()
		code, err := resourceListingHandler(rec, req, &Context{User: env.user})
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, code)
		var page listingResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
		count += len(page.Items)
		pages++
		if !page.HasMore {
			break
		}
		require.NotEmpty(t, page.NextCursor)
		cursor = page.NextCursor
		revision = page.Revision
	}
	pagedMS := time.Since(start).Milliseconds()
	require.Equal(t, 4000, count)
	require.Equal(t, 8, pages)
	require.Zero(t, worker.Snapshot().ReadBytes)
	release()
	require.Eventually(t, func() bool { return worker.Snapshot().Counts["proven_own"] == 1 }, 10*time.Second, 100*time.Millisecond, "status=%+v", worker.Snapshot())
	previous := runtimeDeps.PreviewReuse
	runtimeDeps.PreviewReuse = worker
	t.Cleanup(func() { runtimeDeps.PreviewReuse = previous })
	auditRec := httptest.NewRecorder()
	auditCode, auditErr := previewReuseGet(auditRec, httptest.NewRequest(http.MethodGet, "/api/settings/preview-reuse?referencesAfter=", nil), &Context{User: env.user})
	require.NoError(t, auditErr)
	require.Equal(t, http.StatusOK, auditCode)
	var audit previewwarmup.RetentionPage
	require.NoError(t, json.Unmarshal(auditRec.Body.Bytes(), &audit))
	require.EqualValues(t, 1, audit.Reasons["live_verified_observation"])
	require.Equal(t, "/Pictures/000-image.jpg", audit.Samples[0].Path)
	require.NotContains(t, auditRec.Body.String(), env.root)
	badCode, badErr := previewReuseGet(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/settings/preview-reuse?referencesAfter=/bad", nil), &Context{User: env.user})
	require.Error(t, badErr)
	require.Equal(t, http.StatusBadRequest, badCode)
	t.Logf("in-process handlers: entries=4000 pages=%d ordinary_ms=%.3f paged_total_ms=%d first_preview_ms=%d F_original_bytes_during_GET=0 resumed_bytes=%d", pages, ordinaryMS, pagedMS, firstMS, worker.Snapshot().ReadBytes)
}
