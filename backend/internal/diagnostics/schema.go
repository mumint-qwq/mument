package diagnostics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
)

const (
	SchemaVersion  = 1
	MaxEvents      = 50
	MaxEventBytes  = 2048
	MaxBodyBytes   = 64 * 1024
	maxSeq         = int64(9007199254740991)
	maxCount       = int64(1000000)
	maxFrameIndex  = int64(10000000)
	maxEventTimeMs = float64(1e15)
)

// ValidationError is safe to return to the client: it contains a fixed code
// and message, never the submitted payload.
type ValidationError struct {
	Code    string
	Status  int
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

func invalid(code, message string) error {
	return &ValidationError{Code: code, Status: 422, Message: message}
}

type valueKind int

const (
	kindInt valueKind = iota
	kindNum
	kindBool
	kindEnum
	kindRef
	kindString
	kindArray
	kindObject
)

type fieldSpec struct {
	kind     valueKind
	minInt   int64
	maxInt   int64
	minNum   float64
	maxNum   float64
	enum     []string
	pattern  *regexp.Regexp
	maxLen   int
	nullable bool
	optional bool
	item     *fieldSpec
	fields   map[string]fieldSpec
	maxItems int
}

func intSpec(min, max int64) fieldSpec { return fieldSpec{kind: kindInt, minInt: min, maxInt: max} }
func numSpec(min, max float64) fieldSpec {
	return fieldSpec{kind: kindNum, minNum: min, maxNum: max}
}
func boolSpec() fieldSpec { return fieldSpec{kind: kindBool} }
func enumSpec(values ...string) fieldSpec {
	return fieldSpec{kind: kindEnum, enum: values}
}
func refSpec(pattern string) fieldSpec {
	return fieldSpec{kind: kindRef, pattern: regexp.MustCompile(pattern)}
}
func stringSpec(pattern string, maxLen int) fieldSpec {
	spec := fieldSpec{kind: kindString, maxLen: maxLen}
	if pattern != "" {
		spec.pattern = regexp.MustCompile(pattern)
	}
	return spec
}
func arraySpec(max int, item fieldSpec) fieldSpec {
	return fieldSpec{kind: kindArray, maxItems: max, item: &item}
}
func objectSpec(fields map[string]fieldSpec) fieldSpec {
	return fieldSpec{kind: kindObject, fields: fields}
}
func nullable(spec fieldSpec) fieldSpec {
	spec.nullable = true
	return spec
}

// optionalField keeps a backward-compatible extension absent from older events.
func optionalField(spec fieldSpec) fieldSpec {
	spec.optional = true
	return spec
}

var (
	dirRefPattern  = "^d[0-9]{1,12}$"
	itemRefPattern = "^i[0-9]{1,12}$"
	hex32Pattern   = "^[0-9a-f]{32}$"
)

var eventSpecs = map[string]map[string]fieldSpec{
	"session.start": {
		"browser_family":          enumSpec("chrome", "firefox", "safari", "edge", "other", "unknown"),
		"browser_major":           nullable(intSpec(0, 999)),
		"viewport_width":          intSpec(0, 20000),
		"viewport_height":         intSpec(0, 20000),
		"dpr":                     nullable(numSpec(0.5, 10)),
		"screen_width":            nullable(intSpec(0, 20000)),
		"screen_height":           nullable(intSpec(0, 20000)),
		"locale":                  nullable(stringSpec("^[A-Za-z-]{2,20}$", 20)),
		"touch":                   boolSpec(),
		"capability_long_tasks":   enumSpec("supported", "unsupported", "unknown"),
		"capability_memory":       enumSpec("supported", "unsupported", "unknown"),
		"capability_paint_timing": enumSpec("supported", "unsupported", "unknown"),
		"mode":                    enumSpec("summary", "detailed"),
		"detailed":                boolSpec(),
	},
	"session.end": {
		"reason":      enumSpec("pagehide", "unload", "logout", "disabled", "manual"),
		"duration_ms": numSpec(0, 100000000),
	},
	"listing.start": {
		"dir_ref":  refSpec(dirRefPattern),
		"view":     enumSpec("mosaic", "gallery", "icons", "list", "compact", "unknown"),
		"sort_by":  enumSpec("name", "size", "modified", "type", "unknown"),
		"sort_asc": boolSpec(),
		"density":  nullable(numSpec(0, 1)),
		"paged":    boolSpec(),
		"trigger":  enumSpec("initial", "route", "reload", "preview_return", "delete", "sort", "view_change", "hash", "manual"),
	},
	"listing.response": {
		"api":             enumSpec("resources", "resources_list"),
		"status":          intSpec(0, 599),
		"headers_wait_ms": numSpec(0, 100000000),
		"body_decode_ms":  nullable(numSpec(0, 100000000)),
		"known_items":     nullable(intSpec(0, maxCount)),
		"paged":           boolSpec(),
		"outcome":         enumSpec("ok", "error", "cancelled", "aborted"),
	},
	"listing.store_prepared": {
		"elapsed_ms":      numSpec(0, 100000000),
		"item_count":      intSpec(0, maxCount),
		"selected_before": intSpec(0, maxCount),
		"selected_after":  intSpec(0, maxCount),
	},
	"listing.layout": {
		"geom_items": intSpec(0, maxCount),
		"groups":     intSpec(0, 3),
		"rows":       intSpec(0, maxCount),
		"density":    nullable(numSpec(0, 1)),
		"width_px":   nullable(intSpec(0, 20000)),
		"elapsed_ms": numSpec(0, 100000000),
		"generation": intSpec(0, maxCount),
		"method":     enumSpec("mosaic", "estimated", "unknown"),
	},
	"listing.first_batch_paint_estimate": {
		"elapsed_ms":      numSpec(0, 100000000),
		"queued_items":    intSpec(0, maxCount),
		"committed_items": intSpec(0, maxCount),
		"known_items":     intSpec(0, maxCount),
		"method":          enumSpec("double_raf", "next_tick", "unknown"),
	},
	"listing.mount_summary": {
		"mounted":        intSpec(0, maxCount),
		"total":          intSpec(0, maxCount),
		"batches":        intSpec(0, maxCount),
		"urgent_batches": intSpec(0, maxCount),
		"elapsed_sum_ms": numSpec(0, 100000000),
		"elapsed_max_ms": numSpec(0, 100000000),
		"paused_reason":  enumSpec("none", "scroll", "gesture", "prompt", "hidden", "interaction", "unknown"),
		"dropped":        nullable(intSpec(0, maxCount)),
	},
	"listing.mount_batch": {
		"generation": intSpec(0, maxCount),
		"urgent":     boolSpec(),
		"fast":       boolSpec(),
		"size":       intSpec(0, 10000),
		"enqueue_ms": numSpec(0, 100000000),
		"flush_ms":   nullable(numSpec(0, 100000000)),
	},
	"listing.complete": {
		"known_items":   intSpec(0, maxCount),
		"mounted_items": intSpec(0, maxCount),
		"elapsed_ms":    numSpec(0, 100000000),
		"paged":         boolSpec(),
	},
	"listing.cancel": {
		"reason":        enumSpec("route", "reload", "sort", "view", "unmount", "superseded", "error"),
		"elapsed_ms":    numSpec(0, 100000000),
		"mounted_items": intSpec(0, maxCount),
	},
	"scroll.summary": {
		"range_top_px":    numSpec(0, 100000000),
		"range_height_px": numSpec(0, 100000000),
		"fast_ms":         numSpec(0, 100000000),
		"slow_ms":         numSpec(0, 100000000),
		"urgent_mounted":  intSpec(0, maxCount),
		"scrollbar_drag":  nullable(boolSpec()),
		"samples":         intSpec(0, maxCount),
	},
	"layout.change": {
		"reason":        enumSpec("resize", "zoom", "density_ratio", "view_change", "gesture_commit"),
		"old_density":   nullable(numSpec(0, 1)),
		"new_density":   nullable(numSpec(0, 1)),
		"old_width":     nullable(intSpec(0, 20000)),
		"new_width":     nullable(intSpec(0, 20000)),
		"generation":    intSpec(0, maxCount),
		"anchor_ref":    nullable(refSpec("^[di][0-9]{1,12}$")),
		"offset_before": nullable(numSpec(0, 100000000)),
		"offset_after":  nullable(numSpec(0, 100000000)),
		"result":        enumSpec("success", "failed", "cancelled", "unknown"),
	},
	"images.summary": {
		"thumb_requests":     intSpec(0, maxCount),
		"thumb_ok":           intSpec(0, maxCount),
		"thumb_failed":       intSpec(0, maxCount),
		"thumb_cancelled":    intSpec(0, maxCount),
		"original_requests":  intSpec(0, maxCount),
		"original_ok":        intSpec(0, maxCount),
		"original_failed":    intSpec(0, maxCount),
		"original_cancelled": intSpec(0, maxCount),
		"inflight":           intSpec(0, maxCount),
		"elapsed_sum_ms":     numSpec(0, 100000000),
		"elapsed_max_ms":     numSpec(0, 100000000),
		"cache_known":        enumSpec("none", "partial"),
	},
	"images.failure": {
		"item_ref":     nullable(refSpec(itemRefPattern)),
		"request_kind": enumSpec("thumb", "original"),
		"status":       nullable(intSpec(100, 599)),
		"phase":        enumSpec("network", "decode", "dom", "unknown"),
		"category":     enumSpec("http", "network", "decode", "abort", "other"),
	},
	"selection.change": {
		"operation":    enumSpec("add", "remove", "toggle", "range", "clear", "reconcile", "select_all"),
		"before":       intSpec(0, maxCount),
		"after":        intSpec(0, maxCount),
		"known_items":  intSpec(0, maxCount),
		"multiple":     boolSpec(),
		"merged_count": intSpec(1, maxCount),
	},
	"navigation.restore": {
		"reason":           enumSpec("preview_return", "hash", "delete", "keyboard", "paged", "move", "refresh"),
		"target_ref":       nullable(refSpec("^[di][0-9]{1,12}$")),
		"pre_mounted":      boolSpec(),
		"requested_offset": nullable(numSpec(0, 100000000)),
		"actual_offset":    nullable(numSpec(0, 100000000)),
		"result":           enumSpec("success", "failed", "cancelled"),
		"elapsed_ms":       numSpec(0, 100000000),
		"strategy":         optionalField(enumSpec("identity", "neighbor", "fallback_pixels", "empty")),
		"reason_code": optionalField(enumSpec(
			"empty_directory", "no_identity", "target_not_mounted", "target_lost_after_scroll",
			"offset_mismatch", "target_not_in_plan", "reveal_failed", "settle_timeout",
			"user_scroll", "superseded", "unmounted", "fetch_failed", "directory_changed",
			"restore_error",
		)),
	},
	"operation.result": {
		"operation":        enumSpec("delete", "move", "rename", "copy", "archive", "unarchive"),
		"requested":        intSpec(0, maxCount),
		"succeeded":        intSpec(0, maxCount),
		"failed":           intSpec(0, maxCount),
		"elapsed_ms":       numSpec(0, 100000000),
		"refreshed":        nullable(boolSpec()),
		"selection_before": intSpec(0, maxCount),
		"selection_after":  intSpec(0, maxCount),
		"outcome":          optionalField(enumSpec("ok", "partial", "failed", "unknown")),
	},
	"runtime.error": {
		"category":    enumSpec("vue", "window", "unhandled", "resource", "other"),
		"code":        nullable(stringSpec("^[A-Za-z0-9_.-]{1,64}$", 64)),
		"fingerprint": stringSpec("^[0-9a-f]{8,32}$", 32),
		"frames": arraySpec(8, objectSpec(map[string]fieldSpec{
			"file": stringSpec("^[A-Za-z0-9._-]{1,128}\\.js$", 128),
			"line": intSpec(0, maxFrameIndex),
			"col":  intSpec(0, maxFrameIndex),
		})),
		"repeat_count": intSpec(1, maxCount),
	},
	"perf.long_task_summary": {
		"count":     intSpec(0, maxCount),
		"sum_ms":    numSpec(0, 100000000),
		"max_ms":    numSpec(0, 100000000),
		"phase":     enumSpec("startup", "first_batch", "background", "interaction", "unknown"),
		"supported": boolSpec(),
	},
	"anomaly.detected": {
		"kind":        enumSpec("viewport_mount_gap", "mount_stalled", "restore_target_missing", "selection_identity_mismatch", "listing_slow"),
		"duration_ms": numSpec(0, 100000000),
		"mounted":     intSpec(0, maxCount),
		"known_items": intSpec(0, maxCount),
		"generation":  intSpec(0, maxCount),
		"evidence":    enumSpec("plan_only", "dom_probe"),
	},
	"user.marker": {
		"category": enumSpec("slow_load", "blank_cards", "position_jump", "selection_issue", "other"),
		"mode":     enumSpec("summary", "detailed"),
	},
}

var baseEventFields = []string{"seq", "t_ms", "level", "event", "load_ref", "operation_ref", "request_ref", "incident_ref", "data"}
var validLevels = []string{"debug", "info", "warn", "error"}

// Event is a validated, rebuilt client event.
type Event struct {
	Seq          int64
	TMs          float64
	Level        string
	Name         string
	LoadRef      *string
	OperationRef *string
	RequestRef   *string
	IncidentRef  *string
	Data         map[string]any
}

// ClientBuild identifies the page build without trusting arbitrary strings.
type ClientBuild struct {
	Version *string `json:"version"`
	Commit  *string `json:"commit"`
}

// Batch is a validated client payload.
type Batch struct {
	ClientSession string
	BatchRef      string
	ClientBuild   ClientBuild
	Events        []Event
}

type batchEnvelope struct {
	SchemaVersion int               `json:"schema_version"`
	ClientSession string            `json:"client_session"`
	BatchRef      string            `json:"batch_ref"`
	ClientBuild   ClientBuild       `json:"client_build"`
	Events        []json.RawMessage `json:"events"`
}

var (
	hex32Re    = regexp.MustCompile(hex32Pattern)
	commitRe   = regexp.MustCompile("^[0-9a-f]{40}$")
	buildVerRe = regexp.MustCompile("^[A-Za-z0-9._-]{1,96}$")
	loadRefRe  = regexp.MustCompile("^l[0-9]{1,12}$")
	opRefRe    = regexp.MustCompile("^o[0-9]{1,12}$")
	incidentRe = regexp.MustCompile("^n[0-9]{1,12}$")
)

// ValidateBatch strictly decodes one request body and rebuilds every event.
func ValidateBatch(body []byte) (*Batch, error) {
	if len(body) == 0 {
		return nil, &ValidationError{Code: "empty_body", Status: 400, Message: "empty request body"}
	}
	if len(body) > MaxBodyBytes {
		return nil, &ValidationError{Code: "body_too_large", Status: 413, Message: "request body too large"}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var envelope batchEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return nil, &ValidationError{Code: "invalid_json", Status: 400, Message: "invalid JSON envelope"}
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, &ValidationError{Code: "trailing_data", Status: 400, Message: "unexpected trailing JSON"}
	}
	if envelope.SchemaVersion != SchemaVersion {
		return nil, &ValidationError{Code: "unsupported_schema", Status: 422, Message: "unsupported schema_version"}
	}
	if !hex32Re.MatchString(envelope.ClientSession) {
		return nil, invalid("invalid_client_session", "client_session must be 32 lowercase hex characters")
	}
	if !hex32Re.MatchString(envelope.BatchRef) {
		return nil, invalid("invalid_batch_ref", "batch_ref must be 32 lowercase hex characters")
	}
	if err := validateClientBuild(envelope.ClientBuild); err != nil {
		return nil, err
	}
	if len(envelope.Events) < 1 || len(envelope.Events) > MaxEvents {
		return nil, invalid("invalid_event_count", "events must contain 1 to 50 entries")
	}
	batch := &Batch{
		ClientSession: envelope.ClientSession,
		BatchRef:      envelope.BatchRef,
		ClientBuild:   envelope.ClientBuild,
		Events:        make([]Event, 0, len(envelope.Events)),
	}
	for i, raw := range envelope.Events {
		event, err := ValidateEvent(raw)
		if err != nil {
			if validation, ok := err.(*ValidationError); ok {
				validation.Message = fmt.Sprintf("events[%d]: %s", i, validation.Message)
			}
			return nil, err
		}
		batch.Events = append(batch.Events, *event)
	}
	return batch, nil
}

func validateClientBuild(build ClientBuild) error {
	if build.Version != nil && !buildVerRe.MatchString(*build.Version) {
		return invalid("invalid_client_build", "client_build.version is not allowed")
	}
	if build.Commit != nil && !commitRe.MatchString(*build.Commit) {
		return invalid("invalid_client_build", "client_build.commit is not allowed")
	}
	return nil
}

// ValidateEvent validates one event object. Unknown fields are rejected.
func ValidateEvent(raw json.RawMessage) (*Event, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, &ValidationError{Code: "invalid_event", Status: 400, Message: "event must be an object"}
	}
	for key := range fields {
		if !contains(baseEventFields, key) {
			return nil, invalid("unknown_field", "unknown event field")
		}
	}
	event := &Event{}
	var err error
	if event.Seq, err = decodeIntField(fields["seq"], 1, maxSeq); err != nil {
		return nil, invalid("invalid_seq", "seq must be an integer within range")
	}
	if event.TMs, err = decodeNumField(fields["t_ms"], 0, maxEventTimeMs); err != nil {
		return nil, invalid("invalid_t_ms", "t_ms must be a finite non-negative number")
	}
	if event.Level, err = decodeEnumField(fields["level"], validLevels); err != nil {
		return nil, invalid("invalid_level", "level is not allowed")
	}
	if event.Name, err = decodeStringField(fields["event"], "", 64); err != nil {
		return nil, invalid("invalid_event_name", "event name is not allowed")
	}
	dataFields, ok := eventSpecs[event.Name]
	if !ok {
		return nil, invalid("unknown_event", "event is not supported")
	}
	if event.LoadRef, err = decodeOptionalRef(fields["load_ref"], loadRefRe); err != nil {
		return nil, invalid("invalid_load_ref", "load_ref is invalid")
	}
	if event.OperationRef, err = decodeOptionalRef(fields["operation_ref"], opRefRe); err != nil {
		return nil, invalid("invalid_operation_ref", "operation_ref is invalid")
	}
	if event.RequestRef, err = decodeOptionalRef(fields["request_ref"], hex32Re); err != nil {
		return nil, invalid("invalid_request_ref", "request_ref is invalid")
	}
	if event.IncidentRef, err = decodeOptionalRef(fields["incident_ref"], incidentRe); err != nil {
		return nil, invalid("invalid_incident_ref", "incident_ref is invalid")
	}
	dataRaw, exists := fields["data"]
	if !exists {
		return nil, invalid("missing_data", "data object is required")
	}
	data, err := validateData(dataRaw, dataFields)
	if err != nil {
		return nil, err
	}
	event.Data = data
	if size := canonicalEventSize(event); size > MaxEventBytes {
		return nil, invalid("event_too_large", "event exceeds 2048 bytes")
	}
	return event, nil
}

func validateData(raw json.RawMessage, spec map[string]fieldSpec) (map[string]any, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, invalid("invalid_data", "data must be an object")
	}
	for key := range fields {
		if _, ok := spec[key]; !ok {
			return nil, invalid("unknown_data_field", "unknown data field")
		}
	}
	out := make(map[string]any, len(spec))
	for key, field := range spec {
		valueRaw, ok := fields[key]
		if !ok {
			if field.optional {
				continue
			}
			return nil, invalid("missing_data_field", "data field is required")
		}
		value, err := decodeValue(field, valueRaw)
		if err != nil {
			return nil, invalid("invalid_data_field", fmt.Sprintf("invalid value for data.%s", key))
		}
		out[key] = value
	}
	return out, nil
}

func decodeValue(spec fieldSpec, raw json.RawMessage) (any, error) {
	if isNull(raw) {
		if spec.nullable {
			return nil, nil
		}
		return nil, fmt.Errorf("null not allowed")
	}
	switch spec.kind {
	case kindInt:
		value, err := decodeIntField(raw, spec.minInt, spec.maxInt)
		if err != nil {
			return nil, err
		}
		return value, nil
	case kindNum:
		return decodeNumField(raw, spec.minNum, spec.maxNum)
	case kindBool:
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		return value, nil
	case kindEnum:
		return decodeEnumField(raw, spec.enum)
	case kindRef:
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || !spec.pattern.MatchString(value) {
			return nil, fmt.Errorf("bad reference")
		}
		return value, nil
	case kindString:
		return decodeStringField(raw, patternText(spec.pattern), spec.maxLen)
	case kindArray:
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, err
		}
		if len(items) > spec.maxItems {
			return nil, fmt.Errorf("too many items")
		}
		out := make([]any, 0, len(items))
		for _, item := range items {
			decoded, err := decodeValue(*spec.item, item)
			if err != nil {
				return nil, err
			}
			out = append(out, decoded)
		}
		return out, nil
	case kindObject:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			return nil, fmt.Errorf("not an object")
		}
		for key := range fields {
			if _, ok := spec.fields[key]; !ok {
				return nil, fmt.Errorf("unknown field")
			}
		}
		out := make(map[string]any, len(spec.fields))
		for key, field := range spec.fields {
			valueRaw, ok := fields[key]
			if !ok {
				if field.optional {
					continue
				}
				return nil, fmt.Errorf("missing field")
			}
			value, err := decodeValue(field, valueRaw)
			if err != nil {
				return nil, err
			}
			out[key] = value
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown kind")
}

func decodeIntField(raw json.RawMessage, min, max int64) (int64, error) {
	if raw == nil || !startsJSONNumber(raw) {
		return 0, fmt.Errorf("missing or not a number")
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, err
	}
	value, err := number.Int64()
	if err != nil {
		return 0, err
	}
	if value < min || value > max {
		return 0, fmt.Errorf("out of range")
	}
	return value, nil
}

func decodeNumField(raw json.RawMessage, min, max float64) (float64, error) {
	if raw == nil || !startsJSONNumber(raw) {
		return 0, fmt.Errorf("missing or not a number")
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, err
	}
	value, err := number.Float64()
	if err != nil {
		return 0, err
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < min || value > max {
		return 0, fmt.Errorf("invalid number")
	}
	return value, nil
}

func decodeEnumField(raw json.RawMessage, allowed []string) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	if !contains(allowed, value) {
		return "", fmt.Errorf("not allowed")
	}
	return value, nil
}

func decodeStringField(raw json.RawMessage, pattern string, maxLen int) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	if len(value) > maxLen {
		return "", fmt.Errorf("too long")
	}
	if pattern != "" && !regexp.MustCompile(pattern).MatchString(value) {
		return "", fmt.Errorf("bad pattern")
	}
	return value, nil
}

func decodeOptionalRef(raw json.RawMessage, pattern *regexp.Regexp) (*string, error) {
	if raw == nil || isNull(raw) {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || !pattern.MatchString(value) {
		return nil, fmt.Errorf("bad reference")
	}
	return &value, nil
}

func canonicalEventSize(event *Event) int {
	payload := map[string]any{
		"seq": event.Seq, "t_ms": event.TMs, "level": event.Level, "event": event.Name,
		"load_ref": event.LoadRef, "operation_ref": event.OperationRef,
		"request_ref": event.RequestRef, "incident_ref": event.IncidentRef,
		"data": event.Data,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return MaxEventBytes + 1
	}
	return len(encoded)
}

func patternText(pattern *regexp.Regexp) string {
	if pattern == nil {
		return ""
	}
	return pattern.String()
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// json.Number also unmarshals JSON strings; require a numeric token so "200"
// is rejected instead of silently accepted.
func startsJSONNumber(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	first := trimmed[0]
	return first == '-' || (first >= '0' && first <= '9')
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// EventNames exposes the closed event list for status/tests.
func EventNames() []string {
	names := make([]string, 0, len(eventSpecs))
	for name := range eventSpecs {
		names = append(names, name)
	}
	return names
}

// ValidateRequestRef returns a valid 32-hex trace reference or "".
func ValidateRequestRef(value string) string {
	if hex32Re.MatchString(value) {
		return value
	}
	return ""
}

func sanitizeEventName(name string) string {
	// Kept for report tooling; names are already closed above.
	return strings.TrimSpace(name)
}
