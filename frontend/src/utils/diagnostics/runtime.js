// Diagnostics bootstrap: owns the page-level singleton, auth lifecycle and the
// small UI state exposed to Profile settings. The recorder itself stays free of
// Vue/store imports.
import { watch } from "vue";
import { state } from "@/store";
import { createDiagnosticsRecorder } from "./recorder.js";
import { createDiagnosticsTransport } from "./transport.js";
import { collectSessionEnvironment, installRuntimeErrorCollectors, installVueErrorHandler } from "./errors.js";
import { installLongTaskCollector } from "./longTasks.js";
import { getDiagnosticsStatus, postDiagnosticsBatch } from "@/api/diagnostics.js";
import { setDiagnosticsSink } from "./telemetry.js";
import { LIMITS, STORAGE_KEYS } from "./config.js";

let runtime = null;

export function readClientBuild(doc = globalThis.document) {
  const version = doc?.querySelector?.('meta[name="quantum-mobile-ui"]')?.getAttribute("content") || null;
  const commit = doc?.querySelector?.('meta[name="quantum-source-commit"]')?.getAttribute("content") || null;
  const versionLooksReal = version && /^[A-Za-z0-9._-]{1,96}$/.test(version) && !version.startsWith("__");
  return {
    version: versionLooksReal ? version : null,
    commit: commit && /^[0-9a-f]{40}$/.test(commit) ? commit : null,
  };
}

function readStoredMode(storage) {
  try {
    const value = storage?.getItem?.(STORAGE_KEYS.mode);
    return value === "off" ? "off" : "summary";
  } catch {
    return "summary";
  }
}

function writeStoredMode(storage, mode) {
  try {
    storage?.setItem?.(STORAGE_KEYS.mode, mode === "off" ? "off" : "summary");
  } catch {
    // Private browsing may reject storage; diagnostics still works for this page.
  }
}

function loggedIn() {
  // Public share pages are not account-scoped diagnostics sessions even if a
  // cookie/session id happens to be present.
  if (state.shareInfo?.hash) return false;
  const username = state.user?.username;
  return Boolean(username && username !== "anonymous" && username !== "");
}

export function initDiagnostics(app, options = {}) {
  if (runtime) return runtime;
  const storage = options.storage ?? (typeof localStorage !== "undefined" ? localStorage : null);
  const win = options.window ?? (typeof window !== "undefined" ? window : null);
  const clientBuild = readClientBuild(options.document ?? (typeof document !== "undefined" ? document : null));
  const recorder = createDiagnosticsRecorder({
    clientBuild,
    onStatus: () => publish(),
  });
  const transport = createDiagnosticsTransport({
    recorder,
    api: { getStatus: getDiagnosticsStatus, postBatch: postDiagnosticsBatch },
    clientBuild,
    onState: () => publish(),
  });
  const listeners = new Set();
  const env = collectSessionEnvironment(win);
  let mode = readStoredMode(storage);
  let uninstallErrors = null;
  let uninstallVue = null;
  let uninstallLongTasks = null;
  let currentIdentity = "";
  let detailExpiresAt = 0;

  recorder.setMode(mode);
  setDiagnosticsSink({
    selectionChange: (data) => recorder.selectionChanged(data),
    operationResult: (data, context = undefined) => recorder.record("operation.result", data, context || {}),
  });

  function uiState() {
    const rec = recorder.getStatus();
    const tx = transport.getStatus();
    const server = rec.server_enabled === true
      ? "enabled"
      : rec.server_enabled === false
        ? (tx.state === "unsupported" ? "unsupported" : "disabled")
        : "unknown";
    return {
      schema_version: rec.schema_version,
      mode: rec.mode,
      local_only: rec.local_only,
      server,
      transport: tx.state,
      queue_events: rec.queue_events,
      history_events: rec.history_events,
      dropped: rec.dropped,
      detailed_remaining_ms: rec.detailed_remaining_ms,
      last_error: tx.last_error,
      last_status: tx.last_status,
      client_build: clientBuild,
      can_mark: rec.mode !== "off" && (rec.server_enabled === true || rec.local_only),
      can_export: rec.mode !== "off",
    };
  }

  function publish() {
    const rec = recorder.getStatus();
    // Capability changes are lifecycle changes: stop observers/history when
    // the server is disabled or unsupported instead of continuing local capture.
    if (rec.server_enabled === false && !rec.local_only) removeObservers();
    if ((rec.server_enabled === true || rec.local_only) && loggedIn() && !recorder.isStarted()) {
      startSession();
    }
    const snapshot = uiState();
    for (const listener of listeners) {
      try {
        listener(snapshot);
      } catch {
        // A component must not break diagnostics.
      }
    }
  }

  function collectionAllowed() {
    const rec = recorder.getStatus();
    // null means "waiting for the capability probe": start with a bounded
    // startup buffer so the first listing has a timeline. A false answer later
    // clears that buffer/history and stops collection.
    return rec.server_enabled !== false || rec.local_only;
  }

  function startSession() {
    if (mode === "off" || !loggedIn() || !collectionAllowed()) return false;
    if (recorder.isStarted()) return true;
    recorder.startSession(env);
    installObservers();
    return true;
  }

  function installObservers() {
    if (uninstallErrors === null) {
      uninstallErrors = installRuntimeErrorCollectors(recorder, win);
      uninstallVue = installVueErrorHandler(app, recorder);
      uninstallLongTasks = installLongTaskCollector(recorder, win);
    }
  }

  function removeObservers() {
    if (typeof uninstallErrors === "function") uninstallErrors();
    if (typeof uninstallVue === "function") uninstallVue();
    if (typeof uninstallLongTasks === "function") uninstallLongTasks();
    uninstallErrors = null;
    uninstallVue = null;
    uninstallLongTasks = null;
  }

  function handleAuthChange() {
    if (!loggedIn()) {
      currentIdentity = "";
      transport.stop();
      recorder.resetIdentity("");
      removeObservers();
      publish();
      return;
    }
    const identity = state.user.username;
    if (identity !== currentIdentity) {
      // Account switch: stop and abort old sends before resetting the recorder,
      // otherwise an old pending payload could be retried under the new auth.
      transport.stop();
      currentIdentity = identity;
      recorder.resetIdentity(identity);
    }
    if (mode === "off") {
      removeObservers();
      publish();
      return;
    }
    // Start with a bounded startup buffer; the probe will keep it (enabled),
    // clear it (disabled/unsupported), or pause uploads (auth/network).
    startSession();
    transport.start();
    publish();
  }

  function setMode(nextMode) {
    if (!["off", "summary", "detailed"].includes(nextMode)) return false;
    mode = nextMode;
    writeStoredMode(storage, nextMode);
    if (nextMode === "off") {
      transport.stop();
      removeObservers();
      recorder.setMode("off");
      detailExpiresAt = 0;
    } else {
      recorder.setMode(nextMode);
      detailExpiresAt = nextMode === "detailed" ? Date.now() + LIMITS.DETAILED_MAX_MS : 0;
      if (nextMode === "summary") detailExpiresAt = 0;
      if (loggedIn() && collectionAllowed()) {
        startSession();
        if (!recorder.getStatus().local_only) transport.start();
      }
    }
    publish();
    return true;
  }

  function markIssue(category) {
    const ref = recorder.markIssue(category, {});
    publish();
    return ref;
  }

  function enableLocalOnly() {
    if (mode === "off" || !loggedIn()) return;
    transport.stop();
    recorder.setLocalOnly(true);
    // Explicit local-only is a new, clearly labelled session; never reuse the
    // disabled-server session or its startup buffer. setLocalOnly publishes and
    // starts the new session; only start here if status was already published.
    if (!recorder.isStarted()) recorder.startSession(env);
    installObservers();
    publish();
  }

  function retryConnection() {
    if (mode === "off") return;
    const rec = recorder.getStatus();
    if (rec.local_only) return;
    transport.start();
    publish();
  }

  function exportSnapshot() {
    return recorder.exportSnapshot();
  }

  const cleanupFns = [];
  if (win?.addEventListener) {
    const onPageHide = () => {
      if (mode === "off") return;
      const rec = recorder.getStatus();
      if (!(rec.server_enabled === true || rec.local_only)) return;
      transport.flushBeforeUnload();
      recorder.endSession("pagehide");
    };
    const onOnline = () => {
      const rec = recorder.getStatus();
      if (mode !== "off" && loggedIn() && !rec.local_only) transport.start();
    };
    win.addEventListener("pagehide", onPageHide);
    win.addEventListener("online", onOnline);
    cleanupFns.push(() => win.removeEventListener("pagehide", onPageHide));
    cleanupFns.push(() => win.removeEventListener("online", onOnline));
  }

  const stopWatch = watch(
    () => [state.user?.username, state.sessionId],
    () => handleAuthChange(),
    { immediate: true }
  );

  runtime = {
    recorder,
    transport,
    getUiState: uiState,
    subscribe(listener) {
      listeners.add(listener);
      listener(uiState());
      return () => listeners.delete(listener);
    },
    setMode,
    markIssue,
    exportSnapshot,
    enableLocalOnly,
    retryConnection,
    getDetailExpiry: () => detailExpiresAt,
    shutdown() {
      stopWatch();
      transport.stop();
      removeObservers();
      for (const cleanup of cleanupFns) cleanup();
      recorder.setMode("off");
      setDiagnosticsSink(null);
      listeners.clear();
      runtime = null;
    },
  };
  handleAuthChange();
  return runtime;
}

export function getDiagnosticsRuntime() {
  return runtime;
}

export function resetDiagnosticsRuntimeForTests() {
  runtime = null;
}