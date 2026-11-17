import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import {
  getScrollVelocity,
  isScrolling,
  onScrollIdle,
  onScrollStart,
  onScrollTick,
  registerScrollRoot,
  resetScrollActivity,
  SCROLL_IDLE_MS,
  SCROLL_TICK_MS,
} from "./scrollActivity.js";

const scroll = () => window.dispatchEvent(new Event("scroll"));

describe("scroll activity", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => { resetScrollActivity(); vi.useRealTimers(); });

  it("reports scrolling until the idle delay passes without another event", () => {
    const idle = vi.fn();
    const start = vi.fn();
    const offIdle = onScrollIdle(idle);
    const offStart = onScrollStart(start);
    scroll();
    expect(isScrolling()).toBe(true);
    expect(start).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(SCROLL_IDLE_MS - 1);
    expect(isScrolling()).toBe(true);
    expect(idle).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(isScrolling()).toBe(false);
    expect(idle).toHaveBeenCalledTimes(1);
    offIdle(); offStart();
  });

  it("extends the idle deadline while scrolling continues", () => {
    const idle = vi.fn();
    onScrollIdle(idle);
    scroll();
    vi.advanceTimersByTime(100);
    scroll();
    vi.advanceTimersByTime(100);
    expect(idle).not.toHaveBeenCalled();
    vi.advanceTimersByTime(SCROLL_IDLE_MS - 100);
    expect(idle).toHaveBeenCalledTimes(1);
  });

  it("does not call removed listeners", () => {
    const idle = vi.fn();
    const off = onScrollIdle(idle);
    off();
    scroll();
    vi.advanceTimersByTime(SCROLL_IDLE_MS);
    expect(idle).not.toHaveBeenCalled();
  });
});

describe("scroll velocity and ticks", () => {
  const makeScroller = (top = 0) => {
    const el = document.createElement("div");
    el.scrollTop = top;
    document.body.appendChild(el);
    return el;
  };
  const scrollOn = (el) => el.dispatchEvent(new Event("scroll"));

  beforeEach(() => { vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance", "Date"] }); });
  afterEach(() => { resetScrollActivity(); vi.useRealTimers(); });

  it("estimates velocity from position samples", () => {
    const el = makeScroller(0);
    scrollOn(el);
    vi.advanceTimersByTime(40);
    el.scrollTop = 12;
    scrollOn(el);
    expect(getScrollVelocity()).toBeGreaterThan(0.25);
    expect(getScrollVelocity()).toBeLessThan(0.35);
  });

  it("ticks at most once per SCROLL_TICK_MS", () => {
    const ticks = vi.fn();
    const off = onScrollTick(ticks);
    const el = makeScroller(0);
    scrollOn(el);
    expect(ticks).toHaveBeenCalledTimes(1); // leading tick
    vi.advanceTimersByTime(SCROLL_TICK_MS - 10);
    scrollOn(el);
    expect(ticks).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(20);
    scrollOn(el);
    expect(ticks).toHaveBeenCalledTimes(2);
    off();
  });

  it("ignores scrolling in an unrelated container", () => {
    const root = makeScroller(0);
    const other = makeScroller(0);
    const off = registerScrollRoot(root);
    other.scrollTop = 200;
    scrollOn(other);
    expect(isScrolling()).toBe(false);
    root.scrollTop = 100;
    scrollOn(root);
    expect(isScrolling()).toBe(true);
    off();
  });

  it("resets velocity when scrolling settles", () => {
    const el = makeScroller(0);
    scrollOn(el);
    vi.advanceTimersByTime(40);
    el.scrollTop = 40;
    scrollOn(el);
    expect(getScrollVelocity()).toBeGreaterThan(0);
    vi.advanceTimersByTime(SCROLL_IDLE_MS);
    expect(getScrollVelocity()).toBe(0);
  });
});
