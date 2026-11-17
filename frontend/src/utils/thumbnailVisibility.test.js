import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  findThumbnailScrollRoot,
  resetThumbnailVisibility,
  watchThumbnailBand,
  watchThumbnailRelease,
} from "./thumbnailVisibility.js";

class FakeIntersectionObserver {
  static instances = [];
  constructor(callback, options) {
    this.callback = callback;
    this.options = options;
    this.observed = new Set();
    this.disconnected = false;
    FakeIntersectionObserver.instances.push(this);
  }
  observe(element) { this.observed.add(element); }
  unobserve(element) { this.observed.delete(element); }
  disconnect() { this.disconnected = true; this.observed.clear(); }
  emit(entries) { this.callback(entries); }
}

describe("thumbnail visibility observers", () => {
  beforeEach(() => {
    FakeIntersectionObserver.instances = [];
    resetThumbnailVisibility();
    vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);
  });

  it("shares one band observer per scroll root", () => {
    const root = {};
    const first = document.createElement("div");
    const second = document.createElement("div");
    const stopFirst = watchThumbnailBand(first, root, {});
    watchThumbnailBand(second, root, {});
    expect(FakeIntersectionObserver.instances).toHaveLength(1);
    expect(FakeIntersectionObserver.instances[0].options).toMatchObject({ root, rootMargin: "600px" });
    expect(FakeIntersectionObserver.instances[0].observed.size).toBe(2);
    stopFirst();
    expect(FakeIntersectionObserver.instances[0].observed.size).toBe(1);
    expect(FakeIntersectionObserver.instances[0].disconnected).toBe(false);
  });

  it("prefers the scroll wrapper class without a style walk", () => {
    const wrapper = document.createElement("div");
    wrapper.className = "scroll-wrapper";
    const element = document.createElement("div");
    wrapper.appendChild(element);
    expect(findThumbnailScrollRoot(element)).toBe(wrapper);
  });

  it("finds the nearest scrolling ancestor", () => {
    const outer = document.createElement("div");
    outer.style.overflowY = "auto";
    const inner = document.createElement("div");
    const element = document.createElement("div");
    outer.appendChild(inner);
    inner.appendChild(element);
    expect(findThumbnailScrollRoot(element)).toBe(outer);
    const plain = document.createElement("div");
    plain.style.overflowY = "visible";
    const child = document.createElement("div");
    plain.appendChild(child);
    expect(findThumbnailScrollRoot(child)).toBeNull();
  });

  it("reports band enter and leave to the row", () => {
    const element = document.createElement("div");
    const onEnter = vi.fn();
    const onLeave = vi.fn();
    watchThumbnailBand(element, null, { onEnter, onLeave });
    const observer = FakeIntersectionObserver.instances[0];
    observer.emit([{ target: element, isIntersecting: true }]);
    observer.emit([{ target: element, isIntersecting: false }]);
    expect(onEnter).toHaveBeenCalledTimes(1);
    expect(onLeave).toHaveBeenCalledTimes(1);
  });

  it("reports release only after leaving the 1200px margin", () => {
    const element = document.createElement("div");
    const onRelease = vi.fn();
    watchThumbnailRelease(element, null, { onRelease });
    const observer = FakeIntersectionObserver.instances[0];
    expect(observer.options.rootMargin).toBe("1200px");
    observer.emit([{ target: element, isIntersecting: true }]);
    expect(onRelease).not.toHaveBeenCalled();
    observer.emit([{ target: element, isIntersecting: false }]);
    expect(onRelease).toHaveBeenCalledTimes(1);
  });

  it("disconnects once the last row unwatches and creates a fresh observer later", () => {
    const root = {};
    const first = document.createElement("div");
    const second = document.createElement("div");
    const stopFirst = watchThumbnailBand(first, root, {});
    const stopSecond = watchThumbnailBand(second, root, {});
    stopFirst();
    stopSecond();
    expect(FakeIntersectionObserver.instances[0].disconnected).toBe(true);
    watchThumbnailBand(first, root, {});
    expect(FakeIntersectionObserver.instances).toHaveLength(2);
  });
});
