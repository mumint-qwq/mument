package files

import "sync/atomic"

// IndexWorkProbe collects aggregate counts for index work that the files layer
// schedules. It is disabled by default and only used by isolated measurements
// and tests; it never records paths.
var indexWorkProbeEnabled atomic.Bool

// IndexWorkStats is a snapshot of index-related work started by the files layer.
type IndexWorkStats struct {
	// RefreshRequests counts RefreshIndex calls that reached the index.
	RefreshRequests int64
	// RefreshSkippedDisabled counts RefreshIndex calls for disabled indexes.
	RefreshSkippedDisabled int64
	// AccessRuleUpdates counts per-item background access-rule rewrites.
	AccessRuleUpdates int64
}

var indexWorkStats IndexWorkStats

// SetIndexWorkProbeEnabled resets and toggles the files-layer work counters.
func SetIndexWorkProbeEnabled(enabled bool) {
	indexWorkStats = IndexWorkStats{}
	indexWorkProbeEnabled.Store(enabled)
}

// IndexWorkProbeSnapshot returns the counters gathered since the probe was reset.
func IndexWorkProbeSnapshot() IndexWorkStats {
	return IndexWorkStats{
		RefreshRequests:        atomic.LoadInt64(&indexWorkStats.RefreshRequests),
		RefreshSkippedDisabled: atomic.LoadInt64(&indexWorkStats.RefreshSkippedDisabled),
		AccessRuleUpdates:      atomic.LoadInt64(&indexWorkStats.AccessRuleUpdates),
	}
}

func indexWorkProbeRefresh() {
	if indexWorkProbeEnabled.Load() {
		atomic.AddInt64(&indexWorkStats.RefreshRequests, 1)
	}
}

func indexWorkProbeRefreshSkipped() {
	if indexWorkProbeEnabled.Load() {
		atomic.AddInt64(&indexWorkStats.RefreshSkippedDisabled, 1)
	}
}

func indexWorkProbeAccessRuleUpdate() {
	if indexWorkProbeEnabled.Load() {
		atomic.AddInt64(&indexWorkStats.AccessRuleUpdates, 1)
	}
}
