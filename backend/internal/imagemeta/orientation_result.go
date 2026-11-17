//go:build !386 && !arm

// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.

package imagemeta

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	extimagemeta "github.com/evanoberholster/imagemeta"
)

// ErrOrientationUnsupported means this build/format has no orientation reader.
// It is distinct from "the file confirms there is no orientation tag".
var ErrOrientationUnsupported = errors.New("image orientation is not supported")

// ErrOrientationDeferred means the orientation metadata exists but a bounded
// read could not reach it. The caller should retry later, not call the file bad.
var ErrOrientationDeferred = errors.New("image orientation header exceeds budget")

// OrientationResult separates the three cases the old empty-string API could
// not express: confirmed normal, explicitly tagged, and unreadable.
type OrientationResult struct {
	Value   uint16 // 0 unknown, 1..8 normalized EXIF orientation
	Present bool   // a real orientation tag was read
	Err     error
}

// ReadOrientationFromFile reads orientation from the same bounded reader used
// for dimensions. The caller owns r and must have opened it read-only.
func ReadOrientationFromFile(ctx context.Context, path string, r io.ReadSeeker) OrientationResult {
	if ctx.Err() != nil {
		return OrientationResult{Err: ctx.Err()}
	}
	if path == "" || r == nil {
		return OrientationResult{Err: errors.New("nil orientation input")}
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return OrientationResult{Err: err}
	}

	ext := strings.ToLower(filepath.Ext(path))
	if isHEICExtension(ext) {
		// HEIC orientation depends on the transform parser, which needs a
		// seekable file and has no bounded-reader variant yet. Report it as
		// unsupported rather than silently claiming normal orientation.
		return OrientationResult{Err: ErrOrientationUnsupported}
	}
	if ext == ".tif" || ext == ".tiff" {
		return readTIFFOrientation(r)
	}

	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return OrientationResult{Err: err}
	}
	ex, err := extimagemeta.Decode(r)
	if err == nil {
		if ex.IFD0.Orientation == 0 {
			return OrientationResult{Value: 1}
		}
		if ex.IFD0.Orientation < 1 || ex.IFD0.Orientation > 8 {
			return OrientationResult{Err: fmt.Errorf("invalid EXIF orientation %d", ex.IFD0.Orientation)}
		}
		return OrientationResult{Value: uint16(ex.IFD0.Orientation), Present: true}
	}

	switch ext {
	case ".jpg", ".jpeg", ".jpe":
		present, scanErr := jpegHasEXIF(r)
		if scanErr != nil {
			return OrientationResult{Err: scanErr}
		}
		if !present {
			return OrientationResult{Value: 1}
		}
		return OrientationResult{Err: fmt.Errorf("EXIF present but unreadable: %w", err)}
	case ".png":
		value, present, pngErr := pngEXIFOrientation(r)
		if pngErr != nil {
			return OrientationResult{Err: pngErr}
		}
		if !present {
			return OrientationResult{Value: 1}
		}
		return OrientationResult{Value: value, Present: true}
	default:
		// GIF/WebP/BMP and other containers have no EXIF orientation in the
		// formats this index supports; treat them as confirmed normal.
		return OrientationResult{Value: 1}
	}
}

// jpegHasEXIF walks JPEG APP segments only far enough to tell a missing tag
// from a present-but-corrupt tag. It stops at SOS and never reads pixel data.
func jpegHasEXIF(r io.ReadSeeker) (bool, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	var soi [2]byte
	if _, err := io.ReadFull(r, soi[:]); err != nil {
		return false, err
	}
	if soi[0] != 0xff || soi[1] != 0xd8 {
		return false, errors.New("not a JPEG")
	}
	var total int64
	for {
		var marker [2]byte
		if _, err := io.ReadFull(r, marker[:]); err != nil {
			return false, err
		}
		total += 2
		if total > 1<<20 {
			return false, errors.New("JPEG header exceeds 1MiB")
		}
		if marker[0] != 0xff {
			return false, errors.New("invalid JPEG marker")
		}
		for marker[1] == 0xff {
			if _, err := io.ReadFull(r, marker[1:]); err != nil {
				return false, err
			}
			total++
		}
		m := marker[1]
		if m == 0xd9 || m == 0xda {
			return false, nil
		}
		if m == 0x01 || (m >= 0xd0 && m <= 0xd7) {
			continue
		}
		var lenBuf [2]byte
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return false, err
		}
		segLen := int(binary.BigEndian.Uint16(lenBuf[:]))
		total += 2
		if segLen < 2 {
			return false, errors.New("invalid JPEG segment length")
		}
		payload := make([]byte, segLen-2)
		if _, err := io.ReadFull(r, payload); err != nil {
			return false, err
		}
		total += int64(len(payload))
		if m == 0xe1 && len(payload) >= 6 && bytes.Equal(payload[:6], []byte("Exif\x00\x00")) {
			return true, nil
		}
		if total > 1<<20 {
			return false, errors.New("JPEG header exceeds 1MiB")
		}
	}
}

// pngEXIFOrientation reads the optional eXIf chunk. PNG without eXIf is
// confirmed normal; a present but malformed eXIf is an error, not normal.
func pngEXIFOrientation(r io.ReadSeeker) (uint16, bool, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, false, err
	}
	var sig [8]byte
	if _, err := io.ReadFull(r, sig[:]); err != nil {
		return 0, false, err
	}
	if !bytes.Equal(sig[:], []byte("\x89PNG\r\n\x1a\n")) {
		return 0, false, errors.New("not a PNG")
	}
	var total int64 = 8
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return 0, false, err
		}
		total += 8
		length := binary.BigEndian.Uint32(hdr[:4])
		kind := string(hdr[4:8])
		if kind == "IEND" {
			return 0, false, nil
		}
		if kind == "IDAT" {
			// eXIf must precede image data; do not scan pixel data to prove its
			// absence. A large IDAT is a valid image, not a corrupt header.
			return 0, false, nil
		}
		if kind == "eXIf" {
			if length > 1<<20 {
				return 0, false, ErrOrientationDeferred
			}
			payload := make([]byte, length)
			if _, err := io.ReadFull(r, payload); err != nil {
				return 0, false, err
			}
			order, ifd0, ok := tiffHeaderIFD0(payload)
			if !ok || ifd0 == 0 {
				return 0, false, errors.New("invalid PNG eXIf TIFF header")
			}
			ifd, _, _, err := parseTIFFIFD(payload, order, ifd0)
			if err != nil {
				return 0, false, err
			}
			tag, ok := ifd.tags[tiffTagOrientation]
			if !ok || tag.count == 0 {
				return 0, false, nil
			}
			value := tiffTagFirstUint16(tag, order)
			if value < 1 || value > 8 {
				return 0, false, fmt.Errorf("invalid PNG orientation %d", value)
			}
			return value, true, nil
		}
		// Skip non-eXIf chunks by seeking, not by reading their payload. This
		// keeps a large IDAT/ancillary chunk from exhausting the header budget.
		if _, err := r.Seek(int64(length)+4, io.SeekCurrent); err != nil {
			return 0, false, err
		}
		total += int64(length) + 4
		if total > 1<<30 {
			return 0, false, errors.New("PNG chunk chain exceeds 1GiB")
		}
	}
}
