package sql

import (
	"sync/atomic"
	"time"
)

// SQLMetrics optionally records index database timing. It is disabled by
// default so production pays nothing; isolated measurements turn it on to
// separate lock waiting from actual SQL work.
type SQLMetrics struct {
	// LockWaitCount/LockWaitNs cover waiting for the process-wide TempDB mutex.
	LockWaitCount int64
	LockWaitNs    int64
	// QueryCount counts read statements that took the mutex.
	QueryCount int64
	// TxCount and TxHoldNs cover bulk-insert transactions, including the
	// database work and the mutex hold time.
	TxCount  int64
	TxHoldNs int64
	// TxRows is the number of index rows written by bulk transactions.
	TxRows int64
}

var metricsEnabled atomic.Bool
var sqlMetrics SQLMetrics

// SetMetricsEnabled resets and toggles the index database counters.
func SetMetricsEnabled(enabled bool) {
	sqlMetrics = SQLMetrics{}
	metricsEnabled.Store(enabled)
}

// MetricsSnapshot returns the counters gathered since the probe was reset.
func MetricsSnapshot() SQLMetrics {
	return SQLMetrics{
		LockWaitCount: atomic.LoadInt64(&sqlMetrics.LockWaitCount),
		LockWaitNs:    atomic.LoadInt64(&sqlMetrics.LockWaitNs),
		QueryCount:    atomic.LoadInt64(&sqlMetrics.QueryCount),
		TxCount:       atomic.LoadInt64(&sqlMetrics.TxCount),
		TxHoldNs:      atomic.LoadInt64(&sqlMetrics.TxHoldNs),
		TxRows:        atomic.LoadInt64(&sqlMetrics.TxRows),
	}
}

// MetricsSnapshot exposes the shared index database counters.
func (db *IndexDB) MetricsSnapshot() SQLMetrics {
	return MetricsSnapshot()
}

func recordLockWait(start time.Time) {
	if !metricsEnabled.Load() {
		return
	}
	atomic.AddInt64(&sqlMetrics.LockWaitCount, 1)
	atomic.AddInt64(&sqlMetrics.LockWaitNs, time.Since(start).Nanoseconds())
}

func recordQuery() {
	if metricsEnabled.Load() {
		atomic.AddInt64(&sqlMetrics.QueryCount, 1)
	}
}

func recordTx(holdStart time.Time, rows int) {
	if !metricsEnabled.Load() {
		return
	}
	atomic.AddInt64(&sqlMetrics.TxCount, 1)
	atomic.AddInt64(&sqlMetrics.TxHoldNs, time.Since(holdStart).Nanoseconds())
	atomic.AddInt64(&sqlMetrics.TxRows, int64(rows))
}
