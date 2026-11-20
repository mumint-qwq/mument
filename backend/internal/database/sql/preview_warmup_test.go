package sql

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreviewWarmupSeekAndResume(t *testing.T) {
	db, _, err := NewIndexDB("warmup_seek", "WAL", 100, 8, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx := t.Context()

	_, err = db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<6000)
		INSERT INTO index_items(source,path,parent_path,name,size,mod_time,type,is_dir,is_hidden,has_preview,image_version,image_guard)
		SELECT 'photos', printf('/%s/item-%05d.jpg', CASE WHEN x%2=0 THEN 'Pictures' ELSE 'Pictures-old' END, x),
		'/', 'item.jpg', 300000, 1, 'image/jpeg', 0, 0, 1, printf('version:%d',x), x FROM n`)
	require.NoError(t, err)
	require.NoError(t, db.PreparePreviewWarmup(ctx))
	require.NoError(t, db.PreparePreviewWarmup(ctx))

	for _, tc := range []struct {
		query, index string
		args         []any
	}{
		{warmupHistoryQuery, "idx_warmup_path", []any{"photos", "/Pictures/", "/Pictures0", ""}},
		{warmupRecentQuery, "idx_warmup_recent", []any{"photos"}},
		{warmupManualQuery, "sqlite_autoindex_index_items_1", []any{"photos", "/Pictures/", "/Pictures0", ""}},
	} {
		plan, err := db.ExplainQueryPlan(tc.query, tc.args...)
		require.NoError(t, err)
		require.Contains(t, plan, "SEARCH index_items USING INDEX "+tc.index)
		require.NotContains(t, plan, "TEMP B-TREE")
		t.Logf("%s", plan)
	}

	page, err := db.PreviewWarmupPage(ctx, "photos", "/Pictures/", "", false, false)
	require.NoError(t, err)
	require.Len(t, page, 16)
	done := page[0]
	require.Equal(t, "/Pictures/item-00002.jpg", done.Path)
	require.NoError(t, db.MarkPreviewWarmup(ctx, "photos", done, -1, 0))
	page, err = db.PreviewWarmupPage(ctx, "photos", "/Pictures/", "", false, false)
	require.NoError(t, err)
	require.Equal(t, "/Pictures/item-00004.jpg", page[0].Path, "a restart skips completed versions")
	manual, err := db.PreviewWarmupPage(ctx, "photos", "/Pictures/", "", true, false)
	require.NoError(t, err)
	require.Equal(t, done.Path, manual[0].Path)

	require.NoError(t, db.MarkPreviewWarmup(ctx, "photos", page[0], 2, 4321))
	page, err = db.PreviewWarmupPage(ctx, "photos", "/Pictures/", "", false, false)
	require.NoError(t, err)
	require.EqualValues(t, 2, page[0].Attempts)
	require.EqualValues(t, 4321, page[0].RetryAt)
	_, err = db.Exec("UPDATE index_items SET image_guard=7000,image_version='replaced' WHERE path=?", done.Path)
	require.NoError(t, err)
	require.NoError(t, db.MarkPreviewWarmup(ctx, "photos", done, -1, 0))
	recent, err := db.PreviewWarmupPage(ctx, "photos", "/Pictures/", "", false, true)
	require.NoError(t, err)
	require.Len(t, recent, 16)
	require.Equal(t, "replaced", recent[0].Version)
	require.Zero(t, recent[0].Attempts)

	last := done.Path
	for i := 0; i < 64; i++ {
		_, err := db.Exec(`UPDATE index_items SET image_guard=? WHERE path=?`, 8000+i, fmt.Sprintf("/Pictures/item-%05d.jpg", 5000+i*2))
		require.NoError(t, err)
		page, err := db.PreviewWarmupPage(ctx, "photos", "/Pictures/", last, false, false)
		require.NoError(t, err)
		require.Len(t, page, 16)
		require.Greater(t, page[0].Path, last)
		require.NoError(t, db.MarkPreviewWarmup(ctx, "photos", page[0], -1, 0))
		last = page[0].Path
	}
	require.Equal(t, "/Pictures/item-00130.jpg", last)
}
