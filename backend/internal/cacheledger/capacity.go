package cacheledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const capacitySchema = `
CREATE TABLE IF NOT EXISTS capacity_entries (
 scan_id INTEGER PRIMARY KEY AUTOINCREMENT, asset TEXT NOT NULL, variant TEXT NOT NULL, epoch TEXT NOT NULL,
 storage_key TEXT NOT NULL, generated INTEGER NOT NULL, used INTEGER NOT NULL, proof TEXT NOT NULL,
 UNIQUE(asset,variant), FOREIGN KEY(asset,variant) REFERENCES results(asset,variant) ON DELETE CASCADE);
CREATE INDEX IF NOT EXISTS capacity_unused ON capacity_entries(generated,scan_id) WHERE used=0;
CREATE INDEX IF NOT EXISTS capacity_used ON capacity_entries(used,scan_id) WHERE used>0;
CREATE TRIGGER IF NOT EXISTS capacity_forget_replaced AFTER UPDATE OF storage_key,size,digest,generated_at ON results BEGIN
 DELETE FROM capacity_entries WHERE asset=OLD.asset AND variant=OLD.variant;
END;
CREATE TRIGGER IF NOT EXISTS capacity_record_use AFTER UPDATE OF last_used_at ON results BEGIN
 UPDATE capacity_entries SET used=NEW.last_used_at WHERE asset=NEW.asset AND variant=NEW.variant;
END;
CREATE TABLE IF NOT EXISTS capacity_intents ` + orphanIntentTable + `
CREATE UNIQUE INDEX IF NOT EXISTS capacity_intents_publication ON capacity_intents(storage_key,publications);
CREATE TABLE IF NOT EXISTS capacity_audit (
 id TEXT PRIMARY KEY, object_id TEXT NOT NULL, reason TEXT NOT NULL, logical INTEGER NOT NULL, blocks INTEGER NOT NULL, finished INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS capacity_clock (id INTEGER PRIMARY KEY CHECK(id=1), last INTEGER NOT NULL);
`

const CapacityNewProtection = time.Hour
const CapacityUseProtection = 7 * 24 * time.Hour

type CapacityCursor struct {
	Time int64 `json:"time"`
	Row  int64 `json:"row"`
	Used bool  `json:"used"`
}

type CapacityCandidate struct {
	Key    string
	Cursor CapacityCursor
}

func (r *Repository) CapacityCandidates(ctx context.Context, after CapacityCursor, now time.Time) ([]CapacityCandidate, error) {
	query := `SELECT storage_key,generated,scan_id FROM capacity_entries
 WHERE used=0 AND (generated,scan_id)>(?,?) AND generated<=? ORDER BY generated,scan_id LIMIT ?`
	cutoff := now.Add(-CapacityNewProtection)
	if after.Used {
		query = `SELECT storage_key,used,scan_id FROM capacity_entries
 WHERE used>0 AND (used,scan_id)>(?,?) AND used<=? ORDER BY used,scan_id LIMIT ?`
		cutoff = now.Add(-CapacityUseProtection)
	}
	rows, err := r.db.QueryContext(ctx, query, after.Time, after.Row, cutoff.UnixNano(), OrphanPageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var page []CapacityCandidate
	for rows.Next() {
		c := CapacityCandidate{Cursor: CapacityCursor{Used: after.Used}}
		if err := rows.Scan(&c.Key, &c.Cursor.Time, &c.Cursor.Row); err != nil {
			return nil, err
		}
		page = append(page, c)
	}
	return page, rows.Err()
}

// CapacitySource accepts precisely one reference. Multiple ranges, old
// publications and a path transferred since its last proof remain protected.
func (r *Repository) CapacitySource(ctx context.Context, p OrphanPublication) (string, Ref, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT c.proof,f.source,f.path,f.version FROM capacity_entries c
 JOIN refs f ON f.asset=c.asset WHERE c.asset=? AND c.variant=? AND c.epoch=? LIMIT 2`, p.Asset, p.Variant, p.Epoch)
	if err != nil {
		return "", Ref{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", Ref{}, errors.Join(ErrStale, rows.Err())
	}
	ref := Ref{Asset: p.Asset}
	var proof, version string
	if err := rows.Scan(&proof, &ref.Source, &ref.Path, &version); err != nil {
		return "", Ref{}, err
	}
	if rows.Next() || json.Unmarshal([]byte(version), &ref.Version) != nil || !ref.Version.Strong {
		return "", Ref{}, ErrStale
	}
	return proof, ref, rows.Err()
}

func (r *Repository) CapacityIntent(ctx context.Context, key, set string) (OrphanIntent, error) {
	return r.intent(ctx, "capacity_intents", key, set)
}

func (r *Repository) CapacityIntents(ctx context.Context, after int64) ([]OrphanIntent, error) {
	return r.intents(ctx, "capacity_intents", after)
}

func (r *Repository) ReserveCapacity(ctx context.Context, i OrphanIntent) (OrphanIntent, error) {
	return r.reserveIntent(ctx, "capacity_intents", i)
}

func (r *Repository) CapacityPhase(ctx context.Context, id, state, stamp string) error {
	return r.intentPhase(ctx, "capacity_intents", id, state, stamp)
}

func (r *Repository) SettleCapacity(ctx context.Context, i OrphanIntent, pubs []OrphanPublication, reason string, logical, blocks int64, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range pubs {
		if _, err := removeExactPublication(ctx, tx, i.Key, p); err != nil {
			return err
		}
	}
	if err := settleIntent(ctx, tx, "capacity_audit", "capacity_intents", i, reason, logical, blocks, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) AdvanceCapacityClock(ctx context.Context, now time.Time) error {
	return r.advanceClock(ctx, "capacity_clock", now)
}

func (r *Repository) CapacityAudit(ctx context.Context) ([]OrphanAudit, error) {
	return r.reclaimAudit(ctx, "capacity_audit")
}

func removeExactPublication(ctx context.Context, tx *sql.Tx, key string, p OrphanPublication) (sql.Result, error) {
	return tx.ExecContext(ctx, `DELETE FROM results WHERE asset=? AND variant=? AND storage_key=? AND generated_at=?
 AND EXISTS(SELECT 1 FROM orphan_publications q WHERE q.asset=results.asset AND q.variant=results.variant AND q.epoch=?)`,
		p.Asset, p.Variant, key, p.Generated, p.Epoch)
}
