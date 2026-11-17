import { beforeEach, describe, expect, it } from "vitest";
import {
  consumeScrollbarClick,
  isScrollbarRegion,
  isScrollbarSequenceActive,
  notePointerSequenceStart,
  resetScrollbarClickGuard,
} from "./scrollbarClickGuard";

function buildScrollbar() {
  const scrollbar = document.createElement("div");
  scrollbar.className = "custom-scrollbar";
  const thumb = document.createElement("div");
  thumb.className = "thumb";
  const icon = document.createElement("i");
  icon.className = "material-symbols";
  thumb.appendChild(icon);
  scrollbar.appendChild(thumb);
  document.body.appendChild(scrollbar);
  return { scrollbar, thumb, icon };
}

describe("scrollbar click guard", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    resetScrollbarClickGuard();
  });

  it("recognises the thumb and every descendant of the scrollbar", () => {
    const { scrollbar, thumb, icon } = buildScrollbar();
    expect(isScrollbarRegion(scrollbar)).toBe(true);
    expect(isScrollbarRegion(thumb)).toBe(true);
    expect(isScrollbarRegion(icon)).toBe(true);
    const text = document.createTextNode("A");
    icon.appendChild(text);
    expect(isScrollbarRegion(text)).toBe(true);
    expect(isScrollbarRegion(document.body)).toBe(false);
    expect(isScrollbarRegion(null)).toBe(false);
  });

  it("keeps a click on the scrollbar itself out of selection clearing", () => {
    const { thumb } = buildScrollbar();
    expect(consumeScrollbarClick(thumb)).toBe(true);
  });

  it("keeps the click produced by a drag released outside the thumb", () => {
    const { thumb } = buildScrollbar();
    notePointerSequenceStart(thumb);
    // The browser can retarget this click to a common ancestor such as #main.
    const commonAncestor = document.body;
    expect(consumeScrollbarClick(commonAncestor)).toBe(true);
    // The recorded sequence is consumed, so the next click is normal again.
    expect(consumeScrollbarClick(commonAncestor)).toBe(false);
  });

  it("treats a sequence that began outside the scrollbar as normal", () => {
    const { thumb } = buildScrollbar();
    const content = document.createElement("div");
    document.body.appendChild(content);
    notePointerSequenceStart(thumb);
    notePointerSequenceStart(content);
    expect(isScrollbarSequenceActive()).toBe(false);
    expect(consumeScrollbarClick(content)).toBe(false);
  });

  it("never claims a keyboard or programmatic click from a stale sequence", () => {
    const { thumb } = buildScrollbar();
    notePointerSequenceStart(thumb);
    // No click followed the pointer sequence (right click, release outside the
    // window, or a touch tap whose touchstart was prevented). A keyboard click
    // has detail 0 and must still reach blank-click clearing.
    expect(consumeScrollbarClick(document.body, 0)).toBe(false);
    expect(isScrollbarSequenceActive()).toBe(false);
    expect(consumeScrollbarClick(document.body, 1)).toBe(false);
  });

  it("can be reset on cancel or unmount", () => {
    const { thumb } = buildScrollbar();
    notePointerSequenceStart(thumb);
    resetScrollbarClickGuard();
    expect(isScrollbarSequenceActive()).toBe(false);
    expect(consumeScrollbarClick(document.body)).toBe(false);
  });
});
