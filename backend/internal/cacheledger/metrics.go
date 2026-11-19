// Added for mument 007-C.
package cacheledger

import (
	"sync/atomic"
	"time"
)

type moveCounters struct {
	prepares, completions atomic.Uint64
	intents, resolutions  atomic.Uint64
	sqlNanos, renameNanos atomic.Uint64
}

type MoveStats struct {
	PrepareTransactions    uint64
	CompletionTransactions uint64
	IntentRows             uint64
	OutcomeRows            uint64
	SQLNanos               uint64
	RenameNanos            uint64
}

func (r *Repository) MoveStats() MoveStats {
	return MoveStats{
		PrepareTransactions:    r.metrics.prepares.Load(),
		CompletionTransactions: r.metrics.completions.Load(),
		IntentRows:             r.metrics.intents.Load(),
		OutcomeRows:            r.metrics.resolutions.Load(),
		SQLNanos:               r.metrics.sqlNanos.Load(),
		RenameNanos:            r.metrics.renameNanos.Load(),
	}
}

func (r *Repository) RecordRename(elapsed time.Duration) {
	r.metrics.renameNanos.Add(uint64(elapsed))
}
