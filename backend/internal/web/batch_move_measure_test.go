package web

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/files"
	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	indexsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/internal/database/users"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

// This file is an isolated measurement harness for bulk move index work. It is
// skipped unless BATCH_MOVE_MEASURE=1 so it never runs in ordinary test sweeps.
// It uses real temp files, the real index SQLite database and a real HTTP
// server over loopback; it never touches production data, sources or accounts.

type measureScenario struct {
	Name       string
	Files      int
	Move       int
	DstPreload int
	Rounds     int
}

type measureGetSample struct {
	Path     string  `json:"path"`
	ClientMs float64 `json:"client_ms"`
	Items    int     `json:"items"`
	Status   int     `json:"status"`
	Error    string  `json:"error,omitempty"`
	AtMs     float64 `json:"at_ms"`
}

type measureRound struct {
	Build      string `json:"build"`
	Scenario   string `json:"scenario"`
	Round      int    `json:"round"`
	Files      int    `json:"files"`
	Moved      int    `json:"moved"`
	DstPreload int    `json:"dst_preload"`

	PatchStatus    int     `json:"patch_status"`
	PatchClientMs  float64 `json:"patch_client_ms"`
	PatchSucceeded int     `json:"patch_succeeded"`
	PatchFailed    int     `json:"patch_failed"`

	IdleGetSamples     []measureGetSample `json:"idle_get_samples"`
	IdleGetMedianMs    float64            `json:"idle_get_median_ms"`
	FirstGetAfterPatch measureGetSample   `json:"first_get_after_patch"`
	FirstGetDest       measureGetSample   `json:"first_get_destination"`
	GetsDuringWork     []measureGetSample `json:"gets_during_background"`

	FirstCorrectGetMs float64 `json:"first_correct_get_ms"`
	IndexConvergedMs  float64 `json:"index_converged_ms"`
	BackgroundIdleMs  float64 `json:"background_idle_ms"`
	// CountsMatch records whether the index reached the expected row counts.
	// The baseline can go idle while keeping stale source rows, so idleness and
	// correctness are reported separately instead of waiting for a baseline
	// that will never converge.
	CountsMatch      bool   `json:"index_counts_match"`
	TimedOut         bool   `json:"timed_out"`
	ConvergenceNotes string `json:"convergence_notes,omitempty"`

	SourceCount     int `json:"db_source_count"`
	DestCount       int `json:"db_destination_count"`
	StaleSourceRows int `json:"stale_source_rows"`

	Refresh    indexing.RefreshProbeStats `json:"refresh_probe"`
	Work       files.IndexWorkStats       `json:"index_work_probe"`
	DB         indexsql.SQLMetrics        `json:"db_metrics"`
	Goroutines int                        `json:"process_goroutines"`
}

// measureResultsDir mirrors BATCH_MOVE_RESULTS_DIR for the progress logger.
var measureResultsDir string

func TestBatchMoveIndexMeasurement(t *testing.T) {
	if os.Getenv("BATCH_MOVE_MEASURE") != "1" {
		t.Skip("set BATCH_MOVE_MEASURE=1 to run the isolated bulk move measurement")
	}
	build := os.Getenv("BATCH_MOVE_BUILD")
	if build == "" {
		build = "unknown"
	}
	resultsDir := os.Getenv("BATCH_MOVE_RESULTS_DIR")
	if resultsDir == "" {
		t.Fatal("BATCH_MOVE_RESULTS_DIR is required")
	}
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	measureResultsDir = resultsDir
	scenarios := []measureScenario{
		{Name: "a1913_move267", Files: 1913, Move: 267, Rounds: 3},
		{Name: "a2110_move197", Files: 2110, Move: 197, Rounds: 3},
		{Name: "a4000_move267_dst_empty", Files: 4000, Move: 267, Rounds: 3},
		{Name: "a4000_move267_dst4000", Files: 4000, Move: 267, DstPreload: 4000, Rounds: 3},
		{Name: "single_file", Files: 1, Move: 1, Rounds: 1},
	}
	if filter := os.Getenv("BATCH_MOVE_SCENARIOS"); filter != "" {
		wanted := map[string]bool{}
		for _, name := range bytes.Split([]byte(filter), []byte(",")) {
			wanted[string(bytes.TrimSpace(name))] = true
		}
		kept := scenarios[:0]
		for _, sc := range scenarios {
			if wanted[sc.Name] {
				kept = append(kept, sc)
			}
		}
		scenarios = kept
	}
	for _, sc := range scenarios {
		for round := 1; round <= sc.Rounds; round++ {
			result := runMeasureRound(t, build, sc, round)
			name := fmt.Sprintf("%s-%s-round%d.json", build, sc.Name, round)
			path := filepath.Join(resultsDir, name)
			logMeasureProgress(resultsDir, build, sc, round, "round finished, writing %s", name)
			data, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("measurement %s round %d: patch=%.1fms first_get=%.1fms idle=%.1fms converged=%.1fms counts_match=%v timed_out=%v",
				sc.Name, round, result.PatchClientMs, result.FirstGetAfterPatch.ClientMs, result.BackgroundIdleMs, result.IndexConvergedMs, result.CountsMatch, result.TimedOut)
		}
	}
}

var measurePreviewOnce sync.Once

// logMeasureProgress appends one timestamped line per stage of a round. It is
// how a slow or stalled baseline run can be diagnosed without attaching to the
// test process.
func logMeasureProgress(resultsDir, build string, sc measureScenario, round int, format string, args ...any) {
	path := filepath.Join(resultsDir, fmt.Sprintf("progress-%s-%s-round%d.log", build, sc.Name, round))
	line := fmt.Sprintf("%s %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = file.WriteString(line)
	_ = file.Close()
}

func runMeasureRound(t *testing.T, build string, sc measureScenario, round int) measureRound {
	t.Helper()
	uploadTestResolverMu.Lock()
	defer uploadTestResolverMu.Unlock()
	logMeasureProgress(measureResultsDir, build, sc, round, "round start")
	defer func() { logMeasureProgress(measureResultsDir, build, sc, round, "round return") }()

	prevFilePerm := fileutils.PermFile
	prevDirPerm := fileutils.PermDir
	fileutils.SetFsPermissions(0o644, 0o755)

	// patchAction deletes thumbnails; the preview service must exist even
	// though this source has no previews.
	measurePreviewOnce.Do(func() {
		cacheDir, err := os.MkdirTemp("", "batch-move-measure-preview")
		if err == nil {
			_ = preview.StartPreviewGenerator(1, cacheDir)
		}
	})
	prevCacheDir := settings.Config.Server.CacheDir
	prevSourceMap := settings.Config.Server.SourceMap
	prevNameToSource := settings.Config.Server.NameToSource
	prevSourceResolver := users.GetSourceNameResolver()
	prevSourceConfig := users.GetSourceConfig()
	root := t.TempDir()
	cacheDir := filepath.Join(root, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		fileutils.SetFsPermissions(uint32(prevFilePerm), uint32(prevDirPerm))
		settings.Config.Server.CacheDir = prevCacheDir
		settings.Config.Server.SourceMap = prevSourceMap
		settings.Config.Server.NameToSource = prevNameToSource
		users.SetSourceNameResolver(prevSourceResolver)
		users.SetSourceConfig(prevSourceConfig)
	})

	sourceName := "batchmove"
	sourcePath := filepath.Join(root, "src")
	if err := os.MkdirAll(filepath.Join(sourcePath, "A"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sourcePath, "B"), 0o755); err != nil {
		t.Fatal(err)
	}
	logMeasureProgress(measureResultsDir, build, sc, round, "dataset root %s", root)
	templateFile := filepath.Join(root, "template.bin")
	if err := os.WriteFile(templateFile, []byte("synthetic"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < sc.Files; i++ {
		makeHardlink(t, templateFile, filepath.Join(sourcePath, "A", fmt.Sprintf("f-%06d.jpg", i)))
	}
	for i := 0; i < sc.DstPreload; i++ {
		makeHardlink(t, templateFile, filepath.Join(sourcePath, "B", fmt.Sprintf("g-%06d.jpg", i)))
	}

	settings.Config.Server.CacheDir = cacheDir
	settings.Config.Server.SourceMap = map[string]*settings.Source{sourcePath: {Path: sourcePath, Name: sourceName}}
	settings.Config.Server.NameToSource = map[string]*settings.Source{sourceName: settings.Config.Server.SourceMap[sourcePath]}
	users.SetSourceNameResolver(func(name string) (string, error) {
		if name == sourceName {
			return sourcePath, nil
		}
		return "", fmt.Errorf("unknown source %q", name)
	})
	users.SetSourceConfig(&users.SourceConfigProvider{
		GetSourceByPath: func(path string) (users.SourceInfo, bool) {
			if path == sourcePath {
				return users.SourceInfo{Path: sourcePath, Name: sourceName}, true
			}
			return users.SourceInfo{}, false
		},
		GetSourceByName: func(name string) (users.SourceInfo, bool) {
			if name == sourceName {
				return users.SourceInfo{Path: sourcePath, Name: sourceName}, true
			}
			return users.SourceInfo{}, false
		},
	})

	db, _, err := indexsql.NewIndexDB("measure", "OFF", 1000, 32, true)
	if err != nil {
		t.Fatalf("index db: %v", err)
	}
	indexing.SetIndexDBForTesting(db)
	idx := indexing.SetTestIndexWithDB(sourceName, sourcePath, db)

	logMeasureProgress(measureResultsDir, build, sc, round, "dataset ready files=%d preload=%d", sc.Files, sc.DstPreload)
	// Each round gets its own database and scan state. Wait for this round's
	// scans to settle, then detach the index and close the database so a slow
	// baseline round cannot leak work into the next one.
	defer func() {
		logMeasureProgress(measureResultsDir, build, sc, round, "cleanup: waiting for scans")
		waitForRefreshProbeIdle(t, 300*time.Second)
		logMeasureProgress(measureResultsDir, build, sc, round, "cleanup: scans idle, closing db")
		indexing.ClearTestIndices()
		time.Sleep(50 * time.Millisecond)
		indexing.SetIndexDBForTesting(nil)
		_ = db.Close()
		logMeasureProgress(measureResultsDir, build, sc, round, "cleanup: db closed")
		indexing.SetRefreshProbeEnabled(false)
		files.SetIndexWorkProbeEnabled(false)
		indexsql.SetMetricsEnabled(false)
	}()

	perms := users.SourceFilePermissions{View: true, Download: true, Modify: true, Create: true, Delete: true}
	user := &users.User{
		ID:                       1,
		FrontendUser:             users.FrontendUser{Username: "measurer"},
		BackendScopes:            []users.BackendScope{{Path: sourcePath, Scope: "/", Permissions: perms}},
		BackendSourcePermissions: map[string]users.SourceFilePermissions{sourcePath: perms},
		Version:                  users.SourcePermissionsMigrationVersion,
	}

	// Initial index: outside the measured window.
	if err := idx.RefreshDirectory("/A/", false); err != nil {
		t.Fatalf("initial index A: %v", err)
	}
	if sc.DstPreload > 0 {
		if err := idx.RefreshDirectory("/B/", false); err != nil {
			t.Fatalf("initial index B: %v", err)
		}
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d := &Context{User: user}
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/resources":
			_, _ = ResourcePatchHandler(w, r, d)
		case r.Method == http.MethodGet && r.URL.Path == "/api/resources":
			_, _ = resourceGetHandler(w, r, d)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	// A listing can legitimately take tens of seconds on the baseline; cap it
	// so one pathological sample cannot block the whole round.
	client := &http.Client{Timeout: 60 * time.Second}

	getURL := func(path string) string {
		return srv.URL + "/api/resources?source=" + url.QueryEscape(sourceName) + "&path=" + url.QueryEscape(path)
	}
	doGet := func(path string) measureGetSample {
		start := time.Now()
		sample := measureGetSample{Path: path}
		resp, err := client.Get(getURL(path))
		sample.ClientMs = float64(time.Since(start).Microseconds()) / 1000
		if err != nil {
			sample.Error = err.Error()
			return sample
		}
		defer resp.Body.Close()
		sample.Status = resp.StatusCode
		var body struct {
			Files   []struct{ Name string } `json:"files"`
			Folders []struct{ Name string } `json:"folders"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			sample.Error = err.Error()
			return sample
		}
		sample.Items = len(body.Files) + len(body.Folders)
		return sample
	}

	// Warm up the loopback chain and the real path caches.
	for i := 0; i < 3; i++ {
		if s := doGet("/A/"); s.Error != "" {
			t.Fatalf("warmup get: %v", s.Error)
		}
	}
	logMeasureProgress(measureResultsDir, build, sc, round, "initial index done")
	indexing.SetRefreshProbeEnabled(true)
	files.SetIndexWorkProbeEnabled(true)
	indexsql.SetMetricsEnabled(true)

	logMeasureProgress(measureResultsDir, build, sc, round, "warmup done")
	result := measureRound{Build: build, Scenario: sc.Name, Round: round, Files: sc.Files, Moved: sc.Move, DstPreload: sc.DstPreload}
	for i := 0; i < 5; i++ {
		s := doGet("/A/")
		result.IdleGetSamples = append(result.IdleGetSamples, s)
	}
	result.IdleGetMedianMs = medianSample(result.IdleGetSamples)

	items := make([]map[string]string, 0, sc.Move)
	for i := 0; i < sc.Move; i++ {
		name := fmt.Sprintf("f-%06d.jpg", i)
		items = append(items, map[string]string{
			"fromSource": sourceName, "fromPath": "/A/" + name,
			"toSource": sourceName, "toPath": "/B/" + name,
		})
	}
	payload, err := json.Marshal(map[string]any{"action": "move", "items": items})
	if err != nil {
		t.Fatal(err)
	}
	logMeasureProgress(measureResultsDir, build, sc, round, "patch start items=%d", len(items))
	patchStart := time.Now()
	resp, err := client.Do(mustRequest(t, http.MethodPatch, srv.URL+"/api/resources", payload))
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	result.PatchStatus = resp.StatusCode
	var patchBody struct {
		Succeeded []json.RawMessage `json:"succeeded"`
		Failed    []json.RawMessage `json:"failed"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&patchBody)
	resp.Body.Close()
	result.PatchClientMs = float64(time.Since(patchStart).Microseconds()) / 1000
	result.PatchSucceeded = len(patchBody.Succeeded)
	result.PatchFailed = len(patchBody.Failed)
	patchDone := time.Now()

	logMeasureProgress(measureResultsDir, build, sc, round, "patch done status=%d ms=%.1f ok=%d failed=%d", result.PatchStatus, result.PatchClientMs, result.PatchSucceeded, result.PatchFailed)
	result.FirstGetAfterPatch = doGet("/A/")
	logMeasureProgress(measureResultsDir, build, sc, round, "first get A ms=%.1f items=%d err=%q", result.FirstGetAfterPatch.ClientMs, result.FirstGetAfterPatch.Items, result.FirstGetAfterPatch.Error)
	result.FirstGetDest = doGet("/B/")
	logMeasureProgress(measureResultsDir, build, sc, round, "first get B ms=%.1f items=%d err=%q", result.FirstGetDest.ClientMs, result.FirstGetDest.Items, result.FirstGetDest.Error)
	expectedSource := sc.Files - sc.Move
	expectedDest := sc.DstPreload + sc.Move
	if result.FirstGetAfterPatch.Error == "" && result.FirstGetAfterPatch.Items == expectedSource {
		result.FirstCorrectGetMs = float64(time.Since(patchStart).Microseconds()) / 1000
	}

	// Poll while background index work runs, recording every listing latency.
	// Generous cap: the baseline must be allowed to finish hundreds of
	// serialized whole-directory transactions; a timeout is recorded, never
	// hidden.
	deadline := time.Now().Add(900 * time.Second)
	var lastA, lastB int
	stableSince := time.Time{}
	converged := false
	// Sample listings while background work runs, but stop after a fixed
	// number: every listing itself refreshes the directory (existing product
	// behaviour), so sampling forever would dominate the very work it measures.
	const backgroundListingSamples = 5
	iterations := 0
	for time.Now().Before(deadline) {
		iterations++
		if len(result.GetsDuringWork) < backgroundListingSamples {
			s := doGet("/A/")
			if s.Error == "" {
				result.GetsDuringWork = append(result.GetsDuringWork, s)
			}
			logMeasureProgress(measureResultsDir, build, sc, round, "background get ms=%.1f items=%d err=%q active=%d", s.ClientMs, s.Items, s.Error, indexing.RefreshProbeSnapshot().Active)
		}
		if iterations%20 == 0 {
			logMeasureProgress(measureResultsDir, build, sc, round, "poll iteration=%d active=%d", iterations, indexing.RefreshProbeSnapshot().Active)
		}
		dbA, dbB, err := directoryCounts(db, sourceName)
		if err == nil {
			lastA, lastB = dbA, dbB
			probe := indexing.RefreshProbeSnapshot()
			if probe.Active == 0 {
				if stableSince.IsZero() {
					stableSince = time.Now()
				} else if time.Since(stableSince) >= 300*time.Millisecond {
					result.BackgroundIdleMs = float64(time.Since(patchDone).Milliseconds())
					if dbA == expectedSource && dbB == expectedDest {
						result.CountsMatch = true
						result.IndexConvergedMs = result.BackgroundIdleMs
					} else {
						result.ConvergenceNotes = fmt.Sprintf("background idle but index counts differ: A=%d want %d, B=%d want %d", dbA, expectedSource, dbB, expectedDest)
					}
					converged = true
					break
				}
			} else {
				stableSince = time.Time{}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !converged {
		result.TimedOut = true
		result.BackgroundIdleMs = float64(time.Since(patchDone).Milliseconds())
		result.ConvergenceNotes = fmt.Sprintf("convergence timeout after %.0fms; last counts A=%d B=%d", result.BackgroundIdleMs, lastA, lastB)
	}

	logMeasureProgress(measureResultsDir, build, sc, round, "idle=%v idle_ms=%.1f counts_match=%v timed_out=%v", converged, result.BackgroundIdleMs, result.CountsMatch, result.TimedOut)
	// Stop depending on background work, then take the final snapshots.
	waitForRefreshProbeIdle(t, 60*time.Second)
	result.Refresh = indexing.RefreshProbeSnapshot()
	result.Work = files.IndexWorkProbeSnapshot()
	result.DB = indexsql.MetricsSnapshot()
	result.Goroutines = runtime.NumGoroutine()

	aCount, bCount, err := directoryCounts(db, sourceName)
	if err == nil {
		result.SourceCount = aCount
		result.DestCount = bCount
	}
	stale := 0
	for i := 0; i < sc.Move; i++ {
		item, err := db.GetItem(sourceName, fmt.Sprintf("/A/f-%06d.jpg", i))
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if item != nil || err != nil {
			stale++
		}
	}
	result.StaleSourceRows = stale
	return result
}

func mustRequest(t *testing.T, method, target string, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func makeHardlink(t *testing.T, templateFile, target string) {
	t.Helper()
	if err := os.Link(templateFile, target); err != nil {
		t.Fatalf("link %s: %v", target, err)
	}
}

func directoryCounts(db *indexsql.IndexDB, source string) (int, int, error) {
	a, err := db.GetDirectoryChildren(source, "/A/")
	if err != nil {
		return 0, 0, err
	}
	b, err := db.GetDirectoryChildren(source, "/B/")
	if err != nil {
		return 0, 0, err
	}
	return len(a), len(b), nil
}

func medianSample(samples []measureGetSample) float64 {
	if len(samples) == 0 {
		return 0
	}
	values := make([]float64, 0, len(samples))
	for _, s := range samples {
		values = append(values, s.ClientMs)
	}
	sort.Float64s(values)
	return values[len(values)/2]
}

func waitForRefreshProbeIdle(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if indexing.RefreshProbeSnapshot().Active == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
