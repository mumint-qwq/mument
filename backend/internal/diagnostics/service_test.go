package diagnostics

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func testService(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{Enabled: true, Dir: dir, AllowedOrigins: []string{"https://example.test:8449"}}
	writerCfg := DefaultWriterConfig()
	writerCfg.FlushInterval = 10 * time.Millisecond
	svc, err := NewService(Options{Config: cfg, WriterConfig: writerCfg, Build: "test-build", Logf: t.Logf})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(func() { svc.Close(2 * time.Second) })
	return svc, dir
}

func validBody(t *testing.T) []byte {
	t.Helper()
	fixtures := loadFixtures(t)
	for _, entry := range fixtures.Cases {
		if entry.Name == "valid_listing_response" {
			return batchBody(t, fixtures, entry)
		}
	}
	t.Fatal("valid fixture missing")
	return nil
}

func readRecords(t *testing.T, dir string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, currentFileName))
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid NDJSON line %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func TestServiceAcceptsAndWrites(t *testing.T) {
	svc, dir := testService(t)
	body := validBody(t)
	result, acceptErr := svc.Accept(42, body)
	if acceptErr != nil {
		t.Fatalf("accept: %v", acceptErr)
	}
	if !result.Queued || result.Deduplicated {
		t.Fatalf("unexpected result %+v", result)
	}
	svc.Close(2 * time.Second)
	records := readRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected one record, got %d", len(records))
	}
	record := records[0]
	if record["origin"] != "client" || record["event"] != "listing.response" {
		t.Errorf("unexpected record %+v", record)
	}
	actor, _ := record["actor_ref"].(string)
	if !regexp.MustCompile("^[0-9a-f]{32}$").MatchString(actor) {
		t.Errorf("actor_ref = %q", actor)
	}
	if _, ok := record["path"]; ok {
		t.Error("record contains a path field")
	}
	if record["server_build"] != "test-build" {
		t.Errorf("server_build = %v", record["server_build"])
	}
	if result.WriterState != WriterStateReady {
		t.Errorf("writer state = %s", result.WriterState)
	}
}

func TestServiceDeduplicatesAfterEnqueue(t *testing.T) {
	svc, dir := testService(t)
	body := validBody(t)
	if _, err := svc.Accept(1, body); err != nil {
		t.Fatal(err)
	}
	second, err := svc.Accept(1, body)
	if err != nil {
		t.Fatalf("deduplicated accept returned error: %v", err)
	}
	if !second.Deduplicated {
		t.Error("second batch was not marked deduplicated")
	}
	svc.Close(2 * time.Second)
	if got := len(readRecords(t, dir)); got != 1 {
		t.Errorf("deduplicated batch wrote %d records, want 1", got)
	}
}

func TestServiceRejectsRateLimitAndBadBodies(t *testing.T) {
	svc, _ := testService(t)
	body := validBody(t)
	for i := 0; i < 10; i++ {
		if _, err := svc.Accept(2, body); err != nil {
			t.Fatalf("batch %d: %v", i, err)
		}
	}
	if _, err := svc.Accept(2, body); err == nil || err.Status != 429 || err.RetryAfter < 1 || err.RetryAfter > 60 {
		t.Fatalf("11th batch: %+v", err)
	}
	if _, err := svc.Accept(3, []byte("{")); err == nil || err.Status != 400 {
		t.Errorf("malformed body status: %+v", err)
	}
	tooBig := make([]byte, MaxBodyBytes+1)
	if _, err := svc.Accept(3, tooBig); err == nil || err.Status != 413 {
		t.Errorf("oversized body status: %+v", err)
	}
}

func TestServiceDisabledAndBadDir(t *testing.T) {
	disabled, err := NewService(Options{Config: Config{Enabled: false, Dir: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Enabled() {
		t.Error("disabled service reports enabled")
	}
	if _, acceptErr := disabled.Accept(1, []byte("{}")); acceptErr == nil || acceptErr.Status != 404 {
		t.Errorf("disabled accept: %+v", acceptErr)
	}
	file := filepath.Join(t.TempDir(), "file")
	if writeErr := os.WriteFile(file, []byte("x"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	bad, err := NewService(Options{Config: Config{Enabled: true, Dir: file}})
	if err == nil {
		t.Error("expected startup error for a file path")
	}
	if bad.Enabled() {
		t.Error("service with bad dir reports enabled")
	}
}

func TestServerListingSpan(t *testing.T) {
	svc, dir := testService(t)
	known := 150
	svc.RecordListingSpan(9, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "resources", 200, 12*time.Millisecond, &known, false, "ok")
	svc.RecordListingSpan(9, "not-a-ref", "resources", 200, time.Millisecond, nil, false, "ok")
	svc.Close(2 * time.Second)
	records := readRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected one server record, got %d", len(records))
	}
	record := records[0]
	if record["origin"] != "server" || record["event"] != "server.listing_span" {
		t.Errorf("unexpected record %+v", record)
	}
	data := record["data"].(map[string]any)
	if data["known_items"].(float64) != 150 || data["paged"] != false {
		t.Errorf("unexpected span data %+v", data)
	}
	if _, ok := record["t_ms"]; ok {
		t.Error("server record must not carry client t_ms")
	}
}

func TestStatusCounters(t *testing.T) {
	svc, _ := testService(t)
	body := validBody(t)
	result, err := svc.Accept(5, body)
	if err != nil {
		t.Fatal(err)
	}
	status := svc.Status()
	if !status.Enabled || status.SchemaVersion != 1 {
		t.Errorf("unexpected status %+v", status)
	}
	if status.Counters.ReceivedBatches != 1 || status.Counters.ReceivedEvents != 1 {
		t.Errorf("unexpected counters %+v", status.Counters)
	}
	if status.Counters.LastReceivedAt == "" {
		t.Error("missing last_received_at")
	}
	if result.BatchRef == "" {
		t.Error("missing batch_ref")
	}
}
