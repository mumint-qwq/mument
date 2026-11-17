import { describe, expect, it, vi } from "vitest";
import { collectSessionEnvironment, installRuntimeErrorCollectors, installVueErrorHandler } from "./errors.js";

function fakeRecorder() {
  const calls = [];
  return {
    calls,
    recordRuntimeError: (source, error) => calls.push({ source, error }),
  };
}

function fakeWindow() {
  const listeners = new Map();
  return {
    addEventListener: vi.fn((name, fn) => listeners.set(name, fn)),
    removeEventListener: vi.fn((name) => listeners.delete(name)),
    listeners,
    navigator: { userAgent: "Mozilla/5.0 Chrome/141.0 Safari/537.36", language: "zh-CN", maxTouchPoints: 5 },
    innerWidth: 414,
    innerHeight: 900,
    devicePixelRatio: 2.75,
    screen: { width: 414, height: 900 },
    PerformanceObserver: function PerformanceObserver() {},
  };
}

describe("runtime error collectors", () => {
  it("forwards errors and unhandled rejections, then removes listeners", () => {
    const recorder = fakeRecorder();
    const win = fakeWindow();
    const uninstall = installRuntimeErrorCollectors(recorder, win);
    win.listeners.get("error")({ error: new TypeError("x") });
    win.listeners.get("unhandledrejection")({ reason: new Error("y") });
    expect(recorder.calls.map((call) => call.source)).toEqual(["window", "unhandled"]);
    uninstall();
    expect(win.removeEventListener).toHaveBeenCalledTimes(2);
  });

  it("keeps the previous Vue error handler", () => {
    const recorder = fakeRecorder();
    const previous = vi.fn();
    const app = { config: { errorHandler: previous } };
    const restore = installVueErrorHandler(app, recorder);
    const error = new Error("z");
    app.config.errorHandler(error, {}, "render");
    expect(recorder.calls[0].source).toBe("vue");
    expect(previous).toHaveBeenCalledWith(error, {}, "render");
    restore();
    expect(app.config.errorHandler).toBe(previous);
  });

  it("collects coarse browser information without a device identity", () => {
    const env = collectSessionEnvironment(fakeWindow());
    expect(env.browserFamily).toBe("chrome");
    expect(env.browserMajor).toBe(141);
    expect(env.touch).toBe(true);
    expect(env.locale).toBe("zh-CN");
    expect(JSON.stringify(env)).not.toContain("userAgent");
  });
});
