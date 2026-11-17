// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package indexing

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"golang.org/x/image/tiff"
)

func TestR3LargeSingleIDATPNGIsReady(t *testing.T) {
	var b bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.NoCompression}
	if err := enc.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 1024, 512))); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	var idat, prefix, tail []byte
	prefix = append(prefix, data[:8]...)
	for off := 8; off < len(data); {
		n := int(binary.BigEndian.Uint32(data[off : off+4]))
		chunk := data[off : off+12+n]
		switch string(chunk[4:8]) {
		case "IDAT":
			idat = append(idat, chunk[8:8+n]...)
		case "IEND":
			tail = append(tail, chunk...)
		default:
			prefix = append(prefix, chunk...)
		}
		off += 12 + n
	}
	var merged bytes.Buffer
	merged.Write(prefix)
	_ = binary.Write(&merged, binary.BigEndian, uint32(len(idat)))
	merged.WriteString("IDAT")
	merged.Write(idat)
	_ = binary.Write(&merged, binary.BigEndian, crc32.ChecksumIEEE(append([]byte("IDAT"), idat...)))
	merged.Write(tail)
	if _, err := png.Decode(bytes.NewReader(merged.Bytes())); err != nil {
		t.Fatal("fixture invalid", err)
	}
	p := filepath.Join(t.TempDir(), "large.png")
	writeBytes(t, p, merged.Bytes())
	fi, _ := os.Stat(p)
	got := readImageHeader(t.Context(), p, versionFromFileInfo(fi, p))
	if got.State != iteminfo.ImageMetaReady || got.Width != 1024 || got.Height != 512 || got.Ratio != 2 {
		t.Fatalf("large single-IDAT PNG=%#v", got)
	}
}

func TestR3LargeTIFFIsReady(t *testing.T) {
	var b bytes.Buffer
	im := image.NewRGBA(image.Rect(0, 0, 1024, 512))
	im.Set(0, 0, color.RGBA{A: 255})
	if err := tiff.Encode(&b, im, &tiff.Options{Compression: tiff.Uncompressed}); err != nil {
		t.Fatal(err)
	}
	if _, err := tiff.Decode(bytes.NewReader(b.Bytes())); err != nil {
		t.Fatal("fixture invalid", err)
	}
	p := filepath.Join(t.TempDir(), "large.tiff")
	writeBytes(t, p, b.Bytes())
	fi, _ := os.Stat(p)
	got := readImageHeader(t.Context(), p, versionFromFileInfo(fi, p))
	if got.State != iteminfo.ImageMetaReady || got.Width != 1024 || got.Height != 512 || got.Ratio != 2 {
		t.Fatalf("large TIFF=%#v", got)
	}
}
