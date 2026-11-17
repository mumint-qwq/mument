// Bounded long-task aggregation for the production diagnostics recorder.
// It observes only while the recorder has an active session, and flushes a
// summary (not individual entries) at most every 30 seconds or when hidden.
const FLUSH_INTERVAL_MS = 30000;

export function installLongTaskCollector(recorder, target = globalThis.window, options = {}) {
  const doc = options.document ?? globalThis.document;
  const stats = { count: 0, sum: 0, max: 0 };
  let observer = null;
  try {
    const supported = target?.PerformanceObserver?.supportedEntryTypes?.includes?.("longtask");
    if (!supported) return () => {};
    observer = new target.PerformanceObserver((list) => {
      if (doc?.hidden || !recorder?.isStarted?.()) return;
      for (const entry of list.getEntries()) {
        const duration = Number(entry?.duration);
        if (!Number.isFinite(duration) || duration < 0) continue;
        stats.count = Math.min(1000000, stats.count + 1);
        stats.sum += duration;
        stats.max = Math.max(stats.max, duration);
      }
    });
    observer.observe({ entryTypes: ["longtask"] });
  } catch {
    return () => {};
  }

  const startedAt = Number(globalThis.performance?.now?.() ?? 0);
  let emittedCount = 0;
  let stopped = false;

  function flush(force = false) {
    if (stopped || !recorder?.isStarted?.() || recorder.getMode?.() === "off") return;
    if (!force && stats.count === emittedCount) return;
    const elapsed = Number(globalThis.performance?.now?.() ?? 0) - startedAt;
    recorder.record("perf.long_task_summary", {
      count: stats.count,
      sum_ms: stats.sum,
      max_ms: stats.max,
      phase: elapsed < 10000 ? "startup" : "background",
      supported: true,
    });
    emittedCount = stats.count;
  }

  const timer = setInterval(() => flush(false), FLUSH_INTERVAL_MS);
  const onVisibility = () => {
    if (doc?.hidden) flush(true);
  };
  doc?.addEventListener?.("visibilitychange", onVisibility);

  return () => {
    if (stopped) return;
    stopped = true;
    clearInterval(timer);
    doc?.removeEventListener?.("visibilitychange", onVisibility);
    try {
      observer.disconnect();
    } catch {
      // best effort
    }
  };
}
