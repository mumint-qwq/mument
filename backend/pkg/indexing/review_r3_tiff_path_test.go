// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package indexing

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
)

func minimalTIFF(orientation uint16) []byte {
	count := 2
	if orientation != 0 {
		count = 3
	}
	buf := make([]byte, 8+2+count*12+4)
	copy(buf[:2], "II")
	binary.LittleEndian.PutUint16(buf[2:4], 42)
	binary.LittleEndian.PutUint32(buf[4:8], 8)
	binary.LittleEndian.PutUint16(buf[8:10], uint16(count))
	entry := func(i int, tag, typ uint16, value uint32) {
		off := 10 + i*12
		binary.LittleEndian.PutUint16(buf[off:off+2], tag)
		binary.LittleEndian.PutUint16(buf[off+2:off+4], typ)
		binary.LittleEndian.PutUint32(buf[off+4:off+8], 1)
		binary.LittleEndian.PutUint32(buf[off+8:off+12], value)
	}
	entry(0, 0x0100, 3, 1024)
	entry(1, 0x0101, 3, 512)
	if orientation != 0 {
		entry(2, 0x0112, 3, uint32(orientation))
	}
	return buf
}

func TestR3TIFFHeaderPathAppliesOrientation(t *testing.T) {
	for _, orientation := range []uint16{0, 1, 6, 8} {
		p := filepath.Join(t.TempDir(), "header.tiff")
		writeBytes(t, p, minimalTIFF(orientation))
		fi, _ := os.Stat(p)
		got := readImageHeader(t.Context(), p, versionFromFileInfo(fi, p))
		if got.State != iteminfo.ImageMetaReady {
			t.Fatalf("orientation %d result=%#v", orientation, got)
		}
		want := 2.0
		if orientation >= 5 {
			want = 0.5
		}
		if got.Width != 1024 || got.Height != 512 || got.Ratio != want {
			t.Fatalf("orientation %d result=%#v want ratio %v", orientation, got, want)
		}
	}
}
