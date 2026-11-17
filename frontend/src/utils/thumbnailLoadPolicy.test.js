import { describe, expect, it } from "vitest";
import { SLOW_SCROLL_PX_PER_MS, shouldLoadThumbnail } from "./thumbnailLoadPolicy.js";

const base = {
  inBand: true,
  wanted: false,
  interactionActive: false,
  scrolling: false,
  velocity: 0,
};

describe("shouldLoadThumbnail", () => {
  it("loads an in-band card while the listing is still", () => {
    expect(shouldLoadThumbnail(base)).toBe(true);
  });

  it("does not load when the card is outside the prefetch band", () => {
    expect(shouldLoadThumbnail({ ...base, inBand: false })).toBe(false);
  });

  it("does not reload a card that already has a thumbnail", () => {
    expect(shouldLoadThumbnail({ ...base, wanted: true })).toBe(false);
  });

  it("waits while a pinch or drag interaction is active", () => {
    expect(shouldLoadThumbnail({ ...base, interactionActive: true })).toBe(false);
  });

  it("loads anywhere in the band during a slow scroll, not only mid-viewport", () => {
    expect(shouldLoadThumbnail({ ...base, scrolling: true, velocity: 0.3 })).toBe(true);
  });

  it("stops starting loads during a fast fling", () => {
    expect(shouldLoadThumbnail({ ...base, scrolling: true, velocity: 2 })).toBe(false);
  });

  it("treats the threshold itself as slow enough", () => {
    expect(shouldLoadThumbnail({ ...base, scrolling: true, velocity: SLOW_SCROLL_PX_PER_MS })).toBe(true);
    expect(shouldLoadThumbnail({ ...base, scrolling: true, velocity: SLOW_SCROLL_PX_PER_MS + 0.01 })).toBe(false);
  });

  it("uses an overridable threshold so tests can pin the boundary", () => {
    expect(shouldLoadThumbnail({ ...base, scrolling: true, velocity: 0.4, slowThreshold: 0.5 })).toBe(true);
    expect(SLOW_SCROLL_PX_PER_MS).toBeGreaterThan(0);
  });
});
