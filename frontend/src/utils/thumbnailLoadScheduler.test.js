import { beforeEach, describe, expect, it, vi } from "vitest";

const scroll = vi.hoisted(() => ({
  tick: new Set(),
  idle: new Set(),
  velocity: 0,
  scrolling: false,
}));
const guard = vi.hoisted(() => ({
  active: false,
  listeners: new Set(),
}));

vi.mock("./scrollActivity.js", () => ({
  onScrollTick: (fn) => { scroll.tick.add(fn); return () => scroll.tick.delete(fn); },
  onScrollIdle: (fn) => { scroll.idle.add(fn); return () => scroll.idle.delete(fn); },
  getScrollVelocity: () => scroll.velocity,
  isScrolling: () => scroll.scrolling,
}));

vi.mock("./interactionGuard.js", () => ({
  isInteractionActive: () => guard.active,
  onInteractionChange: (fn) => { guard.listeners.add(fn); return () => guard.listeners.delete(fn); },
}));

const emitTick = (velocity) => {
  scroll.velocity = velocity;
  scroll.scrolling = true;
  for (const fn of [...scroll.tick]) fn(velocity);
};
const emitIdle = () => {
  scroll.velocity = 0;
  scroll.scrolling = false;
  for (const fn of [...scroll.idle]) fn();
};
const setInteraction = (active) => {
  guard.active = active;
  for (const fn of [...guard.listeners]) fn(active);
};

const instantFrame = (callback) => { callback(); return 1; };

import {
  registerThumbnailCandidate,
  resetThumbnailScheduler,
  unregisterThumbnailCandidate,
} from "./thumbnailLoadScheduler.js";

function makeItem(id) {
  return { id, calls: 0, evaluateThumbnailNeed() { this.calls += 1; } };
}

describe("thumbnail load scheduler", () => {
  beforeEach(() => {
    resetThumbnailScheduler();
    scroll.tick.clear();
    scroll.idle.clear();
    scroll.velocity = 0;
    scroll.scrolling = false;
    guard.active = false;
    guard.listeners.clear();
    vi.stubGlobal("requestAnimationFrame", instantFrame);
  });

  it("loads an in-band candidate as soon as it registers", () => {
    const item = makeItem(1);
    registerThumbnailCandidate(item);
    expect(item.calls).toBe(1);
  });

  it("holds a candidate that entered during a fling until the fling slows", () => {
    emitTick(3);
    const item = makeItem(1);
    registerThumbnailCandidate(item);
    expect(item.calls).toBe(0);
    emitTick(3);
    expect(item.calls).toBe(0);
    emitTick(0.2);
    expect(item.calls).toBe(1);
  });

  it("catches up every in-band candidate once scrolling settles", () => {
    emitTick(3);
    const items = Array.from({ length: 40 }, (_, i) => makeItem(i));
    for (const item of items) registerThumbnailCandidate(item);
    expect(items.every((item) => item.calls === 0)).toBe(true);
    emitIdle();
    expect(items.every((item) => item.calls === 1)).toBe(true);
  });

  it("catches up after a gesture ends", () => {
    guard.active = true;
    const items = [makeItem(1), makeItem(2)];
    for (const item of items) registerThumbnailCandidate(item);
    expect(items.every((item) => item.calls === 0)).toBe(true);
    setInteraction(false);
    expect(items.every((item) => item.calls === 1)).toBe(true);
  });

  it("does not walk the registry on every scroll tick", () => {
    const items = Array.from({ length: 50 }, (_, i) => makeItem(i));
    for (const item of items) registerThumbnailCandidate(item);
    const before = items.reduce((sum, item) => sum + item.calls, 0);
    for (let i = 0; i < 10; i += 1) emitTick(0.1);
    const after = items.reduce((sum, item) => sum + item.calls, 0);
    expect(after).toBe(before);
  });

  it("evaluates a candidate once per frame even if it is queued again", () => {
    emitTick(3);
    const item = makeItem(1);
    registerThumbnailCandidate(item);
    registerThumbnailCandidate(item);
    emitIdle();
    expect(item.calls).toBe(1);
  });

  it("drops a candidate that left the band before the catch-up", () => {
    emitTick(3);
    const item = makeItem(1);
    registerThumbnailCandidate(item);
    unregisterThumbnailCandidate(item);
    emitIdle();
    expect(item.calls).toBe(0);
  });

  it("stops listening after the last candidate unregisters", () => {
    const item = makeItem(1);
    registerThumbnailCandidate(item);
    expect(scroll.idle.size).toBe(1);
    unregisterThumbnailCandidate(item);
    expect(scroll.idle.size).toBe(0);
    expect(guard.listeners.size).toBe(0);
  });
});
