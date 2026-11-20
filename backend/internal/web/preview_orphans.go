package web

import (
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"

	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
)

func previewOrphansGet(w http.ResponseWriter, r *http.Request, _ *Context) (int, error) {
	var cursor [2]int64
	for i, field := range []string{"afterCandidate", "afterIntent"} {
		if raw := r.URL.Query().Get(field); raw != "" {
			var err error
			cursor[i], err = strconv.ParseInt(raw, 10, 64)
			if err != nil || cursor[i] < 0 {
				return http.StatusBadRequest, errors.New("invalid orphan status cursor")
			}
		}
	}
	object := r.URL.Query().Get("afterObject")
	if object != "" {
		if _, err := hex.DecodeString(object); len(object) != 64 || err != nil {
			return http.StatusBadRequest, errors.New("invalid orphan object cursor")
		}
	}
	return RenderJSON(w, r, preview.PreviewOrphanStatus(r.Context(), preview.OrphanCursor{
		Candidate: cursor[0], Intent: cursor[1], Object: object,
	}))
}
