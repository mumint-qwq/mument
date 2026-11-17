package indexing

import (
	"context"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"strings"

	"github.com/gtsteffaniak/filebrowser/backend/internal/imagemeta"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// readDisplayAspectRatio reads image headers only. Unsupported containers return
// zero so the browser can retain its existing decode-and-cache fallback.
func readDisplayAspectRatio(realPath, mimeType string) float64 {
	if !strings.HasPrefix(mimeType, "image/") || realPath == "" {
		return 0
	}
	f, err := os.Open(realPath)
	if err != nil {
		return 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0
	}

	width, height := cfg.Width, cfg.Height
	if orientationSwapsDimensions(imagemeta.GetOrientationFromFile(context.Background(), realPath, f)) {
		width, height = height, width
	}
	ratio := float64(width) / float64(height)
	if ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return 0
	}
	return ratio
}

func orientationSwapsDimensions(orientation string) bool {
	switch orientation {
	case "Rotate 90 CW", "Right-top", "6",
		"Rotate 270 CW", "Left-bottom", "8",
		"Mirror horizontal and rotate 270 CW", "Right-bottom", "7",
		"Mirror horizontal and rotate 90 CW", "Left-top", "5":
		return true
	default:
		return false
	}
}
