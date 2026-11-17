// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package indexing

import (
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
)

var b24DecodeCalls, b24ConfigCalls atomic.Int32

func init() {
	image.RegisterFormat("b24-test", "\x89B24", func(r io.Reader) (image.Image, error) {
		b24DecodeCalls.Add(1)
		return nil, io.ErrUnexpectedEOF
	}, func(r io.Reader) (image.Config, error) {
		b24ConfigCalls.Add(1)
		return image.Config{Width: 80, Height: 40, ColorModel: color.GrayModel}, nil
	})
}

func TestB24HeaderReadNeverCallsFullDecode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.b24")
	if err := os.WriteFile(path, append([]byte("\x89B24"), make([]byte, 64)...), 0o600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	got := readImageHeader(t.Context(), path, versionFromFileInfo(info, path))
	if got.State != iteminfo.ImageMetaReady || got.Width != 80 || got.Height != 40 {
		t.Fatalf("result=%#v", got)
	}
	if b24ConfigCalls.Load() != 1 {
		t.Fatalf("DecodeConfig calls=%d want 1", b24ConfigCalls.Load())
	}
	if b24DecodeCalls.Load() != 0 {
		t.Fatalf("full Decode calls=%d want 0", b24DecodeCalls.Load())
	}
}

func TestB22HiddenImageIsNotQueued(t *testing.T) {
	idx, dir := newMetadataTestIndex(t)
	writeBytes(t, filepath.Join(dir, "visible.jpg"), jpegBytes(t, 80, 40))
	writeBytes(t, filepath.Join(dir, ".hidden.jpg"), jpegBytes(t, 80, 40))
	scanner := &Scanner{idx: idx}
	if _, _, err := idx.indexDirectory("/", Options{Recursive: true, IsRoutineScan: true}, scanner, nil); err != nil {
		t.Fatal(err)
	}
	scanner.flushBatch()
	scanner.flushImageCandidates()
	if len(scanner.imageCandidates) != 0 {
		t.Fatalf("candidates were not flushed: %d", len(scanner.imageCandidates))
	}
	if got, _ := idx.db.GetItem("b", "/.hidden.jpg"); got != nil {
		t.Fatalf("hidden file was indexed: %#v", got)
	}
	if got, _ := idx.db.GetItem("b", "/visible.jpg"); got == nil || got.DisplayAspectRatio != 2 {
		t.Fatalf("visible file ratio missing: %#v", got)
	}
}
