//go:build linux && !386 && !arm && !cacheledger_portable

package files

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestOrphanLargeDeleteLeavesLocationGateAvailable(t *testing.T) {
	previous := settings.Config.Server.PreviewOrphanReclamation
	settings.Config.Server.PreviewOrphanReclamation = false
	t.Cleanup(func() { settings.Config.Server.PreviewOrphanReclamation = previous })
	fileutils.SetFsPermissions(0o600, 0o700)
	require.NoError(t, preview.StartPreviewGeneratorWithBudget(1, 128, t.TempDir()))
	t.Cleanup(func() { require.NoError(t, preview.Shutdown(context.Background())) })
	for _, tc := range []struct {
		name string
		dir  bool
	}{
		{"directory", true},
		{"stale-file-type", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertOrphanRecursiveDeleteLeavesLocationsAvailable(t, tc.dir)
		})
	}
}

func assertOrphanRecursiveDeleteLeavesLocationsAvailable(t *testing.T, directory bool) {
	t.Helper()
	repo := preview.OrphanTracking()
	require.NotNil(t, repo)
	root := t.TempDir()
	folder := filepath.Join(root, "large")
	require.NoError(t, os.Mkdir(folder, 0o700))
	for i := range 40000 {
		f, err := os.Create(filepath.Join(folder, fmt.Sprintf("empty-%05d", i)))
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}
	_, err := diskcache.ObserveSourceDirectory(t.Context(), root, folder, 32)
	require.ErrorIs(t, err, diskcache.ErrReclaimScope)
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	require.NoError(t, err)
	defer unix.Close(fd)
	_, err = unix.InotifyAddWatch(fd, folder, unix.IN_DELETE)
	require.NoError(t, err)
	finished := make(chan error, 1)
	start := time.Now()
	go func() { finished <- removeResource("fixture", root, folder, directory) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		buf := make([]byte, 4096)
		_, err = unix.Read(fd, buf)
		if err == nil {
			break
		}
		require.True(t, errors.Is(err, unix.EAGAIN) && time.Now().Before(deadline), "%v", err)
		time.Sleep(time.Millisecond)
	}
	tryStart := time.Now()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	unlock, gateErr := repo.LockLocations(ctx)
	wait := time.Since(tryStart)
	cancel()
	if unlock != nil {
		unlock()
	}
	require.NoError(t, <-finished)
	events, err := repo.OrphanEvents(t.Context())
	require.NoError(t, err)
	require.Empty(t, events)
	t.Logf("over-limit directory, no evidence staged, default executor disabled: location wait=%v error=%v delete elapsed=%v", wait, gateErr, time.Since(start))
	require.NoError(t, gateErr)
}
