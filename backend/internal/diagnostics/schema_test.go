package diagnostics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixtureFile struct {
	Defaults struct {
		ClientSession string      `json:"client_session"`
		BatchRef      string      `json:"batch_ref"`
		ClientBuild   ClientBuild `json:"client_build"`
	} `json:"defaults"`
	Cases []struct {
		Name         string                     `json:"name"`
		Event        json.RawMessage            `json:"event"`
		Events       []json.RawMessage          `json:"events"`
		Envelope     map[string]json.RawMessage `json:"envelope"`
		ExpectServer bool                       `json:"expect_server"`
	} `json:"cases"`
}

func loadFixtures(t *testing.T) fixtureFile {
	t.Helper()
	path := filepath.Join("..", "..", "..", "diagnostics", "fixtures", "cases.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var fixtures fixtureFile
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}
	return fixtures
}

func batchBody(t *testing.T, fixtures fixtureFile, entry struct {
	Name         string                     `json:"name"`
	Event        json.RawMessage            `json:"event"`
	Events       []json.RawMessage          `json:"events"`
	Envelope     map[string]json.RawMessage `json:"envelope"`
	ExpectServer bool                       `json:"expect_server"`
}) []byte {
	t.Helper()
	events := entry.Events
	if len(events) == 0 && len(entry.Event) > 0 {
		events = []json.RawMessage{entry.Event}
	}
	envelope := map[string]any{
		"schema_version": SchemaVersion,
		"client_session": fixtures.Defaults.ClientSession,
		"batch_ref":      fixtures.Defaults.BatchRef,
		"client_build":   fixtures.Defaults.ClientBuild,
		"events":         events,
	}
	for key, raw := range entry.Envelope {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatalf("%s: envelope %s: %v", entry.Name, key, err)
		}
		envelope[key] = value
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("%s: marshal: %v", entry.Name, err)
	}
	return body
}

func TestSharedFixtures(t *testing.T) {
	fixtures := loadFixtures(t)
	if len(fixtures.Cases) < 50 {
		t.Fatalf("expected at least 50 shared cases, got %d", len(fixtures.Cases))
	}
	for _, entry := range fixtures.Cases {
		body := batchBody(t, fixtures, entry)
		batch, err := ValidateBatch(body)
		accepted := err == nil && batch != nil
		if accepted != entry.ExpectServer {
			t.Errorf("%s: server accepted=%v want=%v err=%v", entry.Name, accepted, entry.ExpectServer, err)
		}
	}
}

func TestRejectsUnknownAndUnsafeFields(t *testing.T) {
	fixtures := loadFixtures(t)
	var valid json.RawMessage
	for _, entry := range fixtures.Cases {
		if entry.Name == "valid_listing_response" {
			valid = entry.Event
			break
		}
	}
	var event map[string]any
	if err := json.Unmarshal(valid, &event); err != nil {
		t.Fatal(err)
	}
	addField := func(key string, value any) []byte {
		copyEvent := map[string]any{}
		for k, v := range event {
			copyEvent[k] = v
		}
		copyEvent[key] = value
		body, _ := json.Marshal(map[string]any{
			"schema_version": SchemaVersion,
			"client_session": fixtures.Defaults.ClientSession,
			"batch_ref":      fixtures.Defaults.BatchRef,
			"client_build":   fixtures.Defaults.ClientBuild,
			"events":         []any{copyEvent},
		})
		return body
	}
	for _, key := range []string{"path", "filename", "cookie", "token", "actor_ref", "received_at"} {
		if _, err := ValidateBatch(addField(key, "/srv/pictures/private.jpg")); err == nil {
			t.Errorf("unknown field %q was accepted", key)
		}
	}
	if _, err := ValidateBatch([]byte(`{"schema_version":1}`)); err == nil {
		t.Error("incomplete envelope accepted")
	}
}

func fixtureEvent(t *testing.T, fixtures fixtureFile, name string) map[string]any {
	t.Helper()
	for _, entry := range fixtures.Cases {
		if entry.Name != name {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(entry.Event, &event); err != nil {
			t.Fatalf("%s: unmarshal event: %v", name, err)
		}
		return event
	}
	t.Fatalf("fixture %q not found", name)
	return nil
}

func batchForEvent(t *testing.T, fixtures fixtureFile, event map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"schema_version": SchemaVersion,
		"client_session": fixtures.Defaults.ClientSession,
		"batch_ref":      fixtures.Defaults.BatchRef,
		"client_build":   fixtures.Defaults.ClientBuild,
		"events":         []any{event},
	})
	if err != nil {
		t.Fatalf("marshal batch: %v", err)
	}
	return body
}

func TestOptionalExtensionsStayBackwardCompatible(t *testing.T) {
	fixtures := loadFixtures(t)

	operation := fixtureEvent(t, fixtures, "valid_operation_result_move_outcome_ok")
	if _, err := ValidateBatch(batchForEvent(t, fixtures, operation)); err != nil {
		t.Fatalf("new outcome field rejected: %v", err)
	}
	data := operation["data"].(map[string]any)
	delete(data, "outcome")
	if _, err := ValidateBatch(batchForEvent(t, fixtures, operation)); err != nil {
		t.Fatalf("legacy operation.result without outcome rejected: %v", err)
	}
	data["outcome"] = "maybe"
	if _, err := ValidateBatch(batchForEvent(t, fixtures, operation)); err == nil {
		t.Fatal("invalid outcome enum was accepted")
	}

	restore := fixtureEvent(t, fixtures, "valid_navigation_restore_move")
	if _, err := ValidateBatch(batchForEvent(t, fixtures, restore)); err != nil {
		t.Fatalf("move navigation.restore rejected: %v", err)
	}
	restoreData := restore["data"].(map[string]any)
	delete(restoreData, "strategy")
	if _, err := ValidateBatch(batchForEvent(t, fixtures, restore)); err != nil {
		t.Fatalf("legacy navigation.restore without strategy rejected: %v", err)
	}
	restoreData["strategy"] = "guess"
	if _, err := ValidateBatch(batchForEvent(t, fixtures, restore)); err == nil {
		t.Fatal("invalid strategy enum was accepted")
	}

	cancelled := fixtureEvent(t, fixtures, "valid_navigation_restore_cancelled_superseded")
	if _, err := ValidateBatch(batchForEvent(t, fixtures, cancelled)); err != nil {
		t.Fatalf("closed reason_code rejected: %v", err)
	}
	cancelledData := cancelled["data"].(map[string]any)
	code := cancelledData["reason_code"]
	delete(cancelledData, "reason_code")
	if _, err := ValidateBatch(batchForEvent(t, fixtures, cancelled)); err != nil {
		t.Fatalf("legacy navigation.restore without reason_code rejected: %v", err)
	}
	cancelledData["reason_code"] = code
	cancelledData["reason_code"] = "made_up"
	if _, err := ValidateBatch(batchForEvent(t, fixtures, cancelled)); err == nil {
		t.Fatal("invalid reason_code was accepted")
	}
}

func TestEventNamesClosed(t *testing.T) {
	names := EventNames()
	if len(names) != 22 {
		t.Fatalf("expected 22 events, got %d: %v", len(names), names)
	}
	for _, name := range names {
		if strings.Contains(name, "..") || strings.TrimSpace(name) == "" {
			t.Errorf("invalid event name %q", name)
		}
	}
}
