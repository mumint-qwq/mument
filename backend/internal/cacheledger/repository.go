// Added for mument 007-C. This repository performs no filesystem moves.
package cacheledger

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	dbsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
)

var ErrStale = errors.New("cache ledger observation is stale")

// Repository owns a dedicated durable DB, independent of the rebuildable index.
// The caller must drain its users before Close. There are no internal goroutines.
type Repository struct {
	db            *sql.DB
	locations     chan struct{}
	retryRecovery chan struct{}
	metrics       moveCounters
}

const schema = `
CREATE TABLE IF NOT EXISTS ledger_schema (version INTEGER PRIMARY KEY CHECK(version=1));
INSERT OR IGNORE INTO ledger_schema VALUES(1);
CREATE TABLE IF NOT EXISTS assets (id TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS refs (
 source TEXT NOT NULL, path TEXT NOT NULL, version TEXT NOT NULL,
 asset TEXT NOT NULL REFERENCES assets(id),
 PRIMARY KEY(source,path));
CREATE INDEX IF NOT EXISTS refs_asset ON refs(asset);
CREATE TABLE IF NOT EXISTS results (
 asset TEXT NOT NULL REFERENCES assets(id), variant TEXT NOT NULL,
 storage_key TEXT NOT NULL, size INTEGER NOT NULL CHECK(size>0), digest TEXT NOT NULL,
 generated_at INTEGER NOT NULL, last_used_at INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(asset,variant));
CREATE TABLE IF NOT EXISTS moves (
 id TEXT PRIMARY KEY, source TEXT NOT NULL, path TEXT NOT NULL,
 target_source TEXT NOT NULL, target_path TEXT NOT NULL,
 version TEXT NOT NULL, asset TEXT NOT NULL, target_asset TEXT NOT NULL, target_version TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','done','ambiguous')),
 created_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS moves_pending ON moves(state,id);
`

func Open(path string) (*Repository, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		err = file.Close()
	}
	if err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("cache ledger path is not a regular file")
	}
	db, err := sql.Open(dbsql.SqliteDriver, path)
	if err != nil {
		return nil, err
	}
	// Connection-scoped pragmas stay effective on the sole connection.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	r := &Repository{db: db, locations: make(chan struct{}, 1), retryRecovery: make(chan struct{}, 1)}
	for _, statement := range []string{"PRAGMA busy_timeout=1000", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON"} {
		if _, err = db.Exec(statement); err != nil {
			return nil, errors.Join(err, db.Close())
		}
	}
	var journal string
	if err = db.QueryRow("PRAGMA journal_mode=WAL").Scan(&journal); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if journal != "wal" {
		return nil, errors.Join(fmt.Errorf("cache ledger requires WAL journaling"), db.Close())
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if _, err = tx.Exec(schema + orphanSchema + capacitySchema + capacityCooldownSchema); err != nil {
		return nil, errors.Join(err, tx.Rollback(), db.Close())
	}
	if err = initOrphanIntents(tx); err != nil {
		return nil, errors.Join(err, tx.Rollback(), db.Close())
	}
	var count, version int
	if err = tx.QueryRow("SELECT COUNT(*),MAX(version) FROM ledger_schema").Scan(&count, &version); err != nil {
		return nil, errors.Join(err, tx.Rollback(), db.Close())
	}
	if count != 1 || version != 1 {
		return nil, errors.Join(fmt.Errorf("unsupported cache ledger schema"), tx.Rollback(), db.Close())
	}
	if err = tx.Commit(); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return r, nil
}

func (r *Repository) Close() error { return r.db.Close() }

// LockLocations protects filesystem rename and location resolution as a unit.
// Callers never hold it while decoding or copying.
func (r *Repository) LockLocations(ctx context.Context) (func(), error) {
	select {
	case r.locations <- struct{}{}:
		return func() { <-r.locations }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// RequestRecovery coalesces runtime failures into one bounded wakeup.
func (r *Repository) RequestRecovery() {
	select {
	case r.retryRecovery <- struct{}{}:
	default:
	}
}

func (r *Repository) RecoveryRequested() <-chan struct{} { return r.retryRecovery }

func (r *Repository) RecoveryCutoff(ctx context.Context) (int64, error) {
	var cutoff int64
	err := r.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(rowid),0) FROM moves").Scan(&cutoff)
	return cutoff, err
}

type Ref struct {
	Source, Path, Asset string
	Version             Version
}
type Result struct {
	Key, Digest                   string
	Size, GeneratedAt, LastUsedAt int64
}

func versionKey(v Version) string { b, _ := json.Marshal(v); return string(b) }
func newID() string               { return rand.Text() }

func validPath(source, path string) bool {
	return source != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

// FindRef looks for an existing strong observation without allocating an asset.
func (r *Repository) FindRef(ctx context.Context, source, path string, v Version) (Ref, error) {
	if !v.Strong || !validPath(source, path) {
		return Ref{}, nil
	}
	var asset string
	err := r.db.QueryRowContext(ctx, "SELECT asset FROM refs WHERE source=? AND path=? AND version=?",
		source, path, versionKey(v)).Scan(&asset)
	if errors.Is(err, sql.ErrNoRows) {
		return Ref{}, nil
	}
	return Ref{source, path, asset, v}, err
}

// Resolve assigns a new random content generation on every version change.
// A returned Ref is a publication token; all later writes compare that token.
func (r *Repository) Resolve(ctx context.Context, source, path string, v Version) (Ref, error) {
	if !validPath(source, path) {
		return Ref{}, fmt.Errorf("invalid ledger reference")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Ref{}, err
	}
	defer tx.Rollback()
	var asset, observed string
	err = tx.QueryRowContext(ctx, "SELECT asset,version FROM refs WHERE source=? AND path=?", source, path).Scan(&asset, &observed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Ref{}, err
	}
	if err != nil || observed != versionKey(v) || !v.Strong {
		asset = newID()
		if _, err = tx.ExecContext(ctx, "INSERT INTO assets(id) VALUES(?)", asset); err != nil {
			return Ref{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO refs VALUES(?,?,?,?) ON CONFLICT(source,path) DO UPDATE SET version=excluded.version,asset=excluded.asset`, source, path, versionKey(v), asset); err != nil {
			return Ref{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Ref{}, err
	}
	return Ref{source, path, asset, v}, nil
}

// Lookup never trusts a result attached to a replaced reference. The caller
// verifies current stat and physical bytes separately before serving a hit.
func (r *Repository) Lookup(ctx context.Context, ref Ref, variant string) (Result, bool, error) {
	var result Result
	err := r.db.QueryRowContext(ctx, `SELECT p.storage_key,p.size,p.digest,p.generated_at,p.last_used_at
 FROM results p JOIN refs f ON f.asset=p.asset
 WHERE f.source=? AND f.path=? AND f.asset=? AND f.version=? AND p.variant=?`,
		ref.Source, ref.Path, ref.Asset, versionKey(ref.Version), variant).Scan(&result.Key, &result.Size, &result.Digest, &result.GeneratedAt, &result.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, false, nil
	}
	return result, err == nil, err
}

func Digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (p Result) Matches(data []byte) bool {
	return int64(len(data)) == p.Size && Digest(data) == p.Digest
}

// Publish records generated bytes after successful atomic storage. The returned
// result identifies this publication. Usage is recorded by the consumer.
func (r *Repository) Publish(ctx context.Context, ref Ref, variant, key string, data []byte) (Result, error) {
	return r.PublishObserved(ctx, ref, variant, key, data, "")
}

// PublishObserved binds a coordinated physical cache write to a new epoch.
func (r *Repository) PublishObserved(ctx context.Context, ref Ref, variant, key string, data []byte, stamp string) (Result, error) {
	return r.PublishScoped(ctx, ref, variant, key, data, stamp, "")
}

func (r *Repository) PublishScoped(ctx context.Context, ref Ref, variant, key string, data []byte, stamp, proof string) (Result, error) {
	if variant == "" || key == "" || len(data) == 0 {
		return Result{}, fmt.Errorf("invalid ledger publication")
	}
	published := Result{Key: key, Size: int64(len(data)), Digest: Digest(data), GeneratedAt: time.Now().UnixNano()}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `INSERT INTO results(asset,variant,storage_key,size,digest,generated_at)
 SELECT asset,?,?,?,?,? FROM refs WHERE source=? AND path=? AND asset=? AND version=?
 ON CONFLICT(asset,variant) DO UPDATE SET storage_key=excluded.storage_key,size=excluded.size,digest=excluded.digest,generated_at=excluded.generated_at
 RETURNING last_used_at`,
		variant, published.Key, published.Size, published.Digest, published.GeneratedAt, ref.Source, ref.Path, ref.Asset, versionKey(ref.Version)).Scan(&published.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, ErrStale
	}
	if err != nil {
		return Result{}, err
	}
	if stamp != "" {
		epoch := newID()
		if _, err := tx.ExecContext(ctx, "INSERT INTO orphan_publications VALUES(?,?,?,?,?)", ref.Asset, variant, epoch, published.GeneratedAt, stamp); err != nil {
			return Result{}, err
		}
		// A committed replacement under the physical key gate ends the
		// cooldown for this exact asset and variant, even with no source proof.
		if _, err := tx.ExecContext(ctx, "DELETE FROM capacity_cooldown WHERE asset=? AND variant=?", ref.Asset, variant); err != nil {
			return Result{}, err
		}
		if proof != "" {
			_, err := tx.ExecContext(ctx, `INSERT INTO capacity_entries(asset,variant,epoch,storage_key,generated,used,proof)
 VALUES(?,?,?,?,?,?,?)`, ref.Asset, variant, epoch, key, published.GeneratedAt, published.LastUsedAt, proof)
			if err != nil {
				return Result{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Result{}, err
	}
	return published, nil
}

// Reject removes only the ready record that the reader observed as corrupt.
// The physical key may already contain a concurrent replacement.
func (r *Repository) Reject(ctx context.Context, ref Ref, variant string, result Result) (bool, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM results WHERE asset=? AND variant=? AND storage_key=? AND digest=? AND generated_at=?
 AND EXISTS(SELECT 1 FROM refs WHERE source=? AND path=? AND asset=? AND version=?)`,
		ref.Asset, variant, result.Key, result.Digest, result.GeneratedAt, ref.Source, ref.Path, ref.Asset, versionKey(ref.Version))
	if err != nil {
		return false, err
	}
	count, err := res.RowsAffected()
	return count == 1, err
}

// Touch records foreground use of the observed publication and coalesces
// updates to one mutation per minute, including when waiters race each other.
func (r *Repository) Touch(ctx context.Context, ref Ref, variant string, result Result, now time.Time) error {
	cutoff := now.Add(-time.Minute).UnixNano()
	if result.LastUsedAt >= cutoff {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `UPDATE results SET last_used_at=? WHERE asset=? AND variant=? AND last_used_at<?
 AND storage_key=? AND digest=? AND generated_at=?
 AND EXISTS(SELECT 1 FROM refs WHERE source=? AND path=? AND asset=? AND version=?)`,
		now.UnixNano(), ref.Asset, variant, cutoff, result.Key, result.Digest, result.GeneratedAt,
		ref.Source, ref.Path, ref.Asset, versionKey(ref.Version))
	return err
}

// Detach invalidates a location. Shared physical results are never deleted.
func (r *Repository) Detach(ctx context.Context, ref Ref) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM refs WHERE source=? AND path=? AND asset=? AND version=?", ref.Source, ref.Path, ref.Asset, versionKey(ref.Version))
	return err
}

// Existing returns a reference only if a preview already observed the exact
// source version. Preparing a move never creates an asset for an uncached file.
func (r *Repository) Existing(ctx context.Context, source, path string, v Version) (Ref, bool, error) {
	if !v.Strong {
		return Ref{}, false, nil
	}
	ref := Ref{Source: source, Path: path, Version: v}
	err := r.db.QueryRowContext(ctx, "SELECT asset FROM refs WHERE source=? AND path=? AND version=?", source, path, versionKey(v)).Scan(&ref.Asset)
	if errors.Is(err, sql.ErrNoRows) {
		return Ref{}, false, nil
	}
	return ref, err == nil, err
}

type Move struct {
	ID                       string
	Ref                      Ref
	TargetSource, TargetPath string
}

// Prepare persists at most 64 already-authorized moves in one durable commit.
// Callers must split larger requests. No transaction spans filesystem work.
func (r *Repository) Prepare(ctx context.Context, moves []Move) ([]Move, error) {
	started := time.Now()
	if len(moves) > 64 {
		return nil, fmt.Errorf("ledger move batch exceeds 64")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	out := append([]Move(nil), moves...)
	for i := range out {
		m := &out[i]
		if !validPath(m.TargetSource, m.TargetPath) || !validPath(m.Ref.Source, m.Ref.Path) || !m.Ref.Version.Strong || (m.Ref.Source == m.TargetSource && m.Ref.Path == m.TargetPath) {
			return nil, fmt.Errorf("invalid ledger move")
		}
		m.ID = newID()
		var asset, observed string
		err = tx.QueryRowContext(ctx, "SELECT asset,version FROM refs WHERE source=? AND path=?", m.Ref.Source, m.Ref.Path).Scan(&asset, &observed)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrStale
		}
		if err != nil {
			return nil, err
		}
		if asset != m.Ref.Asset || observed != versionKey(m.Ref.Version) {
			return nil, ErrStale
		}
		var target, targetVersion string
		err = tx.QueryRowContext(ctx, "SELECT asset,version FROM refs WHERE source=? AND path=?", m.TargetSource, m.TargetPath).Scan(&target, &targetVersion)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO moves VALUES(?,?,?,?,?,?,?,?,?, 'pending',?)", m.ID, m.Ref.Source, m.Ref.Path, m.TargetSource, m.TargetPath, observed, asset, target, targetVersion, time.Now().UnixNano())
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	r.metrics.prepares.Add(1)
	r.metrics.intents.Add(uint64(len(out)))
	r.metrics.sqlNanos.Add(uint64(time.Since(started)))
	return out, nil
}

type RenameOutcome struct {
	ID    string
	After Version // Zero for failed rename, copy fallback or ambiguous observations.
}

func (r *Repository) CommitRename(ctx context.Context, id string, after Version) error {
	return r.completeRenames(ctx, []RenameOutcome{{ID: id, After: after}}, true)
}

// CompleteRenames commits a bounded batch. An ambiguous item does not replace
// either reference. Storage errors roll back the batch and leave its intents.
func (r *Repository) CompleteRenames(ctx context.Context, outcomes []RenameOutcome) error {
	return r.completeRenames(ctx, outcomes, false)
}

func (r *Repository) completeRenames(ctx context.Context, outcomes []RenameOutcome, strict bool) error {
	started := time.Now()
	if len(outcomes) == 0 {
		return nil
	}
	if len(outcomes) > 64 {
		return fmt.Errorf("ledger outcome batch exceeds 64")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, outcome := range outcomes {
		err = commitRename(ctx, tx, outcome.ID, outcome.After)
		if errors.Is(err, ErrStale) && !strict {
			_, err = tx.ExecContext(ctx, "UPDATE moves SET state='ambiguous' WHERE id=? AND state='pending'", outcome.ID)
		}
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	r.metrics.completions.Add(1)
	r.metrics.resolutions.Add(uint64(len(outcomes)))
	r.metrics.sqlNanos.Add(uint64(time.Since(started)))
	return nil
}

// commitRename is called only with live application rename evidence.
func commitRename(ctx context.Context, tx *sql.Tx, id string, after Version) error {
	var source, path, targetSource, targetPath, version, asset, targetAsset, targetVersion, state string
	err := tx.QueryRowContext(ctx, "SELECT source,path,target_source,target_path,version,asset,target_asset,target_version,state FROM moves WHERE id=?", id).Scan(&source, &path, &targetSource, &targetPath, &version, &asset, &targetAsset, &targetVersion, &state)
	if err != nil {
		return err
	}
	if state == "done" {
		return nil
	}
	if state != "pending" {
		return ErrStale
	}
	var before Version
	if err = json.Unmarshal([]byte(version), &before); err != nil {
		return err
	}
	if !before.RenameCompatible(after) {
		return ErrStale
	}
	var current, observed string
	err = tx.QueryRowContext(ctx, "SELECT asset,version FROM refs WHERE source=? AND path=?", source, path).Scan(&current, &observed)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStale
	}
	if err != nil {
		return err
	}
	if current != asset || observed != version {
		return ErrStale
	}
	var target, currentTargetVersion string
	err = tx.QueryRowContext(ctx, "SELECT asset,version FROM refs WHERE source=? AND path=?", targetSource, targetPath).Scan(&target, &currentTargetVersion)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if target != targetAsset || currentTargetVersion != targetVersion {
		return ErrStale
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM refs WHERE source=? AND path=?", source, path); err != nil {
		return err
	}
	if err = proveRefLoss(ctx, tx, source, path, asset, version); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO refs VALUES(?,?,?,?) ON CONFLICT(source,path) DO UPDATE SET version=excluded.version,asset=excluded.asset`, targetSource, targetPath, versionKey(after), asset); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE moves SET state='done' WHERE id=?", id); err != nil {
		return err
	}
	return nil
}

// RecoverPage marks a bounded page ambiguous. It changes no real files and
// never revives references. Matching existing refs are detached so a later
// restored mtime cannot make an interrupted intent authoritative. Completed
// moves remain untouched. Call until zero, yielding between pages.
func (r *Repository) RecoverPage(ctx context.Context, limit int) (int, error) {
	cutoff, err := r.RecoveryCutoff(ctx)
	if err != nil {
		return 0, err
	}
	return r.RecoverBefore(ctx, cutoff, limit)
}

func (r *Repository) RecoverBefore(ctx context.Context, cutoff int64, limit int) (int, error) {
	if limit < 1 || limit > 64 {
		return 0, fmt.Errorf("invalid recovery page size")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,source,path,asset,version,target_source,target_path,target_asset,target_version
 FROM moves WHERE state='pending' AND rowid<=? ORDER BY id LIMIT ?`, cutoff, limit)
	if err != nil {
		return 0, err
	}
	type recoveryRow struct{ id, source, path, asset, version, targetSource, targetPath, targetAsset, targetVersion string }
	intents := make([]recoveryRow, 0, limit)
	for rows.Next() {
		var m recoveryRow
		if err = rows.Scan(&m.id, &m.source, &m.path, &m.asset, &m.version, &m.targetSource, &m.targetPath, &m.targetAsset, &m.targetVersion); err != nil {
			rows.Close()
			return 0, err
		}
		intents = append(intents, m)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	for _, m := range intents {
		const detach = "DELETE FROM refs WHERE source=? AND path=? AND asset=? AND version=?"
		if _, err = tx.ExecContext(ctx, detach, m.source, m.path, m.asset, m.version); err != nil {
			return 0, err
		}
		if m.targetAsset != "" {
			if _, err = tx.ExecContext(ctx, detach, m.targetSource, m.targetPath, m.targetAsset, m.targetVersion); err != nil {
				return 0, err
			}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE moves SET state='ambiguous' WHERE id=?", m.id); err != nil {
			return 0, err
		}
	}

	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(intents), nil
}
