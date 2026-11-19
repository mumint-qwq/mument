// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package iteminfo

// ImageParserRevision is stored with every image metadata result. Bump it when
// the header-reading or orientation rules change so old results are revalidated.
const ImageParserRevision uint16 = 1

// ImageMetaState is a finite state, not a set of booleans. Zero is deliberately
// "unknown": a row can have a file version without having readable dimensions.
type ImageMetaState uint8

const (
	ImageMetaUnknown ImageMetaState = iota
	ImageMetaReady
	ImageMetaDeferred
	ImageMetaUnsupported
	ImageMetaFailed
)

// ImageIndexMeta is private index state for one image. It is never marshalled
// into a directory response; only DisplayAspectRatio is public.
type ImageIndexMeta struct {
	Version     string         `json:"-"`
	ObsSeq      uint64         `json:"-"` // directory observation that produced this candidate
	Guard       uint64         `json:"-"` // row-lifetime birth token, allocated by INSERT
	State       ImageMetaState `json:"-"`
	Width       int            `json:"-"`
	Height      int            `json:"-"`
	Orientation uint8          `json:"-"` // 0 unknown, 1 normal, 2..8 EXIF
	ParserRev   uint16         `json:"-"`
	RetryCount  uint16         `json:"-"`
	RetryAt     int64          `json:"-"` // Unix seconds
	LogicalSize int64          `json:"-"`
	MTimeNS     int64          `json:"-"`
}

// Ready reports whether the stored result may be served for the current parser
// revision. Width/height/orientation and a finite positive ratio are required;
// a positive ratio alone is not enough.
func (m *ImageIndexMeta) Ready(parserRev uint16, ratio float64) bool {
	if m == nil || m.State != ImageMetaReady || m.ParserRev != parserRev {
		return false
	}
	if m.Width <= 0 || m.Height <= 0 || m.Orientation < 1 || m.Orientation > 8 {
		return false
	}
	return ratio > 0 && !isNaNOrInf(ratio)
}

// DisplayAspectRatioFromDimensions applies EXIF orientation exactly once.
func DisplayAspectRatioFromDimensions(width, height int, orientation uint8) float64 {
	if width <= 0 || height <= 0 {
		return 0
	}
	if orientation >= 5 && orientation <= 8 {
		width, height = height, width
	}
	ratio := float64(width) / float64(height)
	if ratio <= 0 || isNaNOrInf(ratio) {
		return 0
	}
	return ratio
}

func isNaNOrInf(v float64) bool {
	// Avoid importing math in this small data package; NaN is the only value
	// that is neither equal to itself nor to its negation.
	return v != v || v > 1.7976931348623157e308 || v < -1.7976931348623157e308
}
