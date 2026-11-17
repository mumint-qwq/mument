// Modified from FileBrowser Quantum for the mument derivative. See MODIFICATIONS.md.
package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/files"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/internal/utils"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/gtsteffaniak/go-logger/logger"
)

type FileCache interface {
	Store(ctx context.Context, key string, value []byte) error
	Load(ctx context.Context, key string) ([]byte, bool, error)
	Delete(ctx context.Context, key string) error
}

// isClientCancellation checks if an error is due to client cancellation (navigation away)
func isClientCancellation(ctx context.Context, err error) bool {
	// Check context state first
	if ctx.Err() == context.Canceled {
		return true
	}

	// Check if the error chain contains context cancellation
	return errors.Is(err, context.Canceled)
}

// previewHandler handles the preview request for images.
// @Summary Get image preview
// @Description Returns a preview image based on the requested path and size.
// @Tags Resources
// @Accept json
// @Produce json
// @Param path query string true "File path of the image to preview"
// @Param size query string false "Preview size ('small' or 'large'). Default is based on server settings.Config."
// @Success 200 {file} file "Preview image content"
// @Failure 202 {object} map[string]string "Download permissions required"
// @Failure 400 {object} map[string]string "Invalid request path"
// @Failure 404 {object} map[string]string "File not found"
// @Failure 415 {object} map[string]string "Unsupported file type for preview"
// @Failure 500 {object} map[string]string "Internal server error"
// @Failure 501 {object} map[string]string "Preview generation not implemented"
// @Router /api/resources/preview [get]
func previewHandler(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	if settings.Config.Server.DisablePreviews {
		return http.StatusNotImplemented, fmt.Errorf("preview is disabled")
	}
	path := r.URL.Query().Get("path")
	source := r.URL.Query().Get("source")
	if path == "" {
		return http.StatusBadRequest, fmt.Errorf("invalid request path")
	}
	if source == "" {
		return http.StatusBadRequest, fmt.Errorf("source is required")
	}
	filePerms, err := effectiveFilePerms(d, source)
	if err != nil {
		return http.StatusForbidden, err
	}
	if !filePerms.View {
		return http.StatusForbidden, fmt.Errorf("user is not allowed to view files in this source")
	}
	fileInfo, err := files.FileInfoFaster(utils.FileOptions{
		Path:     path,
		Source:   source,
		AlbumArt: true, // Extract album art for audio previews
		Expand:   false,
	}, d.User)
	if err != nil {
		logger.Errorf("error getting file info: %v", err)
		return ErrToStatus(err), err
	}
	d.FileInfo = *fileInfo
	status, err := PreviewHelperFunc(w, r, d)
	if err != nil {
		// Error already logged in PreviewHelperFunc or its callees
		return ErrToStatus(err), err
	}
	return status, nil
}

// publicPreviewHandler handles the preview request for images from public shares.
// @Summary Get image/video preview from a public share
// @Description Returns a preview (thumbnail) for images or videos accessible via a public share. Preview generation can be disabled globally or per-share. Not available for upload-only shares.
// @Tags Resources
// @Accept json
// @Produce image/jpeg
// @Param hash query string true "Share hash for authentication"
// @Param path query string true "File path within the share to preview"
// @Param size query string false "Preview size: 'small' or 'large'. Default is based on server settings.Config."
// @Success 200 {file} file "Preview image content (JPEG)"
// @Failure 403 {object} map[string]string "Share unavailable or access denied"
// @Failure 404 {object} map[string]string "File not found or preview not available"
// @Failure 500 {object} map[string]string "Internal server error"
// @Failure 501 {object} map[string]string "Previews disabled globally, for this share, or for upload shares"
// @Router /public/api/resources/preview [get]
func publicPreviewHandler(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	if settings.Config.Server.DisablePreviews || d.Share.DisableThumbnails {
		return http.StatusNotImplemented, fmt.Errorf("preview is disabled")
	}
	if d.Share.ShareType == "upload" {
		return http.StatusNotImplemented, fmt.Errorf("preview is disabled for upload shares")
	}
	status, err := PreviewHelperFunc(w, r, d)
	if err != nil {
		logger.Errorf("public preview handler: error getting preview with error %v", err)
		return http.StatusNotFound, fmt.Errorf("preview not available for this item")
	}
	return status, err
}

func shouldServeOriginalPreview(ext string, resizable bool, realPath, previewSize string, fileSize int64) bool {
	if !resizable {
		return false
	}
	// HEIC/TIFF still need conversion for reliable web display.
	switch ext {
	case ".heic", ".heif", ".tiff", ".tif":
		return false
	}
	const maxSizeForOriginal = 256 * 1024 // 256KB
	if settings.Config.Server.DisableResize || fileSize < maxSizeForOriginal {
		return true
	}
	return preview.ShouldServeOriginalImage(realPath, previewSize)
}

func rawFileHandler(w http.ResponseWriter, r *http.Request, file iteminfo.ExtendedFileInfo) (int, error) {
	fd, err := os.Open(file.RealPath)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	defer fd.Close()

	SetContentDisposition(w, r, file.Name, false)

	w.Header().Set("Cache-Control", "private")
	http.ServeContent(w, r, file.Name, file.ModTime, fd)
	return 0, nil
}

func PreviewHelperFunc(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	ctx := r.Context()
	if d.Ctx != nil {
		ctx = d.Ctx
	}

	previewSize := r.URL.Query().Get("size")
	if !(previewSize == "large" || previewSize == "original" || previewSize == "xlarge") {
		previewSize = "small"
	}
	if d.FileInfo.Type != "directory" && !d.FileInfo.HasPreview {
		return http.StatusBadRequest, fmt.Errorf("this item does not have a preview")
	}

	seekPercentage := 0
	percentage := r.URL.Query().Get("atPercentage")
	if percentage != "" {
		var err error
		seekPercentage, err = strconv.Atoi(percentage)
		if err != nil {
			seekPercentage = 0
		}
		if seekPercentage < 0 || seekPercentage > 100 {
			seekPercentage = 0
		}
	}

	// For directories: map atPercentage to frame index 0–3 for motion preview (cycle over previewable items)
	var dirFrameIndex int
	if d.FileInfo.Type == "directory" {
		switch {
		case seekPercentage <= 0:
			dirFrameIndex = 0
		case seekPercentage <= 25:
			dirFrameIndex = 1
		case seekPercentage <= 50:
			dirFrameIndex = 2
		default:
			dirFrameIndex = 3
		}
		fileInfo, err := getDirectoryPreview(ctx, d, dirFrameIndex)
		if err != nil {
			if isClientCancellation(ctx, err) {
				return http.StatusOK, nil
			}
			if ctx.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
				return http.StatusRequestTimeout, fmt.Errorf("preview generation timed out")
			}
			logger.Errorf("error getting directory preview: %v", err)
			return http.StatusInternalServerError, err
		}
		d.FileInfo = *fileInfo
		seekPercentage = 0
	}

	SetContentDisposition(w, r, d.FileInfo.Name, false)
	isImage := strings.HasPrefix(d.FileInfo.Type, "image")
	ext := strings.ToLower(filepath.Ext(d.FileInfo.Name))
	resizable := iteminfo.ResizableImageTypes[ext]

	// For small displayable images (jpg, png, etc.) serve the original to avoid processing.
	// Also serve the original when dimensions already fit the requested preview size.
	if isImage && shouldServeOriginalPreview(ext, resizable, d.FileInfo.RealPath, previewSize, d.FileInfo.Size) {
		return rawFileHandler(w, r, d.FileInfo)
	}

	officeUrl := ""
	if d.FileInfo.OnlyOfficeId != "" {
		pathUrl := fmt.Sprintf("/api/resources/download?file=%s&source=%s", url.QueryEscape(d.FileInfo.Path), url.QueryEscape(d.FileInfo.Source))
		pathUrl = pathUrl + "&auth=" + d.Token
		if settings.Config.Http.InternalUrl != "" {
			officeUrl = settings.Config.Http.InternalUrl + pathUrl
		} else {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			officeUrl = scheme + "://" + r.Host + pathUrl
		}
	}
	previewImg, err := preview.GetPreviewForFile(ctx, d.FileInfo, previewSize, officeUrl, seekPercentage)
	if err != nil {
		// Check if it was a context cancellation (client navigated away)
		if isClientCancellation(ctx, err) {
			// Return 200 to avoid error logging - client cancellation is normal
			return http.StatusOK, nil
		}

		// Check if it was a context timeout (server-side timeout)
		if ctx.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
			logger.Errorf("Preview timeout for file '%s' after 15 seconds", d.FileInfo.Name)
			return http.StatusRequestTimeout, fmt.Errorf("preview generation timed out after 15 seconds")
		}

		// Log detailed error information for actual server errors
		logger.Errorf("Preview generation failed for file '%s' (type: %s, size: %s, seek: %d%%): %v",
			d.FileInfo.Name, d.FileInfo.Type, previewSize, seekPercentage, err)

		return ErrToStatus(err), err
	}
	w.Header().Set("Cache-Control", "private")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeContent(w, r, d.FileInfo.Name+"-preview.jpg", d.FileInfo.ModTime, bytes.NewReader(previewImg))
	return 0, nil
}
