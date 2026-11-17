package indexing

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestReadDisplayAspectRatio(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wide.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	if err = png.Encode(f, img); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}

	if got := readDisplayAspectRatio(path, "image/png"); got != 2 {
		t.Fatalf("readDisplayAspectRatio() = %v, want 2", got)
	}
	if got := readDisplayAspectRatio(path, "text/plain"); got != 0 {
		t.Fatalf("non-image ratio = %v, want 0", got)
	}
	if got := readDisplayAspectRatio(filepath.Join(t.TempDir(), "missing.jpg"), "image/jpeg"); got != 0 {
		t.Fatalf("missing image ratio = %v, want 0", got)
	}
}

func TestOrientationSwapsDimensions(t *testing.T) {
	for _, orientation := range []string{"Rotate 90 CW", "Rotate 270 CW", "Right-bottom", "Left-top", "6", "8"} {
		if !orientationSwapsDimensions(orientation) {
			t.Errorf("orientationSwapsDimensions(%q) = false, want true", orientation)
		}
	}
	for _, orientation := range []string{"", "Horizontal (normal)", "Rotate 180", "Top-right", "Bottom-left", "1", "3"} {
		if orientationSwapsDimensions(orientation) {
			t.Errorf("orientationSwapsDimensions(%q) = true, want false", orientation)
		}
	}
}
