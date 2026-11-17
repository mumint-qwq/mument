// Modified from FileBrowser Quantum for the mument derivative. See MODIFICATIONS.md.
//go:build !386 && !arm

package imagemeta

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	extimagemeta "github.com/evanoberholster/imagemeta"
	"github.com/evanoberholster/imagemeta/meta/exif"
	"github.com/evanoberholster/imagemeta/meta/exif/tag"
)

// GetOrientation returns the display orientation string for a file (e.g. "Rotate 90 CW",
// "Horizontal (normal)"). Returns empty string when orientation cannot be read.
func GetOrientation(ctx context.Context, path string) string {
	if ctx.Err() != nil || path == "" {
		return ""
	}

	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	return GetOrientationFromFile(ctx, path, f)
}

// GetOrientationFromFile reads display orientation from an already-open file.
// The caller retains ownership of f. Keeping this separate lets index scans read
// dimensions and orientation without opening the same image twice.
func GetOrientationFromFile(ctx context.Context, path string, f *os.File) string {
	if ctx.Err() != nil || path == "" || f == nil {
		return ""
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ""
	}

	ext := strings.ToLower(filepath.Ext(path))
	if isHEICExtension(ext) {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return ""
		}
		if orient := heicTransformOrientation(parseHEICTransform(f)); orient != "" {
			return orient
		}
	}

	var ex exif.Exif
	var err error
	switch ext {
	case ".heic", ".heif", ".heics":
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			return ""
		}
		ex, err = extimagemeta.DecodeHeif(f)
	default:
		ex, err = extimagemeta.Decode(f)
	}
	if ctx.Err() != nil {
		return ""
	}
	if err == nil && ex.IFD0.Orientation != 0 {
		return tag.ValueNameFor(tag.IFD0, tag.TagOrientation, uint32(ex.IFD0.Orientation))
	}
	if !isHEICExtension(ext) {
		if orient := extractTIFFOrientation(f); orient != 0 {
			return tag.ValueNameFor(tag.IFD0, tag.TagOrientation, uint32(orient))
		}
	}
	return ""
}
