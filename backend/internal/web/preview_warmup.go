package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gtsteffaniak/filebrowser/backend/internal/previewwarmup"
)

func previewWarmupGet(w http.ResponseWriter, r *http.Request, _ *Context) (int, error) {
	if runtimeDeps.PreviewWarmup == nil {
		return RenderJSON(w, r, previewwarmup.Status{Reason: "disabled"})
	}
	return RenderJSON(w, r, runtimeDeps.PreviewWarmup.Snapshot())
}

func previewWarmupPost(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	var command struct {
		Action string `json:"action"`
		Scope  int    `json:"scope"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&command); err != nil || decoder.Decode(new(any)) != io.EOF {
		return http.StatusBadRequest, previewwarmup.ErrInvalidAction
	}
	if runtimeDeps.PreviewWarmup == nil {
		return http.StatusConflict, previewwarmup.ErrInactive
	}
	if err := runtimeDeps.PreviewWarmup.Control(command.Action, command.Scope); err != nil {
		if errors.Is(err, previewwarmup.ErrInactive) {
			return http.StatusConflict, err
		}
		return http.StatusBadRequest, err
	}
	return previewWarmupGet(w, r, d)
}
