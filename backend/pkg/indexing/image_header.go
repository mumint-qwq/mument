// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package indexing

import (
	"context"
	"errors"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/gtsteffaniak/filebrowser/backend/internal/imagemeta"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
)

const (
	imageHeaderBudgetBytes = 1 << 20
	maxImageDimension      = 1 << 24
)

var errImageHeaderBudget = errors.New("image header read budget exhausted")

// imageHeaderTestHook is nil in production. Tests use it to block a scanner
// between dimension and orientation reads, or to count concurrent readers.
var imageHeaderTestHook atomic.Pointer[func(stage, path string)]

func SetImageHeaderTestHookForTesting(hook func(stage, path string)) {
	if hook == nil {
		imageHeaderTestHook.Store(nil)
		return
	}
	imageHeaderTestHook.Store(&hook)
}

func runImageHeaderTestHook(stage, path string) {
	if hook := imageHeaderTestHook.Load(); hook != nil {
		(*hook)(stage, path)
	}
}

type imageMetaError uint8

const (
	imageMetaErrNone imageMetaError = iota
	imageMetaErrBudget
	imageMetaErrStale
	imageMetaErrUnsupported
	imageMetaErrIO
	imageMetaErrDecode
	imageMetaErrOrientation
	imageMetaErrInvalid
)

type imageHeaderResult struct {
	Width       int
	Height      int
	Orientation uint8
	Ratio       float64
	State       iteminfo.ImageMetaState
	Err         imageMetaError
	Detail      string
	BytesRead   int64
}

// headerReader enforces one total byte budget across Read, ReadAt and Seek.
// Seeking never refills the budget, so parsers cannot bypass the limit by
// rewinding and reading the same prefix again.
type headerReader struct {
	r         *os.File
	size      int64
	remaining int64
}

func newHeaderReader(f *os.File, size, budget int64) *headerReader {
	if budget <= 0 {
		budget = imageHeaderBudgetBytes
	}
	return &headerReader{r: f, size: size, remaining: budget}
}

func (h *headerReader) Read(p []byte) (int, error) {
	if h.remaining <= 0 {
		return 0, errImageHeaderBudget
	}
	if int64(len(p)) > h.remaining {
		p = p[:h.remaining]
	}
	n, err := h.r.Read(p)
	h.remaining -= int64(n)
	if err == nil && n == 0 {
		return 0, io.ErrNoProgress
	}
	return n, err
}

func (h *headerReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= h.size {
		return 0, io.EOF
	}
	if h.remaining <= 0 {
		return 0, errImageHeaderBudget
	}
	max := h.size - off
	if int64(len(p)) > max {
		p = p[:max]
	}
	if int64(len(p)) > h.remaining {
		p = p[:h.remaining]
	}
	n, err := h.r.ReadAt(p, off)
	h.remaining -= int64(n)
	return n, err
}

func (h *headerReader) Seek(offset int64, whence int) (int64, error) {
	return h.r.Seek(offset, whence)
}

// readImageHeader opens the file once, reads dimensions and orientation from the
// same fd, then verifies both the fd and the path still describe expected.
func readImageHeader(ctx context.Context, path string, expected fileVersion) (result imageHeaderResult) {
	select {
	case imageHeaderSem <- struct{}{}:
		defer func() { <-imageHeaderSem }()
	case <-ctx.Done():
		return imageHeaderResult{State: iteminfo.ImageMetaDeferred, Err: imageMetaErrStale, Detail: ctx.Err().Error()}
	}
	if ctx.Err() != nil {
		return imageHeaderResult{State: iteminfo.ImageMetaDeferred, Err: imageMetaErrStale, Detail: ctx.Err().Error()}
	}
	var hr *headerReader
	defer func() {
		if hr != nil {
			result.BytesRead = imageHeaderBudgetBytes - hr.remaining
		}
	}()
	if ctx.Err() != nil {
		return imageHeaderResult{State: iteminfo.ImageMetaDeferred, Err: imageMetaErrStale, Detail: ctx.Err().Error()}
	}
	f, err := os.Open(path)
	if err != nil {
		return imageHeaderResult{State: iteminfo.ImageMetaFailed, Err: imageMetaErrIO, Detail: err.Error()}
	}
	defer f.Close()

	before, err := f.Stat()
	if err != nil {
		return imageHeaderResult{State: iteminfo.ImageMetaFailed, Err: imageMetaErrIO, Detail: err.Error()}
	}
	if got := versionFromFileInfo(before, path); got.String() != expected.String() {
		return imageHeaderResult{State: iteminfo.ImageMetaDeferred, Err: imageMetaErrStale, Detail: "version changed before read"}
	}

	runImageHeaderTestHook("before_read", path)
	hr = newHeaderReader(f, before.Size(), imageHeaderBudgetBytes)
	var cfg image.Config
	var orient imagemeta.OrientationResult
	if isTIFFExtension(path) {
		// TIFF DecodeConfig may read strip payload on large files. Read the
		// structural IFD once for dimensions and orientation instead.
		th := imagemeta.ReadTIFFHeader(hr)
		cfg.Width, cfg.Height = th.Width, th.Height
		orient = th.Orientation
		if orient.Err != nil {
			return orientationFailure(orient.Err)
		}
	} else {
		var err error
		cfg, _, err = image.DecodeConfig(hr)
		if err != nil {
			switch {
			case errors.Is(err, errImageHeaderBudget):
				return imageHeaderResult{State: iteminfo.ImageMetaDeferred, Err: imageMetaErrBudget, Detail: err.Error()}
			case errors.Is(err, image.ErrFormat):
				if isKnownImageExtension(path) {
					return imageHeaderResult{State: iteminfo.ImageMetaFailed, Err: imageMetaErrDecode, Detail: err.Error()}
				}
				return imageHeaderResult{State: iteminfo.ImageMetaUnsupported, Err: imageMetaErrUnsupported, Detail: err.Error()}
			default:
				return imageHeaderResult{State: iteminfo.ImageMetaFailed, Err: imageMetaErrDecode, Detail: err.Error()}
			}
		}
		runImageHeaderTestHook("after_dimensions", path)
		if _, err := hr.Seek(0, io.SeekStart); err != nil {
			return imageHeaderResult{State: iteminfo.ImageMetaFailed, Err: imageMetaErrIO, Detail: err.Error()}
		}
		orient = imagemeta.ReadOrientationFromFile(ctx, path, hr)
		if orient.Err != nil {
			return orientationFailure(orient.Err)
		}
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxImageDimension || cfg.Height > maxImageDimension {
		return imageHeaderResult{State: iteminfo.ImageMetaFailed, Err: imageMetaErrInvalid, Detail: "dimensions out of range"}
	}
	orientation := uint8(1)
	if orient.Present {
		orientation = uint8(orient.Value)
	}
	if orientation < 1 || orientation > 8 {
		return imageHeaderResult{State: iteminfo.ImageMetaFailed, Err: imageMetaErrOrientation, Detail: "orientation out of range"}
	}

	after, err := f.Stat()
	if err != nil {
		return imageHeaderResult{State: iteminfo.ImageMetaFailed, Err: imageMetaErrIO, Detail: err.Error()}
	}
	pathInfo, err := os.Stat(path)
	if err != nil {
		return imageHeaderResult{State: iteminfo.ImageMetaDeferred, Err: imageMetaErrStale, Detail: err.Error()}
	}
	if !os.SameFile(before, after) || !os.SameFile(before, pathInfo) {
		return imageHeaderResult{State: iteminfo.ImageMetaDeferred, Err: imageMetaErrStale, Detail: "path identity changed"}
	}
	if got := versionFromFileInfo(after, path); got.String() != expected.String() {
		return imageHeaderResult{State: iteminfo.ImageMetaDeferred, Err: imageMetaErrStale, Detail: "version changed during read"}
	}

	ratio := iteminfo.DisplayAspectRatioFromDimensions(cfg.Width, cfg.Height, orientation)
	if ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return imageHeaderResult{State: iteminfo.ImageMetaFailed, Err: imageMetaErrInvalid, Detail: "invalid aspect ratio"}
	}
	return imageHeaderResult{
		Width:       cfg.Width,
		Height:      cfg.Height,
		Orientation: orientation,
		Ratio:       ratio,
		State:       iteminfo.ImageMetaReady,
	}
}

func orientationFailure(err error) imageHeaderResult {
	if errors.Is(err, errImageHeaderBudget) {
		return imageHeaderResult{State: iteminfo.ImageMetaDeferred, Err: imageMetaErrBudget, Detail: err.Error()}
	}
	if errors.Is(err, imagemeta.ErrOrientationUnsupported) {
		return imageHeaderResult{State: iteminfo.ImageMetaUnsupported, Err: imageMetaErrUnsupported, Detail: err.Error()}
	}
	if errors.Is(err, imagemeta.ErrOrientationDeferred) {
		return imageHeaderResult{State: iteminfo.ImageMetaDeferred, Err: imageMetaErrBudget, Detail: err.Error()}
	}
	return imageHeaderResult{State: iteminfo.ImageMetaFailed, Err: imageMetaErrOrientation, Detail: err.Error()}
}

func isTIFFExtension(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tif", ".tiff":
		return true
	default:
		return false
	}
}

func isKnownImageExtension(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".jpe", ".png", ".gif", ".webp", ".bmp", ".tif", ".tiff":
		return true
	default:
		return false
	}
}
