import { describe, expect, it } from "vitest";
import {
  folderPreferenceTarget,
  hasRawPreferenceField,
  loadAccountDisplayPreferences,
  normalizeSorting,
  normalizeViewMode,
  patchFolderPreference,
  readDisplayPreferencesStorage,
  readFolderPreference,
  removeFolderPreferenceField,
  removeFolderPreferenceFieldFromTree,
  resolveEffectivePreferences,
  setFolderPreference,
  writeDisplayPreferencesStorage,
} from "@/utils/folderDisplayPreferences.js";

const defaults = { viewMode: "normal", sorting: { by: "name", asc: true } };

function fakeStorage(initial) {
  let value = initial;
  return {
    getItem: (_key) => (value === undefined ? null : value),
    setItem: (key, next) => {
      value = next;
    },
    removeItem: () => {
      value = undefined;
    },
    read: () => value,
  };
}

describe("folder display preference validation", () => {
  it("accepts every concrete view mode and rejects unknown values", () => {
    for (const mode of ["list", "compact", "normal", "gallery", "icons", "mosaic"]) {
      expect(normalizeViewMode(mode)).toBe(mode);
    }
    expect(normalizeViewMode(" Normal ")).toBe("normal");
    expect(normalizeViewMode("grid")).toBeNull();
    expect(normalizeViewMode("")).toBeNull();
    expect(normalizeViewMode(null)).toBeNull();
  });

  it("keeps asc=false and every supported sort field", () => {
    for (const by of ["name", "size", "modified", "duration", "type"]) {
      expect(normalizeSorting({ by, asc: false })).toEqual({ by, asc: false });
    }
    expect(normalizeSorting({ by: "modified", asc: true })).toEqual({ by: "modified", asc: true });
  });

  it("rejects incomplete or unsupported sorting instead of guessing", () => {
    expect(normalizeSorting({ by: "name" })).toBeNull();
    expect(normalizeSorting({ by: "name", asc: "false" })).toBeNull();
    expect(normalizeSorting({ by: "created", asc: true })).toBeNull();
    expect(normalizeSorting({ by: "mod_time", asc: true })).toBeNull();
    expect(normalizeSorting(null)).toBeNull();
    expect(normalizeSorting({ by: "name", asc: 0 })).toBeNull();
  });
});

describe("effective preferences", () => {
  it("resolves the four folder combinations independently", () => {
    const cases = [
      [{}, "normal", { by: "name", asc: true }],
      [{ viewMode: "mosaic" }, "mosaic", { by: "name", asc: true }],
      [{ sorting: { by: "modified", asc: false } }, "normal", { by: "modified", asc: false }],
      [
        { viewMode: "mosaic", sorting: { by: "modified", asc: false } },
        "mosaic",
        { by: "modified", asc: false },
      ],
    ];
    for (const [folder, viewMode, sorting] of cases) {
      const resolved = resolveEffectivePreferences(defaults, folder, {});
      expect(resolved.viewMode).toBe(viewMode);
      expect(resolved.sorting).toEqual(sorting);
    }
  });

  it("keeps an explicit folder value that equals the account default", () => {
    const resolved = resolveEffectivePreferences(defaults, { viewMode: "normal" }, {});
    expect(resolved.viewMode).toBe("normal");
    expect(resolved.viewModeSource).toBe("folder");
    // A later account default change must not silently remove the explicit record.
    const folder = { viewMode: "normal" };
    expect(resolveEffectivePreferences({ ...defaults, viewMode: "mosaic" }, folder, {}).viewMode).toBe("normal");
  });

  it("falls back per field when only one stored value is invalid", () => {
    const resolved = resolveEffectivePreferences(
      { viewMode: "gallery", sorting: { by: "size", asc: false } },
      { viewMode: "broken", sorting: { by: "name", asc: true } },
      {},
    );
    expect(resolved.viewMode).toBe("gallery");
    expect(resolved.viewModeSource).toBe("account");
    expect(resolved.sorting).toEqual({ by: "name", asc: true });
    expect(resolved.sortingSource).toBe("folder");
  });

  it("falls back to normal/name-asc when everything is missing", () => {
    const resolved = resolveEffectivePreferences({}, null, {});
    expect(resolved.viewMode).toBe("normal");
    expect(resolved.sorting).toEqual({ by: "name", asc: true });
  });

  it("does not let folder memory bypass an enforced view, but admins keep it", () => {
    const enforced = resolveEffectivePreferences(
      { viewMode: "list", sorting: { by: "name", asc: true } },
      { viewMode: "mosaic" },
      { enforcedViewMode: true, isAdmin: false },
    );
    expect(enforced.viewMode).toBe("list");
    expect(enforced.viewModeSource).toBe("enforced");

    const admin = resolveEffectivePreferences(
      { viewMode: "list", sorting: { by: "name", asc: true } },
      { viewMode: "mosaic" },
      { enforcedViewMode: true, isAdmin: true },
    );
    expect(admin.viewMode).toBe("mosaic");
    expect(admin.viewModeSource).toBe("folder");
  });

  it("keeps per-share folder memory ahead of the share default (original rule)", () => {
    const resolved = resolveEffectivePreferences(
      { viewMode: "normal", sorting: { by: "name", asc: true } },
      { viewMode: "mosaic" },
      { isShare: true, shareViewMode: "gallery" },
    );
    expect(resolved.viewMode).toBe("mosaic");
    expect(resolved.viewModeSource).toBe("folder");
  });

  it("uses the share view when the share has no folder memory", () => {
    const resolved = resolveEffectivePreferences(
      { viewMode: "normal", sorting: { by: "name", asc: true } },
      null,
      { isShare: true, shareViewMode: "gallery" },
    );
    expect(resolved.viewMode).toBe("gallery");
    expect(resolved.viewModeSource).toBe("share");
  });

  it("uses a transient tool context only when no folder record applies", () => {
    const resolved = resolveEffectivePreferences(defaults, null, {
      transientViewMode: "mosaic",
      transientSorting: { by: "duration", asc: false },
    });
    expect(resolved.viewMode).toBe("mosaic");
    expect(resolved.viewModeSource).toBe("transient");
    expect(resolved.sorting).toEqual({ by: "duration", asc: false });
    expect(resolved.sortingSource).toBe("transient");
  });
});

describe("folder record patching", () => {
  it("changes one field and keeps the other", () => {
    const existing = { viewMode: "mosaic", sorting: { by: "modified", asc: false } };
    expect(patchFolderPreference(existing, { sorting: { by: "size", asc: true } })).toEqual({
      viewMode: "mosaic",
      sorting: { by: "size", asc: true },
    });
    expect(patchFolderPreference(existing, { viewMode: "list" })).toEqual({
      viewMode: "list",
      sorting: { by: "modified", asc: false },
    });
  });

  it("ignores invalid values and drops unknown keys", () => {
    expect(patchFolderPreference({ viewMode: "mosaic", legacy: 1 }, { viewMode: "grid" })).toEqual({ viewMode: "mosaic" });
    expect(patchFolderPreference({ viewMode: "mosaic", sorting: { by: "name", asc: true } }, {})).toEqual({
      viewMode: "mosaic",
      sorting: { by: "name", asc: true },
    });
  });

  it("removes one field only", () => {
    expect(removeFolderPreferenceField({ viewMode: "mosaic", sorting: { by: "size", asc: false } }, "viewMode")).toEqual({
      sorting: { by: "size", asc: false },
    });
    expect(removeFolderPreferenceField({ viewMode: "mosaic" }, "sorting")).toEqual({ viewMode: "mosaic" });
  });

  it("cleans empty folder/source entries but keeps other records", () => {
    const tree = {
      Pictures: {
        "/files/Pictures/a": { viewMode: "mosaic" },
        "/files/Pictures/b": { sorting: { by: "size", asc: true } },
      },
      EHP: { "/files/EHP": { viewMode: "list" } },
    };
    const removed = removeFolderPreferenceFieldFromTree(
      tree,
      "Pictures",
      { key: "/files/Pictures/a", preference: { viewMode: "mosaic" } },
      "viewMode",
    );
    expect(removed.Pictures["/files/Pictures/a"]).toBeUndefined();
    expect(removed.Pictures["/files/Pictures/b"].sorting).toEqual({ by: "size", asc: true });
    expect(removed.EHP["/files/EHP"].viewMode).toBe("list");

    const removedSource = removeFolderPreferenceFieldFromTree(
      { Pictures: { "/files/Pictures/a": { viewMode: "mosaic" } } },
      "Pictures",
      { key: "/files/Pictures/a", preference: { viewMode: "mosaic" } },
      "viewMode",
    );
    expect(removedSource).toEqual({});
  });
});

describe("folder identity and legacy keys", () => {
  it("encodes a decoded folder path exactly once", () => {
    const target = folderPreferenceTarget({
      source: "Pictures",
      path: "/中文 目录/100% real",
    });
    expect(target.sourceKey).toBe("Pictures");
    expect(target.folderKey).toBe(
      "/files/Pictures/%E4%B8%AD%E6%96%87%20%E7%9B%AE%E5%BD%95/100%25%20real",
    );
    expect(target.candidateKeys).toContain(`${target.folderKey}/`);
  });

  it("keeps root and share identities apart", () => {
    const root = folderPreferenceTarget({ source: "Pictures", path: "/" });
    expect(root.folderKey).toBe("/files/Pictures");
    expect(root.candidateKeys).toEqual(["/files/Pictures", "/files/Pictures/"]);

    const share = folderPreferenceTarget({
      isShare: true,
      shareHash: "hash123",
      path: "/sub dir",
    });
    expect(share.sourceKey).toBe("hash123");
    expect(share.folderKey).toBe("/public/share/hash123/sub%20dir");
  });

  it("does not confuse same-named folders in different sources or accounts", () => {
    const tree = {
      alice: {
        Pictures: { "/files/Pictures/same": { viewMode: "mosaic" } },
        EHP: { "/files/EHP/same": { viewMode: "list" } },
      },
    };
    const pic = folderPreferenceTarget({ source: "Pictures", path: "/same" });
    const ehp = folderPreferenceTarget({ source: "EHP", path: "/same" });
    expect(readFolderPreference(tree.alice, "Pictures", pic.candidateKeys).preference).toEqual({
      viewMode: "mosaic",
    });
    expect(readFolderPreference(tree.alice, "EHP", ehp.candidateKeys).preference).toEqual({
      viewMode: "list",
    });
    expect(readFolderPreference(tree.alice, "Pictures", ehp.candidateKeys)).toBeNull();
  });

  it("reads historical trailing-slash keys and reports raw invalid fields", () => {
    const tree = {
      Pictures: {
        "/files/Pictures/legacy/": { viewMode: "mosaic" },
        "/files/Pictures/broken": { viewMode: "grid", sorting: { by: "size", asc: false } },
      },
    };
    const legacy = folderPreferenceTarget({ source: "Pictures", path: "/legacy" });
    const found = readFolderPreference(tree, "Pictures", legacy.candidateKeys);
    expect(found.key).toBe("/files/Pictures/legacy/");
    expect(found.preference).toEqual({ viewMode: "mosaic" });

    const broken = readFolderPreference(
      tree,
      "Pictures",
      folderPreferenceTarget({ source: "Pictures", path: "/broken" }).candidateKeys,
    );
    expect(broken.preference).toEqual({ sorting: { by: "size", asc: false } });
    expect(hasRawPreferenceField(broken.rawPreference, "viewMode")).toBe(true);
  });

  it("writes the new record under the existing legacy key instead of duplicating it", () => {
    const tree = { Pictures: { "/files/Pictures/legacy/": { viewMode: "mosaic" } } };
    const found = readFolderPreference(
      tree,
      "Pictures",
      folderPreferenceTarget({ source: "Pictures", path: "/legacy" }).candidateKeys,
    );
    const next = setFolderPreference(
      tree.Pictures,
      "Pictures",
      found.key,
      patchFolderPreference(found.preference, { sorting: { by: "modified", asc: false } }),
    );
    expect(Object.keys(next.Pictures)).toEqual(["/files/Pictures/legacy/"]);
    expect(next.Pictures["/files/Pictures/legacy/"]).toEqual({
      viewMode: "mosaic",
      sorting: { by: "modified", asc: false },
    });
  });
});

describe("storage safety", () => {
  it("treats missing, empty and valid JSON as usable", () => {
    expect(readDisplayPreferencesStorage(fakeStorage(undefined))).toEqual({ ok: true, value: {}, error: "" });
    expect(readDisplayPreferencesStorage(fakeStorage(""))).toEqual({ ok: true, value: {}, error: "" });
    expect(
      readDisplayPreferencesStorage(fakeStorage(JSON.stringify({ a: { s: { p: { viewMode: "list" } } } }))).value.a.s.p
        .viewMode,
    ).toBe("list");
  });

  it("reports corrupt or non-object JSON without clearing it", () => {
    const corrupt = fakeStorage("{not json");
    expect(readDisplayPreferencesStorage(corrupt)).toEqual({ ok: false, value: {}, error: "unreadable" });
    expect(corrupt.read()).toBe("{not json");

    expect(readDisplayPreferencesStorage(fakeStorage("[1,2]"))).toEqual({
      ok: false,
      value: {},
      error: "invalid-shape",
    });
  });

  it("survives throwing storage accessors and write failures", () => {
    const unreadable = {
      getItem: () => {
        throw new Error("denied");
      },
    };
    expect(readDisplayPreferencesStorage(unreadable).ok).toBe(false);
    expect(loadAccountDisplayPreferences(unreadable, "alice")).toEqual({
      preferences: {},
      error: "unreadable",
    });

    const quota = {
      getItem: () => "{}",
      setItem: () => {
        throw new Error("quota");
      },
    };
    expect(writeDisplayPreferencesStorage(quota, { a: 1 })).toEqual({ ok: false, error: "write-failed" });
  });

  it("loads only the requested account and ignores foreign shapes", () => {
    const storage = fakeStorage(
      JSON.stringify({
        alice: { Pictures: { "/files/Pictures/a": { viewMode: "mosaic" } } },
        bob: { EHP: { "/files/EHP/b": { viewMode: "list" } } },
      }),
    );
    expect(loadAccountDisplayPreferences(storage, "alice").preferences).toEqual({
      Pictures: { "/files/Pictures/a": { viewMode: "mosaic" } },
    });
    expect(loadAccountDisplayPreferences(fakeStorage(JSON.stringify({ alice: "nope" })), "alice").preferences).toEqual({});
  });
});