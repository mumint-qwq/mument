//go:build linux && !386 && !arm && !cacheledger_portable

package files

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	dbsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestContentCopyHintsNeverWaitForLedgerWriter(t *testing.T) {
	if os.Getenv("MUMENT_REUSE_MOUNT_TEST") != "1" {
		t.Skip("private mount namespace required")
	}
	root := t.TempDir()
	a, b, cache := filepath.Join(root, "a"), filepath.Join(root, "b"), t.TempDir()
	for _, path := range []string{a, b} {
		require.NoError(t, os.Mkdir(path, 0700))
		require.NoError(t, unix.Mount("tmpfs", path, "tmpfs", 0, "size=8m"))
		defer unix.Unmount(path, 0)
	}
	previous := settings.Config.Server
	prevFile, prevDir := fileutils.PermFile, fileutils.PermDir
	defer func() {
		settings.Config.Server = previous
		fileutils.SetFsPermissions(uint32(prevFile), uint32(prevDir))
	}()
	fileutils.SetFsPermissions(0600, 0700)
	settings.Config.Server.NameToSource = map[string]*settings.Source{"reuse-a": {Name: "reuse-a", Path: a}, "reuse-b": {Name: "reuse-b", Path: b}}
	indexing.SetTestIndex("reuse-a", a)
	indexing.SetTestIndex("reuse-b", b)
	defer indexing.ClearTestIndices()
	require.NoError(t, preview.StartPreviewGeneratorWithBudget(1, 128, cache))
	defer preview.Shutdown(context.Background())

	writer, err := sql.Open(dbsql.SqliteDriver, filepath.Join(cache, "image-preview-ledger.sqlite"))
	require.NoError(t, err)
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	_, err = writer.Exec("BEGIN IMMEDIATE")
	require.NoError(t, err)
	defer writer.Exec("ROLLBACK")

	run := func(enabled bool, files int, prefix string) time.Duration {
		settings.Config.Server.PreviewReuse = settings.PreviewWarmupConfig{Enabled: enabled, Scopes: []settings.PreviewWarmupScope{{Source: "reuse-b", Directory: "."}}}
		var moves []ResourceMove
		for i := range files {
			name := fmt.Sprintf("%s-%02d.jpg", prefix, i)
			src, dst := filepath.Join(a, name), filepath.Join(b, name)
			require.NoError(t, os.WriteFile(src, []byte("synthetic payload"), 0600))
			moves = append(moves, ResourceMove{Source: "reuse-a", Target: "reuse-b", From: src, To: dst})
		}
		require.ErrorIs(t, os.Rename(moves[0].From, moves[0].To), unix.EXDEV)
		start := time.Now()
		batch := NewIndexImpactBatch()
		res := (&Service{}).moveResources(batch, moves)
		require.Equal(t, 2, batch.Len())
		elapsed := time.Since(start)
		for i, err := range res {
			require.NoError(t, err)
			require.FileExists(t, moves[i].To)
			require.Eventually(t, func() bool {
				_, e := os.Stat(moves[i].From)
				return os.IsNotExist(e)
			}, time.Second, time.Millisecond)
		}
		return elapsed
	}
	off, on, large := run(false, 12, "off"), run(true, 12, "on"), run(true, 64, "large")
	// Saturate the disposable 256-entry queue while the independent writer is
	// still held. There is no F consumer in this fixture.
	for range 512 {
		preview.HintCopiedContent("reuse-b", filepath.Join(b, "on-00.jpg"), true)
	}
	full := run(true, 64, "full")
	for _, elapsed := range []time.Duration{on, large, full} {
		require.Less(t, elapsed-off, 500*time.Millisecond)
	}
	_, err = writer.Exec("ROLLBACK")
	require.NoError(t, err)
	drained := 0
	for {
		n, e := preview.GetService().FlushContentHints(t.Context())
		require.NoError(t, e)
		drained += n
		if n == 0 {
			break
		}
	}
	require.Equal(t, 256, drained, "queue saturation is deterministic and bounded")
	t.Logf("actual EXDEV with independent BEGIN IMMEDIATE: 12 off=%s on=%s; 64 on=%s full=%s; flushed=%d; all physical moves and two directory intents preserved", off, on, large, full, drained)
}
