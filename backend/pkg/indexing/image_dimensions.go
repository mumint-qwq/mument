// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package indexing

import (
	"context"
	"os"
	"strings"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// readDisplayAspectRatio is retained for callers that only need a best-effort
// ratio. It now uses the same bounded, version-checked header reader as the
// indexer; scanner paths queue work instead of calling this synchronously.
func readDisplayAspectRatio(realPath, mimeType string) float64 {
	if !strings.HasPrefix(mimeType, "image/") || realPath == "" {
		return 0
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return 0
	}
	result := readImageHeader(context.Background(), realPath, versionFromFileInfo(info, realPath))
	if result.State != iteminfo.ImageMetaReady {
		return 0
	}
	return result.Ratio
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
