package sql

import (
	"context"
	"time"
)

const warmupRaster = `is_dir = 0 AND has_preview = 1 AND type IN ('image/jpeg','image/png') AND image_guard > 0`
const warmupPending = warmupRaster + ` AND (warmup_guard != image_guard OR warmup_attempts >= 0)`

const warmupPageSize = 16

// PreparePreviewWarmup is called only when the worker is enabled, before HTTP
// starts. Interrupted, additive migrations can be resumed on the next boot.
func (db *IndexDB) PreparePreviewWarmup(ctx context.Context) error {
	rows, err := db.DB().QueryContext(ctx, `SELECT name FROM pragma_table_info('index_items') WHERE name LIKE 'warmup_%'`)
	if err != nil {
		return err
	}
	present := make(map[string]bool, 3)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, name := range []string{"warmup_guard", "warmup_attempts", "warmup_retry_at"} {
		if !present[name] {
			if _, err := db.DB().ExecContext(ctx, `ALTER TABLE index_items ADD COLUMN `+name+` INTEGER NOT NULL DEFAULT 0`); err != nil {
				return err
			}
		}
	}
	_, err = db.DB().ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_warmup_path
		ON index_items(source, path) WHERE `+warmupPending+`;
		CREATE INDEX IF NOT EXISTS idx_warmup_recent
		ON index_items(source, image_guard, path) WHERE `+warmupPending)
	return err
}

type PreviewWarmupCandidate struct {
	Path, Type, Version string
	Guard, MTimeNS      int64
	Attempts, RetryAt   int64
}

const warmupFields = `SELECT path, type, image_version, image_guard, image_mtime_ns,
	CASE WHEN warmup_guard = image_guard THEN warmup_attempts ELSE 0 END,
	CASE WHEN warmup_guard = image_guard THEN warmup_retry_at ELSE 0 END `

const warmupHistoryQuery = warmupFields + `FROM index_items INDEXED BY idx_warmup_path WHERE ` + warmupPending +
	` AND source = ? AND path >= ? AND path < ? AND path > ? ORDER BY path LIMIT 16`
const warmupRecentQuery = warmupFields + `FROM index_items INDEXED BY idx_warmup_recent WHERE ` + warmupPending +
	` AND source = ? ORDER BY image_guard DESC, path DESC LIMIT 16`

// Manual fill seeks ordinary index rows and filters the small page in Go so
// mixed directories cannot turn a single manual query into an unbounded scan.
const warmupManualQuery = `SELECT path, CASE WHEN ` + warmupRaster + ` THEN type ELSE '' END, image_version, image_guard, image_mtime_ns, 0, 0
	FROM index_items WHERE source = ? AND path >= ? AND path < ? AND path > ? ORDER BY path LIMIT 16`

func (db *IndexDB) PreviewWarmupPage(ctx context.Context, source, prefix, after string, manual, recent bool) ([]PreviewWarmupCandidate, error) {
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	// A directory prefix ends with '/', whose immediate successor is '0'.
	upper := prefix[:len(prefix)-1] + "0"
	query, args := warmupHistoryQuery, []any{source, prefix, upper, after}
	switch {
	case manual:
		query = warmupManualQuery
	case recent:
		query, args = warmupRecentQuery, []any{source}
	}
	rows, err := db.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := make([]PreviewWarmupCandidate, 0, warmupPageSize)
	for rows.Next() {
		var c PreviewWarmupCandidate
		if err := rows.Scan(&c.Path, &c.Type, &c.Version, &c.Guard, &c.MTimeNS, &c.Attempts, &c.RetryAt); err != nil {
			return nil, err
		}
		page = append(page, c)
	}
	return page, rows.Err()
}

// MarkPreviewWarmup writes only if the selected row version still exists.
func (db *IndexDB) MarkPreviewWarmup(ctx context.Context, source string, c PreviewWarmupCandidate, attempts, retryAt int64) error {
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	_, err := db.DB().ExecContext(ctx, `UPDATE index_items SET warmup_guard = ?, warmup_attempts = ?, warmup_retry_at = ?
		WHERE source = ? AND path = ? AND image_guard = ? AND image_version = ?`,
		c.Guard, attempts, retryAt, source, c.Path, c.Guard, c.Version)
	return err
}
