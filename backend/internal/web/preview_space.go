package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
)

func previewSpaceGet(w http.ResponseWriter, r *http.Request, _ *Context) (int, error) {
	return RenderJSON(w, r, preview.PreviewSpaceStatus())
}

func previewCapacityGet(w http.ResponseWriter, r *http.Request, _ *Context) (int, error) {
	q := r.URL.Query()
	cursor := preview.CapacityCursor{}
	for key, field := range map[string]*int64{"time": &cursor.Candidate.Time, "row": &cursor.Candidate.Row, "intent": &cursor.Intent} {
		if value := q.Get(key); value != "" {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 0 {
				return http.StatusBadRequest, errors.New("invalid capacity cursor")
			}
			*field = n
		}
	}
	if value := q.Get("used"); value != "" {
		used, err := strconv.ParseBool(value)
		if err != nil {
			return http.StatusBadRequest, errors.New("invalid capacity cursor")
		}
		cursor.Candidate.Used = used
	}
	return RenderJSON(w, r, preview.PreviewCapacityStatus(r.Context(), cursor))
}

func previewCapacityPost(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	var command struct {
		Action string `json:"action"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
		return http.StatusBadRequest, err
	}
	if command.Action != "pause" && command.Action != "resume" {
		return http.StatusBadRequest, errors.New("invalid capacity action")
	}
	if err := preview.ControlPreviewCapacity(command.Action == "pause"); err != nil {
		return http.StatusConflict, err
	}
	return previewSpaceGet(w, r, d)
}
