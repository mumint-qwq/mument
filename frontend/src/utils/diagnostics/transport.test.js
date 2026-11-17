import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createDiagnosticsTransport, TRANSPORT_STATE } from "./transport.js";
import { LIMITS } from "./config.js";

function makeRecorder(count = 3) {
  let seq = 0;
  let events = Array.from({ length: count }, () => ({
    seq: ++seq,
    t_ms: seq,
    level: "info",
    event: "listing.response",
    load_ref: "l1",
    operation_ref: null,
    request_ref: null,
    incident_ref: null,
    data: {
      api: "resources", status: 200, headers_wait_ms: 1, body_decode_ms: 2,
      known_items: 10, paged: false, outcome: "ok",
    },
  }));
  const state = { enabled: null, acked: [], dropped: [], starts: 0, session: "0123456789abcdef0123456789abcdef" };
  return {
    state,
    get serverEnabled() { return state.enabled; },
    setServerEnabled(value) { state.enabled = value; },
    getClientSession() { return state.session; },
    setSession(session) { state.session = session; },
    getQueueEvents() { return events.map((event) => ({ ...event })); },
    getStatus() { return { server_enabled: state.enabled }; },
    ackEvents(seqs) { state.acked.push(...seqs); events = events.filter((event) => !seqs.includes(event.seq)); },
    dropEvents(seqs) { state.dropped.push(...seqs); events = events.filter((event) => !seqs.includes(event.seq)); },
    addEvent(seq) {
      events.push({ ...events[0], seq });
    },
  };
}

function makeApi(responses) {
  const calls = [];
  let index = 0;
  return {
    calls,
    async getStatus() { return responses.status || { status: 200, body: { enabled: true } }; },
    async postBatch(payload, options) {
      calls.push({ payload, options });
      const next = responses.post?.[index++] ?? { status: 202, body: { queued: true } };
      if (typeof next === "function") return next(payload, options);
      return next;
    },
  };
}

async function startTransport(recorder, api, overrides = {}) {
  const transport = createDiagnosticsTransport({
    recorder,
    api,
    randomHex32: () => "fedcba9876543210fedcba9876543210",
    ...overrides,
  });
  transport.start();
  await vi.advanceTimersByTimeAsync(0);
  return transport;
}

describe("diagnostics transport", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("probes once, sends one batch and acknowledges it", async () => {
    const recorder = makeRecorder(3);
    const api = makeApi({});
    const transport = await startTransport(recorder, api);
    expect(recorder.state.enabled).toBe(true);
    expect(transport.getStatus().state).toBe(TRANSPORT_STATE.READY);
    await vi.advanceTimersByTimeAsync(0);
    expect(api.calls).toHaveLength(1);
    expect(api.calls[0].payload.events).toHaveLength(3);
    expect(recorder.state.acked).toEqual([1, 2, 3]);
    await vi.advanceTimersByTimeAsync(LIMITS.SEND_INTERVAL_MS + 100);
    expect(api.calls).toHaveLength(1); // no empty request while idle
  });

  it("stops automatic sending for an unsupported server", async () => {
    const recorder = makeRecorder(3);
    const api = makeApi({ status: { status: 404, body: null } });
    const transport = await startTransport(recorder, api);
    expect(recorder.state.enabled).toBe(false);
    expect(transport.getStatus().state).toBe(TRANSPORT_STATE.UNSUPPORTED);
    await vi.advanceTimersByTimeAsync(60000);
    expect(api.calls).toHaveLength(0);
  });

  it("pauses on 401 without issuing a login storm", async () => {
    const recorder = makeRecorder(1);
    const api = makeApi({ post: [{ status: 401, body: null }] });
    const transport = await startTransport(recorder, api);
    await vi.advanceTimersByTimeAsync(0);
    expect(transport.getStatus().state).toBe(TRANSPORT_STATE.PAUSED);
    expect(api.calls).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(30000);
    expect(api.calls).toHaveLength(1);
  });

  it("honours Retry-After for 429 and then succeeds", async () => {
    const recorder = makeRecorder(1);
    const api = makeApi({ post: [{ status: 429, retryAfter: 2 }, { status: 202, body: {} }] });
    await startTransport(recorder, api);
    await vi.advanceTimersByTimeAsync(0);
    expect(api.calls).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1000);
    expect(api.calls).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1500);
    expect(api.calls).toHaveLength(2);
    expect(recorder.state.acked).toEqual([1]);
  });

  it("drops an oversized event and retries the rest with a new batch ref", async () => {
    const recorder = makeRecorder(2);
    const api = makeApi({ post: [{ status: 413, body: null }, { status: 202, body: {} }] });
    await startTransport(recorder, api);
    await vi.advanceTimersByTimeAsync(0);
    expect(recorder.state.dropped).toHaveLength(1);
    expect(api.calls).toHaveLength(2);
    expect(api.calls[0].payload.events).toHaveLength(2);
    expect(api.calls[1].payload.events).toHaveLength(1);
    expect(recorder.state.acked).toEqual([2]);
  });

  it("drops a rejected 422 batch instead of retrying forever", async () => {
    const recorder = makeRecorder(1);
    const api = makeApi({ post: [{ status: 422, body: null }] });
    await startTransport(recorder, api);
    await vi.advanceTimersByTimeAsync(0);
    expect(recorder.state.dropped).toEqual([1]);
    await vi.advanceTimersByTimeAsync(120000);
    expect(api.calls).toHaveLength(1);
  });

  it("retries network failures with bounded backoff then drops", async () => {
    const recorder = makeRecorder(1);
    const api = makeApi({ post: Array.from({ length: 6 }, () => ({ status: 0, errorName: "TypeError" })) });
    await startTransport(recorder, api);
    for (let i = 0; i < 5; i++) {
      await vi.advanceTimersByTimeAsync(45000);
    }
    expect(api.calls.length).toBeLessThanOrEqual(5);
    expect(recorder.state.dropped).toEqual([1]);
  });

  it("aborts an in-flight request when stopped", async () => {
    const recorder = makeRecorder(1);
    let observedSignal = null;
    const api = makeApi({ post: [(payload, options) => {
      observedSignal = options.signal;
      return new Promise(() => {});
    }] });
    const transport = await startTransport(recorder, api);
    await vi.advanceTimersByTimeAsync(0);
    expect(observedSignal).not.toBeNull();
    transport.stop();
    expect(observedSignal.aborted).toBe(true);
  });


  it("does not let a late A acknowledgement delete B events", async () => {
    const recorder = makeRecorder(1);
    recorder.setServerEnabled(true);
    let resolveOld;
    let calls = 0;
    const api = makeApi({});
    api.postBatch = (_payload) => {
      calls += 1;
      if (calls === 1) return new Promise((resolve) => { resolveOld = resolve; });
      return new Promise(() => {});
    };
    const transport = await startTransport(recorder, api);
    await vi.advanceTimersByTimeAsync(0);
    expect(calls).toBe(1);
    expect(recorder.getQueueEvents().map((event) => event.seq)).toEqual([1]);

    transport.stop();
    recorder.setSession("f".repeat(32));
    transport.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(calls).toBe(2);
    expect(recorder.getQueueEvents().map((event) => event.seq)).toEqual([1]);

    resolveOld({ status: 202, body: { queued: true } });
    await vi.advanceTimersByTimeAsync(0);
    expect(recorder.getQueueEvents().map((event) => event.seq)).toEqual([1]);
    expect(recorder.state.acked).toEqual([]);
    transport.stop();
  });

  it("ignores a probe that resolves after stop and restart", async () => {
    const recorder = makeRecorder(0);
    let resolveOld;
    let probes = 0;
    const api = makeApi({});
    api.getStatus = () => {
      probes += 1;
      if (probes === 1) return new Promise((resolve) => { resolveOld = resolve; });
      return new Promise(() => {});
    };
    const transport = createDiagnosticsTransport({ recorder, api, randomHex32: () => "fedcba9876543210fedcba9876543210" });
    transport.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(probes).toBe(1);
    transport.stop();
    transport.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(probes).toBe(2);
    resolveOld({ status: 200, body: { enabled: true } });
    await vi.advanceTimersByTimeAsync(0);
    expect(recorder.state.enabled).toBeNull();
    transport.stop();
  });

  it("does not retry a pending batch after stopping during backoff", async () => {
    const recorder = makeRecorder(1);
    const api = makeApi({ post: [{ status: 429, retryAfter: 20 }] });
    const transport = await startTransport(recorder, api);
    await vi.advanceTimersByTimeAsync(0);
    expect(api.calls).toHaveLength(1);
    transport.stop();
    recorder.setSession("f".repeat(32));
    transport.start();
    await vi.advanceTimersByTimeAsync(0);
    const before = api.calls.length;
    await vi.advanceTimersByTimeAsync(60000);
    expect(api.calls).toHaveLength(before);
    transport.stop();
  });

  it("labels batches with the allowed client build fields only", async () => {
    const recorder = makeRecorder(1);
    const api = makeApi({});
    await startTransport(recorder, api, { clientBuild: { version: "diag-rc1", commit: null } });
    await vi.advanceTimersByTimeAsync(0);
    expect(api.calls[0].payload.client_build).toEqual({ version: "diag-rc1", commit: null });
    expect(Object.keys(api.calls[0].payload).sort()).toEqual(["batch_ref", "client_build", "client_session", "events", "schema_version"]);
  });
});