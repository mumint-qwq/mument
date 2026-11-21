//go:build linux && !386 && !arm && !cacheledger_portable

package preview

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/stretchr/testify/require"
)

type contentCrashState struct {
	Digests map[string]string
	Target  cacheledger.Ref
	Stage   string
}

func crashJob(ref cacheledger.Ref) string {
	b, _ := json.Marshal(ref.Version)
	return cacheledger.Digest(b) + "|asset:" + ref.Asset
}

func TestContentProcessCrashWindows(t *testing.T) {
	if os.Getenv("MUMENT_REUSE_CRASH_STAGE") != "" {
		t.Skip("subprocess owner")
	}
	for _, stage := range []string{"claimed", "hash_before_sql", "bound_before_work_cleanup"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			child := func(phase string, expected int) {
				t.Helper()
				cmd := exec.Command(os.Args[0], "-test.run", "^TestContentCrashProbe$", "-test.count=1", "-test.v")
				cmd.Env = append(os.Environ(), "MUMENT_REUSE_CRASH_FIXTURE="+dir, "MUMENT_REUSE_CRASH_STAGE="+phase, "MUMENT_REUSE_CRASH_WINDOW="+stage)
				output, err := cmd.CombinedOutput()
				code := 0
				if err != nil {
					var e *exec.ExitError
					require.ErrorAs(t, err, &e, "%s", output)
					code = e.ExitCode()
				}
				require.Equal(t, expected, code, "phase=%s\n%s", phase, output)
				t.Logf("phase=%s exit=%d output=%s", phase, code, output)
			}
			child("seed", 0)
			child("crash", 73)
			// Old trusted proof must survive the source disappearing before recovery.
			require.NoError(t, os.Remove(filepath.Join(dir, "source", "source.jpg")))
			child("recover", 0)
			child("verify", 0)
		})
	}
}

// Real process exit skips all cleanup; SQLite FULL commits and the cache lease
// are exercised by separate processes rather than a fake in-memory restart.
func TestContentCrashProbe(t *testing.T) {
	dir := os.Getenv("MUMENT_REUSE_CRASH_FIXTURE")
	phase := os.Getenv("MUMENT_REUSE_CRASH_STAGE")
	if dir == "" || phase == "" {
		t.Skip("process crash probe")
	}
	f := &orphanFixture{root: filepath.Join(dir, "source"), cache: filepath.Join(dir, "cache")}
	require.NoError(t, os.MkdirAll(f.root, 0700))
	indexing.SetTestIndex("orphan-test", f.root)
	t.Cleanup(indexing.ClearTestIndices)
	f.start(t)
	source := iteminfo.ExtendedFileInfo{Source: "orphan-test", RealPath: filepath.Join(f.root, "source.jpg"), FileInfo: iteminfo.FileInfo{ItemInfo: iteminfo.ItemInfo{Name: "source.jpg", Type: "image/jpeg", HasPreview: true}}}
	target := source
	target.Name = "copy.jpg"
	target.RealPath = filepath.Join(f.root, target.Name)
	stateFile := filepath.Join(dir, "state.json")
	if phase == "seed" {
		source = newNoisyFile(t, f.root, "source.jpg", 1600, 1200)
		source.Source = "orphan-test"
		state := contentCrashState{Digests: map[string]string{}, Stage: os.Getenv("MUMENT_REUSE_CRASH_WINDOW")}
		for _, size := range []string{"small", "xlarge"} {
			data, e := f.s.getPreviewForFile(t.Context(), source, size, "", 0)
			require.NoError(t, e)
			state.Digests[size] = cacheledger.Digest(data)
		}
		out, e := reuseTestFile(t, f.s, source, nil)
		require.NoError(t, e)
		require.Equal(t, "proven_own", out.Reason)
		bytes, e := os.ReadFile(source.RealPath)
		require.NoError(t, e)
		require.NoError(t, os.WriteFile(target.RealPath, bytes, 0600))
		plan, e := f.s.planLedgerPreview(t.Context(), target, "small")
		require.NoError(t, e)
		state.Target = plan.ref
		encoded, e := json.Marshal(state)
		require.NoError(t, e)
		require.NoError(t, os.WriteFile(stateFile, encoded, 0600))
		return
	}
	encoded, e := os.ReadFile(stateFile)
	require.NoError(t, e)
	var state contentCrashState
	require.NoError(t, json.Unmarshal(encoded, &state))
	plan, e := f.s.planLedgerPreview(t.Context(), target, "small")
	require.NoError(t, e)
	claim, e := f.s.ledger.ClaimContentWork(t.Context(), "crash-fixture", "/copy.jpg", crashJob(plan.ref))
	require.NoError(t, e)
	if phase == "crash" {
		require.True(t, claim.Claimed)
		if state.Stage == "claimed" {
			os.Exit(73)
		}
		var read int64
		out, e := reuseTestFile(t, f.s, target, func(_ context.Context, n int) error {
			read += int64(n)
			if state.Stage == "hash_before_sql" && read >= plan.version.Size {
				os.Exit(73)
			}
			return nil
		})
		require.NoError(t, e)
		require.Equal(t, "reused", out.Reason)
		require.Equal(t, "bound_before_work_cleanup", state.Stage)
		os.Exit(73)
	}
	if phase == "recover" {
		require.True(t, claim.Claimed)
		known, e := f.s.ledger.ContentProof(t.Context(), state.Target)
		require.NoError(t, e)
		if state.Stage == "bound_before_work_cleanup" {
			require.True(t, known)
		} else {
			require.False(t, known, "a completed read without SQL commit is not a proof")
		}
		out, e := reuseTestFile(t, f.s, target, nil)
		require.NoError(t, e)
		if state.Stage == "bound_before_work_cleanup" {
			require.Equal(t, "already_proven", out.Reason)
			require.Zero(t, out.Bytes)
			require.EqualValues(t, 1, out.ProofHits)
		} else {
			require.Equal(t, "reused", out.Reason)
			require.Equal(t, plan.version.Size, out.Bytes)
		}
		current, e := f.s.planLedgerPreview(t.Context(), target, "small")
		require.NoError(t, e)
		_, e = f.s.ledger.ClaimContentWork(t.Context(), "crash-fixture", "/copy.jpg", crashJob(current.ref))
		require.NoError(t, e)
		require.NoError(t, f.s.ledger.FinishContentWork(t.Context(), "crash-fixture", "/copy.jpg", crashJob(current.ref), true))
	} else {
		require.Equal(t, "verify", phase)
		require.False(t, claim.Claimed)
		require.Equal(t, "completed", claim.Reason)
	}
	for _, size := range []string{"small", "xlarge"} {
		data, e := f.s.getPreviewForFile(t.Context(), target, size, "", 0)
		require.NoError(t, e)
		require.Equal(t, state.Digests[size], cacheledger.Digest(data))
	}
	require.Zero(t, f.s.Stats().FullDecodes)
}
