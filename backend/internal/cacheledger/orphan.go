package cacheledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// The v1 C tables stay readable by D. Auxiliary rows are protective unless
// the publication and the last-reference event can both be verified.
const orphanSchema = `
CREATE INDEX IF NOT EXISTS results_storage_key ON results(storage_key,asset,variant);
CREATE INDEX IF NOT EXISTS orphan_move_source ON moves(asset) WHERE state IN ('pending','ambiguous');
CREATE INDEX IF NOT EXISTS orphan_move_target ON moves(target_asset) WHERE state IN ('pending','ambiguous');
CREATE TABLE IF NOT EXISTS orphan_events (
 id TEXT PRIMARY KEY, source TEXT NOT NULL, path TEXT NOT NULL, asset TEXT NOT NULL, version TEXT NOT NULL,
 proof TEXT NOT NULL, reason TEXT NOT NULL, observed INTEGER NOT NULL,
 state TEXT NOT NULL, confirmed INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS orphan_candidates (
 scan_id INTEGER PRIMARY KEY AUTOINCREMENT, asset TEXT NOT NULL UNIQUE, event TEXT NOT NULL REFERENCES orphan_events(id), since INTEGER NOT NULL, eligible_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS orphan_publications (
 asset TEXT NOT NULL, variant TEXT NOT NULL, epoch TEXT NOT NULL, generated INTEGER NOT NULL, stamp TEXT NOT NULL,
 PRIMARY KEY(asset,variant), FOREIGN KEY(asset,variant) REFERENCES results(asset,variant) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS orphan_audit (
 id TEXT PRIMARY KEY, object_id TEXT NOT NULL, reason TEXT NOT NULL, logical INTEGER NOT NULL, blocks INTEGER NOT NULL, finished INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS orphan_clock (id INTEGER PRIMARY KEY CHECK(id=1), last INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS orphan_unproven_refs (
 asset TEXT NOT NULL, source TEXT NOT NULL, path TEXT NOT NULL, version TEXT NOT NULL, PRIMARY KEY(asset,source,path,version));
CREATE TRIGGER IF NOT EXISTS orphan_observe_ref_delete BEFORE DELETE ON refs BEGIN
 INSERT OR IGNORE INTO orphan_unproven_refs VALUES(OLD.asset,OLD.source,OLD.path,OLD.version);
END;
CREATE TRIGGER IF NOT EXISTS orphan_observe_ref_replace BEFORE UPDATE OF asset ON refs
 WHEN OLD.asset<>NEW.asset BEGIN
 INSERT OR IGNORE INTO orphan_unproven_refs VALUES(OLD.asset,OLD.source,OLD.path,OLD.version);
END;
CREATE TRIGGER IF NOT EXISTS orphan_forget_replaced_publication
 AFTER UPDATE OF storage_key,size,digest,generated_at ON results BEGIN
 DELETE FROM orphan_publications WHERE asset=OLD.asset AND variant=OLD.variant;
END;
CREATE TRIGGER IF NOT EXISTS orphan_cancel_insert AFTER INSERT ON refs BEGIN
 DELETE FROM orphan_candidates WHERE asset=NEW.asset;
END;
CREATE TRIGGER IF NOT EXISTS orphan_cancel_update AFTER UPDATE OF asset ON refs BEGIN
 DELETE FROM orphan_candidates WHERE asset=NEW.asset;
END;
`

const orphanIntentTable = `(
 scan_id INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, storage_key TEXT NOT NULL, publications TEXT NOT NULL,
 expected TEXT NOT NULL, isolated TEXT NOT NULL DEFAULT '', logical INTEGER NOT NULL, blocks INTEGER NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('reserved','moving','isolated','removed','held')));`

func initOrphanIntents(tx *sql.Tx) error {
	var table, indexed bool
	err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='orphan_intents'),
 EXISTS(SELECT 1 FROM sqlite_schema WHERE type='index' AND name='orphan_intents_publication')`).Scan(&table, &indexed)
	if err != nil || indexed {
		return err
	}
	if table {
		// A legacy reservation could already have moved its object. Preserve
		// the record and isolation name and treat that phase as uncertain.
		_, err = tx.Exec(`CREATE TABLE orphan_intents_next ` + orphanIntentTable + `
 INSERT INTO orphan_intents_next SELECT scan_id,id,storage_key,publications,expected,isolated,logical,blocks,
 CASE state WHEN 'reserved' THEN 'moving' ELSE state END FROM orphan_intents;
 INSERT INTO sqlite_sequence(name,seq)
 SELECT 'orphan_intents_next',seq FROM sqlite_sequence WHERE name='orphan_intents'
 AND NOT EXISTS(SELECT 1 FROM sqlite_sequence WHERE name='orphan_intents_next');
 UPDATE sqlite_sequence SET seq=MAX(seq,COALESCE((SELECT seq FROM sqlite_sequence WHERE name='orphan_intents'),0))
 WHERE name='orphan_intents_next';
 DROP TABLE orphan_intents;
 ALTER TABLE orphan_intents_next RENAME TO orphan_intents;`)
	} else {
		_, err = tx.Exec("CREATE TABLE orphan_intents " + orphanIntentTable)
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec("CREATE UNIQUE INDEX orphan_intents_publication ON orphan_intents(storage_key,publications)")
	return err
}

type Invalidation struct {
	Reason, Proof string
	Ref           Ref
}

const exactRef = "source=? AND path=? AND asset=? AND version=?"

func proveRefLoss(ctx context.Context, tx *sql.Tx, source, path, asset, version string) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM orphan_unproven_refs WHERE "+exactRef, source, path, asset, version)
	return err
}

func (r *Repository) StageInvalidation(ctx context.Context, e Invalidation, now time.Time) (string, error) {
	if !e.Ref.Version.Strong || e.Proof == "" ||
		(e.Reason != "delete" && e.Reason != "directory_delete" && e.Reason != "overwrite" && e.Reason != "replace_target") {
		return "", ErrStale
	}
	id := newID()
	res, err := r.db.ExecContext(ctx, `INSERT INTO orphan_events(id,source,path,asset,version,proof,reason,observed,state)
 SELECT ?,source,path,asset,version,?,?,?,'pending' FROM refs WHERE `+exactRef,
		id, e.Proof, e.Reason, now.UnixNano(), e.Ref.Source, e.Ref.Path, e.Ref.Asset, versionKey(e.Ref.Version))
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if n != 1 {
		return "", errors.Join(ErrStale, err)
	}
	return id, err
}

func (r *Repository) AbortInvalidation(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, "UPDATE orphan_events SET state='unproven' WHERE id=? AND state='pending'", id)
	return err
}

// ConfirmInvalidation is called only with post-operation filesystem proof.
// A move ID is required when C has already atomically transferred the target reference.
func (r *Repository) ConfirmInvalidation(ctx context.Context, id, move string, now time.Time, grace time.Duration) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var source, path, asset, version, state string
	var observed int64
	if err = tx.QueryRowContext(ctx, "SELECT source,path,asset,version,state,observed FROM orphan_events WHERE id=?", id).
		Scan(&source, &path, &asset, &version, &state, &observed); err != nil {
		return err
	}
	if state != "pending" {
		return tx.Commit()
	}
	if now.UnixNano() < observed || grace <= 0 {
		return ErrStale
	}
	var moving bool
	if err := tx.QueryRowContext(ctx, "SELECT "+protectedMove, asset, asset).Scan(&moving); err != nil {
		return err
	}
	if moving {
		return ErrStale
	}
	if move == "" {
		res, err := tx.ExecContext(ctx, "DELETE FROM refs WHERE "+exactRef, source, path, asset, version)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStale
		}
	} else {
		var match bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM moves WHERE id=? AND state='done'
 AND target_source=? AND target_path=? AND target_asset=? AND target_version=? AND asset<>target_asset)`,
			move, source, path, asset, version).Scan(&match); err != nil {
			return err
		}
		if !match {
			return ErrStale
		}
	}
	if err = proveRefLoss(ctx, tx, source, path, asset, version); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE orphan_events SET state='confirmed',confirmed=? WHERE id=?", now.UnixNano(), id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO orphan_candidates(asset,event,since,eligible_at)
 SELECT ?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM refs WHERE asset=?)
 AND NOT EXISTS(SELECT 1 FROM orphan_unproven_refs WHERE asset=?)
 ON CONFLICT(asset) DO NOTHING`, asset, id, now.UnixNano(), now.Add(grace).UnixNano(), asset, asset)
	if err != nil {
		return err
	}
	return tx.Commit()
}

const protectedMove = `(EXISTS(SELECT 1 FROM moves WHERE asset=? AND state IN ('pending','ambiguous'))
 OR EXISTS(SELECT 1 FROM moves WHERE target_asset=? AND state IN ('pending','ambiguous')))`

type OrphanCandidate struct {
	Row   int64
	Asset string
}

type OrphanPublication struct {
	Asset, Variant, Key, Digest, Epoch, Stamp, Proof, Reason string
	Generated, Size, Since, EligibleAt, LastUsed             int64
	Referenced, Moving, Unproven                             bool
}

const OrphanPageSize = 16

func (r *Repository) OrphanCandidates(ctx context.Context, after int64) ([]OrphanCandidate, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT rowid,asset FROM orphan_candidates WHERE rowid>? ORDER BY rowid LIMIT ?", after, OrphanPageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrphanCandidate
	for rows.Next() {
		var p OrphanCandidate
		if err := rows.Scan(&p.Row, &p.Asset); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repository) OrphanKeys(ctx context.Context, asset string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT storage_key FROM results WHERE asset=? ORDER BY variant LIMIT ?", asset, OrphanPageSize+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (r *Repository) OrphanPublications(ctx context.Context, key string) ([]OrphanPublication, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT p.asset,p.variant,p.storage_key,p.digest,p.generated_at,p.size,p.last_used_at,
 COALESCE(q.epoch,''),COALESCE(q.stamp,''),COALESCE(c.since,0),COALESCE(c.eligible_at,0),COALESCE(e.proof,''),COALESCE(e.reason,''),
 EXISTS(SELECT 1 FROM refs f WHERE f.asset=p.asset),
 (EXISTS(SELECT 1 FROM moves WHERE asset=p.asset AND state IN ('pending','ambiguous'))
  OR EXISTS(SELECT 1 FROM moves WHERE target_asset=p.asset AND state IN ('pending','ambiguous'))),
 EXISTS(SELECT 1 FROM orphan_unproven_refs WHERE asset=p.asset)
 FROM results p LEFT JOIN orphan_publications q ON q.asset=p.asset AND q.variant=p.variant AND q.generated=p.generated_at
 LEFT JOIN orphan_candidates c ON c.asset=p.asset LEFT JOIN orphan_events e ON e.id=c.event AND e.state='confirmed'
 WHERE p.storage_key=? ORDER BY p.asset,p.variant LIMIT ?`, key, OrphanPageSize+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrphanPublication
	for rows.Next() {
		var p OrphanPublication
		if err := rows.Scan(&p.Asset, &p.Variant, &p.Key, &p.Digest, &p.Generated, &p.Size, &p.LastUsed, &p.Epoch, &p.Stamp,
			&p.Since, &p.EligibleAt, &p.Proof, &p.Reason, &p.Referenced, &p.Moving, &p.Unproven); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func OrphanPublicationSet(pubs []OrphanPublication) string {
	epochs := make([]string, 0, len(pubs))
	for _, p := range pubs {
		if p.Epoch == "" {
			return ""
		}
		epochs = append(epochs, p.Epoch)
	}
	if len(epochs) == 0 {
		return ""
	}
	raw, _ := json.Marshal(epochs)
	return Digest(raw)
}

type OrphanIntent struct {
	ID, Key, Publications, Expected, Isolated, State string
	Row, Logical, Blocks                             int64
}

func (r *Repository) OrphanIntent(ctx context.Context, key, publications string) (OrphanIntent, error) {
	return r.intent(ctx, "orphan_intents", key, publications)
}

func (r *Repository) intent(ctx context.Context, table, key, publications string) (OrphanIntent, error) {
	return readOrphanIntent(r.db.QueryRowContext(ctx, `SELECT rowid,id,storage_key,publications,expected,isolated,state,logical,blocks
 FROM `+table+` WHERE storage_key=? AND publications=?`, key, publications))
}

func readOrphanIntent(row interface{ Scan(...any) error }) (OrphanIntent, error) {
	var i OrphanIntent
	err := row.Scan(&i.Row, &i.ID, &i.Key, &i.Publications, &i.Expected, &i.Isolated, &i.State, &i.Logical, &i.Blocks)
	return i, err
}

func (r *Repository) OrphanIntents(ctx context.Context, after int64) ([]OrphanIntent, error) {
	return r.intents(ctx, "orphan_intents", after)
}

func (r *Repository) intents(ctx context.Context, table string, after int64) ([]OrphanIntent, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT rowid,id,storage_key,publications,expected,isolated,state,logical,blocks
 FROM `+table+` WHERE rowid>? ORDER BY rowid LIMIT ?`, after, OrphanPageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrphanIntent
	for rows.Next() {
		i, err := readOrphanIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// ReserveOrphan is called with the physical key and location gates held.
func (r *Repository) ReserveOrphan(ctx context.Context, i OrphanIntent) (OrphanIntent, error) {
	return r.reserveIntent(ctx, "orphan_intents", i)
}

func (r *Repository) reserveIntent(ctx context.Context, table string, i OrphanIntent) (OrphanIntent, error) {
	i.ID, i.State = newID(), "reserved"
	_, err := r.db.ExecContext(ctx, `INSERT INTO `+table+`(id,storage_key,publications,expected,logical,blocks,state)
 VALUES(?,?,?,?,?,?,'reserved')`, i.ID, i.Key, i.Publications, i.Expected, i.Logical, i.Blocks)
	return i, err
}

func (r *Repository) OrphanPhase(ctx context.Context, id, state, isolated string) error {
	return r.intentPhase(ctx, "orphan_intents", id, state, isolated)
}

func (r *Repository) intentPhase(ctx context.Context, table, id, state, isolated string) error {
	_, err := r.db.ExecContext(ctx, "UPDATE "+table+" SET state=?,isolated=? WHERE id=?", state, isolated, id)
	return err
}

// SettleOrphan makes removal of exact publication rows and accounting atomic.
// IDs are checked again so interrupted recovery cannot retire a newer result.
func (r *Repository) SettleOrphan(ctx context.Context, i OrphanIntent, pubs []OrphanPublication, reason string, logical, blocks int64, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range pubs {
		if _, err = removeExactPublication(ctx, tx, i.Key, p); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM orphan_candidates WHERE asset=? AND NOT EXISTS(SELECT 1 FROM results WHERE asset=?)", p.Asset, p.Asset); err != nil {
			return err
		}
	}
	if err := settleIntent(ctx, tx, "orphan_audit", "orphan_intents", i, reason, logical, blocks, now); err != nil {
		return err
	}
	return tx.Commit()
}

func settleIntent(ctx context.Context, tx *sql.Tx, audit, table string, i OrphanIntent, reason string, logical, blocks int64, now time.Time) error {
	if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO "+audit+" VALUES(?,?,?,?,?,?)", i.ID, Digest([]byte(i.Key)), reason, logical, blocks, now.UnixNano()); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE id=?", i.ID)
	return err
}

func (r *Repository) AdvanceOrphanClock(ctx context.Context, now time.Time) error {
	return r.advanceClock(ctx, "orphan_clock", now)
}

func (r *Repository) advanceClock(ctx context.Context, table string, now time.Time) error {
	res, err := r.db.ExecContext(ctx, `INSERT INTO `+table+` VALUES(1,?)
 ON CONFLICT(id) DO UPDATE SET last=excluded.last WHERE last<=excluded.last`, now.UnixNano())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if n != 1 {
		return errors.Join(ErrStale, err)
	}
	return err
}

type OrphanEventState struct {
	ID        string `json:"id"`
	Reason    string `json:"reason"`
	State     string `json:"state"`
	Observed  int64  `json:"observed_unix_ns"`
	Confirmed int64  `json:"confirmed_unix_ns,omitempty"`
}

func (r *Repository) OrphanEvents(ctx context.Context) ([]OrphanEventState, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id,reason,state,observed,confirmed FROM orphan_events ORDER BY rowid DESC LIMIT ?", OrphanPageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrphanEventState
	for rows.Next() {
		var e OrphanEventState
		if err := rows.Scan(&e.ID, &e.Reason, &e.State, &e.Observed, &e.Confirmed); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type OrphanAudit struct {
	ID       string `json:"id"`
	ObjectID string `json:"object_id"`
	Reason   string `json:"reason"`
	Logical  int64  `json:"unlinked_logical_bytes"`
	Blocks   int64  `json:"unlinked_allocated_bytes"`
	Finished int64  `json:"finished_unix_ns"`
}

func (r *Repository) OrphanAudit(ctx context.Context) ([]OrphanAudit, error) {
	return r.reclaimAudit(ctx, "orphan_audit")
}

func (r *Repository) reclaimAudit(ctx context.Context, table string) ([]OrphanAudit, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id,object_id,reason,logical,blocks,finished FROM "+table+" ORDER BY rowid DESC LIMIT ?", OrphanPageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrphanAudit
	for rows.Next() {
		var e OrphanAudit
		if err := rows.Scan(&e.ID, &e.ObjectID, &e.Reason, &e.Logical, &e.Blocks, &e.Finished); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
