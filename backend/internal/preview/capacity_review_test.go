//go:build linux && !386 && !arm && !cacheledger_portable

package preview

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestSpaceReviewPartitionAdmission(t *testing.T) {
	if os.Getenv("MUMENT_SPACE_MOUNT_TEST") != "1" {
		t.Skip("private mount namespace required")
	}
	fileutils.SetFsPermissions(0o600, 0o700)
	volume := t.TempDir()
	require.NoError(t, unix.Mount("tmpfs", volume, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=8m,mode=0700"))
	t.Cleanup(func() { require.NoError(t, unix.Unmount(volume, 0)) })
	require.NoError(t, os.Mkdir(filepath.Join(volume, "diskcache"), 0o700))
	tree, err := diskcache.OpenReclaimTree(volume)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tree.Close()) })
	space := newSpaceManager(tree, settings.PreviewSpaceConfig{Mode: "enforce", MinimumFreeMiB: 4, ResumeFreeMiB: 5})
	now := time.Now()
	space.now = func() time.Time { return now }
	space.poll(now)
	require.True(t, space.endInventory(now, 0, diskcache.Usage{}, nil))
	payload := make([]byte, 3*spaceMiB)
	before, err := tree.SpaceSample()
	require.NoError(t, err)
	// A has sampled the filesystem. Let a different key B reserve, write,
	// and return its reservation before A enters reserve with its old sample.
	b, err := space.reserve(int64(len(payload)), false)
	require.NoError(t, err)
	require.NoError(t, tree.Store(t.Context(), "review-writer-b", payload))
	space.finish(b, int64(len(payload)), int64(len(payload)), nil)
	fresh, err := tree.SpaceSample()
	require.NoError(t, err)
	_, err = space.reserve(int64(len(payload)), false)
	require.ErrorContains(t, err, "space_low_free")
	_, err = space.reserve(int64(len(payload)), false)
	require.ErrorContains(t, err, "space_low_free")
	after, err := tree.SpaceSample()
	require.NoError(t, err)
	require.GreaterOrEqual(t, after.Available, int64(4*spaceMiB))
	t.Logf("old external sample=%d current=%d, floor held at %d", before.Available, fresh.Available, after.Available)

}

func reviewScopedJPEG(t *testing.T, f *orphanFixture, variant string) (iteminfo.ExtendedFileInfo, *ledgerPreview, []byte) {
	t.Helper()
	file := newNoisyFile(t, f.root, "review-cold.jpg", 1600, 1200)
	file.Source = "orphan-test"
	data, err := f.s.getPreviewForFile(t.Context(), file, variant, "", 0)
	require.NoError(t, err)
	plan, err := f.s.planLedgerPreview(t.Context(), file, variant)
	require.NoError(t, err)
	stamp, proof := f.s.orphans.publicationStamp(plan.key), plan.capacityProof()
	require.NotEmpty(t, stamp)
	require.NotEmpty(t, proof)
	// Use real encoder bytes while modeling a publication made by D that
	// has never been used by the foreground.
	f.sql(t, "UPDATE results SET last_used_at=0 WHERE asset=? AND variant=?", plan.ref.Asset, plan.variant)
	_, err = f.s.ledger.PublishScoped(t.Context(), plan.ref, plan.variant, plan.key, data, stamp, proof)
	require.NoError(t, err)
	return file, plan, data
}

func reviewIsolatedJPEG(t *testing.T) (*orphanFixture, iteminfo.ExtendedFileInfo, *ledgerPreview, []byte, time.Time, cacheledger.OrphanIntent) {
	t.Helper()
	f := startOrphanFixture(t)
	file, plan, data := reviewScopedJPEG(t, f, "small")
	when := time.Now().Add(2 * time.Hour)
	require.NoError(t, f.s.Shutdown(t.Context()))
	crashCapacityAt(t, f, when, "isolated")
	f.start(t)
	intents, err := f.s.ledger.CapacityIntents(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, intents, 1)
	require.Equal(t, "isolated", intents[0].State)
	return f, file, plan, data, when, intents[0]
}

func TestCapacityReviewRestoreCrashPreservesPublication(t *testing.T) {
	f, _, plan, data, when, intent := reviewIsolatedJPEG(t)
	isolated, err := f.s.orphans.tree.OpenIsolated(intent.ID, false)
	require.NoError(t, err)
	defer isolated.Close()
	object, err := f.s.orphans.tree.OpenKey(plan.key)
	require.NoError(t, err)
	defer object.Close()
	stamp, err := isolated.Stamp()
	require.NoError(t, err)
	// Complete the same physical Move as restoreObject. Do not settle the
	// old ledger state, to model a process stop between rename and SQL.
	require.NoError(t, isolated.Move(object, stamp))
	m := testCapacity(t, f, when, true)
	require.True(t, m.space.endInventory(when, 0, diskcache.Usage{}, nil))
	require.False(t, m.space.needsCapacity())
	recovered := m.report(t.Context(), CapacityCursor{}, true)
	require.Zero(t, recovered.Unlinked)
	same, err := os.ReadFile(filepath.Join(f.cache, "diskcache", diskcache.RelativePath(plan.key)))
	require.NoError(t, err)
	require.Equal(t, data, same)
	_, found, err := f.s.ledger.Lookup(t.Context(), plan.ref, plan.variant)
	require.NoError(t, err)
	require.True(t, found)
	until, err := f.s.ledger.CoolingUntil(t.Context(), plan.ref.Asset, plan.variant)
	require.NoError(t, err)
	require.True(t, until.IsZero())
	audit, err := f.s.ledger.CapacityAudit(t.Context())
	require.NoError(t, err)
	require.Len(t, audit, 1)
	require.Equal(t, "capacity_restored", audit[0].Reason)
	t.Logf("restored file present and identical, publication=%t new cooldown=%t settlement=%s",
		found, !until.IsZero(), audit[0].Reason)
}

func TestCapacityReviewRestoredPreviewCanBeEvictedAgain(t *testing.T) {
	f, _, plan, data, when, _ := reviewIsolatedJPEG(t)
	m := testCapacity(t, f, when, true)
	require.True(t, m.space.endInventory(when, 0, diskcache.Usage{}, nil))
	restored := m.report(t.Context(), CapacityCursor{}, true)
	require.Equal(t, 1, restored.Reasons["capacity_restored"])
	_, published, err := f.s.ledger.Lookup(t.Context(), plan.ref, plan.variant)
	require.NoError(t, err)
	require.True(t, published)
	require.True(t, m.space.endInventory(when, 0, diskcache.Usage{Allocated: spaceMiB}, nil))
	require.True(t, m.space.needsCapacity())
	later := m.report(t.Context(), CapacityCursor{}, true)
	require.Zero(t, later.Reasons["identity_changed"])
	require.Equal(t, 1, later.Unlinked)
	require.NoFileExists(t, filepath.Join(f.cache, "diskcache", diskcache.RelativePath(plan.key)))
	require.NotEmpty(t, data)
	t.Logf("ordinary restore, then renewed pressure: restored result remained published, eviction=%v", later.Reasons)
}

func TestCapacityReviewForegroundPreservesIsolation(t *testing.T) {
	for _, room := range []bool{false, true} {
		t.Run(strconv.FormatBool(room), func(t *testing.T) { testForegroundIsolation(t, room) })
	}
}

func testForegroundIsolation(t *testing.T, room bool) {
	f, file, plan, data, when, intent := reviewIsolatedJPEG(t)
	m := testCapacity(t, f, when, true)
	if room {
		m.space.cfg.BudgetMiB = 64
		m.space.status.BudgetBytes = 64 * spaceMiB
		require.True(t, m.space.endInventory(when, 0, diskcache.Usage{}, nil))
	}
	f.s.space, f.s.capacity = m.space, m
	close(m.done)
	close(m.space.done)
	_, hit, err := f.s.ledger.Lookup(t.Context(), plan.ref, plan.variant)
	require.NoError(t, err)
	require.True(t, hit)
	before := f.s.WarmupMetrics().FullDecodes
	got, err := f.s.getPreviewForFile(t.Context(), file, "small", "", 0)
	require.NoError(t, err)
	require.Equal(t, data, got)
	require.Equal(t, before+1, f.s.WarmupMetrics().FullDecodes)
	require.Equal(t, uint64(1), m.space.snapshot().ForegroundUnstored)
	_, afterward, err := f.s.ledger.Lookup(t.Context(), plan.ref, plan.variant)
	require.NoError(t, err)
	require.True(t, afterward)
	pubs, err := f.s.ledger.OrphanPublications(t.Context(), plan.key)
	require.NoError(t, err)
	require.Equal(t, intent.Publications, cacheledger.OrphanPublicationSet(pubs))
	require.Positive(t, pubs[0].LastUsed)
	require.Zero(t, m.uncertainUsage[keyStripe(plan.key)].Load())
	// Restart before recovery so restored demand must survive in the ledger.
	require.NoError(t, f.s.Shutdown(t.Context()))
	f.start(t)
	m = testCapacity(t, f, when, true)
	require.True(t, m.space.endInventory(when, m.space.revision, diskcache.Usage{}, nil))
	recovered := m.report(t.Context(), CapacityCursor{}, true)
	require.Equal(t, 1, recovered.Reasons["capacity_restored"])
	intents, err := f.s.ledger.CapacityIntents(t.Context(), 0)
	require.NoError(t, err)
	require.Empty(t, intents)
	require.NoFileExists(t, filepath.Join(f.cache, "diskcache", ".orphan-reclamation", intent.ID))
	still, err := os.ReadFile(filepath.Join(f.cache, "diskcache", diskcache.RelativePath(plan.key)))
	require.NoError(t, err)
	require.Equal(t, data, still)
	t.Log("foreground delivery preserved the epoch and recovery restored the identical bytes")
}

func TestCapacityReviewObserveReleasesWarmupCooldown(t *testing.T) {
	f := startOrphanFixture(t)
	file, plan, _ := reviewScopedJPEG(t, f, warmupSize)
	now := time.Now().Add(2 * time.Hour)
	m := testCapacity(t, f, now, true)
	require.Equal(t, 1, m.report(t.Context(), CapacityCursor{}, true).Unlinked)
	until, err := f.s.ledger.CoolingUntil(t.Context(), plan.ref.Asset, plan.variant)
	require.NoError(t, err)
	require.True(t, now.Before(until))
	require.NoError(t, f.s.Shutdown(t.Context()))
	previous := settings.Config.Server.PreviewSpace
	t.Cleanup(func() { settings.Config.Server.PreviewSpace = previous })
	settings.Config.Server.PreviewSpace = settings.PreviewSpaceConfig{Mode: "observe"}
	f.start(t)
	require.NoError(t, f.s.WarmupSpace())
	err = testWarm(t, t.Context(), f.s, file, func(int64) error { return nil })
	require.NoError(t, err)
	require.Equal(t, uint64(1), f.s.WarmupMetrics().FullDecodes)
	t.Log("observe permits D despite the persisted cooldown")
}

func TestCapacityReviewRestoreCrashPhases(t *testing.T) {
	for _, phase := range []string{"restoring", "restored"} {
		t.Run(phase, func(t *testing.T) {
			f, _, plan, data, when, _ := reviewIsolatedJPEG(t)
			require.NoError(t, f.s.Shutdown(t.Context()))
			crashCapacityAt(t, f, when, phase)
			f.start(t)
			m := testCapacity(t, f, when, true)
			report := m.report(t.Context(), CapacityCursor{}, true)
			require.Equal(t, 1, report.Reasons["capacity_restored"], "%+v", report)
			require.Zero(t, report.Unlinked)
			result, found, err := f.s.ledger.Lookup(t.Context(), plan.ref, plan.variant)
			require.NoError(t, err)
			require.True(t, found)
			require.True(t, result.Matches(data))
			intents, err := f.s.ledger.CapacityIntents(t.Context(), 0)
			require.NoError(t, err)
			require.Empty(t, intents)
			// Renewed pressure can verify the new physical stamp and remove once.
			report = m.report(t.Context(), CapacityCursor{}, true)
			require.Equal(t, 1, report.Unlinked, "%+v", report)
		})
	}
}

func TestCapacityReviewRestoredIdentityProtection(t *testing.T) {
	for _, mutation := range []string{"same_bytes_new_inode", "changed_digest", "replaced_directory", "new_publication", "unlinked"} {
		t.Run(mutation, func(t *testing.T) {
			f, _, plan, data, when, intent := reviewIsolatedJPEG(t)
			isolated, err := f.s.orphans.tree.OpenIsolated(intent.ID, false)
			require.NoError(t, err)
			defer isolated.Close()
			object, err := f.s.orphans.tree.OpenKey(plan.key)
			require.NoError(t, err)
			defer object.Close()
			stamp, err := isolated.Stamp()
			require.NoError(t, err)
			require.NoError(t, isolated.Move(object, stamp))
			path := filepath.Join(f.cache, "diskcache", diskcache.RelativePath(plan.key))
			switch mutation {
			case "same_bytes_new_inode":
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.WriteFile(path, data, 0o600))
			case "changed_digest":
				data[0] ^= 1
				require.NoError(t, os.WriteFile(path, data, 0o600))
				require.NoError(t, os.Chtimes(path, time.Unix(0, stamp.Mtime), time.Unix(0, stamp.Mtime)))
			case "replaced_directory":
				require.NoError(t, os.Rename(filepath.Dir(path), filepath.Dir(path)+".old"))
				require.NoError(t, os.Mkdir(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, data, 0o600))
			case "new_publication":
				_, err := f.s.ledger.PublishScoped(t.Context(), plan.ref, plan.variant, plan.key, data, f.s.orphans.publicationStamp(plan.key), plan.capacityProof())
				require.NoError(t, err)
			case "unlinked":
				require.NoError(t, os.Remove(path))
			}
			m := testCapacity(t, f, when, true)
			require.True(t, m.space.endInventory(when, 0, diskcache.Usage{}, nil))
			report := m.report(t.Context(), CapacityCursor{}, true)
			require.Zero(t, report.Unlinked)
			require.Zero(t, report.Reasons["capacity_restored"], "%+v", report)
			if mutation == "unlinked" {
				require.Equal(t, 1, report.Reasons["missing_after_isolation"])
			} else {
				require.FileExists(t, path)
			}
		})
	}
}

func TestSpaceReviewInodeAdmission(t *testing.T) {
	if os.Getenv("MUMENT_SPACE_MOUNT_TEST") != "1" {
		t.Skip("private mount namespace required")
	}
	fileutils.SetFsPermissions(0o600, 0o700)
	volume := t.TempDir()
	require.NoError(t, unix.Mount("tmpfs", volume, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=8m,nr_inodes=38,mode=0700"))
	t.Cleanup(func() { require.NoError(t, unix.Unmount(volume, 0)) })
	require.NoError(t, os.Mkdir(filepath.Join(volume, "diskcache"), 0o700))
	tree, err := diskcache.OpenReclaimTree(volume)
	require.NoError(t, err)
	defer tree.Close()
	m := newSpaceManager(tree, settings.PreviewSpaceConfig{Mode: "enforce", MinimumFreeMiB: 1, ResumeFreeMiB: 2})
	m.poll(time.Now())
	require.True(t, m.endInventory(time.Now(), 0, diskcache.Usage{}, nil))
	old := m.snapshot().Filesystem
	b, err := m.reserve(128, false)
	require.NoError(t, err)
	require.NoError(t, tree.Store(t.Context(), "inode-writer-b", make([]byte, 128)))
	m.finish(b, 128, 4096, nil)
	_, err = m.reserve(128, false)
	require.ErrorContains(t, err, "space_low_inodes")
	require.Less(t, m.snapshot().Filesystem.Inodes, old.Inodes)
	t.Logf("inode observation refreshed from %d to %d", old.Inodes, m.snapshot().Filesystem.Inodes)
}

func TestCapacityReviewCorruptionWithoutIntentIsRepaired(t *testing.T) {
	f := startOrphanFixture(t)
	file, plan, data := reviewScopedJPEG(t, f, "small")
	before, found, err := f.s.ledger.Lookup(t.Context(), plan.ref, plan.variant)
	require.NoError(t, err)
	require.True(t, found)
	m := testCapacity(t, f, time.Now(), true)
	m.space.status.BudgetBytes = 64 * spaceMiB
	require.True(t, m.space.endInventory(m.space.now(), 0, diskcache.Usage{}, nil))
	f.s.space, f.s.capacity = m.space, m
	close(m.done)
	close(m.space.done)
	require.NoError(t, f.s.fileCache.Store(t.Context(), plan.key, []byte("corrupt")))
	got, err := f.s.getPreviewForFile(t.Context(), file, "small", "", 0)
	require.NoError(t, err)
	require.Equal(t, data, got)
	after, found, err := f.s.ledger.Lookup(t.Context(), plan.ref, plan.variant)
	require.NoError(t, err)
	require.True(t, found)
	require.NotEqual(t, before.GeneratedAt, after.GeneratedAt)
	require.True(t, after.Matches(got))
}
