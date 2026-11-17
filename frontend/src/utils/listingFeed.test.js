import { describe, expect, it, vi } from "vitest";
import { createListingFeed } from "./listingFeed.js";

const page = (items, overrides = {}) => ({
  items, revision: "rev-1", hasMore: false, nextCursor: "", filtered: 0,
  ...overrides,
});

describe("listing feed", () => {
  it("loads the first page and appends subsequent pages", async () => {
    const fetchPage = vi.fn()
      .mockResolvedValueOnce(page([{ path: "/a" }, { path: "/b" }], { hasMore: true, nextCursor: "c1" }))
      .mockResolvedValueOnce(page([{ path: "/c" }], { hasMore: false }));
    const feed = createListingFeed(fetchPage, { limit: 2 });

    await feed.reset({ source: "s", path: "/" });
    expect(feed.state.items.map((item) => item.path)).toEqual(["/a", "/b"]);
    expect(feed.state.hasMore).toBe(true);
    expect(fetchPage.mock.calls[0][0]).toMatchObject({ cursor: "", revision: "" });

    await feed.loadMore();
    expect(feed.state.items.map((item) => item.path)).toEqual(["/a", "/b", "/c"]);
    expect(feed.state.hasMore).toBe(false);
    expect(fetchPage.mock.calls[1][0]).toMatchObject({ cursor: "c1", revision: "rev-1" });
  });

  it("does not treat a short page as completion when hasMore is set", async () => {
    const fetchPage = vi.fn().mockResolvedValue(page([{ path: "/only" }], { hasMore: true, nextCursor: "next" }));
    const feed = createListingFeed(fetchPage);
    await feed.reset({ source: "s", path: "/" });
    expect(feed.state.hasMore).toBe(true);
  });

  it("ignores a late response after the listing was reset", async () => {
    let resolveFirst;
    const first = new Promise((resolve) => { resolveFirst = resolve; });
    const fetchPage = vi.fn()
      .mockReturnValueOnce(first)
      .mockResolvedValueOnce(page([{ path: "/new" }]));
    const feed = createListingFeed(fetchPage);

    const pending = feed.reset({ source: "s", path: "/old" });
    await feed.reset({ source: "s", path: "/new" });
    resolveFirst(page([{ path: "/old" }]));
    await pending;

    expect(feed.state.items.map((item) => item.path)).toEqual(["/new"]);
    expect(feed.state.path).toBe("/new");
  });

  it("accumulates the filtered count reported by the server", async () => {
    const fetchPage = vi.fn().mockResolvedValue(page([], { filtered: 3, hasMore: false }));
    const feed = createListingFeed(fetchPage);
    await feed.reset({ source: "s", path: "/" });
    expect(feed.state.filtered).toBe(3);
  });
});
