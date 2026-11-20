//go:build linux

package cachetrial

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	dbsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// When set, keep only the synthetic inputs in this already-created empty directory.
var fixtureOutput = flag.String("trial-fixture-output", "", "retain inputs for command-line replay")

type fixture struct {
	opt       Options
	cache     *diskcache.FileCache
	db        *sql.DB
	ledger    string
	ev        evidence
	generated time.Time
}

func newFixture(t *testing.T, dir string) *fixture {
	opt := Options{CacheDir: filepath.Join(dir, "cache"), Snapshot: filepath.Join(dir, "snapshot.sqlite"),
		FixtureEvidence: filepath.Join(dir, "evidence.json"), MaxEntries: 256, MaxBytes: 1 << 20,
		MaxTime: 10 * time.Second, Samples: 50}
	fileutils.SetFsPermissions(0o600, 0o700)
	cache, err := diskcache.NewFileCache(opt.CacheDir)
	require.NoError(t, err)
	live := filepath.Join(dir, "ledger.sqlite")
	ledger, err := cacheledger.Open(live)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ledger.Close()) })
	db, err := sql.Open(dbsql.SqliteDriver, live)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return &fixture{opt, cache, db, live, evidence{Scope: "synthetic", Publications: map[string]observation{}}, time.Now().Add(-12 * 24 * time.Hour).UTC()}
}

func (f *fixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	_, err := f.db.ExecContext(t.Context(), query, args...)
	require.NoError(t, err)
}

func (f *fixture) store(t *testing.T, key, contents string) string {
	t.Helper()
	require.NoError(t, f.cache.Store(t.Context(), key, []byte(contents)))
	return filepath.Join(f.opt.CacheDir, "diskcache", diskcache.RelativePath(key))
}

func (f *fixture) publication(t *testing.T, asset, contents, state string, refs int, used bool) publication {
	t.Helper()
	p := publication{asset: asset, variant: "raster-v1/test", key: "c-raster/" + asset + "/raster-v1/test",
		size: int64(len(contents)), digest: cacheledger.Digest([]byte(contents)), generated: f.generated.UnixNano()}
	var ref *fixtureRef
	f.exec(t, "INSERT INTO assets VALUES(?)", asset)
	for i := range refs {
		ref = &fixtureRef{Asset: asset, Source: "synthetic-fsid/1:2", Path: fmt.Sprintf("/%s-%d.png", asset, i), Version: "synthetic-version"}
		f.exec(t, "INSERT INTO refs VALUES(?,?,?,?)", ref.Source, ref.Path, ref.Version, ref.Asset)
	}
	if used {
		p.used = f.generated.UnixNano()
	}
	f.exec(t, "INSERT INTO results VALUES(?,?,?,?,?,?,?)", p.asset, p.variant, p.key, p.size, p.digest, p.generated, p.used)
	if state != "" {
		if state != "version_current" {
			ref = nil
		}
		f.ev.Publications[p.id()] = observation{State: state, At: f.generated.Add(time.Hour), Reference: ref}
	}
	return p
}

func (f *fixture) seal(t *testing.T) {
	t.Helper()
	// The writer is open, with committed rows in WAL. SQLite builds the
	// standalone consistent copy and creates any output only in the fixture.
	f.exec(t, "VACUUM INTO ?", f.opt.Snapshot)
	raw, err := json.MarshalIndent(f.ev, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(f.opt.FixtureEvidence, raw, 0o600))
}

func TestTrialSynthetic(t *testing.T) {
	dir := *fixtureOutput
	if dir == "" {
		dir = t.TempDir()
	} else {
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Empty(t, entries)
	}
	f := newFixture(t, dir)
	add := func(asset, contents, state string, refs int, used bool) publication {
		p := f.publication(t, asset, contents, state, refs, used)
		f.store(t, p.key, contents)
		return p
	}
	add("single", "single", "version_current", 1, false)
	add("shared", "shared", "version_current", 2, true)
	retired := add("retired", "retired", "version_retired", 0, false)
	orphan := add("orphan", "orphan", "no_reference", 0, false)
	recent := add("recently-detached", "recent", "no_reference", 0, false)
	f.ev.Publications[recent.id()] = observation{State: "no_reference", At: time.Now().Add(-time.Hour).UTC()}
	add("remaining-reference", "still referenced", "no_reference", 1, false)
	add("empty", "no evidence", "", 0, false)
	add("unmapped", "unmapped", "", 1, false)
	for _, state := range []string{"offline", "identity_changed", "permission", "scan_incomplete", "index_stale"} {
		add(state, state, state, 1, false)
	}
	pending := add("pending", "pending", "no_reference", 0, false)
	f.exec(t, "INSERT INTO moves VALUES('pending','s','/a','s','/b','v','another',?,'v','pending',?)", pending.asset, pending.generated)
	ambiguous := add("ambiguous", "ambiguous", "no_reference", 0, false)
	f.exec(t, "INSERT INTO moves VALUES('ambiguous','s','/a','s','/b','v',?,'','','ambiguous',?)", ambiguous.asset, pending.generated)
	f.publication(t, "missing", "missing", "", 1, false)
	mismatch := f.publication(t, "mismatch", "mismatch", "", 1, false)
	f.store(t, mismatch.key, "other")
	digest := f.publication(t, "same-size-replacement", "abcdef", "no_reference", 0, false)
	f.store(t, digest.key, "fedcba")
	aliased := add("aliased", "same bytes", "no_reference", 0, false)
	other := f.publication(t, "alias-with-reference", "same bytes", "", 1, false)
	f.exec(t, "UPDATE results SET storage_key=? WHERE asset=?", aliased.key, other.asset)
	hard := add("hardlink", "hardlink", "no_reference", 0, false)
	old := f.store(t, "legacy-cache-hash", "old")
	unregisteredKey := "c-raster/unregistered/raster-v1/test"
	f.store(t, unregisteredKey, "unregistered")
	f.ev.Unregistered = []string{anonymous(diskcache.RelativePath(unregisteredKey))}
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(old), ".preview-interrupted"), []byte("tmp"), 0o600))

	link := filepath.Join(f.opt.CacheDir, "diskcache", diskcache.RelativePath("hardlink-alias"))
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o700))
	require.NoError(t, os.Link(filepath.Join(f.opt.CacheDir, "diskcache", diskcache.RelativePath(hard.key)), link))
	outside := filepath.Join(dir, "synthetic-original.png")
	require.NoError(t, os.WriteFile(outside, []byte("outside-cache"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(filepath.Dir(old), strings.Repeat("x", 40))))
	symlink := filepath.Join(f.opt.CacheDir, "diskcache", diskcache.RelativePath("symlink-preview"))
	require.NoError(t, os.MkdirAll(filepath.Dir(symlink), 0o700))
	require.NoError(t, os.Symlink(outside, symlink))
	f.seal(t)

	before := fingerprint(t, dir)
	report, err := Run(t.Context(), f.opt)
	require.NoError(t, err)
	require.Equal(t, before, fingerprint(t, dir))
	require.Equal(t, "synthetic_fixture", report.EvidenceScope)
	require.True(t, report.Ledger.Coverage)
	require.True(t, report.Inventory.Complete)
	require.False(t, report.Inventory.Coverage)
	require.Equal(t, "partial", report.Stop) // The symlink is an explicit scope gap.
	require.Equal(t, 2, report.EstimatedCandidates)
	require.Equal(t, retired.size+orphan.size, report.EstimatedLogical)
	require.GreaterOrEqual(t, report.EstimatedAllocated, report.EstimatedLogical)
	require.Zero(t, report.ExecutableDeletes)
	require.Equal(t, 2, report.Ledger.Classes["valid_referenced"].Count)
	require.Equal(t, 1, report.Ledger.Classes["valid_referenced"].ForegroundUsed)
	require.Equal(t, 2, report.Ledger.Reasons["move_pending_or_ambiguous"])
	require.Equal(t, 1, report.Ledger.Classes["file_missing"].Count)
	require.Equal(t, 1, report.Ledger.Classes["size_mismatch"].Count)
	for _, state := range []string{"offline", "identity_changed", "permission", "scan_incomplete", "index_stale",
		"location_not_resolved", "no_retirement_or_reference_coverage", "reference_present_or_evidence_predates_publication"} {
		require.Positive(t, report.Ledger.Reasons[state], state)
	}
	seen := map[string]string{}
	for _, sample := range report.Ledger.Samples {
		seen[sample.ID] = sample.Estimate
	}
	require.Equal(t, "grace_unelapsed", seen[recent.id()])
	require.Equal(t, "shared_object_protected", seen[aliased.id()])
	require.Equal(t, "hardlink_or_unavailable", seen[hard.id()])
	require.Equal(t, "publication_bytes_unverified", seen[digest.id()])
	require.Equal(t, 1, report.Inventory.Classes["unregistered_publication"].Count)
	require.Equal(t, 2, report.Inventory.Classes["unattributed_hash"].Count)
	require.Equal(t, int64(len("old")+len("hardlink")), report.Inventory.Classes["unattributed_hash"].LogicalBytes)
	require.Equal(t, int64(3+len("unregistered")+len("old")), report.Inventory.LogicalBytes-report.Ledger.LogicalBytes)
	require.Equal(t, 1, report.Inventory.Classes["temporary"].Count)
	require.Equal(t, 1, report.Inventory.Reasons["symlink"])

	f.opt.FixtureEvidence = ""
	repeated, err := Run(t.Context(), f.opt)
	require.NoError(t, err)
	require.Zero(t, repeated.EstimatedCandidates)
	require.Nil(t, repeated.Ledger.Classes["valid_referenced"])
	require.Equal(t, 3, repeated.Inventory.Classes["unattributed_hash"].Count)
	require.Equal(t, before, fingerprint(t, dir))
	live := f.opt
	live.Snapshot = f.ledger
	_, err = Run(t.Context(), live)
	require.ErrorContains(t, err, "sidecars")
	require.Equal(t, before, fingerprint(t, dir))
}

type fileFingerprint struct {
	Digest            [32]byte
	Size              int64
	Mode              fs.FileMode
	Modified, Changed unix.Timespec
}

func fingerprint(t *testing.T, root string) map[string]fileFingerprint {
	t.Helper()
	out := make(map[string]fileFingerprint)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		var stat unix.Stat_t
		require.NoError(t, unix.Lstat(path, &stat))
		info, err := d.Info()
		if err != nil {
			return err
		}
		fp := fileFingerprint{Size: stat.Size, Mode: info.Mode(), Modified: stat.Mtim, Changed: stat.Ctim}
		if d.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fp.Digest = sha256.Sum256(data)
		}
		out[path] = fp
		return nil
	})
	require.NoError(t, err)
	return out
}

func TestTrialBoundedPages(t *testing.T) {
	f := newFixture(t, t.TempDir())
	tx, err := f.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	for i := range 1280 {
		id := fmt.Sprintf("asset-%d", i)
		_, err = tx.Exec("INSERT INTO assets VALUES(?)", id)
		require.NoError(t, err)
		_, err = tx.Exec("INSERT INTO results VALUES(?,'v',?,1,?,1,0)", id, id, strings.Repeat("a", 64))
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
	shard := filepath.Join(f.opt.CacheDir, "diskcache", "a", "bc")
	require.NoError(t, os.MkdirAll(shard, 0o700))
	for i := range 1280 {
		require.NoError(t, os.WriteFile(filepath.Join(shard, fmt.Sprintf("abc%037x", i)), []byte("x"), 0o600))
	}
	f.seal(t)
	db, _, _, err := openSnapshot(f.opt.Snapshot)
	require.NoError(t, err)
	defer db.Close()
	for _, tc := range []struct {
		query, index string
		args         []any
	}{
		{resultPage, "INTEGER PRIMARY KEY", []any{0, 8}},
		{resultPage, "refs_asset", []any{0, 8}},
		{movePage, "moves_pending", []any{"pending", 8}},
	} {
		rows, err := db.Query("EXPLAIN QUERY PLAN "+tc.query, tc.args...)
		require.NoError(t, err)
		var plan strings.Builder
		for rows.Next() {
			var a, b, c int
			var detail string
			require.NoError(t, rows.Scan(&a, &b, &c, &detail))
			fmt.Fprintln(&plan, detail)
		}
		require.NoError(t, rows.Err())
		rows.Close()
		require.Contains(t, plan.String(), tc.index)
		require.NotContains(t, plan.String(), "TEMP B-TREE")
		t.Log(plan.String())
	}

	opt := f.opt
	opt.MaxEntries, opt.MaxBytes, opt.Samples = 99, 80, 0
	var records, files int
	for page := 0; page < 30; page++ {
		result, err := Run(t.Context(), opt)
		require.NoError(t, err)
		require.Zero(t, result.EstimatedCandidates)
		require.LessOrEqual(t, result.Ledger.Visited, 99)
		require.LessOrEqual(t, result.Inventory.Visited, 99)
		require.LessOrEqual(t, result.Inventory.LogicalBytes, int64(80))
		require.Empty(t, result.Inventory.Samples)
		records += result.Ledger.Visited
		if c := result.Inventory.Classes["unattributed"]; c != nil {
			files += c.Count
		}
		if result.Ledger.Complete && result.Inventory.Complete {
			break
		}
		require.NotEqual(t, opt.AfterCache+fmt.Sprint(opt.AfterResult), result.Inventory.NextCache+fmt.Sprint(result.Ledger.NextRow))
		opt.AfterCache, opt.AfterResult, opt.ExpectSnapshot = result.Inventory.NextCache, result.Ledger.NextRow, result.SnapshotID
	}
	require.Equal(t, 1280, records)
	require.Equal(t, 1280, files)
	opt.ExpectSnapshot = strings.Repeat("0", 64)
	_, err = Run(t.Context(), opt)
	require.ErrorContains(t, err, "snapshot identity")

	root, err := openDisk(opt.CacheDir)
	require.NoError(t, err)
	defer unix.Close(root)
	w, err := newWalker(root, "")
	require.NoError(t, err)
	defer w.close()
	_, err = w.next(t.Context())
	require.NoError(t, err)
	cursor := w.cursor()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	section := newSection()
	scanInventory(ctx, opt, &section, w, nil, false, evidence{})
	require.Equal(t, "interrupted", section.Stop)
	require.Equal(t, cursor, section.NextCache)
	require.NoError(t, os.Mkdir(filepath.Join(opt.CacheDir, "diskcache", "b"), 0o700))
	_, err = newWalker(root, cursor)
	require.ErrorContains(t, err, "no longer matches")
}

func TestTrialScopeAndOversize(t *testing.T) {
	f := newFixture(t, t.TempDir())
	oversized := f.publication(t, "oversize", "123456789", "", 0, false)
	f.store(t, oversized.key, "123456789")
	next := f.publication(t, "next", "ab", "", 0, false)
	f.store(t, next.key, "ab")
	f.seal(t)

	root := filepath.Join(f.opt.CacheDir, "diskcache")
	deniedName := "a"
	// At most two of the three shard names belong to these publications.
	for deniedName == diskcache.RelativePath(oversized.key)[:1] || deniedName == diskcache.RelativePath(next.key)[:1] {
		deniedName = string(deniedName[0] + 1)
	}
	denied := filepath.Join(root, deniedName)
	require.NoError(t, os.Mkdir(denied, 0o700))
	require.NoError(t, os.Chmod(denied, 0))
	t.Cleanup(func() { require.NoError(t, os.Chmod(denied, 0o700)) })

	f.opt.MaxBytes = 8
	report, err := Run(t.Context(), f.opt)
	require.NoError(t, err)
	require.Equal(t, "partial", report.Stop)
	require.Equal(t, 1, report.Ledger.Reasons["object_exceeds_byte_limit"])
	require.Equal(t, 1, report.Inventory.Reasons["object_exceeds_byte_limit"])
	require.Equal(t, int64(2), report.Ledger.RecordedBytes)
	require.Equal(t, int64(2), report.Inventory.LogicalBytes)
	if os.Geteuid() != 0 {
		require.Positive(t, report.Inventory.Reasons["permission"])
	}

	alias := filepath.Join(filepath.Dir(f.opt.CacheDir), "cache-symlink")
	require.NoError(t, os.Symlink(f.opt.CacheDir, alias))
	f.opt.CacheDir = alias
	_, err = Run(t.Context(), f.opt)
	require.ErrorContains(t, err, "dedicated diskcache: symlink")
}

func TestTrialLedgerDiskCoverageAfterInventoryDone(t *testing.T) {
	dir := t.TempDir()
	f := newFixture(t, dir)
	p := f.publication(t, "ledger-probe", "abcd", "", 0, false)
	path := f.store(t, p.key, "abcd")
	f.seal(t)

	clean, err := Run(t.Context(), f.opt)
	require.NoError(t, err)
	require.Empty(t, clean.Stop)
	require.True(t, clean.Ledger.Coverage)
	require.True(t, clean.Ledger.ResultKeyCoverage)
	require.Equal(t, "done", clean.Inventory.NextCache)

	opt := f.opt
	opt.ExpectSnapshot = clean.SnapshotID
	opt.AfterCache = clean.Inventory.NextCache

	outside := filepath.Join(dir, "outside-cache")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o600))
	for _, tc := range []struct {
		name, class, reason string
		size                int64
		gap                 bool
		replace             func(*testing.T)
	}{
		{"regular", "uncertain", "no_retirement_or_reference_coverage", 4, false, func(t *testing.T) {
			require.NoError(t, os.WriteFile(path, []byte("abcd"), 0o600))
		}},
		{"symlink", "uncertain", "symlink", 0, true, func(t *testing.T) {
			require.NoError(t, os.Symlink(outside, path))
		}},
		{"directory", "uncertain", "not_regular", 0, true, func(t *testing.T) {
			require.NoError(t, os.Mkdir(path, 0o700))
		}},
		{"wrong size", "size_mismatch", "recorded_size_differs", 2, false, func(t *testing.T) {
			require.NoError(t, os.WriteFile(path, []byte("ab"), 0o600))
		}},
		{"missing", "file_missing", "missing", 0, false, func(*testing.T) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.Remove(path); err != nil {
				require.ErrorIs(t, err, os.ErrNotExist)
			}
			tc.replace(t)
			report, err := Run(t.Context(), opt)
			require.NoError(t, err)

			require.True(t, report.Ledger.Complete)
			require.True(t, report.Ledger.ResultKeyCoverage)
			require.Equal(t, !tc.gap, report.Ledger.Coverage)
			require.Equal(t, tc.gap, report.Ledger.Gaps)
			require.Equal(t, map[string]int{tc.reason: 1}, report.Ledger.Reasons)
			require.Equal(t, 1, report.Ledger.Classes[tc.class].Count)
			require.Equal(t, tc.size, report.Ledger.LogicalBytes)
			require.Equal(t, p.size, report.Ledger.RecordedBytes)
			require.Equal(t, clean.Ledger.NextRow, report.Ledger.NextRow)
			require.True(t, report.Inventory.Complete)
			require.False(t, report.Inventory.Gaps)
			require.Zero(t, report.Inventory.Visited)
			require.Zero(t, report.EstimatedCandidates)
			require.Zero(t, report.ExecutableDeletes)
			if tc.gap {
				require.Equal(t, "partial", report.Stop)
			} else {
				require.Empty(t, report.Stop)
			}
		})
	}
	data, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "outside", string(data))
}
