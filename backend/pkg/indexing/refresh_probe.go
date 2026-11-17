package indexing

import (
	"sync"
	"sync/atomic"
)

// RefreshProbe collects aggregate counters for directory refresh execution.
//
// It exists for isolated measurements and tests: it is disabled by default, so
// the only cost on the normal path is one atomic load per directory refresh.
// It never stores or logs real paths; distinct directories are counted by an
// internal key that is dropped with the probe.
var refreshProbeEnabled atomic.Bool

// RefreshProbeStats is a point-in-time snapshot of directory refresh activity.
type RefreshProbeStats struct {
	// ScanStarted counts top-level RefreshDirectory scans that reached the
	// filesystem read. Skipped/disabled refreshes are counted separately.
	ScanStarted   int64
	ScanCompleted int64
	ScanFailed    int64
	ScanSkipped   int64
	Recursive     int64
	// DistinctDirs is the number of different (source, directory) keys that
	// started a scan since the probe was reset.
	DistinctDirs int
	// Active is the number of scans running right now, PeakActive its maximum.
	Active int
	// PeakActive is the high-water mark of Active.
	PeakActive int
	// Overlaps counts scans that started while another scan of the same
	// (source, directory) was already running.
	Overlaps int64
	// DeleteCalls counts explicit index deletions (moved-away rows).
	DeleteCalls int64
	// DeleteRecursive counts deletions that also remove a subtree.
	DeleteRecursive int64
}

type refreshProbeState struct {
	mu           sync.Mutex
	stats        RefreshProbeStats
	activeByDir  map[string]int
	distinctDirs map[string]struct{}
}

var refreshProbe refreshProbeState

// SetRefreshProbeEnabled turns aggregate refresh counters on or off. Disabling
// it also drops the accumulated state.
func SetRefreshProbeEnabled(enabled bool) {
	if enabled {
		refreshProbe.mu.Lock()
		refreshProbe.stats = RefreshProbeStats{}
		refreshProbe.activeByDir = make(map[string]int)
		refreshProbe.distinctDirs = make(map[string]struct{})
		refreshProbe.mu.Unlock()
		refreshProbeEnabled.Store(true)
		return
	}
	refreshProbeEnabled.Store(false)
	refreshProbe.mu.Lock()
	refreshProbe.stats = RefreshProbeStats{}
	refreshProbe.activeByDir = nil
	refreshProbe.distinctDirs = nil
	refreshProbe.mu.Unlock()
}

// RefreshProbeSnapshot returns the counters gathered since the probe was enabled.
func RefreshProbeSnapshot() RefreshProbeStats {
	refreshProbe.mu.Lock()
	defer refreshProbe.mu.Unlock()
	stats := refreshProbe.stats
	stats.DistinctDirs = len(refreshProbe.distinctDirs)
	stats.Active = refreshProbe.stats.Active
	return stats
}

func refreshProbeBegin(source, indexPath string, recursive bool) func(completed bool) {
	if !refreshProbeEnabled.Load() {
		return func(bool) {}
	}
	key := source + "|" + indexPath
	refreshProbe.mu.Lock()
	refreshProbe.stats.ScanStarted++
	if recursive {
		refreshProbe.stats.Recursive++
	}
	if refreshProbe.activeByDir == nil {
		refreshProbe.activeByDir = make(map[string]int)
	}
	if refreshProbe.distinctDirs == nil {
		refreshProbe.distinctDirs = make(map[string]struct{})
	}
	refreshProbe.distinctDirs[key] = struct{}{}
	if refreshProbe.activeByDir[key] > 0 {
		refreshProbe.stats.Overlaps++
	}
	refreshProbe.activeByDir[key]++
	refreshProbe.stats.Active++
	if refreshProbe.stats.Active > refreshProbe.stats.PeakActive {
		refreshProbe.stats.PeakActive = refreshProbe.stats.Active
	}
	refreshProbe.mu.Unlock()

	return func(completed bool) {
		refreshProbe.mu.Lock()
		defer refreshProbe.mu.Unlock()
		if refreshProbe.activeByDir != nil {
			if refreshProbe.activeByDir[key] <= 1 {
				delete(refreshProbe.activeByDir, key)
			} else {
				refreshProbe.activeByDir[key]--
			}
		}
		if refreshProbe.stats.Active > 0 {
			refreshProbe.stats.Active--
		}
		if completed {
			refreshProbe.stats.ScanCompleted++
		} else {
			refreshProbe.stats.ScanFailed++
		}
	}
}

func refreshProbeDelete(recursive bool) {
	if !refreshProbeEnabled.Load() {
		return
	}
	refreshProbe.mu.Lock()
	refreshProbe.stats.DeleteCalls++
	if recursive {
		refreshProbe.stats.DeleteRecursive++
	}
	refreshProbe.mu.Unlock()
}

func refreshProbeSkip() {
	if !refreshProbeEnabled.Load() {
		return
	}
	refreshProbe.mu.Lock()
	refreshProbe.stats.ScanSkipped++
	refreshProbe.mu.Unlock()
}
