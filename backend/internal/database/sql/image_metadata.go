// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package sql

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/database/sqlitebusy"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
)

var imageMetadataColumnList = []struct {
	name string
	sql  string
}{
	{"image_version", "TEXT NOT NULL DEFAULT ''"},
	{"image_obs_seq", "INTEGER NOT NULL DEFAULT 0"},
	{"image_guard", "INTEGER NOT NULL DEFAULT 0"},
	{"image_state", "INTEGER NOT NULL DEFAULT 0"},
	{"image_width", "INTEGER NOT NULL DEFAULT 0"},
	{"image_height", "INTEGER NOT NULL DEFAULT 0"},
	{"image_orientation", "INTEGER NOT NULL DEFAULT 0"},
	{"image_parser_rev", "INTEGER NOT NULL DEFAULT 0"},
	{"image_retry_count", "INTEGER NOT NULL DEFAULT 0"},
	{"image_retry_at", "INTEGER NOT NULL DEFAULT 0"},
	{"image_size", "INTEGER NOT NULL DEFAULT 0"},
	{"image_mtime_ns", "INTEGER NOT NULL DEFAULT 0"},
}

const imageUpsertSetSQL = `
		image_obs_seq = CASE WHEN excluded.image_obs_seq > index_items.image_obs_seq
			THEN excluded.image_obs_seq ELSE index_items.image_obs_seq END,
		image_version = CASE WHEN excluded.image_obs_seq > index_items.image_obs_seq
			AND excluded.image_version <> '' AND excluded.image_version <> index_items.image_version
			THEN excluded.image_version ELSE index_items.image_version END,
		image_guard = CASE WHEN excluded.image_obs_seq > index_items.image_obs_seq
			AND excluded.image_version <> '' AND excluded.image_version <> index_items.image_version
			THEN MAX(excluded.image_guard, index_items.image_guard + 1, excluded.image_obs_seq)
			ELSE index_items.image_guard END,
		image_state = CASE WHEN excluded.image_obs_seq > index_items.image_obs_seq
			AND excluded.image_version <> '' AND excluded.image_version <> index_items.image_version
			THEN excluded.image_state ELSE index_items.image_state END,
		image_width = CASE WHEN excluded.image_obs_seq > index_items.image_obs_seq
			AND excluded.image_version <> '' AND excluded.image_version <> index_items.image_version
			THEN excluded.image_width ELSE index_items.image_width END,
		image_height = CASE WHEN excluded.image_obs_seq > index_items.image_obs_seq
			AND excluded.image_version <> '' AND excluded.image_version <> index_items.image_version
			THEN excluded.image_height ELSE index_items.image_height END,
		image_orientation = CASE WHEN excluded.image_obs_seq > index_items.image_obs_seq
			AND excluded.image_version <> '' AND excluded.image_version <> index_items.image_version
			THEN excluded.image_orientation ELSE index_items.image_orientation END,
		image_parser_rev = CASE WHEN excluded.image_obs_seq > index_items.image_obs_seq
			AND excluded.image_version <> '' AND excluded.image_version <> index_items.image_version
			THEN excluded.image_parser_rev ELSE index_items.image_parser_rev END,
		image_retry_count = CASE WHEN excluded.image_obs_seq > index_items.image_obs_seq
			AND excluded.image_version <> '' AND excluded.image_version <> index_items.image_version
			THEN excluded.image_retry_count ELSE index_items.image_retry_count END,
		image_retry_at = CASE WHEN excluded.image_obs_seq > index_items.image_obs_seq
			AND excluded.image_version <> '' AND excluded.image_version <> index_items.image_version
			THEN excluded.image_retry_at ELSE index_items.image_retry_at END,
		image_size = CASE WHEN excluded.image_obs_seq >= index_items.image_obs_seq
			AND excluded.image_version <> '' THEN excluded.image_size ELSE index_items.image_size END,
		image_mtime_ns = CASE WHEN excluded.image_obs_seq >= index_items.image_obs_seq
			AND excluded.image_version <> '' THEN excluded.image_mtime_ns ELSE index_items.image_mtime_ns END,
		display_aspect_ratio = CASE WHEN excluded.image_obs_seq > index_items.image_obs_seq
			AND excluded.image_version <> '' AND excluded.image_version <> index_items.image_version
			THEN excluded.display_aspect_ratio ELSE index_items.display_aspect_ratio END`

const imageInsertColumnSQL = `image_version, image_obs_seq, image_guard, image_state, image_width, image_height, image_orientation, image_parser_rev, image_retry_count, image_retry_at, image_size, image_mtime_ns`

const imageInsertPlaceholders = "?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?"

// imageInsertValues returns the 12 image-column arguments plus the projection
// value for a new row. Ordinary rows carry no image state and leave older
// validated values untouched on conflict.
func (db *IndexDB) imageInsertValues(info *iteminfo.FileInfo) ([]interface{}, float64) {
	if info == nil || info.Image == nil {
		return []interface{}{"", 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, info.DisplayAspectRatio
	}
	im := info.Image
	ratio := 0.0
	if im.State == iteminfo.ImageMetaReady {
		ratio = info.DisplayAspectRatio
	}
	// The birth token belongs to the INSERT, not to a cached observation. A
	// cache hit may carry a guard from a row lifetime that was deleted; never
	// let that identity become a new row's guard. Existing same-version rows
	// ignore this candidate in the UPSERT and keep their guard, so ordinary
	// refreshes do not invalidate an in-flight worker.
	var birthGuard uint64
	if im.Version != "" {
		birthGuard = db.NextImageObsSeq()
	}
	return []interface{}{
		im.Version, im.ObsSeq, birthGuard, int(im.State), im.Width, im.Height,
		int(im.Orientation), int(im.ParserRev), int(im.RetryCount), im.RetryAt,
		im.LogicalSize, im.MTimeNS,
	}, ratio
}

func (db *IndexDB) ensureImageMetadataColumns() (bool, error) {
	rows, err := db.Query("PRAGMA table_info(index_items)")
	if err != nil {
		return false, err
	}
	present := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue interface{}
		if err = rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return false, err
		}
		present[name] = true
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	if err = rows.Close(); err != nil {
		return false, err
	}
	added := false
	for _, col := range imageMetadataColumnList {
		if present[col.name] {
			continue
		}
		if _, err = db.Exec("ALTER TABLE index_items ADD COLUMN " + col.name + " " + col.sql); err != nil {
			return false, err
		}
		added = true
	}
	return added, nil
}

// repairImageMetadata runs cheap SQL-only reconciliation at startup. It never
// opens image files; stale versions are left for the scanner/backfill worker.
func (db *IndexDB) repairImageMetadata() error {
	// Legacy A rows only had a ratio and second-precision mtime. They have no
	// private version, so their ratio is not a B-validated result. Old A can
	// also add such rows after migration; clear both cases.
	if _, err := db.Exec("UPDATE index_items SET display_aspect_ratio = 0 WHERE is_dir = 0 AND image_version = '' AND display_aspect_ratio <> 0"); err != nil {
		return err
	}
	// A parser revision change invalidates old results, including their public
	// projection. The scanner/backfill window will rebuild them.
	if _, err := db.Exec("UPDATE index_items SET image_state = 0, display_aspect_ratio = 0 WHERE is_dir = 0 AND image_state = 1 AND image_parser_rev <> ?", int(iteminfo.ImageParserRevision)); err != nil {
		return err
	}
	// A old writer updates mod_time but cannot update image_mtime_ns. That is an
	// observable signal that its private fields may no longer describe the file.
	if _, err := db.Exec("UPDATE index_items SET image_state = 0, display_aspect_ratio = 0 WHERE is_dir = 0 AND image_state = 1 AND image_version <> '' AND image_mtime_ns / 1000000000 <> mod_time"); err != nil {
		return err
	}
	// Recompute the projection from the stored encoded dimensions/orientation.
	// This repairs a zeroed or overwritten display_aspect_ratio left by old A.
	if _, err := db.Exec("UPDATE index_items SET display_aspect_ratio = CASE WHEN image_orientation IN (5,6,7,8) THEN CAST(image_height AS REAL) / CAST(image_width AS REAL) ELSE CAST(image_width AS REAL) / CAST(image_height AS REAL) END WHERE is_dir = 0 AND image_state = 1 AND image_parser_rev = ? AND image_width > 0 AND image_height > 0 AND display_aspect_ratio <> CASE WHEN image_orientation IN (5,6,7,8) THEN CAST(image_height AS REAL) / CAST(image_width AS REAL) ELSE CAST(image_width AS REAL) / CAST(image_height AS REAL) END", int(iteminfo.ImageParserRevision)); err != nil {
		return err
	}
	return nil
}

func (db *IndexDB) seedImageObsSeq() error {
	var maxObs, maxGuard sql.NullInt64
	if err := db.QueryRow("SELECT COALESCE(MAX(image_obs_seq), 0), COALESCE(MAX(image_guard), 0) FROM index_items").Scan(&maxObs, &maxGuard); err != nil {
		return err
	}
	max := maxObs.Int64
	if maxGuard.Int64 > max {
		max = maxGuard.Int64
	}
	if max > 0 {
		db.imageObsSeq.Store(uint64(max))
	}
	return nil
}

// NextImageObsSeq returns a process-wide monotonic observation number, seeded
// from the database maximum so a restart cannot move it backwards.
func (db *IndexDB) NextImageObsSeq() uint64 {
	return db.imageObsSeq.Add(1)
}

// ImageBackfillPending reports that this open added the image columns and
// scanners should perform one startup pass without treating the file index as
// new or discarding its persisted complexity/schedule.
func (db *IndexDB) ImageBackfillPending() bool {
	return db.imageBackfillPending
}

// ImageWork is a row that is eligible for a bounded header read.
type ImageWork struct {
	Path       string
	Version    string
	ObsSeq     uint64
	Guard      uint64
	State      iteminfo.ImageMetaState
	RetryCount uint16
	RetryAt    int64
}

// ImageMetadataCommit is the result of one bounded header read.
type ImageMetadataCommit struct {
	State       iteminfo.ImageMetaState
	Width       int
	Height      int
	Orientation uint8
	Ratio       float64
	RetryCount  uint16
	RetryAt     int64
}

// scanRowWithImage is used only by GetDirectoryChildren, the one query that
// feeds the directory cache. All other nine-column SELECT/scan paths remain
// untouched, so adding private columns cannot shift an unrelated scan.
func scanRowWithImage(rows *sql.Rows) (*iteminfo.FileInfo, error) {
	var item iteminfo.FileInfo
	var modTime int64
	var im iteminfo.ImageIndexMeta
	var state int
	err := rows.Scan(
		&item.Path, &item.Name, &item.Size, &modTime, &item.Type, &item.IsDir,
		&item.Hidden, &item.HasPreview, &item.DisplayAspectRatio,
		&im.Version, &im.ObsSeq, &im.Guard, &state, &im.Width, &im.Height,
		&im.Orientation, &im.ParserRev, &im.RetryCount, &im.RetryAt,
		&im.LogicalSize, &im.MTimeNS,
	)
	if err != nil {
		return nil, err
	}
	item.ModTime = time.Unix(modTime, 0)
	im.State = iteminfo.ImageMetaState(state)
	if strings.HasPrefix(item.Type, "image/") || im.Version != "" {
		item.Image = &im
	}
	return &item, nil
}

func (db *IndexDB) GetImageWorksByPaths(source string, paths []string) (map[string]ImageWork, error) {
	result := make(map[string]ImageWork, len(paths))
	if len(paths) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(paths))
	args := make([]interface{}, 0, len(paths)+1)
	args = append(args, source)
	for i, path := range paths {
		placeholders[i] = "?"
		args = append(args, path)
	}
	query := fmt.Sprintf("SELECT path, image_version, image_obs_seq, image_guard, image_state, image_retry_count, image_retry_at FROM index_items WHERE source = ? AND is_dir = 0 AND path IN (%s)", strings.Join(placeholders, ","))
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, sqlitebusy.Wrap(err)
	}
	defer rows.Close()
	for rows.Next() {
		var w ImageWork
		var state int
		if err := rows.Scan(&w.Path, &w.Version, &w.ObsSeq, &w.Guard, &state, &w.RetryCount, &w.RetryAt); err != nil {
			return nil, err
		}
		w.State = iteminfo.ImageMetaState(state)
		result[w.Path] = w
	}
	return result, rows.Err()
}

// GetImageMetadataCandidates returns at most limit rows needing a header read.
// parentPath=="" scans the whole source; unsupported-current rows are not
// selected so a bad image cannot hot-loop every scan.
func (db *IndexDB) GetImageMetadataCandidates(source, parentPath string, limit int, now int64) ([]ImageWork, error) {
	if limit <= 0 {
		return nil, nil
	}
	query := "SELECT path, image_version, image_obs_seq, image_guard, image_state, image_retry_count, image_retry_at FROM index_items WHERE source = ? AND is_dir = 0 AND image_version <> '' AND ((image_state IN (0,2,4) AND image_retry_at <= ?) OR (image_parser_rev <> ? AND image_state IN (0,1,2,4) AND image_retry_at <= ?) OR (image_state = 3 AND image_parser_rev <> ?))"
	args := []interface{}{source, now, int(iteminfo.ImageParserRevision), now, int(iteminfo.ImageParserRevision)}
	if parentPath != "" {
		query += " AND parent_path = ?"
		args = append(args, parentPath)
	}
	query += " ORDER BY path LIMIT ?"
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, sqlitebusy.Wrap(err)
	}
	defer rows.Close()
	out := make([]ImageWork, 0, limit)
	for rows.Next() {
		var w ImageWork
		var state int
		if err := rows.Scan(&w.Path, &w.Version, &w.ObsSeq, &w.Guard, &state, &w.RetryCount, &w.RetryAt); err != nil {
			return nil, err
		}
		w.State = iteminfo.ImageMetaState(state)
		out = append(out, w)
	}
	return out, rows.Err()
}

// DeferImageWork invalidates and backs off a row whose stat failed or whose
// expected version no longer matches. UPDATE-only, so a removed path is not
// recreated; the future retry_at lets the bounded query reach later paths.
// A ready result is a completed observation of the same version and guard. A
// late stat failure from an older task must not erase it; a real version change
// is already excluded by expectedVersion and is owned by the scanner.
func (db *IndexDB) DeferImageWork(source, path, expectedVersion string, expectedGuard uint64, retryAt int64) (bool, error) {
	query := `UPDATE index_items SET
		image_state = ?, image_width = 0, image_height = 0, image_orientation = 0,
		image_parser_rev = 0, image_retry_count = image_retry_count + 1,
		image_retry_at = ?, display_aspect_ratio = 0
		WHERE source = ? AND path = ? AND is_dir = 0
		  AND image_version = ? AND image_guard = ? AND image_state <> ?`
	res, err := db.Exec(query, int(iteminfo.ImageMetaDeferred), retryAt, source, path, expectedVersion, expectedGuard, int(iteminfo.ImageMetaReady))
	if err != nil {
		return false, sqlitebusy.Wrap(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// CommitImageMetadata is UPDATE-only: a deleted/moved path or a changed
// version/guard updates zero rows and is never re-created by an UPSERT.
func (db *IndexDB) CommitImageMetadata(source, path, expectedVersion string, expectedGuard uint64, commit ImageMetadataCommit) (bool, error) {
	query := "UPDATE index_items SET image_state = ?, image_width = ?, image_height = ?, image_orientation = ?, image_parser_rev = ?, image_retry_count = ?, image_retry_at = ?, display_aspect_ratio = ? WHERE source = ? AND path = ? AND is_dir = 0 AND image_version = ? AND image_guard = ?"
	args := []interface{}{
		int(commit.State), commit.Width, commit.Height, int(commit.Orientation),
		int(iteminfo.ImageParserRevision), int(commit.RetryCount), commit.RetryAt,
		commit.Ratio, source, path, expectedVersion, expectedGuard,
	}
	if commit.State != iteminfo.ImageMetaReady {
		query += " AND image_state <> 1"
	}
	for attempt := 1; attempt <= dbTransientOpAttempts; attempt++ {
		res, err := db.Exec(query, args...)
		if err != nil {
			if sqlitebusy.IsBusyOrLocked(err) && attempt < dbTransientOpAttempts {
				time.Sleep(dbTransientRetryBackoff)
				continue
			}
			return false, sqlitebusy.Wrap(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return false, err
		}
		return n == 1, nil
	}
	return false, sqlitebusy.ErrBusy
}
