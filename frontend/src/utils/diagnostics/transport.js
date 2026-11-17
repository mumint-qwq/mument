// Single-inflight diagnostics transport. It never records its own failures and
// never touches business request concurrency.
import { LIMITS, SCHEMA_VERSION } from "./config.js";
import { utf8Bytes } from "./schema.js";

const STATE = Object.freeze({
  IDLE: "idle",
  PROBING: "probing",
  READY: "ready",
  SENDING: "sending",
  RETRY_WAIT: "retry_wait",
  OFFLINE: "offline",
  PAUSED: "paused",
  UNSUPPORTED: "unsupported",
  DISABLED: "disabled",
  STORAGE_ERROR: "storage_error",
});

function defaultRandomHex32() {
  const bytes = new Uint8Array(16);
  const cryptoApi = globalThis.crypto;
  if (cryptoApi?.getRandomValues) cryptoApi.getRandomValues(bytes);
  else for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

/**
 * @param {object} options
 * @param {object} options.recorder
 * @param {{getStatus:Function,postBatch:Function}} options.api
 * @param {object} [options.clientBuild]
 * @param {() => number} [options.now]
 * @param {() => string} [options.randomHex32]
 * @param {Function} [options.setTimeout]
 * @param {Function} [options.clearTimeout]
 */
export function createDiagnosticsTransport(options = {}) {
  const recorder = options.recorder;
  const api = options.api;
  const clientBuild = options.clientBuild || { version: null, commit: null };
  const now = typeof options.now === "function" ? options.now : () => Date.now();
  const randomHex32 = typeof options.randomHex32 === "function" ? options.randomHex32 : defaultRandomHex32;
  const setTimer = typeof options.setTimeout === "function" ? options.setTimeout : (fn, ms) => setTimeout(fn, ms);
  const clearTimerFn = typeof options.clearTimeout === "function" ? options.clearTimeout : (id) => clearTimeout(id);
  const onState = typeof options.onState === "function" ? options.onState : null;

  let state = STATE.IDLE;
  let running = false;
  let inflight = false;
  let paused = false;
  let timer = null;
  let pendingBatch = null;
  let controller = null;
  let attempts = 0;
  let probeAttempt = 0;
  let lastSuccessAt = 0;
  let lastStatusCode = 0;
  let lastError = null;
  // Every start/stop invalidates this generation. A completion, probe or
  // pagehide response from an older generation may not touch the current
  // controller, timers, queue acknowledgements or state.
  let generation = 0;
  let probeController = null;
  let pagehideController = null;

  function publish() {
    if (!onState) return;
    try {
      onState(getStatus());
    } catch {
      // UI callbacks must not affect sending.
    }
  }

  function setState(next, error = null, epoch = null) {
    if (epoch !== null && !isCurrent(epoch)) return;
    state = next;
    if (error !== undefined) lastError = error;
    publish();
  }

  function clearTimer() {
    if (timer !== null) {
      clearTimerFn(timer);
      timer = null;
    }
  }

  function isCurrent(epoch) {
    return running && epoch === generation;
  }

  function schedule(delayMs, epoch = generation) {
    clearTimer();
    const id = setTimer(() => {
      if (timer === id) timer = null;
      if (!isCurrent(epoch)) return;
      void tick();
    }, Math.max(0, delayMs));
    timer = id;
  }

  function jitter() {
    return Math.floor(Math.random() * 500);
  }

  function start() {
    if (!recorder || running) return;
    const status = recorder.getStatus();
    if (status.local_only) {
      running = false;
      setState(STATE.DISABLED);
      return;
    }
    generation += 1;
    running = true;
    paused = false;
    if (status.server_enabled === true) {
      setState(STATE.READY);
      schedule(0);
      return;
    }
    void probe();
  }

  function stop() {
    generation += 1;
    running = false;
    clearTimer();
    for (const active of [controller, probeController, pagehideController]) {
      if (active) {
        try {
          active.abort();
        } catch {
          // already aborted
        }
      }
    }
    controller = null;
    probeController = null;
    pagehideController = null;
    inflight = false;
    pendingBatch = null;
    setState(STATE.IDLE);
  }

  async function probe() {
    if (!running || probeController) return;
    const epoch = generation;
    const ownController = new AbortController();
    probeController = ownController;
    setState(STATE.PROBING, undefined, epoch);
    let result;
    try {
      result = await api.getStatus({ signal: ownController.signal, timeoutMs: LIMITS.UPLOAD_TIMEOUT_MS });
    } catch {
      result = { status: 0, body: null };
    }
    if (probeController === ownController) probeController = null;
    if (!isCurrent(epoch)) return;
    lastStatusCode = result?.status || 0;
    if (result?.status === 200 && result.body) {
      probeAttempt = 0;
      if (result.body.enabled === true) {
        recorder.setServerEnabled(true);
        setState(STATE.READY, undefined, epoch);
        schedule(0, epoch);
        return;
      }
      recorder.setServerEnabled(false);
      setState(STATE.DISABLED, undefined, epoch);
      running = false;
      return;
    }
    if (result?.status === 404 || result?.status === 405) {
      recorder.setServerEnabled(false);
      setState(STATE.UNSUPPORTED, undefined, epoch);
      running = false;
      return;
    }
    if (result?.status === 401 || result?.status === 403) {
      if (result?.body?.code === "unsupported_auth") {
        recorder.setServerEnabled(false);
        setState(STATE.UNSUPPORTED, undefined, epoch);
        running = false;
        return;
      }
      paused = true;
      setState(STATE.PAUSED, undefined, epoch);
      schedule(60000 + jitter(), epoch);
      return;
    }
    paused = false;
    probeAttempt += 1;
    const delay = Math.min(60000, 5000 * 2 ** Math.min(probeAttempt - 1, 4)) + jitter();
    setState(STATE.OFFLINE, undefined, epoch);
    schedule(delay, epoch);
  }

  function wake() {
    if (!running) return;
    if (!recorder.getStatus().server_enabled) return;
    if (inflight) return;
    if (recorder.getQueueEvents().length >= LIMITS.SEND_EARLY_EVENTS || queueBytes() >= LIMITS.SEND_EARLY_BYTES) schedule(0);
  }

  function queueBytes() {
    return recorder.getQueueEvents().reduce((sum, event) => sum + utf8Bytes(JSON.stringify(event)), 0);
  }

  function buildBatch() {
    const available = recorder.getQueueEvents();
    if (!available.length) return null;
    const selected = [];
    let bytes = 0;
    for (const event of available) {
      const eventBytes = utf8Bytes(JSON.stringify(event)) + 1;
      if (selected.length >= LIMITS.MAX_EVENTS_PER_BATCH) break;
      if (selected.length > 0 && bytes + eventBytes > LIMITS.MAX_BATCH_BYTES) break;
      selected.push(event);
      bytes += eventBytes;
    }
    if (!selected.length) return null;
    const session = recorder.getClientSession();
    if (!session) return null;
    return {
      payload: {
        schema_version: SCHEMA_VERSION,
        client_session: session,
        batch_ref: randomHex32(),
        client_build: {
          version: clientBuild.version ?? null,
          commit: clientBuild.commit ?? null,
        },
        events: selected,
      },
      seqs: selected.map((event) => event.seq),
      bytes,
      createdAt: now(),
      attempt: 0,
    };
  }

  async function tick() {
    if (!running) return;
    if (inflight) return;
    if (paused) {
      void probe();
      return;
    }
    if (!recorder.getStatus().server_enabled) {
      if (state !== STATE.UNSUPPORTED && state !== STATE.DISABLED) void probe();
      return;
    }
    if (pendingBatch) {
      const retry = pendingBatch;
      pendingBatch = null;
      await send(retry);
      return;
    }
    const batch = buildBatch();
    if (!batch) {
      // No empty requests while idle.
      schedule(LIMITS.SEND_INTERVAL_MS);
      return;
    }
    await send(batch);
  }

  async function send(batch, epoch = generation) {
    if (!isCurrent(epoch) || inflight) return;
    const ownController = new AbortController();
    inflight = true;
    attempts = batch.attempt + 1;
    controller = ownController;
    setState(attempts > 1 ? STATE.RETRY_WAIT : STATE.SENDING, undefined, epoch);
    let result;
    try {
      result = await api.postBatch(batch.payload, { signal: ownController.signal, timeoutMs: LIMITS.UPLOAD_TIMEOUT_MS });
    } catch (error) {
      result = { status: 0, errorName: error?.name || "network" };
    }
    // A stale completion must not clear a newer request's controller/inflight.
    if (controller === ownController) {
      controller = null;
      inflight = false;
    }
    if (!isCurrent(epoch)) {
      // Stop/disable/account switch may have cancelled the request; do not
      // retry, acknowledge a different session or publish status.
      return;
    }
    if (ownController.signal.aborted && result?.status !== 202) return;
    lastStatusCode = result?.status || 0;
    handleResult(batch, result, epoch);
  }

  function sameSession(batch) {
    return recorder.getClientSession() === batch.payload.client_session;
  }

  function handleResult(batch, result, epoch) {
    if (!isCurrent(epoch)) return;
    const status = result?.status || 0;
    if (status === 202) {
      // Sequence numbers restart with a new session; never acknowledge by seq
      // alone across a stop/reopen or account change.
      if (sameSession(batch)) recorder.ackEvents(batch.seqs);
      attempts = 0;
      lastSuccessAt = now();
      lastError = null;
      setState(STATE.READY, undefined, epoch);
      if (recorder.getQueueEvents().length >= LIMITS.SEND_EARLY_EVENTS) schedule(0, epoch);
      else schedule(LIMITS.SEND_INTERVAL_MS, epoch);
      return;
    }
    if (status === 400 || status === 422) {
      if (sameSession(batch)) recorder.dropEvents(batch.seqs);
      attempts = 0;
      setState(STATE.STORAGE_ERROR, "rejected", epoch);
      schedule(LIMITS.SEND_INTERVAL_MS, epoch);
      return;
    }
    if (status === 401 || status === 403) {
      if (result?.body?.code === "unsupported_auth") {
        recorder.setServerEnabled(false);
        setState(STATE.UNSUPPORTED, undefined, epoch);
        running = false;
        return;
      }
      paused = true;
      setState(STATE.PAUSED, undefined, epoch);
      schedule(60000 + jitter(), epoch);
      return;
    }
    if (status === 404 || status === 405) {
      recorder.setServerEnabled(false);
      setState(STATE.UNSUPPORTED, undefined, epoch);
      running = false;
      return;
    }
    if (status === 413) {
      handleTooLarge(batch, epoch);
      return;
    }
    if (status === 429) {
      batch.attempt += 1;
      if (batch.attempt >= LIMITS.MAX_ATTEMPTS || now() - batch.createdAt > LIMITS.MAX_BATCH_AGE_MS) {
        if (sameSession(batch)) recorder.dropEvents(batch.seqs);
        setState(STATE.STORAGE_ERROR, "rate_limit_exhausted", epoch);
        schedule(LIMITS.SEND_INTERVAL_MS, epoch);
        return;
      }
      const retryAfter = clampRetryAfter(result?.retryAfter);
      pendingBatch = batch;
      setState(STATE.RETRY_WAIT, undefined, epoch);
      schedule(retryAfter * 1000 + jitter(), epoch);
      return;
    }
    if (status === 503) {
      setState(STATE.STORAGE_ERROR, "storage_unavailable", epoch);
      retryOrDrop(batch, epoch);
      return;
    }
    if (status === 502 || status === 504 || status === 0) {
      setState(STATE.OFFLINE, undefined, epoch);
      retryOrDrop(batch, epoch);
      return;
    }
    // 500 and other unexpected statuses: bounded retry, never infinite.
    setState(STATE.STORAGE_ERROR, "unexpected_status", epoch);
    retryOrDrop(batch, epoch);
  }

  function handleTooLarge(batch, epoch) {
    // One oversized event must not block the rest of the queue.
    let largest = -1;
    let largestBytes = 0;
    batch.payload.events.forEach((event, index) => {
      const bytes = utf8Bytes(JSON.stringify(event));
      if (bytes > largestBytes) {
        largestBytes = bytes;
        largest = index;
      }
    });
    if (largest >= 0 && sameSession(batch)) recorder.dropEvents([batch.payload.events[largest].seq]);
    const remaining = batch.payload.events.filter((_, index) => index !== largest);
    if (!remaining.length) {
      setState(STATE.STORAGE_ERROR, "batch_too_large", epoch);
      schedule(LIMITS.SEND_INTERVAL_MS, epoch);
      return;
    }
    const next = {
      payload: {
        ...batch.payload,
        batch_ref: randomHex32(),
        events: remaining,
      },
      seqs: remaining.map((event) => event.seq),
      bytes: batch.bytes,
      createdAt: batch.createdAt,
      attempt: 0,
    };
    void send(next, epoch);
  }

  function retryOrDrop(batch, epoch) {
    const failedAttempt = batch.attempt + 1;
    const age = now() - batch.createdAt;
    if (!isCurrent(epoch)) return;
    if (failedAttempt >= LIMITS.MAX_ATTEMPTS || age > LIMITS.MAX_BATCH_AGE_MS) {
      if (sameSession(batch)) recorder.dropEvents(batch.seqs);
      attempts = 0;
      setState(STATE.STORAGE_ERROR, "retry_exhausted", epoch);
      schedule(LIMITS.SEND_INTERVAL_MS, epoch);
      return;
    }
    batch.attempt = failedAttempt;
    pendingBatch = batch;
    const delay = (LIMITS.RETRY_BACKOFF_MS[failedAttempt - 1] || 40000) + jitter();
    setState(STATE.RETRY_WAIT, undefined, epoch);
    schedule(delay, epoch);
  }

  function flushBeforeUnload() {
    if (!running || inflight) return;
    const epoch = generation;
    const available = recorder.getQueueEvents();
    if (!available.length) return;
    const batch = buildBatch();
    if (!batch || batch.bytes > LIMITS.PAGEHIDE_MAX_BYTES) return;
    const ownController = new AbortController();
    pagehideController = ownController;
    inflight = true;
    try {
      const promise = api.postBatch(batch.payload, { keepalive: true, signal: ownController.signal, timeoutMs: LIMITS.UPLOAD_TIMEOUT_MS });
      if (promise?.then) {
        promise.then((result) => {
          if (!isCurrent(epoch)) return;
          if (result?.status === 202 && sameSession(batch)) recorder.ackEvents(batch.seqs);
        }).catch(() => {}).finally(() => {
          if (pagehideController === ownController) {
            pagehideController = null;
            inflight = false;
          }
        });
      }
    } catch {
      // A closing page gets one best-effort attempt only.
      if (pagehideController === ownController) {
        pagehideController = null;
        inflight = false;
      }
    }
  }

  function getStatus() {
    return {
      state,
      running,
      paused,
      inflight,
      attempts,
      last_status: lastStatusCode,
      last_success_at: lastSuccessAt,
      last_error: lastError,
      queue_events: recorder.getQueueEvents().length,
      queue_bytes: queueBytes(),
    };
  }

  return {
    start,
    stop,
    probe,
    wake,
    tick,
    flushBeforeUnload,
    getStatus,
  };
}

function clampRetryAfter(value) {
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) return 5;
  return Math.min(60, Math.max(1, Math.ceil(parsed)));
}

export { STATE as TRANSPORT_STATE };