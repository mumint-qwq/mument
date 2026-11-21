//go:build linux && !386 && !arm && !cacheledger_portable

package previewwarmup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	indexsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// This is a real multi-worker/process lifetime exercise, with explicit synthetic
// pressure and eligibility injection. It is not a Docker/cgroup or phone test.
func TestContentMixedBackground(t *testing.T) {
	raw := os.Getenv("MUMENT_REUSE_MIXED_DURATION")
	if raw == "" {
		t.Skip("isolated long mixed scenario")
	}
	duration, err := time.ParseDuration(raw)
	require.NoError(t, err)
	require.GreaterOrEqual(t, duration, 90*time.Second)
	require.LessOrEqual(t, duration, 35*time.Minute)
	db, _, base := warmupTestIndex(t)
	a, b, saved := filepath.Join(base, "a"), filepath.Join(base, "b"), filepath.Join(base, "saved-b")
	for _, p := range []string{a, b, saved} {
		require.NoError(t, os.Mkdir(p, 0700))
	}
	for _, p := range []string{a, b} {
		require.NoError(t, unix.Mount("tmpfs", p, "tmpfs", 0, "size=128m"))
	}
	require.NoError(t, unix.Mount(b, saved, "", unix.MS_BIND, ""))
	t.Cleanup(func() {
		_ = unix.Unmount(b, unix.MNT_DETACH)
		_ = unix.Unmount(saved, unix.MNT_DETACH)
		_ = unix.Unmount(a, unix.MNT_DETACH)
	})
	mountID := func(path string) uint64 {
		var st unix.Statx_t
		for _, mask := range []int{unix.STATX_MNT_ID_UNIQUE, unix.STATX_MNT_ID} {
			if e := unix.Statx(unix.AT_FDCWD, path, 0, mask, &st); e == nil && st.Mask&uint32(mask) != 0 {
				return st.Mnt_id
			}
		}
		t.Fatal("mount identity unavailable")
		return 0
	}
	beforeMount := mountID(b)
	require.NotEqual(t, mountID(a), beforeMount)
	idxA := indexing.SetTestIndexWithDB("mixed-a", a, db)
	idxB := indexing.SetTestIndexWithDB("mixed-b", b, db)
	previous := settings.Config.Server
	t.Cleanup(func() { settings.Config.Server = previous })
	settings.Config.Server.NameToSource = map[string]*settings.Source{"mixed-a": &idxA.Source, "mixed-b": &idxB.Source}
	settings.Config.Server.PreviewOrphanReclamation = true
	settings.Config.Server.PreviewOrphanGraceHours = 24
	settings.Config.Server.PreviewSpace = settings.PreviewSpaceConfig{Mode: "observe", BudgetMiB: 64, MinimumFreeMiB: 1, ResumeFreeMiB: 2}
	cache := t.TempDir()
	require.NoError(t, preview.StartPreviewGeneratorWithBudget(1, 128, cache))
	svc := preview.GetService()
	t.Cleanup(func() { require.NoError(t, preview.Shutdown(context.Background())) })
	require.NoError(t, svc.ContentCacheReady(t.Context()))
	require.NoError(t, db.PreparePreviewWarmup(t.Context()))
	ledgerDB, err := sql.Open(indexsql.SqliteDriver, filepath.Join(cache, "image-preview-ledger.sqlite"))
	require.NoError(t, err)
	ledgerDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, ledgerDB.Close()) })
	_, err = ledgerDB.Exec("PRAGMA busy_timeout=1000")
	require.NoError(t, err)
	info := func(source, path string) iteminfo.ExtendedFileInfo {
		return iteminfo.ExtendedFileInfo{Source: source, RealPath: path, FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: filepath.Base(path), Type: "image/jpeg", HasPreview: true}}}
	}
	photos := make([]string, 8)
	var expected [32]byte
	for i := range photos {
		photos[i] = filepath.Join(a, fmt.Sprintf("source-%d.jpg", i))
		makePhoto(t, photos[i], 1600, 1200)
		data, e := preview.GetPreviewForFile(t.Context(), info("mixed-a", photos[i]), "small", "", 0)
		require.NoError(t, e)
		if i == 0 {
			expected = sha256.Sum256(data)
		} else {
			require.Equal(t, expected, sha256.Sum256(data))
		}
	}
	require.NoError(t, idxA.RefreshDirectory("/", false))
	require.NoError(t, idxB.RefreshDirectory("/", false))
	makeScope := func(source, root string) scope {
		return scope{config: settings.PreviewWarmupScope{Source: source, Directory: ".", MountPoint: "."}, sourceRoot: root, root: root, prefix: "/"}
	}
	fProbe := fakePressure(t, cache)
	dProbe := fakePressure(t, cache)
	f := &ReuseWorker{db: db, svc: svc, repo: svc.ContentLedger(), probe: fProbe, done: make(chan struct{}), scopes: []scope{makeScope("mixed-a", a), makeScope("mixed-b", b)}, status: ReuseStatus{Enabled: true, Counts: map[string]uint64{}}}
	d := &Worker{db: db, svc: svc, probe: dProbe, done: make(chan struct{}), scopes: []scope{makeScope("mixed-a", a)}, status: Status{Enabled: true, Paused: true, Scopes: []ScopeStatus{{Scope: 1}}}}
	svc.EnableWarmupHints()
	ctx, cancel := context.WithCancel(t.Context())
	go f.run(ctx)
	go d.run(ctx)
	t.Cleanup(func() {
		cancel()
		drain, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		require.NoError(t, f.Wait(drain))
		require.NoError(t, d.Wait(drain))
	})
	require.Eventually(t, func() bool { return f.Snapshot().Counts["proven_own"] >= 8 }, 20*time.Second, 100*time.Millisecond, "initial proof status=%+v", f.Snapshot())
	target := filepath.Join(b, "copy-00.jpg")
	require.ErrorIs(t, os.Rename(photos[0], target), unix.EXDEV)
	require.NoError(t, fileutils.CopyFile(photos[0], target))
	require.NoError(t, idxB.RefreshDirectory("/", false))
	require.Eventually(t, func() bool { return f.Snapshot().Counts["reused"] >= 1 }, 10*time.Second, 100*time.Millisecond)
	require.NoError(t, d.Control("resume", 0))
	start := time.Now()
	deadline := start.Add(duration)
	lastReport := start
	cycles, copies, renames, orphans, pressureRounds, mountRounds := 0, 1, 0, 0, 0, 0
	requests := 0
	var requestTotal, timeMax time.Duration
	baseGoroutines := runtime.NumGoroutine()
	peakGoroutines := baseGoroutines
	var peakHeap uint64
	sleep := func(delay time.Duration) {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-timer.C:
		}
	}
	injectPressure := func() {
		set := func(probe *resourceProbe, value string) {
			name := filepath.Join(probe.cgroups[0], "memory.current")
			require.NoError(t, os.WriteFile(name+".next", []byte(value), 0600))
			require.NoError(t, os.Rename(name+".next", name))
		}
		set(&f.probe, "1073741824")
		set(&d.probe, "1073741824")
		sleep(2500 * time.Millisecond)
		fs := f.Snapshot()
		require.Positive(t, fs.Counts["memory_pressure"])
		pressureRounds++
		set(&f.probe, "104857600")
		set(&d.probe, "104857600")
	}
	confirmedOrphan := func() {
		path := filepath.Join(a, fmt.Sprintf("orphan-%05d.jpg", orphans))
		makePhoto(t, path, 800, 600)
		require.NoError(t, idxA.RefreshDirectory("/", false))
		_, e := preview.GetPreviewForFile(t.Context(), info("mixed-a", path), "small", "", 0)
		require.NoError(t, e)
		unlock, e := f.repo.LockLocations(t.Context())
		require.NoError(t, e)
		mutation := preview.BeginOrphanMutation(t.Context(), f.repo, "mixed-a", a, path, "delete")
		require.NotNil(t, mutation)
		require.NoError(t, os.Remove(path))
		require.True(t, mutation.ProvesUnlinked(false))
		mutation.Finish(t.Context(), true, "")
		unlock()
		// Explicit isolated eligibility injection, rather than pretending to wait a
		// real grace day. All roots/cache/ledger are synthetic private fixtures.
		now := time.Now()
		_, e = ledgerDB.Exec(`UPDATE orphan_candidates SET since=?,eligible_at=?`, now.Add(-48*time.Hour).UnixNano(), now.Add(-24*time.Hour).UnixNano())
		require.NoError(t, e)
		require.NoError(t, idxA.RefreshDirectory("/", false))
		orphans++
	}
	for time.Now().Before(deadline) {
		burst := time.Now().Add(1500 * time.Millisecond)
		for j := 0; time.Now().Before(burst); j++ {
			path := photos[(cycles+j)%len(photos)]
			requestStart := time.Now()
			data, e := preview.GetPreviewForFile(t.Context(), info("mixed-a", path), "small", "", 0)
			elapsed := time.Since(requestStart)
			requests++
			requestTotal += elapsed
			timeMax = max(timeMax, elapsed)
			require.NoError(t, e)
			require.Equal(t, expected, sha256.Sum256(data))
			sleep(100 * time.Millisecond)
		}
		slot := copies % 8
		dest := filepath.Join(b, fmt.Sprintf("copy-%02d.jpg", slot))
		require.NoError(t, fileutils.CopyFile(photos[copies%8], dest))
		require.NoError(t, idxB.RefreshDirectory("/", false))
		copies++
		if cycles%3 == 0 {
			which := (cycles / 3) % 8
			next := filepath.Join(a, fmt.Sprintf("renamed-%02d-%05d.jpg", which, renames))
			require.NoError(t, os.Rename(photos[which], next))
			photos[which] = next
			renames++
			require.NoError(t, idxA.RefreshDirectory("/", false))
		}
		if cycles%6 == 0 {
			confirmedOrphan()
		}
		if cycles%12 == 0 {
			injectPressure()
		}
		if cycles%18 == 0 {
			require.NoError(t, unix.Unmount(b, unix.MNT_DETACH))
			sleep(1100 * time.Millisecond)
			// A real wrong-volume mount must not be adopted as the old gallery.
			require.NoError(t, unix.Mount("tmpfs", b, "tmpfs", 0, "size=16m"))
			makePhoto(t, filepath.Join(b, "wrong.jpg"), 800, 600)
			beforeOffline := f.Snapshot().Counts["scope_offline"]
			require.Eventually(t, func() bool { return f.Snapshot().Counts["scope_offline"] > beforeOffline }, 10*time.Second, 100*time.Millisecond)
			require.NoError(t, unix.Unmount(b, unix.MNT_DETACH))
			require.NoError(t, unix.Mount(saved, b, "", unix.MS_BIND, ""))
			require.NoError(t, idxB.RefreshDirectory("/", false))
			mountRounds++
			require.NotEqual(t, beforeMount, mountID(b))
			t.Logf("remount=%d initial_mount=%d current_mount=%d", mountRounds, beforeMount, mountID(b))
		}
		sleep(2500 * time.Millisecond)
		cycles++
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		peakHeap = max(peakHeap, mem.Alloc)
		peakGoroutines = max(peakGoroutines, runtime.NumGoroutine())
		if time.Since(lastReport) >= time.Minute {
			t.Logf("mixed elapsed=%s cycles=%d copies=%d renames=%d confirmed_orphans=%d pressure=%d remounts=%d F=%+v D=%+v", time.Since(start), cycles, copies, renames, orphans, pressureRounds, mountRounds, f.Snapshot(), d.Snapshot())
			lastReport = time.Now()
		}
	}
	cancel()
	drain, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	require.NoError(t, f.Wait(drain))
	require.NoError(t, d.Wait(drain))
	var removed, pending int
	require.NoError(t, ledgerDB.QueryRow(`SELECT count(*) FROM orphan_audit WHERE reason='unlinked'`).Scan(&removed))
	require.NoError(t, ledgerDB.QueryRow(`SELECT count(*) FROM orphan_candidates`).Scan(&pending))
	require.LessOrEqual(t, pending, 4)
	audit, err := f.repo.OrphanAudit(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, audit, "real E2 worker must have completed a bounded reclaim window")
	require.Positive(t, d.Snapshot().Generated)
	require.Positive(t, f.Snapshot().Counts["reused"])
	require.Positive(t, f.Snapshot().Counts["scope_offline"])
	require.Less(t, peakGoroutines, baseGoroutines+64)
	t.Logf("foreground requests=%d mean_us=%d max_ms=%d reclaimed=%d remaining_candidates=%d", requests, requestTotal.Microseconds()/int64(max(requests, 1)), timeMax.Milliseconds(), removed, pending)
	t.Logf("mixed_final requested=%s elapsed=%s cycles=%d copies=%d renames=%d confirmed_orphans=%d removals_sample=%d pressure_rounds=%d remounts=%d peak_goroutines=%d baseline_goroutines=%d peak_heap_mib=%d F=%+v D=%+v preview=%+v", duration, time.Since(start), cycles, copies, renames, orphans, len(audit), pressureRounds, mountRounds, peakGoroutines, baseGoroutines, peakHeap>>20, f.Snapshot(), d.Snapshot(), svc.Stats())
}
