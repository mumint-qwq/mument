//go:build linux && !386 && !arm && !cacheledger_portable

package preview

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
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
	file.Source = "orphan-test"
	plan, err := f.s.planLedgerPreview(t.Context(), file, "small")
	require.NoError(t, err)
	data := []byte("synthetic valid cache record for " + name)
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
			m := testCapacity(t, f, when, true)
			m.orphan.checkpoint = func(stage string) error {
				if phase == stage {
					return errors.New("stop after durable phase")
				}
				return nil
			}
			report := m.report(t.Context(), CapacityCursor{}, true)
			require.Equal(t, 1, report.Reasons["interrupted"], "%+v", report)
			require.NoError(t, f.s.Shutdown(t.Context()))
			f.start(t)
			m = testCapacity(t, f, when, true)
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
