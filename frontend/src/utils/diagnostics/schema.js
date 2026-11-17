/* eslint-disable security/detect-object-injection, security/detect-non-literal-regexp -- values are validated against the closed schema before lookup */
// Closed client diagnostics event schema v1.
// Keep in sync with custom/CLIENT-DIAGNOSTICS-SCHEMA-V1.md and
// diagnostics/schema-v1.json. Unknown events/fields are rejected, never passed
// through: every stored value is rebuilt from this whitelist.
import { LIMITS, SCHEMA_VERSION } from "./config.js";

const MAX_COUNT = 1000000;
const MAX_SAFE = Number.MAX_SAFE_INTEGER;

const int = (min = 0, max = MAX_COUNT) => ({ t: "int", min, max });
const num = (min = 0, max = 100000000) => ({ t: "num", min, max });
const bool = () => ({ t: "bool" });
const oneOf = (...values) => ({ t: "enum", values });
const ref = (regex) => ({ t: "ref", regex });
const nullable = (spec) => ({ ...spec, nullable: true });
// Optional fields stay absent when the producer did not send them: older
// clients must keep validating after a backward-compatible field extension.
const optional = (spec) => ({ ...spec, optional: true });
const str = (regex, max = 128) => ({ t: "str", regex, max });

const REF = {
  dir: "^d[0-9]{1,12}$",
  item: "^i[0-9]{1,12}$",
};

const HEX32 = "^[0-9a-f]{32}$";

const frameFields = {
  file: str("^[A-Za-z0-9._-]{1,128}\\.js$", 128),
  line: int(0, 10000000),
  col: int(0, 10000000),
};

export const EVENT_FIELDS = Object.freeze({
  "session.start": {
    browser_family: oneOf("chrome", "firefox", "safari", "edge", "other", "unknown"),
    browser_major: nullable(int(0, 999)),
    viewport_width: int(0, 20000),
    viewport_height: int(0, 20000),
    dpr: nullable(num(0.5, 10)),
    screen_width: nullable(int(0, 20000)),
    screen_height: nullable(int(0, 20000)),
    locale: nullable(str("^[A-Za-z-]{2,20}$", 20)),
    touch: bool(),
    capability_long_tasks: oneOf("supported", "unsupported", "unknown"),
    capability_memory: oneOf("supported", "unsupported", "unknown"),
    capability_paint_timing: oneOf("supported", "unsupported", "unknown"),
    mode: oneOf("summary", "detailed"),
    detailed: bool(),
  },
  "session.end": {
    reason: oneOf("pagehide", "unload", "logout", "disabled", "manual"),
    duration_ms: num(),
  },
  "listing.start": {
    dir_ref: ref(REF.dir),
    view: oneOf("mosaic", "gallery", "icons", "list", "compact", "unknown"),
    sort_by: oneOf("name", "size", "modified", "type", "unknown"),
    sort_asc: bool(),
    density: nullable(num(0, 1)),
    paged: bool(),
    trigger: oneOf("initial", "route", "reload", "preview_return", "delete", "sort", "view_change", "hash", "manual"),
  },
  "listing.response": {
    api: oneOf("resources", "resources_list"),
    // 0 means no HTTP response (network failure); otherwise a real status.
    status: int(0, 599),
    headers_wait_ms: num(),
    body_decode_ms: nullable(num()),
    known_items: nullable(int()),
    paged: bool(),
    outcome: oneOf("ok", "error", "cancelled", "aborted"),
  },
  "listing.store_prepared": {
    elapsed_ms: num(),
    item_count: int(),
    selected_before: int(),
    selected_after: int(),
  },
  "listing.layout": {
    geom_items: int(),
    groups: int(0, 3),
    rows: int(),
    density: nullable(num(0, 1)),
    width_px: nullable(int(0, 20000)),
    elapsed_ms: num(),
    generation: int(),
    method: oneOf("mosaic", "estimated", "unknown"),
  },
  "listing.first_batch_paint_estimate": {
    elapsed_ms: num(),
    queued_items: int(),
    committed_items: int(),
    known_items: int(),
    method: oneOf("double_raf", "next_tick", "unknown"),
  },
  "listing.mount_summary": {
    mounted: int(),
    total: int(),
    batches: int(),
    urgent_batches: int(),
    elapsed_sum_ms: num(),
    elapsed_max_ms: num(),
    paused_reason: oneOf("none", "scroll", "gesture", "prompt", "hidden", "interaction", "unknown"),
    dropped: nullable(int()),
  },
  "listing.mount_batch": {
    generation: int(),
    urgent: bool(),
    fast: bool(),
    size: int(0, 10000),
    enqueue_ms: num(),
    flush_ms: nullable(num()),
  },
  "listing.complete": {
    known_items: int(),
    mounted_items: int(),
    elapsed_ms: num(),
    paged: bool(),
  },
  "listing.cancel": {
    reason: oneOf("route", "reload", "sort", "view", "unmount", "superseded", "error"),
    elapsed_ms: num(),
    mounted_items: int(),
  },
  "scroll.summary": {
    range_top_px: num(),
    range_height_px: num(),
    fast_ms: num(),
    slow_ms: num(),
    urgent_mounted: int(),
    scrollbar_drag: nullable(bool()),
    samples: int(),
  },
  "layout.change": {
    reason: oneOf("resize", "zoom", "density_ratio", "view_change", "gesture_commit"),
    old_density: nullable(num(0, 1)),
    new_density: nullable(num(0, 1)),
    old_width: nullable(int(0, 20000)),
    new_width: nullable(int(0, 20000)),
    generation: int(),
    anchor_ref: nullable(ref("^[di][0-9]{1,12}$")),
    offset_before: nullable(num()),
    offset_after: nullable(num()),
    result: oneOf("success", "failed", "cancelled", "unknown"),
  },
  "images.summary": {
    thumb_requests: int(),
    thumb_ok: int(),
    thumb_failed: int(),
    thumb_cancelled: int(),
    original_requests: int(),
    original_ok: int(),
    original_failed: int(),
    original_cancelled: int(),
    inflight: int(),
    elapsed_sum_ms: num(),
    elapsed_max_ms: num(),
    cache_known: oneOf("none", "partial"),
  },
  "images.failure": {
    item_ref: nullable(ref(REF.item)),
    request_kind: oneOf("thumb", "original"),
    status: nullable(int(100, 599)),
    phase: oneOf("network", "decode", "dom", "unknown"),
    category: oneOf("http", "network", "decode", "abort", "other"),
  },
  "selection.change": {
    operation: oneOf("add", "remove", "toggle", "range", "clear", "reconcile", "select_all"),
    before: int(),
    after: int(),
    known_items: int(),
    multiple: bool(),
    merged_count: int(1),
  },
  "navigation.restore": {
    reason: oneOf("preview_return", "hash", "delete", "keyboard", "paged", "move", "refresh"),
    target_ref: nullable(ref("^[di][0-9]{1,12}$")),
    pre_mounted: bool(),
    requested_offset: nullable(num()),
    actual_offset: nullable(num()),
    result: oneOf("success", "failed", "cancelled"),
    elapsed_ms: num(),
    strategy: optional(oneOf("identity", "neighbor", "fallback_pixels", "empty")),
    reason_code: optional(oneOf(
      "empty_directory", "no_identity", "target_not_mounted", "target_lost_after_scroll",
      "offset_mismatch", "target_not_in_plan", "reveal_failed", "settle_timeout",
      "user_scroll", "superseded", "unmounted", "fetch_failed", "directory_changed",
      "restore_error",
    )),
  },
  "operation.result": {
    operation: oneOf("delete", "move", "rename", "copy", "archive", "unarchive"),
    requested: int(),
    succeeded: int(),
    failed: int(),
    elapsed_ms: num(),
    refreshed: nullable(bool()),
    selection_before: int(),
    selection_after: int(),
    outcome: optional(oneOf("ok", "partial", "failed", "unknown")),
  },
  "runtime.error": {
    category: oneOf("vue", "window", "unhandled", "resource", "other"),
    code: nullable(str("^[A-Za-z0-9_.-]{1,64}$", 64)),
    fingerprint: str("^[0-9a-f]{8,32}$", 32),
    frames: { t: "array", max: 8, items: { t: "object", fields: frameFields } },
    repeat_count: int(1),
  },
  "perf.long_task_summary": {
    count: int(),
    sum_ms: num(),
    max_ms: num(),
    phase: oneOf("startup", "first_batch", "background", "interaction", "unknown"),
    supported: bool(),
  },
  "anomaly.detected": {
    kind: oneOf("viewport_mount_gap", "mount_stalled", "restore_target_missing", "selection_identity_mismatch", "listing_slow"),
    duration_ms: num(),
    mounted: int(),
    known_items: int(),
    generation: int(),
    evidence: oneOf("plan_only", "dom_probe"),
  },
  "user.marker": {
    category: oneOf("slow_load", "blank_cards", "position_jump", "selection_issue", "other"),
    mode: oneOf("summary", "detailed"),
  },
});

export const EVENT_NAMES = Object.freeze(Object.keys(EVENT_FIELDS));

const BASE_FIELDS = ["seq", "t_ms", "level", "event", "load_ref", "operation_ref", "request_ref", "incident_ref", "data"];
const LEVELS = ["debug", "info", "warn", "error"];

function fail(path, reason) {
  return { ok: false, reason: path ? `${path  }:${  reason}` : reason };
}

function isPlainObject(value) {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const proto = Object.getPrototypeOf(value);
  return proto === Object.prototype || proto === null;
}

function validateValue(spec, value, path) {
  if (value === null) {
    return spec.nullable ? { ok: true, value: null } : fail(path, "null_not_allowed");
  }
  switch (spec.t) {
    case "int":
      if (typeof value !== "number" || !Number.isFinite(value) || !Number.isInteger(value)) {
        return fail(path, "not_integer");
      }
      if (value < spec.min || value > spec.max) return fail(path, "out_of_range");
      return { ok: true, value };
    case "num":
      if (typeof value !== "number" || !Number.isFinite(value)) return fail(path, "not_finite_number");
      if (value < spec.min || value > spec.max) return fail(path, "out_of_range");
      return { ok: true, value };
    case "bool":
      return typeof value === "boolean" ? { ok: true, value } : fail(path, "not_boolean");
    case "enum":
      return spec.values.includes(value) ? { ok: true, value } : fail(path, "not_allowed_value");
    case "ref":
      return typeof value === "string" && new RegExp(spec.regex).test(value)
        ? { ok: true, value }
        : fail(path, "bad_reference");
    case "str":
      if (typeof value !== "string") return fail(path, "not_string");
      if (value.length > spec.max) return fail(path, "too_long");
      if (spec.regex && !new RegExp(spec.regex).test(value)) return fail(path, "bad_string");
      return { ok: true, value };
    case "array": {
      if (!Array.isArray(value)) return fail(path, "not_array");
      if (value.length > spec.max) return fail(path, "too_many_items");
      const out = [];
      for (let i = 0; i < value.length; i++) {
        const item = validateValue(spec.items, value[i], `${path  }[${  i  }]`);
        if (!item.ok) return item;
        out.push(item.value);
      }
      return { ok: true, value: out };
    }
    case "object": {
      if (!isPlainObject(value)) return fail(path, "not_object");
      const keys = Object.keys(value);
      if (keys.some((key) => !Object.hasOwn(spec.fields, key))) {
        return fail(path, "unknown_field");
      }
      const out = {};
      for (const [key, fieldSpec] of Object.entries(spec.fields)) {
        if (!Object.hasOwn(value, key)) {
          if (fieldSpec.optional) continue;
          return fail(`${path  }.${  key}`, "missing");
        }
        const field = validateValue(fieldSpec, value[key], `${path  }.${  key}`);
        if (!field.ok) return field;
        out[key] = field.value;
      }
      return { ok: true, value: out };
    }
    default:
      return fail(path, "unknown_spec");
  }
}

/** Validate and rebuild one event from the whitelist. Never returns extra fields. */
export function sanitizeEvent(event) {
  if (!isPlainObject(event)) return fail("event", "not_object");
  const keys = Object.keys(event);
  for (const key of keys) {
    if (!BASE_FIELDS.includes(key)) return fail("event", "unknown_field");
  }
  if (typeof event.event !== "string" || !Object.hasOwn(EVENT_FIELDS, event.event)) {
    return fail("event", "unknown_event");
  }
  if (typeof event.seq !== "number" || !Number.isFinite(event.seq) || !Number.isInteger(event.seq) || event.seq < 1 || event.seq > MAX_SAFE) {
    return fail("seq", "invalid_sequence");
  }
  if (typeof event.t_ms !== "number" || !Number.isFinite(event.t_ms) || event.t_ms < 0) {
    return fail("t_ms", "invalid_time");
  }
  if (!LEVELS.includes(event.level)) return fail("level", "invalid_level");
  const out = {
    seq: event.seq,
    t_ms: event.t_ms,
    level: event.level,
    event: event.event,
    load_ref: null,
    operation_ref: null,
    request_ref: null,
    incident_ref: null,
    data: {},
  };
  for (const [key, spec] of [
    ["load_ref", ref("^l[0-9]{1,12}$")],
    ["operation_ref", ref("^o[0-9]{1,12}$")],
    ["request_ref", ref(HEX32)],
    ["incident_ref", ref("^n[0-9]{1,12}$")],
  ]) {
    const value = event[key];
    if (value === undefined || value === null) continue;
    const checked = validateValue({ ...spec, nullable: true }, value, key);
    if (!checked.ok) return checked;
    out[key] = checked.value;
  }
  if (!isPlainObject(event.data)) return fail("data", "not_object");
  const dataFields = EVENT_FIELDS[event.event];
  const dataKeys = Object.keys(event.data);
  for (const key of dataKeys) {
    if (!Object.hasOwn(dataFields, key)) return fail("data", "unknown_field");
  }
  for (const [key, spec] of Object.entries(dataFields)) {
    if (!Object.hasOwn(event.data, key)) {
      if (spec.optional) continue;
      return fail(`data.${  key}`, "missing");
    }
    const checked = validateValue(spec, event.data[key], `data.${  key}`);
    if (!checked.ok) return checked;
    out.data[key] = checked.value;
  }
  const size = utf8Bytes(JSON.stringify(out));
  if (size > LIMITS.MAX_EVENT_BYTES) return fail("event", "too_large");
  return { ok: true, value: out, bytes: size };
}

const HEX32_RE = new RegExp(HEX32);
const COMMIT_RE = /^[0-9a-f]{40}$/;
const VERSION_RE = /^[A-Za-z0-9._-]{1,96}$/;

export function validateEnvelope(envelope) {
  if (!isPlainObject(envelope)) return fail("batch", "not_object");
  const allowed = ["schema_version", "client_session", "batch_ref", "client_build", "events"];
  if (Object.keys(envelope).some((key) => !allowed.includes(key))) return fail("batch", "unknown_field");
  if (envelope.schema_version !== SCHEMA_VERSION) return fail("schema_version", "unsupported");
  if (typeof envelope.client_session !== "string" || !HEX32_RE.test(envelope.client_session)) {
    return fail("client_session", "bad_identifier");
  }
  if (typeof envelope.batch_ref !== "string" || !HEX32_RE.test(envelope.batch_ref)) {
    return fail("batch_ref", "bad_identifier");
  }
  const build = envelope.client_build;
  if (!isPlainObject(build)) return fail("client_build", "not_object");
  if (Object.keys(build).some((key) => key !== "version" && key !== "commit")) {
    return fail("client_build", "unknown_field");
  }
  const version = build.version;
  if (version !== null && (typeof version !== "string" || !VERSION_RE.test(version))) {
    return fail("client_build.version", "bad_version");
  }
  const commit = build.commit;
  if (commit !== null && (typeof commit !== "string" || !COMMIT_RE.test(commit))) {
    return fail("client_build.commit", "bad_commit");
  }
  if (!Array.isArray(envelope.events) || envelope.events.length < 1 || envelope.events.length > LIMITS.MAX_EVENTS_PER_BATCH) {
    return fail("events", "bad_count");
  }
  const events = [];
  for (let i = 0; i < envelope.events.length; i++) {
    const checked = sanitizeEvent(envelope.events[i]);
    if (!checked.ok) return { ok: false, reason: `events[${  i  }]:${  checked.reason}` };
    events.push(checked.value);
  }
  return {
    ok: true,
    value: {
      schema_version: SCHEMA_VERSION,
      client_session: envelope.client_session,
      batch_ref: envelope.batch_ref,
      client_build: { version, commit },
      events,
    },
  };
}

export function sanitizeEventForBatch(event) {
  return sanitizeEvent(event);
}

/** UTF-8 byte length without allocating a big buffer for every estimate. */
export function utf8Bytes(value) {
  const text = typeof value === "string" ? value : JSON.stringify(value);
  let bytes = 0;
  for (let i = 0; i < text.length; i++) {
    const code = text.charCodeAt(i);
    if (code < 0x80) bytes += 1;
    else if (code < 0x800) bytes += 2;
    else if (code >= 0xd800 && code <= 0xdbff) {
      bytes += 4;
      i++;
    } else bytes += 3;
  }
  return bytes;
}

/** Deterministic 64-bit (two FNV-1a passes) fingerprint, 16 lowercase hex. */
export function fingerprint(parts) {
  const text = parts.map((part) => String(part ?? "")).join("|");
  const pass = (seed) => {
    let hash = seed >>> 0;
    for (let i = 0; i < text.length; i++) {
      hash ^= text.charCodeAt(i);
      hash = Math.imul(hash, 0x01000193);
    }
    return (hash >>> 0).toString(16).padStart(8, "0");
  };
  return pass(0x811c9dc5) + pass(0x9e3779b9);
}

const FRAME_RE = /([A-Za-z0-9._-]{1,128}\.js):(\d{1,7}):(\d{1,7})/g;

/** Keep at most 8 asset js frames, no URL/path/query/fragment/arguments. */
export function sanitizeStack(stack) {
  if (typeof stack !== "string" || !stack) return [];
  const frames = [];
  const seen = new Set();
  for (const match of stack.matchAll(FRAME_RE)) {
    const file = match[1];
    const line = Number(match[2]);
    const col = Number(match[3]);
    if (line > 10000000 || col > 10000000) continue;
    const key = `${file  }:${  line  }:${  col}`;
    if (seen.has(key)) continue;
    seen.add(key);
    frames.push({ file, line, col });
    if (frames.length >= 8) break;
  }
  return frames;
}

function safeErrorCode(error) {
  const candidates = [error?.code, error?.name];
  for (const value of candidates) {
    if (typeof value === "string" && /^[A-Za-z0-9_.-]{1,64}$/.test(value)) return value;
  }
  return null;
}

/** Build the runtime.error data payload. message/cause are never read. */
export function sanitizeRuntimeError(source, error) {
  const category = ["vue", "window", "unhandled", "resource", "other"].includes(source) ? source : "other";
  const code = safeErrorCode(error);
  const frames = sanitizeStack(error?.stack);
  const fingerprintValue = fingerprint([category, code || "", frames.map((f) => f.file).join(","), error?.errorCode || ""]);
  return { category, code, fingerprint: fingerprintValue, frames, repeat_count: 1 };
}

export function makeIdentifier(prefix, counter) {
  return prefix + String(counter);
}

export function isHex32(value) {
  return typeof value === "string" && HEX32_RE.test(value);
}