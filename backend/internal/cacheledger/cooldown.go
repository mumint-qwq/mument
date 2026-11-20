package cacheledger

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const capacityCooldownSchema = `
CREATE TABLE IF NOT EXISTS capacity_cooldown (
 scan_id INTEGER PRIMARY KEY AUTOINCREMENT, asset TEXT NOT NULL, variant TEXT NOT NULL, epoch TEXT NOT NULL,
 source TEXT NOT NULL, proof TEXT NOT NULL, since INTEGER NOT NULL, eligible INTEGER NOT NULL, reason TEXT NOT NULL,
 UNIQUE(asset,variant));
CREATE INDEX IF NOT EXISTS capacity_cooldown_due ON capacity_cooldown(source,eligible,scan_id);
`

const CapacityCooldown = 24 * time.Hour

type CooldownCursor struct{ Eligible, Row int64 }

type CooldownEntry struct {
	Asset, Variant, Epoch, Proof string
	Cursor                       CooldownCursor
}

func (r *Repository) CoolingUntil(ctx context.Context, asset, variant string) (time.Time, error) {
	var eligible int64
	err := r.db.QueryRowContext(ctx, "SELECT eligible FROM capacity_cooldown WHERE asset=? AND variant=?", asset, variant).Scan(&eligible)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, eligible), nil
}

func (r *Repository) CooldownDue(ctx context.Context, source string, after CooldownCursor, now time.Time) ([]CooldownEntry, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT asset,variant,epoch,proof,eligible,scan_id FROM capacity_cooldown
 WHERE source=? AND (eligible,scan_id)>(?,?) AND eligible<=? ORDER BY eligible,scan_id LIMIT ?`,
		source, after.Eligible, after.Row, now.UnixNano(), OrphanPageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var page []CooldownEntry
	for rows.Next() {
		var e CooldownEntry
		if err := rows.Scan(&e.Asset, &e.Variant, &e.Epoch, &e.Proof, &e.Cursor.Eligible, &e.Cursor.Row); err != nil {
			return nil, err
		}
		page = append(page, e)
	}
	return page, rows.Err()
}

func (r *Repository) CapacityRecoveryPending(ctx context.Context, key string) (bool, error) {
	var pending bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM capacity_intents WHERE storage_key=?
 AND state IN ('moving','isolated','restoring','removed','held'))`, key).Scan(&pending)
	return pending, err
}
