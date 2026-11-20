//go:build linux && !386 && !arm && !cacheledger_portable

package preview

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
)

func testCapacity(t *testing.T, f *orphanFixture, now time.Time, enabled bool) *capacityManager {
	t.Helper()
	space := newSpaceManager(f.s.orphans.tree, settings.PreviewSpaceConfig{
		Mode: "enforce", BudgetMiB: 1, MinimumFreeMiB: 1, ResumeFreeMiB: 2, CapacityEviction: enabled,
	})
	space.now = func() time.Time { return now }
	space.poll(now)
	require.True(t, space.endInventory(now, 0, diskcache.Usage{Allocated: spaceMiB}, nil))
	require.True(t, space.needsCapacity())
	return newCapacityManager(f.s.orphans, space)
}

func publishCapacityFixture(t *testing.T, f *orphanFixture, name string) (*ledgerPreview, cacheledger.Result) {
	t.Helper()
	file := newNoisyFile(t, f.root, name, 800, 600)
	return publishCapacityFile(t, f, file, "small")
}

func publishCapacityFile(t *testing.T, f *orphanFixture, file iteminfo.ExtendedFileInfo, variant string) (*ledgerPreview, cacheledger.Result) {
	t.Helper()
	file.Source = "orphan-test"
	plan, err := f.s.planLedgerPreview(t.Context(), file, variant)
	require.NoError(t, err)
	data := []byte("synthetic valid cache record for " + filepath.Base(file.RealPath))
	require.NoError(t, f.s.fileCache.Store(t.Context(), plan.key, data))
	stamp, proof := f.s.orphans.publicationStamp(plan.key), plan.capacityProof()
	require.NotEmpty(t, stamp)
	require.NotEmpty(t, proof)
	result, err := f.s.ledger.PublishScoped(t.Context(), plan.ref, plan.variant, plan.key, data, stamp, proof)
	require.NoError(t, err)
	return plan, result
}

func TestCapacityEvictionRespectsTrialAndUsage(t *testing.T) {
	f := startOrphanFixture(t)
	hot, used := publishCapacityFixture(t, f, "hot.jpg")
	cold, first := publishCapacityFixture(t, f, "cold.jpg")
	require.NoError(t, f.s.ledger.Touch(t.Context(), hot.ref, hot.variant, used, time.Now()))
	now := time.Now().Add(2 * time.Hour)
	m := testCapacity(t, f, now, false)
	disabled := m.report(t.Context(), CapacityCursor{}, true)
	require.Equal(t, 1, disabled.Due, "%+v", disabled)
	require.Zero(t, disabled.Unlinked)
	require.FileExists(t, filepath.Join(f.cache, "diskcache", diskcache.RelativePath(first.Key)))

	m.space.cfg.CapacityEviction = true
	release, err := m.claim(t.Context(), cold.key)
	require.NoError(t, err)
	active := m.report(t.Context(), CapacityCursor{}, true)
	release()
	require.Zero(t, active.Unlinked)
	require.Equal(t, 1, active.Reasons["in_use"])

	evicted := m.report(t.Context(), CapacityCursor{}, true)
	require.Equal(t, 1, evicted.Unlinked, "%+v", evicted)
	require.Equal(t, first.Size, evicted.UnlinkedBytes)
	require.NoFileExists(t, filepath.Join(f.cache, "diskcache", diskcache.RelativePath(first.Key)))
	require.FileExists(t, filepath.Join(f.cache, "diskcache", diskcache.RelativePath(used.Key)))
	require.FileExists(t, cold.path)
	ref, found, err := f.s.ledger.Existing(t.Context(), cold.ref.Source, cold.ref.Path, cold.ref.Version)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, cold.ref, ref)
	_, found, err = f.s.ledger.Lookup(t.Context(), ref, cold.variant)
	require.NoError(t, err)
	require.False(t, found)
	orphanAudit, err := f.s.ledger.OrphanAudit(t.Context())
	require.NoError(t, err)
	require.Empty(t, orphanAudit)
	capacityAudit, err := f.s.ledger.CapacityAudit(t.Context())
	require.NoError(t, err)
	require.Len(t, capacityAudit, 1)
	require.Equal(t, first.Size, capacityAudit[0].Logical)
}

func TestCapacityRecoverAndRestoreFromIsolation(t *testing.T) {
	for _, phase := range []string{"reserved", "moving", "renamed", "isolated", "unlinked"} {
		t.Run(phase, func(t *testing.T) {
			f := startOrphanFixture(t)
			plan, result := publishCapacityFixture(t, f, "cold.jpg")
			when := time.Now().Add(2 * time.Hour)
			require.NoError(t, f.s.Shutdown(t.Context()))
			crashCapacityAt(t, f, when, phase)
			f.start(t)
			m := testCapacity(t, f, when, true)
			if phase == "isolated" {
				// The original publication receives demand before recovery.
				require.NoError(t, f.s.ledger.Touch(t.Context(), plan.ref, plan.variant, result, when))
			}
			resumed := m.report(t.Context(), CapacityCursor{}, true)
			require.Zero(t, resumed.Reasons["ledger_error"])
			intents, err := f.s.ledger.CapacityIntents(t.Context(), 0)
			require.NoError(t, err)
			require.Empty(t, intents, "%+v", resumed)
			audit, err := f.s.ledger.CapacityAudit(t.Context())
			require.NoError(t, err)
			require.Len(t, audit, 1)
			if phase == "isolated" {
				require.Equal(t, "capacity_restored", audit[0].Reason)
				require.FileExists(t, filepath.Join(f.cache, "diskcache", diskcache.RelativePath(result.Key)))
			} else {
				require.NoFileExists(t, filepath.Join(f.cache, "diskcache", diskcache.RelativePath(result.Key)))
				if phase == "unlinked" {
					require.Equal(t, "missing_after_isolation", audit[0].Reason)
					require.Zero(t, audit[0].Logical)
				} else {
					require.Equal(t, "unlinked", audit[0].Reason)
					require.Equal(t, result.Size, audit[0].Logical)
				}
			}
		})
	}
}

func crashCapacityAt(t *testing.T, f *orphanFixture, now time.Time, phase string) {
	t.Helper()
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCapacityCrashChild$")
	child.Env = append(f.subprocessEnv(now), "MUMENT_CAPACITY_CRASH="+phase)
	raw, err := child.CombinedOutput()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, "%s", raw)
	require.Equal(t, 72, exit.ExitCode(), "%s", raw)
}

func TestCapacityCrashChild(t *testing.T) {
	phase := os.Getenv("MUMENT_CAPACITY_CRASH")
	if phase == "" {
		t.Skip("subprocess crash harness")
	}
	nanos, err := strconv.ParseInt(os.Getenv("MUMENT_ORPHAN_NOW"), 10, 64)
	require.NoError(t, err)
	f := &orphanFixture{root: os.Getenv("MUMENT_ORPHAN_ROOT"), cache: os.Getenv("MUMENT_ORPHAN_CACHE")}
	indexing.SetTestIndex("orphan-test", f.root)
	f.start(t)
	m := testCapacity(t, f, time.Unix(0, nanos), true)
	m.orphan.checkpoint = func(stage string) error {
		if phase == stage {
			os.Exit(72)
		}
		return nil
	}
	t.Fatalf("crash point not reached: %+v", m.report(t.Context(), CapacityCursor{}, true))
}

func TestCapacityOrphanPriorityAndIndependentSwitch(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		testName := "orphan-disabled"
		if enabled {
			testName = "orphan-enabled"
		}
		t.Run(testName, func(t *testing.T) {
			f := startOrphanFixture(t)
			due := f.remove(t).Add(orphanGrace)
			_, live := publishCapacityFixture(t, f, "cold.jpg")
			file := filepath.Join(f.cache, "diskcache", diskcache.RelativePath(live.Key))
			m := testCapacity(t, f, due, true)
			<-f.s.orphans.done
			m.orphan.enabled = enabled
			m.orphan.now = func() time.Time { return due }
			m.orphan.onUnlink = m.space.invalidateInventory
			var cursor CapacityCursor
			offered := false
			m.step(t.Context(), &cursor, &offered)
			require.True(t, offered)
			require.FileExists(t, file)
			require.Nil(t, m.last.Load())
			if enabled {
				require.NoFileExists(t, f.physical)
				require.Equal(t, 1, m.orphan.last.Load().Unlinked)
				require.False(t, m.space.needsCapacity())
				// Full inventory confirms remaining pressure.
				require.True(t, m.space.endInventory(due, m.space.revision, diskcache.Usage{Allocated: spaceMiB}, nil))
			} else {
				require.FileExists(t, f.physical)
				require.Nil(t, m.orphan.last.Load())
			}
			m.step(t.Context(), &cursor, &offered)
			require.Equal(t, 1, m.last.Load().Unlinked)
			require.NoFileExists(t, file)
		})
	}
}

func TestCapacityCooldownAcrossRestartMoveAndForeground(t *testing.T) {
	f := startOrphanFixture(t)
	cold := newNoisyFile(t, f.root, "cold.jpg", 1600, 1200)
	cold.Source = "orphan-test"
	plan, _ := publishCapacityFile(t, f, cold, warmupSize)
	now := time.Now().Add(2 * time.Hour)
	m := testCapacity(t, f, now, true)
	require.Equal(t, 1, m.report(t.Context(), CapacityCursor{}, true).Unlinked)
	until, err := f.s.ledger.CoolingUntil(t.Context(), plan.ref.Asset, plan.variant)
	require.NoError(t, err)
	require.Equal(t, now.Add(cacheledger.CapacityCooldown).UnixNano(), until.UnixNano())

	require.NoError(t, f.s.Shutdown(t.Context()))
	f.start(t)
	m = testCapacity(t, f, now, true)
	f.s.space, f.s.capacity = m.space, m
	close(m.done)
	close(m.space.done)
	m.space.now = func() time.Time { return now }
	setUsage := func(bytes int64) {
		m.space.endInventory(now, m.space.revision, diskcache.Usage{Allocated: bytes}, nil)
	}
	setUsage(0)
	now = now.Add(time.Minute)
	m.space.poll(now)
	require.NoError(t, f.s.WarmupSpace())
	check := func(int64) error { return nil }
	require.ErrorIs(t, testWarm(t, t.Context(), f.s, cold, check), ErrWarmupCooldown)
	require.Zero(t, f.s.WarmupMetrics().FullDecodes)

	previous := now
	now = until
	ready, _, err := f.s.WarmupCooldownPage(t.Context(), cold.Source, cacheledger.CooldownCursor{})
	require.NoError(t, err)
	require.Len(t, ready, 1)
	require.Equal(t, cold.RealPath, ready[0].Path)

	now = previous
	m.space.poll(now)
	repo := f.s.ledger
	moved := filepath.Join(f.root, "moved.jpg")
	namespace, name, ok := cacheledger.Location(f.root, moved)
	require.True(t, ok)
	moves, err := repo.Prepare(t.Context(), []cacheledger.Move{{Ref: plan.ref, TargetSource: namespace, TargetPath: name}})
	require.NoError(t, err)
	require.Len(t, moves, 1)
	require.NoError(t, os.Rename(cold.RealPath, moved))
	version, err := cacheledger.Observe(moved)
	require.NoError(t, err)
	require.NoError(t, repo.CommitRename(t.Context(), moves[0].ID, version))
	cold.RealPath, cold.Name = moved, filepath.Base(moved)
	require.ErrorIs(t, testWarm(t, t.Context(), f.s, cold, check), ErrWarmupCooldown)

	setUsage(spaceMiB)
	data, err := f.s.getPreviewForFile(t.Context(), cold, warmupSize, "", 0)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	still, err := repo.CoolingUntil(t.Context(), plan.ref.Asset, plan.variant)
	require.NoError(t, err)
	require.Equal(t, until, still)
	require.Equal(t, uint64(1), m.space.snapshot().ForegroundUnstored)

	now = until
	setUsage(0)
	m.space.poll(now)
	movedTargets, _, err := f.s.WarmupCooldownPage(t.Context(), cold.Source, cacheledger.CooldownCursor{})
	require.NoError(t, err)
	require.Empty(t, movedTargets)
	// A manual visit can rebuild at its new, still-authorized location.
	second, err := f.s.getPreviewForFile(t.Context(), cold, warmupSize, "", 0)
	require.NoError(t, err)
	require.Equal(t, data, second)
	still, err = repo.CoolingUntil(t.Context(), plan.ref.Asset, plan.variant)
	require.NoError(t, err)
	require.True(t, still.IsZero())
	require.Equal(t, uint64(2), m.cooldownSkips.Load())
}

func TestCapacityHealthyLeavesClockAndOldPublicationsAlone(t *testing.T) {
	f := startOrphanFixture(t)
	plan, _ := publishCapacityFixture(t, f, "old.jpg")
	now := time.Now().Add(8 * 24 * time.Hour)
	m := testCapacity(t, f, now, true)
	m.space.endInventory(now, m.space.revision, diskcache.Usage{}, nil)
	r := m.report(t.Context(), CapacityCursor{}, true)
	require.Zero(t, r.Unlinked)
	require.Zero(t, r.Examined)
	require.NoError(t, f.s.ledger.AdvanceCapacityClock(t.Context(), now.Add(-time.Second)))
	require.FileExists(t, filepath.Join(f.cache, "diskcache", diskcache.RelativePath(plan.key)))
}
