//go:build linux && !386 && !arm && !cacheledger_portable

package preview

import (
	"context"
	"fmt"
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/stretchr/testify/require"
	"image"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func reuseTestFile(t *testing.T, s *Service, file iteminfo.ExtendedFileInfo, pace func(context.Context, int) error) (ContentOutcome, error) {
	t.Helper()
	return s.ReuseContent(t.Context(), file, func(context.Context) (*os.File, error) { return os.Open(file.RealPath) }, func(context.Context) (os.FileInfo, error) { return os.Stat(file.RealPath) }, pace)
}
func TestContentExternalReusePreservesPublication(t *testing.T) {
	f := startOrphanFixture(t)
	ctx := t.Context()
	// Large originals force both actual decodes (xlarge must not be original).
	source := newNoisyFile(t, f.root, "big.jpg", 1600, 1200)
	source.Source = "orphan-test"
	expected := map[string][]byte{}
	epochs := map[string]string{}
	for _, size := range []string{"small", "xlarge"} {
		data, err := f.s.getPreviewForFile(ctx, source, size, "", 0)
		require.NoError(t, err)
		expected[size] = data
		plan, err := f.s.planLedgerPreview(ctx, source, size)
		require.NoError(t, err)
		pubs, err := f.s.ledger.OrphanPublications(ctx, plan.key)
		require.NoError(t, err)
		require.Len(t, pubs, 1)
		epochs[size] = pubs[0].Epoch
	}
	before := f.s.Stats().FullDecodes
	out, err := reuseTestFile(t, f.s, source, nil)
	require.NoError(t, err)
	require.Equal(t, "proven_own", out.Reason)
	require.Positive(t, out.Bytes)
	target := source
	target.Name = "moved.jpg"
	target.RealPath = filepath.Join(f.root, target.Name)
	require.NoError(t, os.Rename(source.RealPath, target.RealPath))
	out, err = reuseTestFile(t, f.s, target, nil)
	require.NoError(t, err)
	require.Equal(t, "reused", out.Reason)
	for _, size := range []string{"small", "xlarge"} {
		data, err := f.s.getPreviewForFile(ctx, target, size, "", 0)
		require.NoError(t, err)
		require.Equal(t, expected[size], data)
		plan, err := f.s.planLedgerPreview(ctx, target, size)
		require.NoError(t, err)
		pubs, err := f.s.ledger.OrphanPublications(ctx, plan.key)
		require.NoError(t, err)
		require.Equal(t, epochs[size], pubs[0].Epoch)
	}
	require.Equal(t, before, f.s.Stats().FullDecodes)
}
func TestContentNoOldProofAndPostHashReplacement(t *testing.T) {
	f := startOrphanFixture(t)
	source := iteminfo.ExtendedFileInfo{Source: "orphan-test", RealPath: f.source, FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: "source.jpg", Type: "image/jpeg", HasPreview: true}}}
	target := source
	target.Name = "moved.jpg"
	target.RealPath = filepath.Join(f.root, target.Name)
	require.NoError(t, os.Rename(source.RealPath, target.RealPath))
	out, err := reuseTestFile(t, f.s, target, nil)
	require.NoError(t, err)
	require.Equal(t, "no_old_proof", out.Reason)
	require.Zero(t, out.Bytes)
	known, err := f.s.ledger.ContentProof(t.Context(), f.ref)
	require.NoError(t, err)
	require.False(t, known)
	// Generate/prove target, then copy its exact bytes and replace during hashing.
	_, err = f.s.getPreviewForFile(t.Context(), target, "small", "", 0)
	require.NoError(t, err)
	_, err = reuseTestFile(t, f.s, target, nil)
	require.NoError(t, err)
	data, err := os.ReadFile(target.RealPath)
	require.NoError(t, err)
	copy := target
	copy.Name = "copy.jpg"
	copy.RealPath = filepath.Join(f.root, copy.Name)
	require.NoError(t, os.WriteFile(copy.RealPath, data, 0600))
	replaced := false
	_, err = reuseTestFile(t, f.s, copy, func(_ context.Context, n int) error {
		if n > 0 && !replaced {
			replaced = true
			tmp := copy.RealPath + ".tmp"
			require.NoError(t, os.WriteFile(tmp, data, 0600))
			require.NoError(t, os.Rename(tmp, copy.RealPath))
		}
		return nil
	})
	require.ErrorIs(t, err, cacheledger.ErrStale)
	plan, err := f.s.planLedgerPreview(t.Context(), copy, "small")
	require.NoError(t, err)
	require.NotEqual(t, f.ref.Asset, plan.ref.Asset)
}

func TestContentCopiesHardlinksAndIndependentMutation(t *testing.T) {
	for _, hardlink := range []bool{false, true} {
		t.Run(fmt.Sprint("hardlink=", hardlink), func(t *testing.T) {
			f := startOrphanFixture(t)
			source := iteminfo.ExtendedFileInfo{Source: "orphan-test", RealPath: f.source, FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: "source.jpg", Type: "image/jpeg", HasPreview: true}}}
			// Hardlink creation changes source ctime. Establish proof after the link so
			// both exact observations are eligible, not by inode-only inference.
			copy := source
			copy.Name = "copy.jpg"
			copy.RealPath = filepath.Join(f.root, copy.Name)
			data, err := os.ReadFile(source.RealPath)
			require.NoError(t, err)
			if hardlink {
				require.NoError(t, os.Link(source.RealPath, copy.RealPath))
				_, err = f.s.getPreviewForFile(t.Context(), source, "small", "", 0)
				require.NoError(t, err)
			} else {
				require.NoError(t, os.WriteFile(copy.RealPath, data, 0600))
			}
			_, err = reuseTestFile(t, f.s, source, nil)
			require.NoError(t, err)
			out, err := reuseTestFile(t, f.s, copy, nil)
			require.NoError(t, err)
			require.Equal(t, "reused", out.Reason)
			sourcePlan, err := f.s.planLedgerPreview(t.Context(), source, "small")
			require.NoError(t, err)
			copyPlan, err := f.s.planLedgerPreview(t.Context(), copy, "small")
			require.NoError(t, err)
			require.Equal(t, sourcePlan.ref.Asset, copyPlan.ref.Asset)
			before := f.s.Stats().FullDecodes
			require.NoError(t, os.Remove(source.RealPath))
			if hardlink {
				// Unlink changes ctime on the surviving inode; F revalidates
				// that new observation before it can share the old publication.
				_, err = reuseTestFile(t, f.s, copy, nil)
				require.NoError(t, err)
			}
			_, err = f.s.getPreviewForFile(t.Context(), copy, "small", "", 0)
			require.NoError(t, err)
			require.Equal(t, before, f.s.Stats().FullDecodes)

			// A new version at the remaining path cannot inherit the shared token.
			require.NoError(t, os.WriteFile(copy.RealPath, append(data, 0), 0600))
			mutated, err := f.s.planLedgerPreview(t.Context(), copy, "small")
			require.NoError(t, err)
			require.NotEqual(t, copyPlan.ref.Asset, mutated.ref.Asset)
		})
	}
}

func TestContentReclamationDefersBindingAndSharedDeletion(t *testing.T) {
	f := startOrphanFixture(t)
	source := iteminfo.ExtendedFileInfo{Source: "orphan-test", RealPath: f.source, FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: "source.jpg", Type: "image/jpeg", HasPreview: true}}}
	_, err := reuseTestFile(t, f.s, source, nil)
	require.NoError(t, err)
	data, err := os.ReadFile(f.source)
	require.NoError(t, err)
	copy := source
	copy.Name = "copy.jpg"
	copy.RealPath = filepath.Join(f.root, copy.Name)
	require.NoError(t, os.WriteFile(copy.RealPath, data, 0600))
	out, err := reuseTestFile(t, f.s, copy, nil)
	require.NoError(t, err)
	require.Equal(t, "reused", out.Reason)
	// Independently confirmed source unlink leaves the copy protected. The last
	// independently confirmed deletion starts E2's original grace, not before.
	<-f.s.orphans.done
	f.s.orphans.enabled = true
	when := f.remove(t)
	report := f.step(t, when, true)
	require.Zero(t, report.Unlinked)
	require.FileExists(t, f.physical)
	last := f.removePath(t, copy.RealPath)
	early := f.step(t, last.Add(orphanGrace-time.Second), true)
	require.Zero(t, early.Unlinked)
	final := f.step(t, last.Add(orphanGrace+time.Second), true)
	require.Equal(t, 1, final.Unlinked, "%+v", final)
}

func TestContentPNGAndVariantIsolation(t *testing.T) {
	f := startOrphanFixture(t)
	image := image.NewNRGBA(image.Rect(0, 0, 1300, 1200))
	_, err := rand.New(rand.NewSource(17)).Read(image.Pix)
	require.NoError(t, err)
	path := filepath.Join(f.root, "image.png")
	file, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, png.Encode(file, image))
	require.NoError(t, file.Close())
	source := iteminfo.ExtendedFileInfo{Source: "orphan-test", RealPath: path, FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: "image.png", Type: "image/png", HasPreview: true}}}
	previews := map[string][]byte{}
	keys := map[string]bool{}
	for _, size := range []string{"small", "large", "xlarge"} {
		data, e := f.s.getPreviewForFile(t.Context(), source, size, "", 0)
		require.NoError(t, e)
		previews[size] = data
		key := ledgerVariant(source, size)
		require.False(t, keys[key])
		keys[key] = true
	}
	out, err := reuseTestFile(t, f.s, source, nil)
	require.NoError(t, err)
	require.Equal(t, "proven_own", out.Reason)
	target := source
	target.Name = "copy.png"
	target.RealPath = filepath.Join(f.root, target.Name)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(target.RealPath, data, 0600))
	before := f.s.Stats().FullDecodes
	out, err = reuseTestFile(t, f.s, target, nil)
	require.NoError(t, err)
	require.Equal(t, "reused", out.Reason)
	for size, expected := range previews {
		data, e := f.s.getPreviewForFile(t.Context(), target, size, "", 0)
		require.NoError(t, e)
		require.Equal(t, expected, data)
	}
	require.Equal(t, before, f.s.Stats().FullDecodes)
	jpeg := target
	jpeg.Type = "image/jpeg"
	jpeg.Name = "copy.jpg"
	require.NotEqual(t, ledgerVariant(jpeg, "small"), ledgerVariant(target, "small"))
	// C's variant includes quality (small/large/xlarge), output format, dimensions
	// and fill/fit mode. F shares assets without aliasing these result keys.
}

func TestContentForegroundGenerationWinsDuringHash(t *testing.T) {
	f := startOrphanFixture(t)
	source := iteminfo.ExtendedFileInfo{Source: "orphan-test", RealPath: f.source, FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: "source.jpg", Type: "image/jpeg", HasPreview: true}}}
	_, err := reuseTestFile(t, f.s, source, nil)
	require.NoError(t, err)
	target := source
	target.Name = "raced.jpg"
	target.RealPath = filepath.Join(f.root, target.Name)
	bytes, err := os.ReadFile(source.RealPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(target.RealPath, bytes, 0600))
	entered, release := blockGeneration(f.s)
	result := make(chan error, 1)
	started := false
	out, err := reuseTestFile(t, f.s, target, func(_ context.Context, n int) error {
		if n > 0 && !started {
			started = true
			go func() { _, e := f.s.getPreviewForFile(t.Context(), target, "small", "", 0); result <- e }()
			<-entered
		}
		return nil
	})
	require.ErrorIs(t, err, ErrPreviewBusy)
	require.Positive(t, out.Bytes)
	close(release)
	require.NoError(t, <-result)
	plan, err := f.s.planLedgerPreview(t.Context(), target, "small")
	require.NoError(t, err)
	require.NotEqual(t, f.ref.Asset, plan.ref.Asset)
	_, hit, err := f.s.ledger.Lookup(t.Context(), plan.ref, plan.variant)
	require.NoError(t, err)
	require.True(t, hit)
	out, err = reuseTestFile(t, f.s, target, nil)
	require.NoError(t, err)
	require.Equal(t, "already_proven", out.Reason)
	require.Zero(t, out.Bytes)
}
