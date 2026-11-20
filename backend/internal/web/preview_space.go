package web

import (
	"net/http"

	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
)

func previewSpaceGet(w http.ResponseWriter, r *http.Request, _ *Context) (int, error) {
	return RenderJSON(w, r, preview.PreviewSpaceStatus())
}
