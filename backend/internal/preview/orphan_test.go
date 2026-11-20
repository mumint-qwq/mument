//go:build linux && !386 && !arm && !cacheledger_portable

package preview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	dbsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

type orphanFixture struct {
	root, cache      string
	source, physical string
	ref              cacheledger.Ref
	variant          string
	key              string
	s                *Service
}

func startOrphanFixture(t *testing.T) *orphanFixture {
	t.Helper()
	return startOrphanFixtureBefore(t, nil)
}

func startOrphanFixtureBefore(t *testing.T, before func(root, cache string)) *orphanFixture {
	t.Helper()
	old := settings.Config.Server.PreviewOrphanReclamation
	settings.Config.Server.PreviewOrphanReclamation = false
	t.Cleanup(func() { settings.Config.Server.PreviewOrphanReclamation = old })
	root, cache := t.TempDir(), t.TempDir()
	indexing.SetTestIndex("orphan-test", root)
	t.Cleanup(indexing.ClearTestIndices)
	f := &orphanFixture{root: root, cache: cache}
	if before != nil {
		before(root, cache)
	}
	f.start(t)
	file := newNoisyFile(t, root, "source.jpg", 800, 600)
	file.Source = "orphan-test"
	data, err := f.s.getPreviewForFile(t.Context(), file, "small", "", 0)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	plan, err := f.s.planLedgerPreview(t.Context(), file, "small")
	require.NoError(t, err)
	f.source, f.key, f.ref, f.variant = file.RealPath, plan.key, plan.ref, plan.variant
	f.physical = filepath.Join(cache, "diskcache", diskcache.RelativePath(f.key))
	require.FileExists(t, f.physical)
	return f
}

func (f *orphanFixture) start(t *testing.T) {
	t.Helper()
	require.NoError(t, StartPreviewGeneratorWithBudget(1, 128, f.cache))
	f.s = service
	require.NotNil(t, f.s.orphans)
	s := f.s
	t.Cleanup(func() { require.NoError(t, s.Shutdown(context.Background())) })
}

func (f *orphanFixture) remove(t *testing.T) time.Time {
	t.Helper()
	return f.removePath(t, f.source)
}

func (f *orphanFixture) removePath(t *testing.T, path string) time.Time {
	t.Helper()
	unlock, err := f.s.ledger.LockLocations(t.Context())
	require.NoError(t, err)
	defer unlock()
	mutation := BeginOrphanMutation(t.Context(), f.s.ledger, "orphan-test", f.root, path, "delete")
	require.NotNil(t, mutation)
	require.NoError(t, os.Remove(path))
	require.True(t, mutation.ProvesUnlinked(false))
	mutation.Finish(t.Context(), true, "")
	events, err := f.s.ledger.OrphanEvents(t.Context())
	require.NoError(t, err)
	require.Equal(t, "confirmed", events[0].State)
	return time.Unix(0, events[0].Confirmed)
}

func (f *orphanFixture) step(t *testing.T, when time.Time, apply bool) OrphanReport {
	t.Helper()
	f.s.orphans.now = func() time.Time { return when }
	return f.s.orphans.report(t.Context(), OrphanCursor{}, apply)
}

func TestOrphanSingleDeleteDefaultAndGrace(t *testing.T) {
	f := startOrphanFixture(t)
	confirmed := f.remove(t)
	require.NoFileExists(t, f.source)

	early := f.step(t, confirmed.Add(orphanGrace-time.Nanosecond), false)
	require.Equal(t, 1, early.Waiting)
	require.Zero(t, early.Due)
	require.Equal(t, "delete", early.Samples[0].Evidence)

	rollback := f.step(t, confirmed.Add(-time.Nanosecond), false)
	require.Equal(t, 1, rollback.Reasons["clock_rollback"])
	require.Zero(t, rollback.Due)

	due := f.step(t, confirmed.Add(orphanGrace), true)
	require.False(t, due.Enabled)
	require.Equal(t, 1, due.Due)
	require.Positive(t, due.EstimatedBytes)
	require.Zero(t, due.Unlinked)
	require.FileExists(t, f.physical)
	require.NoDirExists(t, filepath.Join(f.cache, "diskcache", ".orphan-reclamation"))

	// The disabled loop has already finished. Enable only the synthetic
	// manager so its controlled clock can drive the same executor manually.
	<-f.s.orphans.done
	f.s.orphans.enabled = true
	done := f.step(t, confirmed.Add(orphanGrace), true)
	require.Equal(t, 1, done.Unlinked)
	require.Equal(t, due.EstimatedBytes, done.UnlinkedBytes)
	require.Equal(t, due.EstimatedBlocks, done.UnlinkedBlocks)
	require.NoFileExists(t, f.physical)
	status := f.step(t, confirmed.Add(orphanGrace), false)
	require.Len(t, status.RecentRemoval, 1)
	require.Equal(t, "unlinked", status.RecentRemoval[0].Reason)
	require.Equal(t, done.UnlinkedBytes, status.RecentRemoval[0].Logical)
	t.Logf("expiry trial %+v", due)
	t.Logf("reclaimed %+v", status.RecentRemoval[0])
	pubs, err := f.s.ledger.OrphanPublications(t.Context(), f.key)
	require.NoError(t, err)
	require.Empty(t, pubs)
}

func TestOrphanRecoverDurablePhases(t *testing.T) {
	for _, point := range []string{"reserved", "renamed", "isolated", "unlinked"} {
		t.Run(point, func(t *testing.T) {
			f := startOrphanFixture(t)
			due := f.remove(t).Add(orphanGrace)
			require.NoError(t, f.s.Shutdown(t.Context()))
			child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestOrphanCrashChild$")
			child.Env = append(os.Environ(), "MUMENT_ORPHAN_CRASH="+point,
				"MUMENT_ORPHAN_ROOT="+f.root, "MUMENT_ORPHAN_CACHE="+f.cache,
				"MUMENT_ORPHAN_NOW="+strconv.FormatInt(due.UnixNano(), 10))
			raw, err := child.CombinedOutput()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit, "%s", raw)
			require.Equal(t, 72, exit.ExitCode(), "%s", raw)
			f.start(t)
			<-f.s.orphans.done
			disabled := f.step(t, due, true)
			require.Zero(t, disabled.Unlinked)
			require.NotEmpty(t, disabled.Samples)
			f.s.orphans.enabled = true
			recovered := f.step(t, due, true)
			require.NoFileExists(t, f.physical)
			status := f.step(t, due, false)
			require.Len(t, status.RecentRemoval, 1)
			if point == "unlinked" {
				require.Zero(t, recovered.Unlinked)
				require.Zero(t, status.RecentRemoval[0].Logical)
				require.Equal(t, "missing_after_isolation", status.RecentRemoval[0].Reason)
			} else {
				require.Equal(t, 1, recovered.Unlinked, "%+v", recovered)
				require.Equal(t, "unlinked", status.RecentRemoval[0].Reason)
			}
		})
	}
}

func TestOrphanKeepsUnprovenAndReplacementBytes(t *testing.T) {
	f := startOrphanFixture(t)
	repo := f.s.ledger
	unlock, err := repo.LockLocations(t.Context())
	require.NoError(t, err)
	m := BeginOrphanMutation(t.Context(), repo, "orphan-test", f.root, f.source, "delete")
	require.NotNil(t, m)
	m.Finish(t.Context(), false, "")
	unlock()
	require.Zero(t, f.step(t, time.Now().Add(orphanGrace), false).Due)

	due := f.remove(t).Add(orphanGrace)
	<-f.s.orphans.done
	f.s.orphans.enabled = true
	f.s.orphans.checkpoint = func(string) error { return errors.New("stop after reserve") }
	require.Equal(t, 1, f.step(t, due, true).Reasons["interrupted"])
	f.s.orphans.checkpoint = nil
	newBytes := []byte("written without a matching ledger publication")
	require.NoError(t, f.s.fileCache.Store(t.Context(), f.key, newBytes))
	out := f.step(t, due, true)
	require.Zero(t, out.Unlinked)
	require.Equal(t, 1, out.Reasons["identity_changed"])
	data, err := os.ReadFile(f.physical)
	require.NoError(t, err)
	require.Equal(t, newBytes, data)
}

func TestOrphanOtherWriterRefused(t *testing.T) {
	f := startOrphanFixture(t)
	_, err := diskcache.OpenReclaimTree(f.cache)
	require.ErrorIs(t, err, diskcache.ErrReclaimBusy)
}

func TestOrphanCrashChild(t *testing.T) {
	point := os.Getenv("MUMENT_ORPHAN_CRASH")
	if point == "" {
		t.Skip("subprocess crash harness")
	}
	root, cache := os.Getenv("MUMENT_ORPHAN_ROOT"), os.Getenv("MUMENT_ORPHAN_CACHE")
	indexing.SetTestIndex("orphan-test", root)
	nanos, err := strconv.ParseInt(os.Getenv("MUMENT_ORPHAN_NOW"), 10, 64)
	require.NoError(t, err)
	f := &orphanFixture{root: root, cache: cache}
	f.start(t)
	<-f.s.orphans.done
	f.s.orphans.enabled = true
	f.s.orphans.checkpoint = func(phase string) error {
		if phase == point {
			os.Exit(72)
		}
		return nil
	}
	t.Fatalf("crash point not reached: %+v", f.step(t, time.Unix(0, nanos), true))
}

func (f *orphanFixture) sql(t *testing.T, q string, args ...any) {
	t.Helper()
	db, err := sql.Open(dbsql.SqliteDriver, filepath.Join(f.cache, "image-preview-ledger.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.ExecContext(t.Context(), q, args...)
	require.NoError(t, err)
}

func (f *orphanFixture) addReference(t *testing.T, name string) cacheledger.Ref {
	t.Helper()
	path := filepath.Join(f.root, name)
	require.NoError(t, os.WriteFile(path, []byte("synthetic reference"), 0o600))
	v, err := cacheledger.Observe(path)
	require.NoError(t, err)
	ns, base, ok := cacheledger.Location(f.root, path)
	require.True(t, ok)
	version, err := json.Marshal(v)
	require.NoError(t, err)
	f.sql(t, "INSERT INTO refs VALUES(?,?,?,?)", ns, base, string(version), f.ref.Asset)
	return cacheledger.Ref{Source: ns, Path: base, Asset: f.ref.Asset, Version: v}
}

func TestOrphanSharedReferencesAndRestore(t *testing.T) {
	f := startOrphanFixture(t)
	f.addReference(t, "other.jpg")
	first := f.remove(t)
	held := f.step(t, first.Add(orphanGrace), false)
	require.Zero(t, held.ConfirmedAssets)
	require.Zero(t, held.Due)
	require.FileExists(t, f.physical)

	last := f.removePath(t, filepath.Join(f.root, "other.jpg"))
	require.GreaterOrEqual(t, last.UnixNano(), first.UnixNano())
	wait := f.step(t, last.Add(orphanGrace-time.Nanosecond), false)
	require.Equal(t, 1, wait.Waiting)
	require.Equal(t, last.UTC(), *wait.Samples[0].ConfirmedAt)

	defer func() { f.s.orphans.checkpoint = nil }()
	<-f.s.orphans.done
	f.s.orphans.enabled = true
	f.s.orphans.checkpoint = func(stage string) error {
		if stage == "isolated" {
			return errors.New("stop in quarantine")
		}
		return nil
	}
	due := last.Add(orphanGrace)
	require.Equal(t, 1, f.step(t, due, true).Reasons["interrupted"])
	require.NoFileExists(t, f.physical)
	f.addReference(t, "return.jpg")
	f.s.orphans.checkpoint = nil
	recovered := f.step(t, due, true)
	require.Equal(t, 1, recovered.Reasons["reference_restored"])
	require.Zero(t, recovered.Unlinked)
	require.FileExists(t, f.physical)
	next := f.removePath(t, filepath.Join(f.root, "return.jpg"))
	require.Equal(t, next.UTC(), *f.step(t, next, false).Samples[0].ConfirmedAt)
	require.Equal(t, 1, f.step(t, next.Add(orphanGrace-time.Nanosecond), false).Waiting)
}

func TestOrphanSharedPhysicalAndHardlink(t *testing.T) {
	f := startOrphanFixture(t)
	f.sql(t, "INSERT INTO assets VALUES('zz-shared')")
	f.sql(t, `INSERT INTO results SELECT 'zz-shared',variant,storage_key,size,digest,generated_at,last_used_at
		FROM results WHERE asset=?`, f.ref.Asset)
	f.sql(t, "INSERT INTO refs VALUES('fixture','/linked','version','zz-shared')")
	due := f.remove(t).Add(orphanGrace)
	report := f.step(t, due, false)
	require.Zero(t, report.Due)
	require.Equal(t, 1, report.Reasons["reference_present"])
	f.sql(t, "DELETE FROM refs WHERE asset='zz-shared'")
	report = f.step(t, due, false)
	require.Zero(t, report.Due)
	require.Equal(t, 1, report.Reasons["unproven_ref_loss"])
	f.sql(t, "DELETE FROM results WHERE asset='zz-shared'")
	alias := filepath.Join(t.TempDir(), "outside-cache")
	require.NoError(t, os.Link(f.physical, alias))
	report = f.step(t, due, false)
	require.Zero(t, report.Due)
	require.Equal(t, 1, report.Reasons["hardlink_or_nonregular"])
	require.FileExists(t, f.physical)
	require.FileExists(t, alias)
}

func TestOrphanMoveIntentProtectsOldReference(t *testing.T) {
	f := startOrphanFixture(t)
	moves, err := f.s.ledger.Prepare(t.Context(), []cacheledger.Move{{
		Ref: f.ref, TargetSource: f.ref.Source, TargetPath: "/move.jpg",
	}})
	require.NoError(t, err)
	require.Len(t, moves, 1)
	repo := f.s.ledger
	unlock, err := repo.LockLocations(t.Context())
	require.NoError(t, err)
	m := BeginOrphanMutation(t.Context(), repo, "orphan-test", f.root, f.source, "delete")
	require.NotNil(t, m)
	require.NoError(t, os.Remove(f.source))
	m.Finish(t.Context(), m.ProvesUnlinked(false), "")
	unlock()
	_, found, err := repo.Existing(t.Context(), f.ref.Source, f.ref.Path, f.ref.Version)
	require.NoError(t, err)
	require.True(t, found)
	r := f.step(t, time.Now().Add(orphanGrace), false)
	require.Zero(t, r.Due)
	require.Equal(t, "pending", r.RecentEvents[0].State)
	require.FileExists(t, f.physical)
}

func TestOrphanReplacementEpochProtectsNewBytes(t *testing.T) {
	f := startOrphanFixture(t)
	due := f.remove(t).Add(orphanGrace)
	<-f.s.orphans.done
	f.s.orphans.enabled = true
	f.s.orphans.checkpoint = func(stage string) error {
		if stage == "isolated" {
			return errors.New("stop in quarantine")
		}
		return nil
	}
	require.Equal(t, 1, f.step(t, due, true).Reasons["interrupted"])
	f.s.orphans.checkpoint = nil
	ref := f.addReference(t, "replacement.jpg")
	data := []byte("new publication under the same key")
	require.NoError(t, f.s.fileCache.Store(t.Context(), f.key, data))
	stamp := f.s.orphans.publicationStamp(f.key)
	require.NotEmpty(t, stamp)
	_, err := f.s.ledger.PublishObserved(t.Context(), ref, f.variant, f.key, data, stamp)
	require.NoError(t, err)
	recovered := f.step(t, due, true)
	require.Equal(t, 1, recovered.Reasons["publication_changed"])
	require.Zero(t, recovered.Unlinked)
	still, err := os.ReadFile(f.physical)
	require.NoError(t, err)
	require.Equal(t, data, still)
	intents, err := f.s.ledger.OrphanIntents(t.Context(), 0)
	require.NoError(t, err)
	require.Equal(t, "held", intents[0].State)
}

func TestOrphanSymlinkAndSourceIdentity(t *testing.T) {
	f := startOrphanFixture(t)
	due := f.remove(t).Add(orphanGrace)
	other := t.TempDir()
	indexing.SetTestIndex("orphan-test", other)
	r := f.step(t, due, false)
	require.Equal(t, 1, r.Reasons["source_identity_unavailable"])
	indexing.SetTestIndex("orphan-test", f.root)
	moved := f.physical + ".retained"
	require.NoError(t, os.Rename(f.physical, moved))
	outside := filepath.Join(other, "outside")
	require.NoError(t, os.WriteFile(outside, []byte("unaltered"), 0o600))
	require.NoError(t, os.Symlink(outside, f.physical))
	<-f.s.orphans.done
	f.s.orphans.enabled = true
	r = f.step(t, due, true)
	require.Equal(t, 1, r.Reasons["symlink"])
	require.Zero(t, r.Unlinked)
	actual, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "unaltered", string(actual))
	require.FileExists(t, moved)
}

func TestOrphanPrivateMounts(t *testing.T) {
	if os.Getenv("MUMENT_ORPHAN_MOUNT_TEST") != "1" {
		t.Skip("run in a private user/mount namespace")
	}
	var cacheRoot string
	f := startOrphanFixtureBefore(t, func(_, cache string) {
		cacheRoot = filepath.Join(cache, "diskcache")
		require.NoError(t, os.Mkdir(cacheRoot, 0o700))
		require.NoError(t, unix.Mount("tmpfs", cacheRoot, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=32m,mode=0700"))
		t.Cleanup(func() { require.NoError(t, unix.Unmount(cacheRoot, 0)) })
	})
	due := f.remove(t).Add(orphanGrace)
	<-f.s.orphans.done
	f.s.orphans.enabled = true
	require.NoError(t, unix.Mount("tmpfs", cacheRoot, "tmpfs", unix.MS_REMOUNT|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_RDONLY, "size=32m,mode=0700"))
	require.Equal(t, 1, f.step(t, due, true).Reasons["read_only"])
	require.NoError(t, unix.Mount("tmpfs", cacheRoot, "tmpfs", unix.MS_REMOUNT|unix.MS_NOSUID|unix.MS_NODEV, "size=32m,mode=0700"))

	shard := filepath.Dir(f.physical)
	outside := t.TempDir()
	other := filepath.Join(outside, filepath.Base(f.physical))
	require.NoError(t, os.WriteFile(other, []byte("outside mount"), 0o600))
	require.NoError(t, unix.Mount(outside, shard, "", unix.MS_BIND, ""))
	require.Equal(t, 1, f.step(t, due, true).Reasons["mount_boundary"])
	require.NoError(t, unix.Unmount(shard, 0))

	require.NoError(t, unix.Mount(outside, f.root, "", unix.MS_BIND, ""))
	require.Equal(t, 1, f.step(t, due, true).Reasons["source_identity_unavailable"])
	require.NoError(t, unix.Unmount(f.root, 0))
	require.Equal(t, 1, f.step(t, due, true).Unlinked)
	actual, err := os.ReadFile(other)
	require.NoError(t, err)
	require.Equal(t, "outside mount", string(actual))
}

func TestOrphanUnprovenLossBlocksLaterDelete(t *testing.T) {
	f := startOrphanFixture(t)
	f.addReference(t, "last.jpg")
	require.NoError(t, f.s.ledger.Detach(t.Context(), f.ref))
	confirmed := f.removePath(t, filepath.Join(f.root, "last.jpg"))
	report := f.step(t, confirmed.Add(orphanGrace), false)
	require.Zero(t, report.Due)
	require.Zero(t, report.ConfirmedAssets)
	pubs, err := f.s.ledger.OrphanPublications(t.Context(), f.key)
	require.NoError(t, err)
	require.Len(t, pubs, 1)
	require.True(t, pubs[0].Unproven)
	require.FileExists(t, f.source)
	require.FileExists(t, f.physical)
}

func TestOrphanReadKeyGateAndCancellation(t *testing.T) {
	f := startOrphanFixture(t)
	due := f.remove(t).Add(orphanGrace)
	<-f.s.orphans.done
	f.s.orphans.enabled = true
	unlock, err := f.s.orphans.lockKey(t.Context(), f.key)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	f.s.orphans.now = func() time.Time { return due }
	report := f.s.orphans.report(ctx, OrphanCursor{}, true)
	require.Positive(t, report.Reasons["interrupted"])
	require.Zero(t, report.Unlinked)
	unlock()
	require.FileExists(t, f.physical)
	require.Equal(t, 1, f.step(t, due, true).Unlinked)
}
