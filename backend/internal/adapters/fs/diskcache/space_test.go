//go:build linux

package diskcache

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestSpaceInventoryCountsPhysicalFilesOnce(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "diskcache")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".orphan-reclamation"), 0o700))
	data := []byte("synthetic")
	old := filepath.Join(root, "old")
	require.NoError(t, os.WriteFile(old, data, 0o600))
	require.NoError(t, os.Link(old, filepath.Join(root, "alias")))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".orphan-reclamation", "isolated"), data, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".preview-temp"), data, 0o600))
	tree, err := OpenReclaimTree(base)
	require.NoError(t, err)
	defer tree.Close()
	inventory, err := tree.NewInventory()
	require.NoError(t, err)
	defer inventory.Close()
	var total Usage
	var done bool
	for range 20 {
		total, done, err = inventory.Step(t.Context(), 1)
		require.NoError(t, err)
		if done {
			break
		}
	}
	require.True(t, done)
	require.Equal(t, int64(3), total.Files)
	require.Equal(t, int64(3*len(data)), total.Logical)
	require.Positive(t, total.Allocated)
	require.Zero(t, total.Excluded)
}

func TestSpaceStoreUsesRootDescriptor(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "diskcache")
	require.NoError(t, os.Mkdir(root, 0o700))
	fileutils.SetFsPermissions(0o600, 0o700)
	tree, err := OpenReclaimTree(base)
	require.NoError(t, err)
	defer tree.Close()
	require.NoError(t, tree.Store(t.Context(), "key", []byte("first")))
	require.NoError(t, tree.Store(t.Context(), "key", []byte("second")))
	leaf := filepath.Join(root, RelativePath("key"))
	actual, err := os.ReadFile(leaf)
	require.NoError(t, err)
	require.Equal(t, []byte("second"), actual)
	saved := root + "-saved"
	require.NoError(t, os.Rename(root, saved))
	require.NoError(t, os.Mkdir(root, 0o700))
	require.ErrorIs(t, tree.Store(t.Context(), "key", []byte("outside")), ErrReclaimIdentity)
	actual, err = os.ReadFile(filepath.Join(saved, RelativePath("key")))
	require.NoError(t, err)
	require.Equal(t, []byte("second"), actual)
	require.NoFileExists(t, leaf)
}

func TestSpaceSmallPrivateFilesystem(t *testing.T) {
	if os.Getenv("MUMENT_SPACE_MOUNT_TEST") != "1" {
		t.Skip("run in a private user and mount namespace")
	}
	base := t.TempDir()
	root := filepath.Join(base, "diskcache")
	require.NoError(t, os.Mkdir(root, 0o700))
	require.NoError(t, unix.Mount("tmpfs", root, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=8m,nr_inodes=128,mode=0700"))
	defer unix.Unmount(root, 0)
	tree, err := OpenReclaimTree(base)
	require.NoError(t, err)
	defer tree.Close()
	initial, err := tree.SpaceSample()
	require.NoError(t, err)
	require.Equal(t, int64(8<<20), initial.Available)
	require.True(t, initial.HasInodes)
	filler := filepath.Join(root, "external")
	require.NoError(t, os.WriteFile(filler, make([]byte, 7<<20), 0o600))
	reduced, err := tree.SpaceSample()
	require.NoError(t, err)
	require.Equal(t, int64(1<<20), reduced.Available)
	require.Less(t, reduced.Inodes, initial.Inodes)

	fileutils.SetFsPermissions(0o600, 0o700)
	err = tree.Store(t.Context(), "too-large", make([]byte, 2<<20))
	require.ErrorIs(t, err, unix.ENOSPC)
	entries, err := os.ReadDir(filepath.Dir(filepath.Join(root, RelativePath("too-large"))))
	require.NoError(t, err)
	require.Empty(t, entries)
	after, err := tree.SpaceSample()
	require.NoError(t, err)
	require.Equal(t, reduced.Available, after.Available)

	for i := range 100 {
		require.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("inode-%d", i)), nil, 0o600))
	}
	inodes, err := tree.SpaceSample()
	require.NoError(t, err)
	require.Less(t, inodes.Inodes, uint64(32))
	t.Logf("real tmpfs usable inodes %d → %d", initial.Inodes, inodes.Inodes)

	require.NoError(t, unix.Mount("tmpfs", root, "tmpfs", unix.MS_REMOUNT|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_RDONLY, "size=8m,nr_inodes=128,mode=0700"))
	readonly, err := tree.SpaceSample()
	require.NoError(t, err)
	require.True(t, readonly.ReadOnly)
	require.ErrorIs(t, tree.Store(t.Context(), "readonly", []byte("data")), unix.EROFS)
	t.Logf("real tmpfs available bytes %d → %d, ENOSPC with no temp, read_only=%t",
		initial.Available, reduced.Available, readonly.ReadOnly)
}
