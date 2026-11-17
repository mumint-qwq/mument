import { beforeEach, describe, expect, it, vi } from "vitest";

const { api, storeState, shareMode } = vi.hoisted(() => ({
  api: {
    getItems: vi.fn(),
    getItemsPublic: vi.fn(),
    fetchFiles: vi.fn(),
    fetchFilesPublic: vi.fn(),
  },
  storeState: {
    shareInfo: { hash: "active-share" },
    req: { source: "pictures", path: "/" },
    sources: { info: { pictures: {} } },
  },
  shareMode: { value: false },
}));

vi.mock("@/api", () => ({ resourcesApi: api }));
vi.mock("@/store", () => ({
  state: storeState,
  mutations: {},
  getters: {
    isShare: () => shareMode.value,
    sorting: () => ({ by: "name", asc: true }),
  },
}));
vi.mock("@/notify", () => ({ notify: { showError: vi.fn() } }));

import FileList from "./FileList.vue";

describe("FileList folder-only browsing", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    shareMode.value = false;
  });

  it("uses the narrow authenticated items endpoint", async () => {
    api.getItems.mockResolvedValue({ folders: ["Trips"] });
    const result = await FileList.methods.fetchBrowseListing.call(
      { browseShare: null, showFiles: false },
      "pictures",
      "/Camera/",
    );

    expect(api.getItems).toHaveBeenCalledWith("pictures", "/Camera/", "folders");
    expect(api.fetchFiles).not.toHaveBeenCalled();
    expect(result.items).toEqual([
      { name: "Trips", path: "/Camera/Trips/", source: "pictures", type: "directory" },
    ]);
  });

  it("uses the narrow public-share items endpoint", async () => {
    api.getItemsPublic.mockResolvedValue({ folders: ["Shared"] });
    const result = await FileList.methods.fetchBrowseListing.call(
      { browseShare: "explicit-share", showFiles: false },
      null,
      "/",
    );

    expect(api.getItemsPublic).toHaveBeenCalledWith("explicit-share", "/", "folders");
    expect(api.fetchFilesPublic).not.toHaveBeenCalled();
    expect(result.items[0].path).toBe("/Shared/");
    expect(result.items[0].source).toBe("explicit-share");
  });
});
