import { describe, expect, it, vi } from "vitest";
import {
  clampScrollTop,
  createAnchorRestore,
  resolveAnchorTarget,
} from "./listingRestore.js";

const items = [
  { source: "pictures", path: "/a.jpg" },
  { source: "pictures", path: "/b.jpg" },
  { source: "pictures", path: "/c.jpg" },
];

function fakeScroller({ scrollTop = 500, height = 800, content = 100_000 } = {}) {
  return {
    scrollTop,
    clientHeight: height,
    scrollHeight: content,
    getBoundingClientRect() {
      return { top: 0, left: 0, right: 400, bottom: height, width: 400, height };
    },
  };
}

function fakeElement(scroller, contentTop, height = 100) {
  return {
    getBoundingClientRect() {
      const top = contentTop - scroller.scrollTop;
      return { top, left: 0, right: 100, bottom: top + height, width: 100, height };
    },
  };
}

const anchorFor = (overrides = {}) => ({
  ticket: "a1",
  reason: "move",
  anchorKey: "pictures|/b.jpg",
  anchorKind: "identity",
  offsetTop: 40,
  candidates: ["pictures|/c.jpg"],
  fallbackScrollTop: 5000,
  ...overrides,
});

const drain = async () => {
  await Promise.resolve();
  await Promise.resolve();
};

describe("resolveAnchorTarget", () => {
  it("prefers stable identity over the old index", () => {
    const resolved = resolveAnchorTarget([
      { source: "pictures", path: "/c.jpg" },
      { source: "pictures", path: "/b.jpg" },
    ], anchorFor(), "pictures");
    expect(resolved).toMatchObject({ index: 1, strategy: "identity", offsetTop: 40 });
  });

  it("uses a bounded neighbour and puts it at the viewport top", () => {
    const resolved = resolveAnchorTarget(items, anchorFor({ anchorKey: "pictures|/gone.jpg" }), "pictures");
    expect(resolved).toMatchObject({ index: 2, strategy: "neighbor", offsetTop: 0 });
  });

  it("keeps the neighbour capture strategy when that neighbour is still present", () => {
    const resolved = resolveAnchorTarget(items, anchorFor({
      anchorKey: "pictures|/c.jpg",
      anchorKind: "neighbor_after",
      candidates: [],
      offsetTop: 0,
    }), "pictures");
    expect(resolved).toMatchObject({ index: 2, strategy: "neighbor", key: "pictures|/c.jpg", offsetTop: 0 });
  });

  it("reports the real backup identity after the primary disappears", () => {
    const resolved = resolveAnchorTarget(items, anchorFor({
      anchorKey: "pictures|/gone.jpg",
      candidates: ["pictures|/a.jpg"],
    }), "pictures");
    expect(resolved).toMatchObject({ index: 0, strategy: "neighbor", key: "pictures|/a.jpg" });
  });

  it("falls back to pixels when no identity survives", () => {
    const resolved = resolveAnchorTarget(items, anchorFor({ anchorKey: "x", candidates: [] }), "pictures");
    expect(resolved.strategy).toBe("fallback_pixels");
  });

  it("marks an empty directory explicitly", () => {
    expect(resolveAnchorTarget([], anchorFor(), "pictures").strategy).toBe("empty");
  });
});

describe("clampScrollTop", () => {
  it("keeps the final offset inside the legal range", () => {
    expect(clampScrollTop(5000, 1200)).toBe(1200);
    expect(clampScrollTop(-10, 1200)).toBe(0);
    expect(clampScrollTop(Number.NaN, 1200)).toBe(0);
  });
});

describe("createAnchorRestore", () => {
  it("does not report success merely because the bridge accepted the reveal", async () => {
    const scroller = fakeScroller();
    const results = [];
    let settle = null;
    const restore = createAnchorRestore({
      anchor: anchorFor(),
      items,
      source: "pictures",
      scroller,
      getElement: () => null,
      reveal: (target) => { settle = target.onSettled; return true; },
      onSettled: (result) => results.push(result),
      raf: (fn) => { fn(); return 1; },
    });
    restore.begin();
    await drain();
    expect(results).toHaveLength(0);
    settle({ result: "success", requestedOffset: 460, actualOffset: 460 });
    expect(results).toHaveLength(1);
    expect(results[0]).toMatchObject({ result: "success", requestedOffset: 460, actualOffset: 460 });
  });

  it("applies a failed bridge request as a clamped pixel fallback", () => {
    const scroller = fakeScroller({ content: 4000 });
    const results = [];
    const restore = createAnchorRestore({
      anchor: anchorFor({ fallbackScrollTop: 999_999 }),
      items,
      source: "pictures",
      scroller,
      getElement: () => null,
      reveal: (target) => { target.onSettled({ result: "failed", reason: "target_not_mounted" }); return true; },
      onSettled: (result) => results.push(result),
      raf: (fn) => { fn(); return 1; },
    });
    restore.begin();
    expect(results[0].result).toBe("failed");
    expect(scroller.scrollTop).toBe(3200);
    expect(results[0].strategy).toBe("fallback_pixels");
  });

  it("cancels without scrolling when the user interacts first", () => {
    const scroller = fakeScroller({ scrollTop: 800 });
    const results = [];
    let cancelCalled = false;
    const restore = createAnchorRestore({
      anchor: anchorFor(),
      items,
      source: "pictures",
      scroller,
      getElement: () => null,
      reveal: () => true,
      cancelReveal: () => { cancelCalled = true; },
      onSettled: (result) => results.push(result),
      raf: (fn) => { fn(); return 1; },
    });
    restore.begin();
    restore.handleUserIntent();
    expect(cancelCalled).toBe(true);
    expect(results[0]).toMatchObject({ result: "cancelled", reason: "user_scroll" });
    expect(scroller.scrollTop).toBe(800);
  });

  it("scrolls to an empty directory's top", () => {
    const scroller = fakeScroller({ scrollTop: 5000 });
    const results = [];
    const restore = createAnchorRestore({
      anchor: anchorFor(),
      items: [],
      source: "pictures",
      scroller,
      getElement: () => null,
      reveal: () => false,
      onSettled: (result) => results.push(result),
      raf: (fn) => { fn(); return 1; },
    });
    restore.begin();
    expect(scroller.scrollTop).toBe(0);
    expect(results[0]).toMatchObject({ result: "success", reason: "empty_directory" });
  });

  it("positions the target card by measured geometry when the bridge is unavailable", () => {
    const scroller = fakeScroller({ scrollTop: 1000 });
    const element = fakeElement(scroller, 5000);
    const results = [];
    const restore = createAnchorRestore({
      anchor: anchorFor(),
      items,
      source: "pictures",
      scroller,
      getElement: () => element,
      reveal: () => false,
      onSettled: (result) => results.push(result),
      raf: (fn) => { fn(); return 1; },
    });
    restore.begin();
    expect(scroller.scrollTop).toBe(4960);
    expect(results[0]).toMatchObject({ result: "success", requestedOffset: 4960, actualOffset: 4960 });
  });

  it("retries a missing target a bounded number of frames before falling back", () => {
    const scroller = fakeScroller();
    const results = [];
    const getElement = vi.fn(() => null);
    const restore = createAnchorRestore({
      anchor: anchorFor({ fallbackScrollTop: 120 }),
      items,
      source: "pictures",
      scroller,
      getElement,
      reveal: () => false,
      onSettled: (result) => results.push(result),
      raf: (fn) => { fn(); return 1; },
    });
    restore.begin();
    expect(getElement).toHaveBeenCalledTimes(8);
    expect(results[0]).toMatchObject({ result: "failed", reason: "target_not_mounted" });
    expect(scroller.scrollTop).toBe(120);
  });

  it("ignores a late bridge failure after the business request is gone", () => {
    const scroller = fakeScroller({ scrollTop: 731 });
    const results = [];
    let settle = null;
    let active = true;
    const restore = createAnchorRestore({
      anchor: anchorFor(),
      items,
      source: "pictures",
      scroller,
      getElement: () => null,
      isActive: () => active,
      reveal: (target) => { settle = target.onSettled; return true; },
      onSettled: (result) => results.push(result),
      raf: (fn) => { fn(); return 1; },
    });
    restore.begin();
    active = false;
    settle({ result: "failed", reason: "settle_timeout" });
    expect(scroller.scrollTop).toBe(731);
    expect(results[0]).toMatchObject({ result: "cancelled", reason: "superseded" });
    expect(results).toHaveLength(1);
  });

  it("does not write a pixel fallback when begin is already inactive", () => {
    const scroller = fakeScroller({ scrollTop: 731 });
    const results = [];
    createAnchorRestore({
      anchor: anchorFor({ anchorKey: "gone", candidates: [] }),
      items,
      source: "pictures",
      scroller,
      getElement: () => null,
      isActive: () => false,
      reveal: () => false,
      onSettled: (result) => results.push(result),
      raf: (fn) => { fn(); return 1; },
    }).begin();
    expect(scroller.scrollTop).toBe(731);
    expect(results[0]).toMatchObject({ result: "cancelled", reason: "superseded" });
  });

  it("does not scroll an empty directory when begin is already inactive", () => {
    const scroller = fakeScroller({ scrollTop: 731 });
    const results = [];
    createAnchorRestore({
      anchor: anchorFor(),
      items: [],
      source: "pictures",
      scroller,
      getElement: () => null,
      isActive: () => false,
      reveal: () => false,
      onSettled: (result) => results.push(result),
      raf: (fn) => { fn(); return 1; },
    }).begin();
    expect(scroller.scrollTop).toBe(731);
    expect(results[0]).toMatchObject({ result: "cancelled", reason: "superseded" });
  });

  it("cancels the accepted controller reveal, not just the callback", () => {
    const scroller = fakeScroller({ scrollTop: 731 });
    const results = [];
    const cancelReveal = vi.fn(() => true);
    const restore = createAnchorRestore({
      anchor: anchorFor(),
      items,
      source: "pictures",
      scroller,
      getElement: () => null,
      reveal: () => true,
      cancelReveal,
      onSettled: (result) => results.push(result),
      raf: (fn) => { fn(); return 1; },
    });
    restore.begin();
    expect(restore.cancel("navigation")).toBe(true);
    expect(cancelReveal).toHaveBeenCalledTimes(1);
    expect(cancelReveal).toHaveBeenCalledWith("a1", { reason: "navigation" });
    expect(scroller.scrollTop).toBe(731);
    expect(results[0]).toMatchObject({ result: "cancelled", reason: "navigation" });
  });

  it("still cancels the controller reveal when the user scrolls first", () => {
    const results = [];
    const cancelReveal = vi.fn(() => true);
    const restore = createAnchorRestore({
      anchor: anchorFor(),
      items,
      source: "pictures",
      scroller: fakeScroller({ scrollTop: 810 }),
      getElement: () => null,
      reveal: () => true,
      cancelReveal,
      onSettled: (result) => results.push(result),
      raf: (fn) => { fn(); return 1; },
    });
    restore.begin();
    restore.handleUserIntent();
    expect(cancelReveal).toHaveBeenCalledWith("a1", { reason: "user_scroll" });
    expect(results[0]).toMatchObject({ result: "cancelled", reason: "user_scroll" });
  });
});
