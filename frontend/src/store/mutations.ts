// Modified from FileBrowser Quantum for the mument derivative. See MODIFICATIONS.md.
import { setActiveViewGrantScope } from "@/api/viewToken.js";
import { markRaw } from "vue";
import { resourcesApi, usersApi } from "@/api";
import { getEnforcedUserDefaults } from "@/api/settings";
import i18n, { detectLocale, setLocale } from "@/i18n";
import { notify } from "@/notify";
import { url } from "@/utils";
import { getTypeInfo } from "@/utils/mimetype";
import { getObjectProperty, setObjectProperty, omitObjectProperty } from '@/utils/object.js';
import { sortedItems } from "@/utils/sort.js";
import {
  keysForIndices,
  reconcileSelection as reconcileSelectedKeys,
  selectedKeysFor,
  selectionKey,
} from "@/utils/selectionKeys.js";
import { isSourceSidebarCategory } from "@/utils/sidebarCategory";
import { updateManifestLink } from "@/utils/pwaManifest";
import { syncEventTheme } from "@/utils/theme";
import {
  cancelCloseUploadPrompt,
  confirmCloseUploadPrompt,
  hasCloseWithActiveUploadsPrompt,
} from "@/utils/uploadNavigationGuard";
import {
  getDeviceLabel,
  resolveDeviceId,
  resolveDeviceIdForSave,
} from "@/utils/deviceIdentity.js";
import {
  buildDeviceEntry,
  buildDevicePatchUser,
  computeEffectiveSingleClick,
  getSingleClickOverride,
  isSingleClickEnforced,
} from "@/utils/deviceSettings.js";
import { createDeviceSaveQueue } from "@/utils/deviceSaveQueue.js";
import {
  loadAccountDisplayPreferences,
  normalizeSorting,
  normalizeViewMode,
  patchFolderPreference,
  readDisplayPreferencesStorage,
  readFolderPreference,
  removeFolderPreferenceFieldFromTree,
  setFolderPreference,
  writeDisplayPreferencesStorage,
} from "@/utils/folderDisplayPreferences.js";
import { emitStateChanged } from './eventBus';
import { diagnosticsSelectionChange } from "@/utils/diagnostics/telemetry.js";
import { getters } from "./getters";
import { state } from "./state";
import type { FolderDisplayContext, SourceInfo, SourceInfoUpdate, UserObject } from "./types";

const uploadBeforeUnloadHandler = (event: BeforeUnloadEvent) => {
  if (!state.upload.isUploading) {
    return;
  }
  event.preventDefault();
  event.returnValue = "";
};

/**
 * Device-scoped singleClick context.
 *
 * Two monotonic counters keep asynchronous work honest:
 *  - deviceSessionToken is bumped on logout and account switches. Every save
 *    operation captures the token it started under, so a late response can
 *    never be re-attributed to a newer session.
 *  - deviceIntentRevision is bumped on every user intent (toggle or reset). A
 *    failed save and its readback only settle the revision they started with,
 *    so an older network result can never overwrite a newer choice.
 * Neither counter replaces server authorization: the server still checks every
 * PATCH against the authenticated user.
 */
let deviceSessionToken = 0;
let deviceIntentRevision = 0;

function deviceSessionIsActive(token: number) {
  return token === deviceSessionToken;
}

function canUseDeviceSingleClick() {
  return Boolean(state.user) && state.user.username !== "anonymous" && !getters.isShare();
}

/** Admin enforcement wins over the device layer (mirrors the backend rule). */
function singleClickEnforcedForCurrentUser() {
  if (!state.user || state.user.username === "anonymous") {
    return false;
  }
  return isSingleClickEnforced(state.enforcedUserDefaults, getters.isAdmin());
}

function applyEffectiveSingleClick() {
  if (!state.user) {
    return;
  }
  state.user.singleClick = computeEffectiveSingleClick(
    state.deviceSettings.baseSingleClick,
    state.deviceSettings.override,
    singleClickEnforcedForCurrentUser(),
  );
}

/**
 * Point the device block at this browser's stored record. Called on login, on
 * user refresh and after a reset, so all paths share one calculation.
 */
function loadDeviceContext(user: UserObject | null) {
  const device = state.deviceSettings;
  device.error = "";
  device.unconfirmed = false;
  device.pending = false;
  device.confirmedStale = false;
  device.baseSingleClick = user?.singleClick === true;
  device.override = null;
  device.confirmedOverride = null;
  if (!user?.username || user.username === "anonymous") {
    device.deviceId = "";
    device.persistent = true;
    return;
  }
  const resolved = resolveDeviceId();
  device.deviceId = resolved.id || "";
  device.persistent = resolved.persistent;
  if (!resolved.id) {
    return;
  }
  const override = getSingleClickOverride(user, resolved.id);
  device.override = override;
  device.confirmedOverride = override;
  applyEffectiveSingleClick();
}

function clearDeviceContext() {
  const device = state.deviceSettings;
  // Invalidate any readback that captured the previous intent as well.
  deviceIntentRevision += 1;
  device.deviceId = "";
  device.persistent = true;
  device.baseSingleClick = false;
  device.override = null;
  device.confirmedOverride = null;
  device.confirmedStale = false;
  device.pending = false;
  device.error = "";
  device.unconfirmed = false;
}

/**
 * After a failed save the server may still have committed (timeouts, dropped
 * responses). Re-read the profile once and trust the server value, but only if
 * nothing newer exists: the response is gated by the operation's session token,
 * intent revision, username and device id. A delayed readback from an older
 * intent therefore cannot overwrite a later successful choice.
 */
async function reconcileDeviceContextAfterFailure(operation) {
  const { token, intentRevision, deviceId, username } = operation;
  try {
    const fresh = await usersApi.get(username);
    if (!deviceSessionIsActive(token) || state.user?.username !== username) {
      return;
    }
    if (state.deviceSettings.deviceId !== deviceId) {
      return;
    }
    if (deviceIntentRevision !== intentRevision) {
      return;
    }
    const device = state.deviceSettings;
    device.baseSingleClick = fresh?.singleClick === true;
    device.confirmedOverride = getSingleClickOverride(fresh, deviceId);
    device.override = device.confirmedOverride;
    device.confirmedStale = false;
    device.unconfirmed = false;
    applyEffectiveSingleClick();
  } catch {
    if (deviceSessionIsActive(token) && state.user?.username === username) {
      state.deviceSettings.unconfirmed = true;
    }
  }
}

const deviceSaveQueue = createDeviceSaveQueue({
  /**
   * Build the next immutable operation. An operation captures the session,
   * intent revision, device and username at the moment the worker starts, so
   * every later callback answers "whose write was this?" without reading a
   * mutable global.
   */
  readOperation: () => {
    const device = state.deviceSettings;
    if (!canUseDeviceSingleClick()) {
      return null;
    }
    if (!device.persistent || !device.deviceId) {
      return null;
    }
    if (device.override === device.confirmedOverride && !device.confirmedStale) {
      return null;
    }
    return {
      desired: device.override,
      token: deviceSessionToken,
      intentRevision: deviceIntentRevision,
      deviceId: device.deviceId,
      username: state.user.username,
    };
  },
  isActive: (operation) =>
    deviceSessionIsActive(operation.token)
    && state.user?.username === operation.username,
  save: async (operation) => {
    const device = state.deviceSettings;
    // Re-read the persisted id right before writing: another tab may have
    // replaced it (for example after clearing site data there). Sending the
    // replaced id would create a phantom device record.
    const resolved = resolveDeviceIdForSave(device.deviceId);
    if (!resolved.persistent || !resolved.id) {
      device.persistent = false;
      device.deviceId = resolved.id || "";
      throw new Error("browser storage is unavailable; the choice was not saved");
    }
    if (resolved.id !== device.deviceId) {
      device.deviceId = resolved.id;
      device.confirmedOverride = getSingleClickOverride(state.user, resolved.id);
    }
    operation.deviceId = device.deviceId;
    const entry = buildDeviceEntry({ label: getDeviceLabel(), override: operation.desired });
    await usersApi.update(
      buildDevicePatchUser(operation.username, operation.deviceId, entry),
      ["devices"],
    );
    if (!deviceSessionIsActive(operation.token) || state.user?.username !== operation.username) {
      return;
    }
    state.user.devices = {
      ...(state.user.devices || {}),
      [operation.deviceId]: { ...entry },
    };
  },
  onPending: (operation, pending) => {
    if (!deviceSessionIsActive(operation.token) || state.user?.username !== operation.username) {
      return;
    }
    state.deviceSettings.pending = pending;
    emitStateChanged();
  },
  onSettled: (operation, { ok, error }) => {
    if (!deviceSessionIsActive(operation.token) || state.user?.username !== operation.username) {
      return "stop";
    }
    const device = state.deviceSettings;
    if (ok) {
      device.confirmedOverride = operation.desired;
      device.confirmedStale = false;
      device.error = "";
      device.unconfirmed = false;
      emitStateChanged();
      return "continue";
    }
    // A failure says "we do not know the server state": confirmedOverride may
    // be stale because the request could have committed. Only the intent this
    // request was started for may be rolled back; a newer choice is kept.
    const newerIntent = deviceIntentRevision !== operation.intentRevision;
    device.confirmedStale = true;
    device.error = error?.message || String(error || "failed to save device setting");
    device.unconfirmed = true;
    if (!newerIntent) {
      device.override = device.confirmedOverride;
      applyEffectiveSingleClick();
    }
    notify.showError(device.error);
    emitStateChanged();
    if (newerIntent) {
      // Keep the newer intent and let the worker send it next. No readback
      // runs here: an explicit newer choice is authoritative.
      return "continue";
    }
    void reconcileDeviceContextAfterFailure(operation).finally(() => {
      emitStateChanged();
    });
    return "stop";
  },
});

/**
 * Account display defaults (viewMode/sorting) are saved separately from the
 * per-folder memory. Two counters keep this honest:
 * - displayPreferencesSessionToken is bumped on every setCurrentUser. A late
 *   response from a previous login/session is discarded instead of being
 *   applied to the next account (including the same name after logout).
 * - displayDefaultRevisions[field] is bumped on every explicit default change.
 *   Only the newest intent may confirm or roll back that field.
 */
let displayPreferencesSessionToken = 0;
const displayDefaultRevisions = { viewMode: 0, sorting: 0 };
const displayDefaultConfirmed: {
  viewMode: string;
  sorting: { by: string; asc: boolean };
} = {
  viewMode: "normal",
  sorting: { by: "name", asc: true },
};
let displayDefaultsSaveChain: Promise<void> = Promise.resolve();
let displayPreferencesStorageErrorShown = false;

/**
 * Locale changes touch the user object, the vue-i18n translator and the
 * optional userLocale cache. Serialize the translator calls and keep a
 * revision so a late save failure cannot switch back a newer language or
 * write the old value into the cache. The revision is independent of the
 * display-default tracking: a language save and a view/sorting save settle
 * separately.
 */
let localeIntentRevision = 0;
let localeApplyChain: Promise<void> = Promise.resolve();
let latestLocaleSentRevision = 0;
let confirmedLocale = "en";
let confirmedLocaleRevision = 0;
const pendingLocaleOperations = new Map<number, string>();

function localeRequestIsCurrent(
  revision: number,
  locale: string,
  sessionIsActive: () => boolean,
) {
  return revision === localeIntentRevision
    && sessionIsActive()
    && (state.user?.locale || "en") === locale;
}

function requestLocaleApply(locale: string, sessionIsActive: () => boolean) {
  const revision = ++localeIntentRevision;
  const run = async () => {
    if (!localeRequestIsCurrent(revision, locale, sessionIsActive)) {
      return false;
    }
    await setLocale(locale);
    return localeRequestIsCurrent(revision, locale, sessionIsActive);
  };
  const applied = localeApplyChain.then(run, run);
  localeApplyChain = applied.then(() => undefined, () => undefined);
  return { revision, applied };
}

function hasNewerLocaleOperation(revision: number) {
  for (const pending of pendingLocaleOperations.keys()) {
    if (pending > revision) {
      return true;
    }
  }
  return false;
}

/**
 * A late success may establish the newest confirmed language after an even
 * newer intent has already failed and rolled the page back. Converge the
 * translator, account field and cache to that confirmed value, but only while
 * it is still the newest confirmed revision and no newer locale operation is
 * pending. The revision and intent checks before and after the await keep a
 * new user choice from being overwritten by this correction.
 */
async function convergeToConfirmedLocale(
  locale: string,
  revision: number,
  sessionIsActive: () => boolean,
) {
  const run = async () => {
    if (
      revision !== confirmedLocaleRevision
      || !sessionIsActive()
      || hasNewerLocaleOperation(revision)
    ) {
      return;
    }
    const intentAtStart = localeIntentRevision;
    await setLocale(locale);
    if (
      revision !== confirmedLocaleRevision
      || !sessionIsActive()
      || hasNewerLocaleOperation(revision)
      || localeIntentRevision !== intentAtStart
    ) {
      return;
    }
    if ((state.user?.locale || "en") !== locale) {
      state.user = { ...state.user, locale };
    }
    try {
      localStorage.setItem("userLocale", locale);
    } catch {
      // The account field and translator still converge; the cache is optional.
    }
  };
  const queued = localeApplyChain.then(run, run);
  localeApplyChain = queued.then(() => undefined, () => undefined);
  await queued;
}

/**
 * Every terminal path of a language save settles through this pair. The
 * session check comes first: a response from a previous login must not delete
 * the next session's pending work or write its confirmed baseline. A late
 * success is still a real server save; it is simply not this session's state.
 */
function releaseLocaleOperation(revision: number, sessionIsActive: () => boolean) {
  if (!sessionIsActive()) {
    return false;
  }
  pendingLocaleOperations.delete(revision);
  return true;
}

function confirmLocaleOperation(
  revision: number,
  locale: string,
  sessionIsActive: () => boolean,
) {
  if (!releaseLocaleOperation(revision, sessionIsActive)) {
    return false;
  }
  if (revision > confirmedLocaleRevision) {
    confirmedLocale = locale || "en";
    confirmedLocaleRevision = revision;
  }
  return true;
}

/**
 * A terminal only unblocks a correction; it does not have to own the current
 * intent. Once no newer operation is pending, converge to the newest confirmed
 * language. convergeToConfirmedLocale repeats the revision/pending/session
 * checks before and after the translator await.
 */
async function settleLocaleConvergence(sessionIsActive: () => boolean) {
  if (!sessionIsActive() || confirmedLocaleRevision === 0) {
    return;
  }
  if ((state.user?.locale || "en") === confirmedLocale) {
    return;
  }
  await convergeToConfirmedLocale(
    confirmedLocale,
    confirmedLocaleRevision,
    sessionIsActive,
  );
}

function readDefaultRevision(field: "viewMode" | "sorting") {
  return field === "viewMode"
    ? displayDefaultRevisions.viewMode
    : displayDefaultRevisions.sorting;
}

function bumpDefaultRevision(field: "viewMode" | "sorting") {
  if (field === "viewMode") {
    displayDefaultRevisions.viewMode += 1;
    return displayDefaultRevisions.viewMode;
  }
  displayDefaultRevisions.sorting += 1;
  return displayDefaultRevisions.sorting;
}

function setDefaultPending(field: "viewMode" | "sorting", pending: boolean) {
  if (field === "viewMode") {
    state.accountDefaultsSave.viewMode = pending;
  } else {
    state.accountDefaultsSave.sorting = pending;
  }
}

function asPreferenceObject(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

function notifyDisplayPreferenceStorageError() {
  if (displayPreferencesStorageErrorShown) {
    return;
  }
  displayPreferencesStorageErrorShown = true;
  notify.showError(i18n.global.t("files.folderPreferenceStorageError"));
}

/** Point all display-default state at one account/session. */
function resetDisplayPreferenceTracking(user: UserObject | null) {
  displayPreferencesSessionToken += 1;
  displayPreferencesStorageErrorShown = false;
  displayDefaultConfirmed.viewMode = normalizeViewMode(user?.viewMode) || "normal";
  displayDefaultConfirmed.sorting = normalizeSorting(user?.sorting) || { by: "name", asc: true };
  state.accountDefaultsSave = { viewMode: false, sorting: false, error: "" };
  state.listingContextOverride = {
    key: "",
    username: user?.username || "",
    viewMode: "",
    sorting: null,
  };
}

/**
 * Apply one change to the current folder record.
 *
 * The write is based on the latest stored account subtree, not on the possibly
 * older in-memory copy, so another tab's other folders survive. If storage is
 * corrupt/unreadable it is never overwritten: the page keeps the choice in
 * memory and the user is told it was not persisted. A failed write keeps the
 * in-memory record too (page usable, never a false "saved" claim).
 */
function applyCurrentFolderPreferenceChange(
  updater: (account: Record<string, unknown>, context: FolderDisplayContext) => Record<string, unknown>,
): boolean {
  const context = getters.folderDisplayContext();
  if (!context?.isDirectory) {
    return false;
  }
  const user = state.user;
  if (!user?.username) {
    return false;
  }
  if (!context.canPersist || typeof localStorage === "undefined") {
    state.displayPreferences = updater(asPreferenceObject(state.displayPreferences), context);
    emitStateChanged();
    return true;
  }
  const read = readDisplayPreferencesStorage(localStorage);
  if (!read.ok) {
    state.displayPreferences = updater(asPreferenceObject(state.displayPreferences), context);
    notifyDisplayPreferenceStorageError();
    emitStateChanged();
    return true;
  }
  const latestAccount = asPreferenceObject(getObjectProperty(read.value, user.username));
  const nextAccount = updater(latestAccount, context);
  const written = writeDisplayPreferencesStorage(
    localStorage,
    setObjectProperty(read.value, user.username, nextAccount),
  );
  if (written.ok) {
    state.displayPreferences = nextAccount;
  } else {
    state.displayPreferences = updater(asPreferenceObject(state.displayPreferences), context);
    notifyDisplayPreferenceStorageError();
  }
  emitStateChanged();
  return true;
}

export const mutations = {
  disableEventThemes: () => {
    if (state.disableEventThemes) {
      return;
    }
    localStorage.setItem("disableEventThemes", "true");
    state.disableEventThemes = true;
    syncEventTheme(false);
    // Set theme color back to user's preference or default
    if (state.user.themeColor) {
      document.documentElement.style.setProperty("--primaryColor", state.user.themeColor);
    } else {
      // Remove the override to use the default CSS variable
      document.documentElement.style.removeProperty("--primaryColor");
    }
    emitStateChanged();
  },
  setPreviousHistoryItem: (value) => {
    if (value === state.previousHistoryItem) {
      return;
    }
    if (value !== null) {
      value.isShare = getters.isShare();
    }
    state.previousHistoryItem = value;
    emitStateChanged();
  },
  setContextMenuHasItems: (value) => {
    if (value === state.contextMenuHasItems) {
      return;
    }
    state.contextMenuHasItems = value;
    emitStateChanged();
  },
  setEditorDirty: (value) => {
    if (value === state.editorDirty) {
      return;
    }
    state.editorDirty = value;
    emitStateChanged();
  },
  setEditorSaveHandler: (handler) => {
    state.editorSaveHandler = handler;
    emitStateChanged();
  },
  setEditorStats: (stats) => {
    if (JSON.stringify(state.editorStats) === JSON.stringify(stats)) return;
    state.editorStats = stats;
    emitStateChanged();
  },
  setEditorFontSize: (size) => {
    if (state.editorFontSize === size) return;
    state.editorFontSize = size;
    localStorage.setItem('editorFontSize', size);
    emitStateChanged();
  },
  setDeletedItem: (value) => {
    if (value === state.deletedItem) {
      return;
    }
    state.deletedItem = value;
    emitStateChanged();
  },
  setSeenUpdate: (value) => {
    if (value === state.seenUpdate) {
      return;
    }
    state.seenUpdate = value
    localStorage.setItem("seenUpdate", value);
    emitStateChanged();
  },
  toggleOverflowMenu: () => {
    state.showOverflowMenu = !state.showOverflowMenu;
    emitStateChanged();
  },
  setWatchDirChangeAvailable() {
    state.req.hasUpdate = true;
  },
  setPreviewSource: (value) => {
    if (value === state.popupPreviewSourceInfo?.url) {
      return;
    }
    if (value) {
      state.popupPreviewSourceInfo = {
        ...state.popupPreviewSourceInfo,
        url: value,
        size: "xlarge",
      };
    } else {
      state.popupPreviewSourceInfo = null;
    }
    emitStateChanged();
  },
  updateListing: (value) => {
    if (value === state.listing) {
      return;
    }
    state.listing = value;
    emitStateChanged();
  },
  setCurrentSource: (value) => {
    if (value === state.sources.current) {
      return;
    }
    state.sources.current = value;
    emitStateChanged();
  },
  updateSource: (sourcename, value) => {
    if (getObjectProperty(state.sources.info, sourcename)) {
      state.sources.info = setObjectProperty(state.sources.info, sourcename, value) as Record<string, SourceInfo>;
    }
    emitStateChanged();
  },
  updateSourceInfo: (value: Record<string, SourceInfoUpdate> | "error") => {
    if (value === "error") {
      state.realtimeActive = false;
      let info = state.sources.info;
      for (const k of Object.keys(info)) {
        info = setObjectProperty(info, k, { ...(getObjectProperty(info, k) as SourceInfo), status: "error" }) as Record<string, SourceInfo>;
      }
      state.sources.info = info;
    } else {
      let info = state.sources.info;
      let hasAny = false;
      for (const [k, source] of Object.entries(value)) {
        const existing = getObjectProperty(info, k) as SourceInfo | undefined;
        if (existing) {
          const used = Number(source.used) || 0;
          const total = Number(source.total) || 0;
          const updated: SourceInfo = {
            ...existing,
            used,
            usedAlt: source.usedAlt || 0,
            totalRoot: source.totalRoot || 0,
            usedAltRoot: source.usedAltRoot || 0,
            total,
            usedPercentage: total > 0 ? Math.min(100, Math.round((used / total) * 100)) : 0,
            usageScopeMismatch:
              source.usageScopeMismatch === true || (total > 0 && used > total),
            status: source.status || "unknown",
            name: source.name || k,
            files: source.numFiles || 0,
            folders: source.numDirs || 0,
            lastIndex: source.lastIndexedUnixTime || 0,
            quickScanDurationSeconds: source.quickScanDurationSeconds || 0,
            fullScanDurationSeconds: source.fullScanDurationSeconds || 0,
            complexity: source.complexity || 0,
            scanners: source.scanners || [],
            readOnly: source.readOnly || false,
            private: source.private || false,
          };
          info = setObjectProperty(info, k, updated) as Record<string, SourceInfo>;
          if (updated.total > 0 || updated.used > 0 || updated.usedAlt > 0) hasAny = true;
        }
      }
      state.sources.info = info;
      state.sources.hasSourceInfo = hasAny;
    }
    emitStateChanged();
  },
  setRealtimeActive: (value) => {
    if ( value === false ) {
      state.realtimeDownCount = state.realtimeDownCount + 1;
    } else {
      state.realtimeDownCount = 0;
    }
    if (value === state.realtimeActive) {
      return;
    }
    state.realtimeActive = value;
    emitStateChanged();
  },
  setSources: (user) => {
    // Rebuilding sources from /api/users must not wipe stats already merged from
    // /api/settings/sources, SSE, or a prior validateLogin — otherwise the sidebar
    // shows "unknown"/red until a full page reload (common after OTP: initAuth then
    // router beforeResolve both refresh the user).
    const sameUser = Boolean(state.user && user.username === state.user.username);
    const prevInfo = sameUser && state.sources.info ? state.sources.info : {};
    let currentSource = user.scopes.length > 0 ? user.scopes[0].name : "";
    if (
      sameUser &&
      state.sources.current &&
      user.scopes.some((s) => s.name === state.sources.current)
    ) {
      currentSource = state.sources.current;
    }
    const sources: {
      info: Record<string, SourceInfo>;
      current: string;
      count: number;
      hasSourceInfo: boolean;
      defaultSource?: string;
    } = { info: {}, current: currentSource, count: user.scopes.length, hasSourceInfo: false };
    for (const source of user.scopes) {
      const prev = prevInfo[source.name];
      const merge = Boolean(prev);
      sources.info[source.name] = {
        pathPrefix: sources.count === 1 ? "" : encodeURIComponent(source.name),
        used: merge ? prev.used : 0,
        total: merge ? prev.total : 0,
        usedAlt: merge ? prev.usedAlt : 0,
        totalRoot: merge ? prev.totalRoot : 0,
        usedAltRoot: merge ? prev.usedAltRoot : 0,
        usedPercentage: merge ? prev.usedPercentage : 0,
        usageScopeMismatch: merge ? prev.usageScopeMismatch : false,
        status: merge ? prev.status : "unknown",
        name: merge ? prev.name : source.name,
        files: merge ? prev.files : 0,
        folders: merge ? prev.folders : 0,
        lastIndex: merge ? prev.lastIndex : 0,
        quickScanDurationSeconds: merge ? prev.quickScanDurationSeconds : 0,
        fullScanDurationSeconds: merge ? prev.fullScanDurationSeconds : 0,
        complexity: merge ? prev.complexity : 0,
        scanners: merge && prev.scanners ? [...prev.scanners] : [],
        readOnly: merge ? prev.readOnly : false,
        private: merge ? prev.private : false,
      };
    }
    // Sidebar usage bar uses hasSourceInfo + per-source used/total; must survive object replace
    sources.hasSourceInfo = user.scopes.some((s) => {
      const e = sources.info[s.name];
      return e && (e.total > 0 || e.used > 0 || e.usedAlt > 0);
    });
    // Check if user has custom sidebar links with sources
    let targetSource = sources.current;
    if (state.user?.sidebarLinks && state.user.sidebarLinks.length > 0) {
      // Find first source link in user's sidebar links
      const firstSourceLink = state.user.sidebarLinks.find(link =>
        isSourceSidebarCategory(link.category) && link.sourceName
      );
      if (firstSourceLink) {
        targetSource = firstSourceLink.sourceName;
      }
    }
    sources.defaultSource = targetSource;
    state.sources = sources;
    emitStateChanged();
  },
  setGallerySize: (value) => {
    if (value === state.user.gallerySize) {
      return;
    }
    state.user.gallerySize = value
    const isAnonymous = state.user.username === 'anonymous';
    if (!isAnonymous) {
      const encoded = url.base64Encode(state.user.username);
      localStorage.setItem(`GallerySize_${encoded}`, value);
    }
    emitStateChanged();
  },
  setActiveSettingsView: (value) => {
    if (value === state.activeSettingsView) {
      return;
    }
    state.activeSettingsView = value;
    // Update the hash in the URL without reloading or changing history state
    window.history.replaceState(null, "", `#${value}`);
    const container = document.getElementById("main");
    const element = document.getElementById(value);
    if (container && element) {
      const offset = 4 * parseFloat(getComputedStyle(document.documentElement).fontSize); // 4em in px
      const containerTop = container.getBoundingClientRect().top;
      const elementTop = element.getBoundingClientRect().top;
      const scrollOffset = elementTop - containerTop - offset;
      container.scrollTo({
        top: container.scrollTop + scrollOffset,
        behavior: "smooth",
      });
    }
    emitStateChanged();
  },
  setSettings: (value) => {
    state.settings = value;
    emitStateChanged();
  },
  toggleDarkMode() {
    void mutations.updateCurrentUser({ "darkMode": !state.user.darkMode });
    emitStateChanged();
  },
  toggleSidebar() {
    state.showSidebar = !state.showSidebar;
    emitStateChanged();
  },
  closeSidebar() {
    if (!state.showSidebar) {
      return;
    }
    state.showSidebar = false;
    emitStateChanged();
  },
  setSidebarVisible(value) {
    if (value === state.showSidebar) {
      return;
    }
    state.showSidebar = value;
    emitStateChanged();
  },
  setIsUploading(value) {
    if (value === state.upload.isUploading) {
      return;
    }
    state.upload.isUploading = value;
    if (typeof window !== "undefined") {
      if (value) {
        window.addEventListener("beforeunload", uploadBeforeUnloadHandler);
      } else {
        window.removeEventListener("beforeunload", uploadBeforeUnloadHandler);
      }
    }
    emitStateChanged();
  },
  closeHovers: () => {
    // Close only specific lightweight/ephemeral hovers. Like ContextMenu, OverflowMenu, tooltip or sidebar (if not pinned and floating)
    const closeableHovers = ['ContextMenu', 'OverflowMenu'];
    state.prompts = state.prompts.filter(p => !closeableHovers.includes(p.name));
    mutations.closeSidebar();
    mutations.hideTooltip(true);
  },
  closeTopPrompt: (id, options) => {
    if (!options?.force && state.prompts.length > 0) {
      const targetId =
        id ?? state.prompts[state.prompts.length - 1]?.id;
      const promptToClose = state.prompts.find((p) => p.id === targetId);
      if (
        promptToClose?.name === "upload" &&
        state.upload.isUploading &&
        !hasCloseWithActiveUploadsPrompt()
      ) {
        mutations.showPrompt({
          name: "CloseWithActiveUploads",
          pinned: true,
          confirm: () => {
            confirmCloseUploadPrompt(targetId);
          },
          cancel: () => {
            cancelCloseUploadPrompt();
          },
        });
        return;
      }
    }

    if (id === undefined) {
      // close topmost prompt
      if (state.prompts.length === 0) return;
      mutations.closeHovers();
      state.prompts.pop();
    } else {
      // close prompt by ID
      const idx = state.prompts.findIndex(p => p.id === id);
      if (idx === -1) return;
      state.prompts.splice(idx, 1);
    }
    // Pre-existing: no top-level state.stickySidebar (only state.user.stickySidebar), so this
    // is always truthy-negated. Left as-is; see PR description.
    if (state.prompts.length === 0 && !(state as unknown as { stickySidebar?: boolean }).stickySidebar) {
      state.showSidebar = false;
    }
    emitStateChanged();
  },
  showPrompt: (value) => {
    state.promptIdCounter += 1;
    const id = state.promptIdCounter;
    // Set parentId to the current topmost prompt, unless the prompt is pinned.
    let parentId = value?.parentId;
    if (!parentId && !value?.pinned) {
      const topPrompt = state.prompts[state.prompts.length - 1];
      if (topPrompt) {
        parentId = topPrompt.id;
      }
    }
    const entry = typeof value === "object" ? {
      id,
      name: value?.name,
      parentId,
      pinned: value?.pinned || false,
      confirm: value?.confirm,
      action: value?.action,
      props: value?.props || {},
      discard: value?.discard,
      cancel: value?.cancel,
    } : {
      id,
      name: value,
      parentId,
      pinned: false,
      confirm: value?.confirm,
      action: value?.action,
      props: value?.props || {},
      discard: value?.discard,
      cancel: value?.cancel,
    };
    const pinnedCount = state.prompts.filter(p => p.pinned).length;
    if (entry.pinned) {
      state.prompts.push(entry);
    } else {
      // Non‑pinned prompts go just before the first pinned prompt
      const insertIndex = state.prompts.length - pinnedCount;
      state.prompts.splice(insertIndex, 0, entry);
    }
    mutations.hideTooltip(true);
  },
  updatePromptTitle: (id, title) => {
    const prompt = state.prompts.find((p) => p.id === id);
    if (!prompt) {
      return;
    }
    prompt.props.title = title;
    emitStateChanged();
  },
  setLoading: (loadType, status) => {
    if (status === false) {
      state.loading = omitObjectProperty(state.loading, loadType);
    } else {
      state.loading = setObjectProperty(state.loading, loadType, true);
    }
    emitStateChanged();
  },
  setReload: (value) => {
    if (value === state.reload) {
      return;
    }
    state.reload = value;
    emitStateChanged();
  },
  setCurrentUser: async (value) => {
    try {
      // If value is null or undefined, emit state change and exit early
      if (!value) {
        state.user = value;
        state.enforcedUserDefaults = {};
        confirmedLocale = "en";
        confirmedLocaleRevision = 0;
        pendingLocaleOperations.clear();
        // Invalidate in-flight device saves of the session being left; the
        // server may still finish a request that was already sent.
        deviceSessionToken += 1;
        deviceSaveQueue.cancel();
        clearDeviceContext();
        // Folder memory and account-default writes belong to the session that
        // is ending; clear the local view and invalidate pending saves.
        resetDisplayPreferenceTracking(null);
        state.displayPreferences = {};
        emitStateChanged();
        return;
      }
      const previousUsername = state.user?.username;
      if (value.username !== "anonymous") {
        mutations.setSources(value);
      }
      // Ensure locale exists and is valid
      if (!value.locale) {
        value.locale = detectLocale();
      }
      await setLocale(value.locale);
      state.user = value;
      confirmedLocale = value.locale || "en";
      confirmedLocaleRevision = 0;
      pendingLocaleOperations.clear();
      if (previousUsername && previousUsername !== value.username) {
        // A different account must not inherit the previous session's device state.
        deviceSessionToken += 1;
        deviceSaveQueue.cancel();
      }
      loadDeviceContext(state.user);
      // The server values are the account defaults. Preserve a valid saved
      // sorting/view exactly; invalid or missing values only get an in-memory
      // safe fallback (never written back by initialization).
      state.user.viewMode = normalizeViewMode(state.user.viewMode) || "normal";
      state.user.sorting = normalizeSorting(state.user.sorting) || { by: "name", asc: true };
      // Establish the new session/account identity before touching optional
      // browser storage: a storage exception must not skip confirmed-default
      // initialization or leave the previous account's folder tree in memory.
      resetDisplayPreferenceTracking(state.user);
      state.displayPreferences = {};

      // Ensure fileLoading defaults are set
      if (!state.user.fileLoading) {
        state.user.fileLoading = {
          maxConcurrentUpload: 3,
          uploadChunkSizeMb: 5,
          downloadChunkSizeMb: 0,
          clearAll: false
        };
      } else {
        // Ensure each property has a default if missing
        if (state.user.fileLoading.maxConcurrentUpload === undefined) {
          state.user.fileLoading.maxConcurrentUpload = 3;
        }
        if (state.user.fileLoading.uploadChunkSizeMb === undefined) {
          state.user.fileLoading.uploadChunkSizeMb = 5;
        }
        if (state.user.fileLoading.clearAll === undefined) {
          state.user.fileLoading.clearAll = false;
        }
        if (state.user.fileLoading.downloadChunkSizeMb === undefined) {
          state.user.fileLoading.downloadChunkSizeMb = 0;
        }
      }

      // Gallery size remains a browser-local preference (unchanged scope).
      // The retired ViewMode_* key is intentionally neither read nor written:
      // the account default comes from the server and folder memory is separate.
      const isAnonymous = state.user.username === 'anonymous';
      const encoded = !isAnonymous ? url.base64Encode(state.user.username) : '';
      let storedGallerySize = NaN;
      if (!isAnonymous) {
        try {
          storedGallerySize = parseInt(localStorage.getItem(`GallerySize_${encoded}`), 10);
        } catch {
          // Compatibility cache only; continue with the server/in-memory value.
        }
      }
      let gallerySize = Number.isFinite(storedGallerySize) && storedGallerySize > 0
        ? storedGallerySize
        : state.user.gallerySize || 3;
      gallerySize = Math.min(9, Math.max(1, gallerySize)); // ensure 1–9 since this is the slider min and max size
      state.user.gallerySize = gallerySize;
      if (!isAnonymous) {
        try {
          localStorage.setItem(`GallerySize_${encoded}`, String(gallerySize));
        } catch {
          // Keep the in-memory size; do not abort login or clear other keys.
        }
      }

      // Load the folder overrides for this account through the safe reader; a
      // corrupt/unreadable value means "no folder memory" for this session,
      // never the previous account's tree.
      const loadedPreferences = loadAccountDisplayPreferences(localStorage, state.user.username);
      state.displayPreferences = loadedPreferences.preferences;

    } catch (_error) {
      // Silently ignore errors when loading preferences
    }
    emitStateChanged();
  },
  syncEnforcedUserDefaults: async () => {
    if (
      !getters.isLoggedIn() ||
      getters.isShare() ||
      state.user?.username === "anonymous"
    ) {
      state.enforcedUserDefaults = {};
      emitStateChanged();
      return;
    }
    try {
      const data = await getEnforcedUserDefaults();
      state.enforcedUserDefaults = data.enforced || {};
    } catch {
      state.enforcedUserDefaults = {};
    }
    // Enforcement can override the device layer; recompute the effective value.
    applyEffectiveSingleClick();
    emitStateChanged();
  },
  /**
   * Save this browser's explicit open mode. The account-wide profile value is
   * not touched; only data.devices for the current device is sent.
   */
  setDeviceSingleClick: async (value) => {
    if (!canUseDeviceSingleClick()) {
      return;
    }
    if (singleClickEnforcedForCurrentUser()) {
      return;
    }
    const desired = value === true;
    const device = state.deviceSettings;
    if (!device.persistent || !device.deviceId) {
      // No durable browser identity: keep the choice for this page only and
      // do not send a request that could create an orphan server record.
      deviceIntentRevision += 1;
      device.override = desired;
      device.error = "storage";
      applyEffectiveSingleClick();
      emitStateChanged();
      return;
    }
    if (
      device.override === desired
      && !device.confirmedStale
      && !device.error
      && !device.pending
    ) {
      return;
    }
    // Fast clicks update the visible intent immediately; the queue coalesces
    // them so the last intent is the last write. The revision lets a late
    // failure/readback know that a newer choice already exists.
    deviceIntentRevision += 1;
    device.override = desired;
    device.error = "";
    device.unconfirmed = false;
    applyEffectiveSingleClick();
    emitStateChanged();
    await deviceSaveQueue.enqueue();
  },
  /** Clear this browser's override; other devices and the account value stay. */
  resetDeviceSingleClick: async () => {
    if (!canUseDeviceSingleClick()) {
      return;
    }
    const device = state.deviceSettings;
    if (!device.persistent || !device.deviceId) {
      return;
    }
    if (device.confirmedOverride === null && device.override === null && !device.confirmedStale) {
      return;
    }
    // A reset is allowed even when an admin enforces the field: the server
    // accepts an empty override and only the override is removed.
    deviceIntentRevision += 1;
    device.override = null;
    device.error = "";
    device.unconfirmed = false;
    applyEffectiveSingleClick();
    emitStateChanged();
    await deviceSaveQueue.enqueue();
  },
  setShareData: (shareData) => {
    const newShare = { ...state.shareInfo, ...shareData };
    if (JSON.stringify(newShare) === JSON.stringify(state.shareInfo)) {
      return;
    }
    state.shareInfo = newShare;
    updateManifestLink();
    emitStateChanged();
  },
  clearShareData: () => {
    state.shareInfo = {
      isShare: false,
      disableThumbnails: false,
      hash: "",
      token: "",
      subPath: "",
      passwordValid: false,
      enforceDarkLightMode: "",
      disableSidebar: false,
      isValid: true,
      shareType: "",
      title: "",
      description: "",
    };
    updateManifestLink();
    emitStateChanged();
  },
  setSession: (value) => {
    if (value === state.sessionId) {
      return;
    }
    state.sessionId = value;
    emitStateChanged();
  },
  setMultiple: (value) => {
    if (value === state.multiple) return;
    state.multiple = value;
    if (value) {
      notify.showMultipleSelection();
    } else {
      notify.hideMultipleSelection();
    }
    emitStateChanged();
  },
  addSelected: (value) => {
    const before = state.selected.length;
    state.selected.push(value);
    const key = typeof value === "number"
      ? selectionKey(state.req?.items?.at(value))
      : selectionKey(value);
    if (key && !state.selectedKeys.includes(key)) {
      state.selectedKeys.push(key);
    }
    diagnosticsSelectionChange({
      operation: "add",
      before,
      after: state.selected.length,
      known_items: state.req?.items?.length || 0,
      multiple: state.multiple,
      merged_count: 1,
    });
    emitStateChanged();
  },
  removeSelected: (value) => {
    const i = state.selected.indexOf(value);
    if (i === -1) return;
    const before = state.selected.length;
    state.selected.splice(i, 1);
    // Re-derive keys from the remaining indices; selecting by index is still
    // supported by callers, keys are the stable identity for refreshes.
    state.selectedKeys = selectedKeysFor(state.req?.items || [], state.selected);
    diagnosticsSelectionChange({
      operation: "remove",
      before,
      after: state.selected.length,
      known_items: state.req?.items?.length || 0,
      multiple: state.multiple,
      merged_count: 1,
    });
    emitStateChanged();
  },
  resetSelected: () => {
    const before = state.selected.length;
    state.selected = [];
    state.selectedKeys = [];
    mutations.setMultiple(false);
    if (before > 0) {
      diagnosticsSelectionChange({
        operation: "clear",
        before,
        after: 0,
        known_items: state.req?.items?.length || 0,
        multiple: false,
        merged_count: 1,
      });
    }
    emitStateChanged();
  },
  /** Re-map the stored selection keys onto a re-ordered or appended item list. */
  reconcileSelection: (items) => {
    const reconciled = reconcileSelectedKeys(items, state.selectedKeys);
    state.selected = reconciled.indices;
    state.selectedKeys = reconciled.keys;
    emitStateChanged();
  },
  selectAllItems: (options = { multiple: true }) => {
    if (state.req?.items?.length > 0) {
      // Close hovers
      mutations.closeHovers();
      // Clear current selection first
      mutations.resetSelected();
      // Add all items from current directory to selection by their indices
      state.req.items.forEach((_item, index) => {
        mutations.addSelected(index);
      });
      state.selectedKeys = keysForIndices(state.req.items, state.selected);
      if (options.multiple) {
        mutations.setMultiple(true);
      }
    }
  },
  setLastSelectedIndex: (index) => {
    if (index === state.lastSelectedIndex) {
      return;
    }
    state.lastSelectedIndex = index;
    emitStateChanged();
  },
  setRaw: (value) => {
    if (value === state.previewRaw) {
      return;
    }
    state.previewRaw = value;
    emitStateChanged();
  },
  updateCurrentUser: async (value) => {
    // Ensure the input is a valid object
    if (typeof value !== "object" || value === null) return;

    // singleClick is device-scoped. A full settings draft carries the effective
    // value, which is not the user's account default and may be stale after a
    // sidebar toggle; it must never be written to the account profile or merged
    // back over the device layer. Only an explicit single-field call is treated
    // as this browser's intent.
    if (Object.hasOwn(value, "singleClick")) {
      const explicitSingleClick = value.singleClick;
      const singleFieldCall = Object.keys(value).length === 1;
      const withoutSingleClick = { ...value };
      delete withoutSingleClick.singleClick;
      value = withoutSingleClick;
      if (singleFieldCall && typeof explicitSingleClick === "boolean") {
        await mutations.setDeviceSingleClick(explicitSingleClick);
        return;
      }
    }
    // Device records are written only through setDeviceSingleClick/reset, never
    // as part of a whole-profile draft.
    if (Object.hasOwn(value, "devices")) {
      const withoutDevices = { ...value };
      delete withoutDevices.devices;
      value = withoutDevices;
    }
    // Account display defaults are saved only by saveAccountDisplayDefaults.
    // A settings page draft can be stale (it was cloned before another tab
    // changed the default), so a language/toggle save must never replay it.
    if (Object.hasOwn(value, "viewMode") || Object.hasOwn(value, "sorting")) {
      const withoutDisplayDefaults = { ...value };
      delete withoutDisplayDefaults.viewMode;
      delete withoutDisplayDefaults.sorting;
      value = withoutDisplayDefaults;
    }

    // Initialize state.user if it's null
    if (!state.user) {
      state.user = {} as UserObject;
    }
    // Store previous state for comparison
    const previousUser = { ...state.user };
    // A whole-profile save may settle after a logout/account switch. Capture
    // the session it started in; an old failure must not restore the old user
    // object over the next session.
    const updateSessionToken = displayPreferencesSessionToken;
    const updateUsername = state.user.username || "";
    const updateSessionIsActive = () =>
      updateSessionToken === displayPreferencesSessionToken
      && state.user?.username === updateUsername;

    // Merge the new values into the current user state
    state.user = { ...state.user, ...value };

    const isAnonymous = state.user.username === 'anonymous';

    if (value.gallerySize !== undefined && !isAnonymous) {
      try {
        const encoded = url.base64Encode(state.user.username);
        localStorage.setItem(`GallerySize_${encoded}`, value.gallerySize);
      } catch {
        // Gallery size is a compatibility cache; a quota/privacy failure must
        // not abort the account save or the field-level rollback below.
      }
    }

    const syncableProperties = [
      "locale",
      "dateFormat",
      "themeColor",
      "customTheme",
      "quickDownload",
      "preview",
      "stickySidebar",
      "singleClick",
      "darkMode",
      "showHidden",
      "sorting",
      "showFirstLogin",
      "sidebarLinks",
      "showToolsInSidebar",
      "fileLoading",
      "deleteAfterArchive",
      "deleteWithoutConfirming",
      "preferEditorForMarkdown",
      "disablePreviewExt",
      "disableViewingExt",
      "disableOnlyOfficeExt",
      "hideFileExt",
      "disableQuickToggles",
      "disableSearchOptions",
      "hideSidebarFileActions",
      "showCopyPath",
      "hideFilesInTree",
      "editorQuickSave",
      "showSelectMultiple",
      "debugOffice",
      "disableUpdateNotifications",
      "viewMode",
      "gallerySize",
    ];
    const updatedProperties = Object.keys(value).filter(
      (key) =>
        syncableProperties.includes(key) &&
        JSON.stringify(getObjectProperty(previousUser, key)) !==
          JSON.stringify(getObjectProperty(value, key)),
    );
    // Freeze this operation's fields and values before waiting for the locale
    // translator. A later language choice may change state.user in the
    // meantime; this request must not turn into a PATCH of that newer snapshot.
    const sentSnapshot = { ...state.user };

    // Handle locale change. The translator is serialized so an older request
    // cannot finish after a newer choice; only the request that is still the
    // latest intent may write the userLocale cache.
    let localeRequest: { revision: number; applied: Promise<boolean> } | null = null;
    if (state.user.locale !== previousUser.locale) {
      localeRequest = requestLocaleApply(state.user.locale, updateSessionIsActive);
      pendingLocaleOperations.set(localeRequest.revision, state.user.locale || "en");
      const applied = await localeRequest.applied;
      if (
        applied
        && localeRequestIsCurrent(
          localeRequest.revision,
          state.user.locale || "en",
          updateSessionIsActive,
        )
      ) {
        try {
          localStorage.setItem("userLocale", state.user.locale);
        } catch {
          // The translator and account field still move; the cache is optional.
        }
      }
    }

    if (updatedProperties.length > 0) {
      let submittedProperties = updatedProperties;
      const localeSuperseded = localeRequest !== null
        && localeRequest.revision !== localeIntentRevision;
      const newerLocaleAlreadySent = localeRequest !== null
        && latestLocaleSentRevision > localeRequest.revision;
      if (
        localeRequest
        && (
          !updateSessionIsActive()
          || (localeSuperseded && newerLocaleAlreadySent)
        )
      ) {
        // Skip only when this intent no longer belongs to the session, or a
        // newer language has already reached the server. Otherwise send the
        // frozen original value: the newer choice will be sent after it, so
        // the old request cannot turn into a PATCH of the newer snapshot.
        submittedProperties = updatedProperties.filter((key) => key !== "locale");
      }
      if (submittedProperties.length === 0) {
        if (
          localeRequest
          && releaseLocaleOperation(localeRequest.revision, updateSessionIsActive)
        ) {
          await settleLocaleConvergence(updateSessionIsActive);
        }
        emitStateChanged();
        return;
      }
      const localeWasSent = submittedProperties.includes("locale");
      if (
        localeRequest
        && !localeWasSent
        && releaseLocaleOperation(localeRequest.revision, updateSessionIsActive)
      ) {
        await settleLocaleConvergence(updateSessionIsActive);
      }
      if (localeWasSent && localeRequest) {
        latestLocaleSentRevision = Math.max(
          latestLocaleSentRevision,
          localeRequest.revision,
        );
      }
      try {
        await usersApi.update(sentSnapshot, submittedProperties);
        if (localeWasSent && localeRequest) {
          const settled = confirmLocaleOperation(
            localeRequest.revision,
            sentSnapshot.locale || "en",
            updateSessionIsActive,
          );
          if (settled) {
            await settleLocaleConvergence(updateSessionIsActive);
          }
        }
      } catch (e) {
        if (updateSessionIsActive()) {
          if (localeRequest) {
            releaseLocaleOperation(localeRequest.revision, updateSessionIsActive);
          }
          // Settle only the fields this request actually submitted. Locale is
          // owned by its intent revision, not by value equality: if a newer
          // language was chosen, even the same string can belong to that newer
          // operation and must not be rolled back here.
          let rollback: Record<string, unknown> = {};
          const localeStillOwned = localeWasSent
            && localeRequest !== null
            && localeRequest.revision === localeIntentRevision
            && (state.user.locale || "en") === (sentSnapshot.locale || "en");
          for (const key of submittedProperties) {
            if (key === "locale") {
              if (localeStillOwned) {
                rollback = setObjectProperty(rollback, "locale", confirmedLocale);
              }
              continue;
            }
            const currentValue = getObjectProperty(state.user, key);
            const sentValue = getObjectProperty(sentSnapshot, key);
            if (JSON.stringify(currentValue) === JSON.stringify(sentValue)) {
              rollback = setObjectProperty(rollback, key, getObjectProperty(previousUser, key));
            }
          }
          const localeRolledBack = Object.hasOwn(rollback, "locale");
          if (Object.keys(rollback).length > 0) {
            state.user = { ...state.user, ...rollback };
          }
          if (localeRolledBack) {
            const prevLocale = confirmedLocale || "en";
            const localeRollback = requestLocaleApply(prevLocale, updateSessionIsActive);
            const applied = await localeRollback.applied;
            // A newer language may have been chosen while setLocale was in
            // flight; it owns the translator and the cache now.
            if (
              applied
              && localeRollback.revision === localeIntentRevision
              && updateSessionIsActive()
              && (state.user.locale || "en") === prevLocale
            ) {
              try {
                localStorage.setItem("userLocale", prevLocale);
              } catch {
                // The locale still reverts in memory; the cache is optional.
              }
            }
          }
          if (localeWasSent && localeRequest) {
            await settleLocaleConvergence(updateSessionIsActive);
          }
          notify.showError(e);
        }
        emitStateChanged();
        throw e;
      }
    }
    // Emit state change event
    emitStateChanged();
  },
  replaceRequest: (value) => {
    // A refreshed listing for the same directory keeps the selection: keys are
    // re-mapped onto the new indices. A different directory clears it.
    const sameDirectory = Boolean(
      value?.items &&
      state.req &&
      value.path === state.req.path &&
      (value.source || "") === (state.req.source || "")
    );
    const preservedKeys = sameDirectory
      ? (state.selectedKeys.length > 0
        ? [...state.selectedKeys]
        : selectedKeysFor(state.req?.items || [], state.selected))
      : [];
    if (!sameDirectory) {
      state.selected = [];
      state.selectedKeys = [];
      mutations.setMultiple(false);
    }
    if (!value?.items) {
      state.req = value;
      state.selected = [];
      state.selectedKeys = [];
      setActiveViewGrantScope(value?.source ?? "");
      emitStateChanged();
      return
    }
    // Use the response's own folder identity: state.req still points at the
    // previous directory on the first load, and this call happens before the
    // new request is installed below.
    const sorting = getters.sortingForRequest(value);
    const sortby = sorting.by;
    const asc = sorting.asc;
    // Separate directories and files
    const dirs = value.items.filter((item) => item.type === 'directory');
    const files = value.items.filter((item) => item.type !== 'directory');
    // Sort them separately
    const sortedDirs = sortedItems(dirs, sortby, asc);
    const sortedFiles = sortedItems(files, sortby, asc);
    value.items = [...sortedDirs, ...sortedFiles];
    value.items.forEach((item, index) => {
      item.index = index;
    });
    const selectionBefore = state.selected.length;
    if (sameDirectory && preservedKeys.length > 0) {
      const reconciled = reconcileSelectedKeys(value.items, preservedKeys);
      state.selected = reconciled.indices;
      state.selectedKeys = reconciled.keys;
      if (reconciled.indices.length === 0) {
        mutations.setMultiple(false);
      }
    }
    if (sameDirectory && state.selected.length !== selectionBefore) {
      diagnosticsSelectionChange({
        operation: "reconcile",
        before: selectionBefore,
        after: state.selected.length,
        known_items: value.items.length,
        multiple: state.multiple,
        merged_count: 1,
      });
    }
    state.req = value;
    setActiveViewGrantScope(value?.source ?? "");
    emitStateChanged();
  },
  /** Append one page of items, re-index them and keep the key-based selection. */
  appendListingItems: (items) => {
    if (!Array.isArray(items) || items.length === 0) return;
    if (!Array.isArray(state.req?.items)) return;
    const target = state.req.items;
    let index = target.length;
    for (const item of items) {
      item.index = index;
      target.push(item);
      index += 1;
    }
    mutations.reconcileSelection(target);
    emitStateChanged();
  },
  patchListingMetadata: (items, metadataMap, mergeForPath = null) => {
    if (!items?.length || !metadataMap?.size) {
      return;
    }
    for (const item of items) {
      if (!metadataMap.has(item.name)) continue;
      const metadata = metadataMap.get(item.name);
      item.metadata = item.path === mergeForPath
        ? { ...metadata, ...item.metadata }
        : metadata;
    }
    emitStateChanged();
  },
  patchRequestMetadata: (metadataItems) => {
    if (!state.req?.items || !metadataItems?.length) {
      return;
    }
    const byName = new Map();
    for (const e of metadataItems) {
      if (e.metadata !== null) {
        byName.set(e.name, e.metadata);
      }
    }
    mutations.patchListingMetadata(state.req.items, byName);
  },
  /** Merge media fields from GET /api/media/metadata (single file). Replace req object so Vue watchers see the update. */
  patchRequestFileMediaMetadata: (enriched) => {
    if (
      !state.req ||
      state.req.type === "directory" ||
      !enriched ||
      enriched.type === "directory"
    ) {
      return;
    }
    const next = { ...state.req };
    if (enriched.metadata !== undefined) {
      next.metadata = enriched.metadata;
    }
    if (enriched.subtitles !== undefined) {
      next.subtitles = enriched.subtitles;
    }
    if (enriched.hasPreview !== undefined) {
      next.hasPreview = enriched.hasPreview;
    }
    state.req = next;
    emitStateChanged();
  },
  setRequestViewToken: (viewToken) => {
    if (!state.req || !viewToken || state.req.viewToken === viewToken) {
      return;
    }
    state.req = { ...state.req, viewToken };
    emitStateChanged();
  },
  clearRequest: () => {
    // Set req to null to prevent API calls with empty paths
    // Components should check for null req before accessing
    state.req = null;
    state.selected = [];
    mutations.setMultiple(false);
    emitStateChanged();
  },
  setRoute: (value) => {
    if (value === state.route) {
      return;
    }
    state.route = value;
    emitStateChanged();
  },
  /**
   * One entry point for a view toggle. A real folder writes only its own
   * viewMode; a tool result list (advanced search) keeps a page-local view.
   * No account default is ever changed here.
   */
  setCurrentListingViewMode: (mode) => {
    const viewMode = normalizeViewMode(mode);
    if (viewMode === null || getters.viewModeChangeLocked()) {
      return false;
    }
    const context = getters.folderDisplayContext();
    if (context?.isDirectory) {
      return mutations.updateDisplayPreferences({ viewMode });
    }
    if (!context && getters.currentView() === "tools") {
      return mutations.setListingContextOverride({ viewMode });
    }
    return false;
  },
  /**
   * One entry point for a sorting choice. A real folder remembers the whole
   * field+direction pair; tool result lists stay page-local. state.user.sorting
   * (the account default) is never touched.
   */
  setCurrentListingSorting: (field, asc) => {
    const sorting = normalizeSorting({ by: field, asc });
    if (sorting === null) {
      return false;
    }
    const context = getters.folderDisplayContext();
    if (context?.isDirectory) {
      return mutations.updateDisplayPreferences({ sorting });
    }
    if (!context && getters.currentView() === "tools") {
      return mutations.setListingContextOverride({ sorting });
    }
    return false;
  },
  /** Page-local view/sorting for a result list that is not a folder. */
  setListingContextOverride: (patch) => {
    const user = state.user;
    if (!user?.username || getters.folderDisplayContext()) {
      return false;
    }
    const key = state.route?.path || "";
    const current = state.listingContextOverride;
    const sameContext = current.key === key && current.username === user.username;
    let viewMode = sameContext ? current.viewMode : "";
    let sorting = sameContext ? current.sorting : null;
    if (Object.hasOwn(patch, "viewMode")) {
      const next = normalizeViewMode(patch.viewMode);
      if (next === null) {
        return false;
      }
      viewMode = next;
    }
    if (Object.hasOwn(patch, "sorting")) {
      const next = normalizeSorting(patch.sorting);
      if (next === null) {
        return false;
      }
      sorting = next;
    }
    if (
      sameContext
      && viewMode === current.viewMode
      && JSON.stringify(sorting) === JSON.stringify(current.sorting)
    ) {
      return false;
    }
    state.listingContextOverride = { key, username: user.username, viewMode, sorting };
    emitStateChanged();
    return true;
  },
  /** Remove only this folder's remembered view; sorting stays. */
  resetFolderViewMode: () => {
    return applyCurrentFolderPreferenceChange((account, context) => {
      const found = readFolderPreference(account, context.sourceKey, context.candidateKeys);
      return removeFolderPreferenceFieldFromTree(account, context.sourceKey, found, "viewMode");
    });
  },
  /** Remove only this folder's remembered sorting; viewMode stays. */
  resetFolderSorting: () => {
    return applyCurrentFolderPreferenceChange((account, context) => {
      const found = readFolderPreference(account, context.sourceKey, context.candidateKeys);
      return removeFolderPreferenceFieldFromTree(account, context.sourceKey, found, "sorting");
    });
  },
  /**
   * Save explicit account display defaults. Each field is its own PATCH, so a
   * failure rolls back only that field. The newest intent for a field wins;
   * late responses from an older intent/session are ignored.
   */
  saveAccountDisplayDefaults: async (patch) => {
    if (
      !state.user
      || state.user.username === "anonymous"
      || getters.isShare()
      || typeof patch !== "object"
      || patch === null
    ) {
      return false;
    }
    const changes: {
      field: "viewMode" | "sorting";
      value: string | { by: string; asc: boolean };
    }[] = [];
    if (Object.hasOwn(patch, "viewMode")) {
      const viewMode = normalizeViewMode(patch.viewMode);
      if (viewMode === null) {
        return false;
      }
      if (!getters.isAdmin() && state.enforcedUserDefaults?.listing?.viewMode) {
        return false;
      }
      changes.push({ field: "viewMode", value: viewMode });
    }
    if (Object.hasOwn(patch, "sorting")) {
      const sorting = normalizeSorting(patch.sorting);
      if (sorting === null) {
        return false;
      }
      changes.push({ field: "sorting", value: sorting });
    }
    if (changes.length === 0) {
      return false;
    }

    const username = state.user.username;
    const token = displayPreferencesSessionToken;
    const sessionActive = () =>
      token === displayPreferencesSessionToken && state.user?.username === username;
    const runs: Promise<boolean>[] = [];

    for (const change of changes) {
      const { field, value } = change;
      const revision = bumpDefaultRevision(field);
      if (field === "viewMode") {
        state.user.viewMode = value as string;
      } else {
        state.user.sorting = value as { by: string; asc: boolean };
      }
      setDefaultPending(field, true);
      state.accountDefaultsSave.error = "";
      emitStateChanged();

      const run = displayDefaultsSaveChain.then(async (): Promise<boolean> => {
        if (!sessionActive() || readDefaultRevision(field) !== revision) {
          return false;
        }
        const target = field === "viewMode"
          ? ({ username, viewMode: value as string } as UserObject)
          : ({ username, sorting: value as { by: string; asc: boolean } } as UserObject);
        try {
          await usersApi.update(target, [field]);
          if (!sessionActive() || readDefaultRevision(field) !== revision) {
            return false;
          }
          if (field === "viewMode") {
            displayDefaultConfirmed.viewMode = value as string;
          } else {
            displayDefaultConfirmed.sorting = value as { by: string; asc: boolean };
          }
          state.accountDefaultsSave.error = "";
          return true;
        } catch (error) {
          if (!sessionActive() || readDefaultRevision(field) !== revision) {
            return false;
          }
          // Roll back only the field this request was for; other state,
          // including the 005 device override, is untouched.
          if (field === "viewMode") {
            state.user.viewMode = displayDefaultConfirmed.viewMode;
          } else {
            state.user.sorting = { ...displayDefaultConfirmed.sorting };
          }
          state.accountDefaultsSave.error =
            (error as Error | undefined)?.message || String(error);
          notify.showError(state.accountDefaultsSave.error);
          return false;
        } finally {
          if (sessionActive() && readDefaultRevision(field) === revision) {
            setDefaultPending(field, false);
            emitStateChanged();
          }
        }
      });
      displayDefaultsSaveChain = run.then(() => undefined, () => undefined);
      runs.push(run);
    }

    const results = await Promise.all(runs);
    return results.every(Boolean);
  },
  /** Legacy name used by ListingHeader/Files: now folder/tool scoped only. */
  updateListingSortConfig: ({ field, asc }) => {
    const changed = mutations.setCurrentListingSorting(field, asc);
    emitStateChanged();
    return changed;
  },
  updateListingItems: () => {
    mutations.replaceRequest(state.req);
    emitStateChanged();
  },
  updateClipboard: (value) => {
    if (value.key === state.clipboard.key &&
        JSON.stringify(value.items) === JSON.stringify(state.clipboard.items) &&
        value.path === state.clipboard.path) {
      return;
    }
    state.clipboard.key = value.key;
    state.clipboard.items = value.items;
    state.clipboard.path = value.path;
    emitStateChanged();
  },
  resetClipboard: () => {
    state.clipboard.key = "";
    state.clipboard.items = [];
    emitStateChanged();
  },
  setSharePassword: (value) => {
    if (value === state.sharePassword) {
      return;
    }
    state.sharePassword = value;
    emitStateChanged();
  },
  setSearch: (value) => {
    if (value === state.isSearchActive) {
      return;
    }
    state.isSearchActive = value;
    emitStateChanged();
  },
  resetAll: () => {
    state.isSearchActive = false;
    state.selected = [];
    mutations.setMultiple(false);
    emitStateChanged();
  },
  showTooltip(value) {
    const useComponent = Boolean(value.component);
    state.tooltip.content = useComponent ? "" : (value.content ?? "");
    state.tooltip.component = useComponent ? markRaw(value.component) : null;
    state.tooltip.componentProps = useComponent ? (value.componentProps ?? {}) : null;
    state.tooltip.x = value.x;
    state.tooltip.y = value.y;
    state.tooltip.width = value.width ?? null;
    state.tooltip.pointerEvents = value.pointerEvents ?? false;
    state.tooltip.show = true;
    emitStateChanged();
  },
  hideTooltip(force=false) {
    if (!state.tooltip.show) {
      if (force) {
        emitStateChanged();
      }
      return;
    }
    state.tooltip.show = false;
    state.tooltip.content = "";
    state.tooltip.component = null;
    state.tooltip.componentProps = null;
    state.tooltip.width = null;
    state.tooltip.pointerEvents = false;
    emitStateChanged();
  },
  setMaxConcurrentUpload: (value) => {
    if (!state.user.fileLoading) {
      state.user.fileLoading = {};
    }
    if (value === state.user.fileLoading.maxConcurrentUpload) {
      return;
    }
    state.user.fileLoading.maxConcurrentUpload = value;
    emitStateChanged();
  },
  /**
   * Internal folder-memory writer (also used by tests). UI code goes through
   * setCurrentListingViewMode / setCurrentListingSorting so tool routes and
   * unconfirmed folder identities are rejected first.
   */
  updateDisplayPreferences: (payload) => {
    if (typeof payload !== "object" || payload === null) {
      return false;
    }
    const change: Record<string, unknown> = {};
    if (Object.hasOwn(payload, "viewMode")) {
      const viewMode = normalizeViewMode(payload.viewMode);
      if (viewMode === null) {
        return false;
      }
      change.viewMode = viewMode;
    }
    if (Object.hasOwn(payload, "sorting")) {
      const sorting = normalizeSorting(payload.sorting);
      if (sorting === null) {
        return false;
      }
      change.sorting = sorting;
    }
    if (Object.keys(change).length === 0) {
      return false;
    }
    return applyCurrentFolderPreferenceChange((account, context) => {
      const found = readFolderPreference(account, context.sourceKey, context.candidateKeys);
      return setFolderPreference(
        account,
        context.sourceKey,
        found?.key || context.folderKey,
        patchFolderPreference(found?.preference, change),
      );
    });
  },
  setNavigationEnabled: (enabled) => {
    if (state.navigation.enabled === enabled) {
      return;
    }
    state.navigation.enabled = enabled;
    if (!enabled) {
      mutations.clearNavigation();
    }
    emitStateChanged();
  },
  setupNavigation: ({ listing, currentItem, directoryPath }) => {
    // Cancel any pending auto-hide from a previous setupNavigation; the raw
    // setTimeout used to leak and could fire setNavigationShow(false) right
    // after opening a new image (nextPrevious flicker).
    mutations.clearNavigationTimeout();
    state.navigation.listing = listing;
    state.navigation.currentIndex = -1;
    state.navigation.previousItem = null;
    state.navigation.nextItem = null;
    state.navigation.previousLink = "";
    state.navigation.nextLink = "";
    state.navigation.previousRaw = "";
    state.navigation.nextRaw = "";

    if (!listing || !currentItem) {
      emitStateChanged();
      return;
    }

    // Sort listing according to sorting preferences
    const sorting = getters.sorting();
    listing = sortedItems(listing, sorting.by, sorting.asc);

    // Find current item index in the listing
    for (let i = 0; i < listing.length; i++) {
      if (listing.at(i).name === currentItem.name) {
        state.navigation.currentIndex = i;
        break;
      }
    }

    if (state.navigation.currentIndex === -1) {
      emitStateChanged();
      return;
    }

    // Find previous item (skip directories)
    for (const item of listing.slice(0, state.navigation.currentIndex).reverse()) {
      if (item.type === 'directory') continue;

      item.path = url.joinPath(directoryPath, item.name);
      state.navigation.previousItem = item;
      state.navigation.previousLink = url.buildItemUrl(item.source, item.path);

      if (getTypeInfo(item.type).simpleType === "image") {
        const prefetch = mutations.getPrefetchUrl(item);
        if (prefetch) {
          state.navigation.previousRaw = prefetch;
        }
      }
      break;
    }

    // Find next item (skip directories)
    for (const item of listing.slice(state.navigation.currentIndex + 1)) {
      if (item.type === 'directory') continue;

      item.path = url.joinPath(directoryPath, item.name);
      state.navigation.nextItem = item;
      state.navigation.nextLink = url.buildItemUrl(item.source, item.path);

      if (getTypeInfo(item.type).simpleType === "image") {
        const prefetch = mutations.getPrefetchUrl(item);
        if (prefetch) {
          state.navigation.nextRaw = prefetch;
        }
      }
      break;
    }

    emitStateChanged();

    // Auto-show navigation when it's first set up (timer tracked so new opens clear it)
    if (state.navigation.enabled && (state.navigation.previousLink || state.navigation.nextLink)) {
      const isImage = getTypeInfo(currentItem.type).simpleType === 'image';
      if (isImage) {
        mutations.showNavigationChromePersistent();
      } else {
        mutations.setNavigationShow(true);
        const hideTimer = setTimeout(() => {
          if (!state.navigation.hoverNav) {
            mutations.setNavigationShow(false);
          }
        }, 3000);
        mutations.setNavigationTimeout(hideTimer);
      }
    }
  },
  getPrefetchUrl: (item) => {
    const viewToken = item.viewToken;
    const typeHint = item.type || item.name;
    if (getters.isShare()) {
      return resourcesApi.getViewURL(
        item.source,
        item.path,
        viewToken,
        {
          path: item.path,
          hash: state.shareInfo.hash,
        },
        false,
        typeHint,
      );
    }
    return resourcesApi.getViewURL(item.source, item.path, viewToken, null, false, typeHint);
  },
  setNavigationShow: (show) => {
    if (state.navigation.show === show) {
      return;
    }
    state.navigation.show = show;
    emitStateChanged();
  },
  showNavigationChromePersistent: () => {
    if (!state.navigation.enabled) {
      return;
    }
    mutations.clearNavigationTimeout();
    mutations.setNavigationShow(true);
  },
  toggleNavigationChrome: () => {
    if (!state.navigation.enabled) {
      return;
    }
    mutations.clearNavigationTimeout();
    mutations.setNavigationShow(!state.navigation.show);
  },
  /** Briefly reveal prev/next chrome (e.g. edge tap on video while gestures capture the event). */
  peekNavigationChrome: (side = null) => {
    if (!state.navigation.enabled) {
      return;
    }
    const queueNav = getters.isPreviewPlaybackQueueNavMode();
    const hasPrevious = queueNav
      ? getters.playbackQueueCanGoPrevious()
      : Boolean(state.navigation.previousLink);
    const hasNext = queueNav
      ? getters.playbackQueueCanGoNext()
      : Boolean(state.navigation.nextLink);
    if (side === 'left' && !hasPrevious) {
      return;
    }
    if (side === 'right' && !hasNext) {
      return;
    }
    if (!side && !hasPrevious && !hasNext) {
      return;
    }
    if (getters.previewType() === 'image') {
      mutations.showNavigationChromePersistent();
      return;
    }
    mutations.setNavigationShow(true);
    mutations.clearNavigationTimeout();
    const hideTimer = setTimeout(() => {
      if (!state.navigation.hoverNav) {
        mutations.setNavigationShow(false);
      }
      mutations.clearNavigationTimeout();
    }, 3000);
    mutations.setNavigationTimeout(hideTimer);
  },
  setNavigationHover: (hover) => {
    if (state.navigation.hoverNav === hover) {
      return;
    }
    state.navigation.hoverNav = hover;
    emitStateChanged();
  },
  setNavigationGestureHint: (payload: { kind?: null | 'previous' | 'next' | 'close', commitReady?: boolean, flashClose?: boolean } = {}) => {
    const kind = payload.kind ?? null;
    const commitReady = !!payload.commitReady;
    const flashClose = !!payload.flashClose;
    if (
      state.navigation.gestureHint === kind &&
      state.navigation.gestureHintCommitReady === commitReady &&
      state.navigation.gestureHintFlashClose === flashClose
    ) {
      return;
    }
    state.navigation.gestureHint = kind;
    state.navigation.gestureHintCommitReady = commitReady;
    state.navigation.gestureHintFlashClose = flashClose;
    emitStateChanged();
  },
  setNavigationTimeout: (timeout) => {
    if (state.navigation.timeout) {
      clearTimeout(state.navigation.timeout);
    }
    state.navigation.timeout = timeout;
  },
  clearNavigationTimeout: () => {
    if (state.navigation.timeout) {
      clearTimeout(state.navigation.timeout);
      state.navigation.timeout = null;
    }
  },
  clearNavigation: () => {
    state.navigation.show = false;
    state.navigation.hoverNav = false;
    state.navigation.gestureHint = null;
    state.navigation.gestureHintCommitReady = false;
    state.navigation.gestureHintFlashClose = false;
    state.navigation.listing = null;
    state.navigation.currentIndex = -1;
    state.navigation.previousItem = null;
    state.navigation.nextItem = null;
    state.navigation.previousLink = "";
    state.navigation.nextLink = "";
    state.navigation.previousRaw = "";
    state.navigation.nextRaw = "";
    mutations.clearNavigationTimeout();
    emitStateChanged();
  },
  setNavigationTransitioning: (isTransitioning) => {
    if (isTransitioning === state.navigation.isTransitioning) {
      return;
    }
    state.navigation.isTransitioning = isTransitioning;
    if (isTransitioning) {
      state.navigation.transitionStartTime = Date.now();
      // Safety timeout: if transition takes more than 5 seconds, clear it
      setTimeout(() => {
        if (state.navigation.isTransitioning &&
            state.navigation.transitionStartTime &&
            Date.now() - state.navigation.transitionStartTime > 5000) {
          mutations.setNavigationTransitioning(false);
        }
      }, 5500);
    } else {
      state.navigation.transitionStartTime = null;
    }
    emitStateChanged();
  },
  setPlaybackQueue: (playback) => {
    state.playbackQueue.queue = playback.queue || [];
    state.playbackQueue.currentIndex = playback.currentIndex ?? -1;
    state.playbackQueue.mode = playback.mode || 'single';
    state.playbackQueue.loop = playback.loop || 'off';
    try {
      sessionStorage.setItem('playbackQueue', JSON.stringify({
        queue: state.playbackQueue.queue,
        currentIndex: state.playbackQueue.currentIndex,
        mode: state.playbackQueue.mode,
        loop: state.playbackQueue.loop,
      }));
    } catch (_) { /* ignore */ }
    emitStateChanged();
  },
  setPlaybackState: (isPlaying) => {
    if (isPlaying === state.playbackQueue.isPlaying) {
      return;
    }
    state.playbackQueue.isPlaying = isPlaying;
    emitStateChanged();
  },
  navigateToQueueIndex: (index) => {
    if (index < 0 || index >= state.playbackQueue.queue.length) return;
    const item = state.playbackQueue.queue.at(index);
    state.playbackQueue.currentIndex = index;
    // Update the current request to trigger navigation
    mutations.replaceRequest(item);
    emitStateChanged();
  },
  togglePlayPause: () => {
    state.playbackQueue.shouldTogglePlayPause = !state.playbackQueue.shouldTogglePlayPause;
    emitStateChanged();
  },
  setShareInfo: (shareInfo) => {
    const merged = { ...state.shareInfo, ...shareInfo };
    if (shareInfo.passwordValid === undefined && state.shareInfo.passwordValid !== undefined) {
      merged.passwordValid = state.shareInfo.passwordValid;
    }
    if (JSON.stringify(merged) === JSON.stringify(state.shareInfo)) {
      return;
    }
    state.shareInfo = merged;
    updateManifestLink();
    emitStateChanged();
  },
  setSidebarWidth: (value) => {
    // Ensure width is within bounds
    const minWidth = state.sidebar.minWidth;
    const maxWidth = state.sidebar.maxWidth;
    const newWidth = Math.max(minWidth, Math.min(value, maxWidth));
    if (newWidth === state.sidebar.width) {
      return;
    }
    state.sidebar.width = newWidth;
    localStorage.setItem("sidebarWidth", newWidth.toString());
    emitStateChanged();
  },
  setSidebarResizing: (value) => {
    if (value === state.sidebar.isResizing) {
      return;
    }
    state.sidebar.isResizing = value;
    emitStateChanged();
  },
  setSidebarMode(value) {
    const newMode = value === 'navigation' ? 'navigation' : 'links';
    if (newMode === state.sidebar.mode) return;
    state.sidebar.mode = newMode;
    localStorage.setItem('sidebarMode', newMode);
    emitStateChanged();
  },
};