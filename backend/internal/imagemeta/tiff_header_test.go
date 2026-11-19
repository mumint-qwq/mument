// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package imagemeta

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func tiFFHeaderWithOrientation(orientation uint16) []byte {
	count := 2
	if orientation != 0 {
		count = 3
	}
	buf := make([]byte, 8+2+count*12+4)
	copy(buf[:2], "II")
	binary.LittleEndian.PutUint16(buf[2:4], 42)
	binary.LittleEndian.PutUint32(buf[4:8], 8)
	binary.LittleEndian.PutUint16(buf[8:10], uint16(count))
	write := func(i int, tag, typ uint16, value uint32) {
		off := 10 + i*12
		binary.LittleEndian.PutUint16(buf[off:off+2], tag)
		binary.LittleEndian.PutUint16(buf[off+2:off+4], typ)
		binary.LittleEndian.PutUint32(buf[off+4:off+8], 1)
		binary.LittleEndian.PutUint32(buf[off+8:off+12], value)
	}
	write(0, tiffTagImageWidth, tiffTypeShort, 1024)
	write(1, tiffTagImageLength, tiffTypeShort, 512)
	if orientation != 0 {
		write(2, tiffTagOrientation, tiffTypeShort, uint32(orientation))
	}
	return buf
}

func TestR3TIFFRejectsArrayFields(t *testing.T) {
	for _, entry := range []int{0, 2} {
		buf := tiFFHeaderWithOrientation(1)
		off := 10 + entry*12
		binary.LittleEndian.PutUint32(buf[off+4:off+8], 2)
		h := ReadTIFFHeader(bytes.NewReader(buf))
		if h.Orientation.Err == nil {
			t.Fatalf("entry %d accepted array count: %#v", entry, h)
		}
	}
}

func TestR3TIFFHeaderOrientationAndDimensions(t *testing.T) {
	for _, orientation := range []uint16{0, 1, 6, 8} {
		h := ReadTIFFHeader(bytes.NewReader(tiFFHeaderWithOrientation(orientation)))
		if h.Orientation.Err != nil || h.Width != 1024 || h.Height != 512 {
			t.Fatalf("orientation %d: %#v", orientation, h)
		}
		if orientation == 0 {
			if h.Orientation.Present || h.Orientation.Value != 1 {
				t.Fatalf("absent orientation=%#v", h.Orientation)
			}
		} else if !h.Orientation.Present || h.Orientation.Value != orientation {
			t.Fatalf("orientation %d=%#v", orientation, h.Orientation)
		}
	}
	buf := tiFFHeaderWithOrientation(9)
	h := ReadTIFFHeader(bytes.NewReader(buf))
	if h.Orientation.Err == nil {
		t.Fatalf("invalid orientation accepted: %#v", h)
	}
}
