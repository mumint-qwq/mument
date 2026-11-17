/* eslint-disable security/detect-object-injection -- the test indexes fixture objects by user/folder name on purpose. */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// The shared setup mocks the store; this suite exercises the real mutations,
// getters, localStorage merge and usersApi wiring.
vi.unmock("@/store");

vi.mock("@/api", () => ({
  resourcesApi: { fetchFiles: vi.fn(), fetchFilesPublic: vi.fn() },
  usersApi: { update: vi.fn(), get: vi.fn() },
}));
vi.mock("@/api/settings", () => ({
  getEnforcedUserDefaults: vi.fn(async () => ({ enforced: {} })),
}));
vi.mock("@/notify", () => ({
  notify: {
    showError: vi.fn(),
    showSuccessToast: vi.fn(),
    showErrorToast: vi.fn(),
  },
  events: { startSSE: vi.fn() },
}));
vi.mock("@/i18n", () => ({
  detectLocale: () => "en",
  setLocale: vi.fn(async () => {}),
  default: { global: { t: (key) => key } },
}));

import { usersApi } from "@/api";
import { setLocale } from "@/i18n";
import { notify } from "@/notify";
import { state, getters, mutations } from "@/store";
import { folderPreferenceTarget } from "@/utils/folderDisplayPreferences.js";

const USERNAME = "alice";
const ENCODED = btoa(unescape(encodeURIComponent(USERNAME)));

function serverUser(overrides = {}) {
  return {
    username: USERNAME,
    scopes: [{ name: "Pictures" }, { name: "EHP" }],
    permissions: {},
    singleClick: false,
    viewMode: "gallery",
    sorting: { by: "size", asc: false },
    gallerySize: 4,
    locale: "en",
    ...overrides,
  };
}

async function login(overrides = {}) {
  await mutations.setCurrentUser(serverUser(overrides));
  state.sources.info = { Pictures: {}, EHP: {} };
  state.sources.current = "Pictures";
  state.sources.count = 2;
  state.shareInfo = { ...state.shareInfo, isShare: false, hash: "", viewMode: "" };
  useFolder("Pictures", "/中文 目录");
}

function useFolder(source, path) {
  const encoded = path.split("/").filter(Boolean).map((part) => encodeURIComponent(part)).join("/");
  state.req = { type: "directory", path, source, items: [] };
  state.route = {
    path: `/files/${encodeURIComponent(source)}${encoded ? `/${encoded}` : "/"}`,
    query: {},
    params: {},
    hash: "",
  };
}

function usePreview(source, path) {
  const encoded = path.split("/").filter(Boolean).map((part) => encodeURIComponent(part)).join("/");
  state.req = { type: "image/png", path, source };
  state.route = { path: `/files/${encodeURIComponent(source)}/${encoded}`, query: {}, params: {}, hash: "" };
}

function target(source = "Pictures", path = "/中文 目录") {
  return folderPreferenceTarget({ source, path });
}

function storedAccount(username = USERNAME) {
  const all = JSON.parse(localStorage.getItem("displayPreferences") || "{}");
  return all[username] || {};
}

function seedAccount(tree) {
  localStorage.setItem("displayPreferences", JSON.stringify(tree));
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

beforeEach(async () => {
  localStorage.clear();
  vi.clearAllMocks();
  usersApi.update.mockReset();
  usersApi.update.mockResolvedValue(undefined);
  usersApi.get.mockReset();
  vi.mocked(setLocale).mockReset();
  vi.mocked(setLocale).mockResolvedValue(undefined);
  state.displayPreferences = {};
  state.listingContextOverride = { key: "", username: "", viewMode: "", sorting: null };
  state.accountDefaultsSave = { viewMode: false, sorting: false, error: "" };
  state.enforcedUserDefaults = {};
  state.selected = [];
  state.selectedKeys = [];
  await mutations.setCurrentUser(null);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("login keeps account defaults and legacy keys", () => {
  it("does not let ViewMode_* override the server viewMode", async () => {
    localStorage.setItem(`ViewMode_${ENCODED}`, "mosaic");
    localStorage.setItem(`GallerySize_${ENCODED}`, "7");
    localStorage.setItem("quantumDeviceId", "abcdefgh-1234");
    await login();
    expect(getters.viewMode()).toBe("gallery");
    expect(state.user.viewMode).toBe("gallery");
    expect(state.user.gallerySize).toBe(7);
    expect(getters.sorting()).toEqual({ by: "size", asc: false });
    expect(state.user.sorting).toEqual({ by: "size", asc: false });
    expect(localStorage.getItem("quantumDeviceId")).toBe("abcdefgh-1234");
    expect(usersApi.update).not.toHaveBeenCalled();
  });

  it("replaces an invalid server sorting with an in-memory fallback only", async () => {
    await login({ sorting: { by: "not-a-field", asc: true }, viewMode: "grid" });
    expect(state.user.sorting).toEqual({ by: "name", asc: true });
    expect(state.user.viewMode).toBe("normal");
    expect(localStorage.getItem("displayPreferences")).toBeNull();
    expect(usersApi.update).not.toHaveBeenCalled();
  });

  it("clears the previous account's folder memory on logout", async () => {
    await login();
    mutations.setCurrentListingViewMode("mosaic");
    expect(getters.hasFolderViewPreference()).toBe(true);
    await mutations.setCurrentUser(null);
    expect(state.displayPreferences).toEqual({});
    expect(state.listingContextOverride).toEqual({
      key: "",
      username: "",
      viewMode: "",
      sorting: null,
    });
  });
});

describe("effective resolution", () => {
  const cases = [
    [{}, "gallery", { by: "size", asc: false }],
    [{ viewMode: "mosaic" }, "mosaic", { by: "size", asc: false }],
    [{ sorting: { by: "modified", asc: false } }, "gallery", { by: "modified", asc: false }],
    [
      { viewMode: "mosaic", sorting: { by: "modified", asc: false } },
      "mosaic",
      { by: "modified", asc: false },
    ],
  ];

  for (const [folder, viewMode, sorting] of cases) {
    it(`resolves ${JSON.stringify(folder)} independently`, async () => {
      await login();
      const t = target();
      state.displayPreferences = { Pictures: { [t.folderKey]: folder } };
      expect(getters.viewMode()).toBe(viewMode);
      expect(getters.sorting()).toEqual(sorting);
    });
  }

  it("keeps an explicit value equal to the default after the default changes", async () => {
    await login();
    const t = target();
    state.displayPreferences = { Pictures: { [t.folderKey]: { viewMode: "gallery" } } };
    state.user.viewMode = "mosaic";
    expect(getters.viewMode()).toBe("gallery");
  });

  it("falls back only the invalid field", async () => {
    await login();
    const t = target();
    state.displayPreferences = {
      Pictures: {
        [t.folderKey]: { viewMode: "grid", sorting: { by: "name", asc: true } },
      },
    };
    expect(getters.viewMode()).toBe("gallery");
    expect(getters.sorting()).toEqual({ by: "name", asc: true });
  });

  it("first listing load sorts by the target folder record, not the account", async () => {
    await login();
    const t = target("Pictures", "/target");
    state.displayPreferences = { Pictures: { [t.folderKey]: { sorting: { by: "name", asc: true } } } };
    // Previous folder is still installed while the response arrives.
    state.req = { type: "directory", path: "/old", source: "Pictures", items: [] };
    state.route = { path: "/files/Pictures/target", query: {}, params: {}, hash: "" };
    const items = [
      { name: "b.txt", path: "/target/b.txt", source: "Pictures", type: "text/plain", size: 2 },
      { name: "a.txt", path: "/target/a.txt", source: "Pictures", type: "text/plain", size: 1 },
    ];
    mutations.replaceRequest({ type: "directory", path: "/target", source: "Pictures", name: "target", items });
    expect(state.req.items.map((item) => item.name)).toEqual(["a.txt", "b.txt"]);
    expect(state.user.sorting).toEqual({ by: "size", asc: false });
  });

  it("resolves a paged target folder before state.req switches", async () => {
    await login();
    const t = target("Pictures", "/target");
    state.displayPreferences = { Pictures: { [t.folderKey]: { sorting: { by: "name", asc: true } } } };
    useFolder("Pictures", "/other");
    expect(getters.sortingForTarget("Pictures", "/target")).toEqual({ by: "name", asc: true });
    expect(getters.sortingForTarget("Pictures", "/no-record")).toEqual({ by: "size", asc: false });
  });

  it("reads a historical trailing-slash record in a preview and writes to the same key", async () => {
    await login();
    seedAccount({
      [USERNAME]: {
        Pictures: {
          "/files/Pictures/%E4%B8%AD%E6%96%87%20%E7%9B%AE%E5%BD%95/": {
            sorting: { by: "duration", asc: false },
          },
        },
      },
    });
    await mutations.setCurrentUser(serverUser());
    usePreview("Pictures", "/中文 目录/a.jpg");
    expect(getters.sorting()).toEqual({ by: "duration", asc: false });
    expect(mutations.updateDisplayPreferences({ viewMode: "mosaic" })).toBe(false);
    expect(storedAccount().Pictures["/files/Pictures/%E4%B8%AD%E6%96%87%20%E7%9B%AE%E5%BD%95/"].viewMode).toBeUndefined();
  });
});

describe("folder writes never touch the account default", () => {
  it("writes view and sorting separately for the current folder only", async () => {
    await login();
    expect(mutations.setCurrentListingViewMode("mosaic")).toBe(true);
    expect(state.user.viewMode).toBe("gallery");
    expect(usersApi.update).not.toHaveBeenCalled();

    expect(mutations.updateListingSortConfig({ field: "modified", asc: false })).toBe(true);
    expect(state.user.sorting).toEqual({ by: "size", asc: false });
    expect(usersApi.update).not.toHaveBeenCalled();

    const account = storedAccount();
    const first = account.Pictures[target().folderKey];
    expect(first).toEqual({ viewMode: "mosaic", sorting: { by: "modified", asc: false } });

    useFolder("Pictures", "/other");
    expect(getters.viewMode()).toBe("gallery");
    expect(getters.sorting()).toEqual({ by: "size", asc: false });
    mutations.setCurrentListingViewMode("list");
    expect(storedAccount().Pictures["/files/Pictures/other"]).toEqual({ viewMode: "list" });
    expect(storedAccount().Pictures[target().folderKey].sorting).toEqual({ by: "modified", asc: false });
  });

  it("keeps asc=false and duration exactly", async () => {
    await login();
    mutations.updateListingSortConfig({ field: "duration", asc: false });
    expect(getters.sorting()).toEqual({ by: "duration", asc: false });
    expect(storedAccount().Pictures[target().folderKey].sorting).toEqual({ by: "duration", asc: false });
  });

  it("does not write on login, refresh or sorting initialization", async () => {
    const seed = {
      [USERNAME]: { Pictures: { "/files/Pictures/existing": { viewMode: "list" } } },
      bob: { EHP: { "/files/EHP/b": { sorting: { by: "name", asc: true } } } },
    };
    seedAccount(seed);
    const before = localStorage.getItem("displayPreferences");
    await login();
    expect(localStorage.getItem("displayPreferences")).toBe(before);
    expect(state.displayPreferences).toEqual(seed[USERNAME].Pictures ? { Pictures: seed[USERNAME].Pictures } : {});
    expect(usersApi.update).not.toHaveBeenCalled();
  });

  it("merges the latest stored tree instead of dropping other tabs/accounts", async () => {
    await login();
    seedAccount({
      bob: { EHP: { "/files/EHP/b": { viewMode: "list" } } },
      [USERNAME]: { EHP: { "/files/EHP/a": { sorting: { by: "size", asc: true } } } },
    });
    // The in-memory alice subtree is older than storage here; the write must
    // still preserve bob and alice/EHP.
    state.displayPreferences = {};
    mutations.setCurrentListingViewMode("mosaic");
    const all = JSON.parse(localStorage.getItem("displayPreferences"));
    expect(all.bob.EHP["/files/EHP/b"].viewMode).toBe("list");
    expect(all[USERNAME].EHP["/files/EHP/a"].sorting).toEqual({ by: "size", asc: true });
    expect(all[USERNAME].Pictures[target().folderKey].viewMode).toBe("mosaic");
  });

  it("reuses an existing trailing-slash key and root identity", async () => {
    await login();
    seedAccount({
      [USERNAME]: { Pictures: { "/files/Pictures/legacy/": { viewMode: "mosaic" } } },
    });
    await mutations.setCurrentUser(serverUser());
    useFolder("Pictures", "/legacy");
    expect(getters.viewMode()).toBe("mosaic");
    mutations.updateListingSortConfig({ field: "size", asc: true });
    const pictures = storedAccount().Pictures;
    expect(Object.keys(pictures)).toEqual(["/files/Pictures/legacy/"]);
    expect(pictures["/files/Pictures/legacy/"]).toEqual({
      viewMode: "mosaic",
      sorting: { by: "size", asc: true },
    });

    useFolder("Pictures", "/");
    mutations.setCurrentListingViewMode("list");
    expect(storedAccount().Pictures["/files/Pictures"]).toEqual({ viewMode: "list" });
    expect(storedAccount().Pictures["/files/Pictures/legacy/"].viewMode).toBe("mosaic");
  });

  it("isolates accounts, sources and same-named folders", async () => {
    await login();
    mutations.setCurrentListingViewMode("mosaic");
    mutations.updateListingSortConfig({ field: "modified", asc: true });
    useFolder("EHP", "/中文 目录");
    expect(getters.viewMode()).toBe("gallery");
    mutations.setCurrentListingViewMode("list");

    const pictures = storedAccount().Pictures;
    const ehp = storedAccount().EHP;
    expect(pictures[target("Pictures").folderKey].viewMode).toBe("mosaic");
    expect(ehp[target("EHP").folderKey].viewMode).toBe("list");

    await login({ username: "bob", sorting: { by: "name", asc: true }, viewMode: "list" });
    useFolder("Pictures", "/中文 目录");
    expect(getters.viewMode()).toBe("list");
    expect(getters.sorting()).toEqual({ by: "name", asc: true });
  });
});

describe("reset and availability", () => {
  it("resets one field and leaves the other plus other folders", async () => {
    await login();
    mutations.setCurrentListingViewMode("mosaic");
    mutations.updateListingSortConfig({ field: "modified", asc: false });
    mutations.setCurrentListingViewMode("compact");
    useFolder("Pictures", "/other");
    mutations.updateListingSortConfig({ field: "size", asc: true });

    useFolder("Pictures", "/中文 目录");
    expect(getters.hasFolderViewPreference()).toBe(true);
    expect(getters.hasFolderSortingPreference()).toBe(true);
    expect(mutations.resetFolderViewMode()).toBe(true);
    expect(getters.hasFolderViewPreference()).toBe(false);
    expect(getters.hasFolderSortingPreference()).toBe(true);
    expect(getters.viewMode()).toBe("gallery");
    expect(getters.sorting()).toEqual({ by: "modified", asc: false });
    expect(storedAccount().Pictures[target().folderKey]).toEqual({
      sorting: { by: "modified", asc: false },
    });
    expect(storedAccount().Pictures["/files/Pictures/other"]).toEqual({ sorting: { by: "size", asc: true } });

    expect(mutations.resetFolderSorting()).toBe(true);
    expect(storedAccount().Pictures[target().folderKey]).toBeUndefined();
    expect(getters.sorting()).toEqual({ by: "size", asc: false });
    expect(storedAccount().Pictures["/files/Pictures/other"].sorting).toEqual({ by: "size", asc: true });
  });

  it("does not offer reset in a preview or on another unconfirmed route", async () => {
    await login();
    mutations.setCurrentListingViewMode("mosaic");
    usePreview("Pictures", "/中文 目录/a.jpg");
    expect(getters.hasFolderViewPreference()).toBe(false);
    expect(mutations.resetFolderViewMode()).toBe(false);

    useFolder("Pictures", "/other");
    state.route = { ...state.route, path: "/files/Pictures/%E4%B8%AD%E6%96%87%20%E7%9B%AE%E5%BD%95" };
    expect(getters.folderDisplayContext()).toBeNull();
    expect(mutations.updateDisplayPreferences({ viewMode: "list" })).toBe(false);
    expect(storedAccount().Pictures["/files/Pictures/other"]).toBeUndefined();
  });
});

describe("account default saving", () => {
  it("PATCHes the real username with only the explicit field", async () => {
    await login();
    const ok = await mutations.saveAccountDisplayDefaults({ sorting: { by: "modified", asc: false } });
    expect(ok).toBe(true);
    expect(usersApi.update).toHaveBeenCalledTimes(1);
    const [targetUser, which] = usersApi.update.mock.calls[0];
    expect(which).toEqual(["sorting"]);
    expect(targetUser.username).toBe("alice");
    expect(targetUser.sorting).toEqual({ by: "modified", asc: false });
    expect(Object.hasOwn(targetUser, "viewMode")).toBe(false);
    expect(state.user.sorting).toEqual({ by: "modified", asc: false });
    expect(localStorage.getItem("displayPreferences")).toBeNull();
  });

  it("does not save a default for anonymous or share sessions", async () => {
    await login({ username: "anonymous", permissions: {} });
    expect(await mutations.saveAccountDisplayDefaults({ viewMode: "list" })).toBe(false);
    await login();
    state.shareInfo = { ...state.shareInfo, isShare: true, hash: "share123" };
    state.route = { ...state.route, path: "/public/share/share123/" };
    expect(await mutations.saveAccountDisplayDefaults({ viewMode: "list" })).toBe(false);
    expect(usersApi.update).not.toHaveBeenCalled();
  });

  it("rolls back only the failed field and settles once", async () => {
    await login();
    usersApi.update.mockRejectedValueOnce(new Error("offline"));
    const ok = await mutations.saveAccountDisplayDefaults({ sorting: { by: "modified", asc: false } });
    expect(ok).toBe(false);
    expect(state.user.sorting).toEqual({ by: "size", asc: false });
    expect(state.user.viewMode).toBe("gallery");
    expect(state.accountDefaultsSave.sorting).toBe(false);
    expect(state.accountDefaultsSave.error).toBe("offline");
    expect(notify.showError).toHaveBeenCalledTimes(1);
    expect(usersApi.update).toHaveBeenCalledTimes(1);
  });

  it("keeps the newest intent when an older response arrives late", async () => {
    await login();
    const gate = deferred();
    usersApi.update.mockImplementationOnce(() => gate.promise);
    const first = mutations.saveAccountDisplayDefaults({ sorting: { by: "modified", asc: false } });
    await Promise.resolve();
    const second = mutations.saveAccountDisplayDefaults({ sorting: { by: "duration", asc: true } });
    expect(state.user.sorting).toEqual({ by: "duration", asc: true });
    gate.resolve();
    await first;
    await second;
    expect(usersApi.update).toHaveBeenCalledTimes(2);
    expect(usersApi.update.mock.calls[0][0].sorting).toEqual({ by: "modified", asc: false });
    expect(usersApi.update.mock.calls[1][0].sorting).toEqual({ by: "duration", asc: true });
    expect(state.user.sorting).toEqual({ by: "duration", asc: true });
    expect(state.accountDefaultsSave.sorting).toBe(false);
  });

  it("ignores a late response after logout and does not pollute the next login", async () => {
    await login();
    const gate = deferred();
    usersApi.update.mockImplementationOnce(() => gate.promise);
    const saving = mutations.saveAccountDisplayDefaults({ viewMode: "mosaic" });
    await Promise.resolve();
    await mutations.setCurrentUser(null);
    gate.resolve();
    await saving;
    expect(state.user).toBeNull();
    expect(state.accountDefaultsSave).toEqual({ viewMode: false, sorting: false, error: "" });

    await login({ username: "bob", viewMode: "list", sorting: { by: "name", asc: true } });
    expect(state.user.username).toBe("bob");
    expect(state.user.viewMode).toBe("list");
    expect(state.user.sorting).toEqual({ by: "name", asc: true });
    expect(state.accountDefaultsSave.viewMode).toBe(false);
  });

  it("does not replay stale viewMode/sorting from a whole-profile draft", async () => {
    await login();
    const stale = JSON.parse(JSON.stringify(state.user));
    // Another tab/page already saved newer account defaults.
    state.user.viewMode = "normal";
    state.user.sorting = { by: "modified", asc: false };
    usersApi.update.mockClear();
    await mutations.updateCurrentUser({ ...stale, locale: "de" });
    expect(usersApi.update).toHaveBeenCalledTimes(1);
    expect(usersApi.update.mock.calls[0][1]).toEqual(["locale"]);
    expect(state.user.viewMode).toBe("normal");
    expect(state.user.sorting).toEqual({ by: "modified", asc: false });
  });
});

describe("storage failures stay page-only and honest", () => {
  it("keeps state usable when displayPreferences is corrupt and never overwrites it", async () => {
    localStorage.setItem("displayPreferences", "{broken json");
    localStorage.setItem("quantumDeviceId", "abcdefgh-1234");
    await login();
    expect(state.displayPreferences).toEqual({});
    mutations.setCurrentListingViewMode("mosaic");
    expect(getters.viewMode()).toBe("mosaic");
    expect(localStorage.getItem("displayPreferences")).toBe("{broken json");
    expect(notify.showError).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem("quantumDeviceId")).toBe("abcdefgh-1234");
  });

  it("reports a quota/write failure once but keeps the page choice", async () => {
    await login();
    const originalSetItem = Storage.prototype.setItem;
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(function setItem(key, value) {
      if (key === "displayPreferences") {
        throw new Error("quota");
      }
      return originalSetItem.call(this, key, value);
    });
    mutations.setCurrentListingViewMode("mosaic");
    expect(getters.viewMode()).toBe("mosaic");
    expect(notify.showError).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem("displayPreferences")).toBeNull();

    mutations.updateListingSortConfig({ field: "size", asc: true });
    expect(notify.showError).toHaveBeenCalledTimes(1);
    expect(getters.sorting()).toEqual({ by: "size", asc: true });
  });

  it("handles unreadable storage without throwing", async () => {
    await login();
    const originalGetItem = Storage.prototype.getItem;
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(function getItem(key) {
      if (key === "displayPreferences") {
        throw new Error("denied");
      }
      return originalGetItem.call(this, key);
    });
    expect(() => mutations.setCurrentListingViewMode("mosaic")).not.toThrow();
    expect(getters.viewMode()).toBe("mosaic");
    expect(notify.showError).toHaveBeenCalled();
  });
});

describe("non-folder contexts stay page-local", () => {
  it("keeps advanced-search view/sorting in memory and never on disk", async () => {
    await login();
    state.route = { path: "/tools/advancedSearch", query: {}, params: {}, hash: "" };
    state.req = { type: "directory", path: "/", source: "", items: [] };
    expect(getters.folderDisplayContext()).toBeNull();
    expect(mutations.setCurrentListingViewMode("mosaic")).toBe(true);
    expect(state.user.viewMode).toBe("gallery");
    expect(getters.viewMode()).toBe("mosaic");
    expect(mutations.updateListingSortConfig({ field: "duration", asc: false })).toBe(true);
    expect(state.user.sorting).toEqual({ by: "size", asc: false });
    expect(getters.sorting()).toEqual({ by: "duration", asc: false });
    expect(localStorage.getItem("displayPreferences")).toBeNull();

    useFolder("Pictures", "/中文 目录");
    expect(getters.viewMode()).toBe("gallery");
    expect(getters.sorting()).toEqual({ by: "size", asc: false });
  });
});
describe("review boundary regressions (R1/R2)", () => {
  it("keeps the true server default when storage is unreadable at login", async () => {
    const spy = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("storage denied");
    });
    try {
      await login({ viewMode: "gallery" });
      expect(state.user.viewMode).toBe("gallery");
      usersApi.update.mockRejectedValueOnce(new Error("network unavailable"));
      await mutations.saveAccountDisplayDefaults({ viewMode: "mosaic" });
      expect(state.user.viewMode).toBe("gallery");
    } finally {
      spy.mockRestore();
    }
  });

  it("keeps confirmed defaults when GallerySize persistence fails during login", async () => {
    const spy = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("quota exceeded");
    });
    try {
      await login({ viewMode: "gallery" });
      expect(state.user.viewMode).toBe("gallery");
      usersApi.update.mockRejectedValueOnce(new Error("network unavailable"));
      await mutations.saveAccountDisplayDefaults({ viewMode: "mosaic" });
      expect(state.user.viewMode).toBe("gallery");
    } finally {
      spy.mockRestore();
    }
  });

  it("does not leak the old account folder tree when storage fails on the next login", async () => {
    await login();
    mutations.setCurrentListingViewMode("mosaic");
    const spy = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("storage denied");
    });
    try {
      await mutations.setCurrentUser(serverUser({ username: "bob", viewMode: "normal" }));
      useFolder("Pictures", "/中文 目录");
      expect(state.user.username).toBe("bob");
      expect(state.displayPreferences).toEqual({});
      expect(getters.viewMode()).toBe("normal");
    } finally {
      spy.mockRestore();
    }
  });

  it("an unrelated failed save cannot roll back a newer successful display default", async () => {
    await login({ viewMode: "gallery" });
    const gate = deferred();
    usersApi.update.mockImplementationOnce(() => gate.promise);
    const otherSave = mutations.updateCurrentUser({ showHidden: true }).catch(() => {});
    await Promise.resolve();
    const ok = await mutations.saveAccountDisplayDefaults({ viewMode: "mosaic" });
    expect(ok).toBe(true);
    expect(state.user.viewMode).toBe("mosaic");
    gate.reject(new Error("unrelated setting failed"));
    await otherSave;
    expect(state.user.viewMode).toBe("mosaic");
    expect(state.user.showHidden).not.toBe(true);
  });

  it("an old failed whole-profile save cannot restore the previous account after a switch", async () => {
    await login();
    const gate = deferred();
    usersApi.update.mockImplementationOnce(() => gate.promise);
    const saving = mutations.updateCurrentUser({ showHidden: true }).catch(() => {});
    await Promise.resolve();
    await mutations.setCurrentUser(serverUser({ username: "bob", viewMode: "normal" }));
    gate.reject(new Error("old session failure"));
    await saving;
    expect(state.user.username).toBe("bob");
    expect(state.user.viewMode).toBe("normal");
    expect(state.user.showHidden).not.toBe(true);
  });
});

describe("locale settlement across failed and delayed saves (R5/R7)", () => {
  it("restores the account field, translator and cache when one locale save fails", async () => {
    await login({ locale: "en" });
    localStorage.setItem("userLocale", "en");
    const setLocaleMock = vi.mocked(setLocale);
    setLocaleMock.mockClear();
    usersApi.update.mockRejectedValueOnce(new Error("locale save failed"));

    await expect(mutations.updateCurrentUser({ locale: "zh-cn" })).rejects.toThrow(
      "locale save failed",
    );

    expect(state.user.locale).toBe("en");
    expect(setLocaleMock.mock.calls.map((call) => call[0])).toEqual(["zh-cn", "en"]);
    expect(localStorage.getItem("userLocale")).toBe("en");
  });

  it("keeps a newer successful language when an older locale request fails late", async () => {
    await login({ locale: "en" });
    const gate = deferred();
    usersApi.update.mockImplementationOnce(() => gate.promise);
    const oldSave = mutations.updateCurrentUser({ locale: "zh-cn" }).catch(() => {});
    await vi.waitFor(() => expect(usersApi.update).toHaveBeenCalledTimes(1));

    await mutations.updateCurrentUser({ locale: "de" });
    expect(state.user.locale).toBe("de");

    gate.reject(new Error("old locale save failed"));
    await oldSave;

    expect(state.user.locale).toBe("de");
    expect(vi.mocked(setLocale).mock.calls.at(-1)?.[0]).toBe("de");
    expect(localStorage.getItem("userLocale")).toBe("de");
    // The old request must submit its own frozen snapshot, never the newer
    // state.user that was current when its translator call returned.
    expect(usersApi.update.mock.calls.map((call) => call[0].locale)).toEqual([
      "zh-cn",
      "de",
    ]);
  });

  it("keeps field and translator consistent when the rollback cache write fails", async () => {
    await login({ locale: "en" });
    const gate = deferred();
    usersApi.update.mockImplementationOnce(() => gate.promise);
    const saving = mutations.updateCurrentUser({ locale: "zh-cn" });
    await vi.waitFor(() => expect(usersApi.update).toHaveBeenCalledTimes(1));

    const storage = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("quota exceeded");
    });
    try {
      gate.reject(new Error("locale save failed"));
      await expect(saving).rejects.toThrow("locale save failed");
      expect(state.user.locale).toBe("en");
      expect(vi.mocked(setLocale).mock.calls.at(-1)?.[0]).toBe("en");
    } finally {
      storage.mockRestore();
    }
  });

  it("does not revert a newer default view when an older locale save fails", async () => {
    await login({ locale: "en", viewMode: "gallery" });
    const gate = deferred();
    usersApi.update.mockImplementationOnce(() => gate.promise);
    const localeSave = mutations.updateCurrentUser({ locale: "zh-cn" }).catch(() => {});
    await vi.waitFor(() => expect(usersApi.update).toHaveBeenCalledTimes(1));

    expect(await mutations.saveAccountDisplayDefaults({ viewMode: "mosaic" })).toBe(true);
    expect(state.user.viewMode).toBe("mosaic");

    gate.reject(new Error("locale save failed"));
    await localeSave;

    expect(state.user.locale).toBe("en");
    expect(state.user.viewMode).toBe("mosaic");
    expect(localStorage.getItem("userLocale")).toBe("en");
  });

  it("keeps the delayed request's own language instead of resubmitting the newer snapshot", async () => {
    await login({ locale: "en" });
    const setLocaleMock = vi.mocked(setLocale);
    setLocaleMock.mockClear();
    const firstApply = deferred();
    setLocaleMock
      .mockImplementationOnce(() => firstApply.promise)
      .mockImplementation(async () => {});
    const olderPatch = deferred();
    usersApi.update
      .mockImplementationOnce(() => olderPatch.promise)
      .mockResolvedValueOnce(undefined);

    const oldSave = mutations.updateCurrentUser({ locale: "zh-cn" }).catch(() => {});
    await vi.waitFor(() => expect(setLocaleMock).toHaveBeenCalledTimes(1));
    const newSave = mutations.updateCurrentUser({ locale: "de" });

    firstApply.resolve();
    await newSave;
    expect(usersApi.update).toHaveBeenCalledTimes(2);
    expect(usersApi.update.mock.calls.map((call) => call[0].locale)).toEqual([
      "zh-cn",
      "de",
    ]);
    expect(state.user.locale).toBe("de");

    olderPatch.reject(new Error("older locale save failed"));
    await oldSave;

    expect(state.user.locale).toBe("de");
    expect(setLocaleMock.mock.calls.at(-1)?.[0]).toBe("de");
    expect(localStorage.getItem("userLocale")).toBe("de");
  });

  it("restores the older successful locale when the newer save fails (older completes first)", async () => {
    await login({ locale: "en" });
    const first = deferred();
    const second = deferred();
    usersApi.update
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise);
    const firstSave = mutations.updateCurrentUser({ locale: "zh-cn" });
    await vi.waitFor(() => expect(usersApi.update).toHaveBeenCalledTimes(1));
    const secondSave = mutations.updateCurrentUser({ locale: "de" }).catch(() => {});
    await vi.waitFor(() => expect(usersApi.update).toHaveBeenCalledTimes(2));

    first.resolve();
    await firstSave;
    expect(state.user.locale).toBe("de");
    second.reject(new Error("newer language PATCH failed"));
    await secondSave;

    expect(state.user.locale).toBe("zh-cn");
    expect(vi.mocked(setLocale).mock.calls.at(-1)?.[0]).toBe("zh-cn");
    expect(localStorage.getItem("userLocale")).toBe("zh-cn");
  });

  it("restores the older successful locale when the newer save failed first", async () => {
    await login({ locale: "en" });
    const first = deferred();
    const second = deferred();
    usersApi.update
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise);
    const firstSave = mutations.updateCurrentUser({ locale: "zh-cn" });
    await vi.waitFor(() => expect(usersApi.update).toHaveBeenCalledTimes(1));
    const secondSave = mutations.updateCurrentUser({ locale: "de" }).catch(() => {});
    await vi.waitFor(() => expect(usersApi.update).toHaveBeenCalledTimes(2));

    second.reject(new Error("newer language PATCH failed"));
    await secondSave;
    expect(state.user.locale).toBe("en");

    first.resolve();
    await firstSave;

    expect(state.user.locale).toBe("zh-cn");
    expect(vi.mocked(setLocale).mock.calls.at(-1)?.[0]).toBe("zh-cn");
    expect(localStorage.getItem("userLocale")).toBe("zh-cn");
  });

  it("does not submit a language when the session ends during translation loading", async () => {
    await login({ locale: "en" });
    const setLocaleMock = vi.mocked(setLocale);
    setLocaleMock.mockClear();
    const loading = deferred();
    setLocaleMock.mockImplementationOnce(() => loading.promise);

    const saving = mutations.updateCurrentUser({ locale: "zh-cn" });
    await vi.waitFor(() => expect(setLocaleMock).toHaveBeenCalledTimes(1));
    await mutations.setCurrentUser(null);
    loading.resolve();
    await saving;

    expect(usersApi.update).not.toHaveBeenCalled();
    expect(state.user).toBeNull();
  });

  it("does not let an in-flight rollback overwrite a newer language or its cache", async () => {
    await login({ locale: "en" });
    const setLocaleMock = vi.mocked(setLocale);
    setLocaleMock.mockClear();
    const rollbackGate = deferred();
    setLocaleMock
      .mockImplementationOnce(async () => {})
      .mockImplementationOnce(() => rollbackGate.promise)
      .mockImplementation(async () => {});
    usersApi.update.mockRejectedValueOnce(new Error("locale save failed"));
    const oldSave = mutations.updateCurrentUser({ locale: "zh-cn" }).catch(() => {});
    await vi.waitFor(() => expect(setLocaleMock).toHaveBeenCalledTimes(2));

    const newSave = mutations.updateCurrentUser({ locale: "de" });
    await Promise.resolve();
    expect(state.user.locale).toBe("de");

    rollbackGate.resolve();
    await oldSave;
    await newSave;

    expect(state.user.locale).toBe("de");
    expect(setLocaleMock.mock.calls.at(-1)?.[0]).toBe("de");
    expect(localStorage.getItem("userLocale")).toBe("de");
  });

  it("converges when the last pending request fails without owning the current intent", async () => {
    await login({ locale: "en" });
    const setLocaleMock = vi.mocked(setLocale);
    setLocaleMock.mockClear();
    const gates = [deferred(), deferred(), deferred()];
    usersApi.update.mockReset();
    for (const gate of gates) {
      usersApi.update.mockImplementationOnce(() => gate.promise);
    }
    const saves = [];
    for (const [index, locale] of ["zh-cn", "de", "fr"].entries()) {
      saves.push(mutations.updateCurrentUser({ locale }).catch(() => {}));
      await vi.waitFor(() => expect(usersApi.update).toHaveBeenCalledTimes(index + 1));
    }

    // The newest intent fails first and rolls back to en. The oldest request
    // then succeeds, but must wait while the middle request is still pending.
    gates[2].reject(new Error("fr failed"));
    await saves[2];
    expect(state.user.locale).toBe("en");
    gates[0].resolve();
    await saves[0];
    expect(state.user.locale).toBe("en");

    // The middle request does not own the current intent, so it cannot roll
    // back by itself. Its failure is still the terminal that releases the
    // confirmed zh-cn baseline and must converge the whole language state.
    gates[1].reject(new Error("de failed"));
    await saves[1];

    expect(state.user.locale).toBe("zh-cn");
    expect(setLocaleMock.mock.calls.at(-1)?.[0]).toBe("zh-cn");
    expect(localStorage.getItem("userLocale")).toBe("zh-cn");
  });
});

describe("confirmed locale belongs to one account session", () => {
  it("does not let an old session success become the next account rollback baseline", async () => {
    await login({ locale: "en" });
    const old = deferred();
    usersApi.update.mockImplementationOnce(() => old.promise);
    const oldSave = mutations.updateCurrentUser({ locale: "zh-cn" });
    await vi.waitFor(() => expect(usersApi.update).toHaveBeenCalledTimes(1));

    await mutations.setCurrentUser(null);
    await login({ username: "bob", locale: "en" });
    old.resolve();
    await oldSave;
    expect(state.user.username).toBe("bob");
    expect(state.user.locale).toBe("en");

    usersApi.update.mockRejectedValueOnce(new Error("bob language save failed"));
    await mutations.updateCurrentUser({ locale: "de" }).catch(() => {});
    expect(state.user.locale).toBe("en");
    expect(vi.mocked(setLocale).mock.calls.at(-1)?.[0]).toBe("en");
    expect(localStorage.getItem("userLocale")).toBe("en");
  });

  it("does not let an old session failure roll back the next account's successful language", async () => {
    await login({ locale: "en" });
    const old = deferred();
    usersApi.update.mockImplementationOnce(() => old.promise);
    const oldSave = mutations.updateCurrentUser({ locale: "zh-cn" }).catch(() => {});
    await vi.waitFor(() => expect(usersApi.update).toHaveBeenCalledTimes(1));

    await mutations.setCurrentUser(null);
    await login({ username: "bob", locale: "en" });
    old.reject(new Error("old session language failed"));
    await oldSave;
    expect(state.user.username).toBe("bob");
    expect(state.user.locale).toBe("en");

    await mutations.updateCurrentUser({ locale: "de" });
    expect(state.user.locale).toBe("de");
    expect(vi.mocked(setLocale).mock.calls.at(-1)?.[0]).toBe("de");
    expect(localStorage.getItem("userLocale")).toBe("de");
  });

  it("does not let an old session failure replace the next account's own rollback baseline", async () => {
    await login({ locale: "en" });
    const old = deferred();
    usersApi.update.mockImplementationOnce(() => old.promise);
    const oldSave = mutations.updateCurrentUser({ locale: "zh-cn" }).catch(() => {});
    await vi.waitFor(() => expect(usersApi.update).toHaveBeenCalledTimes(1));

    await mutations.setCurrentUser(null);
    await login({ username: "bob", locale: "en" });
    old.reject(new Error("old session language failed"));
    await oldSave;

    usersApi.update.mockRejectedValueOnce(new Error("bob language save failed"));
    await mutations.updateCurrentUser({ locale: "de" }).catch(() => {});
    expect(state.user.locale).toBe("en");
    expect(vi.mocked(setLocale).mock.calls.at(-1)?.[0]).toBe("en");
    expect(localStorage.getItem("userLocale")).toBe("en");
  });
});
