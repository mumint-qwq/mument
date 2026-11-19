// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package imagemeta

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	tiffTagImageWidth  = 0x0100
	tiffTagImageLength = 0x0101
)

// TIFFHeader is a structural header read: dimensions and orientation come from
// IFD0 without touching pixel/strip payload.
type TIFFHeader struct {
	Width       int
	Height      int
	Orientation OrientationResult
}

func readTIFFIFD0(r io.ReadSeeker) (binary.ByteOrder, []byte, int, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, 0, err
	}
	var header [8]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, nil, 0, err
	}
	var order binary.ByteOrder
	switch string(header[0:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return nil, nil, 0, errors.New("invalid TIFF byte order")
	}
	if order.Uint16(header[2:4]) != 42 {
		return nil, nil, 0, errors.New("unsupported TIFF magic")
	}
	ifd0 := order.Uint32(header[4:8])
	if ifd0 < 8 || ifd0 > 1<<30 {
		return nil, nil, 0, errors.New("invalid TIFF IFD offset")
	}
	if _, err := r.Seek(int64(ifd0), io.SeekStart); err != nil {
		return nil, nil, 0, err
	}
	var countBuf [2]byte
	if _, err := io.ReadFull(r, countBuf[:]); err != nil {
		return nil, nil, 0, err
	}
	count := int(order.Uint16(countBuf[:]))
	if count > 4096 {
		return nil, nil, 0, ErrOrientationDeferred
	}
	entries := make([]byte, count*12)
	if _, err := io.ReadFull(r, entries); err != nil {
		return nil, nil, 0, err
	}
	return order, entries, count, nil
}

func tiffEntryUint(order binary.ByteOrder, e []byte) (uint32, error) {
	if count := order.Uint32(e[4:8]); count != 1 {
		return 0, fmt.Errorf("unsupported TIFF field count %d", count)
	}
	switch order.Uint16(e[2:4]) {
	case tiffTypeShort:
		return uint32(order.Uint16(e[8:10])), nil
	case tiffTypeLong:
		return order.Uint32(e[8:12]), nil
	default:
		return 0, fmt.Errorf("unsupported TIFF field type %d", order.Uint16(e[2:4]))
	}
}

// ReadTIFFHeader returns the standard IFD0 dimensions and orientation. It is
// used instead of image.DecodeConfig for TIFF so a large uncompressed file does
// not make the decoder read strip/pixel payload just to learn its dimensions.
func ReadTIFFHeader(r io.ReadSeeker) TIFFHeader {
	var out TIFFHeader
	out.Orientation = OrientationResult{Value: 1}
	order, entries, count, err := readTIFFIFD0(r)
	if err != nil {
		out.Orientation.Err = err
		return out
	}
	for i := 0; i < count; i++ {
		e := entries[i*12 : i*12+12]
		tag := order.Uint16(e[0:2])
		switch tag {
		case tiffTagImageWidth:
			v, err := tiffEntryUint(order, e)
			if err != nil {
				out.Orientation.Err = err
				return out
			}
			out.Width = int(v)
		case tiffTagImageLength:
			v, err := tiffEntryUint(order, e)
			if err != nil {
				out.Orientation.Err = err
				return out
			}
			out.Height = int(v)
		case tiffTagOrientation:
			if order.Uint32(e[4:8]) == 0 {
				continue
			}
			v, err := tiffEntryUint(order, e)
			if err != nil {
				out.Orientation.Err = err
				return out
			}
			if v < 1 || v > 8 {
				out.Orientation.Err = fmt.Errorf("invalid TIFF orientation %d", v)
				return out
			}
			out.Orientation.Value = uint16(v)
			out.Orientation.Present = true
		}
	}
	if out.Width <= 0 || out.Height <= 0 {
		out.Orientation.Err = errors.New("TIFF dimensions missing")
		return out
	}
	return out
}

func readTIFFOrientation(r io.ReadSeeker) OrientationResult {
	return ReadTIFFHeader(r).Orientation
}
