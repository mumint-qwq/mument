package cacheledger

import (
	"context"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestContentHashFullAndChanges(t *testing.T) {
	for _, mode := range []string{"normal", "middle", "truncate", "append", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "image")
			original := strings.Repeat("x", 800<<10)
			require.NoError(t, os.WriteFile(path, []byte(original), 0600))
			f, err := os.Open(path)
			require.NoError(t, err)
			defer f.Close()
			v, err := Observe(path)
			require.NoError(t, err)
			if !v.Strong {
				t.Skip("strong source observations unavailable")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			changed := false
			proof, read, err := HashContent(ctx, f, v, 64<<20, func(_ context.Context, n int) error {
				if n == 0 || changed {
					return nil
				}
				changed = true
				switch mode {
				case "middle":
					time.Sleep(5 * time.Millisecond) // Cross the filesystem timestamp update quantum before the injected edit.
					w, e := os.OpenFile(path, os.O_WRONLY, 0)
					require.NoError(t, e)
					_, e = w.WriteAt([]byte("z"), 400<<10)
					require.NoError(t, e)
					require.NoError(t, w.Close())
				case "truncate":
					require.NoError(t, os.Truncate(path, 10))
				case "append":
					w, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
					require.NoError(t, e)
					_, e = w.Write([]byte("extra"))
					require.NoError(t, e)
					w.Close()
				case "cancel":
					cancel()
				}
				return nil
			})
			if mode == "normal" {
				require.NoError(t, err)
				require.Equal(t, Digest([]byte(original)), proof.Digest)
				require.Equal(t, v.Size, read)
			} else {
				require.Error(t, err)
				require.Empty(t, proof.Digest)
			}
		})
	}
}
func TestContentSharingCASAndForegroundWins(t *testing.T) {
	r := testRepository(t)
	ctx := t.Context()
	v := Version{Size: 100, MtimeNS: 1, CtimeNS: 2, Dev: 1, Ino: 3, Strong: true}
	a, err := r.Resolve(ctx, "disk", "/source", v)
	require.NoError(t, err)
	first, err := r.PublishObserved(ctx, a, "small", "key-small", []byte("small"), "stamp")
	require.NoError(t, err)
	_, err = r.PublishObserved(ctx, a, "xlarge", "key-xl", []byte("xlarge"), "stamp")
	require.NoError(t, err)
	proof := ContentProof{Asset: a.Asset, Bytes: v.Size, Digest: Digest([]byte("original"))}
	require.NoError(t, r.ProveContent(ctx, a, proof))
	v.Ino++
	b, err := r.Resolve(ctx, "disk", "/copy", v)
	require.NoError(t, err)
	results, err := r.ContentResults(ctx, a.Asset)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.NoError(t, r.BindContent(ctx, b, proof, results[0]))
	shared, err := r.FindRef(ctx, "disk", "/copy", v)
	require.NoError(t, err)
	require.Equal(t, a.Asset, shared.Asset)
	got, hit, err := r.Lookup(ctx, shared, "small")
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, first, got)
	_, err = r.Publish(ctx, b, "small", "late", []byte("bad"))
	require.ErrorIs(t, err, ErrStale)
	// Old source deletion cannot remove a still-shared result.
	require.NoError(t, r.Detach(ctx, a))
	_, hit, err = r.Lookup(ctx, shared, "small")
	require.NoError(t, err)
	require.True(t, hit)
	v.Ino++
	c, err := r.Resolve(ctx, "disk", "/raced", v)
	require.NoError(t, err)
	_, err = r.Publish(ctx, c, "small", "own", []byte("own"))
	require.NoError(t, err)
	require.ErrorIs(t, r.BindContent(ctx, c, proof, results[0]), ErrStale)
	// Proofs are immutable and cannot be assigned to a superseded reference.
	wrong := proof
	wrong.Digest = Digest([]byte("different"))
	require.ErrorIs(t, r.ProveContent(ctx, shared, wrong), ErrStale)
	v.CtimeNS++
	_, err = r.Resolve(ctx, "disk", "/copy", v)
	require.NoError(t, err)
	require.ErrorIs(t, r.ProveContent(ctx, shared, proof), ErrStale)
}
func TestContentWorkRestartAndScopeIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger")
	r, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, r.BindContentScope(t.Context(), "scope", "disk-one"))
	require.ErrorIs(t, r.BindContentScope(t.Context(), "scope", "disk-two"), ErrStale)
	yes, err := r.ContentWork(t.Context(), "scope", "/a", "v1")
	require.NoError(t, err)
	require.True(t, yes)
	require.NoError(t, r.Close())
	r, err = Open(path)
	require.NoError(t, err)
	defer r.Close()
	yes, err = r.ContentWork(t.Context(), "scope", "/a", "v1")
	require.NoError(t, err)
	require.True(t, yes, "pending hashes restart")
	require.NoError(t, r.FinishContentWork(t.Context(), "scope", "/a", "v1", true))
	yes, err = r.ContentWork(t.Context(), "scope", "/a", "v1")
	require.NoError(t, err)
	require.False(t, yes)
	yes, err = r.ContentWork(t.Context(), "scope", "/a", "v2")
	require.NoError(t, err)
	require.True(t, yes)
	require.NoError(t, r.PruneContentWork(t.Context()))
}

func TestContentBindingWaitsForReclamation(t *testing.T) {
	for _, table := range []string{"orphan_intents", "capacity_intents"} {
		t.Run(table, func(t *testing.T) {
			r := testRepository(t)
			v := Version{Size: 100, MtimeNS: 1, CtimeNS: 2, Dev: 1, Ino: 3, Strong: true}
			src, e := r.Resolve(t.Context(), "disk", "/source", v)
			require.NoError(t, e)
			_, e = r.Publish(t.Context(), src, "small", "key", []byte("data"))
			require.NoError(t, e)
			p := ContentProof{Asset: src.Asset, Bytes: 100, Digest: Digest([]byte("original"))}
			require.NoError(t, r.ProveContent(t.Context(), src, p))
			v.Ino++
			target, e := r.Resolve(t.Context(), "disk", "/target", v)
			require.NoError(t, e)
			results, e := r.ContentResults(t.Context(), src.Asset)
			require.NoError(t, e)
			_, e = r.db.Exec("INSERT INTO " + table + "(id,storage_key,publications,expected,logical,blocks,state) VALUES('intent','key','pubs','stamp',4,512,'isolated')")
			require.NoError(t, e)
			require.ErrorIs(t, r.BindContent(t.Context(), target, p, results[0]), ErrStale)
			_, e = r.db.Exec("UPDATE " + table + " SET state='removed'")
			require.NoError(t, e)
			require.NoError(t, r.BindContent(t.Context(), target, p, results[0]))
		})
	}
}
