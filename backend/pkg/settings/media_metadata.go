package settings

import "time"

const (
	// DefaultMetadataWorkers is the per-request audio/video extraction concurrency when unset.
	DefaultMetadataWorkers = 2
	// MaxMetadataWorkers is the hard upper bound for MetadataWorkers.
	MaxMetadataWorkers = 8

	// DefaultMetadataTimeout is the per-file metadata extraction budget when unset.
	DefaultMetadataTimeout = 10 * time.Second
	// MaxMetadataTimeout is the hard upper bound for MetadataTimeoutSeconds.
	MaxMetadataTimeout = 60 * time.Second
)

// ResolvedMetadataWorkers returns the per-request worker count, applying the
// default (2) and hard cap (8) so an accidental large value cannot fan out
// unbounded ffprobe processes.
func (m Media) ResolvedMetadataWorkers() int {
	switch {
	case m.MetadataWorkers <= 0:
		return DefaultMetadataWorkers
	case m.MetadataWorkers > MaxMetadataWorkers:
		return MaxMetadataWorkers
	default:
		return m.MetadataWorkers
	}
}

// ResolvedMetadataTimeout returns the per-file extraction timeout, applying the
// default (10s) and hard cap (60s).
func (m Media) ResolvedMetadataTimeout() time.Duration {
	switch {
	case m.MetadataTimeoutSeconds <= 0:
		return DefaultMetadataTimeout
	case m.MetadataTimeoutSeconds >= int(MaxMetadataTimeout/time.Second):
		// Compare whole seconds before multiplying so a pathological config
		// value cannot overflow time.Duration.
		return MaxMetadataTimeout
	default:
		return time.Duration(m.MetadataTimeoutSeconds) * time.Second
	}
}
