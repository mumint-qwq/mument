//go:build linux && !386 && !arm

package previewwarmup

import (
	"context"
	"image"
	"image/jpeg"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	indexsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
)

func warmupTestIndex(t *testing.T) (*indexsql.IndexDB, *indexing.Index, string) {
	t.Helper()
	previousFile, previousDir := fileutils.PermFile, fileutils.PermDir
	fileutils.SetFsPermissions(0o644, 0o755)
	t.Cleanup(func() { fileutils.SetFsPermissions(uint32(previousFile), uint32(previousDir)) })
	root := t.TempDir()
	cache := settings.Config.Server.CacheDir
	settings.Config.Server.CacheDir = t.TempDir()
	t.Cleanup(func() { settings.Config.Server.CacheDir = cache })
	db, _, err := indexsql.NewIndexDB("warmup_worker", "WAL", 100, 8, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	idx := indexing.SetTestIndexWithDB("library", root, db)
	t.Cleanup(indexing.ClearTestIndices)
	for _, dir := range []string{"Pictures", "Pictures-old", "Other"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, dir), 0o755))
	}
	return db, idx, root
}

func makePhoto(t *testing.T, name string, width, height int) {
	t.Helper()
	file, err := os.Create(name)
	require.NoError(t, err)
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	_, err = rand.New(rand.NewSource(7)).Read(img.Pix)
	require.NoError(t, err)
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	require.NoError(t, jpeg.Encode(file, img, &jpeg.Options{Quality: 90}))
	require.NoError(t, file.Close())
	past := time.Now().Add(-time.Minute)
	require.NoError(t, os.Chtimes(name, past, past))
}

func fakePressure(t *testing.T, cache string) resourceProbe {
	t.Helper()
	fake := t.TempDir()
	for file, contents := range map[string]string{
		"memory.max": "1073741824", "memory.current": "104857600",
		"cpu.stat": "nr_throttled 0\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(fake, file), []byte(contents), 0o600))
	}
	probe := newResourceProbe(cache)
	probe.cgroups = []string{fake}
	probe.minFree = 1
	return probe
}

func TestWarmupGuardsDirectoriesAndVersions(t *testing.T) {
	db, idx, root := warmupTestIndex(t)
	photo := filepath.Join(root, "Pictures", "a.jpg")
	makePhoto(t, photo, 64, 64)
	require.NoError(t, idx.RefreshDirectory("/Pictures/", false))
	page, err := db.GetImageWorksByPaths("library", []string{"/Pictures/a.jpg"})
	require.NoError(t, err)
	c := page["/Pictures/a.jpg"]
	require.NotEmpty(t, c.Version)
	test := scope{config: settings.PreviewWarmupScope{Source: "library", Directory: "Pictures"},
		sourceRoot: root, root: filepath.Dir(photo), prefix: "/Pictures/"}
	claimed, err := test.online()
	require.NoError(t, err)
	g := guardedFile{scope: &test, relative: "a.jpg", version: c.Version, claimedIdentity: claimed}
	f, err := g.open(t.Context())
	require.NoError(t, err)
	defer f.Close()

	other := filepath.Join(root, "Pictures-old", "a.jpg")
	makePhoto(t, other, 64, 64)
	require.NoError(t, os.Remove(photo))
	require.NoError(t, os.Symlink(other, photo))
	_, err = g.open(t.Context())
	require.ErrorIs(t, err, preview.ErrPreviewSourceChanged)
	_, err = g.stat(t.Context())
	require.ErrorIs(t, err, preview.ErrPreviewSourceChanged)

	moved := filepath.Join(root, "moved")
	require.NoError(t, os.Rename(test.root, moved))
	require.NoError(t, os.Mkdir(test.root, 0o755))
	_, err = test.online()
	require.ErrorIs(t, err, errScopeOffline)
	require.NoError(t, os.Remove(test.root))
	require.NoError(t, os.Rename(moved, test.root))
	_, err = test.online()
	require.NoError(t, err)

	test.config.MountPoint = "."
	_, err = test.online()
	require.ErrorIs(t, err, errScopeOffline, "the configured mount point cannot be an ordinary directory")
	test.config.MountPoint = ""
	g.claimedIdentity.directory.mount++
	_, err = g.open(t.Context())
	require.ErrorIs(t, err, errScopeOffline, "an in-flight claim must use its original mount")

	unsafe := guardedFile{scope: &test, relative: "../Pictures-old/a.jpg", version: c.Version, claimedIdentity: claimed}
	_, err = unsafe.open(t.Context())
	require.ErrorIs(t, err, preview.ErrPreviewSourceChanged)
}

func TestWarmupProcessesAndRecoversBoundedProgress(t *testing.T) {
	db, idx, root := warmupTestIndex(t)
	require.NoError(t, db.PreparePreviewWarmup(t.Context()))
	cache := t.TempDir()
	startService := func() *preview.Service {
		require.NoError(t, preview.StartPreviewGeneratorWithBudget(1, 64, cache))
		return preview.GetService()
	}
	t.Cleanup(func() { require.NoError(t, preview.Shutdown(context.Background())) })

	// A second eligible scope proceeds while the primary is unavailable.
	primary, secondary := filepath.Join(root, "Pictures"), filepath.Join(root, "Other")
	outside := filepath.Join(root, "Pictures-old")
	for _, dir := range []string{primary, secondary, outside} {
		makePhoto(t, filepath.Join(dir, "photo.jpg"), 1280, 1080)
		require.NoError(t, idx.RefreshDirectory("/"+filepath.Base(dir)+"/", false))
	}
	makePhoto(t, filepath.Join(secondary, "tiny.jpg"), 16, 16)
	bad := filepath.Join(secondary, "bad.jpg")
	require.NoError(t, os.WriteFile(bad, []byte{0xff, 0xd8, 0xff}, 0o600))
	past := time.Now().Add(-time.Minute)
	require.NoError(t, os.Chtimes(bad, past, past))
	require.NoError(t, idx.RefreshDirectory("/Other/", false))

	makeWorker := func(svc *preview.Service) *Worker {
		return &Worker{db: db, svc: svc, probe: fakePressure(t, cache),
			scopes: []scope{
				{config: settings.PreviewWarmupScope{Source: "library", Directory: "Pictures"}, sourceRoot: root, root: primary, prefix: "/Pictures/"},
				{config: settings.PreviewWarmupScope{Source: "library", Directory: "Other"}, sourceRoot: root, root: secondary, prefix: "/Other/"},
			},
			status: Status{Scopes: []ScopeStatus{{Scope: 1}, {Scope: 2}}},
		}
	}
	w := makeWorker(startService())
	require.NoError(t, w.Control("pause", 0))
	require.True(t, w.Snapshot().Paused)
	require.ErrorIs(t, w.Control("fill", 3), ErrInvalidAction)
	require.NoError(t, w.Control("fill", 2))
	require.Equal(t, 2, w.Snapshot().ManualScope)
	require.NoError(t, w.Control("cancel", 0))
	require.Zero(t, w.Snapshot().ManualScope)
	require.NoError(t, w.Control("resume", 0))

	_, err := w.scopes[0].online()
	require.NoError(t, err)
	away := filepath.Join(root, "detached")
	require.NoError(t, os.Rename(primary, away))
	require.Equal(t, "scope_offline", w.step(t.Context(), 0, nil))
	w.probe.minFree = ^uint64(0)
	require.Equal(t, "low_cache_space", w.step(t.Context(), 1, nil))
	w.probe.minFree = 1
	require.NoError(t, os.WriteFile(filepath.Join(w.probe.cgroups[0], "memory.current"), []byte("1000000000"), 0o600))
	require.Equal(t, "memory_pressure", w.step(t.Context(), 1, nil))
	require.NoError(t, os.WriteFile(filepath.Join(w.probe.cgroups[0], "memory.current"), []byte("104857600"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(w.probe.cgroups[0], "cpu.stat"), []byte("nr_throttled 1\n"), 0o600))
	require.Equal(t, "cpu_throttled", w.step(t.Context(), 1, nil))
	for range 10 {
		w.step(t.Context(), 1, nil)
	}
	status := w.Snapshot()
	require.EqualValues(t, 1, status.Generated)
	require.EqualValues(t, 1, status.Original)
	require.EqualValues(t, 1, status.Retried)
	require.EqualValues(t, 1, status.FullDecodes)

	// Model publication preceding the durable progress update. The next
	// service will find the existing bytes and finish the index marker.
	_, err = db.Exec("UPDATE index_items SET warmup_attempts=0,warmup_guard=0 WHERE path='/Other/photo.jpg'")
	require.NoError(t, err)
	require.NoError(t, preview.Shutdown(t.Context()))
	w = makeWorker(startService())
	require.NoError(t, os.Rename(away, primary))
	for range 8 {
		w.step(t.Context(), 0, nil)
		w.step(t.Context(), 1, nil)
	}
	status = w.Snapshot()
	require.EqualValues(t, 1, status.Cached)
	require.EqualValues(t, 1, status.Generated)
	require.EqualValues(t, 1, status.FullDecodes)
	require.EqualValues(t, 0, status.Retried, "backoff survived restart")

	_, err = db.Exec("UPDATE index_items SET warmup_attempts=2,warmup_retry_at=1 WHERE path='/Other/bad.jpg'")
	require.NoError(t, err)
	for range 6 {
		w.step(t.Context(), 1, nil)
	}
	require.EqualValues(t, 1, w.Snapshot().Terminal)
	require.EqualValues(t, 0, w.Snapshot().Retried)
	var siblingAttempts int
	require.NoError(t, db.QueryRow("SELECT warmup_attempts FROM index_items WHERE path='/Pictures-old/photo.jpg'").Scan(&siblingAttempts))
	require.Zero(t, siblingAttempts)
}

func TestWarmupDisabledDoesNotPrepareIndex(t *testing.T) {
	db, _, _ := warmupTestIndex(t)
	w := New(t.Context(), settings.Server{}, db, nil)
	w.Wait()
	require.False(t, w.Snapshot().Enabled)
	require.Equal(t, "disabled", w.Snapshot().Reason)
	var columns int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('index_items') WHERE name LIKE 'warmup_%'").Scan(&columns))
	require.Zero(t, columns)
}
