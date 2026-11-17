// Modified from FileBrowser Quantum for the mument derivative. See MODIFICATIONS.md.
//go:build 386 || arm

package imagemeta

import (
	"context"
	"os"
)

// ExtractEmbeddedPreview is unavailable on 32-bit platforms (imagemeta dependency omitted).
func ExtractEmbeddedPreview(ctx context.Context, path string) ([]byte, error) {
	return nil, nil
}

// GetOrientation is unavailable on 32-bit platforms (imagemeta dependency omitted).
func GetOrientation(ctx context.Context, path string) string {
	return ""
}

// GetOrientationFromFile is unavailable on 32-bit platforms (imagemeta dependency omitted).
func GetOrientationFromFile(ctx context.Context, path string, f *os.File) string {
	return ""
}
