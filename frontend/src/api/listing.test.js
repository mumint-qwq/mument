import { describe, expect, it, vi } from "vitest";

vi.mock("@/utils/url", () => ({
  getApiPath: (path, params) => `/api/${path}?${new URLSearchParams(params).toString()}`,
  getPublicApiPath: (path, params) => `/public/api/${path}?${new URLSearchParams(params).toString()}`,
}));

import { fetchListingPage, ListingRevisionError, ListingUnsupportedError } from "./listing";

const response = (body) => ({ json: async () => body });

describe("fetchListingPage", () => {
  it("sends the cursor, revision and limit", async () => {
    const fetchImpl = vi.fn().mockResolvedValue(response({ items: [], revision: "r2", hasMore: true }));
    await fetchListingPage({ source: "s", path: "/", cursor: "c1", revision: "r1", limit: 50 }, { fetchImpl });
    const url = fetchImpl.mock.calls[0][0];
    expect(url).toContain("resources/list");
    expect(url).toContain("cursor=c1");
    expect(url).toContain("revision=r1");
    expect(url).toContain("limit=50");
  });

  it("maps a missing endpoint to ListingUnsupportedError", async () => {
    const missing = Object.assign(new Error("not found"), { status: 404 });
    const fetchImpl = async () => { throw missing; };
    const rejected = await fetchListingPage({ source: "s", path: "/" }, { fetchImpl }).catch((error) => error);
    expect(rejected).toBeInstanceOf(ListingUnsupportedError);
  });

  it("maps a conflict to ListingRevisionError", async () => {
    const conflict = Object.assign(new Error("conflict"), { status: 409 });
    const fetchImpl = async () => { throw conflict; };
    const rejected = await fetchListingPage({ source: "s", path: "/" }, { fetchImpl }).catch((error) => error);
    expect(rejected).toBeInstanceOf(ListingRevisionError);
  });

  it("uses the public endpoint and share password for shares", async () => {
    const fetchImpl = vi.fn().mockResolvedValue(response({ items: [], revision: "r", hasMore: false }));
    await fetchListingPage({ isShare: true, hash: "abc", password: "pw", path: "/" }, { fetchImpl });
    const [url, options] = fetchImpl.mock.calls[0];
    expect(url.startsWith("/public/api/resources/list")).toBe(true);
    expect(options.headers["X-SHARE-PASSWORD"]).toBe("pw");
  });
});
