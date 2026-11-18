// Added for mument 007-C.
package cacheledger

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testRepository(t *testing.T) *Repository {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), "ledger.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close()) })
	return r
}

func TestPublicationAndMoveCAS(t *testing.T) {
	r := testRepository(t)
	ctx := context.Background()
	v := Version{Size: 100, MtimeNS: 10, CtimeNS: 20, Dev: 1, Ino: 2, Strong: true}
	src, err := r.Resolve(ctx, "disk", "/a.jpg", v)
	require.NoError(t, err)
	dst, err := r.Resolve(ctx, "disk", "/b.jpg", Version{Size: 101, MtimeNS: 10, CtimeNS: 20, Dev: 1, Ino: 3, Strong: true})
	require.NoError(t, err)
	require.NoError(t, r.Publish(ctx, src, "large:v1", "source-cache", []byte("source")))
	require.NoError(t, r.Publish(ctx, dst, "large:v1", "target-cache", []byte("target")))
	moves, err := r.Prepare(ctx, []Move{{Ref: src, TargetSource: "disk", TargetPath: "/b.jpg"}})
	require.NoError(t, err)
	after := v
	after.CtimeNS++
	require.NoError(t, r.CommitRename(ctx, moves[0].ID, after))
	require.NoError(t, r.CommitRename(ctx, moves[0].ID, after))
	require.ErrorIs(t, r.Publish(ctx, src, "large:v1", "late", []byte("wrong")), ErrStale)
	require.ErrorIs(t, r.Publish(ctx, dst, "large:v1", "late", []byte("wrong")), ErrStale)
	newRef, err := r.Resolve(ctx, "disk", "/b.jpg", after)
	require.NoError(t, err)
	require.Equal(t, src.Asset, newRef.Asset)
	result, hit, err := r.Lookup(ctx, newRef, "large:v1")
	require.NoError(t, err)
	require.True(t, hit)
	require.True(t, result.Matches([]byte("source")))
	require.False(t, result.Matches([]byte("target")))
	require.Zero(t, result.LastUsedAt)
	_, hit, err = r.Lookup(ctx, newRef, "small:v1")
	require.NoError(t, err)
	require.False(t, hit)
	require.NoError(t, r.Touch(ctx, newRef, "large:v1", time.Now()))
	used, hit, err := r.Lookup(ctx, newRef, "large:v1")
	require.NoError(t, err)
	require.True(t, hit)
	require.Greater(t, used.LastUsedAt, int64(0))
	require.Equal(t, result.GeneratedAt, used.GeneratedAt)
}

func TestReplacementAndWeakVersionsMiss(t *testing.T) {
	r := testRepository(t)
	ctx := context.Background()
	v := Version{Size: 100, MtimeNS: 10, CtimeNS: 20, Dev: 1, Ino: 2, Strong: true}
	old, err := r.Resolve(ctx, "disk", "/a", v)
	require.NoError(t, err)
	require.NoError(t, r.Publish(ctx, old, "v", "key", []byte("data")))
	// Same inode/size/mtime but new ctime cannot inherit the old result.
	v.CtimeNS++
	newRef, err := r.Resolve(ctx, "disk", "/a", v)
	require.NoError(t, err)
	require.NotEqual(t, old.Asset, newRef.Asset)
	_, hit, err := r.Lookup(ctx, newRef, "v")
	require.NoError(t, err)
	require.False(t, hit)
	require.NoError(t, r.Detach(ctx, old)) // late invalidation cannot delete new ref
	resolved, err := r.Resolve(ctx, "disk", "/a", v)
	require.NoError(t, err)
	require.Equal(t, newRef.Asset, resolved.Asset)
	v.Strong = false
	weak1, err := r.Resolve(ctx, "disk", "/weak", v)
	require.NoError(t, err)
	weak2, err := r.Resolve(ctx, "disk", "/weak", v)
	require.NoError(t, err)
	require.NotEqual(t, weak1.Asset, weak2.Asset)
	_, err = r.Prepare(ctx, []Move{{Ref: weak2, TargetSource: "disk", TargetPath: "/other"}})
	require.Error(t, err)
}

func TestBatchRollbackAndRecoveryPreserveNewerReferences(t *testing.T) {
	r := testRepository(t)
	ctx := context.Background()
	v := Version{Size: 100, MtimeNS: 10, CtimeNS: 20, Dev: 1, Ino: 2, Strong: true}
	ref, err := r.Resolve(ctx, "disk", "/a", v)
	require.NoError(t, err)
	_, err = r.Prepare(ctx, []Move{{Ref: ref, TargetSource: "disk", TargetPath: "/b"}, {Ref: ref, TargetSource: "disk", TargetPath: "relative"}})
	require.Error(t, err)
	n, err := r.RecoverPage(ctx, 64)
	require.NoError(t, err)
	require.Zero(t, n)
	_, err = r.Prepare(ctx, []Move{{Ref: ref, TargetSource: "disk", TargetPath: "/b"}})
	require.NoError(t, err)
	v.CtimeNS++
	newRef, err := r.Resolve(ctx, "disk", "/a", v)
	require.NoError(t, err)
	n, err = r.RecoverPage(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	n, err = r.RecoverPage(ctx, 1)
	require.NoError(t, err)
	require.Zero(t, n)
	actual, err := r.Resolve(ctx, "disk", "/a", v)
	require.NoError(t, err)
	require.Equal(t, newRef.Asset, actual.Asset)
	_, err = r.db.Exec("PRAGMA query_only=ON")
	require.NoError(t, err)
	require.Error(t, r.Publish(ctx, actual, "v", "key", []byte("result")))
}

func TestTargetChangedAfterIntentRejectsCommit(t *testing.T) {
	r := testRepository(t)
	ctx := context.Background()
	v := Version{Size: 100, MtimeNS: 10, CtimeNS: 20, Dev: 1, Ino: 2, Strong: true}
	ref, err := r.Resolve(ctx, "disk", "/a", v)
	require.NoError(t, err)
	moves, err := r.Prepare(ctx, []Move{{Ref: ref, TargetSource: "disk", TargetPath: "/b"}})
	require.NoError(t, err)
	_, err = r.Resolve(ctx, "disk", "/b", v)
	require.NoError(t, err)
	require.ErrorIs(t, r.CommitRename(ctx, moves[0].ID, v), ErrStale)
}

// The child exits without Close or deferred rollback. Both the filesystem
// rename and SQLite WAL are real and use the same temporary directory.
func TestProcessIntentRecovery(t *testing.T) {
	if stage := os.Getenv("MUMENT_LEDGER_CRASH_STAGE"); stage != "" {
		runCrashStage(os.Getenv("MUMENT_LEDGER_CRASH_DIR"), stage)
	}
	for _, stage := range []string{"before-intent", "after-intent", "after-rename", "after-commit", "rename-then-replace"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			from, to := filepath.Join(dir, "a"), filepath.Join(dir, "b")
			require.NoError(t, os.WriteFile(from, []byte("source"), 0o600))
			v, err := Observe(from)
			require.NoError(t, err)
			if !v.Strong {
				t.Skip("strong observations unavailable")
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestProcessIntentRecovery$")
			cmd.Env = append(os.Environ(), "MUMENT_LEDGER_CRASH_DIR="+dir, "MUMENT_LEDGER_CRASH_STAGE="+stage)
			var exit *exec.ExitError
			require.ErrorAs(t, cmd.Run(), &exit)
			require.Equal(t, 23, exit.ExitCode())
			if stage == "rename-then-replace" {
				require.NoError(t, os.WriteFile(to, []byte("edited"), 0o600))
			}
			repo, err := Open(filepath.Join(dir, "ledger.db"))
			require.NoError(t, err)
			defer repo.Close()
			ctx := t.Context()
			n, err := repo.RecoverPage(ctx, 64)
			require.NoError(t, err)
			pending := 0
			if stage != "before-intent" && stage != "after-commit" {
				pending = 1
			}
			require.Equal(t, pending, n)
			n, err = repo.RecoverPage(ctx, 64)
			require.NoError(t, err)
			require.Zero(t, n)
			path := from
			if stage == "after-rename" || stage == "after-commit" || stage == "rename-then-replace" {
				path = to
				_, err = os.Stat(from)
				require.ErrorIs(t, err, os.ErrNotExist, "recovery cannot undo the rename")
			}
			actual, err := os.ReadFile(path)
			require.NoError(t, err)
			if stage == "rename-then-replace" {
				require.Equal(t, "edited", string(actual))
			} else {
				require.Equal(t, "source", string(actual))
			}
			after, err := Observe(path)
			require.NoError(t, err)
			ref, err := repo.Resolve(ctx, "disk", path, after)
			require.NoError(t, err)
			result, hit, err := repo.Lookup(ctx, ref, "large")
			require.NoError(t, err)
			require.Equal(t, stage == "before-intent" || stage == "after-commit", hit)
			if hit {
				require.True(t, result.Matches([]byte("generated")))
			}
		})
	}
}

func runCrashStage(dir, stage string) {
	ctx := context.Background()
	from, to := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	repo, err := Open(filepath.Join(dir, "ledger.db"))
	if err != nil {
		os.Exit(2)
	}
	v, err := Observe(from)
	if err != nil {
		os.Exit(3)
	}
	ref, err := repo.Resolve(ctx, "disk", from, v)
	if err != nil {
		os.Exit(4)
	}
	if err = repo.Publish(ctx, ref, "large", "file", []byte("generated")); err != nil {
		os.Exit(5)
	}
	if stage == "before-intent" {
		os.Exit(23)
	}
	intents, err := repo.Prepare(ctx, []Move{{Ref: ref, TargetSource: "disk", TargetPath: to}})
	if err != nil {
		os.Exit(6)
	}
	if stage == "after-intent" {
		os.Exit(23)
	}
	if err = os.Rename(from, to); err != nil {
		os.Exit(7)
	}
	if stage == "after-commit" {
		after, err := Observe(to)
		if err != nil {
			os.Exit(8)
		}
		if err = repo.CommitRename(ctx, intents[0].ID, after); err != nil {
			os.Exit(9)
		}
	}
	os.Exit(23)
}
