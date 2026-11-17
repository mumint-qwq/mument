package users

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxDeviceRecords caps how many distinct devices keep overrides for one user.
	MaxDeviceRecords = 32
	// MaxDeviceLabelRunes caps the human-readable label stored with a device record.
	MaxDeviceLabelRunes = 80
	// Device IDs are opaque client-generated identifiers persisted in browser storage.
	minDeviceIDLen = 8
	maxDeviceIDLen = 64
)

// reservedDeviceIDs are rejected even though Go maps accept them: browser storage
// and JavaScript objects treat these keys specially (prototype pollution).
var reservedDeviceIDs = map[string]struct{}{
	"__proto__":   {},
	"prototype":   {},
	"constructor": {},
}

// DeviceOverrides holds the profile settings that are scoped to one browser.
// A nil pointer means "this device has no opinion" and is distinct from an
// explicit false. Only singleClick is device-scoped in this release.
type DeviceOverrides struct {
	SingleClick *bool `json:"singleClick,omitempty"`
}

// DeviceRecord is the persisted state for one device.
type DeviceRecord struct {
	Label     string          `json:"label,omitempty"`
	LastSeen  int64           `json:"lastSeen,omitempty"`
	Overrides DeviceOverrides `json:"overrides,omitempty"`
}

// DeviceSettings maps a client-generated device ID to its stored record.
type DeviceSettings map[string]DeviceRecord

func (o DeviceOverrides) clone() DeviceOverrides {
	out := DeviceOverrides{}
	if o.SingleClick != nil {
		v := *o.SingleClick
		out.SingleClick = &v
	}
	return out
}

func (r DeviceRecord) clone() DeviceRecord {
	r.Overrides = r.Overrides.clone()
	return r
}

// CloneDeviceSettings returns a deep copy that is safe to mutate before persisting.
func CloneDeviceSettings(in DeviceSettings) DeviceSettings {
	if in == nil {
		return nil
	}
	out := make(DeviceSettings, len(in))
	for id, rec := range in {
		out[id] = rec.clone()
	}
	return out
}

// ValidateDeviceID rejects identifiers that no supported client generates.
func ValidateDeviceID(id string) error {
	if len(id) < minDeviceIDLen || len(id) > maxDeviceIDLen {
		return fmt.Errorf("device id must be %d-%d characters", minDeviceIDLen, maxDeviceIDLen)
	}
	if _, reserved := reservedDeviceIDs[strings.ToLower(id)]; reserved {
		return fmt.Errorf("device id is reserved")
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("device id contains unsupported characters")
	}
	return nil
}

// NormalizeDeviceLabel removes control characters, trims surrounding space and
// rejects labels longer than MaxDeviceLabelRunes after cleaning.
func NormalizeDeviceLabel(raw string) (string, error) {
	label := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw)
	label = strings.TrimSpace(label)
	if utf8.RuneCountInString(label) > MaxDeviceLabelRunes {
		return "", fmt.Errorf("device label must be at most %d characters", MaxDeviceLabelRunes)
	}
	return label, nil
}

// ValidateDeviceSettings checks a device map that comes from an internal caller
// rather than the strict wire parser.
func ValidateDeviceSettings(devices DeviceSettings) error {
	if len(devices) > MaxDeviceRecords {
		return fmt.Errorf("too many devices: at most %d are supported", MaxDeviceRecords)
	}
	for id, rec := range devices {
		if err := ValidateDeviceID(id); err != nil {
			return err
		}
		if _, err := NormalizeDeviceLabel(rec.Label); err != nil {
			return err
		}
		if rec.LastSeen < 0 {
			return fmt.Errorf("device lastSeen must not be negative")
		}
	}
	return nil
}

// MergeDeviceSettings merges a validated patch into the stored device map.
// Patch entries replace the stored record for the same device ID; other devices
// are retained. The server owns lastSeen so clients cannot reorder retention.
func MergeDeviceSettings(existing, patch DeviceSettings, now int64) DeviceSettings {
	out := CloneDeviceSettings(existing)
	if len(patch) == 0 {
		return out
	}
	if out == nil {
		out = make(DeviceSettings, len(patch))
	}
	patched := make(map[string]struct{}, len(patch))
	for id, rec := range patch {
		label, err := NormalizeDeviceLabel(rec.Label)
		if err != nil || label == "" {
			// A client may patch only the overrides; keep the last known label.
			label = out[id].Label
		}
		rec.Label = label
		rec.LastSeen = now
		rec.Overrides = rec.Overrides.clone()
		out[id] = rec
		patched[id] = struct{}{}
	}
	pruneDeviceSettings(out, patched, MaxDeviceRecords)
	return out
}

// pruneDeviceSettings drops the records with the oldest server save time.
// Records saved by the current patch are never evicted, and ties are broken by
// ascending device ID so the outcome is deterministic.
func pruneDeviceSettings(devices DeviceSettings, patched map[string]struct{}, max int) {
	if len(devices) <= max {
		return
	}
	type candidate struct {
		id       string
		lastSeen int64
	}
	candidates := make([]candidate, 0, len(devices))
	for id, rec := range devices {
		if _, isPatched := patched[id]; isPatched {
			continue
		}
		candidates = append(candidates, candidate{id: id, lastSeen: rec.LastSeen})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].lastSeen == candidates[j].lastSeen {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].lastSeen < candidates[j].lastSeen
	})
	remove := len(devices) - max
	for _, victim := range candidates {
		if remove == 0 {
			break
		}
		delete(devices, victim.id)
		remove--
	}
}

// ParseDeviceSettingsPatch strictly validates the wire shape of data.devices for
// the device PATCH entry point: a JSON object with exactly one device record.
func ParseDeviceSettingsPatch(raw json.RawMessage) (DeviceSettings, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, fmt.Errorf("devices must be a JSON object with exactly one device entry")
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &entries); err != nil {
		return nil, fmt.Errorf("devices must be a JSON object with exactly one device entry")
	}
	if len(entries) != 1 {
		return nil, fmt.Errorf("devices must contain exactly one device entry")
	}
	out := make(DeviceSettings, 1)
	for id, rawRecord := range entries {
		if err := ValidateDeviceID(id); err != nil {
			return nil, err
		}
		record, err := parseDeviceRecord(rawRecord)
		if err != nil {
			return nil, err
		}
		out[id] = record
	}
	return out, nil
}

func parseDeviceRecord(raw json.RawMessage) (DeviceRecord, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(raw), &fields); err != nil || fields == nil {
		return DeviceRecord{}, fmt.Errorf("device entry must be a JSON object")
	}
	var record DeviceRecord
	overridesSeen := false
	for key, value := range fields {
		switch key {
		case "label":
			var label string
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return DeviceRecord{}, fmt.Errorf("device label must be a string")
			}
			if err := json.Unmarshal(value, &label); err != nil {
				return DeviceRecord{}, fmt.Errorf("device label must be a string")
			}
			normalized, err := NormalizeDeviceLabel(label)
			if err != nil {
				return DeviceRecord{}, err
			}
			record.Label = normalized
		case "lastSeen":
			seen, err := parseNonNegativeInt64(value)
			if err != nil {
				return DeviceRecord{}, fmt.Errorf("device lastSeen must be a non-negative integer")
			}
			// Callers may send it for compatibility; the server replaces it on merge.
			record.LastSeen = seen
		case "overrides":
			overridesSeen = true
			overrides, err := parseDeviceOverrides(value)
			if err != nil {
				return DeviceRecord{}, err
			}
			record.Overrides = overrides
		default:
			return DeviceRecord{}, fmt.Errorf("unsupported device field %q", key)
		}
	}
	if !overridesSeen {
		return DeviceRecord{}, fmt.Errorf("device overrides are required")
	}
	return record, nil
}

func parseDeviceOverrides(raw json.RawMessage) (DeviceOverrides, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return DeviceOverrides{}, fmt.Errorf("device overrides must be a JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
		return DeviceOverrides{}, fmt.Errorf("device overrides must be a JSON object")
	}
	var overrides DeviceOverrides
	for key, value := range fields {
		switch key {
		case "singleClick":
			enabled, err := parseJSONBool(value)
			if err != nil {
				return DeviceOverrides{}, fmt.Errorf("device override singleClick must be a boolean")
			}
			overrides.SingleClick = &enabled
		default:
			return DeviceOverrides{}, fmt.Errorf("unsupported device override %q", key)
		}
	}
	return overrides, nil
}

func parseJSONBool(raw json.RawMessage) (bool, error) {
	switch strings.TrimSpace(string(raw)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("not a boolean")
	}
}

func parseNonNegativeInt64(raw json.RawMessage) (int64, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return 0, err
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, fmt.Errorf("not a number")
	}
	parsed, err := number.Int64()
	if err != nil {
		return 0, err
	}
	if parsed < 0 {
		return 0, fmt.Errorf("negative")
	}
	return parsed, nil
}
