// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package preview

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/stretchr/testify/require"
)

func testJPEGHeader(width, height int, progressive bool, comps []jpegComponent, orientation int) []byte {
	var buf []byte
	buf = append(buf, 0xff, 0xd8) // SOI
	if orientation > 0 {
		exif := makeExifOrientation(orientation)
		buf = append(buf, 0xff, 0xe1)
		buf = binary.BigEndian.AppendUint16(buf, uint16(len(exif)+2))
		buf = append(buf, exif...)
	}
	sof := 0xc0
	if progressive {
		sof = 0xc2
	}
	payloadLen := 6 + 3*len(comps)
	buf = append(buf, 0xff, byte(sof))
	buf = binary.BigEndian.AppendUint16(buf, uint16(payloadLen+2))
	buf = append(buf, 8)
	buf = binary.BigEndian.AppendUint16(buf, uint16(height))
	buf = binary.BigEndian.AppendUint16(buf, uint16(width))
	buf = append(buf, byte(len(comps)))
	for i, c := range comps {
		buf = append(buf, byte(i+1), byte(c.h<<4|c.v), 0)
	}
	buf = append(buf, 0xff, 0xda) // SOS; the header parser stops here
	return buf
}

func makeExifOrientation(orientation int) []byte {
	var b []byte
	b = append(b, []byte("Exif\x00\x00")...)
	tiff := make([]byte, 8)
	copy(tiff[0:2], "II")
	binary.LittleEndian.PutUint16(tiff[2:4], 42)
	binary.LittleEndian.PutUint32(tiff[4:8], 8)
	tiff = append(tiff, 1, 0) // one IFD entry
	entry := make([]byte, 12)
	binary.LittleEndian.PutUint16(entry[0:2], 0x0112)
	binary.LittleEndian.PutUint16(entry[2:4], 3) // SHORT
	binary.LittleEndian.PutUint32(entry[4:8], 1)
	binary.LittleEndian.PutUint16(entry[8:10], uint16(orientation))
	tiff = append(tiff, entry...)
	tiff = append(tiff, 0, 0, 0, 0) // next IFD
	b = append(b, tiff...)
	return b
}

func testFile(t *testing.T, dir, name string, data []byte, typ string) iteminfo.ExtendedFileInfo {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	st, err := os.Stat(path)
	require.NoError(t, err)
	return iteminfo.ExtendedFileInfo{
		RealPath: path,
		FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{
			Name: name, Size: st.Size(), ModTime: st.ModTime(), Type: typ, HasPreview: true,
		}},
	}
}

func padHeaderForParse(data []byte) []byte {
	if len(data) < 300*1024 {
		data = append(data, make([]byte, 300*1024-len(data))...)
	}
	return data
}

func TestEstimateJPEGProgressiveHighPixel(t *testing.T) {
	dir := t.TempDir()
	header := padHeaderForParse(testJPEGHeader(4609, 8375, true, []jpegComponent{{1, 1}, {1, 1}, {1, 1}}, 0))
	file := testFile(t, dir, "progressive.jpg", header, "image/jpeg")
	svc := NewPreviewGeneratorWithBudget(4, 768, dir)
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })

	est, err := svc.estimateGeneration(context.Background(), file, "xlarge")
	require.NoError(t, err)
	require.Equal(t, "jpeg-progressive", est.Kind)
	require.Greater(t, est.Bytes, int64(600<<20))
	require.Less(t, est.Bytes, int64(670<<20))
	require.Equal(t, int64(4609*8375), est.Pixels)
}

func TestEstimateJPEGBaselineAndSubsampling(t *testing.T) {
	dir := t.TempDir()
	// 4:2:0 baseline: one plane pair at half resolution each for Cb/Cr.
	header := padHeaderForParse(testJPEGHeader(9109, 6130, false, []jpegComponent{{2, 2}, {1, 1}, {1, 1}}, 0))
	file := testFile(t, dir, "baseline.jpg", header, "image/jpeg")
	svc := NewPreviewGeneratorWithBudget(4, 768, dir)
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })

	est, err := svc.estimateGeneration(context.Background(), file, "xlarge")
	require.NoError(t, err)
	require.Equal(t, "jpeg", est.Kind)
	require.Greater(t, est.Bytes, int64(90<<20))
	require.Less(t, est.Bytes, int64(140<<20))

	// 4:4:4 baseline has three full-resolution planes and must cost more.
	header444 := padHeaderForParse(testJPEGHeader(9109, 6130, false, []jpegComponent{{1, 1}, {1, 1}, {1, 1}}, 0))
	file444 := testFile(t, dir, "baseline444.jpg", header444, "image/jpeg")
	est444, err := svc.estimateGeneration(context.Background(), file444, "xlarge")
	require.NoError(t, err)
	require.Greater(t, est444.Bytes, est.Bytes)
}

func TestEstimateJPEGOrientationAddsFullFrame(t *testing.T) {
	dir := t.TempDir()
	plain := padHeaderForParse(testJPEGHeader(4000, 3000, false, []jpegComponent{{1, 1}, {1, 1}, {1, 1}}, 0))
	rotated := padHeaderForParse(testJPEGHeader(4000, 3000, false, []jpegComponent{{1, 1}, {1, 1}, {1, 1}}, 6))
	svc := NewPreviewGeneratorWithBudget(4, 768, dir)
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })

	plainEst, err := svc.estimateGeneration(context.Background(), testFile(t, dir, "plain.jpg", plain, "image/jpeg"), "xlarge")
	require.NoError(t, err)
	rotEst, err := svc.estimateGeneration(context.Background(), testFile(t, dir, "rot.jpg", rotated, "image/jpeg"), "xlarge")
	require.NoError(t, err)
	require.Greater(t, rotEst.Bytes-plainEst.Bytes, int64(4000*3000*4))
}

func TestEstimateRejectsBadDimensionsAndTruncatedHeader(t *testing.T) {
	dir := t.TempDir()
	svc := NewPreviewGeneratorWithBudget(4, 768, dir)
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })

	cases := []struct {
		name string
		data []byte
		err  error
	}{
		{"zero-width", testJPEGHeader(0, 100, false, []jpegComponent{{1, 1}}, 0), ErrPreviewHeaderInvalid},
		{"overflow-pixels", testJPEGHeader(65000, 65000, false, []jpegComponent{{1, 1}}, 0), ErrPreviewDimensionsTooLarge},
		{"truncated", []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x20, 1, 2, 3}, ErrPreviewHeaderInvalid},
		{"not-an-image", []byte("this is not an image at all"), ErrPreviewHeaderInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Keep the file above the safe "serve original bytes" threshold so
			// the bounded header parser, not the small-file shortcut, decides.
			data := append([]byte(nil), tc.data...)
			if len(data) < 300*1024 {
				data = append(data, make([]byte, 300*1024-len(data))...)
			}
			file := testFile(t, dir, tc.name+".jpg", data, "image/jpeg")
			_, err := svc.estimateGeneration(context.Background(), file, "xlarge")
			require.ErrorIs(t, err, tc.err)
		})
	}
}

func TestEstimateHeaderReadIsBounded(t *testing.T) {
	dir := t.TempDir()
	// More than the 1 MiB read limit of comment segments before SOF.
	data := []byte{0xff, 0xd8}
	for len(data) < previewHeaderReadLimit+4096 {
		n := 60000
		data = append(data, 0xff, 0xfe)
		data = binary.BigEndian.AppendUint16(data, uint16(n+2))
		data = append(data, make([]byte, n)...)
	}
	data = append(data, testJPEGHeader(100, 100, false, []jpegComponent{{1, 1}}, 0)...)
	file := testFile(t, dir, "huge-header.jpg", data, "image/jpeg")
	svc := NewPreviewGeneratorWithBudget(4, 768, dir)
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })

	_, err := svc.estimateGeneration(context.Background(), file, "xlarge")
	require.ErrorIs(t, err, ErrPreviewHeaderInvalid)
}

func TestEstimateSmallUnreadableFileStaysBounded(t *testing.T) {
	dir := t.TempDir()
	file := testFile(t, dir, "tiny.jpg", []byte("not an image"), "image/jpeg")
	svc := NewPreviewGeneratorWithBudget(4, 768, dir)
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })

	est, err := svc.estimateGeneration(context.Background(), file, "xlarge")
	require.NoError(t, err)
	require.Less(t, est.Bytes, int64(64<<20))
	require.Equal(t, "small-raw-bytes", est.Kind)
}

func TestGIFAnimationFramesAndTruncation(t *testing.T) {
	// Minimal GIF89a with a global color table and two image blocks.
	gif := []byte("GIF89a")
	gif = append(gif, 2, 0, 2, 0, 0x80, 0, 0)
	gif = append(gif, 0, 0, 0, 255, 255, 255)
	for i := 0; i < 2; i++ {
		gif = append(gif, 0x2c, 0, 0, 0, 0, 2, 0, 2, 0, 0)
		gif = append(gif, 2, 2, 0x4c, 0x01, 0) // LZW min code size + one sub-block + terminator
	}
	gif = append(gif, 0x3b)
	frames, err := gifAnimationFrames(gif, true)
	require.NoError(t, err)
	require.Equal(t, 2, frames)

	_, err = gifAnimationFrames(gif[:len(gif)-8], false)
	require.Error(t, err)
}

func TestPNGAnimationFrames(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	chunk := func(typ string, data []byte) []byte {
		var b []byte
		b = binary.BigEndian.AppendUint32(b, uint32(len(data)))
		b = append(b, typ...)
		b = append(b, data...)
		b = append(b, 0, 0, 0, 0) // CRC is irrelevant to the frame-count scan
		return b
	}
	png = append(png, chunk("acTL", []byte{0, 0, 0, 3, 0, 0, 0, 0})...)
	frames, err := pngAnimationFrames(png)
	require.NoError(t, err)
	require.Equal(t, 3, frames)
}

func TestEstimateContextCancellation(t *testing.T) {
	dir := t.TempDir()
	file := testFile(t, dir, "x.jpg", testJPEGHeader(100, 100, false, []jpegComponent{{1, 1}}, 0), "image/jpeg")
	svc := NewPreviewGeneratorWithBudget(4, 768, dir)
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.estimateGeneration(ctx, file, "xlarge")
	require.ErrorIs(t, err, context.Canceled)
}

func TestEstimateUnreadablePathIsTyped(t *testing.T) {
	svc := NewPreviewGeneratorWithBudget(4, 768, t.TempDir())
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	file := iteminfo.ExtendedFileInfo{RealPath: filepath.Join(t.TempDir(), "missing.jpg"), FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: "missing.jpg", Size: 10 << 20, ModTime: time.Now(), Type: "image/jpeg", HasPreview: true}}}
	_, err := svc.estimateGeneration(context.Background(), file, "xlarge")
	require.ErrorIs(t, err, ErrPreviewHeaderInvalid)
}

var _ = errors.Is
