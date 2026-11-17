package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/files"
	"github.com/gtsteffaniak/filebrowser/backend/internal/database/users"
	"github.com/gtsteffaniak/filebrowser/backend/internal/utils"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

const metadataSubsetBodyLimit = 256 * 1024

type metadataSubsetRequest struct {
	Source   string   `json:"source"`
	Paths    []string `json:"paths"`
	AlbumArt bool     `json:"albumArt"`
}

// MetadataSubsetFunc is mockable in HTTP tests.
var MetadataSubsetFunc = files.GetMetadataSubset

// decodeMetadataSubsetRequest validates the shared body for the authenticated
// and public subset endpoints.
func decodeMetadataSubsetRequest(w http.ResponseWriter, r *http.Request) (metadataSubsetRequest, error) {
	var req metadataSubsetRequest
	body := http.MaxBytesReader(w, r.Body, metadataSubsetBodyLimit)
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		return req, fmt.Errorf("invalid request body")
	}
	req.Source = strings.TrimSpace(req.Source)
	paths, err := files.NormalizeMetadataPaths(req.Paths, files.MetadataSubsetMaxPaths)
	if err != nil {
		return req, err
	}
	for i, p := range paths {
		clean, err := utils.SanitizePath(p)
		if err != nil {
			return req, fmt.Errorf("invalid path: %v", err)
		}
		paths[i] = clean
	}
	req.Paths = paths
	return req, nil
}

func metadataSubsetHandler(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	req, err := decodeMetadataSubsetRequest(w, r)
	if err != nil {
		return http.StatusBadRequest, err
	}
	if req.Source == "" {
		return http.StatusBadRequest, fmt.Errorf("source is required")
	}
	filePerms, err := effectiveFilePerms(d, req.Source)
	if err != nil {
		return http.StatusForbidden, err
	}
	if !filePerms.View {
		return http.StatusForbidden, fmt.Errorf("user is not allowed to view files in this source")
	}
	return renderMetadataSubset(w, r, d, utils.FileOptions{
		Source:         req.Source,
		FollowSymlinks: true,
		ShowHidden:     d.User.ShowHidden,
		HideFileExt:    d.User.HideFileExt,
	}, d.User, req.Paths, req.AlbumArt, "")
}

// publicMetadataSubsetHandler serves metadata for a bounded set of files inside
// a share. Body paths are interpreted relative to the share root, matching the
// item paths returned by the public listing.
func publicMetadataSubsetHandler(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	if d.Share.ShareType == "upload" {
		return http.StatusNotImplemented, fmt.Errorf("browsing is disabled for upload shares")
	}
	sourceInfo, ok := settings.Config.Server.SourceMap[d.Share.SourcePath]
	if !ok {
		return http.StatusNotFound, fmt.Errorf("source not found")
	}
	filePerms, err := effectiveFilePerms(d, sourceInfo.Name)
	if err != nil {
		return http.StatusForbidden, err
	}
	if !filePerms.View {
		return http.StatusForbidden, fmt.Errorf("user is not allowed to view files in this source")
	}
	if d.ShareUser == nil {
		return http.StatusForbidden, fmt.Errorf("share user not available")
	}
	req, err := decodeMetadataSubsetRequest(w, r)
	if err != nil {
		return http.StatusBadRequest, err
	}
	paths := make([]string, len(req.Paths))
	for i, p := range req.Paths {
		paths[i] = utils.JoinScopedIndexPath(d.IndexPath, p)
	}
	return renderMetadataSubset(w, r, d, utils.FileOptions{
		Source:         sourceInfo.Name,
		FollowSymlinks: false,
		ShowHidden:     d.Share.ShowHidden,
		HideFileExt:    d.Share.HideFileExt,
	}, d.ShareUser, paths, req.AlbumArt, d.IndexPath)
}

func renderMetadataSubset(w http.ResponseWriter, r *http.Request, d *Context, opts utils.FileOptions, user *users.User, paths []string, albumArt bool, sharePrefix string) (int, error) {
	ctx, cancel := context.WithTimeout(r.Context(), files.MetadataSubsetRequestTimeout)
	defer cancel()
	response, err := MetadataSubsetFunc(ctx, opts, user, paths, albumArt)
	if err != nil {
		return ErrToStatus(err), err
	}
	shareRelativeMetadataPaths(response, sharePrefix)
	return RenderJSON(w, r, response)
}

// shareRelativeMetadataPaths rewrites index paths back to the share-relative
// paths the public client requested.
func shareRelativeMetadataPaths(response files.MetadataResponse, sharePrefix string) {
	prefix := strings.TrimSuffix(sharePrefix, "/")
	if prefix == "" {
		return
	}
	for i := range response.Items {
		p := response.Items[i].Path
		if p == prefix {
			response.Items[i].Path = "/"
			continue
		}
		if strings.HasPrefix(p, prefix+"/") {
			response.Items[i].Path = strings.TrimPrefix(p, prefix)
		}
	}
}
