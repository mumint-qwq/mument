package sql

import (
	"context"
	"time"
)

// PreviewContentExact needs only B's persisted observation columns. F must work
// before D has ever migrated its independent completion/retry columns.
func (db *IndexDB) PreviewContentExact(ctx context.Context, source, path string) (PreviewWarmupCandidate, error) {
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	var c PreviewWarmupCandidate
	err := db.DB().QueryRowContext(ctx, `SELECT path,type,image_version,image_guard,image_mtime_ns FROM index_items WHERE source=? AND path=? AND `+warmupRaster, source, path).Scan(&c.Path, &c.Type, &c.Version, &c.Guard, &c.MTimeNS)
	return c, err
}
