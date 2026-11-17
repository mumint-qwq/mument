// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package indexing

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	dbsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

func newMetadataTestIndex(t *testing.T) (*Index, string) {
	t.Helper()
	dir := t.TempDir()
	prevCache := settings.Config.Server.CacheDir
	settings.Config.Server.CacheDir = t.TempDir()
	db, _, err := dbsql.NewIndexDB("b_contract_"+sanitizeTestName(t.Name()), "OFF", 1000, 32, false)
	if err != nil {
		t.Fatal(err)
	}
	idx := &Index{
		ReducedIndex: ReducedIndex{},
		Source: settings.Source{
			Name: "b",
			Path: dir,
			Config: settings.SourceConfig{
				UseLogicalSize: true,
			},
		},
		db:                  db,
		scanUpdatedPaths:    make(map[string]bool),
		folderSizes:         make(map[string]uint64),
		folderSizesUnsynced: make(map[string]struct{}),
	}
	t.Cleanup(func() {
		_ = db.Close()
		settings.Config.Server.CacheDir = prevCache
	})
	return idx, dir
}

func sanitizeTestName(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.NRGBA{R: 10, A: 255})
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// jpegWithOrientation injects a minimal EXIF APP1 segment after SOI.
func jpegWithOrientation(t *testing.T, w, h int, orientation uint16) []byte {
	t.Helper()
	base := jpegBytes(t, w, h)
	tiff := []byte{'I', 'I', 0x2a, 0x00, 0x08, 0x00, 0x00, 0x00}
	tiff = append(tiff, 0x01, 0x00) // one IFD entry
	entry := make([]byte, 12)
	binary.LittleEndian.PutUint16(entry[0:2], 0x0112)
	binary.LittleEndian.PutUint16(entry[2:4], 3)
	binary.LittleEndian.PutUint32(entry[4:8], 1)
	binary.LittleEndian.PutUint16(entry[8:10], orientation)
	tiff = append(tiff, entry...)
	tiff = append(tiff, 0x00, 0x00, 0x00, 0x00) // next IFD
	payload := append([]byte("Exif\x00\x00"), tiff...)
	segLen := len(payload) + 2
	out := []byte{0xff, 0xd8, 0xff, 0xe1, byte(segLen >> 8), byte(segLen)}
	out = append(out, payload...)
	out = append(out, base[2:]...)
	return out
}

func writeBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestB01JPEGAndPNGDimensions(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "wide.png"), 80, 40)
	writeBytes(t, filepath.Join(dir, "wide.jpg"), jpegBytes(t, 80, 40))
	for _, name := range []string{"wide.png", "wide.jpg"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		got := readImageHeader(t.Context(), path, versionFromFileInfo(info, path))
		if got.State != iteminfo.ImageMetaReady || got.Width != 80 || got.Height != 40 || got.Orientation != 1 {
			t.Fatalf("%s: %#v", name, got)
		}
		if got.Ratio != 2 {
			t.Fatalf("%s: ratio=%v want 2", name, got.Ratio)
		}
	}
}

func TestB02EXIFOrientationAppliedOnce(t *testing.T) {
	dir := t.TempDir()
	for orientation := uint16(1); orientation <= 8; orientation++ {
		path := filepath.Join(dir, "o"+string(rune('0'+orientation))+".jpg")
		writeBytes(t, path, jpegWithOrientation(t, 80, 40, orientation))
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		got := readImageHeader(t.Context(), path, versionFromFileInfo(info, path))
		if got.State != iteminfo.ImageMetaReady {
			t.Fatalf("orientation %d: %#v", orientation, got)
		}
		if got.Orientation != uint8(orientation) {
			t.Fatalf("orientation %d: got %d", orientation, got.Orientation)
		}
		wantRatio := 2.0
		if orientation >= 5 {
			wantRatio = 0.5
		}
		if got.Ratio != wantRatio {
			t.Fatalf("orientation %d: ratio=%v want %v", orientation, got.Ratio, wantRatio)
		}
	}
}

func TestB03NoEXIFAndCorruptEXIF(t *testing.T) {
	dir := t.TempDir()
	clean := filepath.Join(dir, "clean.jpg")
	writeBytes(t, clean, jpegBytes(t, 80, 40))
	info, _ := os.Stat(clean)
	got := readImageHeader(t.Context(), clean, versionFromFileInfo(info, clean))
	if got.State != iteminfo.ImageMetaReady || got.Orientation != 1 || got.Ratio != 2 {
		t.Fatalf("no EXIF: %#v", got)
	}

	// A present but truncated EXIF APP1 must not be treated as normal.
	base := jpegBytes(t, 80, 40)
	corrupt := append([]byte{0xff, 0xd8, 0xff, 0xe1, 0x00, 0x08, 'E', 'x', 'i', 'f', 0, 0, 0}, base[2:]...)
	badPath := filepath.Join(dir, "bad.jpg")
	writeBytes(t, badPath, corrupt)
	info, _ = os.Stat(badPath)
	got = readImageHeader(t.Context(), badPath, versionFromFileInfo(info, badPath))
	if got.State == iteminfo.ImageMetaReady {
		t.Fatalf("corrupt EXIF was accepted: %#v", got)
	}
}

func TestB04HeaderBudgetAndInvalidDimensions(t *testing.T) {
	dir := t.TempDir()
	base := jpegBytes(t, 80, 40)
	var padded bytes.Buffer
	padded.Write([]byte{0xff, 0xd8})
	for i := 0; i < 20; i++ {
		seg := make([]byte, 60000)
		padded.Write([]byte{0xff, 0xe2, byte((len(seg) + 2) >> 8), byte(len(seg) + 2)})
		padded.Write(seg)
	}
	padded.Write(base[2:])
	path := filepath.Join(dir, "budget.jpg")
	writeBytes(t, path, padded.Bytes())
	info, _ := os.Stat(path)
	got := readImageHeader(t.Context(), path, versionFromFileInfo(info, path))
	if got.State != iteminfo.ImageMetaDeferred {
		t.Fatalf("budget state=%v want deferred (%#v)", got.State, got)
	}

	broken := filepath.Join(dir, "broken.png")
	writeBytes(t, broken, []byte("not an image"))
	info, _ = os.Stat(broken)
	got = readImageHeader(t.Context(), broken, versionFromFileInfo(info, broken))
	if got.State != iteminfo.ImageMetaFailed {
		t.Fatalf("broken state=%v want failed (%#v)", got.State, got)
	}
	if iteminfo.DisplayAspectRatioFromDimensions(1<<30, 1, 1) <= 0 {
		t.Fatal("large finite dimensions should still produce a ratio")
	}
	if iteminfo.DisplayAspectRatioFromDimensions(0, 10, 1) != 0 || iteminfo.DisplayAspectRatioFromDimensions(10, 0, 1) != 0 {
		t.Fatal("zero dimensions must yield unknown")
	}
}

func pngTestChunk(kind string, data []byte) []byte {
	out := make([]byte, 8, 12+len(data))
	binary.BigEndian.PutUint32(out[:4], uint32(len(data)))
	copy(out[4:8], kind)
	out = append(out, data...)
	sum := crc32.ChecksumIEEE(append([]byte(kind), data...))
	var crc [4]byte
	binary.BigEndian.PutUint32(crc[:], sum)
	return append(out, crc[:]...)
}

func TestB04HugeDimensionsAreRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.png")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], 1<<30)
	binary.BigEndian.PutUint32(ihdr[4:8], 1)
	ihdr[8] = 8
	ihdr[9] = 2
	data := append([]byte("\x89PNG\r\n\x1a\n"), pngTestChunk("IHDR", ihdr)...)
	data = append(data, pngTestChunk("IEND", nil)...)
	writeBytes(t, path, data)
	info, _ := os.Stat(path)
	got := readImageHeader(t.Context(), path, versionFromFileInfo(info, path))
	if got.State != iteminfo.ImageMetaFailed || got.Err != imageMetaErrInvalid {
		t.Fatalf("huge result=%#v want failed/invalid", got)
	}
}

func TestB05MetadataCommitCASAndCache(t *testing.T) {
	idx, dir := newMetadataTestIndex(t)
	path := filepath.Join(dir, "photo.jpg")
	writeBytes(t, path, jpegBytes(t, 80, 40))
	info, _ := os.Stat(path)
	observed := versionFromFileInfo(info, path)
	im := &iteminfo.ImageIndexMeta{Version: observed.String(), ObsSeq: idx.db.NextImageObsSeq(), LogicalSize: observed.Size, MTimeNS: observed.MtimeNS}
	row := &iteminfo.FileInfo{Path: "/photo.jpg", ItemInfo: iteminfo.ItemInfo{Name: "photo.jpg", Size: int64(observed.Size), ModTime: info.ModTime(), Type: "image/jpeg", Image: im}}
	if err := idx.db.InsertItem("b", row.Path, row); err != nil {
		t.Fatal(err)
	}
	work, err := idx.db.GetImageWorksByPaths("b", []string{row.Path})
	if err != nil || work[row.Path].Guard == 0 || work[row.Path].Guard <= im.ObsSeq {
		t.Fatalf("work=%#v err=%v", work, err)
	}
	if !idx.processImageWork(t.Context(), work[row.Path], observed) {
		t.Fatal("ready commit rejected")
	}
	if !idx.processImageWork(t.Context(), work[row.Path], observed) {
		t.Fatal("second ready commit should be allowed for parser upgrade")
	}

	// A new version must invalidate and bump the guard; old commit must fail.
	time.Sleep(20 * time.Millisecond)
	writeBytes(t, path, jpegBytes(t, 40, 80))
	info2, _ := os.Stat(path)
	observed2 := versionFromFileInfo(info2, path)
	im2 := &iteminfo.ImageIndexMeta{Version: observed2.String(), ObsSeq: idx.db.NextImageObsSeq(), LogicalSize: observed2.Size, MTimeNS: observed2.MtimeNS}
	row.ItemInfo.Size = int64(observed2.Size)
	row.ItemInfo.ModTime = info2.ModTime()
	row.ItemInfo.Image = im2
	if err := idx.db.InsertItem("b", row.Path, row); err != nil {
		t.Fatal(err)
	}
	after, _ := idx.db.GetImageWorksByPaths("b", []string{row.Path})
	if after[row.Path].Guard <= work[row.Path].Guard {
		t.Fatalf("guard=%d did not advance from %d", after[row.Path].Guard, work[row.Path].Guard)
	}
	if idx.processImageWork(t.Context(), work[row.Path], observed) {
		t.Fatal("stale metadata commit must be rejected")
	}
	if !idx.processImageWork(t.Context(), after[row.Path], observed2) {
		t.Fatal("fresh metadata commit rejected")
	}
}
