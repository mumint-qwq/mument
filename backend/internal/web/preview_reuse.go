package web

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gtsteffaniak/filebrowser/backend/internal/previewwarmup"
)

func previewReuseGet(w http.ResponseWriter, r *http.Request, _ *Context) (int, error) {
	if runtimeDeps.PreviewReuse == nil {
		return RenderJSON(w, r, previewwarmup.ReuseStatus{Reason: "disabled"})
	}
	if r.URL.Query().Has("referencesAfter") {
		cursor := r.URL.Query().Get("referencesAfter")
		if !previewwarmup.ValidRetentionCursor(cursor) {
			return http.StatusBadRequest, previewwarmup.ErrInvalidAction
		}
		page, err := runtimeDeps.PreviewReuse.Retention(r.Context(), cursor)
		if err != nil {
			return http.StatusConflict, err
		}
		return RenderJSON(w, r, page)
	}
	return RenderJSON(w, r, runtimeDeps.PreviewReuse.Snapshot())
}
func previewReusePost(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	var command struct {
		Action string `json:"action"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&command); err != nil || decoder.Decode(new(any)) != io.EOF {
		return http.StatusBadRequest, previewwarmup.ErrInvalidAction
	}
	if runtimeDeps.PreviewReuse == nil {
		return http.StatusConflict, previewwarmup.ErrInactive
	}
	if err := runtimeDeps.PreviewReuse.Control(command.Action); err != nil {
		return http.StatusBadRequest, err
	}
	return previewReuseGet(w, r, d)
}
