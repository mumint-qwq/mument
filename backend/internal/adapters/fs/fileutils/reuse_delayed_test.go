//go:build linux && !386 && !arm && !cacheledger_portable

package fileutils_test

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestContentRealEXDEVDelayedSourceRemoval(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%t", fail), func(t *testing.T) { testDelayedRemoval(t, fail) })
	}
}
func testDelayedRemoval(t *testing.T, fail bool) {
	if os.Getenv("MUMENT_REUSE_MOUNT_TEST") != "1" {
		t.Skip("private mount namespace required")
	}
	base := t.TempDir()
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	for _, path := range []string{a, b} {
		require.NoError(t, os.Mkdir(path, 0700))
		require.NoError(t, unix.Mount("tmpfs", path, "tmpfs", 0, "size=32m"))
		defer unix.Unmount(path, 0)
	}
	oldFile, oldDir := fileutils.PermFile, fileutils.PermDir
	fileutils.SetFsPermissions(0600, 0700)
	defer fileutils.SetFsPermissions(uint32(oldFile), uint32(oldDir))
	prev := settings.Config.Server
	defer func() { settings.Config.Server = prev }()
	settings.Config.Server.PreviewReuse = settings.PreviewWarmupConfig{Enabled: true, Scopes: []settings.PreviewWarmupScope{{Source: "delay-a", Directory: "."}, {Source: "delay-b", Directory: "."}}}
	settings.Config.Server.NameToSource = map[string]*settings.Source{"delay-a": {Name: "delay-a", Path: a}, "delay-b": {Name: "delay-b", Path: b}}
	indexing.SetTestIndex("delay-a", a)
	indexing.SetTestIndex("delay-b", b)
	defer indexing.ClearTestIndices()
	source, target := filepath.Join(a, "photo.jpg"), filepath.Join(b, "photo.jpg")
	img := image.NewNRGBA(image.Rect(0, 0, 1600, 1200))
	_, err := rand.New(rand.NewSource(71)).Read(img.Pix)
	require.NoError(t, err)
	var encoded bytes.Buffer
	require.NoError(t, jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 90}))
	require.NoError(t, os.WriteFile(source, encoded.Bytes(), 0600))
	require.ErrorIs(t, os.Rename(source, target), unix.EXDEV)
	require.NoError(t, preview.StartPreviewGeneratorWithBudget(1, 128, t.TempDir()))
	defer preview.Shutdown(context.Background())
	svc := preview.GetService()
	info := func(src, path string) iteminfo.ExtendedFileInfo {
		return iteminfo.ExtendedFileInfo{Source: src, RealPath: path, FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: "photo.jpg", Type: "image/jpeg", HasPreview: true}}}
	}
	prove := func(src, path string) preview.ContentOutcome {
		out, e := svc.ReuseContent(t.Context(), info(src, path), func(context.Context) (*os.File, error) { return os.Open(path) }, func(context.Context) (os.FileInfo, error) { return os.Stat(path) }, nil)
		require.NoError(t, e)
		return out
	}
	expected := map[string][]byte{}
	for _, size := range []string{"small", "xlarge"} {
		data, e := preview.GetPreviewForFile(t.Context(), info("delay-a", source), size, "", 0)
		require.NoError(t, e)
		expected[size] = data
	}
	require.Equal(t, "proven_own", prove("delay-a", source).Reason)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	restore := fileutils.HoldMoveRemovalForTest(entered, release, done, fail)
	released := false
	defer func() {
		if !released {
			close(release)
		}
		<-done
		restore()
	}()
	require.NoError(t, fileutils.MoveByCopy(source, target))
	<-entered
	preview.HintCopiedContent("delay-b", target, true)
	require.FileExists(t, source)
	require.FileExists(t, target)
	before := svc.Stats().FullDecodes
	require.Equal(t, "reused", prove("delay-b", target).Reason)
	// Both current physical locations remain independently readable while the
	// original MoveByCopy goroutine is paused before its own removal call.
	for _, src := range []struct{ name, path string }{{"delay-a", source}, {"delay-b", target}} {
		for size, want := range expected {
			data, e := preview.GetPreviewForFile(t.Context(), info(src.name, src.path), size, "", 0)
			require.NoError(t, e)
			require.Equal(t, want, data)
		}
	}
	require.Equal(t, before, svc.Stats().FullDecodes)
	close(release)
	released = true
	<-done
	if fail {
		require.FileExists(t, source)
	} else {
		require.NoFileExists(t, source)
	}
	for size, want := range expected {
		data, e := preview.GetPreviewForFile(t.Context(), info("delay-b", target), size, "", 0)
		require.NoError(t, e)
		require.Equal(t, want, data)
	}
	require.Equal(t, before, svc.Stats().FullDecodes)
	t.Log("actual EXDEV, copy returned while original deletion held; both paths reused small/xlarge with zero decode increment; target survived original removal")
}
