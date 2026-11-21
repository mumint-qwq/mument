//go:build linux && !386 && !arm && !cacheledger_portable

package preview

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/stretchr/testify/require"
)

func TestContentChoosesReadyMosaicOverSmallDuplicate(t *testing.T) {
	for _, split := range []bool{false, true} {
		t.Run(fmt.Sprintf("split_%t", split), func(t *testing.T) {
			f := startOrphanFixture(t)
			ctx := t.Context()
			first := newNoisyFile(t, f.root, "first.jpg", 1600, 1200)
			first.Source = "orphan-test"
			second := first
			second.Name = "second.jpg"
			second.RealPath = filepath.Join(f.root, second.Name)
			original, err := os.ReadFile(first.RealPath)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(second.RealPath, original, 0600))
			require.NoError(t, os.Chtimes(second.RealPath, first.ModTime, first.ModTime))

			a, err := f.s.planLedgerPreview(ctx, first, "small")
			require.NoError(t, err)
			b, err := f.s.planLedgerPreview(ctx, second, "small")
			require.NoError(t, err)
			if a.ref.Asset > b.ref.Asset {
				first, second = second, first
				a, b = b, a
			}
			require.Less(t, a.ref.Asset, b.ref.Asset)
			// Deliberately give the arbitrary first candidate only a small preview.
			_, err = f.s.getPreviewForFile(ctx, first, "small", "", 0)
			require.NoError(t, err)
			var large []byte
			for _, size := range []string{"small", "xlarge"} {
				if split && size == "small" {
					continue
				}
				large, err = f.s.getPreviewForFile(ctx, second, size, "", 0)
				require.NoError(t, err)
			}
			for _, which := range []iteminfo.ExtendedFileInfo{first, second} {
				out, e := reuseTestFile(t, f.s, which, nil)
				require.NoError(t, e)
				require.Equal(t, "proven_own", out.Reason)
			}
			originalLarge, err := f.s.planLedgerPreview(ctx, second, "xlarge")
			require.NoError(t, err)
			publication, ok, err := f.s.ledger.Lookup(ctx, originalLarge.ref, originalLarge.variant)
			require.NoError(t, err)
			require.True(t, ok)
			require.True(t, publication.Matches(large))

			moved := second
			moved.Name = "moved.jpg"
			moved.RealPath = filepath.Join(f.root, moved.Name)
			require.NoError(t, os.Rename(second.RealPath, moved.RealPath))
			out, err := reuseTestFile(t, f.s, moved, nil)
			require.NoError(t, err)
			require.Equal(t, "reused", out.Reason)
			chosen, err := f.s.planLedgerPreview(ctx, moved, "xlarge")
			require.NoError(t, err)
			require.Equal(t, b.ref.Asset, chosen.ref.Asset)
			reused, ok, err := f.s.ledger.Lookup(ctx, chosen.ref, chosen.variant)
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, publication.Key, reused.Key)
			require.Equal(t, publication.GeneratedAt, reused.GeneratedAt)
			before := f.s.Stats().FullDecodes
			got, err := f.s.getPreviewForFile(ctx, moved, "xlarge", "", 0)
			require.NoError(t, err)
			require.Equal(t, large, got)
			require.Equal(t, before, f.s.Stats().FullDecodes)
			t.Log("richer original publication preserved; xlarge decode_delta=0")
		})
	}
}
