// Optional runtime error collection. The recorder sanitizes; this module only
// observes and always preserves the application's existing error handling.
import { sanitizeRuntimeError } from "./schema.js";

/**
 * @param {object} recorder diagnostics recorder
 * @param {Window} [target]
 * @returns {() => void} uninstall
 */
export function installRuntimeErrorCollectors(recorder, target = globalThis.window) {
  if (!target || typeof target.addEventListener !== "function" || !recorder) {
    return () => {};
  }
  const onError = (event) => {
    if (!event?.error) return; // resource load errors are handled by image counters
    try {
      recorder.recordRuntimeError("window", event.error);
    } catch {
      // Never let diagnostics replace the browser's own error handling.
    }
  };
  const onRejection = (event) => {
    try {
      recorder.recordRuntimeError("unhandled", event?.reason);
    } catch {
      // Best effort only.
    }
  };
  target.addEventListener("error", onError);
  target.addEventListener("unhandledrejection", onRejection);
  return () => {
    target.removeEventListener("error", onError);
    target.removeEventListener("unhandledrejection", onRejection);
  };
}

/**
 * Wrap a Vue app's errorHandler without dropping the previous one. Vue calls
 * this after its own logging; we keep the same behaviour when no previous
 * handler exists.
 * @param {import("vue").App} app
 * @param {object} recorder
 * @returns {() => void} restore
 */
export function installVueErrorHandler(app, recorder) {
  if (!app?.config || !recorder) return () => {};
  const previous = app.config.errorHandler;
  const handler = (error, instance, info) => {
    try {
      recorder.recordRuntimeError("vue", error);
    } catch {
      // Recording must not throw into Vue.
    }
    if (typeof previous === "function") {
      previous(error, instance, info);
    } else {
      console.error(error);
    }
  };
  app.config.errorHandler = handler;
  return () => {
    if (app.config.errorHandler === handler) app.config.errorHandler = previous;
  };
}

/** Build a session.start payload from browser globals without device identity. */
export function collectSessionEnvironment(win = globalThis.window) {
  const nav = win?.navigator || {};
  const ua = String(nav.userAgent || "");
  const family = /Edg\//.test(ua) ? "edge" : /Firefox\//.test(ua) ? "firefox" : /Chrome\//.test(ua) ? "chrome" : /Safari\//.test(ua) ? "safari" : "other";
  const majorMatch = ua.match(/(?:Edg|Firefox|Chrome|Version)\/(\d+)/);
  const major = majorMatch ? Number(majorMatch[1]) : null;
  const viewportWidth = Number(win?.innerWidth) || 0;
  const viewportHeight = Number(win?.innerHeight) || 0;
  const dpr = Number(win?.devicePixelRatio);
  const screenWidth = Number(win?.screen?.width);
  const screenHeight = Number(win?.screen?.height);
  const longTasks = typeof win?.PerformanceObserver === "function" && (() => {
    try {
      return win.PerformanceObserver.supportedEntryTypes?.includes("longtask") || false;
    } catch {
      return false;
    }
  })();
  const memory = typeof nav?.deviceMemory === "number" ? "supported" : "unsupported";
  return {
    browserFamily: family,
    browserMajor: major,
    viewportWidth,
    viewportHeight,
    dpr: Number.isFinite(dpr) && dpr > 0 ? dpr : null,
    screenWidth: Number.isFinite(screenWidth) ? screenWidth : null,
    screenHeight: Number.isFinite(screenHeight) ? screenHeight : null,
    locale: typeof nav.language === "string" ? nav.language.slice(0, 20) : null,
    touch: Number(nav.maxTouchPoints) > 0 || "ontouchstart" in win,
    capabilityLongTasks: longTasks ? "supported" : "unsupported",
    capabilityMemory: memory,
    capabilityPaintTiming: typeof win?.PerformancePaintTiming === "function" || "PerformancePaintTiming" in (win || {}) ? "supported" : "unsupported",
  };
}

export function errorFingerprint(source, error) {
  return sanitizeRuntimeError(source, error).fingerprint;
}
