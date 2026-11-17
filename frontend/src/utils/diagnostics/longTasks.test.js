// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { installLongTaskCollector } from "./longTasks.js";

function setup() {
  let callback = null;
  class FakeObserver {
    static supportedEntryTypes = ["longtask"];
    constructor(cb) { callback = cb; }
    observe() {}
    disconnect() {}
  }
  const listeners = {};
  const doc = {
    hidden: false,
    addEventListener: (name, fn) => { listeners[name] = fn; },
    removeEventListener: (name) => { delete listeners[name]; },
  };
  const records = [];
  const recorder = {
    isStarted: () => true,
    getMode: () => "summary",
    record: (name, data) => { records.push({ name, data }); },
  };
  const target = { PerformanceObserver: FakeObserver };
  const uninstall = installLongTaskCollector(recorder, target, { document: doc });
  return {
    records,
    doc,
    listeners,
    emit(entries) { callback({ getEntries: () => entries }); },
    uninstall,
  };
}

describe("long task collector", () => {
  afterEach(() => { vi.useRealTimers(); });

  it("aggregates entries and emits a bounded summary", () => {
    vi.useFakeTimers();
    const ctx = setup();
    ctx.emit([{ duration: 60 }, { duration: 90 }]);
    vi.advanceTimersByTime(30000);
    expect(ctx.records).toHaveLength(1);
    expect(ctx.records[0].name).toBe("perf.long_task_summary");
    expect(ctx.records[0].data).toMatchObject({ count: 2, sum_ms: 150, max_ms: 90, supported: true });
    ctx.uninstall();
  });

  it("stops observing after uninstall and flushes when hidden", () => {
    vi.useFakeTimers();
    const ctx = setup();
    ctx.emit([{ duration: 120 }]);
    ctx.doc.hidden = true;
    ctx.listeners.visibilitychange();
    expect(ctx.records).toHaveLength(1);
    ctx.uninstall();
    vi.advanceTimersByTime(60000);
    expect(ctx.records).toHaveLength(1);
  });
});
