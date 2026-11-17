import { describe, expect, it } from "vitest";
import { folderItemsFromResponse, folderListingFromResponse } from "./directoryItems.js";

describe("folder-only resource responses", () => {
  it("builds stable root directory items", () => {
    expect(folderItemsFromResponse({ folders: ["Camera", "旅行"] }, "pictures", "/")).toEqual([
      { name: "Camera", path: "/Camera/", source: "pictures", type: "directory" },
      { name: "旅行", path: "/旅行/", source: "pictures", type: "directory" },
    ]);
  });

  it("builds nested paths without duplicate separators", () => {
    expect(folderListingFromResponse({ folders: ["2026"] }, "pictures", "/Camera/")).toEqual({
      path: "/Camera/",
      source: "pictures",
      type: "directory",
      items: [{ name: "2026", path: "/Camera/2026/", source: "pictures", type: "directory" }],
    });
  });

  it("treats an omitted folders field as an empty listing", () => {
    expect(folderItemsFromResponse({}, "pictures", "/")).toEqual([]);
  });
});
