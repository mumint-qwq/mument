import { describe, expect, it } from "vitest";
import { createDiagnosticsRecorder } from "./recorder.js";
import { LIMITS } from "./config.js";

function makeRecorder(started = true) {
  let clock = 0;
  let sessionCounter = 0;
  const recorder = createDiagnosticsRecorder({
    now: () => clock,
    randomHex32: () => `0123456789abcdef0123456789abcd${  (sessionCounter++).toString(16).padStart(2, "0")}`,
  });
  recorder.setMode("summary");
  if (started) recorder.startSession({ viewportWidth: 414, viewportHeight: 900, touch: true });
  return {
    recorder,
    advance(ms) { clock += ms; },
    now() { return clock; },
  };
}

const response = (overrides = {}) => ({
  api: "resources",
  status: 200,
  headers_wait_ms: 10,
  body_decode_ms: 20,
  known_items: 4000,
  paged: false,
  outcome: "ok",
  ...overrides,
});

const start = (recorder, path = "/A") => recorder.beginLoad({
  path,
  view: "mosaic",
  sortBy: "name",
  sortAsc: true,
  density: 0.5,
  paged: false,
  trigger: "route",
});

describe("recorder session and load refs", () => {
  it("emits session.start and unique load refs for A-B-A", () => {
    const { recorder } = makeRecorder();
    const a1 = start(recorder, "/A");
    recorder.finishLoad(a1, "cancel", { reason: "route" });
    const b = start(recorder, "/B");
    recorder.finishLoad(b, "complete", { knownItems: 1, mountedItems: 1 });
    const a2 = start(recorder, "/A");
    recorder.finishLoad(a2, "complete", { knownItems: 1, mountedItems: 1 });
    expect([a1.loadRef, b.loadRef, a2.loadRef]).toEqual(["l1", "l2", "l3"]);
    expect([a1.dirRef, b.dirRef, a2.dirRef]).toEqual(["d1", "d2", "d1"]);
    expect(recorder.exportSnapshot().events[0].event).toBe("session.start");
  });

  it("does not record after the session ends", () => {
    const { recorder } = makeRecorder();
    recorder.endSession("pagehide");
    const before = recorder.getHistoryCount();
    const ctx = start(recorder);
    expect(ctx).toBeNull();
    expect(recorder.getHistoryCount()).toBe(before);
  });
});

describe("generation-scoped recording", () => {
  it("ignores a stale load context after an identity switch", () => {
    const { recorder } = makeRecorder();
    recorder.setServerEnabled(true);
    const old = start(recorder, "/A");
    recorder.resetIdentity("user-b");
    recorder.startSession({ viewportWidth: 1, viewportHeight: 1 });
    recorder.setServerEnabled(true);
    expect(recorder.record("listing.response", response(), old)).toBeNull();
    expect(recorder.exportSnapshot().events.some((event) => event.load_ref === old.loadRef)).toBe(false);
    expect(recorder.markIssue("slow_load", old)).toBeNull();
    const current = start(recorder, "/B");
    expect(recorder.markIssue("other", current)).toBe("n1");
  });

  it("ignores a stale load context after disabling and re-enabling capture", () => {
    const { recorder } = makeRecorder();
    recorder.setServerEnabled(true);
    const old = start(recorder, "/A");
    recorder.setServerEnabled(false);
    recorder.startSession({ viewportWidth: 1, viewportHeight: 1 });
    recorder.setServerEnabled(true);
    expect(recorder.record("listing.response", response(), old)).toBeNull();
    expect(recorder.getQueueEvents().some((event) => event.load_ref === old.loadRef)).toBe(false);
  });

  it("keeps a stale startup record out of the new session after a rebuild", () => {
    const { recorder } = makeRecorder();
    const old = start(recorder, "/A");
    recorder.record("listing.response", response(), old);
    recorder.resetIdentity("user-b");
    recorder.startSession({ viewportWidth: 1, viewportHeight: 1 });
    expect(recorder.record("listing.response", response(), old)).toBeNull();
    recorder.setServerEnabled(true);
    expect(recorder.getQueueEvents().some((event) => event.load_ref === old.loadRef)).toBe(false);
  });

  it("does not let a stale finishLoad affect the new session", () => {
    const { recorder } = makeRecorder();
    recorder.setServerEnabled(true);
    const old = start(recorder, "/A");
    recorder.resetIdentity("user-b");
    recorder.startSession({ viewportWidth: 1, viewportHeight: 1 });
    recorder.setServerEnabled(true);
    const current = start(recorder, "/B");
    expect(current.loadRef).not.toBe(old.loadRef);
    expect(recorder.finishLoad(old, "cancel", { reason: "route" })).toBeNull();
    const completed = recorder.finishLoad(current, "complete", { knownItems: 1, mountedItems: 1 });
    expect(completed).not.toBeNull();
    expect(completed.load_ref).toBe(current.loadRef);
  });

  it("keeps the current load and global events after the load completes", () => {
    const { recorder } = makeRecorder();
    recorder.setServerEnabled(true);
    const ctx = start(recorder, "/A");
    expect(recorder.record("listing.response", response(), ctx)).not.toBeNull();
    recorder.finishLoad(ctx, "complete", { knownItems: 1, mountedItems: 1 });
    expect(recorder.record("scroll.summary", {
      range_top_px: 0, range_height_px: 1, fast_ms: 0, slow_ms: 0,
      urgent_mounted: 0, scrollbar_drag: null, samples: 0,
    }, ctx)).not.toBeNull();
    expect(recorder.record("user.marker", { category: "other", mode: "summary" })).not.toBeNull();
  });
});

describe("bounded history and queue", () => {
  it("caps history at the configured event count", () => {
    const { recorder } = makeRecorder();
    const ctx = start(recorder);
    for (let i = 0; i < LIMITS.HISTORY_EVENTS + 5; i++) {
      recorder.record("listing.response", response(), ctx);
    }
    expect(recorder.getHistoryCount()).toBe(LIMITS.HISTORY_EVENTS);
    expect(recorder.getStatus().dropped.history).toBeGreaterThan(0);
  });

  it("evicts debug before info when the upload queue is full", () => {
    const { recorder } = makeRecorder();
    recorder.setMode("detailed");
    recorder.startSession({ viewportWidth: 414, viewportHeight: 900 });
    recorder.setServerEnabled(true);
    for (let i = 0; i < LIMITS.QUEUE_EVENTS; i++) {
      recorder.record("listing.mount_batch", {
        generation: 1, urgent: false, fast: false, size: 10, enqueue_ms: 0.1, flush_ms: 1,
      });
    }
    expect(recorder.getStatus().queue_events).toBe(LIMITS.QUEUE_EVENTS);
    recorder.record("user.marker", { category: "other", mode: "detailed" });
    const events = recorder.getQueueEvents();
    expect(events.some((event) => event.event === "user.marker")).toBe(true);
    expect(events.filter((event) => event.level === "debug").length).toBeLessThan(LIMITS.QUEUE_EVENTS);
  });

  it("keeps a sequence gap for rejected or deduplicated records", () => {
    const { recorder } = makeRecorder();
    const ctx = start(recorder);
    const first = recorder.record("listing.response", response(), ctx);
    const rejected = recorder.record("listing.response", response({ status: "200" }), ctx);
    const third = recorder.record("listing.response", response(), ctx);
    expect(rejected).toBeNull();
    expect(first.seq).toBe(3);
    expect(third.seq).toBe(5);
    expect(recorder.getStatus().dropped.invalid).toBe(1);
  });

  it("never queues debug in summary mode but does in detailed mode", () => {
    const { recorder } = makeRecorder();
    recorder.setServerEnabled(true);
    const ctx = start(recorder);
    recorder.record("listing.mount_batch", { generation: 1, urgent: false, fast: false, size: 1, enqueue_ms: 0, flush_ms: null }, ctx);
    expect(recorder.getQueueEvents().some((event) => event.level === "debug")).toBe(false);
    recorder.setMode("detailed");
    recorder.record("listing.mount_batch", { generation: 1, urgent: false, fast: false, size: 1, enqueue_ms: 0, flush_ms: null }, ctx);
    expect(recorder.getQueueEvents().some((event) => event.level === "debug")).toBe(true);
  });
});

describe("error dedupe and incidents", () => {
  it("merges repeated errors within 10s and emits repeat_count after the window", () => {
    const { recorder, advance } = makeRecorder();
    const error = new TypeError("private /srv/pictures/a.jpg");
    error.stack = "TypeError: private\n    at f (https://host/assets/index-a.js:1:2)";
    expect(recorder.recordRuntimeError("vue", error)).not.toBeNull();
    expect(recorder.recordRuntimeError("vue", error)).toBeNull();
    advance(LIMITS.ERROR_DEDUPE_MS + 1);
    recorder.flushErrorDedupe();
    const errors = recorder.exportSnapshot().events.filter((event) => event.event === "runtime.error");
    expect(errors).toHaveLength(2);
    expect(errors[1].data.repeat_count).toBe(2);
    expect(JSON.stringify(errors)).not.toContain("private");
  });

  it("promotes recent debug context with original seq for a user marker", () => {
    const { recorder } = makeRecorder();
    recorder.setMode("detailed");
    recorder.setServerEnabled(true);
    recorder.startSession({ viewportWidth: 414, viewportHeight: 900 });
    const ctx = start(recorder);
    const debug = recorder.record("listing.mount_batch", { generation: 1, urgent: false, fast: false, size: 1, enqueue_ms: 0, flush_ms: null }, ctx);
    const incident = recorder.markIssue("blank_cards", ctx);
    expect(incident).toMatch(/^n[0-9]+$/);
    const marker = recorder.getQueueEvents().find((event) => event.event === "user.marker");
    expect(marker.incident_ref).toBe(incident);
    expect(recorder.getQueueEvents().some((event) => event.seq === debug.seq)).toBe(true);
    expect(recorder.exportSnapshot().events.some((event) => event.seq === debug.seq)).toBe(true);
  });
});

describe("lifecycle and server states", () => {
  it("holds a small sanitized startup buffer until the server is confirmed", () => {
    const { recorder } = makeRecorder();
    const ctx = start(recorder);
    recorder.record("listing.response", response(), ctx);
    expect(recorder.getStatus().queue_events).toBe(0);
    recorder.setServerEnabled(true);
    expect(recorder.getStatus().queue_events).toBeGreaterThan(0);
    expect(recorder.getQueueEvents().every((event) => event.event !== "session.start" || event.data.mode === "summary")).toBe(true);
  });

  it("stops and clears history when the server is disabled", () => {
    const { recorder } = makeRecorder();
    recorder.setServerEnabled(true);
    const ctx = start(recorder);
    recorder.record("listing.response", response(), ctx);
    expect(recorder.getStatus().queue_events).toBeGreaterThan(0);
    expect(recorder.getHistoryCount()).toBeGreaterThan(0);
    recorder.setServerEnabled(false);
    expect(recorder.getStatus().queue_events).toBe(0);
    expect(recorder.getHistoryCount()).toBe(0);
    expect(recorder.isStarted()).toBe(false);
    expect(recorder.getClientSession()).toBeNull();
    expect(recorder.record("user.marker", { category: "other", mode: "summary" })).toBeNull();
    expect(recorder.getHistoryCount()).toBe(0);
  });

  it("settles the last image completion even without a new request", () => {
    const { recorder, advance } = makeRecorder();
    recorder.setServerEnabled(true);
    const token = recorder.noteImageStart("thumb");
    advance(100);
    recorder.noteImageEnd("thumb", "ok", 100, {}, token);
    recorder.flushImageSummary();
    const summary = recorder.exportSnapshot().events.filter((event) => event.event === "images.summary").at(-1);
    expect(summary.data).toMatchObject({ thumb_requests: 1, thumb_ok: 1, inflight: 0 });
  });

  it("ignores an old image completion after an identity switch", () => {
    const { recorder } = makeRecorder();
    recorder.setServerEnabled(true);
    const oldToken = recorder.noteImageStart("thumb");
    expect(recorder.resetIdentity("user-b")).toBe(true);
    recorder.startSession({ viewportWidth: 1, viewportHeight: 1 });
    expect(recorder.noteImageEnd("thumb", "ok", 5, {}, oldToken)).toBe(false);
    expect(recorder.getStatus().dropped.invalid).toBe(0);
    const summary = recorder.exportSnapshot().events.filter((event) => event.event === "images.summary");
    expect(summary.some((event) => event.data.thumb_ok > 0)).toBe(false);
  });

  it("does not settle a request that started while diagnostics were off", () => {
    const { recorder } = makeRecorder();
    recorder.setMode("off");
    const token = recorder.noteImageStart("thumb");
    expect(token).toBeNull();
    recorder.setMode("summary");
    recorder.startSession({ viewportWidth: 1, viewportHeight: 1 });
    recorder.setServerEnabled(true);
    expect(recorder.noteImageEnd("thumb", "ok", 10, {}, token)).toBe(false);
    recorder.noteImageStart("thumb");
    recorder.flushImageSummary();
    const summary = recorder.exportSnapshot().events.filter((event) => event.event === "images.summary").at(-1);
    expect(summary.data).toMatchObject({ thumb_requests: 1, thumb_ok: 0, inflight: 1 });
  });

  it("does not settle an off request after switching to local-only", () => {
    const { recorder } = makeRecorder();
    recorder.setServerEnabled(false);
    const token = recorder.noteImageStart("thumb");
    expect(token).toBeNull();
    recorder.setLocalOnly(true);
    recorder.startSession({ viewportWidth: 1, viewportHeight: 1 });
    expect(recorder.noteImageEnd("thumb", "ok", 10, {}, token)).toBe(false);
    recorder.noteImageStart("thumb");
    recorder.flushImageSummary();
    const summary = recorder.exportSnapshot().events.filter((event) => event.event === "images.summary").at(-1);
    expect(summary.data).toMatchObject({ thumb_requests: 1, thumb_ok: 0, inflight: 1 });
  });

  it("settles a network completion at most once", () => {
    const { recorder } = makeRecorder();
    recorder.setServerEnabled(true);
    const token = recorder.noteImageStart("thumb");
    expect(recorder.noteImageEnd("thumb", "ok", 10, {}, token)).toBe(true);
    expect(recorder.noteImageEnd("thumb", "ok", 10, {}, token)).toBe(false);
    recorder.flushImageSummary();
    const summary = recorder.exportSnapshot().events.filter((event) => event.event === "images.summary").at(-1);
    expect(summary.data).toMatchObject({ thumb_requests: 1, thumb_ok: 1, inflight: 0 });
  });

  it("uses the request kind captured at start, not the completion mode", () => {
    const { recorder } = makeRecorder();
    recorder.setServerEnabled(true);
    const token = recorder.noteImageStart("original");
    expect(recorder.noteImageEnd("thumb", "ok", 10, {}, token)).toBe(true);
    recorder.flushImageSummary();
    const summary = recorder.exportSnapshot().events.filter((event) => event.event === "images.summary").at(-1);
    expect(summary.data).toMatchObject({
      thumb_requests: 0, original_requests: 1, original_ok: 1, inflight: 0,
    });
  });

  it("counts normal failure and cancellation outcomes", () => {
    const { recorder } = makeRecorder();
    recorder.setServerEnabled(true);
    const failed = recorder.noteImageStart("thumb");
    expect(recorder.noteImageEnd("thumb", "error", 10, { status: 500 }, failed)).toBe(true);
    const cancelled = recorder.noteImageStart("original");
    expect(recorder.noteImageEnd("original", "cancelled", 5, {}, cancelled)).toBe(true);
    recorder.flushImageSummary();
    const summary = recorder.exportSnapshot().events.filter((event) => event.event === "images.summary").at(-1);
    expect(summary.data).toMatchObject({
      thumb_requests: 1, thumb_failed: 1,
      original_requests: 1, original_cancelled: 1, inflight: 0,
    });
  });

  it("accepts one decode failure after the network token settles", () => {
    const { recorder } = makeRecorder();
    recorder.setServerEnabled(true);
    const token = recorder.noteImageStart("thumb");
    expect(recorder.noteImageEnd("thumb", "ok", 10, {}, token)).toBe(true);
    expect(recorder.noteImageDecodeFailure("thumb", "/a.jpg", token)).toBe(true);
    expect(recorder.noteImageDecodeFailure("thumb", "/a.jpg", token)).toBe(false);
    const failures = recorder.exportSnapshot().events.filter((event) => event.event === "images.failure");
    expect(failures).toHaveLength(1);
    expect(failures[0].data).toMatchObject({ request_kind: "thumb", phase: "decode", category: "decode" });
    recorder.flushImageSummary();
    const summary = recorder.exportSnapshot().events.filter((event) => event.event === "images.summary").at(-1);
    expect(summary.data).toMatchObject({ thumb_requests: 1, thumb_ok: 1, inflight: 0 });
  });

  it("rejects decode failures without a current token", () => {
    const { recorder } = makeRecorder();
    recorder.setServerEnabled(true);
    expect(recorder.noteImageDecodeFailure("thumb", "/a.jpg", null)).toBe(false);
    const old = recorder.noteImageStart("thumb");
    recorder.resetIdentity("user-b");
    recorder.startSession({ viewportWidth: 1, viewportHeight: 1 });
    recorder.setServerEnabled(true);
    expect(recorder.noteImageDecodeFailure("thumb", "/a.jpg", old)).toBe(false);
    expect(recorder.exportSnapshot().events.some((event) => event.event === "images.failure")).toBe(false);
  });

  it("flushes a throttled selection tail", () => {
    const { recorder } = makeRecorder();
    const first = recorder.selectionChanged({ operation: "add", before: 0, after: 1, known_items: 3, multiple: true, merged_count: 1 });
    expect(first).not.toBeNull();
    expect(recorder.selectionChanged({ operation: "add", before: 1, after: 2, known_items: 3, multiple: true, merged_count: 1 })).toBeNull();
    const tail = recorder.flushSelection();
    expect(tail.data).toMatchObject({ operation: "add", before: 1, after: 2, merged_count: 2 });
  });

  it("drops the old queue when the account changes", () => {
    const { recorder } = makeRecorder();
    recorder.setServerEnabled(true);
    const ctx = start(recorder);
    recorder.record("listing.response", response(), ctx);
    const oldSession = recorder.getClientSession();
    expect(recorder.resetIdentity("user-b")).toBe(true);
    expect(recorder.getStatus().queue_events).toBe(0);
    expect(recorder.getHistoryCount()).toBe(0);
    expect(recorder.isStarted()).toBe(false);
    expect(recorder.getClientSession()).toBeNull();
    recorder.startSession({ viewportWidth: 1, viewportHeight: 1 });
    expect(recorder.getClientSession()).not.toBe(oldSession);
  });

  it("returns an empty sanitized export after switching off", () => {
    const { recorder } = makeRecorder();
    const ctx = start(recorder);
    recorder.record("listing.response", response(), ctx);
    recorder.setMode("off");
    const snapshot = recorder.exportSnapshot();
    expect(snapshot.events).toEqual([]);
    expect(snapshot.mode).toBe("off");
    expect(JSON.stringify(snapshot)).not.toContain("/A");
  });

  it("expires detailed mode after ten minutes", () => {
    const { recorder, advance } = makeRecorder();
    recorder.setMode("detailed");
    recorder.startSession({ viewportWidth: 1, viewportHeight: 1 });
    const ctx = start(recorder);
    advance(LIMITS.DETAILED_MAX_MS + 1);
    expect(recorder.isDetailedActive()).toBe(false);
    expect(recorder.getMode()).toBe("summary");
    recorder.record("listing.mount_batch", { generation: 1, urgent: false, fast: false, size: 1, enqueue_ms: 0, flush_ms: null }, ctx);
    expect(recorder.getQueueEvents().some((event) => event.level === "debug")).toBe(false);
  });

  it("exports no client path in any event", () => {
    const { recorder } = makeRecorder();
    const ctx = start(recorder, "/private/album");
    recorder.record("listing.response", response(), ctx);
    recorder.markIssue("slow_load", ctx);
    expect(JSON.stringify(recorder.exportSnapshot())).not.toContain("private");
    expect(JSON.stringify(recorder.getQueueEvents())).not.toContain("private");
  });
});