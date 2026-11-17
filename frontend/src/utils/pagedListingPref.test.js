import { afterEach, describe, expect, it, vi } from "vitest";
import { isPagedListingEnabled } from "./pagedListingPref.js";

describe("paged listing flag", () => {
  afterEach(() => {
    localStorage.clear();
    vi.unstubAllGlobals();
  });

  it("is disabled by default", () => {
    expect(isPagedListingEnabled()).toBe(false);
  });

  it("enables from localStorage", () => {
    localStorage.setItem("quantum.pagedListing", "1");
    expect(isPagedListingEnabled()).toBe(true);
  });

  it("enables from ?pagedListing=1 and persists it", () => {
    vi.stubGlobal("location", { search: "?pagedListing=1" });
    expect(isPagedListingEnabled()).toBe(true);
    vi.unstubAllGlobals();
    expect(localStorage.getItem("quantum.pagedListing")).toBe("1");
  });

  it("disables from ?pagedListing=0", () => {
    localStorage.setItem("quantum.pagedListing", "1");
    vi.stubGlobal("location", { search: "?pagedListing=0" });
    expect(isPagedListingEnabled()).toBe(false);
    vi.unstubAllGlobals();
    expect(localStorage.getItem("quantum.pagedListing")).toBeNull();
  });
});
