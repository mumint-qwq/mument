//go:build linux

package cachetrial

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	_ "modernc.org/sqlite"
)

const resultPage = `SELECT p.rowid,
 CASE WHEN length(p.asset)<=512 THEN p.asset ELSE '' END,
 CASE WHEN length(p.variant)<=512 THEN p.variant ELSE '' END,
 CASE WHEN length(p.storage_key)<=2048 THEN p.storage_key ELSE '' END,
 p.size,CASE WHEN length(p.digest)=64 THEN p.digest ELSE '' END,p.generated_at,p.last_used_at,
 EXISTS(SELECT 1 FROM refs f WHERE f.asset=p.asset)
 FROM results p WHERE p.rowid>? ORDER BY p.rowid LIMIT ?`

const movePage = `SELECT CASE WHEN length(asset)<=512 THEN asset END,
 CASE WHEN length(target_asset)<=512 THEN target_asset END FROM moves WHERE state=? ORDER BY id LIMIT ?`

type publication struct {
	row, size, generated, used  int64
	asset, variant, key, digest string
	hasRef                      bool
}

func openSnapshot(path string) (*sql.DB, string, func() bool, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, "", nil, err
	}
	initial, err := os.Lstat(absolute)
	if err != nil || !initial.Mode().IsRegular() {
		return nil, "", nil, errors.New("snapshot must be an existing regular file")
	}
	stat := initial.Sys().(*syscall.Stat_t)
	identity := anonymous(fmt.Sprintf("%d/%d/%d/%v/%v", stat.Dev, stat.Ino, initial.Size(), stat.Mtim, stat.Ctim))
	unchanged := func() bool {
		current, err := os.Lstat(absolute)
		if err != nil || !os.SameFile(initial, current) || initial.Size() != current.Size() ||
			!initial.ModTime().Equal(current.ModTime()) || stat.Ctim != current.Sys().(*syscall.Stat_t).Ctim {
			return false
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, err := os.Lstat(absolute + suffix); !errors.Is(err, os.ErrNotExist) {
				return false
			}
		}
		return true
	}
	if !unchanged() {
		return nil, "", nil, errors.New("snapshot changed or has SQLite sidecars")
	}
	// Require a standalone rollback-mode backup. A WAL-mode header with
	// missing sidecars could be an incomplete copy of the live ledger.
	f, err := os.Open(absolute)
	if err != nil {
		return nil, "", nil, errors.New("cannot read snapshot header")
	}
	var header [20]byte
	_, readErr := io.ReadFull(f, header[:])
	f.Close()
	if readErr != nil || string(header[:16]) != "SQLite format 3\x00" || header[18] != 1 || header[19] != 1 {
		return nil, "", nil, errors.New("snapshot requires a standalone SQLite backup in DELETE journal mode")
	}
	uri := url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, "", nil, errors.New("cannot open read-only snapshot")
	}
	db.SetMaxOpenConns(1)
	return db, identity, unchanged, nil
}

func readMoves(ctx context.Context, db *sql.DB, limit int) (map[string]bool, bool, error) {
	protected := make(map[string]bool)
	for _, state := range []string{"pending", "ambiguous"} {
		page, err := db.QueryContext(ctx, movePage, state, limit+1)
		if err != nil {
			return nil, false, err
		}
		for page.Next() {
			var asset, target sql.NullString
			if err := page.Scan(&asset, &target); err != nil {
				page.Close()
				return nil, false, err
			}
			limit--
			if limit < 0 || !asset.Valid || asset.String == "" || !target.Valid {
				page.Close()
				return protected, false, nil
			}
			protected[asset.String] = true
			if target.String != "" {
				protected[target.String] = true
			}
		}
		if err := page.Err(); err != nil {
			page.Close()
			return nil, false, err
		}
		page.Close()
	}
	return protected, true, nil
}
