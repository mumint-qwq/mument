// Added for mument 007-C.
package web

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	indexsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/stretchr/testify/require"
)

func setMoveEXIF(t *testing.T, path string, orientation uint16) {
	t.Helper()
	base, err := os.ReadFile(path)
	require.NoError(t, err)
	payload := []byte{'E', 'x', 'i', 'f', 0, 0, 'I', 'I', 0x2a, 0, 8, 0, 0, 0, 1, 0}
	entry := make([]byte, 12)
	binary.LittleEndian.PutUint16(entry[0:2], 0x0112)
	binary.LittleEndian.PutUint16(entry[2:4], 3)
	binary.LittleEndian.PutUint32(entry[4:8], 1)
	binary.LittleEndian.PutUint16(entry[8:10], orientation)
	payload = append(payload, entry...)
	payload = append(payload, 0, 0, 0, 0)
	length := len(payload) + 2
	jpegBytes := []byte{0xff, 0xd8, 0xff, 0xe1, byte(length >> 8), byte(length)}
	jpegBytes = append(jpegBytes, payload...)
	jpegBytes = append(jpegBytes, base[2:]...)
	require.NoError(t, os.WriteFile(path, jpegBytes, 0o644))
}

func seedMoveImageMetadata(t *testing.T, env *moveIndexEnv, path string, orientation uint8) indexsql.ImageWork {
	t.Helper()
	work, err := env.db.GetImageWorksByPaths("src", []string{path})
	require.NoError(t, err)
	row, exists := work[path]
	require.True(t, exists)
	require.NotEmpty(t, row.Version)
	ok, err := env.db.CommitImageMetadata("src", path, row.Version, row.Guard, indexsql.ImageMetadataCommit{
		State: iteminfo.ImageMetaReady, Width: 800, Height: 600, Orientation: orientation,
		Ratio: iteminfo.DisplayAspectRatioFromDimensions(800, 600, orientation),
	})
	require.NoError(t, err)
	require.True(t, ok)
	return row
}

func TestLedgerOverwriteMetadataAndVariantsHTTP(t *testing.T) {
	env := newMoveIndexEnv(t, "src")
	server := ledgerPreviewServer(t, env)
	env.mkdir("src", "A")
	env.mkdir("src", "B")
	from, to := "/A/camera.jpg", "/B/camera.jpg"
	writeMoveImage(t, env.path("src", from), 741)
	writeMoveImage(t, env.path("src", to), 149)
	setMoveEXIF(t, env.path("src", from), 6)
	env.index("src", "/A/", false)
	env.index("src", "/B/", false)
	oldSource := seedMoveImageMetadata(t, env, from, 6)
	oldTarget := seedMoveImageMetadata(t, env, to, 1)
	images := make(map[string][]byte)
	for _, size := range []string{"small", "large"} {
		images[size] = readMovePreview(t, env, server, from, size)
		other := readMovePreview(t, env, server, to, size)
		require.NotEqual(t, sha256.Sum256(images[size]), sha256.Sum256(other))
		if size == "large" {
			rotated, err := jpeg.DecodeConfig(bytes.NewReader(images[size]))
			require.NoError(t, err)
			normal, err := jpeg.DecodeConfig(bytes.NewReader(other))
			require.NoError(t, err)
			require.Equal(t, [2]int{480, 640}, [2]int{rotated.Width, rotated.Height})
			require.Equal(t, [2]int{640, 480}, [2]int{normal.Width, normal.Height})
		}
	}
	baseline := preview.GetService().Stats().FullDecodes
	result := env.patch("rename", []map[string]string{moveItem("src", from, "src", to)})
	require.Equal(t, http.StatusOK, result.Status)
	require.Equal(t, 1, result.Succeeded)
	for size, expected := range images {
		require.Equal(t, expected, readMovePreview(t, env, server, to, size))
	}
	require.Equal(t, baseline, preview.GetService().Stats().FullDecodes)
	env.waitIdle()
	rows, err := env.db.GetDirectoryChildren("src", "/B/")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	meta := rows[0].Image
	require.NotNil(t, meta)
	require.True(t, meta.Ready(iteminfo.ImageParserRevision, rows[0].DisplayAspectRatio))
	require.Equal(t, 0.75, rows[0].DisplayAspectRatio)
	require.Equal(t, [2]int{800, 600}, [2]int{meta.Width, meta.Height})
	require.Equal(t, uint8(6), meta.Orientation)
	require.NotEqual(t, oldSource.Version, meta.Version)
	require.NotEqual(t, oldTarget.Version, meta.Version)
	require.NotEqual(t, oldTarget.Guard, meta.Guard)
	page, err := env.db.GetDirectoryListingPage(t.Context(), "src", "/B/", indexsql.DirectoryListingQuery{Limit: 10, Sort: indexsql.ListingSortName, Order: indexsql.ListingOrderAsc})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, 0.75, page.Items[0].DisplayAspectRatio)
	for _, stale := range []indexsql.ImageWork{oldSource, oldTarget} {
		ok, err := env.db.CommitImageMetadata("src", stale.Path, stale.Version, stale.Guard, indexsql.ImageMetadataCommit{
			State: iteminfo.ImageMetaReady, Width: 800, Height: 600, Orientation: 1, Ratio: 4.0 / 3.0,
		})
		require.NoError(t, err)
		require.False(t, ok)
	}
}

func TestLedgerDirectoryMoveChainHTTP(t *testing.T) {
	env := newMoveIndexEnv(t, "src")
	server := ledgerPreviewServer(t, env)
	for _, dir := range []string{"a/nested", "ab", "drop"} {
		env.mkdir("src", dir)
	}
	originals := map[string][]byte{}
	for i, path := range []string{"/a/one.jpg", "/a/nested/two.jpg", "/ab/one.jpg"} {
		writeMoveImage(t, env.path("src", path), int64(i+760))
		originals[path] = readMovePreview(t, env, server, path, "small")
	}
	env.index("src", "/a/", true)
	seedMoveImageMetadata(t, env, "/a/one.jpg", 1)
	seedMoveImageMetadata(t, env, "/a/nested/two.jpg", 1)
	before := preview.GetService().Stats().FullDecodes
	require.Equal(t, 1, env.patch("move", []map[string]string{moveItem("src", "/a", "src", "/drop/b")}).Succeeded)
	env.waitIdle()
	for _, dir := range []string{"/drop/b/", "/drop/b/nested/"} {
		rows, err := env.db.GetDirectoryChildren("src", dir)
		require.NoError(t, err)
		var ready int
		for _, row := range rows {
			if row.Image.Ready(iteminfo.ImageParserRevision, row.DisplayAspectRatio) {
				ready++
			}
		}
		require.Equal(t, 1, ready, dir)
	}
	require.Equal(t, 1, env.patch("rename", []map[string]string{moveItem("src", "/drop/b", "src", "/z")}).Succeeded)
	env.waitIdle()
	for path, from := range map[string]string{"/z/one.jpg": "/a/one.jpg", "/z/nested/two.jpg": "/a/nested/two.jpg", "/ab/one.jpg": "/ab/one.jpg"} {
		require.Equal(t, originals[from], readMovePreview(t, env, server, path, "small"))
	}
	require.Equal(t, before, preview.GetService().Stats().FullDecodes)
	require.NoError(t, os.MkdirAll(filepath.Dir(env.path("src", "/a/one.jpg")), 0o755))
	writeMoveImage(t, env.path("src", "/a/one.jpg"), 77836)
	require.NotEqual(t, originals["/a/one.jpg"], readMovePreview(t, env, server, "/a/one.jpg", "small"))
	require.Equal(t, before+1, preview.GetService().Stats().FullDecodes)
}

func TestLedgerLockedDatabaseDoesNotFailMove(t *testing.T) {
	env := newMoveIndexEnv(t, "src")
	server := ledgerPreviewServer(t, env)
	env.mkdir("src", "A")
	env.mkdir("src", "B")
	from, to := "/A/source.jpg", "/B/target.jpg"
	writeMoveImage(t, env.path("src", from), 222)
	source := readMovePreview(t, env, server, from, "small")
	ledgerPath := filepath.Join(env.root, "previews", "image-preview-ledger.sqlite")
	db, err := sql.Open(indexsql.SqliteDriver, ledgerPath)
	require.NoError(t, err)
	defer db.Close()
	conn, err := db.Conn(t.Context())
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(t.Context(), "BEGIN IMMEDIATE")
	require.NoError(t, err)
	result := env.patch("move", []map[string]string{moveItem("src", from, "src", to)})
	_, err = conn.ExecContext(t.Context(), "ROLLBACK")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, result.Status)
	require.Equal(t, 1, result.Succeeded)
	require.False(t, env.exists("src", from))
	rereadBefore := preview.GetService().Stats().FullDecodes
	require.Equal(t, source, readMovePreview(t, env, server, to, "small"))
	require.Equal(t, rereadBefore+1, preview.GetService().Stats().FullDecodes)
	require.Equal(t, source, readMovePreview(t, env, server, to, "small"))
	require.Equal(t, rereadBefore+1, preview.GetService().Stats().FullDecodes)
}

func TestLedgerCopyFallbackDoesNotTransfer(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("rename must fail for an unprivileged user")
	}
	env := newMoveIndexEnv(t, "src")
	server := ledgerPreviewServer(t, env)
	env.mkdir("src", "A")
	env.mkdir("src", "B")
	from, to := "/A/source.jpg", "/B/target.jpg"
	sourcePath, targetPath := env.path("src", from), env.path("src", to)
	writeMoveImage(t, sourcePath, 472)
	writeMoveImage(t, targetPath, 882)
	targetInfo, err := os.Stat(targetPath)
	require.NoError(t, err)
	source := readMovePreview(t, env, server, from, "small")
	require.NotEqual(t, source, readMovePreview(t, env, server, to, "small"))
	parent := filepath.Dir(targetPath)
	require.NoError(t, os.Chmod(parent, 0o555))
	t.Cleanup(func() { require.NoError(t, os.Chmod(parent, 0o755)) })
	// The destination file can be truncated; its directory cannot be renamed
	// into. This exercises the same-device fallback through the real handler.
	result := env.patch("move", []map[string]string{moveItem("src", from, "src", to)})
	require.Equal(t, http.StatusOK, result.Status)
	require.Equal(t, 1, result.Succeeded)
	after, err := os.Stat(targetPath)
	require.NoError(t, err)
	require.True(t, os.SameFile(targetInfo, after))
	rereadBefore := preview.GetService().Stats().FullDecodes
	require.Equal(t, source, readMovePreview(t, env, server, to, "small"))
	require.Equal(t, rereadBefore+1, preview.GetService().Stats().FullDecodes)
}
