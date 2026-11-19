// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package preview

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
)

// Header inspection is intentionally bounded: preview admission must never
// allocate a large buffer just to decide whether a decode is safe.
const (
	previewHeaderReadLimit     = 1 << 20 // 1 MiB is enough for SOF/IHDR/GIF/WEBP headers
	previewHeaderReadTimeLimit = 2 * time.Second
	maxPreviewDimension        = 100_000
	maxPreviewPixels           = 1 << 28 // 268,435,456 pixels; larger files are a bounded explicit failure
	maxAnimationFrames         = 4096
	previewEstimateOverhead    = 16 << 20
)

var (
	// ErrPreviewOverBudget means the conservative decode estimate exceeds the
	// configured preview memory budget. Callers must not retry the same decode
	// without changing the source or the budget.
	ErrPreviewOverBudget = errors.New("preview generation exceeds the memory budget")
	// ErrPreviewHeaderInvalid means the image header could not be read safely.
	ErrPreviewHeaderInvalid = errors.New("preview source header is invalid")
	// ErrPreviewDimensionsTooLarge means the header declares dimensions beyond
	// the parser's bounded acceptance limits.
	ErrPreviewDimensionsTooLarge = errors.New("preview source dimensions are too large")
)

type previewEstimate struct {
	Bytes       int64
	Pixels      int64
	Frames      int
	Kind        string
	HeaderBytes int
	// Source is captured from the same descriptor that supplied the header.
	// Generation must prove that descriptor still matches before decoding.
	Source *sourceBinding
}

type jpegComponent struct {
	h int
	v int
}

type jpegHeader struct {
	width       int
	height      int
	progressive bool
	components  []jpegComponent
	orientation int
	exifFound   bool
	exifParsed  bool
}

func previewTargetBytes(previewSize string) int64 {
	opts, err := getPreviewOptions(previewSize)
	if err != nil {
		// "original" and unknown sizes are served/resized elsewhere; use the
		// largest configured output as a bounded working-set estimate.
		return 1024 * 1024 * 4 * 2
	}
	return int64(opts.Width) * int64(opts.Height) * 4 * 2
}

func withSafetyMargin(estimate int64) int64 {
	if estimate <= 0 {
		return previewEstimateOverhead
	}
	return estimate + estimate/10
}

// estimateGeneration returns a conservative byte estimate for the work that the
// preview path may perform for this file/type. It reads only a bounded header;
// it never decodes pixels. Errors are explicit so callers cannot silently
// bypass admission.
func (s *Service) estimateGeneration(ctx context.Context, file iteminfo.ExtendedFileInfo, previewSize string) (previewEstimate, error) {
	if err := ctx.Err(); err != nil {
		return previewEstimate{}, err
	}
	previewType := determinePreviewType(file)
	target := previewTargetBytes(previewSize)
	var estimate previewEstimate
	switch previewType {
	case previewTypeDocument:
		// MuPDF renders at the target size under docGenMutex. The reservation is
		// deliberately conservative; it is not a measured Go heap bound.
		estimate = previewEstimate{Bytes: 128 << 20, Kind: "document"}
	case previewTypeOffice:
		estimate = previewEstimate{Bytes: 64 << 20, Kind: "office"}
	case previewTypeHEIC:
		// FFmpeg decodes the container, then the orientation/resize step may
		// decode a full-resolution JPEG in-process. 256 MiB is a conservative
		// fixed reservation for that second step.
		estimate = previewEstimate{Bytes: 256 << 20, Kind: "heic-ffmpeg"}
	case previewTypeVideo:
		estimate = previewEstimate{Bytes: 64 << 20, Kind: "video-ffmpeg"}
	case previewTypeAudio:
		art := int64(0)
		if file.Metadata != nil {
			art = int64(len(file.Metadata.AlbumArt))
		}
		est := art*6 + target + previewEstimateOverhead
		estimate = previewEstimate{Bytes: withSafetyMargin(est), Kind: "audio-art"}
	case previewTypeImage:
		ext := strings.ToLower(filepath.Ext(file.Name))
		if iteminfo.IsRawImage(ext) {
			// RAW/HEIC top-level previews use the embedded JPEG path; only the
			// small embedded payload is decoded in-process.
			estimate = previewEstimate{Bytes: 64 << 20, Kind: "embedded-raw"}
		} else if ext == ".heic" || ext == ".heif" {
			estimate = previewEstimate{Bytes: 256 << 20, Kind: "heic-inprocess"}
		} else {
			// Always inspect the file's own descriptor, including small files.
			// Index Size may be stale or an allocated-block count, and a small
			// path can be replaced by a large image while queued.
			return estimateRasterHeader(ctx, s, file, target)
		}
	default:
		// Unsupported types fail before any decode. A tiny reservation keeps the
		// task accounting uniform without hiding the real error.
		estimate = previewEstimate{Bytes: 1 << 20, Kind: "unsupported"}
	}
	if binding, err := bindSourcePath(file.RealPath); err == nil {
		estimate.Source = &binding
	}
	return estimate, nil
}

func estimateRasterHeader(ctx context.Context, svc *Service, file iteminfo.ExtendedFileInfo, target int64) (previewEstimate, error) {
	releaseHeader, err := svc.acquireHeaderRead(ctx)
	if err != nil {
		return previewEstimate{}, err
	}
	defer releaseHeader()

	f, err := os.Open(file.RealPath)
	if err != nil {
		return previewEstimate{}, fmt.Errorf("%w: %v", ErrPreviewHeaderInvalid, err)
	}
	defer f.Close()
	binding, err := newSourceBinding(f)
	if err != nil {
		return previewEstimate{}, err
	}
	header, err := readPreviewHeader(ctx, f)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return previewEstimate{}, err
		}
		// Files below the "serve original bytes" threshold are read without a
		// pixel decode, so an unreadable header is safe to admit at file size.
		if binding.size > 0 && binding.size < 256*1024 {
			return previewEstimate{Bytes: withSafetyMargin(binding.size + target + previewEstimateOverhead), Kind: "small-raw-bytes", HeaderBytes: len(header), Source: &binding}, nil
		}
		return previewEstimate{}, err
	}
	var estimate previewEstimate
	if len(header) >= 2 && header[0] == 0xff && header[1] == 0xd8 {
		estimate, err = estimateJPEGHeader(header, target)
	} else {
		estimate, err = estimateNonJPEGHeader(ctx, header, file, target)
	}
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && binding.size > 0 && binding.size < 256*1024 {
		return previewEstimate{Bytes: withSafetyMargin(binding.size + target + previewEstimateOverhead), Kind: "small-raw-bytes", HeaderBytes: len(header), Source: &binding}, nil
	}
	estimate.Source = &binding
	return estimate, err
}

func (s *Service) acquireHeaderRead(ctx context.Context) (func(), error) {
	if s == nil || s.headerSem == nil {
		return func() {}, nil
	}
	select {
	case s.headerSem <- struct{}{}:
		if s.stats != nil {
			s.stats.headerReads.Add(1)
		}
		return func() { <-s.headerSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func readPreviewHeader(ctx context.Context, f *os.File) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	start := time.Now()
	buf := make([]byte, 0, 64*1024)
	chunk := make([]byte, 64*1024)
	for len(buf) < previewHeaderReadLimit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if time.Since(start) > previewHeaderReadTimeLimit {
			return nil, fmt.Errorf("%w: header read exceeded %s", ErrPreviewHeaderInvalid, previewHeaderReadTimeLimit)
		}
		n, readErr := f.Read(chunk)
		if n > 0 {
			remain := previewHeaderReadLimit - len(buf)
			if n > remain {
				n = remain
			}
			buf = append(buf, chunk[:n]...)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrPreviewHeaderInvalid, readErr)
		}
		if n == 0 {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(buf) == 0 {
		return nil, fmt.Errorf("%w: empty file", ErrPreviewHeaderInvalid)
	}
	return buf, nil
}

func estimateJPEGHeader(buf []byte, target int64) (previewEstimate, error) {
	header, err := parseJPEGHeader(buf)
	if err != nil {
		return previewEstimate{}, err
	}
	if err := validateDimensions(header.width, header.height); err != nil {
		return previewEstimate{}, err
	}
	pixels := int64(header.width) * int64(header.height)
	maxH, maxV := 1, 1
	for _, c := range header.components {
		if c.h > maxH {
			maxH = c.h
		}
		if c.v > maxV {
			maxV = c.v
		}
	}
	if maxH < 1 || maxV < 1 {
		return previewEstimate{}, fmt.Errorf("%w: invalid sampling factors", ErrPreviewHeaderInvalid)
	}

	// YCbCr plane allocation follows the JPEG sampling factors. This is the
	// exact pixel surface the Go decoder retains until resize completes.
	var planes int64
	for _, c := range header.components {
		hs := maxH / c.h
		vs := maxV / c.v
		if hs < 1 {
			hs = 1
		}
		if vs < 1 {
			vs = 1
		}
		pw := (int64(header.width) + int64(hs) - 1) / int64(hs)
		ph := (int64(header.height) + int64(vs) - 1) / int64(vs)
		planes += pw * ph
	}

	// Progressive decoders retain one int32 coefficient block (64*4 bytes) per
	// 8x8 block per component until the final scan completes. This is the
	// dominant cost for the high-pixel progressive JPEGs measured in 007-A.
	var coefficients int64
	if header.progressive {
		mx := (int64(header.width) + int64(8*maxH) - 1) / int64(8*maxH)
		my := (int64(header.height) + int64(8*maxV) - 1) / int64(8*maxV)
		for _, c := range header.components {
			coefficients += mx * my * int64(c.h) * int64(c.v) * 64 * 4
		}
	}

	var orientationExtra int64
	if header.exifFound && (!header.exifParsed || (header.orientation != 0 && header.orientation != 1)) {
		// Auto-orientation may materialise a full-resolution NRGBA frame.
		orientationExtra = pixels * 4
	}
	if len(header.components) == 4 {
		// CMYK/YCCK is commonly converted to an RGB-compatible in-process image;
		// reserve one more full frame for that conversion.
		orientationExtra += pixels * 4
	}

	est := coefficients + planes + orientationExtra + target + previewEstimateOverhead
	kind := "jpeg"
	if header.progressive {
		kind = "jpeg-progressive"
	}
	return previewEstimate{
		Bytes:       withSafetyMargin(est),
		Pixels:      pixels,
		Frames:      1,
		Kind:        kind,
		HeaderBytes: len(buf),
	}, nil
}

func parseJPEGHeader(buf []byte) (jpegHeader, error) {
	var header jpegHeader
	if len(buf) < 4 || buf[0] != 0xff || buf[1] != 0xd8 {
		return header, fmt.Errorf("%w: missing JPEG SOI", ErrPreviewHeaderInvalid)
	}
	sawSOF := false
	i := 2
	for i+1 < len(buf) {
		if buf[i] != 0xff {
			return header, fmt.Errorf("%w: JPEG marker alignment", ErrPreviewHeaderInvalid)
		}
		for i < len(buf) && buf[i] == 0xff {
			i++
		}
		if i >= len(buf) {
			break
		}
		marker := buf[i]
		i++
		switch {
		case marker == 0xd9: // EOI
			return header, fmt.Errorf("%w: JPEG ended before SOS", ErrPreviewHeaderInvalid)
		case marker == 0xda: // SOS
			if !sawSOF {
				return header, fmt.Errorf("%w: JPEG SOS before SOF", ErrPreviewHeaderInvalid)
			}
			return header, nil
		case marker == 0x01 || (marker >= 0xd0 && marker <= 0xd7):
			continue
		}
		if i+2 > len(buf) {
			return header, fmt.Errorf("%w: truncated JPEG segment length", ErrPreviewHeaderInvalid)
		}
		n := int(binary.BigEndian.Uint16(buf[i : i+2]))
		if n < 2 || i+n > len(buf) {
			return header, fmt.Errorf("%w: truncated JPEG segment", ErrPreviewHeaderInvalid)
		}
		payload := buf[i+2 : i+n]
		if marker == 0xe1 {
			if orientation, found, parsed := parseExifOrientation(payload); found {
				header.exifFound = true
				header.exifParsed = parsed
				if parsed {
					header.orientation = orientation
				}
			}
		}
		if isJPEGSOF(marker) {
			parsed, err := parseJPEGSOF(marker, payload)
			if err != nil {
				return header, err
			}
			header.width = parsed.width
			header.height = parsed.height
			header.progressive = parsed.progressive
			header.components = parsed.components
			sawSOF = true
		}
		i += n
	}
	return header, fmt.Errorf("%w: JPEG header exceeds the bounded read limit", ErrPreviewHeaderInvalid)
}

func isJPEGSOF(marker byte) bool {
	switch marker {
	case 0xc0, 0xc1, 0xc2, 0xc3, 0xc5, 0xc6, 0xc7, 0xc9, 0xca, 0xcb, 0xcd, 0xce, 0xcf:
		return true
	}
	return false
}

func parseJPEGSOF(marker byte, payload []byte) (jpegHeader, error) {
	var header jpegHeader
	if len(payload) < 6 {
		return header, fmt.Errorf("%w: truncated SOF", ErrPreviewHeaderInvalid)
	}
	header.height = int(binary.BigEndian.Uint16(payload[1:3]))
	header.width = int(binary.BigEndian.Uint16(payload[3:5]))
	n := int(payload[5])
	if n < 1 || len(payload) != 6+3*n {
		return header, fmt.Errorf("%w: inconsistent SOF component count", ErrPreviewHeaderInvalid)
	}
	header.components = make([]jpegComponent, 0, n)
	for k := 0; k < n; k++ {
		hv := payload[6+k*3+1]
		h := int(hv >> 4)
		v := int(hv & 0x0f)
		if h < 1 || v < 1 || h > 4 || v > 4 {
			return header, fmt.Errorf("%w: invalid JPEG sampling factor", ErrPreviewHeaderInvalid)
		}
		header.components = append(header.components, jpegComponent{h: h, v: v})
	}
	header.progressive = marker == 0xc2 || marker == 0xc6 || marker == 0xca || marker == 0xce
	return header, nil
}

// parseExifOrientation parses only APP1 EXIF IFD0 orientation. found reports
// whether an EXIF APP1 block was present; parsed reports whether orientation
// could actually be read (an unparsed EXIF block is treated conservatively).
func parseExifOrientation(payload []byte) (orientation int, found bool, parsed bool) {
	if len(payload) < 6 || !bytes.Equal(payload[:6], []byte("Exif\x00\x00")) {
		return 0, false, false
	}
	t := payload[6:]
	if len(t) < 8 {
		return 0, true, false
	}
	var order binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0, true, false
	}
	if order.Uint16(t[2:4]) != 42 {
		return 0, true, false
	}
	off := int(order.Uint32(t[4:8]))
	if off < 8 || off+2 > len(t) {
		return 0, true, false
	}
	count := int(order.Uint16(t[off : off+2]))
	base := off + 2
	if count < 0 || base+count*12 > len(t) {
		return 0, true, false
	}
	for k := 0; k < count; k++ {
		entry := t[base+k*12 : base+(k+1)*12]
		if order.Uint16(entry[0:2]) != 0x0112 {
			continue
		}
		if order.Uint16(entry[2:4]) != 3 || order.Uint32(entry[4:8]) < 1 {
			return 0, true, false
		}
		value := int(order.Uint16(entry[8:10]))
		if value < 1 || value > 8 {
			return 0, true, false
		}
		return value, true, true
	}
	return 0, true, true
}

func estimateNonJPEGHeader(ctx context.Context, buf []byte, file iteminfo.ExtendedFileInfo, target int64) (previewEstimate, error) {
	if err := ctx.Err(); err != nil {
		return previewEstimate{}, err
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(buf))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return previewEstimate{}, fmt.Errorf("%w: cannot decode image configuration", ErrPreviewHeaderInvalid)
	}
	if err := validateDimensions(cfg.Width, cfg.Height); err != nil {
		return previewEstimate{}, err
	}
	pixels := int64(cfg.Width) * int64(cfg.Height)
	frames := 1
	switch format {
	case "png":
		frames, err = pngAnimationFrames(buf)
	case "gif":
		frames, err = gifAnimationFrames(buf, int64(len(buf)) >= file.Size)
		if frames == 0 {
			frames = 1
		}
	case "webp":
		frames, err = webpAnimationFrames(buf, int64(len(buf)) >= file.Size)
	case "tiff", "tif":
		// TIFF can contain multiple directories; imaging decodes the primary
		// frame. Treat it as one frame with an NRGBA-sized working surface.
		frames = 1
	}
	if err != nil {
		return previewEstimate{}, fmt.Errorf("%w: %v", ErrPreviewHeaderInvalid, err)
	}
	if frames < 1 {
		frames = 1
	}
	if frames > maxAnimationFrames {
		return previewEstimate{}, fmt.Errorf("%w: animation frames exceed the bounded limit", ErrPreviewDimensionsTooLarge)
	}
	// The decoder does not always return an 8-bit RGBA surface. In particular,
	// 16-bit PNG decodes to image.NRGBA64/RGBA64/Gray16, whose pixel surface is
	// twice as large per channel. Use the format header rather than assuming
	// four bytes per pixel.
	bytesPerPixel := int64(8) // unknown format: conservative default
	kind := "raster-" + format + "-conservative"
	switch format {
	case "png":
		bytesPerPixel, err = pngDecoderBytesPerPixel(buf)
		if err != nil {
			// DecodeConfig succeeded, so the file is a PNG; if the IHDR cannot
			// be classified safely, reserve the largest in-process surface.
			bytesPerPixel = 8
		} else {
			kind = "raster-png"
		}
	case "tiff", "tif":
		// x/image/tiff may return Gray16/RGBA64/NRGBA64 for 16-bit samples.
		bytesPerPixel = 8
		kind = "raster-tiff"
	case "gif", "webp", "bmp":
		bytesPerPixel = 4
		kind = "raster-" + format
	}
	decoded := pixels * bytesPerPixel * int64(frames)
	est := decoded + target + previewEstimateOverhead
	return previewEstimate{
		Bytes:       withSafetyMargin(est),
		Pixels:      pixels,
		Frames:      frames,
		Kind:        kind,
		HeaderBytes: len(buf),
	}, nil
}

// pngDecoderBytesPerPixel returns a conservative pixel-surface size for the
// concrete image type produced by the APNG/PNG decoder.
func pngDecoderBytesPerPixel(buf []byte) (int64, error) {
	if len(buf) < 29 || !bytes.Equal(buf[:8], []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}) {
		return 0, errors.New("not a PNG")
	}
	if string(buf[12:16]) != "IHDR" {
		return 0, errors.New("PNG missing IHDR")
	}
	bitDepth := int(buf[24])
	colorType := int(buf[25])
	switch colorType {
	case 0: // grayscale
		if bitDepth == 16 {
			// Gray16 may be promoted to NRGBA64 when transparency is present.
			return 8, nil
		}
		// A tRNS chunk makes the 8-bit/low-bit grayscale decoder allocate an
		// NRGBA surface (4 B/px), even though IHDR alone looks like 1 B/px.
		// If the bounded header cannot see IDAT/IEND, conservatively assume
		// transparency rather than underestimating the pixel surface.
		trns, complete := pngHasTransparencyChunk(buf)
		if trns || !complete {
			return 4, nil
		}
		return 1, nil
	case 2, 4, 6: // RGB, gray+alpha, RGBA
		if bitDepth == 16 {
			return 8, nil
		}
		return 4, nil
	case 3: // palette; tRNS changes alpha values, not the paletted pixel surface.
		return 1, nil
	default:
		return 0, fmt.Errorf("unsupported PNG color type %d", colorType)
	}
}

// pngHasTransparencyChunk reports whether a tRNS chunk appears before the first
// IDAT. complete is false when the bounded header ends before IDAT/IEND, in
// which case callers must use a conservative estimate.
func pngHasTransparencyChunk(buf []byte) (trns bool, complete bool) {
	if len(buf) < 8 {
		return false, false
	}
	for i := 8; i+8 <= len(buf); {
		n := int(binary.BigEndian.Uint32(buf[i : i+4]))
		if n < 0 {
			return false, false
		}
		typ := string(buf[i+4 : i+8])
		switch typ {
		case "tRNS":
			if i+12+n > len(buf) {
				return false, false
			}
			return true, true
		case "IDAT", "IEND":
			return false, true
		}
		next := int64(i) + 12 + int64(n)
		if next <= int64(i) || next > int64(len(buf)) {
			return false, false
		}
		i = int(next)
	}
	return false, false
}

func validateDimensions(width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("%w: width/height must be positive", ErrPreviewHeaderInvalid)
	}
	if width > maxPreviewDimension || height > maxPreviewDimension {
		return fmt.Errorf("%w: dimension %dx%d exceeds %d", ErrPreviewDimensionsTooLarge, width, height, maxPreviewDimension)
	}
	pixels := int64(width) * int64(height)
	if pixels > maxPreviewPixels {
		return fmt.Errorf("%w: %d pixels exceeds %d", ErrPreviewDimensionsTooLarge, pixels, maxPreviewPixels)
	}
	return nil
}

func pngAnimationFrames(buf []byte) (int, error) {
	if len(buf) < 8 || !bytes.Equal(buf[:8], []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}) {
		return 0, errors.New("not a PNG")
	}
	for i := 8; i+8 <= len(buf); {
		n := int(binary.BigEndian.Uint32(buf[i : i+4]))
		typ := string(buf[i+4 : i+8])
		if typ == "acTL" {
			if n < 8 || i+8+8 > len(buf) {
				return 0, errors.New("truncated APNG acTL")
			}
			frames := int(binary.BigEndian.Uint32(buf[i+8 : i+12]))
			if frames < 1 {
				return 0, errors.New("invalid APNG frame count")
			}
			return frames, nil
		}
		if typ == "IDAT" || typ == "IEND" {
			return 1, nil
		}
		next := int64(i) + 12 + int64(n)
		if next <= int64(i) || next > int64(len(buf)) {
			return 0, errors.New("truncated PNG chunk")
		}
		i = int(next)
	}
	return 1, nil
}

func gifAnimationFrames(buf []byte, complete bool) (int, error) {
	if len(buf) < 13 || !bytes.HasPrefix(buf, []byte("GIF8")) {
		return 0, errors.New("not a GIF")
	}
	packed := buf[10]
	i := 13
	if packed&0x80 != 0 {
		i += 3 * (1 << ((packed & 0x07) + 1))
	}
	frames := 0
	for i < len(buf) {
		switch buf[i] {
		case 0x3b: // trailer
			return frames, nil
		case 0x21: // extension
			i++
			if i >= len(buf) {
				return 0, errors.New("truncated GIF extension")
			}
			i++ // label
			for {
				if i >= len(buf) {
					return 0, errors.New("truncated GIF extension block")
				}
				n := int(buf[i])
				i++
				if n == 0 {
					break
				}
				if i+n > len(buf) {
					return 0, errors.New("truncated GIF extension data")
				}
				i += n
			}
		case 0x2c: // image descriptor
			if i+10 > len(buf) {
				return 0, errors.New("truncated GIF image descriptor")
			}
			packed := buf[i+9]
			i += 10
			if packed&0x80 != 0 {
				i += 3 * (1 << ((packed & 0x07) + 1))
			}
			if i >= len(buf) {
				return 0, errors.New("truncated GIF LZW header")
			}
			i++ // LZW minimum code size
			for {
				if i >= len(buf) {
					return 0, errors.New("truncated GIF image data")
				}
				n := int(buf[i])
				i++
				if n == 0 {
					break
				}
				if i+n > len(buf) {
					return 0, errors.New("truncated GIF image data")
				}
				i += n
			}
			frames++
			if frames > maxAnimationFrames {
				return frames, nil
			}
		default:
			if complete {
				return 0, errors.New("invalid GIF block")
			}
			return 0, errors.New("GIF header exceeds the bounded read limit")
		}
	}
	if complete {
		return 0, errors.New("GIF missing trailer")
	}
	return 0, errors.New("GIF header exceeds the bounded read limit")
}

func webpAnimationFrames(buf []byte, complete bool) (int, error) {
	if len(buf) < 12 || string(buf[:4]) != "RIFF" || string(buf[8:12]) != "WEBP" {
		return 0, errors.New("not a WebP")
	}
	i := 12
	frames := 0
	animated := false
	for i+8 <= len(buf) {
		typ := string(buf[i : i+4])
		n := int(binary.LittleEndian.Uint32(buf[i+4 : i+8]))
		if n < 0 {
			return 0, errors.New("invalid WebP chunk length")
		}
		if typ == "VP8X" && n >= 1 && i+8+1 <= len(buf) {
			animated = buf[i+8]&0x02 != 0
		}
		if typ == "ANMF" {
			frames++
			if frames > maxAnimationFrames {
				return frames, nil
			}
		}
		next := int64(i) + 8 + int64(n) + int64(n&1)
		if next <= int64(i) || next > int64(len(buf)) {
			if !complete || typ == "ANMF" {
				if complete {
					return frames, nil
				}
				return 0, errors.New("WebP header exceeds the bounded read limit")
			}
			break
		}
		i = int(next)
	}
	if animated && frames == 0 {
		return 0, errors.New("animated WebP without ANMF")
	}
	if frames == 0 {
		frames = 1
	}
	return frames, nil
}
