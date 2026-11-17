package sql

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/database/sqlitebusy"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
)

// ListingSort is the sort key for a paginated directory listing.
type ListingSort string

const (
	ListingSortName    ListingSort = "name"
	ListingSortSize    ListingSort = "size"
	ListingSortModTime ListingSort = "mod_time"
	ListingSortType    ListingSort = "type"
)

// ListingOrder is the direction applied to the sort key. Directories are always
// returned before files; the order applies within each group.
type ListingOrder string

const (
	ListingOrderAsc  ListingOrder = "asc"
	ListingOrderDesc ListingOrder = "desc"
)

// ListingCursorKey is the decoded (sort key, path) cursor used by the SQL layer.
// Kind is "dir" or "file" and mirrors the two-segment ordering.
type ListingCursorKey struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
	Path string `json:"path"`
}

// DirectoryListingQuery is one bounded page request. After is exclusive.
type DirectoryListingQuery struct {
	Sort        ListingSort
	Order       ListingOrder
	Limit       int
	After       *ListingCursorKey
	ShowHidden  bool
	HideFileExt string
}

// DirectoryListingFingerprint identifies the indexed snapshot of one directory.
// Count catches child additions/removals; DirectoryUpdated is the directory
// row's index write time and catches in-place child updates/refreshes. Both are
// cheap SQL aggregates; no child rows are materialized.
type DirectoryListingFingerprint struct {
	Count            int64
	DirectoryUpdated int64
	SortKeysMissing  bool
}

// DirectoryListingResult is a page plus the snapshot it was read from.
type DirectoryListingResult struct {
	Items       []*iteminfo.FileInfo
	HasMore     bool
	Next        *ListingCursorKey
	Fingerprint DirectoryListingFingerprint
}

type listingRow struct {
	item *iteminfo.FileInfo
	key  string
}

// listingNameSortKey builds a deterministic, SQLite-indexable natural key.
//
// Text runs are lower-cased and tagged with 0x02; digit runs are tagged with
// 0x01 followed by a zero-padded length and the significant digits. That makes
// "2" sort before "10" while keeping ordinary case-insensitive name ordering.
// Names whose keys compare equal (case variants, leading-zero forms) are ordered
// by path, which is unique, so pagination remains a total order.
func listingNameSortKey(name string) string {
	lower := strings.ToLower(name)
	var b strings.Builder
	b.Grow(len(lower) + 8)
	for i := 0; i < len(lower); {
		if lower[i] >= '0' && lower[i] <= '9' {
			j := i
			for j < len(lower) && lower[j] >= '0' && lower[j] <= '9' {
				j++
			}
			digits := strings.TrimLeft(lower[i:j], "0")
			if digits == "" {
				digits = "0"
			}
			b.WriteByte(0x01)
			// Filename components are capped far below 1000 bytes; keep the
			// length prefix fixed-width so it compares numerically.
			fmt.Fprintf(&b, "%03d:%s", len(digits), digits)
			i = j
			continue
		}
		j := i
		for j < len(lower) && (lower[j] < '0' || lower[j] > '9') {
			j++
		}
		b.WriteByte(0x02)
		b.WriteString(lower[i:j])
		i = j
	}
	return b.String()
}

func validateListingQuery(q DirectoryListingQuery) error {
	switch q.Sort {
	case ListingSortName, ListingSortSize, ListingSortModTime, ListingSortType:
	default:
		return fmt.Errorf("unsupported listing sort %q", q.Sort)
	}
	switch q.Order {
	case ListingOrderAsc, ListingOrderDesc:
	default:
		return fmt.Errorf("unsupported listing order %q", q.Order)
	}
	if q.Limit <= 0 {
		return fmt.Errorf("listing limit must be positive")
	}
	if q.After != nil && q.After.Kind != "dir" && q.After.Kind != "file" {
		return fmt.Errorf("invalid listing cursor kind %q", q.After.Kind)
	}
	return nil
}

func listingColumn(sort ListingSort) string {
	switch sort {
	case ListingSortName:
		return "sort_name"
	case ListingSortSize:
		return "size"
	case ListingSortModTime:
		return "mod_time"
	default:
		return "type"
	}
}

// ListingKeyForItem recomputes the cursor sort key for an item returned by a
// page without reading the stored sort_name. Used when the cursor must advance
// to the last *visible* row after access rules filtered a page.
func ListingKeyForItem(sort ListingSort, item *iteminfo.FileInfo) string {
	if item == nil {
		return ""
	}
	if sort == ListingSortName {
		return listingNameSortKey(item.Name)
	}
	return listingKeyForRow(sort, item, "")
}

func listingKeyForRow(sort ListingSort, item *iteminfo.FileInfo, sortName string) string {
	switch sort {
	case ListingSortName:
		return sortName
	case ListingSortSize:
		return strconv.FormatInt(item.Size, 10)
	case ListingSortModTime:
		return strconv.FormatInt(item.ModTime.Unix(), 10)
	default:
		return item.Type
	}
}

func listingHiddenFilter(q DirectoryListingQuery) (string, []interface{}) {
	if q.ShowHidden {
		return "", nil
	}
	return " AND is_hidden = 0", nil
}

// listingHideExtFilter keeps hideFileExt semantics in SQL instead of trimming a
// page after LIMIT. Directories are never filtered by extension, matching the
// live listing behavior.
func listingHideExtFilter(q DirectoryListingQuery, isDir bool) (string, []interface{}) {
	if isDir || q.HideFileExt == "" {
		return "", nil
	}
	exts := strings.Fields(q.HideFileExt)
	if len(exts) == 0 {
		return "", nil
	}
	var clause strings.Builder
	args := make([]interface{}, 0, len(exts))
	for _, ext := range exts {
		ext = strings.TrimSpace(ext)
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		clause.WriteString(" AND lower(name) NOT LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(strings.ToLower(ext)))
	}
	if len(args) == 0 {
		return "", nil
	}
	return clause.String(), args
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "%", "\\%")
	s = strings.ReplaceAll(s, "_", "\\_")
	return s
}

// GetDirectoryListingPage reads one page and its fingerprint from the same
// SQLite read transaction. Only LIMIT-sized rows are materialized; callers never
// receive the full child set.
func (db *IndexDB) GetDirectoryListingPage(ctx context.Context, source, dirPath string, q DirectoryListingQuery) (*DirectoryListingResult, error) {
	if err := validateListingQuery(q); err != nil {
		return nil, err
	}
	tx, err := db.DB().BeginTx(ctx, nil)
	if err != nil {
		return nil, sqlitebusy.Wrap(err)
	}
	defer func() { _ = tx.Rollback() }()

	fingerprint, err := readDirectoryListingFingerprint(tx, source, dirPath)
	if err != nil {
		return nil, sqlitebusy.Wrap(err)
	}
	result := &DirectoryListingResult{Fingerprint: fingerprint}
	if fingerprint.SortKeysMissing {
		// A migration/rescan is incomplete. Do not paginate on an empty key.
		if err = tx.Commit(); err != nil {
			return nil, sqlitebusy.Wrap(err)
		}
		return result, nil
	}

	remaining := q.Limit
	after := q.After
	if after == nil || after.Kind == "dir" {
		need := remaining + 1
		rows, err := readListingSegment(tx, source, dirPath, q, true, after, need)
		if err != nil {
			return nil, sqlitebusy.Wrap(err)
		}
		if len(rows) > remaining {
			rows = rows[:remaining]
			result.HasMore = true
		}
		for _, row := range rows {
			result.Items = append(result.Items, row.item)
		}
		remaining -= len(rows)
		if len(rows) > 0 {
			last := rows[len(rows)-1]
			result.Next = &ListingCursorKey{Kind: "dir", Key: last.key, Path: last.item.Path}
		}
		if result.HasMore {
			if err = tx.Commit(); err != nil {
				return nil, sqlitebusy.Wrap(err)
			}
			return result, nil
		}
	}

	if remaining > 0 && (after == nil || after.Kind == "dir" || after.Kind == "file") {
		need := remaining + 1
		rows, err := readListingSegment(tx, source, dirPath, q, false, after, need)
		if err != nil {
			return nil, sqlitebusy.Wrap(err)
		}
		if len(rows) > remaining {
			rows = rows[:remaining]
			result.HasMore = true
		}
		for _, row := range rows {
			result.Items = append(result.Items, row.item)
		}
		if len(rows) > 0 {
			last := rows[len(rows)-1]
			result.Next = &ListingCursorKey{Kind: "file", Key: last.key, Path: last.item.Path}
		}
	} else if remaining == 0 && (after == nil || after.Kind == "dir") {
		// The page filled exactly with directories. Probe for a first file so
		// hasMore is accurate without adding an item past the limit.
		probe, err := readListingSegment(tx, source, dirPath, q, false, after, 1)
		if err != nil {
			return nil, sqlitebusy.Wrap(err)
		}
		result.HasMore = len(probe) > 0
	}

	if !result.HasMore {
		result.Next = nil
	}
	if err = tx.Commit(); err != nil {
		return nil, sqlitebusy.Wrap(err)
	}
	return result, nil
}

func readDirectoryListingFingerprint(tx *stdsql.Tx, source, dirPath string) (DirectoryListingFingerprint, error) {
	var fp DirectoryListingFingerprint
	if err := tx.QueryRow(`
		SELECT COUNT(*) FROM index_items
		WHERE source = ? AND parent_path = ?`, source, dirPath).Scan(&fp.Count); err != nil {
		return fp, err
	}
	if err := tx.QueryRow(`
		SELECT COALESCE(last_updated, 0) FROM index_items
		WHERE source = ? AND path = ?`, source, dirPath).Scan(&fp.DirectoryUpdated); err != nil && !errors.Is(err, stdsql.ErrNoRows) {
		return fp, err
	}
	// Missing natural-sort keys mean an index upgrade is still backfilling.
	// Force the tiny partial index so an all-populated directory does not scan
	// every child row just to prove that no key is empty.
	var one int
	err := tx.QueryRow(`
		SELECT 1 FROM index_items INDEXED BY idx_listing_sort_name_empty
		WHERE sort_name = '' AND source = ? AND parent_path = ? LIMIT 1`, source, dirPath).Scan(&one)
	if err == nil {
		fp.SortKeysMissing = true
	} else if !errors.Is(err, stdsql.ErrNoRows) {
		return fp, err
	}
	return fp, nil
}

func readListingSegment(tx *stdsql.Tx, source, dirPath string, q DirectoryListingQuery, isDir bool, after *ListingCursorKey, limit int) ([]listingRow, error) {
	column := listingColumn(q.Sort)
	op := ">"
	if q.Order == ListingOrderDesc {
		op = "<"
	}
	query := `
		SELECT path, name, size, mod_time, type, is_dir, is_hidden, has_preview, display_aspect_ratio, sort_name
		FROM index_items
		WHERE source = ? AND parent_path = ? AND is_dir = ?`
	args := []interface{}{source, dirPath, isDir}
	hiddenClause, hiddenArgs := listingHiddenFilter(q)
	query += hiddenClause
	args = append(args, hiddenArgs...)
	hideClause, hideArgs := listingHideExtFilter(q, isDir)
	query += hideClause
	args = append(args, hideArgs...)

	if after != nil && after.Kind == listingKind(isDir) {
		query += fmt.Sprintf(" AND ((%s %s ?) OR (%s = ? AND path %s ?))", column, op, column, op)
		args = append(args, after.Key, after.Key, after.Path)
	}
	query += fmt.Sprintf(" ORDER BY %s %s, path %s LIMIT ?", column, strings.ToUpper(string(q.Order)), strings.ToUpper(string(q.Order)))
	args = append(args, limit)

	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]listingRow, 0, limit)
	for rows.Next() {
		item, sortName, err := scanListingRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, listingRow{item: item, key: listingKeyForRow(q.Sort, item, sortName)})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func listingKind(isDir bool) string {
	if isDir {
		return "dir"
	}
	return "file"
}

func scanListingRow(rows *stdsql.Rows) (*iteminfo.FileInfo, string, error) {
	var item iteminfo.FileInfo
	var modTime int64
	var sortName string
	err := rows.Scan(
		&item.Path,
		&item.Name,
		&item.Size,
		&modTime,
		&item.Type,
		&item.IsDir,
		&item.Hidden,
		&item.HasPreview,
		&item.DisplayAspectRatio,
		&sortName,
	)
	if err != nil {
		return nil, "", err
	}
	item.ModTime = time.Unix(modTime, 0)
	return &item, sortName, nil
}
