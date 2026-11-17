<!-- Modified from FileBrowser Quantum for the mument derivative. See MODIFICATIONS.md. -->
<template>
  <div>
    <div v-if="loadingProgress < 100" class="progress-line" :style="{ width: `${loadingProgress}%`, ...moveWithSidebar }"></div>
    <errors v-if="error" :errorCode="error.status" />
    <component v-else-if="currentViewLoaded" :is="currentView" :fbdata="req"></component>
    <div v-else>
      <h2 class="message delayed">
        <LoadingSpinner size="medium" />
        <span>{{ $t("general.loading", { suffix: "..." }) }}</span>
      </h2>
    </div>
  </div>
</template>

<script>
import { createAsyncComponent } from "@/utils/asyncComponent.js";
import { resourcesApi, shareApi, mediaApi } from "@/api";
import Errors from "@/views/Errors.vue";
import Preview from "@/views/files/Preview.vue";
import ListingView from "@/views/files/ListingView.vue";
import { state, mutations, getters } from "@/store";
import router from "@/router";
import { extractSourceFromPath, removeLastDir, base64Encode, removeTrailingSlash } from "@/utils/url.js";
import LoadingSpinner from "@/components/LoadingSpinner.vue";
import { globalVars } from "@/utils/constants";
import { syncEventTheme } from "@/utils/theme";
import { isRichTextPreviewMimeType } from "@/utils/mimetype";
import { invalidateDirMetadataCache } from "@/utils/metadataCache.js";
import { showShareDownloadPrompt as openShareDownloadPrompt } from "@/utils/download.js";
import { takeListAnchor } from "@/utils/listAnchor.js";
import {
  cancelAllListingReveals,
  clearListingRestorePreparation,
  prepareListingRestore,
  revealListingTarget,
} from "@/utils/listingMountBridge.js";
import {
  beginListingLifecycle,
  currentListingLifecycleId,
  invalidateListingLifecycle,
  updateListingLifecycleIdentity,
} from "@/utils/listingLifecycle.js";
import { createAnchorRestore } from "@/utils/listingRestore.js";
import { fetchListingPage, ListingRevisionError } from "@/api/listing";
import { isPagedListingEnabled } from "@/utils/pagedListingPref";
import { getDiagnosticsRuntime } from "@/utils/diagnostics/runtime.js";
import { setActiveLoad, clearActiveLoad } from "@/utils/diagnostics/listingBridge.js";
import { getMosaicDensity } from "@/utils/mosaicDensity.js";

function directoryListingHasMediaChildren(req) {
  return (
    req?.type === "directory" &&
    req.items?.some((i) => i.type?.startsWith("audio") || i.type?.startsWith("video"))
  );
}

/** @returns {Promise<{ items?: object[], name: string, type: string, path: string, source: string, hash?: string, token?: string, parentDirItems?: object[] }>} */
async function fetchShareItemWithParent(sharePassword) {
  let file = await resourcesApi.fetchFilesPublic(
    state.shareInfo.subPath,
    state.shareInfo.hash,
    sharePassword,
    false,
    false
  );
  file.hash = state.shareInfo.hash;
  mutations.setShareData({ passwordValid: true });

  if (file.type === "directory") {
    return file;
  }

  let directoryPath = removeLastDir(state.shareInfo.subPath);
  if (!directoryPath || directoryPath === "") {
    directoryPath = "/";
  }
  const shouldFetchParent = directoryPath !== state.shareInfo.subPath;
  const contentPromise = getters.shouldFetchFileContent(file)
    ? resourcesApi
        .fetchFilesPublic(
          state.shareInfo.subPath,
          state.shareInfo.hash,
          sharePassword,
          true,
          false
        )
        .catch(() => file)
    : Promise.resolve(file);
  const promises = [contentPromise];
  if (shouldFetchParent) {
    promises.push(
      resourcesApi
        .fetchFilesPublic(directoryPath, state.shareInfo.hash, sharePassword, false, false)
        .catch(() => null)
    );
  }
  const results = await Promise.all(promises);
  file = results[0];
  file.hash = state.shareInfo.hash;
  if (shouldFetchParent && results[1]?.items) {
    file.parentDirItems = results[1].items;
  }
  return file;
}

/** @returns {Promise<{ items?: object[], name: string, type: string, path: string, source: string, parentDirItems?: object[] }>} */
async function fetchAuthItemWithParent(fetchSource, fetchPath, diagnostics = null) {
  let res = await resourcesApi.fetchFiles(fetchSource, fetchPath, false, false, false, diagnostics);
  if (res.type === "directory") {
    return res;
  }
  let directoryPath = removeLastDir(res.path);
  if (!directoryPath || directoryPath === "") {
    directoryPath = "/";
  }
  const shouldFetchParent = directoryPath !== res.path;
  const contentPromise = getters.shouldFetchFileContent(res)
    ? resourcesApi.fetchFiles(res.source, res.path, true, false).catch(() => res)
    : Promise.resolve(res);
  const promises = [contentPromise];
  if (shouldFetchParent) {
    promises.push(
      resourcesApi.fetchFiles(res.source, directoryPath, false, false).catch(() => null)
    );
  }
  const results = await Promise.all(promises);
  res = results[0];
  if (shouldFetchParent && results[1]?.items) {
    res.parentDirItems = results[1].items;
  }
  return res;
}

export default {
  name: "files",
  components: {
    Errors,
    Preview,
    ListingView,
    LoadingSpinner,
    Editor: createAsyncComponent(() => import('@/views/files/Editor.vue')),
    OnlyOfficeEditor: createAsyncComponent(() => import('@/views/files/OnlyOfficeEditor.vue')),
    EpubViewer: createAsyncComponent(() => import('@/views/files/EpubViewer.vue')),
    DocViewer: createAsyncComponent(() => import('@/views/files/DocViewer.vue')),
    MarkdownViewer: createAsyncComponent(() => import('@/views/files/MarkdownViewer.vue')),
    ThreeJsViewer: createAsyncComponent(() => import('@/views/files/ThreeJs.vue')),
  },
  data() {
    return {
      error: null,
      width: window.innerWidth,
      lastPath: "",
      lastHash: "",
      diagLoad: null,
      diagRequestRef: "",
      diagTrigger: "initial",
      popupSource: "",
      loadingProgress: 0,
      loadingStartTime: null,
      loadingTimeout: null,
      // Share-specific data
      sharePassword: "",
      attemptedPasswordLogin: false,
      lastShareHash: "",
      // Paged listing state (opt-in via localStorage).
      pagedFeed: null,
      pagedScroller: null,
      pagedScrollHandler: null,
      pagedScrollBound: false,
      pagedLoading: false,
      pagedRequestSeq: 0,
    };
  },
  computed: {
    showShareInfo() {
      return getters.isShare() && getters.isMobile() && state.req.path === "/" && !state.shareInfo?.disableShareCard;
    },
    currentView() {
      return getters.currentView();
    },
    currentViewLoaded() {
      return getters.currentView() !== "";
    },
    req() {
      return state.req;
    },
    reload() {
      return state.reload;
    },
    moveWithSidebar() {
      const style = {
        width: `${this.loadingProgress}%`,
      };
      if (getters.isStickySidebar() && getters.isSidebarVisible()) {
        style.left = `${state.sidebar.width}em`;
      }
      return style;
    },
  },
  created() {
    // Business request generation: a late fetch/reveal must never overwrite a
    // newer navigation, and diagnostics are not part of this decision.
    this._requestSeq = 0;
    this._activeRestore = null;
    this._restoreCleanup = null;
    this._pendingAnchorClaim = null;
    syncEventTheme(getters.eventTheme() === "halloween");
    this.fetchData();
  },
  watch: {
    $route() {
      syncEventTheme(getters.eventTheme() === "halloween");
      this.diagTrigger = "route";
      this.fetchData();
    },
    reload(value) {
      if (value) {
        this.diagTrigger = "reload";
        this.fetchData();
      }
    },
  },
  mounted() {
    window.addEventListener("hashchange", this.scrollToHash);
    window.addEventListener("keydown", this.keyEvent);
  },
  beforeUnmount() {
    this._requestSeq += 1;
    this.abortActiveRestore("unmounted");
    cancelAllListingReveals({ reason: "unmounted" });
    clearListingRestorePreparation();
    invalidateListingLifecycle();
    this.cancelDiagnosticsLoad("unmount");
    this.removePagedScrollListener();
    window.removeEventListener("keydown", this.keyEvent);
  },
  unmounted() {
    mutations.replaceRequest({}); // Use mutation
  },
  methods: {
    diagnosticsRecorder() {
      return getDiagnosticsRuntime()?.recorder || null;
    },
    diagnosticView() {
      const view = getters.viewMode?.() || "unknown";
      return ["mosaic", "gallery", "icons", "list", "compact"].includes(view) ? view : "unknown";
    },
    diagnosticSort() {
      const sorting = getters.sorting?.() || {};
      const by = ["name", "size", "modified", "type"].includes(sorting.by) ? sorting.by : "unknown";
      return { by, asc: sorting.asc !== false };
    },
    startDiagnosticsLoad(source, path, trigger, paged = false) {
      const recorder = this.diagnosticsRecorder();
      if (!recorder?.isStarted() || recorder.getMode() === "off") return null;
      this.cancelDiagnosticsLoad("superseded");
      const sort = this.diagnosticSort();
      const context = recorder.beginLoad({
        path,
        view: this.diagnosticView(),
        sortBy: sort.by,
        sortAsc: sort.asc,
        density: getMosaicDensity(state.user?.username),
        paged,
        trigger,
      });
      if (!context) return null;
      const requestRef = recorder.newRequestRef();
      this.diagLoad = context;
      this.diagRequestRef = requestRef;
      setActiveLoad(context, { source, path });
      return { context, requestRef, recorder };
    },
    cancelDiagnosticsLoad(reason = "superseded", expectedContext = undefined) {
      if (!this.diagLoad) return;
      if (expectedContext !== undefined && this.diagLoad !== expectedContext) return;
      const recorder = this.diagnosticsRecorder();
      if (recorder) recorder.finishLoad(this.diagLoad, "cancel", { reason });
      clearActiveLoad(this.diagLoad);
      this.diagLoad = null;
      this.diagRequestRef = "";
    },
    finishDiagnosticsLoad(res, context) {
      if (!context || this.diagLoad !== context) return;
      if (res?.type !== "directory") {
        this.cancelDiagnosticsLoad("superseded", context);
      }
    },
    recordNavigation({
      reason,
      targetName = null,
      requestedOffset = null,
      actualOffset = null,
      result,
      startedAt,
      preMounted = false,
      strategy = null,
      reasonCode = null,
      operationRef = null,
      context = this.diagLoad,
    } = {}) {
      const recorder = this.diagnosticsRecorder();
      if (!recorder?.isStarted() || recorder.getMode() === "off") return;
      // Diagnostics only decides whether an event is recorded; the scroll has
      // already been applied (or cancelled) by business logic.
      if (!context || this.diagLoad !== context || !recorder.isCurrentContext(context)) return;
      const data = {
        reason,
        target_ref: targetName ? recorder.codeForItem(targetName) : null,
        pre_mounted: Boolean(preMounted),
        requested_offset: Number.isFinite(requestedOffset) ? Math.max(0, requestedOffset) : null,
        actual_offset: Number.isFinite(actualOffset) ? Math.max(0, actualOffset) : null,
        result,
        elapsed_ms: Math.max(0, performance.now() - startedAt),
      };
      if (strategy) data.strategy = strategy;
      if (reasonCode) data.reason_code = reasonCode;
      const eventContext = operationRef ? { ...context, operationRef } : context;
      recorder.record("navigation.restore", data, eventContext);
    },
    scrollToHash() {
      this.scrollToHashFor(this.diagLoad);
    },
    scrollToHashFor(diagContext) {
      const recorder = this.diagnosticsRecorder();
      if (diagContext && recorder && !recorder.isCurrentContext(diagContext)) {
        // Stale logging context: keep the real preview/hash navigation, only
        // skip attributing it to the old diagnostics session.
        diagContext = null;
      }
      const navStartedAt = performance.now();
      const navScroller = this.$el?.closest?.(".scroll-wrapper") || document.scrollingElement;
      let scrollToId = "";
      let targetName = "";
      // scroll to previous item either from location hash or from previousItemHashId state
      // prefers location hash
      const noHashChange = window.location.hash === this.lastHash
      if (noHashChange && state.previousHistoryItem?.name === "") return;
      this.lastHash = window.location.hash;
      if (window.location.hash) {
        const rawHash = window.location.hash.slice(1);
        let decodedName;
        try {
          decodedName = decodeURIComponent(rawHash);
        } catch (_e) {
          // If the hash contains malformed escape sequences, fall back to raw
          decodedName = rawHash;
        }
        targetName = decodedName;
        scrollToId = base64Encode(encodeURIComponent(decodedName));
      } else if (state.previousHistoryItem?.name &&
                 state.previousHistoryItem.path === state.req.path &&
                 state.previousHistoryItem.source === state.req.source) {
        targetName = state.previousHistoryItem?.name;
        scrollToId = base64Encode(encodeURIComponent(state.previousHistoryItem?.name));
      }
      // Don't call getElementById with empty string
      if (!scrollToId || scrollToId.trim() === '') {
        return;
      }
      // Re-select the item we are returning to -- replaces the previous glow effect.
      const targetIndex = state.req?.items?.findIndex((item) => item.name === targetName);
      if (targetIndex !== -1) {
        mutations.addSelected(targetIndex);
      }
      const preMounted = Boolean(document.getElementById(scrollToId));
      const reason = state.previousHistoryItem?.name && state.previousHistoryItem?.path === state.req?.path
        ? "preview_return"
        : "hash";
      // The progressive listing can scroll by geometry before the target card
      // exists; it mounts the target row first. DOM lookup stays as fallback.
      // "Accepted" is not "applied": record the terminal state from the bridge.
      if (revealListingTarget({
        name: targetName,
        anchorId: scrollToId,
        block: "center",
        onSettled: (info) => {
          this.recordNavigation({
            reason,
            targetName,
            requestedOffset: Number.isFinite(info.requestedOffset) ? info.requestedOffset : null,
            actualOffset: Number.isFinite(info.actualOffset) ? info.actualOffset : navScroller?.scrollTop,
            result: info.result === "success" ? "success" : info.result === "cancelled" ? "cancelled" : "failed",
            startedAt: navStartedAt,
            preMounted,
            context: diagContext,
          });
        },
      })) {
        return;
      }
      const element = document.getElementById(scrollToId);
      if (element) {
        element.scrollIntoView({
          behavior: "instant",
          block: "center",
        });
        this.recordNavigation({ reason, targetName, requestedOffset: null, actualOffset: navScroller?.scrollTop, result: "success", startedAt: navStartedAt, preMounted, context: diagContext });
      } else {
        this.recordNavigation({ reason, targetName, requestedOffset: null, actualOffset: navScroller?.scrollTop, result: "cancelled", startedAt: navStartedAt, preMounted, context: diagContext });
      }
    },
    /** Visible media first, then the rest in listing order; batches fill in after paint. */
    visibleFirstMediaItems(items) {
      if (!Array.isArray(items)) {
        return [];
      }
      const media = items.filter(
        (item) => item && (item.type?.startsWith("audio") || item.type?.startsWith("video"))
      );
      if (media.length < 2 || typeof document === "undefined" || !document.getElementById) {
        return media;
      }
      const viewport = window.innerHeight || 0;
      const ranked = media.map((item, index) => {
        let visible = false;
        let top = Number.POSITIVE_INFINITY;
        const id = base64Encode(encodeURIComponent(item.name || ""));
        const element = id ? document.getElementById(id) : null;
        if (element?.getBoundingClientRect) {
          const rect = element.getBoundingClientRect();
          visible = rect.bottom >= 0 && rect.top <= viewport;
          top = rect.top;
        }
        return { item, index, visible, top };
      });
      ranked.sort((a, b) => {
        if (a.visible !== b.visible) {
          return a.visible ? -1 : 1;
        }
        if (a.visible && a.top !== b.top) {
          return a.top - b.top;
        }
        return a.index - b.index;
      });
      return ranked.map((entry) => entry.item);
    },

    /** Opt-in paged listing: returns a legacy-shaped envelope or null to fall back. */
    async loadPagedListingIfEnabled(source, path) {
      if (!isPagedListingEnabled()) return null;
      // The target request has not become state.req yet; resolve the folder
      // memory for (source, path) instead of the previous/empty request.
      const sorting = getters.sortingForTarget(source, path);
      const sortMap = { name: "name", size: "size", modified: "mod_time", mod_time: "mod_time", type: "type" };
      const sort = sortMap[sorting?.by || "name"];
      if (!sort) return null;
      const order = sorting?.asc === false ? "desc" : "asc";
      try {
        const page = await fetchListingPage({ source, path, sort, order, limit: 150 });
        // Pinned items need the legacy envelope semantics for now.
        if (Array.isArray(page.pinnedItems) && page.pinnedItems.length > 0) return null;
        const items = (page.items || []).map((item) => ({ ...item, source }));
        this.pagedFeed = {
          source, path, sort, order,
          cursor: page.nextCursor || "",
          revision: page.revision || "",
          hasMore: Boolean(page.hasMore && page.nextCursor),
        };
        this.$nextTick(() => this.ensurePagedScrollListener());
        return {
          name: page.name || path.split("/").filter(Boolean).pop() || source,
          path: page.path ?? path,
          source,
          type: page.type || "directory",
          items,
          pinnedItems: [],
        };
      } catch (error) {
        return null;
      }
    },

    ensurePagedScrollListener() {
      if (this.pagedScrollBound) return;
      const scroller = document.querySelector(".scroll-wrapper");
      if (!scroller) return;
      this.pagedScroller = scroller;
      this.pagedScrollHandler = () => {
        if (!this.pagedFeed?.hasMore || this.pagedLoading) return;
        if (scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 900) {
          void this.loadMorePaged();
        }
      };
      scroller.addEventListener("scroll", this.pagedScrollHandler, { passive: true });
      this.pagedScrollBound = true;
    },

    removePagedScrollListener() {
      if (this.pagedScroller && this.pagedScrollHandler) {
        this.pagedScroller.removeEventListener("scroll", this.pagedScrollHandler);
      }
      this.pagedScroller = null;
      this.pagedScrollHandler = null;
      this.pagedScrollBound = false;
    },

    async loadMorePaged() {
      const feed = this.pagedFeed;
      if (!feed?.hasMore || this.pagedLoading) return;
      // A sorting change after the first page must not append rows fetched with
      // the old sort/order. Restart the directory with the new effective sort;
      // unsupported fields (e.g. duration) fall back to the legacy listing.
      const sortMap = { name: "name", size: "size", modified: "mod_time", mod_time: "mod_time", type: "type" };
      const expectedFor = (target) => {
        const effective = getters.sortingForTarget(target.source, target.path);
        return {
          sort: sortMap[effective.by],
          order: effective.asc === false ? "desc" : "asc",
        };
      };
      const sortingChanged = (target) => {
        const expected = expectedFor(target);
        return !expected.sort || expected.sort !== target.sort || expected.order !== target.order;
      };
      if (sortingChanged(feed)) {
        this.pagedFeed = null;
        mutations.setReload(true);
        return;
      }
      const requestSeq = (this.pagedRequestSeq || 0) + 1;
      this.pagedRequestSeq = requestSeq;
      this.pagedLoading = true;
      try {
        const page = await fetchListingPage({
          source: feed.source, path: feed.path, sort: feed.sort, order: feed.order,
          limit: 150, cursor: feed.cursor, revision: feed.revision,
        });
        // The response may arrive after a navigation, a new feed, or a sorting
        // change. Drop it before touching the list; a changed sort restarts the
        // directory instead of waiting for the next scroll.
        const stillActiveFeed = this.pagedFeed === feed;
        let stillSameDirectory = true;
        if (typeof state !== "undefined") {
          const req = state.req;
          stillSameDirectory = Boolean(
            req?.type === "directory"
            && req.source === feed.source
            && req.path === feed.path
          );
        }
        if (!stillActiveFeed || !stillSameDirectory) {
          if (stillActiveFeed) {
            this.pagedFeed = null;
            mutations.setReload(true);
          }
          return;
        }
        if (sortingChanged(feed)) {
          this.pagedFeed = null;
          mutations.setReload(true);
          return;
        }
        const items = (page.items || []).map((item) => ({ ...item, source: feed.source }));
        mutations.appendListingItems(items);
        feed.cursor = page.nextCursor || "";
        feed.revision = page.revision || feed.revision;
        feed.hasMore = Boolean(page.hasMore && page.nextCursor);
      } catch (error) {
        if (error instanceof ListingRevisionError) {
          this.pagedFeed = null;
          void this.fetchData();
        }
      } finally {
        if ((this.pagedRequestSeq || 0) === requestSeq) {
          this.pagedLoading = false;
        }
      }
    },

    async patchMediaMetadataIfNeeded(listing, source) {
      if (!directoryListingHasMediaChildren(listing)) {
        this.loadingProgress = 100;
        return;
      }
      // The listing is already rendered. Never hold the first paint for probes;
      // batches patch in place as they return.
      this.loadingProgress = 100;
      await this.$nextTick();
      const isShare = getters.isShare();
      const path = listing.path || (isShare ? state.shareInfo?.subPath : listing.path);
      const currentSource = listing.source ?? source;
      const currentHash = state.shareInfo?.hash;
      const isCurrentListing = () => isShare
        ? getters.isShare() && state.shareInfo?.hash === currentHash && state.req?.path === path
        : !getters.isShare() && state.req?.source === currentSource && state.req?.path === path;
      if (!isCurrentListing()) {
        return;
      }
      const items = this.visibleFirstMediaItems(listing.items);
      if (!items.length) {
        return;
      }
      const onBatch = (chunk) => {
        if (!isCurrentListing() || !chunk?.size || !state.req.items) {
          return;
        }
        mutations.patchListingMetadata(state.req.items, chunk);
      };
      try {
        const metaMap = isShare
          ? await mediaApi.getDirectoryMetadataMap(path || state.shareInfo.subPath, {
              isShare: true,
              hash: currentHash,
              password: this.sharePassword,
              items,
              onBatch,
            })
          : await mediaApi.getDirectoryMetadataMap(path, { source: currentSource, items, onBatch });
        if (isCurrentListing() && metaMap?.size) {
          mutations.patchListingMetadata(state.req.items, metaMap);
        }
      } catch (_e) {
        // A failed metadata batch must not disturb the visible listing.
      }
    },

    async fetchData() {
      // Every fetch is a new business generation. Starting one invalidates
      // older results and anchors even if they target the same directory.
      const requestId = ++this._requestSeq;
      // A new fetch owns the page. Drop the old restore gesture/callback and
      // any accepted-but-unsettled controller reveal before doing any work.
      this.abortActiveRestore("superseded");
      cancelAllListingReveals({ reason: "superseded" });
      const previousLifecycleId = currentListingLifecycleId();
      // Route identity, not request generation: a same-directory reload keeps
      // the view id, while A -> B -> A gets a new one.
      beginListingLifecycle({ routeKey: getters.routePath() });
      let claimedAnchor = null;
      let claimedAnchorSource = "";
      let claimedAnchorPath = "";
      this.diagTrigger = this.diagTrigger || "initial";
      const hash = getters.shareHash();
      const isShare = hash !== "";

      // Fetch and store share info if this is a share
      if (isShare) {
        const shareInfo = await shareApi.getShareInfoPublic(hash);
        if (requestId !== this._requestSeq) return;

        // Check if the response is an error (has status field indicating error)
        if (!shareInfo || shareInfo.status >= 400) {
          // show message that share is invalid and don't do anything else
          this.error = {
            status: shareInfo?.status || "share404",
            message: shareInfo?.message || "errors.shareNotFound",
          };
          this.loadingProgress = 0;
          return;
        }

        // Valid share - add the hash and other required fields, then store in state
        shareInfo.hash = hash;

        const sameShare =
          this.lastShareHash.length === hash.length &&
          this.lastShareHash.indexOf(hash) === 0;
        if (this.lastShareHash && !sameShare) {
          this.sharePassword = "";
        }
        this.lastShareHash = hash;

        // Parse share route to get subPath
        const urlPath = getters.routePath('public/share')
        const parts = urlPath.split("/");
        // Decode each part since URL paths are encoded
        const decodedParts = parts.slice(2).map(part => decodeURIComponent(part));
        shareInfo.subPath = `/${decodedParts.join("/")}`;
        // Set shareInfo in state
        mutations.setShareInfo(shareInfo);

        // Check for password requirement (applies to both regular and upload shares)
        if (shareInfo.hasPassword) {
          if (this.sharePassword === "") {
            this.sharePassword = localStorage.getItem(`sharepass:${shareInfo.hash}`);
            if (this.sharePassword === null || this.sharePassword === "") {
              this.showPasswordPrompt();
              return;
            }
          }
        }

        if (shareInfo.themeColor) {
          document.documentElement.style.setProperty("--primaryColor", shareInfo.themeColor);
        }

        // Handle password (same for both regular and upload shares)
        if (this.sharePassword === null) {
          this.sharePassword = "";
        }
      }

      if (state.deletedItem) {
        return;
      }

      // Entering a directory is not a sorting choice. Missing defaults are
      // resolved by getters.sorting() without creating folder memory.

      // Set loading and reset error
      mutations.setLoading(isShare ? "share" : "files", true);
      this.error = null;
      mutations.setReload(false);
      let diagContext = null;

      try {
        if (isShare) {
          // For upload shares, validate password on startup and return early
          // Password validation happens via fetchPub call, which will throw 401 if incorrect
          // A 501 error means browsing is disabled (expected for upload shares) and indicates auth succeeded
          if (state.shareInfo.shareType === "upload") {
            // Initialize password validation state
            if (state.shareInfo.hasPassword) {
              mutations.setShareData({ passwordValid: false });
              try {
                await resourcesApi.fetchFilesPublic(state.shareInfo.subPath, state.shareInfo.hash, this.sharePassword, false, false);
                // If we get here, password is valid (unlikely for upload shares, but handle it)
                mutations.setShareData({ passwordValid: true });
                this.error = null; // Clear any previous errors
              } catch (e) {
                // 501 means browsing is disabled for upload shares - this is expected and means auth succeeded
                if (e.status === 501) {
                  mutations.setShareData({ passwordValid: true });
                  this.error = null; // Clear any previous errors
                } else {
                  throw e;
                }
              }
            } else {
              // No password required, mark as validated
              mutations.setShareData({ passwordValid: true });
              this.error = null; // Clear any previous errors
            }
            return;
          }

          // For regular shares, validate password on startup (similar to upload shares)
          if (state.shareInfo.hasPassword) {
            mutations.setShareData({ passwordValid: false });
            await resourcesApi.fetchFilesPublic(state.shareInfo.subPath, state.shareInfo.hash, this.sharePassword, false, false);
            mutations.setShareData({ passwordValid: true });
            this.error = null; // Clear any previous errors
          } else {
            // No password required, mark as validated
            mutations.setShareData({ passwordValid: true });
            this.error = null; // Clear any previous errors
          }

          if (requestId !== this._requestSeq) return;
          mutations.resetSelected();
          mutations.setMultiple(false);

          if (state.shareInfo?.singleFileShare) {
            mutations.setSidebarVisible(true);
          }

          this.loadingProgress = 10;
          claimedAnchor = this.claimListAnchor(
            requestId,
            previousLifecycleId,
            state.req?.source || state.shareInfo?.source || "",
            state.req?.path || state.shareInfo?.subPath || "/",
            state.shareInfo?.hash || ""
          );
          const file = await fetchShareItemWithParent(this.sharePassword);
          if (requestId !== this._requestSeq) return;
          const shareSource = file.source || state.shareInfo?.source || "";
          const sharePath = file.path ?? state.shareInfo?.subPath ?? "/";
          claimedAnchorSource = shareSource;
          claimedAnchorPath = sharePath;
          if (claimedAnchor) prepareListingRestore({ ticket: claimedAnchor.ticket });
          mutations.replaceRequest(file);
          updateListingLifecycleIdentity({
            source: shareSource,
            path: sharePath,
            shareHash: state.shareInfo?.hash || "",
            view: file.type === "directory" ? "listingView" : "preview",
          });
          document.title = `${globalVars.name} - ${this.$t("general.share")} - ${file.name}`;
          void this.patchMediaMetadataIfNeeded(file);
          if (this.$route.query.download === "true") {
            this.promptShareDownload(file);
          }
        }

        // === FILES-SPECIFIC INITIALIZATION ===
        else {
          if (!getters.isLoggedIn()) {
            return;
          }

          // Clear share data when accessing files
          mutations.clearShareData();
          const routePath = removeTrailingSlash(getters.routePath());

          // Redirect if multiple sources and user went to /files/
          if (routePath === "/files") {
            let targetPath = `/${state.sources.current}`;
            for (const link of state.user?.sidebarLinks || []) {
              if (link.target.startsWith('/')) {
                if (!link.category.startsWith('source')) {
                  continue;
                }
                targetPath = `/${link.sourceName}${link.target}`;
                break;
              }
            }
            if (removeTrailingSlash(targetPath) === "/files" && state.sources.current) {
              targetPath = `/files/${encodeURIComponent(state.sources.current)}/`;
            }
            void router.push(targetPath);
            return;
          }

          const result = extractSourceFromPath(getters.routePath(), Object.keys(state.sources.info));

          if (result.source === "" && state.sources.count === 1) {
            const fallback = state.sources.defaultSource || state.sources.current;
            if (fallback) {
              result.source = fallback;
              result.path = "/";
            }
          }

          if (result.source === "") {
            // No sources available - show a more graceful message instead of error popup
            this.error = { message: $t("index.noSources") };
            mutations.replaceRequest({});
            return;
          }

          this.lastHash = "";
          mutations.resetSelected();

          this.loadingProgress = 10;
          const fetchSource = decodeURIComponent(result.source);
          const fetchPath = decodeURIComponent(result.path);
          claimedAnchor = this.claimListAnchor(requestId, previousLifecycleId, fetchSource, fetchPath, "");

          let res = null;
          let diagnostics = null;
          const pagedEnabled = isPagedListingEnabled();
          if (pagedEnabled) {
            res = await this.loadPagedListingIfEnabled(fetchSource, fetchPath);
          }
          if (!res) {
            const diag = pagedEnabled
              ? null
              : this.startDiagnosticsLoad(fetchSource, fetchPath, this.diagTrigger, false);
            if (diag) {
              diagContext = diag.context;
              diagnostics = {
                requestRef: diag.requestRef,
                onResponse: (response) => {
                  diag.recorder.record("listing.response", {
                    api: "resources",
                    status: Number.isFinite(response.status) ? Math.min(599, Math.max(0, response.status)) : 0,
                    headers_wait_ms: Math.max(0, response.headersWaitMs || 0),
                    body_decode_ms: Number.isFinite(response.bodyDecodeMs) ? Math.max(0, response.bodyDecodeMs) : null,
                    known_items: Number.isFinite(response.knownItems) ? Math.max(0, response.knownItems) : null,
                    paged: false,
                    outcome: response.outcome,
                  }, { ...diag.context, requestRef: diag.requestRef });
                },
              };
            }
            res = await fetchAuthItemWithParent(fetchSource, fetchPath, diagnostics);
          }
          if (requestId !== this._requestSeq) return;
          this.finishDiagnosticsLoad(res, diagContext);
          if (state.sources.count > 1) {
            mutations.setCurrentSource(res.source);
          }
          document.title = `${globalVars.name} - ${this.$t("general.files")} - ${res.name}`;
          const listingSource = res.source || fetchSource;
          const listingPath = res.path ?? fetchPath;
          claimedAnchorSource = listingSource;
          claimedAnchorPath = listingPath;
          if (claimedAnchor) prepareListingRestore({ ticket: claimedAnchor.ticket });
          const selectedBefore = state.selected.length;
          const preparedAt = performance.now();
          mutations.replaceRequest(res);
          updateListingLifecycleIdentity({
            source: listingSource,
            path: listingPath,
            shareHash: "",
            view: res.type === "directory" ? "listingView" : "preview",
          });
          if (diagContext && this.diagLoad === diagContext) {
            this.diagnosticsRecorder()?.record("listing.store_prepared", {
              elapsed_ms: Math.max(0, performance.now() - preparedAt),
              item_count: Array.isArray(res.items) ? res.items.length : 0,
              selected_before: selectedBefore,
              selected_after: state.selected.length,
            }, diagContext);
          }
          mutations.setLoading("files", false);
          void this.patchMediaMetadataIfNeeded(res, fetchSource);
        }

      } catch (e) {
        if (requestId !== this._requestSeq) return;
        this.settleAbandonedAnchor(claimedAnchor, diagContext, "fetch_failed");
        claimedAnchor = null;
        this.cancelDiagnosticsLoad("error", diagContext);
        this.error = e;
        mutations.replaceRequest({});
        this.loadingProgress = 0;

        if (e.status === 404) {
          void router.push({ name: "notFound" });
        } else if (e.status === 403) {
          void router.push({ name: "forbidden" });
        } else if (e.status === 401 && isShare) {
          // Handle share password requirement
          this.clearStoredSharePassword(state.shareInfo?.hash);
          this.attemptedPasswordLogin = this.sharePassword !== "";
          // Reset password validation state on wrong password
          mutations.setShareData({ passwordValid: false });
          // Clear error for upload shares so upload interface can be shown once password is correct
          if (state.shareInfo?.shareType === "upload") {
            this.error = null;
          }
          this.showPasswordPrompt();
        } else {
          void router.push({ name: "error" });
        }
      } finally {
        if (requestId === this._requestSeq) {
          mutations.setLoading(isShare ? "share" : "files", false);
          // Clear navigation transition when data fetch completes
          if (state.navigation.isTransitioning) {
            mutations.setNavigationTransitioning(false);
          }
        }
      }

      if (requestId !== this._requestSeq) {
        this.settleAbandonedAnchor(claimedAnchor, diagContext, "superseded");
        return;
      }
      if (claimedAnchor) {
        this.$nextTick(() => {
          clearListingRestorePreparation();
          if (requestId !== this._requestSeq) {
            this.settleAbandonedAnchor(claimedAnchor, diagContext, "superseded");
            return;
          }
          this.applyClaimedAnchor({
            anchor: claimedAnchor,
            requestId,
            diagContext,
            startedAt: performance.now(),
            expectedSource: claimedAnchorSource,
            expectedPath: claimedAnchorPath,
          });
        });
      } else {
        setTimeout(() => {
          if (requestId !== this._requestSeq) return;
          this.scrollToHashFor(diagContext);
        }, 25);
      }
      this.lastPath = state.route.path;
    },

    /** Claim a move/delete ticket for this exact request; other requests get null. */
    claimListAnchor(requestId, previousLifecycleId, source, path, shareHash) {
      if (requestId !== this._requestSeq) return null;
      return takeListAnchor({
        source: source || "",
        path: path || "/",
        shareHash: shareHash || "",
        lifecycleId: previousLifecycleId,
      });
    },

    /** Restrict DOM lookup to the active main listing (not prompts/file trees). */
    listingElementForIndex(index) {
      const root = this.$el?.querySelector?.(".listing-view-root");
      if (!root || !Number.isInteger(index) || index < 0) return null;
      return root.querySelector(`.listing-item[data-index="${index}"]`);
    },

    currentScrollTop() {
      const scroller = this.$el?.closest?.(".scroll-wrapper") || document.scrollingElement;
      return Number.isFinite(scroller?.scrollTop) ? scroller.scrollTop : null;
    },

    applyClaimedAnchor({ anchor, requestId, diagContext, startedAt, expectedSource, expectedPath }) {
      // Only one restore may own the scroll container at a time.
      if (this._activeRestore) this.abortActiveRestore("superseded");
      const scroller = this.$el?.closest?.(".scroll-wrapper") || document.scrollingElement;
      const isCurrent = () =>
        requestId === this._requestSeq &&
        (state.req?.source || "") === (expectedSource || "") &&
        (state.req?.path ?? "/") === (expectedPath ?? "/");
      if (!isCurrent()) {
        this.settleAbandonedAnchor(anchor, diagContext, "directory_changed", startedAt);
        return;
      }
      const source = state.req?.source || expectedSource || "";
      const restore = createAnchorRestore({
        anchor,
        items: state.req?.items || [],
        source,
        scroller,
        getElement: (index) => this.listingElementForIndex(index),
        isActive: isCurrent,
        onSettled: (result) => {
          if (this._activeRestore === restore) this._activeRestore = null;
          this.detachRestoreGesture();
          this.recordAnchorRestore(anchor, result, { startedAt, diagContext });
        },
      });
      this._activeRestore = restore;
      this.attachRestoreGesture(scroller, restore);
      try {
        restore.begin();
      } catch (_error) {
        // A broken listing must not keep the ticket alive or block navigation.
        if (this._activeRestore === restore) this._activeRestore = null;
        this.detachRestoreGesture();
        this.settleAbandonedAnchor(anchor, diagContext, "restore_error", startedAt);
      }
    },

    /** Real wheel/touch/pointer/key input cancels a late automatic correction. */
    attachRestoreGesture(scroller, restore) {
      this.detachRestoreGesture();
      if (!scroller?.addEventListener) return;
      const cancel = () => {
        if (this._activeRestore === restore) restore.handleUserIntent();
      };
      const onWheel = () => cancel();
      const onTouch = () => cancel();
      const onPointer = () => cancel();
      const onKey = (event) => {
        if (["ArrowUp", "ArrowDown", "PageUp", "PageDown", "Home", "End", " "].includes(event.key)) cancel();
      };
      scroller.addEventListener("wheel", onWheel, { passive: true, capture: true });
      scroller.addEventListener("touchstart", onTouch, { passive: true, capture: true });
      scroller.addEventListener("pointerdown", onPointer, { capture: true });
      window.addEventListener("keydown", onKey, { capture: true });
      this._restoreCleanup = () => {
        scroller.removeEventListener("wheel", onWheel, { capture: true });
        scroller.removeEventListener("touchstart", onTouch, { capture: true });
        scroller.removeEventListener("pointerdown", onPointer, { capture: true });
        window.removeEventListener("keydown", onKey, { capture: true });
      };
    },

    detachRestoreGesture() {
      if (this._restoreCleanup) {
        try {
          this._restoreCleanup();
        } catch (_error) {
          // Element may already be detached; nothing else to clean.
        }
        this._restoreCleanup = null;
      }
    },

    recordAnchorRestore(anchor, result, { startedAt, diagContext }) {
      // ListingView also emits "unmount"/"cancelled" for its own plan teardown;
      // normalize to the closed schema without dropping the event.
      const rawReason = result.reason || null;
      const reasonCode = rawReason === "unmount"
        ? "unmounted"
        : rawReason === "cancelled"
          ? "superseded"
          : rawReason;
      this.recordNavigation({
        reason: anchor.reason,
        // The neighbour actually used may differ from the captured primary.
        targetName: result.targetKey || null,
        requestedOffset: result.requestedOffset,
        actualOffset: result.actualOffset,
        result: result.result,
        startedAt,
        preMounted: result.preMounted,
        strategy: result.strategy,
        reasonCode,
        operationRef: anchor.operationRef || null,
        context: diagContext,
      });
    },

    /** A claimed ticket whose request/directory is gone gets one terminal record. */
    settleAbandonedAnchor(anchor, diagContext, reason, startedAt = performance.now()) {
      if (!anchor) return;
      this.recordNavigation({
        reason: anchor.reason,
        targetName: anchor.anchorKey || null,
        requestedOffset: null,
        actualOffset: this.currentScrollTop(),
        result: "cancelled",
        startedAt,
        preMounted: false,
        reasonCode: reason,
        operationRef: anchor.operationRef || null,
        context: diagContext,
      });
    },

    abortActiveRestore(reason = "unmounted") {
      const restore = this._activeRestore;
      this._activeRestore = null;
      this.detachRestoreGesture();
      if (restore && !restore.isDone()) restore.cancel(reason);
    },

    storeSharePassword(hash, password) {
      if (!hash || !password) {
        return;
      }
      localStorage.setItem(`sharepass:${hash}`, password);
    },
    clearStoredSharePassword(hash) {
      if (!hash) {
        return;
      }
      localStorage.removeItem(`sharepass:${hash}`);
    },
    showPasswordPrompt() {
      mutations.showPrompt({
        name: "password",
        pinned: true,
        props: {
          submitCallback: (password) => {
            this.sharePassword = password;
            this.storeSharePassword(state.shareInfo?.hash, password);
            void this.fetchData();
          },
          showWrongCredentials: this.attemptedPasswordLogin,
          initialPassword: this.sharePassword,
        },
      });
    },
    resolveShareDownloadPath(file) {
      const subPath = state.shareInfo?.subPath;
      if (subPath && subPath !== "/") {
        return subPath;
      }
      if (file?.path && file.path !== "/") {
        return file.path;
      }
      return "/";
    },
    promptShareDownload(file) {
      const downloadPath = this.resolveShareDownloadPath(file);
      openShareDownloadPrompt([{
        path: downloadPath,
        name: file?.name,
        type: file?.type,
        size: file?.size,
        isDir: file?.type === "directory",
      }]);
    },
    keyEvent(event) {
      // F1!
      if (event.key === "F1" && !event.ctrlKey && !event.metaKey) {
        event.preventDefault();
        mutations.setSearch(false);
        if (!getters.currentPromptName()) {
          mutations.showPrompt("help"); // Use mutation
        }
      }

      // Ctrl+, - navigate to settings
      if (event.ctrlKey && event.key === ',') {
        event.preventDefault();
        void router.push('/settings');
      }

      // Esc! - for shares, reset selection
      if (getters.isShare() && event.key === "Escape") {
        if (getters.selectedCount() > 0) {
          mutations.resetSelected();
        }
      }
      // F2! - for rename in listing or preview
      if (event.key === "F2" && !event.ctrlKey && !event.metaKey) {
        event.preventDefault();
        if (getters.currentPromptName()) return;

        const canModify = getters.sourcePermissions().modify;

        if (getters.isPreviewView() && canModify) {
          const parentItems = state.navigation.listing || [];
          mutations.showPrompt({
            name: "rename",
            props: {
              item: state.req,
              parentItems: parentItems
            },
          });
        } else if (getters.currentView() === 'listingView' && canModify && state.selected.length === 1) {
          const item = getters.getFirstSelected();
          if (item) {
            mutations.showPrompt({ name: "rename", props: { item, parentItems: [] } });
          }
        }
      }
      // F4! - refresh the listing with its metadata
      if (event.key === "F4" && !event.ctrlKey && !event.metaKey && !event.repeat) {
        if (getters.currentPromptName() || getters.currentView() !== 'listingView') return;
        event.preventDefault();
        invalidateDirMetadataCache();
        mutations.setReload(true);
      }
      // CTRL+E - switch between editor and markdown viewer
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'e') {
        event.preventDefault();
        const currentFile = state.req;
        if (isRichTextPreviewMimeType(currentFile?.type)) {
          const currentView = getters.currentView();
          if (currentView === 'editor') {
            void router.replace({ hash: '#preview' });
          } else if (currentView === 'markdownViewer') {
            if (getters.sourcePermissions()?.modify) {
              void router.replace({ hash: '#edit' });
            }
          }
        }
      }
      // Ctrl+F1, Ctrl+F2, Ctrl+F3 to change view modes
      if ((event.ctrlKey || event.metaKey) && ['F1', 'F2', 'F3'].includes(event.key)) {
        if (getters.currentView() !== 'listingView') return;
        event.preventDefault();

        const key = event.key;
        let baseMode;
        switch (key) {
          case 'F1':
            baseMode = 'normal';
            break;
          case 'F2':
            baseMode = 'icons';
            break;
          case 'F3':
            baseMode = 'list';
            break;
          default: return;
        }
        const size = state.user?.gallerySize ?? 5;
        let newViewMode = baseMode;
        if (baseMode === 'list') {
          newViewMode = size <= 3 ? 'compact' : 'list';
        } else if (baseMode === 'icons') {
          newViewMode = size <= 4 ? 'icons' : 'gallery';
        }

        mutations.setCurrentListingViewMode(newViewMode);
        mutations.closeHovers();
      }
    },
  },
};
</script>

<style>
.share-info-component {
  margin-top: 0.5em;
}

.progress-line {
  position: fixed;
  top: 4em;
  left: 0;
  right: 0;
  height: 1px;
  background: var(--primaryColor);
  z-index: 2000;
  transition: width 0.3s ease, left 0.2s ease;
  box-shadow: 0 0 10px var(--primaryColor);
}

</style>