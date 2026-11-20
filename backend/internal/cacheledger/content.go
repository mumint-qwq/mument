// 007-F keeps original content evidence separate from encoded result digests.
package cacheledger

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const ContentAlgorithm = "sha256-full-v1"
const contentSchema = `
CREATE TABLE IF NOT EXISTS content_proofs (
 asset TEXT PRIMARY KEY REFERENCES assets(id), algorithm TEXT NOT NULL,
 bytes INTEGER NOT NULL, digest TEXT NOT NULL, source TEXT NOT NULL,
 path TEXT NOT NULL, version TEXT NOT NULL, verified_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS content_match ON content_proofs(algorithm,bytes,digest,asset);
CREATE TABLE IF NOT EXISTS content_ready (asset TEXT PRIMARY KEY REFERENCES content_proofs(asset), bytes INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS content_ready_size ON content_ready(bytes,asset);
CREATE TRIGGER IF NOT EXISTS content_ready_proof AFTER INSERT ON content_proofs BEGIN
 INSERT OR IGNORE INTO content_ready SELECT NEW.asset,NEW.bytes WHERE EXISTS(SELECT 1 FROM results WHERE asset=NEW.asset);
END;
CREATE TRIGGER IF NOT EXISTS content_ready_result AFTER INSERT ON results BEGIN
 INSERT OR IGNORE INTO content_ready SELECT asset,bytes FROM content_proofs WHERE asset=NEW.asset;
END;
CREATE TRIGGER IF NOT EXISTS content_ready_remove AFTER DELETE ON results BEGIN
 DELETE FROM content_ready WHERE asset=OLD.asset AND NOT EXISTS(SELECT 1 FROM results WHERE asset=OLD.asset);
END;
CREATE TABLE IF NOT EXISTS content_scopes (scope TEXT PRIMARY KEY, identity TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS content_work (
 scope TEXT NOT NULL, path TEXT NOT NULL, version TEXT NOT NULL,
 attempts INTEGER NOT NULL, retry_at INTEGER NOT NULL, state TEXT NOT NULL, updated INTEGER NOT NULL,
 PRIMARY KEY(scope,path));
CREATE INDEX IF NOT EXISTS content_work_expiry ON content_work(updated,scope,path);
CREATE TABLE IF NOT EXISTS content_hints (
 source TEXT NOT NULL, path TEXT NOT NULL, version TEXT NOT NULL,
 exdev INTEGER NOT NULL, created INTEGER NOT NULL, PRIMARY KEY(source,path));
CREATE INDEX IF NOT EXISTS content_hint_age ON content_hints(created,source,path);
`

type ContentProof struct {
	Asset, Digest string
	Bytes         int64
}
type ContentResult struct {
	Variant string
	Result
}

// BindContentScope never adopts a replacement root under an existing config key.
func (r *Repository) BindContentScope(ctx context.Context, scope, identity string) error {
	if scope == "" || identity == "" {
		return ErrStale
	}
	_, err := r.db.ExecContext(ctx, "INSERT OR IGNORE INTO content_scopes VALUES(?,?)", scope, identity)
	if err != nil {
		return err
	}
	var old string
	if err = r.db.QueryRowContext(ctx, "SELECT identity FROM content_scopes WHERE scope=?", scope).Scan(&old); err != nil {
		return err
	}
	if old != identity {
		return ErrStale
	}
	return nil
}

func (r *Repository) ContentProof(ctx context.Context, ref Ref) (bool, error) {
	var yes bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM content_proofs WHERE asset=? AND algorithm=? AND bytes=?)`, ref.Asset, ContentAlgorithm, ref.Version.Size).Scan(&yes)
	return yes, err
}

// Candidate size is only a cheap filter. Callers must compare the complete hash.
func (r *Repository) ContentCandidates(ctx context.Context, size int64) ([]ContentProof, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT p.asset,p.digest,p.bytes FROM content_ready q INDEXED BY content_ready_size JOIN content_proofs p ON p.asset=q.asset
 WHERE p.algorithm=? AND q.bytes=? ORDER BY q.asset LIMIT 16`, ContentAlgorithm, size)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContentProof
	for rows.Next() {
		var p ContentProof
		if err := rows.Scan(&p.Asset, &p.Digest, &p.Bytes); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repository) ContentResults(ctx context.Context, asset string) ([]ContentResult, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT variant,storage_key,size,digest,generated_at,last_used_at FROM results WHERE asset=? ORDER BY variant LIMIT 16`, asset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContentResult
	for rows.Next() {
		var p ContentResult
		if err := rows.Scan(&p.Variant, &p.Key, &p.Size, &p.Digest, &p.GeneratedAt, &p.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func validContent(p ContentProof, v Version) bool {
	b, err := hex.DecodeString(p.Digest)
	return err == nil && len(b) == 32 && v.Strong && p.Bytes == v.Size && p.Bytes > 0
}

// ProveContent may only be called after hashing this token's exact source fd.
// A target fd is never evidence for an unrelated old token.
func (r *Repository) ProveContent(ctx context.Context, ref Ref, p ContentProof) error {
	if p.Asset != ref.Asset || !validContent(p, ref.Version) {
		return ErrStale
	}
	res, err := r.db.ExecContext(ctx, `INSERT INTO content_proofs
 SELECT asset,?,?,?,?,?,?,? FROM refs WHERE source=? AND path=? AND asset=? AND version=?
 ON CONFLICT(asset) DO UPDATE SET verified_at=excluded.verified_at
 WHERE content_proofs.digest=excluded.digest AND content_proofs.bytes=excluded.bytes AND content_proofs.algorithm=excluded.algorithm`,
		ContentAlgorithm, p.Bytes, p.Digest, ref.Source, ref.Path, versionKey(ref.Version), time.Now().Unix(), ref.Source, ref.Path, ref.Asset, versionKey(ref.Version))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return ErrStale
	}
	return err
}

// BindContent is called under physical gates then the location gate. A witness
// publication must still be present; E intents must have settled before binding.
// A foreground publication on the target always wins. No result row is copied.
func (r *Repository) BindContent(ctx context.Context, ref Ref, p ContentProof, witness ContentResult) error {
	if !validContent(p, ref.Version) || ref.Asset == p.Asset {
		return ErrStale
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var yes bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM content_proofs p JOIN results r ON r.asset=p.asset
 WHERE p.asset=? AND p.algorithm=? AND p.bytes=? AND p.digest=? AND r.variant=? AND r.storage_key=? AND r.digest=? AND r.generated_at=?)
 AND NOT EXISTS(SELECT 1 FROM results WHERE asset=?)
 AND NOT EXISTS(SELECT 1 FROM moves WHERE state='pending' AND ((source=? AND path=?) OR (target_source=? AND target_path=?)))
 AND NOT EXISTS(SELECT 1 FROM orphan_intents WHERE storage_key IN (SELECT storage_key FROM results WHERE asset=?) AND state<>'removed')
 AND NOT EXISTS(SELECT 1 FROM capacity_intents WHERE storage_key IN (SELECT storage_key FROM results WHERE asset=?) AND state<>'removed')`,
		p.Asset, ContentAlgorithm, p.Bytes, p.Digest, witness.Variant, witness.Key, witness.Digest, witness.GeneratedAt, ref.Asset, ref.Source, ref.Path, ref.Source, ref.Path, p.Asset, p.Asset).Scan(&yes)
	if err != nil {
		return err
	}
	if !yes {
		return ErrStale
	}
	res, err := tx.ExecContext(ctx, `UPDATE refs SET asset=? WHERE source=? AND path=? AND version=? AND asset=?`, p.Asset, ref.Source, ref.Path, versionKey(ref.Version), ref.Asset)
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
	// The old empty asset was not a published source; keep C/E's conservative
	// unproven marker rather than inventing a confirmed deletion.
	return tx.Commit()
}

// ContentWork coalesces the current indexed version at one path. Pending rows
// survive process exit. Completed rows are skipped; failed reads have a fixed
// attempt budget, while resource/offline deferrals are not recorded as failures.
func (r *Repository) ContentWork(ctx context.Context, scope, path, version string) (bool, error) {
	var old, state string
	var attempts, retry int64
	err := r.db.QueryRowContext(ctx, `SELECT version,state,attempts,retry_at FROM content_work WHERE scope=? AND path=?`, scope, path).Scan(&old, &state, &attempts, &retry)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err == nil && old == version && (state == "done" || attempts >= 3 || retry > time.Now().Unix()) {
		return false, nil
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO content_work VALUES(?,?,?,0,0,'pending',?) ON CONFLICT(scope,path) DO UPDATE SET
 version=excluded.version,attempts=CASE WHEN version=excluded.version THEN attempts ELSE 0 END,
 retry_at=CASE WHEN version=excluded.version THEN retry_at ELSE 0 END,state='pending',updated=excluded.updated`, scope, path, version, time.Now().Unix())
	return err == nil, err
}
func (r *Repository) FinishContentWork(ctx context.Context, scope, path, version string, done bool) error {
	state := "pending"
	if done {
		state = "done"
	}
	_, err := r.db.ExecContext(ctx, `UPDATE content_work SET state=?,attempts=attempts+?,retry_at=?,updated=? WHERE scope=? AND path=? AND version=?`, state, boolInt(!done), time.Now().Add(time.Minute).Unix(), time.Now().Unix(), scope, path, version)
	return err
}
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (r *Repository) HintContent(ctx context.Context, source, path string, v Version, exdev bool) error {
	if !v.Strong || !validPath(source, path) {
		return fmt.Errorf("invalid content hint")
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO content_hints SELECT ?,?,?,?,? WHERE
 (SELECT COUNT(*) FROM content_hints)<4096 OR EXISTS(SELECT 1 FROM content_hints WHERE source=? AND path=?)
 ON CONFLICT(source,path) DO UPDATE SET version=excluded.version,exdev=excluded.exdev,created=excluded.created`, source, path, versionKey(v), boolInt(exdev), time.Now().Unix(), source, path)
	return err
}

type ContentHint struct {
	Source, Path string
	Version      Version
	EXDEV        bool
}

func (r *Repository) ContentHints(ctx context.Context) ([]ContentHint, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT source,path,version,exdev FROM content_hints ORDER BY created,source,path LIMIT 16`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContentHint
	for rows.Next() {
		var h ContentHint
		var v string
		if err = rows.Scan(&h.Source, &h.Path, &v, &h.EXDEV); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(v), &h.Version); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
func (r *Repository) DropContentHint(ctx context.Context, h ContentHint) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM content_hints WHERE source=? AND path=? AND version=?`, h.Source, h.Path, versionKey(h.Version))
	return err
}
func (r *Repository) PruneContentWork(ctx context.Context) error {
	// No unbounded historical per-version log. Removed paths expire after 30 days;
	// proofs remain with assets and are never reconstructed from target files.
	_, err := r.db.ExecContext(ctx, `DELETE FROM content_work WHERE rowid IN (SELECT rowid FROM content_work WHERE updated<? ORDER BY updated LIMIT 32);
 DELETE FROM content_hints WHERE rowid IN (SELECT rowid FROM content_hints WHERE created<? ORDER BY created LIMIT 32)`, time.Now().Add(-30*24*time.Hour).Unix(), time.Now().Add(-24*time.Hour).Unix())
	return err
}

func (r *Repository) ContentMatches(ctx context.Context, p ContentProof) ([]ContentProof, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT asset,digest,bytes FROM content_proofs p WHERE algorithm=? AND bytes=? AND digest=?
 AND EXISTS(SELECT 1 FROM results WHERE asset=p.asset) ORDER BY asset LIMIT 16`, ContentAlgorithm, p.Bytes, p.Digest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContentProof
	for rows.Next() {
		var p ContentProof
		if err = rows.Scan(&p.Asset, &p.Digest, &p.Bytes); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (r *Repository) ContentPending(ctx context.Context, key string) (bool, error) {
	var yes bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM orphan_intents WHERE storage_key=? AND state<>'removed') OR EXISTS(SELECT 1 FROM capacity_intents WHERE storage_key=? AND state<>'removed')`, key, key).Scan(&yes)
	return yes, err
}
func (r *Repository) KnownContent(ctx context.Context, ref Ref) (ContentProof, bool, error) {
	var p ContentProof
	err := r.db.QueryRowContext(ctx, `SELECT p.asset,p.digest,p.bytes FROM content_proofs p JOIN refs f ON f.asset=p.asset WHERE f.source=? AND f.path=? AND f.asset=? AND f.version=? AND p.algorithm=? AND p.bytes=?`, ref.Source, ref.Path, ref.Asset, versionKey(ref.Version), ContentAlgorithm, ref.Version.Size).Scan(&p.Asset, &p.Digest, &p.Bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	return p, err == nil, err
}
func (r *Repository) DeferContentWork(ctx context.Context, scope, path, version string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE content_work SET retry_at=?,updated=? WHERE scope=? AND path=? AND version=?`, time.Now().Add(time.Minute).Unix(), time.Now().Unix(), scope, path, version)
	return err
}
