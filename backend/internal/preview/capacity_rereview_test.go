//go:build linux && !386 && !arm && !cacheledger_portable

package preview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestCapacityRereviewLiveRestoreRejectsReplacement(t *testing.T) {
	f, _, plan, data, when, intent := reviewIsolatedJPEG(t)
	m := testCapacity(t, f, when, true)
	require.True(t, m.space.endInventory(when, 0, diskcache.Usage{}, nil))

	var expected diskcache.ObjectStamp
	require.NoError(t, json.Unmarshal([]byte(intent.Expected), &expected))
	path := filepath.Join(f.cache, "diskcache", diskcache.RelativePath(plan.key))
	replaced := false
	m.orphan.checkpoint = func(stage string) error {
		if stage != "restored" || replaced {
			return nil
		}
		replaced = true
		replace, err := os.CreateTemp(filepath.Dir(path), "replacement-")
		require.NoError(t, err)
		_, err = replace.Write(data)
		require.NoError(t, err)
		require.NoError(t, replace.Close())
		require.NoError(t, os.Rename(replace.Name(), path))
		return nil
	}
	recovered := m.report(t.Context(), CapacityCursor{}, true)
	require.True(t, replaced, "%+v", recovered)
	pubs, err := f.s.ledger.OrphanPublications(t.Context(), plan.key)
	require.NoError(t, err)
	require.Len(t, pubs, 1)
	require.Equal(t, intent.Publications, cacheledger.OrphanPublicationSet(pubs))
	var stamped diskcache.ObjectStamp
	require.NoError(t, json.Unmarshal([]byte(pubs[0].Stamp), &stamped))
	require.Equal(t, expected, stamped)
	require.Zero(t, recovered.Reasons["capacity_restored"])
	require.Equal(t, 1, recovered.Reasons["restoration_changed"])
	require.True(t, m.space.endInventory(when, 0, diskcache.Usage{Allocated: spaceMiB}, nil))
	later := m.report(t.Context(), CapacityCursor{}, true)
	require.Zero(t, later.Unlinked, "%+v", later)
	require.FileExists(t, path)
	intents, err := f.s.ledger.CapacityIntents(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, intents, 1)
	require.Equal(t, "held", intents[0].State)
	require.FileExists(t, plan.path)
	t.Log("replacement left intact, old publication stamp retained, intention held for inspection")
}

func startDisabledCapacity(t *testing.T, f *orphanFixture, mode string) {
	t.Helper()
	previous := settings.Config.Server.PreviewSpace
	t.Cleanup(func() { settings.Config.Server.PreviewSpace = previous })
	cfg := settings.PreviewSpaceConfig{}
	if mode != "" {
		cfg = settings.PreviewSpaceConfig{Mode: mode, BudgetMiB: 64, MinimumFreeMiB: 1, ResumeFreeMiB: 2}
	}
	settings.Config.Server.PreviewSpace = cfg
	f.start(t)
	require.NotNil(t, f.s.capacity)
}

func TestCapacityRereviewDisabledRecoversExisting(t *testing.T) {
	for _, mode := range []string{"", "observe", "enforce"} {
		t.Run("mode="+mode, func(t *testing.T) {
			f, file, plan, data, _, intent := reviewIsolatedJPEG(t)
			// A second cold candidate must survive the disabled recovery window.
			cold, _ := publishCapacityFixture(t, f, "other-cold.jpg")
			pubs, err := f.s.ledger.OrphanPublications(t.Context(), cold.key)
			require.NoError(t, err)
			proof, _, err := f.s.ledger.CapacitySource(t.Context(), pubs[0])
			require.NoError(t, err)
			old := time.Now().Add(-2 * time.Hour).UnixNano()
			f.sql(t, "UPDATE results SET generated_at=? WHERE asset=? AND variant=?", old, cold.ref.Asset, cold.variant)
			f.sql(t, "INSERT INTO orphan_publications VALUES(?,?,?,?,?)", cold.ref.Asset, cold.variant, pubs[0].Epoch, old, pubs[0].Stamp)
			f.sql(t, "INSERT INTO capacity_entries(asset,variant,epoch,storage_key,generated,used,proof) VALUES(?,?,?,?,?,0,?)", cold.ref.Asset, cold.variant, pubs[0].Epoch, cold.key, old, proof)
			require.NoError(t, f.s.Shutdown(t.Context()))
			startDisabledCapacity(t, f, mode)
			require.Eventually(t, func() bool {
				pending, err := f.s.ledger.CapacityIntents(t.Context(), 0)
				return err == nil && len(pending) == 0 && f.s.capacity.last.Load() != nil
			}, 12*time.Second, 25*time.Millisecond)
			before := f.s.WarmupMetrics().FullDecodes
			for range 2 {
				got, err := f.s.getPreviewForFile(t.Context(), file, "small", "", 0)
				require.NoError(t, err)
				require.Equal(t, data, got)
			}
			require.Equal(t, before, f.s.WarmupMetrics().FullDecodes)
			pubs, err = f.s.ledger.OrphanPublications(t.Context(), plan.key)
			require.NoError(t, err)
			require.Equal(t, intent.Publications, cacheledger.OrphanPublicationSet(pubs))
			require.FileExists(t, filepath.Join(f.cache, "diskcache", diskcache.RelativePath(cold.key)))
			require.NoFileExists(t, filepath.Join(f.cache, "diskcache", ".orphan-reclamation", intent.ID))
			audit, err := f.s.ledger.CapacityAudit(t.Context())
			require.NoError(t, err)
			require.Len(t, audit, 1)
			require.Equal(t, "capacity_restored", audit[0].Reason)
			report := f.s.capacity.last.Load()
			require.NotNil(t, report)
			require.Zero(t, report.Unlinked)
			require.Zero(t, report.Examined)
			expectedMode := mode
			if expectedMode == "" {
				expectedMode = "off"
			}
			require.Equal(t, expectedMode, PreviewSpaceStatus().Mode)
			if mode != "" {
				require.NotNil(t, f.s.space)
			}
		})
	}
}

func TestCapacityRereviewDisabledProtectsUncertainObjects(t *testing.T) {
	for _, mode := range []string{"", "observe", "enforce"} {
		for _, mutation := range []string{"wrong_root", "fake_object"} {
			t.Run(mode+"/"+mutation, func(t *testing.T) {
				f, file, plan, data, _, intent := reviewIsolatedJPEG(t)
				pubs, err := f.s.ledger.OrphanPublications(t.Context(), plan.key)
				require.NoError(t, err)
				originalStamp := pubs[0].Stamp
				require.NoError(t, f.s.Shutdown(t.Context()))
				root := filepath.Join(f.cache, "diskcache")
				originalIsolation := filepath.Join(root, ".orphan-reclamation", intent.ID)
				if mutation == "wrong_root" {
					require.NoError(t, os.Rename(root, root+".saved"))
					require.NoError(t, os.Mkdir(root, 0o700))
					originalIsolation = filepath.Join(root+".saved", ".orphan-reclamation", intent.ID)
				} else {
					require.NoError(t, os.Rename(originalIsolation, originalIsolation+".saved"))
					require.NoError(t, os.WriteFile(originalIsolation, data, 0o600))
				}
				startDisabledCapacity(t, f, mode)
				r := f.s.capacity.recover(t.Context(), 0)
				require.Zero(t, r.Unlinked)
				require.Zero(t, r.Reasons["capacity_restored"])
				for range 2 {
					got, err := f.s.getPreviewForFile(t.Context(), file, "small", "", 0)
					require.NoError(t, err)
					require.Equal(t, data, got)
				}
				pubs, err = f.s.ledger.OrphanPublications(t.Context(), plan.key)
				require.NoError(t, err)
				require.Equal(t, intent.Publications, cacheledger.OrphanPublicationSet(pubs))
				require.Equal(t, originalStamp, pubs[0].Stamp)
				require.FileExists(t, originalIsolation)
				require.NoFileExists(t, filepath.Join(root, diskcache.RelativePath(plan.key)))
			})
		}
	}
}

func TestSpaceReviewDisabledRecoveryReadOnly(t *testing.T) {
	if os.Getenv("MUMENT_SPACE_MOUNT_TEST") != "1" {
		t.Skip("private mount namespace required")
	}
	for _, mode := range []string{"", "observe", "enforce"} {
		t.Run("mode="+mode, func(t *testing.T) {
			f := startOrphanFixture(t)
			require.NoError(t, f.s.Shutdown(t.Context()))
			root := filepath.Join(f.cache, "diskcache")
			// A filesystem created inside this user namespace supports a real
			// read-only remount without privileges over the host filesystem.
			require.NoError(t, unix.Mount("tmpfs", root, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=32m,mode=0700"))
			defer unix.Unmount(root, 0)
			f.start(t)
			file, plan, data := reviewScopedJPEG(t, f, "small")
			when := time.Now().Add(2 * time.Hour)
			require.NoError(t, f.s.Shutdown(t.Context()))
			crashCapacityAt(t, f, when, "isolated")
			f.start(t)
			intents, err := f.s.ledger.CapacityIntents(t.Context(), 0)
			require.NoError(t, err)
			require.Len(t, intents, 1)
			intent := intents[0]
			require.NoError(t, f.s.Shutdown(t.Context()))
			require.NoError(t, unix.Mount("tmpfs", root, "tmpfs", unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, "size=32m,mode=0700"))
			startDisabledCapacity(t, f, mode)
			defer func() { require.NoError(t, f.s.Shutdown(t.Context())) }()
			r := f.s.capacity.recover(t.Context(), 0)
			require.Equal(t, 1, r.Reasons["read_only"])
			require.Zero(t, r.Unlinked)
			for range 2 {
				got, err := f.s.getPreviewForFile(t.Context(), file, "small", "", 0)
				require.NoError(t, err)
				require.Equal(t, data, got)
			}
			pubs, err := f.s.ledger.OrphanPublications(t.Context(), plan.key)
			require.NoError(t, err)
			require.Equal(t, intent.Publications, cacheledger.OrphanPublicationSet(pubs))
			require.FileExists(t, filepath.Join(root, ".orphan-reclamation", intent.ID))
			require.NoFileExists(t, filepath.Join(root, diskcache.RelativePath(plan.key)))
		})
	}
}

func TestCapacityRereviewDisabledEvictionStillOffersOrphans(t *testing.T) {
	f := startOrphanFixture(t)
	due := f.remove(t).Add(orphanGrace)
	m := testCapacity(t, f, due, false)
	<-f.s.orphans.done
	m.orphan.enabled = true
	m.orphan.now = func() time.Time { return due }
	m.orphan.onUnlink = m.space.invalidateInventory
	var cursor CapacityCursor
	offered := false
	m.step(t.Context(), &cursor, &offered)
	require.True(t, offered)
	require.NoFileExists(t, f.physical)
	require.Equal(t, 1, m.orphan.last.Load().Unlinked)
	audit, err := f.s.ledger.CapacityAudit(t.Context())
	require.NoError(t, err)
	require.Empty(t, audit)
}
