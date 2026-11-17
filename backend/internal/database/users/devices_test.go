package users

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func boolValue(v bool) *bool { return &v }

func TestValidateDeviceID(t *testing.T) {
	valid := []string{
		"12345678",
		"11111111-2222-4333-8444-555555555555",
		"browser_abc-123",
	}
	for _, id := range valid {
		if err := ValidateDeviceID(id); err != nil {
			t.Fatalf("expected %q to be valid: %v", id, err)
		}
	}
	invalid := []string{
		"",
		"short",
		"has space",
		"bad/slash",
		"emoji-😀-device",
		"__proto__",
		"Prototype",
		"constructor",
		strings.Repeat("a", 65),
	}
	for _, id := range invalid {
		if err := ValidateDeviceID(id); err == nil {
			t.Fatalf("expected %q to be rejected", id)
		}
	}
}

func TestNormalizeDeviceLabel(t *testing.T) {
	got, err := NormalizeDeviceLabel("  Chrome\n· Linux  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Chrome· Linux" {
		t.Fatalf("unexpected sanitized label: %q", got)
	}
	if _, err := NormalizeDeviceLabel(strings.Repeat("界", MaxDeviceLabelRunes)); err != nil {
		t.Fatalf("expected %d runes to be accepted: %v", MaxDeviceLabelRunes, err)
	}
	if _, err := NormalizeDeviceLabel(strings.Repeat("界", MaxDeviceLabelRunes+1)); err == nil {
		t.Fatal("expected an overlong label to be rejected")
	}
	// Control characters are removed before the length check.
	if _, err := NormalizeDeviceLabel("\x00" + strings.Repeat("a", MaxDeviceLabelRunes) + "\x7f"); err != nil {
		t.Fatalf("expected control characters to be stripped before the length check: %v", err)
	}
}

func TestMergeDeviceSettingsRetainsOtherDevices(t *testing.T) {
	existing := DeviceSettings{
		"device-0001": {
			Label:     "Old",
			LastSeen:  100,
			Overrides: DeviceOverrides{SingleClick: boolValue(false)},
		},
		"device-0002": {
			Label:     "Phone",
			LastSeen:  200,
			Overrides: DeviceOverrides{SingleClick: boolValue(true)},
		},
	}
	patch := DeviceSettings{
		"device-0001": {
			Label:     "  Chrome · Linux  ",
			LastSeen:  1, // client value must be ignored
			Overrides: DeviceOverrides{SingleClick: boolValue(true)},
		},
	}

	merged := MergeDeviceSettings(existing, patch, 999)
	if len(merged) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(merged))
	}
	if merged["device-0001"].LastSeen != 999 {
		t.Fatalf("expected server lastSeen, got %d", merged["device-0001"].LastSeen)
	}
	if merged["device-0001"].Label != "Chrome · Linux" {
		t.Fatalf("unexpected label %q", merged["device-0001"].Label)
	}
	if merged["device-0001"].Overrides.SingleClick == nil || !*merged["device-0001"].Overrides.SingleClick {
		t.Fatal("expected singleClick override true")
	}
	if merged["device-0002"].Overrides.SingleClick == nil || !*merged["device-0002"].Overrides.SingleClick {
		t.Fatal("expected the other device to be retained unchanged")
	}
	if existing["device-0001"].LastSeen != 100 {
		t.Fatal("merge must not mutate the existing map")
	}
	if patch["device-0001"].LastSeen != 1 || !*patch["device-0001"].Overrides.SingleClick {
		t.Fatal("merge must not mutate the patch map")
	}
}

func TestMergeDeviceSettingsKeepsLabelWhenPatchOmitsIt(t *testing.T) {
	existing := DeviceSettings{
		"device-0001": {
			Label:     "Chrome · Linux",
			LastSeen:  100,
			Overrides: DeviceOverrides{SingleClick: boolValue(true)},
		},
	}
	patch := DeviceSettings{
		"device-0001": {
			Overrides: DeviceOverrides{SingleClick: boolValue(false)},
		},
	}
	merged := MergeDeviceSettings(existing, patch, 999)
	if merged["device-0001"].Label != "Chrome · Linux" {
		t.Fatalf("expected existing label to be kept, got %q", merged["device-0001"].Label)
	}
	if merged["device-0001"].Overrides.SingleClick == nil || *merged["device-0001"].Overrides.SingleClick {
		t.Fatal("expected explicit false to be stored")
	}
}

func TestMergeDeviceSettingsClearsOverrideButKeepsRecord(t *testing.T) {
	existing := DeviceSettings{
		"device-0001": {
			Label:     "Chrome · Linux",
			LastSeen:  100,
			Overrides: DeviceOverrides{SingleClick: boolValue(true)},
		},
	}
	patch := DeviceSettings{
		"device-0001": {Overrides: DeviceOverrides{}},
	}
	merged := MergeDeviceSettings(existing, patch, 999)
	record, ok := merged["device-0001"]
	if !ok {
		t.Fatal("expected the reset record to stay for label/retention")
	}
	if record.Overrides.SingleClick != nil {
		t.Fatal("expected the override to be cleared")
	}
	if record.Label != "Chrome · Linux" {
		t.Fatalf("expected the label to survive, got %q", record.Label)
	}
}

func TestMergeDeviceSettingsPrunesOldestDeterministically(t *testing.T) {
	existing := make(DeviceSettings, MaxDeviceRecords)
	for i := 0; i < MaxDeviceRecords; i++ {
		id := fmt.Sprintf("device-%03d", i)
		existing[id] = DeviceRecord{
			Label:     "old",
			LastSeen:  int64(i + 1),
			Overrides: DeviceOverrides{SingleClick: boolValue(true)},
		}
	}
	patch := DeviceSettings{
		"device-newest-0001": {Label: "New", Overrides: DeviceOverrides{SingleClick: boolValue(true)}},
	}
	merged := MergeDeviceSettings(existing, patch, 999)
	if len(merged) != MaxDeviceRecords {
		t.Fatalf("expected %d devices after pruning, got %d", MaxDeviceRecords, len(merged))
	}
	if _, ok := merged["device-000"]; ok {
		t.Fatal("expected the oldest device to be pruned")
	}
	if _, ok := merged["device-newest-0001"]; !ok {
		t.Fatal("expected the patched device to be retained")
	}
}

func TestMergeDeviceSettingsPruneTieKeepsPatchedDevice(t *testing.T) {
	const now = 1700000000
	existing := make(DeviceSettings, MaxDeviceRecords)
	for i := 0; i < MaxDeviceRecords; i++ {
		id := fmt.Sprintf("device-%03d", i)
		existing[id] = DeviceRecord{
			Label:    "same-second",
			LastSeen: now,
		}
	}
	// The new device ID sorts before every stored ID; a plain lastSeen+ID sort
	// would evict it immediately. The patched record must always survive.
	patch := DeviceSettings{
		"0-new-device": {Overrides: DeviceOverrides{SingleClick: boolValue(true)}},
	}
	merged := MergeDeviceSettings(existing, patch, now)
	if len(merged) != MaxDeviceRecords {
		t.Fatalf("expected %d devices, got %d", MaxDeviceRecords, len(merged))
	}
	if _, ok := merged["0-new-device"]; !ok {
		t.Fatal("expected the patched device to survive a same-second tie")
	}
	if _, ok := merged["device-000"]; ok {
		t.Fatal("expected the lowest-ID same-second device to be pruned")
	}
}

func TestDeviceSettingsJSONToleratesLegacyFields(t *testing.T) {
	// Records written by the old four-field experiment may carry other keys.
	// They must keep loading, but only singleClick may become an override.
	raw := []byte(`{"device-0001":{"label":"Chrome · Linux","lastSeen":42,"overrides":{"singleClick":false,"dateFormat":true,"unknown":1},"stale":true}}`)
	var settings DeviceSettings
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("expected tolerant decode: %v", err)
	}
	record := settings["device-0001"]
	if record.Overrides.SingleClick == nil || *record.Overrides.SingleClick {
		t.Fatal("expected explicit false singleClick to round-trip")
	}
	if record.Label != "Chrome · Linux" || record.LastSeen != 42 {
		t.Fatalf("unexpected record: %+v", record)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "dateFormat") {
		t.Fatalf("legacy override fields must be dropped on re-save: %s", encoded)
	}
}

func validDevicePatch(id string, overrides string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"%s":{"label":"Chrome · Linux","overrides":%s}}`, id, overrides))
}

func TestParseDeviceSettingsPatchAcceptsSingleEntry(t *testing.T) {
	settings, err := ParseDeviceSettingsPatch(validDevicePatch("11111111-2222-4333-8444-555555555555", `{"singleClick":true}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(settings) != 1 {
		t.Fatalf("expected one device, got %d", len(settings))
	}
	record := settings["11111111-2222-4333-8444-555555555555"]
	if record.Overrides.SingleClick == nil || !*record.Overrides.SingleClick {
		t.Fatal("expected singleClick true")
	}
	if record.Label != "Chrome · Linux" {
		t.Fatalf("unexpected label %q", record.Label)
	}

	// false must be preserved, and an empty override object must be accepted.
	falseSettings, err := ParseDeviceSettingsPatch(validDevicePatch("11111111-2222-4333-8444-555555555555", `{"singleClick":false}`))
	if err != nil {
		t.Fatalf("unexpected error for false: %v", err)
	}
	if v := falseSettings["11111111-2222-4333-8444-555555555555"].Overrides.SingleClick; v == nil || *v {
		t.Fatal("expected explicit false to survive parsing")
	}
	cleared, err := ParseDeviceSettingsPatch(validDevicePatch("11111111-2222-4333-8444-555555555555", `{}`))
	if err != nil {
		t.Fatalf("unexpected error for cleared overrides: %v", err)
	}
	if cleared["11111111-2222-4333-8444-555555555555"].Overrides.SingleClick != nil {
		t.Fatal("expected empty overrides to clear the device")
	}
}

func TestParseDeviceSettingsPatchRejectsInvalidShapes(t *testing.T) {
	const id = "11111111-2222-4333-8444-555555555555"
	invalid := map[string]string{
		"null devices":              "null",
		"empty map":                 "{}",
		"array":                     "[]",
		"string":                    "\"devices\"",
		"two devices":               fmt.Sprintf(`{"%s":{"overrides":{}},"device-0002":{"overrides":{}}}`, id),
		"missing overrides":         fmt.Sprintf(`{"%s":{"label":"x"}}`, id),
		"null overrides":            fmt.Sprintf(`{"%s":{"overrides":null}}`, id),
		"string overrides":          fmt.Sprintf(`{"%s":{"overrides":"singleClick"}}`, id),
		"null singleClick":          validPatchRaw(id, `{"singleClick":null}`),
		"string singleClick":        validPatchRaw(id, `{"singleClick":"true"}`),
		"number singleClick":        validPatchRaw(id, `{"singleClick":1}`),
		"array singleClick":         validPatchRaw(id, `{"singleClick":[true]}`),
		"extra override field":      validPatchRaw(id, `{"singleClick":true,"dateFormat":true}`),
		"unknown record field":      fmt.Sprintf(`{"%s":{"overrides":{},"futureField":1}}`, id),
		"short id":                  fmt.Sprintf(`{"bad":{"overrides":{}}}`),
		"reserved id":               fmt.Sprintf(`{"__proto__":{"overrides":{}}}`),
		"invalid id characters":     fmt.Sprintf(`{"has space":{"overrides":{}}}`),
		"null label":                fmt.Sprintf(`{"%s":{"label":null,"overrides":{}}}`, id),
		"number label":              fmt.Sprintf(`{"%s":{"label":7,"overrides":{}}}`, id),
		"overlong label":            fmt.Sprintf(`{"%s":{"label":%q,"overrides":{}}}`, id, strings.Repeat("a", MaxDeviceLabelRunes+1)),
		"negative lastSeen":         fmt.Sprintf(`{"%s":{"lastSeen":-1,"overrides":{}}}`, id),
		"fractional lastSeen":       fmt.Sprintf(`{"%s":{"lastSeen":1.5,"overrides":{}}}`, id),
		"string lastSeen":           fmt.Sprintf(`{"%s":{"lastSeen":"now","overrides":{}}}`, id),
		"null lastSeen":             fmt.Sprintf(`{"%s":{"lastSeen":null,"overrides":{}}}`, id),
		"device entry is not object": fmt.Sprintf(`{"%s":"x"}`, id),
	}
	for name, raw := range invalid {
		if _, err := ParseDeviceSettingsPatch(json.RawMessage(raw)); err == nil {
			t.Fatalf("expected %s to be rejected: %s", name, raw)
		}
	}
}

func validPatchRaw(id, overrides string) string {
	return fmt.Sprintf(`{"%s":{"overrides":%s}}`, id, overrides)
}
