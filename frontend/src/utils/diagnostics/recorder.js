/* eslint-disable security/detect-object-injection -- closed event whitelist */
// Pure client diagnostics recorder: no Vue, store, DOM or network imports.
// All values pass through schema.sanitizeEvent; failures only increment local
// counters and never block the caller.
import { LIMITS, MERGEABLE_EVENTS, SCHEMA_VERSION } from "./config.js";
import { EVENT_FIELDS, sanitizeEvent, sanitizeRuntimeError } from "./schema.js";

const LEVEL_BY_EVENT = Object.freeze({
  "runtime.error": "error",
  "images.failure": "warn",
  "anomaly.detected": "warn",
  "listing.mount_batch": "debug",
});

const DEBUG_PROMOTION_MS = LIMITS.CONTEXT_WINDOW_MS;

function defaultRandomHex32() {
  const bytes = new Uint8Array(16);
  const cryptoApi = globalThis.crypto;
  if (cryptoApi?.getRandomValues) {
    cryptoApi.getRandomValues(bytes);
  } else {
    for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256);
  }
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

function levelFor(eventName, requested) {
  return requested || LEVEL_BY_EVENT[eventName] || "info";
}

function freezeContext(context) {
  return Object.freeze({ ...context });
}

/** Bounded LRU map for directory/item codes. Paths never leave this map. */
class EntryCodeBook {
  constructor(limit) {
    this.limit = limit;
    this.byPath = new Map();
    this.nextDir = 1;
    this.nextItem = 1;
  }

  codeFor(kind, path) {
    const key = `${kind  }:${  path || "/"}`;
    const existing = this.byPath.get(key);
    if (existing) {
      // refresh LRU position
      this.byPath.delete(key);
      this.byPath.set(key, existing);
      return existing;
    }
    if (this.byPath.size >= this.limit) {
      const oldest = this.byPath.keys().next().value;
      if (oldest !== undefined) this.byPath.delete(oldest);
    }
    const code = kind === "d" ? `d${  this.nextDir++}` : `i${  this.nextItem++}`;
    this.byPath.set(key, code);
    return code;
  }

  clear() {
    this.byPath.clear();
    this.nextDir = 1;
    this.nextItem = 1;
  }
}

/**
 * @param {object} options
 * @param {() => number} [options.now] monotonic milliseconds (performance.now)
 * @param {() => string} [options.randomHex32]
 * @param {(info: object) => void} [options.onWake] called when a batch is ready
 * @param {(info: object) => void} [options.onStatus] called on coarse status changes
 * @param {object} [options.clientBuild]
 */
export function createDiagnosticsRecorder(options = {}) {
  const now = typeof options.now === "function" ? options.now : () => globalThis.performance?.now?.() ?? Date.now();
  const randomHex32 = typeof options.randomHex32 === "function" ? options.randomHex32 : defaultRandomHex32;
  const onWake = typeof options.onWake === "function" ? options.onWake : null;
  const onStatus = typeof options.onStatus === "function" ? options.onStatus : null;
  const clientBuild = options.clientBuild || { version: null, commit: null };

  const book = new EntryCodeBook(LIMITS.ENTRY_CODES);
  let generation = 0;
  let sessionId = "";
  let sessionStartedAt = 0;
  let started = false;
  let identity = "";
  let mode = "off";
  let localOnly = false;
  let serverEnabled = null; // null unknown, true enabled, false unsupported/disabled
  let detailedUntil = 0;
  let seq = 0;
  let loadCounter = 0;
  let operationCounter = 0;
  let incidentCounter = 0;
  let history = [];
  let historyBytes = 0;
  let queue = [];
  let queueBytes = 0;
  let startupPending = [];
  let promotedUntil = -Infinity;
  let promotionCount = 0;
  let lastPromotionAt = -Infinity;
  let pendingStartupBytes = 0;
  let activeLoads = new Map();
  let recentIncidents = [];
  const errorDedupe = new Map();
  const promotedSeqs = new Set();
  const dropped = { invalid: 0, tooLarge: 0, history: 0, queue: 0, transport: 0, deduped: 0 };
  let lastRecordError = null;
  let flushedDedupeSignature = "";
  let imageStats = newImageStats();
  let imageDirty = false;
  let lastImageSummaryAt = 0;
  let lastImageEmittedRequests = 0;
  const imageFailureAt = new Map();
  let lastSelectionEmitAt = -Infinity;
  let pendingSelection = null;
  let pendingSelectionCount = 0;
  let selectionTimer = null;
  const setTimer = typeof options.setTimeout === "function" ? options.setTimeout : (fn, ms) => setTimeout(fn, ms);
  const clearTimerFn = typeof options.clearTimeout === "function" ? options.clearTimeout : (id) => clearTimeout(id);

  function statusChange() {
    if (onStatus) {
      try {
        onStatus(getStatus());
      } catch {
        // A UI callback must never break recording.
      }
    }
  }

  function resetSessionState() {
    imageStats = newImageStats();
    imageDirty = false;
    lastImageSummaryAt = 0;
    lastImageEmittedRequests = 0;
    imageFailureAt.clear();
    lastSelectionEmitAt = -Infinity;
    pendingSelection = null;
    pendingSelectionCount = 0;
    if (selectionTimer !== null) {
      clearTimerFn(selectionTimer);
      selectionTimer = null;
    }
    history = [];
    historyBytes = 0;
    queue = [];
    queueBytes = 0;
    startupPending = [];
    pendingStartupBytes = 0;
    activeLoads = new Map();
    errorDedupe.clear();
    promotedSeqs.clear();
    recentIncidents = [];
    seq = 0;
    detailedUntil = 0;
    promotedUntil = -Infinity;
    promotionCount = 0;
    lastPromotionAt = -Infinity;
    book.clear();
  }

  function isCurrentContext(context) {
    if (!context) return true;
    return context.generation === undefined || context.generation === generation;
  }

  function nextSeq() {
    seq += 1;
    return seq;
  }

  function newImageStats() {
    return {
      thumb: { requests: 0, ok: 0, failed: 0, cancelled: 0, sum: 0, max: 0 },
      original: { requests: 0, ok: 0, failed: 0, cancelled: 0, sum: 0, max: 0 },
      inflight: 0,
    };
  }

  function clampCount(value) {
    if (!Number.isFinite(value)) return 0;
    return Math.max(0, Math.min(1000000, Math.round(value)));
  }

  function imageCounters(kind) {
    return kind === "original" ? imageStats.original : imageStats.thumb;
  }

  function noteImageStart(kind) {
    if (!started || mode === "off") return null;
    if (kind !== "thumb" && kind !== "original") return null;
    const counters = imageCounters(kind);
    counters.requests = clampCount(counters.requests + 1);
    imageStats.inflight = clampCount(imageStats.inflight + 1);
    imageDirty = true;
    maybeEmitImageSummary();
    return { generation, kind, settled: false, decodeSettled: false };
  }

  function validImageToken(token) {
    return Boolean(token)
      && token.generation === generation
      && (token.kind === "thumb" || token.kind === "original");
  }

  function noteImageEnd(_kind, outcome, durationMs, details = {}, token = null) {
    if (!started || mode === "off") return false;
    // An unobserved start has no token. Never treat null as a successful
    // completion: the request began before this recorder session existed.
    if (!validImageToken(token) || token.settled) return false;
    token.settled = true;
    // The kind captured at start is authoritative; the completion-time UI mode
    // may have changed while the request was in flight.
    const requestKind = token.kind;
    const counters = imageCounters(requestKind);
    imageStats.inflight = clampCount(Math.max(0, imageStats.inflight - 1));
    if (outcome === "ok") {
      counters.ok = clampCount(counters.ok + 1);
      if (Number.isFinite(durationMs) && durationMs >= 0) {
        counters.sum += durationMs;
        counters.max = Math.max(counters.max, durationMs);
      }
    } else if (outcome === "cancelled" || outcome === "aborted") {
      counters.cancelled = clampCount(counters.cancelled + 1);
    } else {
      counters.failed = clampCount(counters.failed + 1);
      const category = details.category || (details.status ? "http" : "network");
      const at = now();
      const last = imageFailureAt.get(category) || -Infinity;
      if (at - last >= LIMITS.ERROR_DEDUPE_MS) {
        imageFailureAt.set(category, at);
        emit("images.failure", {
          item_ref: details.itemPath ? codeForItem(details.itemPath) : null,
          request_kind: requestKind,
          status: details.status ?? null,
          phase: details.phase || "unknown",
          category,
        });
      }
    }
    imageDirty = true;
    maybeEmitImageSummary();
    return true;
  }

  function noteImageDecodeFailure(_kind, itemPath, token = null) {
    if (!started || mode === "off") return false;
    // Decode may happen after the network token settled; keep the two stages
    // independent but require the same live, observed request token.
    if (!validImageToken(token) || token.decodeSettled) return false;
    token.decodeSettled = true;
    const at = now();
    const last = imageFailureAt.get("decode") || -Infinity;
    if (at - last < LIMITS.ERROR_DEDUPE_MS) return false;
    imageFailureAt.set("decode", at);
    emit("images.failure", {
      item_ref: itemPath ? codeForItem(itemPath) : null,
      request_kind: token.kind,
      status: null,
      phase: "decode",
      category: "decode",
    });
    return true;
  }

  function maybeEmitImageSummary() {
    if (!imageDirty) return;
    const at = now();
    if (at - lastImageSummaryAt < 1000) return;
    emitImageSummary();
  }

  function emitImageSummary() {
    const total = imageStats.thumb.requests + imageStats.original.requests;
    if (total === 0) return;
    emit("images.summary", {
      thumb_requests: imageStats.thumb.requests,
      thumb_ok: imageStats.thumb.ok,
      thumb_failed: imageStats.thumb.failed,
      thumb_cancelled: imageStats.thumb.cancelled,
      original_requests: imageStats.original.requests,
      original_ok: imageStats.original.ok,
      original_failed: imageStats.original.failed,
      original_cancelled: imageStats.original.cancelled,
      inflight: imageStats.inflight,
      elapsed_sum_ms: Math.max(0, imageStats.thumb.sum + imageStats.original.sum),
      elapsed_max_ms: Math.max(0, imageStats.thumb.max, imageStats.original.max),
      cache_known: "none",
    });
    lastImageSummaryAt = now();
    lastImageEmittedRequests = total;
    imageDirty = false;
  }

  function flushImageSummary() {
    if (imageStats.thumb.requests + imageStats.original.requests === 0) return;
    if (!imageDirty && imageStats.thumb.requests + imageStats.original.requests === lastImageEmittedRequests) return;
    emitImageSummary();
  }

  function scheduleSelectionFlush() {
    if (selectionTimer !== null) return;
    const epoch = generation;
    selectionTimer = setTimer(() => {
      selectionTimer = null;
      if (epoch !== generation) return;
      emitPendingSelection();
    }, 250);
  }

  function emitPendingSelection() {
    if (!started || mode === "off" || !pendingSelection) return null;
    const payload = {
      ...pendingSelection,
      merged_count: Math.min(1000000, pendingSelectionCount + 1),
    };
    pendingSelection = null;
    pendingSelectionCount = 0;
    lastSelectionEmitAt = now();
    return emit("selection.change", payload);
  }

  function flushSelection() {
    if (selectionTimer !== null) {
      clearTimerFn(selectionTimer);
      selectionTimer = null;
    }
    return emitPendingSelection();
  }

  function selectionChanged(data) {
    if (!started || mode === "off") return null;
    const at = now();
    if (at - lastSelectionEmitAt < 200) {
      pendingSelection = data;
      pendingSelectionCount += 1;
      scheduleSelectionFlush();
      return null;
    }
    let payload = { ...data };
    if (pendingSelection) {
      payload = { ...payload, before: pendingSelection.before, merged_count: Math.min(1000000, pendingSelectionCount + 1) };
      pendingSelection = null;
      pendingSelectionCount = 0;
    }
    if (selectionTimer !== null) {
      clearTimerFn(selectionTimer);
      selectionTimer = null;
    }
    lastSelectionEmitAt = at;
    return emit("selection.change", payload);
  }

  function enqueue(event, bytes, options = {}) {
    if (!event) return false;
    if (queue.some((item) => item.event.seq === event.seq)) return false;
    queue.push({ event, bytes });
    queueBytes += bytes;
    if (!options.skipTrim) trimQueue();
    if (onWake) {
      try {
        onWake({ queueEvents: queue.length, queueBytes });
      } catch {
        // Transport wake-up is best effort.
      }
    }
    return true;
  }

  function removeQueueAt(index) {
    const [removed] = queue.splice(index, 1);
    if (removed) queueBytes = Math.max(0, queueBytes - removed.bytes);
  }

  function trimQueue() {
    while (queue.length > LIMITS.QUEUE_EVENTS || queueBytes > LIMITS.QUEUE_BYTES) {
      let removed = false;
      for (let i = 0; i < queue.length; i++) {
        if (queue[i].event.level === "debug") {
          removeQueueAt(i);
          dropped.queue += 1;
          removed = true;
          break;
        }
      }
      if (removed) continue;
      // Coalesce repeated info summaries of the same family/load before
      // dropping anything else.
      for (let i = 0; i < queue.length && !removed; i++) {
        const older = queue[i];
        if (older.event.level === "debug") continue;
        if (!MERGEABLE_EVENTS.includes(older.event.event)) continue;
        for (let j = i + 1; j < queue.length; j++) {
          const newer = queue[j];
          if (newer.event.event !== older.event.event) continue;
          if ((newer.event.load_ref || "") !== (older.event.load_ref || "")) continue;
          removeQueueAt(i);
          dropped.deduped += 1;
          removed = true;
          break;
        }
      }
      if (removed) continue;
      removeQueueAt(0);
      dropped.queue += 1;
    }
  }

  function appendHistory(event, bytes) {
    history.push({ event, bytes });
    historyBytes += bytes;
    while (history.length > LIMITS.HISTORY_EVENTS || historyBytes > LIMITS.HISTORY_BYTES) {
      const removed = history.shift();
      if (!removed) break;
      historyBytes = Math.max(0, historyBytes - removed.bytes);
      dropped.history += 1;
    }
  }

  function isDetailedActive() {
    if (mode !== "detailed") return false;
    if (now() >= detailedUntil) {
      mode = "summary";
      detailedUntil = 0;
      statusChange();
      return false;
    }
    return true;
  }

  function shouldQueue(event) {
    if (!started || mode === "off") return false;
    if (localOnly || serverEnabled !== true) return false;
    if (event.level !== "debug") return true;
    if (isDetailedActive()) return true;
    if (promotedSeqs.has(event.seq)) return true;
    return now() <= promotedUntil;
  }

  function emit(eventName, data, context = {}, requestedLevel = null) {
    if (!started || mode === "off" || !isCurrentContext(context)) return null;
    const eventSeq = nextSeq();
    const event = {
      seq: eventSeq,
      t_ms: Math.max(0, now()),
      level: levelFor(eventName, requestedLevel),
      event: eventName,
      load_ref: context.loadRef || null,
      operation_ref: context.operationRef || null,
      request_ref: context.requestRef || null,
      incident_ref: context.incidentRef || null,
      data,
    };
    const clean = sanitizeEvent(event);
    if (!clean.ok) {
      dropped.invalid += 1;
      lastRecordError = clean.reason;
      return null;
    }
    appendHistory(clean.value, clean.bytes);
    if (shouldQueue(clean.value)) enqueue(clean.value, clean.bytes);
    else if (!localOnly && serverEnabled === null && clean.value.level !== "debug" && startupPending.length < 20) {
      const bytes = clean.bytes;
      if (pendingStartupBytes + bytes <= 32 * 1024) {
        startupPending.push({ event: clean.value, bytes });
        pendingStartupBytes += bytes;
      }
    }
    if (clean.value.level === "error") flushErrorDedupe();
    return clean.value;
  }

  function beginLoad(config = {}) {
    if (!started || mode === "off") return null;
    const dirPath = typeof config.dirRefPath === "string" ? config.dirRefPath : config.path;
    const dirRef = book.codeFor("d", dirPath);
    loadCounter += 1;
    const loadRef = `l${  loadCounter}`;
    const context = freezeContext({
      loadRef,
      dirRef,
      dirPath: dirPath || null,
      generation,
      startedAt: now(),
      paged: Boolean(config.paged),
      view: config.view || "unknown",
    });
    activeLoads.set(loadRef, context);
    emit("listing.start", {
      dir_ref: dirRef,
      view: ["mosaic", "gallery", "icons", "list", "compact", "unknown"].includes(config.view) ? config.view : "unknown",
      sort_by: ["name", "size", "modified", "type", "unknown"].includes(config.sortBy) ? config.sortBy : "unknown",
      sort_asc: config.sortAsc !== false,
      density: typeof config.density === "number" && config.density >= 0 && config.density <= 1 ? config.density : null,
      paged: Boolean(config.paged),
      trigger: config.trigger || "route",
    }, context);
    return context;
  }

  function finishLoad(context, kind, extra = {}, level = null) {
    // Match by identity, not just by load ref: a stale completion must never
    // remove a different load that happens to share the ref.
    if (!context || activeLoads.get(context.loadRef) !== context) return null;
    if (context.generation !== generation) return null;
    const startedAt = context.startedAt;
    const data = kind === "complete"
      ? {
          known_items: extra.knownItems || 0,
          mounted_items: extra.mountedItems || 0,
          elapsed_ms: Math.max(0, now() - startedAt),
          paged: Boolean(context.paged),
        }
      : {
          reason: extra.reason || "route",
          elapsed_ms: Math.max(0, now() - startedAt),
          mounted_items: extra.mountedItems || 0,
        };
    activeLoads.delete(context.loadRef);
    return emit(kind === "complete" ? "listing.complete" : "listing.cancel", data, context, level);
  }

  function codeForItem(path) {
    return book.codeFor("i", path);
  }

  function codeForDir(path) {
    return book.codeFor("d", path);
  }

  function nextOperationRef() {
    operationCounter += 1;
    return `o${  operationCounter}`;
  }

  function recordRuntimeError(source, error) {
    if (!started || mode === "off") return null;
    const eventSeq = nextSeq();
    const data = sanitizeRuntimeError(source, error);
    const signature = `${data.fingerprint  }:${  data.code || ""}`;
    const current = now();
    const last = errorDedupe.get(signature);
    if (last && current - last.at < LIMITS.ERROR_DEDUPE_MS) {
      last.count += 1;
      dropped.deduped += 1;
      // Keep a sequence gap; the merged event is only emitted after the window.
      return null;
    }
    if (last && last.count > 1) {
      emitAggregatedError(signature, last, current);
    }
    const event = {
      seq: eventSeq,
      t_ms: current,
      level: "error",
      event: "runtime.error",
      load_ref: null,
      operation_ref: null,
      request_ref: null,
      incident_ref: null,
      data,
    };
    const clean = sanitizeEvent(event);
    if (!clean.ok) {
      dropped.invalid += 1;
      lastRecordError = clean.reason;
      return null;
    }
    appendHistory(clean.value, clean.bytes);
    if (shouldQueue(clean.value)) enqueue(clean.value, clean.bytes);
    errorDedupe.set(signature, { at: current, count: 1, data: { ...data, repeat_count: 1 } });
    return clean.value;
  }

  function emitAggregatedError(signature, entry, current) {
    if (!entry || entry.count <= 1) return;
    const aggregateSeq = nextSeq();
    const data = { ...entry.data, repeat_count: Math.min(1000000, entry.count) };
    const event = {
      seq: aggregateSeq,
      t_ms: current,
      level: "error",
      event: "runtime.error",
      load_ref: null,
      operation_ref: null,
      request_ref: null,
      incident_ref: null,
      data,
    };
    const clean = sanitizeEvent(event);
    if (!clean.ok) {
      dropped.invalid += 1;
      return;
    }
    appendHistory(clean.value, clean.bytes);
    if (shouldQueue(clean.value)) enqueue(clean.value, clean.bytes);
    errorDedupe.set(signature, { at: current, count: 0, data });
  }

  /** Emit pending repeat aggregates once their 10s window has elapsed. */
  function flushErrorDedupe(at = now()) {
    for (const [signature, entry] of errorDedupe.entries()) {
      if (entry.count > 1 && at - entry.at >= LIMITS.ERROR_DEDUPE_MS && signature !== flushedDedupeSignature) {
        emitAggregatedError(signature, entry, at);
        flushedDedupeSignature = signature;
      }
    }
  }

  function promoteRecentDebug() {
    if (localOnly || serverEnabled !== true) return 0;
    const cutoff = now() - LIMITS.CONTEXT_WINDOW_MS;
    let promoted = 0;
    for (let i = history.length - 1; i >= 0 && promoted < LIMITS.CONTEXT_MAX_EVENTS; i--) {
      const entry = history[i];
      if (entry.event.t_ms < cutoff) break;
      if (entry.event.level !== "debug") continue;
      if (promotedSeqs.has(entry.event.seq)) continue;
      promotedSeqs.add(entry.event.seq);
      enqueue(entry.event, entry.bytes, { skipTrim: true });
      promoted += 1;
    }
    trimQueue();
    promotedUntil = now() + DEBUG_PROMOTION_MS;
    return promoted;
  }

  function markIssue(category, context = {}) {
    if (!started || mode === "off" || !isCurrentContext(context)) return null;
    incidentCounter += 1;
    const incidentRef = `n${  incidentCounter}`;
    const promoted = promoteRecentDebug();
    const event = emit("user.marker", {
      category: ["slow_load", "blank_cards", "position_jump", "selection_issue", "other"].includes(category) ? category : "other",
      mode: mode,
    }, { ...context, incidentRef });
    if (!event) return null;
    recentIncidents.push({ ref: incidentRef, at: now(), promoted });
    if (recentIncidents.length > 20) recentIncidents.shift();
    return incidentRef;
  }

  /** Auto incident promotion is rate limited per page. */
  function autoIncident(kind, data, context = {}) {
    if (!started || mode === "off" || !isCurrentContext(context)) return null;
    const at = now();
    if (promotionCount >= LIMITS.AUTO_PROMOTE_MAX) return null;
    if (at - lastPromotionAt < LIMITS.AUTO_PROMOTE_INTERVAL_MS) return null;
    promotionCount += 1;
    lastPromotionAt = at;
    incidentCounter += 1;
    const incidentRef = `n${  incidentCounter}`;
    promoteRecentDebug();
    const event = emit("anomaly.detected", data, { ...context, incidentRef });
    return event ? incidentRef : null;
  }

  function startSession(sessionData = {}) {
    if (started) endSession("manual");
    generation += 1;
    resetSessionState();
    if (mode === "detailed") detailedUntil = now() + LIMITS.DETAILED_MAX_MS;
    sessionId = randomHex32();
    sessionStartedAt = now();
    started = true;
    const data = {
      browser_family: sessionData.browserFamily || "unknown",
      browser_major: sessionData.browserMajor ?? null,
      viewport_width: sessionData.viewportWidth || 0,
      viewport_height: sessionData.viewportHeight || 0,
      dpr: sessionData.dpr ?? null,
      screen_width: sessionData.screenWidth ?? null,
      screen_height: sessionData.screenHeight ?? null,
      locale: sessionData.locale ?? null,
      touch: Boolean(sessionData.touch),
      capability_long_tasks: sessionData.capabilityLongTasks || "unknown",
      capability_memory: sessionData.capabilityMemory || "unknown",
      capability_paint_timing: sessionData.capabilityPaintTiming || "unknown",
      mode: mode === "detailed" ? "detailed" : "summary",
      detailed: mode === "detailed",
    };
    emit("session.start", data);
    statusChange();
    return { sessionId, generation };
  }

  function endSession(reason = "manual") {
    if (!started) return null;
    flushImageSummary();
    flushSelection();
    const duration = Math.max(0, now() - sessionStartedAt);
    emit("session.end", { reason, duration_ms: duration });
    started = false;
    if (selectionTimer !== null) {
      clearTimerFn(selectionTimer);
      selectionTimer = null;
    }
    statusChange();
    return { sessionId, duration };
  }

  function setMode(nextMode) {
    if (!["off", "summary", "detailed"].includes(nextMode)) return false;
    if (nextMode === "off") {
      mode = "off";
      started = false;
      resetSessionState();
      statusChange();
      return true;
    }
    mode = nextMode;
    detailedUntil = nextMode === "detailed" ? now() + LIMITS.DETAILED_MAX_MS : 0;
    if (nextMode === "summary") promotedUntil = 0;
    statusChange();
    return true;
  }

  function setServerEnabled(enabled) {
    if (enabled === true) {
      serverEnabled = true;
      localOnly = false;
      for (const entry of startupPending) enqueue(entry.event, entry.bytes, { skipTrim: true });
      startupPending = [];
      pendingStartupBytes = 0;
      trimQueue();
    } else if (enabled === false) {
      // Disabled/unsupported is a hard stop, not just an upload pause. Clear
      // the bounded startup buffer and page history so a later export cannot
      // claim that an automatically disabled server was still being recorded.
      serverEnabled = false;
      localOnly = false;
      started = false;
      resetSessionState();
      sessionId = "";
    } else {
      serverEnabled = null;
    }
    statusChange();
  }

  function setLocalOnly(value) {
    localOnly = Boolean(value);
    if (localOnly) serverEnabled = false;
    statusChange();
  }

  /** Called when the authenticated account changes or logs out. */
  function resetIdentity(nextIdentity) {
    if (nextIdentity === identity) return false;
    identity = nextIdentity;
    generation += 1;
    started = false;
    serverEnabled = null;
    localOnly = false;
    if (mode === "detailed") mode = "summary";
    resetSessionState();
    sessionId = "";
    statusChange();
    return true;
  }

  function getStatus() {
    const detailed = isDetailedActive();
    return {
      schema_version: SCHEMA_VERSION,
      mode,
      local_only: localOnly,
      server_enabled: serverEnabled,
      session: sessionId || null,
      queue_events: queue.length,
      queue_bytes: queueBytes,
      history_events: history.length,
      history_bytes: historyBytes,
      dropped: { ...dropped },
      detailed_remaining_ms: detailed ? Math.max(0, detailedUntil - now()) : 0,
      promotion_count: promotionCount,
      last_record_error: lastRecordError,
      incidents: recentIncidents.map((item) => ({ ref: item.ref, promoted: item.promoted })),
    };
  }

  function getQueueEvents() {
    return queue.map((item) => item.event);
  }

  function ackEvents(seqs) {
    const acked = new Set(seqs || []);
    const before = queue.length;
    queue = queue.filter((item) => !acked.has(item.event.seq));
    queueBytes = queue.reduce((sum, item) => sum + item.bytes, 0);
    return before - queue.length;
  }

  function dropEvents(seqs) {
    const removed = ackEvents(seqs);
    dropped.transport += removed;
    return removed;
  }

  function noteTransportDrop(count = 1) {
    dropped.transport += count;
  }

  function exportSnapshot(meta = {}) {
    return {
      schema_version: SCHEMA_VERSION,
      client_session: sessionId || null,
      client_build: { version: clientBuild.version ?? null, commit: clientBuild.commit ?? null },
      mode,
      generated_at: new Date().toISOString(),
      dropped: { ...dropped },
      truncated: Boolean(meta.truncated),
      events: history.flatMap((entry) => {
        const clean = sanitizeEvent(entry.event);
        return clean.ok ? [clean.value] : [];
      }),
    };
  }

  function clearHistory() {
    history = [];
    historyBytes = 0;
    dropped.history += 1;
  }

  return {
    // lifecycle
    startSession,
    endSession,
    resetIdentity,
    setMode,
    setServerEnabled,
    setLocalOnly,
    getStatus,
    exportSnapshot,
    // recording
    beginLoad,
    finishLoad,
    record: emit,
    recordRuntimeError,
    flushErrorDedupe,
    markIssue,
    autoIncident,
    codeForItem,
    codeForDir,
    nextOperationRef,
    makeContext(context = {}) {
      return freezeContext({ ...context, generation });
    },
    // transport-facing queue primitives (no network here)
    getQueueEvents,
    ackEvents,
    dropEvents,
    noteTransportDrop,
    clearHistory,
    getClientSession() {
      return sessionId || null;
    },
    getGeneration() {
      return generation;
    },
    getMode() {
      return mode;
    },
    getHistoryCount() {
      return history.length;
    },
    newRequestRef() {
      return randomHex32();
    },
    noteImageStart,
    noteImageEnd,
    noteImageDecodeFailure,
    flushImageSummary,
    flushSelection,
    selectionChanged,
    isStarted() {
      return started;
    },
    isDetailedActive,
    isCurrentContext,
    hasEventType(eventName) {
      return Object.hasOwn(EVENT_FIELDS, eventName);
    },
  };
}

export default createDiagnosticsRecorder;