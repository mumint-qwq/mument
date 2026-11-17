import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  chooseListingAnchor,
  clearListAnchor,
  isEntryInDirectory,
  listingItemKey,
  normalizeListingPath,
  peekListAnchor,
  rememberListAnchor,
  removedListingKeys,
  takeListAnchor,
} from "./listAnchor.js";

const items = [
  { path: "/a.jpg" },
  { path: "/b.jpg" },
  { path: "/c.jpg" },
  { path: "/d.jpg" },
];

const keys = items.map((item) => listingItemKey(item, "pictures"));

describe("stable identity", () => {
  it("uses source plus path, never the index or the bare name", () => {
    expect(listingItemKey({ source: "pictures", path: "/a b#%.jpg" })).toBe("pictures|/a b#%.jpg");
    expect(listingItemKey({ path: "/a.jpg" }, "pictures")).toBe("pictures|/a.jpg");
    expect(listingItemKey({ path: "/a.jpg" }, "other")).not.toBe(listingItemKey({ path: "/a.jpg" }, "pictures"));
    expect(listingItemKey({ name: "a.jpg" }, "pictures")).toBe("pictures|a.jpg");
  });

  it("normalizes trailing slashes for directory comparison only", () => {
    expect(normalizeListingPath("/sub/")).toBe("/sub");
    expect(normalizeListingPath("/")).toBe("/");
    expect(normalizeListingPath("")).toBe("/");
  });

  it("matches only confirmed results from this source and directory", () => {
    expect(isEntryInDirectory({ source: "pictures", path: "/a.jpg" }, "pictures", "/")).toBe(true);
    expect(isEntryInDirectory({ source: "pictures", path: "/sub/a.jpg" }, "pictures", "/sub/")).toBe(true);
    expect(isEntryInDirectory({ source: "pictures", path: "/sub/a.jpg" }, "pictures", "/")).toBe(false);
    expect(isEntryInDirectory({ source: "other", path: "/a.jpg" }, "pictures", "/")).toBe(false);
    const removed = removedListingKeys([
      { source: "pictures", path: "/a.jpg" },
      { source: "other", path: "/b.jpg" },
      { source: "pictures", path: "/sub/c.jpg" },
    ], "pictures", "/");
    expect([...removed]).toEqual(["pictures|/a.jpg"]);
  });
});

describe("chooseListingAnchor", () => {
  const visible = [
    { index: 1, key: keys[1], top: 10, left: 0 },
    { index: 2, key: keys[2], top: 10, left: 120 },
    { index: 3, key: keys[3], top: 220, left: 0 },
  ];

  it("picks the topmost surviving visible card and keeps a negative offset", () => {
    const negative = [
      { index: 0, key: keys[0], top: -30, left: 0 },
      { index: 1, key: keys[1], top: 80, left: 0 },
    ];
    const choice = chooseListingAnchor(negative, keys, new Set());
    expect(choice.key).toBe(keys[0]);
    expect(choice.offsetTop).toBe(-30);
    expect(choice.kind).toBe("identity");
  });

  it("skips every confirmed moved card, including the whole first row", () => {
    const choice = chooseListingAnchor(visible, keys, new Set([keys[1], keys[2]]));
    expect(choice.key).toBe(keys[3]);
    expect(choice.offsetTop).toBe(220);
  });

  it("uses the next survivor after the visible block when all visible cards moved", () => {
    const choice = chooseListingAnchor(visible, keys, new Set([keys[1], keys[2], keys[3]]));
    expect(choice.key).toBe(keys[0]);
    expect(choice.kind).toBe("neighbor_before");
    const allVisibleGone = [
      { index: 1, key: keys[1], top: 0, left: 0 },
      { index: 2, key: keys[2], top: 10, left: 0 },
    ];
    const neighbor = chooseListingAnchor(allVisibleGone, [...keys, "pictures|/e.jpg"], new Set([keys[1], keys[2]]));
    expect(neighbor.key).toBe(keys[3]);
    expect(neighbor.offsetTop).toBe(0);
    expect(neighbor.kind).toBe("neighbor_after");
  });

  it("falls back to the previous survivor when nothing follows", () => {
    const tail = [
      { index: 2, key: keys[2], top: 0, left: 0 },
      { index: 3, key: keys[3], top: 20, left: 0 },
    ];
    const choice = chooseListingAnchor(tail, keys, new Set([keys[2], keys[3]]));
    expect(choice.key).toBe(keys[1]);
    expect(choice.kind).toBe("neighbor_before");
    expect(choice.offsetTop).toBe(0);
  });

  it("returns null when every item moved out (empty directory)", () => {
    const choice = chooseListingAnchor(visible, keys, new Set(keys));
    expect(choice).toBeNull();
  });

  it("keeps a bounded set of backups that are not removed", () => {
    const many = Array.from({ length: 20 }, (_, index) => `pictures|/${index}.jpg`);
    const choice = chooseListingAnchor([{ index: 0, key: many[0], top: 0, left: 0 }], many, new Set([many[1], many[2]]));
    expect(choice.backups.length).toBe(5);
    expect(choice.backups).not.toContain(many[1]);
    expect(choice.backups).not.toContain(many[2]);
  });
});

describe("pending list anchor mailbox", () => {
  const anchor = { reason: "move", source: "pictures", path: "/", shareHash: "", anchorKey: keys[1], offsetTop: 12, fallbackScrollTop: 400, lifecycleId: 3 };

  beforeEach(() => { clearListAnchor(); vi.useFakeTimers(); });
  afterEach(() => { clearListAnchor(); vi.useRealTimers(); });

  it("is claimed exactly once by the matching lifecycle and directory", () => {
    rememberListAnchor(anchor);
    expect(takeListAnchor({ source: "pictures", path: "/", shareHash: "", lifecycleId: 3 })).toMatchObject({ anchorKey: keys[1], ticket: "a1" });
    expect(takeListAnchor({ source: "pictures", path: "/", shareHash: "", lifecycleId: 3 })).toBeNull();
  });

  it("does not let another directory consume the ticket", () => {
    rememberListAnchor(anchor);
    expect(takeListAnchor({ source: "pictures", path: "/sub/", shareHash: "", lifecycleId: 3 })).toBeNull();
    expect(takeListAnchor({ source: "pictures", path: "/", shareHash: "", lifecycleId: 3 })).not.toBeNull();
  });

  it("expires after the TTL when nobody claimed it", () => {
    rememberListAnchor(anchor);
    vi.advanceTimersByTime(11_000);
    expect(takeListAnchor({ source: "pictures", path: "/", shareHash: "", lifecycleId: 3 })).toBeNull();
    expect(peekListAnchor()).toBeNull();
  });

  it("invalidates an old lifecycle ticket after A -> B -> A", () => {
    rememberListAnchor(anchor);
    expect(takeListAnchor({ source: "pictures", path: "/", shareHash: "", lifecycleId: 4 })).toBeNull();
    expect(peekListAnchor()).toBeNull();
  });

  it("treats a share hash as part of the directory identity", () => {
    rememberListAnchor({ ...anchor, shareHash: "abc" });
    expect(takeListAnchor({ source: "pictures", path: "/", shareHash: "other", lifecycleId: 3 })).toBeNull();
    expect(takeListAnchor({ source: "pictures", path: "/", shareHash: "abc", lifecycleId: 3 })).not.toBeNull();
  });

  it("only clears the ticket owned by the caller", () => {
    const first = rememberListAnchor(anchor);
    const second = rememberListAnchor(anchor);
    expect(first.ticket).not.toBe(second.ticket);
    clearListAnchor(first.ticket);
    expect(peekListAnchor()?.ticket).toBe(second.ticket);
    clearListAnchor(second.ticket);
    expect(peekListAnchor()).toBeNull();
  });
});
