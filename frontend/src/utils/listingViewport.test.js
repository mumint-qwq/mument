import { beforeEach, describe, expect, it } from "vitest";
import { captureListingAnchor, readVisibleListingEntries } from "./listingViewport.js";
import { peekListAnchor, clearListAnchor } from "./listAnchor.js";

const rect = (top, bottom, left = 0, right = 100) => ({
  top, bottom, left, right, width: right - left, height: bottom - top,
  x: left, y: top,
});

function mountListing(rects) {
  document.body.innerHTML = `
    <div class="scroll-wrapper">
      <div class="listing-view-root">
        <div class="listing-item" data-index="0" id="item-0"></div>
        <div class="listing-item" data-index="1" id="item-1"></div>
        <div class="listing-item" data-index="2" id="item-2"></div>
      </div>
    </div>
    <div class="file-list">
      <div class="listing-item" data-index="0" id="destination-0"></div>
    </div>`;
  const scroller = document.querySelector(".scroll-wrapper");
  scroller.getBoundingClientRect = () => rect(0, 900, 0, 400);
  Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 10_000 });
  Object.defineProperty(scroller, "clientHeight", { configurable: true, value: 900 });
  scroller.scrollTop = 400;
  document.querySelector("#item-0").getBoundingClientRect = () => rects[0];
  document.querySelector("#item-1").getBoundingClientRect = () => rects[1];
  document.querySelector("#item-2").getBoundingClientRect = () => rects[2];
  document.querySelector("#destination-0").getBoundingClientRect = () => rect(10, 60);
  return scroller;
}

const items = [{ path: "/a.jpg" }, { path: "/b.jpg" }, { path: "/c.jpg" }];

describe("listing viewport capture", () => {
  beforeEach(() => clearListAnchor());

  it("reads only the main listing root and keeps partially visible cards", () => {
    const scroller = mountListing([rect(-40, 60), rect(100, 300), rect(950, 1200)]);
    const entries = readVisibleListingEntries(scroller, items, "pictures");
    expect(entries.map((entry) => entry.index)).toEqual([0, 1]);
    expect(entries[0]).toMatchObject({ key: "pictures|/a.jpg", top: -40 });
  });

  it("picks the topmost surviving card and excludes only confirmed removals", () => {
    const scroller = mountListing([rect(-40, 60), rect(100, 300), rect(950, 1200)]);
    const anchor = captureListingAnchor({
      reason: "move",
      source: "pictures",
      path: "/",
      removed: [{ source: "pictures", path: "/a.jpg" }],
      items,
      type: "directory",
      scroller,
      lifecycleId: 7,
    });
    expect(anchor).toMatchObject({
      reason: "move",
      anchorKey: "pictures|/b.jpg",
      anchorKind: "identity",
      offsetTop: 100,
      fallbackScrollTop: 400,
      lifecycleId: 7,
    });
    expect(anchor.candidates).toEqual(["pictures|/c.jpg"]);
    expect(peekListAnchor()?.ticket).toBeTruthy();
  });

  it("treats an unmatched failed result as a surviving anchor", () => {
    const scroller = mountListing([rect(-40, 60), rect(100, 300), rect(950, 1200)]);
    const anchor = captureListingAnchor({
      reason: "move",
      source: "pictures",
      path: "/",
      removed: [
        { source: "pictures", path: "/a.jpg" },
        { source: "other", path: "/b.jpg" },
        { source: "pictures", path: "/sub/c.jpg" },
      ],
      items,
      type: "directory",
      scroller,
      lifecycleId: 7,
    });
    // /a.jpg only: the other entries are outside this source/directory.
    expect(anchor.anchorKey).toBe("pictures|/b.jpg");
  });

  it("uses the next off-screen survivor when every visible card is removed", () => {
    const scroller = mountListing([rect(-40, 60), rect(100, 300), rect(950, 1200)]);
    const anchor = captureListingAnchor({
      reason: "move",
      source: "pictures",
      path: "/",
      removed: [
        { source: "pictures", path: "/a.jpg" },
        { source: "pictures", path: "/b.jpg" },
      ],
      items,
      type: "directory",
      scroller,
      lifecycleId: 7,
    });
    expect(anchor.anchorKey).toBe("pictures|/c.jpg");
    expect(anchor.anchorKind).toBe("neighbor_after");
    expect(anchor.offsetTop).toBe(0);
  });

  it("keeps a pixel fallback ticket when the directory becomes empty", () => {
    const scroller = mountListing([rect(-40, 60), rect(100, 300), rect(950, 1200)]);
    const anchor = captureListingAnchor({
      reason: "move",
      source: "pictures",
      path: "/",
      removed: items.map((item) => ({ source: "pictures", path: item.path })),
      items,
      type: "directory",
      scroller,
      lifecycleId: 7,
    });
    expect(anchor.anchorKey).toBeNull();
    expect(anchor.fallbackScrollTop).toBe(400);
  });

  it("captures a same-directory refresh without pretending anything was removed", () => {
    const scroller = mountListing([rect(-40, 60), rect(100, 300), rect(950, 1200)]);
    const anchor = captureListingAnchor({
      reason: "refresh", source: "pictures", path: "/", removed: [],
      items, type: "directory", scroller, lifecycleId: 7,
    });
    expect(anchor).toMatchObject({
      reason: "refresh",
      anchorKey: "pictures|/a.jpg",
      offsetTop: -40,
      fallbackScrollTop: 400,
    });
  });

  it("does not capture a ticket outside a directory listing", () => {
    const scroller = mountListing([rect(-40, 60), rect(100, 300), rect(950, 1200)]);
    expect(captureListingAnchor({
      reason: "move", source: "pictures", path: "/", items, type: "image", scroller, lifecycleId: 7,
    })).toBeNull();
    expect(captureListingAnchor({
      reason: "copy", source: "pictures", path: "/", items, type: "directory", scroller, lifecycleId: 7,
    })).toBeNull();
    expect(peekListAnchor()).toBeNull();
  });
});
