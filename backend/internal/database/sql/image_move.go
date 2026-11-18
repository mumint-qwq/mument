// Added for mument 007-C. See docs/development/007c/DESIGN.md.
package sql

import (
	"fmt"
	"strings"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
)

// TransferImageMetadata copies a ready B observation to a new filesystem
// version. A fresh guard prevents old target callbacks from completing. The
// source row is removed by the existing directory maintenance batch.
func (db *IndexDB) TransferImageMetadata(source, from, target, to, name, before, after string, size, mtime int64, seq uint64, sameFormat bool) error {
	if sameFormat {
		result, err := db.Exec(`
  INSERT INTO index_items (source,path,parent_path,name,size,mod_time,type,is_dir,is_hidden,has_preview,
    display_aspect_ratio,sort_name,last_updated,`+imageInsertColumnSQL+`)
  SELECT ?,?,?,?,size,?,type,0,?,has_preview,display_aspect_ratio,?,?,
    ?,?,?,image_state,image_width,image_height,image_orientation,image_parser_rev,0,0,?,?
  FROM index_items
  WHERE source=? AND path=? AND is_dir=0 AND image_version=? AND image_state=?
    AND image_parser_rev=? AND image_width>0 AND image_height>0 AND image_orientation BETWEEN 1 AND 8
  ON CONFLICT(source,path) DO UPDATE SET
    name=excluded.name, size=excluded.size, mod_time=excluded.mod_time, type=excluded.type,
    is_dir=0,is_hidden=excluded.is_hidden,has_preview=excluded.has_preview,
    sort_name=excluded.sort_name,last_updated=excluded.last_updated,
    display_aspect_ratio=excluded.display_aspect_ratio,
    image_version=excluded.image_version,image_obs_seq=excluded.image_obs_seq,
    image_guard=MAX(index_items.image_guard+1,excluded.image_guard),
    image_state=excluded.image_state,image_width=excluded.image_width,image_height=excluded.image_height,
    image_orientation=excluded.image_orientation,image_parser_rev=excluded.image_parser_rev,
    image_retry_count=0,image_retry_at=0,image_size=excluded.image_size,image_mtime_ns=excluded.image_mtime_ns
  WHERE index_items.image_obs_seq < excluded.image_obs_seq`,
			target, to, getParentPath(to), name, time.Unix(0, mtime).Unix(), strings.HasPrefix(name, "."), listingNameSortKey(name), time.Now().Unix(),
			after, seq, seq, size, mtime, source, from, before, iteminfo.ImageMetaReady, iteminfo.ImageParserRevision)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n != 0 {
			return err
		}
	}
	// The replaced target can no longer expose its old ratio, even if the moved
	// source has no ready header result. A newer observation takes precedence.
	_, err := db.Exec(`UPDATE index_items SET image_version=?,image_obs_seq=?,image_guard=MAX(image_guard+1,?),
   image_state=0,image_width=0,image_height=0,image_orientation=0,image_parser_rev=0,image_retry_count=0,
   image_retry_at=0,image_size=?,image_mtime_ns=?,display_aspect_ratio=0
   WHERE source=? AND path=? AND is_dir=0 AND image_obs_seq<? AND image_version<>?`,
		after, seq, seq, size, mtime, target, to, seq, after)
	return err
}

type readyImagePath struct {
	Path    string
	Version string
}

// ReadyImagesAfter reads a bounded page using the existing (source,path) key.
func (db *IndexDB) ReadyImagesAfter(source, prefix, after string, limit int) ([]readyImagePath, error) {
	if prefix == "" || !strings.HasSuffix(prefix, "/") {
		return nil, fmt.Errorf("invalid image subtree prefix")
	}
	upper := prefix[:len(prefix)-1] + "0"
	rows, err := db.Query(`SELECT path,image_version FROM index_items
 WHERE source=? AND path>=? AND path<? AND path>? AND is_dir=0 AND image_state=?
 AND image_parser_rev=? ORDER BY path LIMIT ?`, source, prefix, upper, after, iteminfo.ImageMetaReady, iteminfo.ImageParserRevision, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := make([]readyImagePath, 0, limit)
	for rows.Next() {
		var row readyImagePath
		if err := rows.Scan(&row.Path, &row.Version); err != nil {
			return nil, err
		}
		found = append(found, row)
	}
	return found, rows.Err()
}
