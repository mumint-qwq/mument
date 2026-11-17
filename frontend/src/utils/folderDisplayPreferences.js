/**
 * Folder display preferences: validation and merge rules shared by the store
 * and the settings UI.
 *
 * The persisted tree is unchanged from earlier Quantum versions:
 *
 *   displayPreferences = {
 *     [username]: {
 *       [sourceName or share hash]: {
 *         [encoded route path, e.g. "/files/Pictures/%E4%B8%AD%E6%96%87"]: {
 *           viewMode?: "list" | "compact" | "normal" | "gallery" | "icons" | "mosaic",
 *           sorting?: { by: "name" | "size" | "modified" | "duration" | "type", asc: boolean },
 *         },
 *       },
 *     },
 *   }
 *
 * A folder may remember only one of the two fields. Missing/invalid fields fall
 * back independently, and a stored value is never removed just because it
 * equals the account default. These helpers never touch storage, the network or
 * the store; callers own those side effects.
 */

import {
  getObjectProperty,
  omitObjectProperty,
  setObjectProperty,
} from "@/utils/object.js";

export const DISPLAY_PREFERENCES_STORAGE_KEY = "displayPreferences";

/** Concrete view modes understood by the listing renderer. */
export const VIEW_MODES = Object.freeze([
  "list",
  "compact",
  "normal",
  "gallery",
  "icons",
  "mosaic",
]);

/**
 * Sort fields with a real data/consumer path:
 * - name/size/modified: listing header, sortedItems, paged API
 * - duration: listing header and sortedItems (metadata.duration, missing -> 0)
 * - type: legacy sortedItems/paged API compatibility, no header button
 * "mod_time" is only the paged API wire name for "modified" and is never stored.
 */
export const SORT_FIELDS = Object.freeze([
  "name",
  "size",
  "modified",
  "duration",
  "type",
]);

export const DEFAULT_SORTING = Object.freeze({ by: "name", asc: true });

function isPlainObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

/** @returns {string|null} */
export function normalizeViewMode(value) {
  if (typeof value !== "string") {
    return null;
  }
  const mode = value.trim().toLowerCase();
  return VIEW_MODES.includes(mode) ? mode : null;
}

/** @returns {string|null} */
export function normalizeSortField(value) {
  if (typeof value !== "string") {
    return null;
  }
  const field = value.trim().toLowerCase();
  return SORT_FIELDS.includes(field) ? field : null;
}

/**
 * Sorting is atomic: a valid field plus the literal boolean direction.
 * A false asc value is a real choice and must never be coerced to true.
 * @returns {{ by: string, asc: boolean } | null}
 */
export function normalizeSorting(value) {
  if (!isPlainObject(value)) {
    return null;
  }
  const by = normalizeSortField(value.by);
  const asc = value.asc;
  if (by === null || typeof asc !== "boolean") {
    return null;
  }
  return { by, asc };
}

/** Keep only valid, known fields. Invalid values are dropped, never rewritten. */
export function sanitizeFolderPreference(value) {
  if (!isPlainObject(value)) {
    return {};
  }
  const result = {};
  const viewMode = normalizeViewMode(value.viewMode);
  if (viewMode !== null && Object.hasOwn(value, "viewMode")) {
    result.viewMode = viewMode;
  }
  const sorting = normalizeSorting(value.sorting);
  if (sorting !== null && Object.hasOwn(value, "sorting")) {
    result.sorting = sorting;
  }
  return result;
}

/** True when the raw record claims a field, even if its value is invalid. */
export function hasRawPreferenceField(value, field) {
  return isPlainObject(value) && Object.hasOwn(value, field);
}

/** Strip one field. The other field and the caller's other records stay. */
export function removeFolderPreferenceField(value, field) {
  const next = sanitizeFolderPreference(value);
  if (field === "viewMode" || field === "sorting") {
    return omitObjectProperty(next, field);
  }
  return next;
}

/**
 * Apply one validated change to a folder record. Unknown/invalid change values
 * are ignored; the caller decides whether that is an error.
 */
export function patchFolderPreference(existing, change) {
  const next = sanitizeFolderPreference(existing);
  if (!isPlainObject(change)) {
    return next;
  }
  if (Object.hasOwn(change, "viewMode")) {
    const viewMode = normalizeViewMode(change.viewMode);
    if (viewMode !== null) {
      next.viewMode = viewMode;
    }
  }
  if (Object.hasOwn(change, "sorting")) {
    const sorting = normalizeSorting(change.sorting);
    if (sorting !== null) {
      next.sorting = sorting;
    }
  }
  return next;
}

/**
 * Resolve the values the listing should actually use.
 *
 * Priority (documented in DESIGN.md):
 * - view: applicable enforcement > share view > folder > transient tool view >
 *   account default > "normal"
 * - sorting: folder > transient tool view > account default > name/asc
 *
 * Enforcement only applies to non-admins, matching the backend rule; folder
 * memory is never allowed to bypass an enforced view. There is no sorting
 * enforcement in this task.
 *
 * @param {{viewMode?: string, sorting?: {by: string, asc: boolean}}} accountDefaults
 * @param {unknown} folderPreference
 * @param {{
 *   isAdmin?: boolean,
 *   isShare?: boolean,
 *   shareViewMode?: string,
 *   enforcedViewMode?: boolean,
 *   transientViewMode?: string,
 *   transientSorting?: {by: string, asc: boolean},
 * }} [context]
 * @returns {{
 *   viewMode: string,
 *   sorting: {by: string, asc: boolean},
 *   viewModeSource: "enforced"|"share"|"folder"|"transient"|"account"|"fallback",
 *   sortingSource: "folder"|"transient"|"account"|"fallback",
 * }}
 */
export function resolveEffectivePreferences(accountDefaults, folderPreference, context = {}) {
  const defaults = isPlainObject(accountDefaults) ? accountDefaults : {};
  const folder = sanitizeFolderPreference(folderPreference);
  const accountView = normalizeViewMode(defaults.viewMode);
  const accountSorting = normalizeSorting(defaults.sorting);
  const transientView = normalizeViewMode(context.transientViewMode);
  const transientSorting = normalizeSorting(context.transientSorting);
  const shareView = normalizeViewMode(context.shareViewMode);
  const enforced = context.enforcedViewMode === true && context.isAdmin !== true;

  let viewMode;
  let viewModeSource;
  if (enforced) {
    // The account value already carries the enforced value on login; still do
    // not let an old folder record override it.
    viewMode = accountView || "normal";
    viewModeSource = accountView ? "enforced" : "fallback";
  } else if (folder.viewMode) {
    viewMode = folder.viewMode;
    viewModeSource = "folder";
  } else if (context.isShare === true && shareView !== null) {
    viewMode = shareView;
    viewModeSource = "share";
  } else if (transientView !== null) {
    viewMode = transientView;
    viewModeSource = "transient";
  } else if (accountView !== null) {
    viewMode = accountView;
    viewModeSource = "account";
  } else {
    viewMode = "normal";
    viewModeSource = "fallback";
  }

  let sorting;
  let sortingSource;
  if (folder.sorting) {
    sorting = folder.sorting;
    sortingSource = "folder";
  } else if (transientSorting !== null) {
    sorting = transientSorting;
    sortingSource = "transient";
  } else if (accountSorting !== null) {
    sorting = accountSorting;
    sortingSource = "account";
  } else {
    sorting = { ...DEFAULT_SORTING };
    sortingSource = "fallback";
  }

  return { viewMode, sorting, viewModeSource, sortingSource };
}

/** Normalize a decoded folder path to segments. "/" or "" is the source root. */
function pathSegments(path) {
  if (typeof path !== "string") {
    return null;
  }
  const trimmed = path.replace(/^\/+|\/+$/g, "");
  return trimmed === "" ? [] : trimmed.split("/").filter(Boolean);
}

/**
 * Build the storage identity for a folder.
 *
 * The path key keeps the historical route shape ("/files/<encoded source>/
 * <encoded folder>"), so existing records stay valid. Share routes use
 * "/public/share/<encoded hash>/<encoded sub path>" as before. The physical
 * path is encoded exactly once; literal percent signs survive as %25 rather
 * than being decoded twice.
 *
 * @param {{source?: string, path?: string, isShare?: boolean, shareHash?: string}} input
 * @returns {{sourceKey: string, folderKey: string, candidateKeys: string[]} | null}
 */
export function folderPreferenceTarget({ source, path, isShare = false, shareHash = "" }) {
  const segments = pathSegments(path);
  if (segments === null) {
    return null;
  }
  const sourceKey = isShare ? shareHash : source;
  if (typeof sourceKey !== "string" || sourceKey === "") {
    return null;
  }
  const base = isShare
    ? `/public/share/${encodeURIComponent(sourceKey)}`
    : `/files/${encodeURIComponent(sourceKey)}`;
  const encodedPath = segments.map((segment) => encodeURIComponent(segment)).join("/");
  const folderKey = encodedPath === "" ? base : `${base}/${encodedPath}`;
  const candidateKeys = [folderKey, `${folderKey}/`];
  return { sourceKey, folderKey, candidateKeys: [...new Set(candidateKeys)] };
}

/**
 * Read the folder record, tolerating historical trailing-slash keys.
 * @returns {{ preference: Record<string, unknown>, rawPreference: unknown, key: string } | null}
 */
export function readFolderPreference(accountPreferences, sourceKey, candidateKeys) {
  if (!isPlainObject(accountPreferences) || typeof sourceKey !== "string") {
    return null;
  }
  const sourceLevel = getObjectProperty(accountPreferences, sourceKey);
  if (!isPlainObject(sourceLevel)) {
    return null;
  }
  for (const key of candidateKeys) {
    if (!Object.hasOwn(sourceLevel, key)) {
      continue;
    }
    const rawPreference = getObjectProperty(sourceLevel, key);
    return {
      key,
      rawPreference,
      preference: sanitizeFolderPreference(rawPreference),
    };
  }
  return null;
}

/**
 * Immutably write one folder record. Empty records (including an empty source
 * map) are cleaned up; other sources/folders are preserved.
 */
export function setFolderPreference(accountPreferences, sourceKey, folderKey, preference) {
  const current = isPlainObject(accountPreferences) ? accountPreferences : {};
  const clean = sanitizeFolderPreference(preference);
  const sourceLevel = getObjectProperty(current, sourceKey);
  const safeSource = isPlainObject(sourceLevel) ? sourceLevel : {};

  if (Object.keys(clean).length === 0) {
    const nextSource = omitObjectProperty(safeSource, folderKey);
    if (Object.keys(nextSource).length === 0) {
      return omitObjectProperty(current, sourceKey);
    }
    return setObjectProperty(current, sourceKey, nextSource);
  }
  const nextSource = setObjectProperty(safeSource, folderKey, clean);
  return setObjectProperty(current, sourceKey, nextSource);
}

/** Remove one field from a found record; empty records are cleaned up. */
export function removeFolderPreferenceFieldFromTree(accountPreferences, sourceKey, found, field) {
  if (!found) {
    return isPlainObject(accountPreferences) ? accountPreferences : {};
  }
  const nextPreference = removeFolderPreferenceField(found.preference, field);
  return setFolderPreference(accountPreferences, sourceKey, found.key, nextPreference);
}

/** Safe localStorage read; a corrupt/foreign value is reported, never cleared. */
export function readDisplayPreferencesStorage(storage) {
  try {
    const raw = storage.getItem(DISPLAY_PREFERENCES_STORAGE_KEY);
    if (raw === null || raw === undefined || raw === "") {
      return { ok: true, value: {}, error: "" };
    }
    const parsed = JSON.parse(raw);
    if (!isPlainObject(parsed)) {
      return { ok: false, value: {}, error: "invalid-shape" };
    }
    return { ok: true, value: parsed, error: "" };
  } catch {
    return { ok: false, value: {}, error: "unreadable" };
  }
}

/** Safe localStorage write; reporting is left to the caller. */
export function writeDisplayPreferencesStorage(storage, value) {
  try {
    storage.setItem(DISPLAY_PREFERENCES_STORAGE_KEY, JSON.stringify(value));
    return { ok: true, error: "" };
  } catch {
    return { ok: false, error: "write-failed" };
  }
}

/** Load one account's subtree. Never throws on corrupt or unreadable storage. */
export function loadAccountDisplayPreferences(storage, username) {
  if (typeof username !== "string" || username === "") {
    return { preferences: {}, error: "" };
  }
  const read = readDisplayPreferencesStorage(storage);
  if (!read.ok) {
    return { preferences: {}, error: read.error };
  }
  const account = getObjectProperty(read.value, username);
  return {
    preferences: isPlainObject(account) ? account : {},
    error: "",
  };
}

/** Copy helper used by tests/UI so callers cannot mutate the resolved object. */
export function copySorting(sorting) {
  const normalized = normalizeSorting(sorting);
  return normalized === null ? null : { by: normalized.by, asc: normalized.asc };
}
