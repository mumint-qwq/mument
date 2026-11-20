//go:build linux && !386 && !arm && !cacheledger_portable

package files

import (
	"context"
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

func TestContentRealEXDEVCopyHint(t *testing.T) {
	if os.Getenv("MUMENT_REUSE_MOUNT_TEST") != "1" {
		t.Skip("private mount namespace required")
	}
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, p := range []string{a, b} {
		require.NoError(t, os.Mkdir(p, 0700))
		require.NoError(t, unix.Mount("tmpfs", p, "tmpfs", 0, "size=32m"))
		defer unix.Unmount(p, 0)
	}
	var ma, mb unix.Statx_t
	require.NoError(t, unix.Statx(unix.AT_FDCWD, a, 0, unix.STATX_MNT_ID, &ma))
	require.NoError(t, unix.Statx(unix.AT_FDCWD, b, 0, unix.STATX_MNT_ID, &mb))
	require.NotEqual(t, ma.Mnt_id, mb.Mnt_id)
	source, target := filepath.Join(a, "photo.jpg"), filepath.Join(b, "photo.jpg")
	f, err := os.Create(source)
	require.NoError(t, err)
	img := image.NewRGBA(image.Rect(0, 0, 1600, 1200))
	_, err = rand.New(rand.NewSource(7)).Read(img.Pix)
	require.NoError(t, err)
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	require.NoError(t, jpeg.Encode(f, img, nil))
	require.NoError(t, f.Close())
	require.ErrorIs(t, os.Rename(source, target), unix.EXDEV)
	previous := settings.Config.Server
	prevFile, prevDir := fileutils.PermFile, fileutils.PermDir
	defer func() {
		settings.Config.Server = previous
		fileutils.SetFsPermissions(uint32(prevFile), uint32(prevDir))
	}()
	fileutils.SetFsPermissions(0600, 0700)
	settings.Config.Server.PreviewReuse = settings.PreviewWarmupConfig{Enabled: true, Scopes: []settings.PreviewWarmupScope{{Source: "reuse-a", Directory: "."}, {Source: "reuse-b", Directory: "."}}}
	settings.Config.Server.NameToSource = map[string]*settings.Source{"reuse-a": {Name: "reuse-a", Path: a}, "reuse-b": {Name: "reuse-b", Path: b}}
	indexing.SetTestIndex("reuse-a", a)
	indexing.SetTestIndex("reuse-b", b)
	defer indexing.ClearTestIndices()
	require.NoError(t, preview.StartPreviewGeneratorWithBudget(1, 128, t.TempDir()))
	defer preview.Shutdown(context.Background())
	svc := preview.GetService()
	sourceInfo := iteminfo.ExtendedFileInfo{Source: "reuse-a", RealPath: source, FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: "photo.jpg", Type: "image/jpeg", HasPreview: true}}}
	expected := map[string][]byte{}
	for _, size := range []string{"small", "xlarge"} {
		data, e := preview.GetPreviewForFile(t.Context(), sourceInfo, size, "", 0)
		require.NoError(t, e)
		expected[size] = data
	}
	proven, err := svc.ReuseContent(t.Context(), sourceInfo, func(context.Context) (*os.File, error) { return os.Open(source) }, func(context.Context) (os.FileInfo, error) { return os.Stat(source) }, nil)
	require.NoError(t, err)
	require.Equal(t, "proven_own", proven.Reason)
	before := svc.Stats().FullDecodes
	moves := []ResourceMove{{Source: "reuse-a", Target: "reuse-b", From: source, To: target}}
	batch := NewIndexImpactBatch()
	results := (&Service{}).moveResources(batch, moves)
	require.NoError(t, results[0])
	require.Equal(t, 2, batch.Len())

	hints, err := svc.ContentLedger().ContentHints(t.Context())
	require.NoError(t, err)
	require.Len(t, hints, 1)
	require.True(t, hints[0].EXDEV)
	targetInfo := sourceInfo
	targetInfo.Source = "reuse-b"
	targetInfo.RealPath = target
	out, err := svc.ReuseContent(t.Context(), targetInfo, func(context.Context) (*os.File, error) { return os.Open(target) }, func(context.Context) (os.FileInfo, error) { return os.Stat(target) }, nil)
	require.NoError(t, err)
	require.Equal(t, "reused", out.Reason)
	for _, size := range []string{"small", "xlarge"} {
		data, e := preview.GetPreviewForFile(t.Context(), targetInfo, size, "", 0)
		require.NoError(t, e)
		require.Equal(t, expected[size], data)
	}
	require.Equal(t, before, svc.Stats().FullDecodes)
	require.Eventually(t, func() bool { _, e := os.Stat(source); return os.IsNotExist(e) }, time.Second, time.Millisecond)
	t.Logf("mounts=%d,%d actual EXDEV fallback, persisted hint, two variants reused; complete decodes delta=0", ma.Mnt_id, mb.Mnt_id)
}
