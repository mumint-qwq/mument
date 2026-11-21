//go:build linux && !386 && !arm && !cacheledger_portable

package previewwarmup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/files"
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestContentWorkerVariantCoverageAndCopyPreference(t *testing.T) {
	for _, mode := range []string{"external", "exdev", "queue_full"} {
		t.Run(mode, func(t *testing.T) {
			if mode != "external" && os.Getenv("MUMENT_REUSE_MOUNT_TEST") != "1" {
				t.Skip("private mount namespace required")
			}
			db, idx, root := warmupTestIndex(t)
			a, b := filepath.Join(root, "Pictures"), filepath.Join(root, "Pictures")
			sourceName, targetName := "library", "library"
			if mode != "external" {
				a, b = filepath.Join(root, "a"), filepath.Join(root, "b")
				for _, p := range []string{a, b} {
					require.NoError(t, os.Mkdir(p, 0700))
					require.NoError(t, unix.Mount("tmpfs", p, "tmpfs", 0, "size=32m"))
					defer unix.Unmount(p, 0)
				}
				sourceName, targetName = "revision-a", "revision-b"
				idx = indexing.SetTestIndexWithDB(sourceName, a, db)
				indexing.SetTestIndexWithDB(targetName, b, db)
			}
			previous := settings.Config.Server
			t.Cleanup(func() { settings.Config.Server = previous })
			cfg := settings.PreviewWarmupConfig{Enabled: true}
			w := &ReuseWorker{db: db, done: make(chan struct{}), status: ReuseStatus{Enabled: true, Counts: map[string]uint64{}}}
			if mode == "external" {
				cfg.Scopes = []settings.PreviewWarmupScope{{Source: sourceName, Directory: "Pictures"}}
				settings.Config.Server.NameToSource = map[string]*settings.Source{sourceName: {Name: sourceName, Path: root}}
				w.scopes = []scope{{config: cfg.Scopes[0], sourceRoot: root, root: a, prefix: "/Pictures/"}}
			} else {
				cfg.Scopes = []settings.PreviewWarmupScope{{Source: sourceName, Directory: ".", MountPoint: "."}, {Source: targetName, Directory: ".", MountPoint: "."}}
				settings.Config.Server.NameToSource = map[string]*settings.Source{sourceName: {Name: sourceName, Path: a}, targetName: {Name: targetName, Path: b}}
				for i, p := range []string{a, b} {
					w.scopes = append(w.scopes, scope{config: cfg.Scopes[i], sourceRoot: p, root: p, prefix: "/"})
				}
			}
			settings.Config.Server.PreviewReuse = cfg
			cache := t.TempDir()
			require.NoError(t, preview.StartPreviewGeneratorWithBudget(1, 128, cache))
			svc := preview.GetService()
			w.svc = svc
			w.repo = svc.ContentLedger()
			w.probe = fakePressure(t, cache)
			t.Cleanup(func() { require.NoError(t, preview.Shutdown(context.Background())) })
			info := func(path, name string) iteminfo.ExtendedFileInfo {
				return iteminfo.ExtendedFileInfo{Source: name, RealPath: path, FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: filepath.Base(path), Type: "image/jpeg", HasPreview: true}}}
			}
			original, duplicate, distinct := info(filepath.Join(a, "source.jpg"), sourceName), info(filepath.Join(a, "duplicate.jpg"), sourceName), info(filepath.Join(a, "distinct.jpg"), sourceName)
			makePhoto(t, original.RealPath, 1600, 1200)
			data, e := os.ReadFile(original.RealPath)
			require.NoError(t, e)
			require.NoError(t, os.WriteFile(duplicate.RealPath, data, 0600))
			past := time.Now().Add(-time.Minute)
			require.NoError(t, os.Chtimes(duplicate.RealPath, past, past))
			makePhoto(t, distinct.RealPath, 1700, 1200) // An independent original digest.
			require.NoError(t, idx.RefreshDirectory(w.scopes[0].prefix, false))
			ref := func(f iteminfo.ExtendedFileInfo) cacheledger.Ref {
				sourceRoot := settings.Config.Server.NameToSource[f.Source].Path
				ns, path, ok := cacheledger.Location(sourceRoot, f.RealPath)
				require.True(t, ok)
				v, e := cacheledger.Observe(f.RealPath)
				require.NoError(t, e)
				r, e := w.repo.FindRef(t.Context(), ns, path, v)
				require.NoError(t, e)
				return r
			}
			for _, f := range []iteminfo.ExtendedFileInfo{original, duplicate} {
				_, e = preview.GetPreviewForFile(t.Context(), f, "small", "", 0)
				require.NoError(t, e)
			}
			if mode == "external" && ref(original).Asset < ref(duplicate).Asset {
				original, duplicate = duplicate, original
			}
			expected := map[string][]byte{}
			for _, f := range []iteminfo.ExtendedFileInfo{original, distinct} {
				expected[f.RealPath], e = preview.GetPreviewForFile(t.Context(), f, "xlarge", "", 0)
				require.NoError(t, e)
			}
			if mode != "external" {
				for _, size := range []string{"large", "xlarge"} {
					_, e = preview.GetPreviewForFile(t.Context(), duplicate, size, "", 0)
					require.NoError(t, e)
				}
			}
			epochs := map[string]string{}
			publications := map[string][]cacheledger.ContentResult{}
			for _, f := range []iteminfo.ExtendedFileInfo{original, distinct} {
				publications[f.RealPath], e = w.repo.ContentResults(t.Context(), ref(f).Asset)
				require.NoError(t, e)
				for _, p := range publications[f.RealPath] {
					pubs, e := w.repo.OrphanPublications(t.Context(), p.Key)
					require.NoError(t, e)
					require.Len(t, pubs, 1)
					epochs[p.Key] = pubs[0].Epoch
				}
			}
			time.Sleep(1100 * time.Millisecond)
			proveStart := time.Now()
			for _, f := range []iteminfo.ExtendedFileInfo{original, duplicate, distinct} {
				rel, e := filepath.Rel(w.scopes[0].sourceRoot, f.RealPath)
				require.NoError(t, e)
				c, e := db.PreviewContentExact(t.Context(), sourceName, "/"+filepath.ToSlash(rel))
				require.NoError(t, e)
				require.True(t, w.processReuse(t.Context(), &w.scopes[0], c))
			}
			sourceProofTime, sourceBytes := time.Since(proveStart), w.Snapshot().ReadBytes
			before := svc.Stats().FullDecodes
			if mode == "queue_full" {
				for range 512 {
					preview.HintCopiedContent(sourceName, duplicate.RealPath, true)
				}
			}
			oldRefs := map[string]cacheledger.Ref{original.RealPath: ref(original), distinct.RealPath: ref(distinct)}
			var targets []iteminfo.ExtendedFileInfo
			moveStart := time.Now()
			var moveTime time.Duration
			if mode == "external" {
				for _, f := range []iteminfo.ExtendedFileInfo{original, distinct} {
					target := info(filepath.Join(b, "moved-"+f.Name), targetName)
					require.NoError(t, os.Rename(f.RealPath, target.RealPath))
					targets = append(targets, target)
				}
			} else {
				var moves []files.ResourceMove
				for _, f := range []iteminfo.ExtendedFileInfo{original, distinct} {
					target := info(filepath.Join(b, "moved-"+f.Name), targetName)
					targets = append(targets, target)
					moves = append(moves, files.ResourceMove{Source: sourceName, Target: targetName, From: f.RealPath, To: target.RealPath})
				}
				require.ErrorIs(t, os.Rename(moves[0].From, moves[0].To), unix.EXDEV)
				moves = append(moves, files.ResourceMove{Source: sourceName, Target: targetName, From: filepath.Join(a, "missing.jpg"), To: filepath.Join(b, "missing.jpg")})
				beforeMaintenance := files.IndexMaintenanceStats()
				batch := files.NewIndexImpactBatch()
				result := files.MoveResourcesBatched(batch, moves)
				moveTime = time.Since(moveStart)
				require.NoError(t, result[0])
				require.NoError(t, result[1])
				require.Error(t, result[2])
				require.Equal(t, 2, batch.Len())
				batch.Submit()
				wait, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				require.NoError(t, files.WaitForIndexMaintenance(wait))
				cancel()
				require.EqualValues(t, 2, files.IndexMaintenanceStats().Refreshes-beforeMaintenance.Refreshes)
			}
			if mode == "external" {
				moveTime = time.Since(moveStart)
			}
			require.Equal(t, sourceBytes, w.Snapshot().ReadBytes, "moving performs no F hash")
			// Refresh to the same committed index consumed by the product scheduler.
			if mode == "external" {
				require.NoError(t, idx.RefreshDirectory(w.scopes[0].prefix, false))
			}
			if mode == "exdev" {
				n, e := svc.FlushContentHints(t.Context())
				require.NoError(t, e)
				require.Equal(t, 2, n)
				hints, e := w.repo.ContentHints(t.Context())
				require.NoError(t, e)
				require.Len(t, hints, 2)
				for _, h := range hints {
					require.True(t, h.EXDEV)
					require.NotEmpty(t, h.PreferredAsset)
				}
			}
			verifyStart := time.Now()
			ctx, cancel := context.WithCancel(t.Context())
			go w.run(ctx)
			require.Eventually(t, func() bool { return w.Snapshot().Counts["reused"] >= 2 }, 15*time.Second, 25*time.Millisecond)
			cancel()
			require.NoError(t, w.Wait(t.Context()))
			for i, target := range targets {
				old := []iteminfo.ExtendedFileInfo{original, distinct}[i]
				gotRef := ref(target)
				if mode != "queue_full" {
					require.Equal(t, oldRefs[old.RealPath].Asset, gotRef.Asset)
				} // Queue loss uses coverage ranking, without original-identity knowledge.
				got, e := preview.GetPreviewForFile(t.Context(), target, "xlarge", "", 0)
				require.NoError(t, e)
				require.Equal(t, expected[old.RealPath], got)
				current, e := w.repo.ContentResults(t.Context(), gotRef.Asset)
				require.NoError(t, e)
				if mode != "queue_full" {
					for _, p := range publications[old.RealPath] {
						found := false
						for _, q := range current {
							if q.Variant == p.Variant {
								require.Equal(t, p.Key, q.Key)
								require.Equal(t, p.GeneratedAt, q.GeneratedAt)
								require.Equal(t, p.Digest, q.Digest)
								found = true
							}
						}
						require.True(t, found, "original publication retained")
						pubs, e := w.repo.OrphanPublications(t.Context(), p.Key)
						require.NoError(t, e)
						require.Len(t, pubs, 1)
						require.Equal(t, epochs[p.Key], pubs[0].Epoch)
					}
				}
			}
			require.Equal(t, before, svc.Stats().FullDecodes)
			t.Logf("mode=%s independent_digests=2 source_hash_bytes=%d source_hash_ms=%d move_ms=%d target_wait_ms=%d target_hash_bytes=%d xlarge_decodes=0", mode, sourceBytes, sourceProofTime.Milliseconds(), moveTime.Milliseconds(), time.Since(verifyStart).Milliseconds(), w.Snapshot().ReadBytes-sourceBytes)
		})
	}
}
