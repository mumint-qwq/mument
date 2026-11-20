//go:build linux

package diskcache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	"github.com/stretchr/testify/require"
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
