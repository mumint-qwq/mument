//go:build linux && !386 && !arm && !cacheledger_portable

package preview

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func orphanMountID(t *testing.T, path string) uint64 {
	t.Helper()
	var stat unix.Statx_t
	require.NoError(t, unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_MNT_ID, &stat))
	require.NotZero(t, stat.Mask&unix.STATX_MNT_ID)
	return stat.Mnt_id
}

func TestOrphanRestartAcrossMountNamespaces(t *testing.T) {
	if mode := os.Getenv("MUMENT_ORPHAN_NAMESPACE_CHILD"); mode != "" {
		testOrphanNamespaceChild(t, mode)
		return
	}
	if os.Getenv("MUMENT_ORPHAN_NAMESPACE_TEST") != "1" {
		t.Skip("run with private user and mount namespaces")
	}
	for _, phase := range []string{"before-reserve", "isolated"} {
		t.Run(phase, func(t *testing.T) {
			f := startOrphanFixture(t)
			cacheRoot := filepath.Join(f.cache, "diskcache")
			due := f.remove(t).Add(orphanGrace)
			require.NoError(t, f.s.Shutdown(t.Context()))
			modes := []string{"same-directories"}
			if phase == "isolated" {
				f.crashAt(t, due, "isolated")
				require.NoFileExists(t, f.physical)
				modes = []string{"wrong-volume", "nested-isolation", "same-directories"}
			}
			for _, mode := range modes {
				cmd := exec.CommandContext(t.Context(), "unshare", "--user", "--map-root-user", "--mount", "--propagation", "private",
					os.Args[0], "-test.run=^TestOrphanRestartAcrossMountNamespaces$", "-test.v")
				cmd.Env = append(f.subprocessEnv(due), "MUMENT_ORPHAN_NAMESPACE_CHILD="+mode, "MUMENT_ORPHAN_NAMESPACE_PHASE="+phase,
					"MUMENT_ORPHAN_SOURCE_MOUNT="+strconv.FormatUint(orphanMountID(t, f.root), 10),
					"MUMENT_ORPHAN_CACHE_MOUNT="+strconv.FormatUint(orphanMountID(t, cacheRoot), 10))
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s\n%s", mode, out)
				t.Logf("%s\n%s", mode, out)
			}
			require.NoFileExists(t, f.physical)
		})
	}
}

func testOrphanNamespaceChild(t *testing.T, mode string) {
	f := &orphanFixture{root: os.Getenv("MUMENT_ORPHAN_ROOT"), cache: os.Getenv("MUMENT_ORPHAN_CACHE")}
	cacheRoot := filepath.Join(f.cache, "diskcache")
	for path, parentID := range map[string]string{
		f.root: os.Getenv("MUMENT_ORPHAN_SOURCE_MOUNT"), cacheRoot: os.Getenv("MUMENT_ORPHAN_CACHE_MOUNT"),
	} {
		require.NotEqual(t, parentID, strconv.FormatUint(orphanMountID(t, path), 10), path)
	}
	nanos, err := strconv.ParseInt(os.Getenv("MUMENT_ORPHAN_NOW"), 10, 64)
	require.NoError(t, err)
	due := time.Unix(0, nanos)
	isolation := filepath.Join(cacheRoot, ".orphan-reclamation")
	var path string
	var payload []byte
	reason := ""
	switch mode {
	case "wrong-volume", "nested-isolation":
		entries, err := os.ReadDir(isolation)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		path = filepath.Join(isolation, entries[0].Name())
		payload, err = os.ReadFile(path)
		require.NoError(t, err)
		info, err := os.Stat(path)
		require.NoError(t, err)
		mount := isolation
		reason = "mount_boundary"
		if mode == "wrong-volume" {
			mount = cacheRoot
			reason = "identity_changed"
		}
		require.NoError(t, unix.Mount("tmpfs", mount, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=32m,mode=0700"))
		t.Cleanup(func() { require.NoError(t, unix.Unmount(mount, 0)) })
		require.NoError(t, os.MkdirAll(isolation, 0o700))
		require.NoError(t, os.WriteFile(path, payload, 0o600))
		require.NoError(t, os.Chtimes(path, info.ModTime(), info.ModTime()))
	case "same-directories":
	default:
		t.Fatalf("unknown namespace mode %q", mode)
	}
	indexing.SetTestIndex("orphan-test", f.root)
	f.start(t)
	<-f.s.orphans.done
	f.s.orphans.enabled = true
	before, err := f.s.ledger.OrphanIntents(t.Context(), 0)
	require.NoError(t, err)
	if os.Getenv("MUMENT_ORPHAN_NAMESPACE_PHASE") == "isolated" {
		require.Len(t, before, 1)
		require.Equal(t, "isolated", before[0].State)
	} else {
		require.Empty(t, before)
	}
	report := f.step(t, due, true)
	t.Logf("changed source and cache mount IDs, unlinked=%d reasons=%v", report.Unlinked, report.Reasons)
	intents, err := f.s.ledger.OrphanIntents(t.Context(), 0)
	require.NoError(t, err)
	if reason == "" {
		require.Equal(t, 1, report.Unlinked)
		require.Empty(t, intents)
		return
	}
	require.Zero(t, report.Unlinked)
	require.Equal(t, 1, report.Reasons[reason])
	require.Len(t, intents, 1)
	require.Equal(t, before, intents)
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, payload, actual)
}
