package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/database/users"
	"github.com/gtsteffaniak/filebrowser/backend/internal/diagnostics"
)

func testDiagnosticsService(t *testing.T) (*diagnostics.Service, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := diagnostics.Config{
		Enabled:        true,
		Dir:            dir,
		AllowedOrigins: []string{"https://example.test:8449"},
	}
	writerCfg := diagnostics.DefaultWriterConfig()
	writerCfg.FlushInterval = 10 * time.Millisecond
	service, err := diagnostics.NewService(diagnostics.Options{Config: cfg, WriterConfig: writerCfg, Build: "web-test", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	previous := diagnosticsRuntime
	diagnosticsRuntime = service
	t.Cleanup(func() {
		service.Close(2 * time.Second)
		diagnosticsRuntime = previous
	})
	return service, dir
}

func testUser() *users.User {
	return &users.User{ID: 7, FrontendUser: users.FrontendUser{Username: "alice"}}
}

func validDiagnosticsBatch() []byte {
	event := map[string]any{
		"seq": 1, "t_ms": 1.5, "level": "info", "event": "session.end",
		"load_ref": nil, "operation_ref": nil, "request_ref": nil, "incident_ref": nil,
		"data": map[string]any{"reason": "manual", "duration_ms": 10},
	}
	body, _ := json.Marshal(map[string]any{
		"schema_version": 1,
		"client_session": "0123456789abcdef0123456789abcdef",
		"batch_ref":      "fedcba9876543210fedcba9876543210",
		"client_build":   map[string]any{"version": nil, "commit": nil},
		"events":         []any{event},
	})
	return body
}

func diagnosticsPost(body []byte, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/diagnostics/client", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Quantum-Diagnostics", "1")
	if mutate != nil {
		mutate(req)
	}
	recorder := httptest.NewRecorder()
	status, err := diagnosticsClientHandler(recorder, req, &Context{User: testUser()})
	if err != nil && status >= 400 {
		// handler already rendered a fixed error body
	}
	return recorder
}

func TestDiagnosticsStatusHandler(t *testing.T) {
	testDiagnosticsService(t)
	req := httptest.NewRequest(http.MethodGet, "/diagnostics/status", nil)
	recorder := httptest.NewRecorder()
	status, err := diagnosticsStatusHandler(recorder, req, &Context{User: testUser()})
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["enabled"] != true && payload["writer_state"] != "ready" {
		t.Errorf("unexpected status %+v", payload)
	}
	if _, ok := payload["dir"]; ok {
		t.Error("status exposes the storage directory")
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Error("status is cacheable")
	}
}

func TestDiagnosticsClientAuthAndSourceChecks(t *testing.T) {
	testDiagnosticsService(t)
	body := validDiagnosticsBatch()

	cases := []struct {
		name   string
		mutate func(*http.Request)
		want   int
	}{
		{"missing_header", func(r *http.Request) { r.Header.Del("X-Quantum-Diagnostics") }, 400},
		{"wrong_content_type", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415},
		{"cross_site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403},
		{"bad_origin", func(r *http.Request) { r.Header.Set("Origin", "https://evil.test") }, 403},
		{"allowed_origin", func(r *http.Request) { r.Header.Set("Origin", "https://example.test:8449") }, 202},
		{"same_origin", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") }, 202},
	}
	for _, tc := range cases {
		recorder := diagnosticsPost(body, tc.mutate)
		if recorder.Code != tc.want {
			t.Errorf("%s: status=%d want=%d body=%s", tc.name, recorder.Code, tc.want, recorder.Body.String())
		}
	}
}

func TestDiagnosticsClientWritesAndRejectsOversize(t *testing.T) {
	_, dir := testDiagnosticsService(t)
	recorder := diagnosticsPost(validDiagnosticsBatch(), nil)
	if recorder.Code != 202 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["queued"] != true || result["batch_ref"] == "" {
		t.Errorf("unexpected 202 body %+v", result)
	}
	diagnosticsRuntime.Close(2 * time.Second)
	data, err := os.ReadFile(filepath.Join(dir, "quantum-diag-current.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"origin":"client"`) || !strings.Contains(string(data), `"server_build":"web-test"`) {
		t.Errorf("unexpected record: %s", data)
	}

	big := bytes.Repeat([]byte("x"), diagnostics.MaxBodyBytes+10)
	recorder = diagnosticsPost(big, nil)
	if recorder.Code != 413 {
		t.Errorf("oversize status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestDiagnosticsSpanOnlyWithValidTrace(t *testing.T) {
	_, dir := testDiagnosticsService(t)
	called := 0
	handler := withDiagnosticsSpan("resources", func(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
		called++
		markDirectorySpanCount(d, 42)
		return 200, nil
	})
	ref := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	req := httptest.NewRequest(http.MethodGet, "/resources", nil)
	req.Header.Set(diagnosticsTraceHeader, ref)
	recorder := httptest.NewRecorder()
	status, err := handler(recorder, req, &Context{User: testUser()})
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	diagnosticsRuntime.Close(2 * time.Second)
	data, err := os.ReadFile(filepath.Join(dir, "quantum-diag-current.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"origin":"server"`) || !strings.Contains(string(data), `"known_items":42`) {
		t.Errorf("span missing: %s", data)
	}
	if called != 1 {
		t.Errorf("handler called %d times", called)
	}

	invalid := httptest.NewRequest(http.MethodGet, "/resources", nil)
	invalid.Header.Set(diagnosticsTraceHeader, "not-a-ref")
	recorder = httptest.NewRecorder()
	called = 0
	if _, err := handler(recorder, invalid, &Context{User: testUser()}); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Error("handler not called without a valid trace")
	}
}
