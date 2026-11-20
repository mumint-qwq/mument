//go:build linux && !386 && !arm && !cacheledger_portable

package preview

import (
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestContentRollbackF(t *testing.T) {
	dir, s, source := fRollbackStart(t)
	phase := os.Getenv("MUMENT_REUSE_ROLLBACK_PHASE")
	state := fRollbackRead(t, dir)
	copy := source
	copy.Name = "copy.jpg"
	copy.RealPath = filepath.Join(filepath.Dir(source.RealPath), copy.Name)
	require.NoError(t, s.ContentCacheReady(t.Context()))
	if phase == "f-first" {
		out, e := reuseTestFile(t, s, source, nil)
		require.NoError(t, e)
		require.Equal(t, "proven_own", out.Reason)
		data, e := os.ReadFile(source.RealPath)
		require.NoError(t, e)
		require.NoError(t, os.WriteFile(copy.RealPath, data, 0600))
		out, e = reuseTestFile(t, s, copy, nil)
		require.NoError(t, e)
		require.Equal(t, "reused", out.Reason)
	} else {
		require.Equal(t, "f-return", phase)
		out, e := reuseTestFile(t, s, source, nil)
		require.NoError(t, e)
		require.Equal(t, "proven_own", out.Reason)
	}
	before := s.Stats().FullDecodes
	for _, size := range []string{"small", "xlarge"} {
		data, e := s.getPreviewForFile(t.Context(), copy, size, "", 0)
		require.NoError(t, e)
		require.Equal(t, state.First[size], cacheledger.Digest(data))
		if phase == "f-return" {
			data, e = s.getPreviewForFile(t.Context(), source, size, "", 0)
			require.NoError(t, e)
			require.Equal(t, state.Rewritten[size], cacheledger.Digest(data))
		}
	}
	require.Equal(t, before, s.Stats().FullDecodes)
}
