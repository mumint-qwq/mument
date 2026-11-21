//go:build linux && !386 && !arm && !cacheledger_portable

package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
	"image"
	"image/jpeg"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Core protocol scale comparison, intentionally unpaced. This is not a claim
// about production-worker throughput, real HTTP latency or browser refreshes.
func TestContentCrossMountScale(t *testing.T) {
	if os.Getenv("MUMENT_REUSE_SCALE") != "1" {
		t.Skip("isolated 1000-file private-mount benchmark")
	}
	const count = 1000
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, p := range []string{a, b} {
		require.NoError(t, os.Mkdir(p, 0700))
		require.NoError(t, unix.Mount("tmpfs", p, "tmpfs", 0, "size=1g"))
		defer unix.Unmount(p, 0)
	}
	var ma, mb unix.Statx_t
	require.NoError(t, unix.Statx(unix.AT_FDCWD, a, 0, unix.STATX_MNT_ID, &ma))
	require.NoError(t, unix.Statx(unix.AT_FDCWD, b, 0, unix.STATX_MNT_ID, &mb))
	require.NotEqual(t, ma.Mnt_id, mb.Mnt_id)
	previous := settings.Config.Server
	prevFile, prevDir := fileutils.PermFile, fileutils.PermDir
	defer func() {
		settings.Config.Server = previous
		fileutils.SetFsPermissions(uint32(prevFile), uint32(prevDir))
	}()
	fileutils.SetFsPermissions(0600, 0700)
	contentScaleConfigure(a, b)
	indexing.SetTestIndex("scale-a", a)
	indexing.SetTestIndex("scale-b", b)
	defer indexing.ClearTestIndices()
	require.NoError(t, preview.StartPreviewGeneratorWithBudget(1, 128, t.TempDir()))
	defer preview.Shutdown(context.Background())
	svc := preview.GetService()
	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	_, err := rand.New(rand.NewSource(42)).Read(img.Pix)
	require.NoError(t, err)
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var encoded bytes.Buffer
	require.NoError(t, jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 92}))
	require.Greater(t, encoded.Len(), 256<<10)
	files := make([]iteminfo.ExtendedFileInfo, count)
	moves := make([]ResourceMove, count)
	digests := make([][32]byte, count)
	start := time.Now()
	for i := range files {
		name := fmt.Sprintf("photo-%04d.jpg", i)
		path := filepath.Join(a, name)
		require.NoError(t, os.WriteFile(path, encoded.Bytes(), 0600))
		files[i] = iteminfo.ExtendedFileInfo{Source: "scale-a", RealPath: path, FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: name, Type: "image/jpeg", HasPreview: true}}}
		data, e := preview.GetPreviewForFile(t.Context(), files[i], "small", "", 0)
		require.NoError(t, e)
		digests[i] = sha256.Sum256(data)
		moves[i] = ResourceMove{Source: "scale-a", Target: "scale-b", From: path, To: filepath.Join(b, name)}
	}
	warm := svc.Stats().FullDecodes
	require.EqualValues(t, count, warm)
	warmMS := time.Since(start).Milliseconds()
	start = time.Now()
	var sourceBytes int64
	for _, file := range files {
		sourceBytes += contentScaleProve(t, svc, file)
	}
	sourceHashMS := time.Since(start).Milliseconds()
	require.ErrorIs(t, os.Rename(moves[0].From, moves[0].To), unix.EXDEV)
	batch := NewIndexImpactBatch()
	start = time.Now()
	for from := 0; from < count; from += MoveGroupLimit {
		for _, e := range (&Service{}).moveResources(batch, moves[from:min(from+MoveGroupLimit, count)]) {
			require.NoError(t, e)
		}
	}
	moveMS := time.Since(start).Milliseconds()
	require.Equal(t, 2, batch.Len())
	require.Equal(t, warm, svc.Stats().FullDecodes)
	start = time.Now()
	var targetBytes int64
	for i := range files {
		files[i].Source = "scale-b"
		files[i].RealPath = moves[i].To
		targetBytes += contentScaleProve(t, svc, files[i])
	}
	targetHashMS := time.Since(start).Milliseconds()
	start = time.Now()
	for i, file := range files {
		data, e := preview.GetPreviewForFile(t.Context(), file, "small", "", 0)
		require.NoError(t, e)
		require.Equal(t, digests[i], sha256.Sum256(data))
	}
	rereadMS := time.Since(start).Milliseconds()
	retained, proofAssets := contentScaleRetention(t, svc)
	if contentScaleIsF {
		require.Equal(t, count, retained)
		require.Equal(t, count, proofAssets)
	}
	delta := svc.Stats().FullDecodes - warm
	if contentScaleIsF {
		require.Zero(t, delta)
		require.Equal(t, sourceBytes, targetBytes)
		require.Positive(t, targetBytes)
	} else {
		require.EqualValues(t, count, delta)
	}
	t.Logf("core_scale f=%t images=%d mounts=%d,%d warm_ms=%d move_ms=%d source_hash_ms=%d source_bytes=%d target_hash_ms=%d target_bytes=%d reread_ms=%d decode_delta=%d directory_intents=%d retained_origin_refs=%d proven_ready_assets=%d", contentScaleIsF, count, ma.Mnt_id, mb.Mnt_id, warmMS, moveMS, sourceHashMS, sourceBytes, targetHashMS, targetBytes, rereadMS, delta, batch.Len(), retained, proofAssets)
}
