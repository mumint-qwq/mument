/* eslint-disable security/detect-non-literal-fs-filename -- fixed repo fixture paths */
// @vitest-environment node
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { EVENT_NAMES, fingerprint, sanitizeEvent, sanitizeRuntimeError, sanitizeStack, utf8Bytes, validateEnvelope } from "./schema.js";

const fixturePath = fileURLToPath(new URL("../../../../diagnostics/fixtures/cases.json", import.meta.url));
const schemaPath = fileURLToPath(new URL("../../../../diagnostics/schema-v1.json", import.meta.url));
const fixtures = JSON.parse(readFileSync(fixturePath, "utf8"));
const canonical = JSON.parse(readFileSync(schemaPath, "utf8"));

const session = fixtures.defaults.client_session;
const batchRef = fixtures.defaults.batch_ref;
const build = fixtures.defaults.client_build;

function envelopeFor(entry) {
  const envelope = {
    schema_version: 1,
    client_session: session,
    batch_ref: batchRef,
    client_build: build,
    events: entry.events || [entry.event],
  };
  return { ...envelope, ...(entry.envelope || {}) };
}

describe("protocol fixtures", () => {
  it("matches every shared client/server case", () => {
    expect(fixtures.cases.length).toBeGreaterThan(50);
    for (const entry of fixtures.cases) {
      const result = validateEnvelope(envelopeFor(entry));
      expect(result.ok, `${entry.name  } -> ${  result.reason || "ok"}`).toBe(entry.expect_client);
    }
  });

  it("keeps the canonical event list in sync", () => {
    expect(EVENT_NAMES).toEqual(Object.keys(canonical.events));
  });

  it("rebuilds events without unknown fields", () => {
    const event = fixtures.cases.find((entry) => entry.name === "valid_listing_response").event;
    const result = sanitizeEvent({ ...event, actor_ref: "forged" });
    expect(result.ok).toBe(false);
    const clean = sanitizeEvent(event);
    expect(clean.ok).toBe(true);
    expect(Object.keys(clean.value).sort()).toEqual(["data", "event", "incident_ref", "level", "load_ref", "operation_ref", "request_ref", "seq", "t_ms"]);
  });

  it("accepts optional extensions and keeps legacy events valid", () => {
    const operation = fixtures.cases.find((entry) => entry.name === "valid_operation_result_move_outcome_ok").event;
    expect(sanitizeEvent(operation).ok).toBe(true);
    const legacy = { ...operation, data: { ...operation.data } };
    delete legacy.data.outcome;
    const legacyResult = sanitizeEvent(legacy);
    expect(legacyResult.ok).toBe(true);
    expect(legacyResult.value.data).not.toHaveProperty("outcome");
    expect(sanitizeEvent({ ...operation, data: { ...operation.data, outcome: "maybe" } }).ok).toBe(false);

    const restore = fixtures.cases.find((entry) => entry.name === "valid_navigation_restore_move").event;
    expect(sanitizeEvent(restore).ok).toBe(true);
    const legacyRestore = { ...restore, data: { ...restore.data } };
    delete legacyRestore.data.strategy;
    expect(sanitizeEvent(legacyRestore).ok).toBe(true);
    expect(sanitizeEvent({ ...restore, data: { ...restore.data, reason: "sidebar" } }).ok).toBe(false);
    expect(sanitizeEvent({ ...restore, data: { ...restore.data, strategy: "guess" } }).ok).toBe(false);

    const cancelled = fixtures.cases.find((entry) => entry.name === "valid_navigation_restore_cancelled_superseded").event;
    expect(sanitizeEvent(cancelled).ok).toBe(true);
    const legacyCancelled = { ...cancelled, data: { ...cancelled.data } };
    delete legacyCancelled.data.reason_code;
    expect(sanitizeEvent(legacyCancelled).ok).toBe(true);
    expect(sanitizeEvent({ ...cancelled, data: { ...cancelled.data, reason_code: "made_up" } }).ok).toBe(false);
  });

  it("does not coerce numeric strings or round floats", () => {
    const base = fixtures.cases.find((entry) => entry.name === "valid_listing_response").event;
    expect(sanitizeEvent({ ...base, data: { ...base.data, status: "200" } }).ok).toBe(false);
    expect(sanitizeEvent({ ...base, data: { ...base.data, known_items: 2.5 } }).ok).toBe(false);
  });
});

describe("sanitizers", () => {
  it("counts UTF-8 bytes, not UTF-16 length", () => {
    expect(utf8Bytes("abc")).toBe(3);
    expect(utf8Bytes("图片")).toBe(6);
    expect(utf8Bytes("😀")).toBe(4);
  });

  it("fingerprints deterministically", () => {
    const a = fingerprint(["vue", "TypeError", "index.js"]);
    const b = fingerprint(["vue", "TypeError", "index.js"]);
    expect(a).toBe(b);
    expect(a).toMatch(/^[0-9a-f]{16}$/);
    expect(a).not.toBe(fingerprint(["vue", "TypeError", "other.js"]));
  });

  it("keeps only asset js frames and never the raw stack", () => {
    const stack = [
      "TypeError: secret message /srv/pictures/a.jpg",
      "    at handler (https://host/static/assets/index-abc123.js:12:34)",
      "    at fn (/srv/pictures/a.jpg:1:2)",
      "    at again (https://host/static/assets/index-abc123.js:12:34?token=abc)",
    ].join("\n");
    const frames = sanitizeStack(stack);
    expect(frames).toEqual([{ file: "index-abc123.js", line: 12, col: 34 }]);
    expect(JSON.stringify(frames)).not.toContain("a.jpg");
    expect(JSON.stringify(frames)).not.toContain("secret");
    expect(JSON.stringify(frames)).not.toContain("https://");
  });

  it("never copies Error.message or cause into runtime.error", () => {
    const error = new TypeError("path /srv/pictures/private.jpg token=abc");
    error.code = "ERR_PRIVATE";
    error.cause = new Error("cause text");
    error.stack = "TypeError: x\n    at f (https://host/assets/index-x.js:1:2)";
    const data = sanitizeRuntimeError("vue", error);
    expect(data).toEqual({
      category: "vue",
      code: "ERR_PRIVATE",
      fingerprint: expect.stringMatching(/^[0-9a-f]{16}$/),
      frames: [{ file: "index-x.js", line: 1, col: 2 }],
      repeat_count: 1,
    });
    expect(JSON.stringify(data)).not.toContain("private");
    expect(JSON.stringify(data)).not.toContain("cause");
  });
});