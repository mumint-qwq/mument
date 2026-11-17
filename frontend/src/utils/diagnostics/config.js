// Client diagnostics limits and defaults (v1). Keep every default here so
// tests can reference the same values instead of duplicating magic numbers.
export const SCHEMA_VERSION = 1;

export const LIMITS = Object.freeze({
  MAX_REQUEST_BYTES: 65536,
  MAX_EVENTS_PER_BATCH: 50,
  MAX_BATCH_BYTES: 48 * 1024,
  MAX_EVENT_BYTES: 2048,
  HISTORY_EVENTS: 1000,
  HISTORY_BYTES: 1024 * 1024,
  QUEUE_EVENTS: 200,
  QUEUE_BYTES: 256 * 1024,
  ENTRY_CODES: 4096,
  SEND_INTERVAL_MS: 5000,
  SEND_EARLY_EVENTS: 50,
  SEND_EARLY_BYTES: 48 * 1024,
  UPLOAD_TIMEOUT_MS: 5000,
  RETRY_BACKOFF_MS: [5000, 10000, 20000, 40000],
  MAX_ATTEMPTS: 5,
  MAX_BATCH_AGE_MS: 120000,
  DETAILED_MAX_MS: 10 * 60 * 1000,
  ERROR_DEDUPE_MS: 10000,
  CONTEXT_WINDOW_MS: 30000,
  CONTEXT_MAX_EVENTS: 100,
  AUTO_PROMOTE_MAX: 20,
  AUTO_PROMOTE_INTERVAL_MS: 30000,
  ANOMALY_REPEAT_MS: 30000,
  SAMPLE_INTERVAL_MS: 500,
  SLOW_FIRST_BATCH_MS: 1500,
  VIEWPORT_GAP_MS: 500,
  MOUNT_STALL_MS: 5000,
  SELECTION_MAX_PER_SECOND: 5,
  PAGEHIDE_MAX_BYTES: 16 * 1024,
});

export const STORAGE_KEYS = Object.freeze({
  mode: "quantum.diagnostics.mode",
});

export const STATUS = Object.freeze({
  DISABLED: "disabled",
  WAITING_LOGIN: "waiting_login",
  READY: "ready",
  SENDING: "sending",
  OFFLINE_BUFFER: "offline_buffer",
  PAUSED: "paused",
  SERVER_STORAGE_ERROR: "server_storage_error",
  SERVER_UNSUPPORTED: "server_unsupported",
  DEGRADED: "degraded",
});

export const MODES = Object.freeze(["off", "summary", "detailed"]);

export const MERGEABLE_EVENTS = Object.freeze([
  "listing.mount_summary",
  "images.summary",
  "scroll.summary",
  "selection.change",
]);
