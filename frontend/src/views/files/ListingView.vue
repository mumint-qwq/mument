<!-- Modified from FileBrowser Quantum for the mument derivative. See MODIFICATIONS.md. -->
<template>
  <div v-if="shareInfo.shareType !== 'upload'" class="listing-view-root no-select" :class="{ 'mosaic-root': listingViewMode === 'mosaic' }" :style="containerStyles">
    <!-- Show loading spinner while loading OR if we haven't loaded any data yet -->
    <div v-if="loading">
      <h2 class="message delayed">
        <LoadingSpinner size="medium" />
        <span>{{ $t("general.loading", { suffix: "..." }) }}</span>
      </h2>
    </div>

    <!-- listing container -->
    <div
      v-else
      ref="listingView"
      :class="{
        'add-padding': isStickySidebar,
        [listingViewMode]: true,
        dropping: isDragging,
        'rectangle-selecting': isRectangleSelecting,
        'font-size-large': numDirs + numFiles + numPinned === 0 
      }"
      :style="itemStyles"
      class="listing-items"
    >
      <!-- Rectangle selection overlay -->
      <div class="selection-rectangle" :style="rectangleStyle"></div>

      <!-- Drop indicator -->
      <div v-if="isDragging" class="drop-indicator">
        <div class="drop-indicator-content">
          <i class="material-symbols">cloud_upload</i>
          <p>{{ $t("prompts.dragAndDrop") }}</p>
        </div>
      </div>

      <!-- Empty state -->
      <template v-if="numDirs + numFiles + numPinned === 0">
        <h2 class="message font-size-large">
          <i class="material-symbols-outlined">sentiment_dissatisfied</i>
          <span>{{ $t("files.lonely") }}</span>
        </h2>
        <input
          style="display: none"
          type="file"
          id="upload-input"
          @change="uploadInput($event)"
          multiple
        />
        <input
          style="display: none"
          type="file"
          id="upload-folder-input"
          @change="uploadInput($event)"
          webkitdirectory
          multiple
        />
      </template>

      <template v-else>
        <!-- Pinned Items Section -->
        <div v-if="numPinned > 0">
          <h2 :class="{'dark-mode': isDarkMode}">{{ pinnedHeaderText }}</h2>
        </div>
        <div
          v-if="numPinned > 0"
          class="pinned-items"
          :style="mosaicGroupStyles.pinned"
          aria-label="Pinned Items"
          :class="{ lastGroup: numDirs === 0 && numFiles === 0 }"
        >
          <item-batch
            v-for="batch in renderPinnedBatches"
            :key="batch.key"
            :items="batch.items"
            :boxes="mosaicBoxes"
            :source="req.source"
            :hash="shareInfo.hash"
            :isDragging="isDragging"
            :hasDuration="hasDuration"
            @preview-size="onPreviewSize"
          />
        </div>

        <!-- Directories Section -->
        <div v-if="numDirs > 0">
          <h2 :class="{'dark-mode': isDarkMode}">{{ $t("general.folders") }}</h2>
        </div>
        <div
          v-if="numDirs > 0"
          class="folder-items"
          :style="mosaicGroupStyles.dirs"
          aria-label="Folder Items"
          :class="{ lastGroup: numFiles === 0 }"
        >
          <item-batch
            v-for="batch in renderDirsBatches"
            :key="batch.key"
            :items="batch.items"
            :boxes="mosaicBoxes"
            :source="req.source"
            :hash="shareInfo.hash"
            :isDragging="isDragging"
            :hasDuration="hasDuration"
            @preview-size="onPreviewSize"
          />
        </div>

        <!-- Files Section -->
        <div v-if="numFiles > 0">
          <h2 :class="{'dark-mode': isDarkMode}">{{ $t("general.files") }}</h2>
        </div>
        <div
          v-if="numFiles > 0"
          class="file-items"
          :style="mosaicGroupStyles.files"
          :class="{ lastGroup: numFiles > 0 }"
          aria-label="File Items"
        >
          <item-batch
            v-for="batch in renderFilesBatches"
            :key="batch.key"
            :items="batch.items"
            :boxes="mosaicBoxes"
            :source="req.source"
            :hash="shareInfo.hash"
            :isDragging="isDragging"
            :hasDuration="hasDuration"
            @preview-size="onPreviewSize"
          />
        </div>

        <input
          style="display: none"
          type="file"
          id="upload-input"
          @change="uploadInput($event)"
          multiple
        />
        <input
          style="display: none"
          type="file"
          id="upload-folder-input"
          @change="uploadInput($event)"
          webkitdirectory
          multiple
        />
      </template>
    </div>
    <div v-if="mosaicGesturePreview" class="mosaic-zoom-preview" :style="mosaicPreviewStyle" aria-hidden="true">
      <img v-if="mosaicGesturePreview.src" :src="mosaicGesturePreview.src" alt="" />
      <span>{{ mosaicGesturePreview.density >= 1 ? $t("files.mosaicSingleColumn") : $t("files.mosaicResizing") }}</span>
    </div>
  </div>

  <!-- Upload Share Target -->
  <!-- Only show upload interface if password is validated (or no password required) -->
  <div v-else-if="!shareInfo.hasPassword || shareInfo.passwordValid" class="upload-share-embed">
    <Upload :initialItems="null" />
  </div>
</template>

<script>
import { getObjectProperty } from "@/utils/object.js";
import downloadFiles from "@/utils/download";
import { attachMosaicPinch, isMosaicInteractionBlocked } from "@/utils/mosaicPinch.js";
import { consumeScrollbarClick, notePointerSequenceStart, resetScrollbarClickGuard } from "@/utils/scrollbarClickGuard.js";
import { captureMosaicAnchor, restoreMosaicAnchor } from "@/utils/mosaicAnchor.js";
import { getMosaicDensity, setMosaicDensity } from "@/utils/mosaicDensity.js";
import { mosaicGeometry, MOSAIC_PADDING, densityToExtent, mosaicGap, resolveMosaicItemRatio } from "@/utils/mosaicLayout.js";
import { readMosaicRatios, saveMosaicRatios } from "@/utils/mosaicPreferences.js";
import { resourcesApi } from "@/api";
import { router } from "@/router";
import { readAllDirectoryEntries, checkConflict } from "@/utils/upload";
import throttle from "@/utils/throttle";
import { state, mutations, getters } from "@/store";
import { registerScrollRoot, onScrollIdle, onScrollTick, isScrolling, getScrollVelocity } from "@/utils/scrollActivity.js";
import { isInteractionActive, onInteractionChange } from "@/utils/interactionGuard.js";
import { getDiagnosticsRuntime } from "@/utils/diagnostics/runtime.js";
import { getActiveLoadFor, clearActiveLoad } from "@/utils/diagnostics/listingBridge.js";
import { LIMITS as DIAG_LIMITS } from "@/utils/diagnostics/config.js";
import { rowsInBand, buildRowQueue, takeBatch } from "@/utils/listingMountPlan.js";
import {
  registerListingMountController,
  settleListingReveal,
  unregisterListingMountController,
} from "@/utils/listingMountBridge.js";
import { listingItemKey } from "@/utils/listAnchor.js";
import { url } from "@/utils";
import ItemBatch from "@/components/files/ListingItemBatch.vue";
import Upload from "@/components/prompts/Upload.vue";
import LoadingSpinner from "@/components/LoadingSpinner.vue";
import {
  isTypeAheadSessionActive,
  processTypeAheadKey,
  resetTypeAheadSession,
} from "@/utils/listingTypeAhead.js";

// Progressive mosaic mounting: above this item count, only the viewport band
// (plus preload) is created up front and the rest is filled in by idle batches.
const MOUNT_ITEM_THRESHOLD = 80;
const MOUNT_PRELOAD_PX = 600;
const MOUNT_BATCH_ITEMS = 20;
const MOUNT_BATCH_MIN_ITEMS = 16;
const MOUNT_BATCH_MAX_ITEMS = 90;
const MOUNT_BATCH_TARGET_FLUSH_MS = 40;
// Fast scrolling still gets the current viewport mounted; only the 600px
// preload is dropped and each urgent frame has a small item budget.
const URGENT_FAST_VELOCITY = 2.5;
const URGENT_VIEWPORT_BUDGET = 48;
const MOUNT_LAYOUT_DEBOUNCE_MS = 200;
// A restored anchor counts as applied when the card is within this distance of
// the requested viewport offset (or the scroll is clamped at a legal boundary).
const REVEAL_TOLERANCE_PX = 8;

export default {
  name: "listingView",
  components: {
    ItemBatch,
    Upload,
    LoadingSpinner,
  },
  data() {
    return {
      mosaicGesturePreview: null,
      mosaicFrozenLayout: null,
      mosaicRatios: readMosaicRatios(state.user.username),
      mosaicRatioSaveTimer: null,
      mosaicWidth: 0,
      columnWidth: 250 + state.user.gallerySize * 50,
      dragTargets: new Set(),
      width: window.innerWidth,
      lastSelected: {},
      contextTimeout: null,
      clipboard: { items: [] },
      internalClipboardTimestamp: 0,
      isRectangleSelecting: false,
      rectangleStart: { x: 0, y: 0 },
      rectangleEnd: { x: 0, y: 0 },
      rectangleSelection: [],
      cssVariables: {},
      rafId: null,
      selectionUpdatePending: false,
      isResizing: false,
      resizeTimeout: null,
      // Progressive mount state (mosaic only). The plan itself is non-reactive;
      // these arrays are what the template renders.
      visiblePinnedBatches: [],
      visibleDirsBatches: [],
      visibleFilesBatches: [],
      mountActive: false,
      mountComplete: false,
      mountMounted: 0,
    };
  },
  watch: {
    mosaicDensity() {
      this.resetMosaicLoadingAnchor();
      this.preserveMosaicPosition();
      this.handleMosaicLayoutChanged();
    },
    mosaicWidth() {
      this.handleMosaicLayoutChanged();
    },
    listingViewMode(value, oldValue) {
      if (value !== "mosaic") this.mosaicPinch?.cancel();
      this.handleViewModeChanged(value, oldValue);
    },
    items() {
      this.handleListingItemsChanged();
    },
    promptName(value) {
      if (!value) this.wakeMount();
    },
    gallerySize() {
      this.columnWidth = 250 + state.user.gallerySize * 50;
    },
    scrolling() {
      const scrollContainer = this.$refs.listingView;
      if (!scrollContainer) return;

      // Select all visible listing items
      const itemNodes = scrollContainer.querySelectorAll(".listing-item");

      // Find the first item near the top of the viewport
      let topItem = null;
      let minTop = Infinity;
      itemNodes.forEach((el) => {
        const rect = el.getBoundingClientRect();
        if (rect.top >= 0 && rect.top < minTop) {
          minTop = rect.top;
          topItem = el;
        }
      });

      // Decide category by checking which section is above
      let letter = "A";
      let category = "folders"; // Default category

      if (topItem) {
        letter = topItem.getAttribute("data-name")?.[0]?.toUpperCase() || "A";
      } else if (this.numPinned > 0) {
        const pinnedHeader = this.$el.querySelector(".pinned-items h2");
        if (pinnedHeader && pinnedHeader.getBoundingClientRect().top >= 0) {
          category = "pinned";
          const firstPinned = this.pinnedItems[0];
          letter = firstPinned?.name?.[0]?.toUpperCase();
        }
      }

      if (topItem?.closest('.pinned-items')) {
        category = "pinned";
        const firstPinned = this.pinnedItems[0];
        letter = firstPinned?.name?.[0]?.toUpperCase();
      } 
      else if (this.numFiles > 0) {
        const fileSection = this.$el.querySelector(".file-items");
        const fileTop = fileSection?.getBoundingClientRect().top ?? 0;
        category = fileTop <= 0 ? "files" : "folders";
      }
      if (this.numDirs === 0 && category !== "pinned") {
        category = "files"; // If no directories, only files
      }

      mutations.updateListing({
        ...state.listing,
        category,
        letter,
      });
    },
  },
  computed: {
    mosaicPreviewStyle() {
      if (!this.mosaicGesturePreview) return {};
      const preview = this.mosaicGesturePreview;
      const width = Math.max(40, Math.min(this.mosaicWidth || this.width, densityToExtent(this.mosaicWidth || this.width, preview.density)));
      const height = Math.min(window.innerHeight * 0.6, width / preview.ratio);
      return { width: `${width}px`, height: `${height}px`,
        left: `${Math.max(2, Math.min(window.innerWidth - width - 2, preview.x - width / 2))}px`,
        top: `${Math.max(70, Math.min(window.innerHeight - height - 40, preview.y - height / 2))}px` };
    },
    mosaicDensity() { return getMosaicDensity(state.user?.username); },
    mosaicLayoutState() {
      if (this.mosaicFrozenLayout) return this.mosaicFrozenLayout;
      if (getters.viewMode() !== "mosaic") return { boxes: {}, groups: {} };
      const contentWidth = Math.max(1, (this.mosaicWidth || this.width) - MOSAIC_PADDING * 2);
      const groups = {}, boxes = {};
      for (const [name, items] of [['pinned', this.pinnedItems], ['dirs', this.dirs], ['files', this.files]]) {
        const layout = mosaicGeometry(items.map(item => ({
          index: item.index, ratio: resolveMosaicItemRatio(item, this.mosaicRatios[this.mosaicKey(item)]),
        })), contentWidth, { density: this.mosaicDensity });
        Object.assign(groups, { [name]: layout });
        Object.assign(boxes, layout.boxes);
      }
      return { boxes, groups };
    },
    mosaicBoxes() { return this.mosaicLayoutState.boxes; },
    mosaicGroupStyles() {
      return Object.fromEntries(Object.entries(this.mosaicLayoutState.groups)
        .map(([name, layout]) => [name, { height: `${layout.height}px` }]));
    },
    // Progressive mounting only swaps the rendered arrays; all data-derived
    // counts, sorting, selection and actions keep using the full items.
    renderPinnedBatches() {
      return this.mountActive ? this.visiblePinnedBatches : this.fullBatches(this.pinnedItems, "pinned");
    },
    renderDirsBatches() {
      return this.mountActive ? this.visibleDirsBatches : this.fullBatches(this.dirs, "dirs");
    },
    renderFilesBatches() {
      return this.mountActive ? this.visibleFilesBatches : this.fullBatches(this.files, "files");
    },
    promptName() {
      return getters.currentPromptName();
    },
    permissions() {
      return { ...getters.globalPermissions(), ...getters.sourcePermissions() };
    },
    shareInfo() {
      return state.shareInfo;
    },
    state() {
      return state;
    },
    isDragging() {
      if (getters.isShare()) {
        return state.shareInfo.allowCreate && this.dragTargets.has(this.$el);
      }
      return this.dragTargets.has(this.$el);
    },
    scrolling() {
      return state.listing.scrollRatio;
    },
    isStickySidebar() {
      return getters.isStickySidebar();
    },
    allItems() {
      return [...this.pinnedItems, ...this.dirs, ...this.files];
    },
    numColumns() {
      void this.width;
      if (!getters.isCardView()) {
        return 1;
      }
      const elem = document.querySelector("#main");
      if (!elem) {
        return 1;
      }
      if (getters.viewMode() === "icons") {
        const containerSize = 70 + state.user.gallerySize * 15;
        let columns = Math.floor(elem.offsetWidth / containerSize);
        if (columns === 0) {
          columns = 1;
        }
        const minColumns = 3;
        const maxColumns = 12;
        return Math.max(minColumns, Math.min(columns, maxColumns));
      }
      let columns = Math.floor(elem.offsetWidth / this.columnWidth);
      if (columns === 0) {
        columns = 1;
      }
      return columns;
    },
    gallerySize() {
      return state.user.gallerySize;
    },
    isDarkMode() {
      return getters.isDarkMode();
    },
    ascOrdered() {
      return getters.sorting().asc;
    },
    hasDuration() {
      // Check if any pinned file or regular file has duration metadata
      return [...this.pinnedItems, ...this.files].some(
        file => file.type !== "directory" && file.metadata?.duration
      );
    },
    items() {
      return getters.reqItems();
    },
    numPinned() {
      return this.pinnedItems.length;
    },
    pinnedItems() {
      return this.items.pinned || [];
    },
    numFiles() {
      return this.files.length;
    },
    numDirs() {
      return this.dirs.length;
    },
    pinnedHeaderText() {
      const pinnedFolders = this.pinnedItems.filter(item => item.type === 'directory').length;
      const pinnedFiles = this.pinnedItems.filter(item => item.type !== 'directory').length;
      if (pinnedFolders > 0 && pinnedFiles === 0) {
        return this.$t("general.pinnedFolders");
      }
      if (pinnedFiles > 0 && pinnedFolders === 0) {
        return this.$t("general.pinnedFiles");
      }
      return this.$t("general.pinned");
    },
    dirs() {
      return this.items.dirs;
    },
    files() {
      return this.items.files;
    },
    viewIcon() {
      const icons = {
        list: "view_module",
        compact: "view_module",
        normal: "grid_view",
        gallery: "view_list",
      };
      return icons[getters.viewMode()];
    },
    listingViewMode() {
      return getters.viewMode();
    },
    selectedCount() {
      return state.selected.length;
    },
    req() {
      return state.req;
    },
    loading() {
      const isLoading = getters.isLoading();
      return isLoading;
    },
    rectangleStyle() {
      if (!this.isRectangleSelecting) return { display: 'none' };
      const left = Math.min(this.rectangleStart.x, this.rectangleEnd.x);
      const top = Math.min(this.rectangleStart.y, this.rectangleEnd.y);
      const width = Math.abs(this.rectangleStart.x - this.rectangleEnd.x);
      const height = Math.abs(this.rectangleStart.y - this.rectangleEnd.y);
      return {
        left: `${left}px`,
        top: `${top}px`,
        width: `${width}px`,
        height: `${height}px`,
      };
    },
    containerStyles() {
      // Dynamic padding-top: applied to the entire container (loading spinner + listing items)
      const isEmpty = this.numDirs + this.numFiles + this.numPinned === 0;
      const isRootPath = state.req.path === '/' || !state.req.path;

      if (isEmpty) {
        return { 'padding-top': '4.1em' }; // Empty - no files or folders
      } else if (isRootPath) {
        return { 'padding-top': '4.25em' }; // Root - no breadcrumbs showing
      } else {
        return { 'padding-top': '7.28em' }; // Non-root - breadcrumbs + listing header
      }
    },
    itemStyles() {
      const viewMode = getters.viewMode();
      const styles = {};
      if (viewMode === "mosaic") styles["--mosaic-gap"] = `${mosaicGap(this.mosaicDensity)}px`;
      const size = state.user.gallerySize;

      if (viewMode === 'icons') {
        const baseSize = 20 + (size * 15); // 35px to 155px
        const cellSize = baseSize + 30;
        styles['--icon-size'] = `${baseSize}px`;
        styles['--icon-font-size'] = `${baseSize}px`;
        styles['--icons-view-cell-size'] = `${cellSize}px`;
      } else if (viewMode === 'gallery') {
        const baseCalc = 80 + (size * 25);
        const extraScaling = Math.max(0, size - 5) * 15;
        const baseSize = baseCalc + extraScaling; // Size 5: 205px, Size 9: 345px
        const iconFontSize = (3 + (size * 0.5)).toFixed(2); // 3em to 7.5em
        styles['--icon-font-size'] = `${iconFontSize}em`;

        if (getters.isMobile()) {
          const minWidth = size <= 3 ? 120 : size <= 7 ? 160 : 280;
          const mobileHeight = 120 + (size * 20); // 120px to 300px
          styles['--gallery-mobile-min-width'] = `${minWidth}px`;
          styles['--item-width'] = `${minWidth}px`;
          styles['--item-height'] = `${mobileHeight}px`;
        } else {
          styles['--item-width'] = `${baseSize}px`;
          styles['--item-height'] = `${Math.round(baseSize * 1.2)}px`;
        }
      } else if (viewMode === 'list' || viewMode === 'compact') {
        const baseHeight = viewMode === 'compact'
          ? 40 + (size * 2)  // 42px to 58px
          : 50 + (size * 3); // 53px to 77px
        const iconSize = (2 + (size * 0.12)).toFixed(2); // 2.12em to 3.08em
        const iconFontSize = (1.5 + (size * 0.12)).toFixed(2); // 1.62em to 2.58em

        styles['--item-width'] = `calc(${(100 / this.numColumns).toFixed(2)}% - 1em)`;
        styles['--item-height'] = `${baseHeight}px`;
        styles['--icon-size'] = `${iconSize}em`;
        styles['--icon-font-size'] = `${iconFontSize}em`;
      } else {
        // Normal view
        const iconSize = (3.2 + (size * 0.15)).toFixed(2); // 3.35em to 4.55em
        const iconFontSize = (2.2 + (size * 0.12)).toFixed(2); // 2.32em to 3.28em

        styles['--item-width'] = `calc(${(100 / this.numColumns)}% - 1em)`;
        styles['--item-height'] = 'auto';
        styles['--icon-size'] = `${iconSize}em`;
        styles['--icon-font-size'] = `${iconFontSize}em`;
      }

      return styles;
    },
  },
  mounted() {
    // Only the listing scroller counts for thumbnail deferral; panel scrolling
    // elsewhere must not freeze image loading.
    this.unregisterScrollRoot = registerScrollRoot(this.$el?.closest?.(".scroll-wrapper") || null);
    this.mosaicResizeObserver = new ResizeObserver(() => {
      const listing = this.$refs.listingView;
      const width = listing?.clientWidth || this.$el.clientWidth;
      if (width !== this.mosaicWidth) {
        const oldWidth = this.mosaicWidth;
        this.preserveMosaicPosition();
        this.mosaicWidth = width;
        this.recordLayoutChange("resize", this.mosaicDensity, this.mosaicDensity, oldWidth, width);
      }
    });
    this.mosaicResizeObserver.observe(this.$el);
    this.mosaicPinch = attachMosaicPinch(this.$el, {
      isEnabled: () => getters.viewMode() === "mosaic" && !getters.currentPromptName(),
      getDensity: () => this.mosaicDensity,
      getWidth: () => Math.max(1, (this.mosaicWidth || this.width) - MOSAIC_PADDING * 2),
      onStart: center => {
        this.cancelContext();
        this.resetMosaicLoadingAnchor();
        this.mosaicFrozenLayout = this.mosaicLayoutState;
        this._mosaicGestureAnchor = captureMosaicAnchor(this.$el, center);
        const img = this._mosaicGestureAnchor?.element.querySelector('img');
        this.mosaicGesturePreview = { density: this.mosaicDensity, x: center.x, y: center.y,
          src: img?.src || '', ratio: img?.naturalWidth && img?.naturalHeight ? img.naturalWidth / img.naturalHeight : 1 };
      },
      onPreview: ({density, center}) => {
        if (this.mosaicGesturePreview) this.mosaicGesturePreview = { ...this.mosaicGesturePreview, density, x: center.x, y: center.y };
      },
      onCommit: density => {
        const oldDensity = this.mosaicDensity;
        this.mosaicFrozenLayout = null;
        this.mosaicGesturePreview = null;
        this.preserveMosaicPosition(this._mosaicGestureAnchor);
        this._mosaicGestureAnchor = null;
        setMosaicDensity(state.user?.username, density);
        this.recordLayoutChange("gesture_commit", oldDensity, density, null, null);
        this.wakeMount();
      },
      onCancel: () => {
        this.mosaicFrozenLayout = null;
        this.mosaicGesturePreview = null;
        this.preserveMosaicPosition(this._mosaicGestureAnchor);
        this._mosaicGestureAnchor = null;
        this.wakeMount();
      },
    });
    window.addEventListener('touchstart', this.mosaicUserTouchStart, { capture: true, passive: true });
    window.addEventListener('touchend', this.mosaicUserTouchEnd, { capture: true, passive: true });
    window.addEventListener('touchcancel', this.mosaicUserTouchEnd, { capture: true, passive: true });
    this.$el.addEventListener('wheel', this.mosaicUserWheel, { passive: true });
    mutations.setSearch(false);
    this.lastSelected = state.selected;

    // Add the needed event listeners to the window and document.
    window.addEventListener("keydown", this.keyEvent);
    window.addEventListener("resize", this.windowsResize);
    window.addEventListener("click", this.clickClear);
    // Capture where each pointer sequence starts before the thumb's own
    // mousedown/touchstart handler runs, so the click a scrollbar drag produces
    // is not mistaken for a click on blank listing space.
    window.addEventListener("mousedown", this.trackPointerSequenceStart, true);
    window.addEventListener("touchstart", this.trackPointerSequenceStart, { capture: true, passive: true });
    window.addEventListener("touchcancel", this.resetScrollbarClickSequence, true);
    window.addEventListener("pointercancel", this.resetScrollbarClickSequence, true);
    window.addEventListener("dragover", this.preventDefault);
    window.addEventListener('paste', this.handlePaste);
    document.addEventListener('mousemove', this.updateRectangleSelection, { passive: true });
    document.addEventListener('mouseup', this.endRectangleSelection);
    this.$el.addEventListener('mousedown', this.startRectangleSelection);
    this.$el.addEventListener("touchmove", this.handleTouchMove, { passive: true });
    this.$el.addEventListener('dblclick', this.handleDoubleClick);

    // Single dragend listener for all items (prevents N listeners for N items)
    document.addEventListener('dragend', this.handleGlobalDragEnd, { passive: true });

    this.$el.addEventListener("contextmenu", this.openContext);
    // Adjust contextmenu listener based on browser
    if (state.isSafari) {
      // For Safari, add touchstart or mousedown to open the context menu
      this.$el.addEventListener("touchstart", this.openContextForSafari, {
        passive: true,
      });
      this.$el.addEventListener("mousedown", this.openContextForSafari);

      // Also clear the timeout if the user clicks or taps quickly
      this.$el.addEventListener("touchend", this.cancelContext);
      this.$el.addEventListener("mouseup", this.cancelContext);
    }

    // if safari , make sure click and hold opens context menu, but not for any other browser
    if (this.permissions?.modify || getters.isShare()) {
      this.$el.addEventListener("dragenter", this.dragEnter);
      this.$el.addEventListener("dragleave", this.dragLeave);
      this.$el.addEventListener("drop", this.drop);
    }

    registerListingMountController(this);
    this._unsubscribeScrollIdle = onScrollIdle(() => this.handleScrollIdle());
    this._unsubscribeScrollTick = onScrollTick(() => this.handleScrollTick());
    this._unsubscribeInteraction = onInteractionChange(active => {
      if (!active) this.wakeMount();
    });
    this._mountVisibilityHandler = () => {
      if (!document.hidden) this.handleScrollIdle();
    };
    document.addEventListener("visibilitychange", this._mountVisibilityHandler);
    this.$nextTick(() => {
      if (!this.mosaicMountEligible()) this.startFlatDiagnostics();
    });
  },
  beforeUnmount() {
    if (this.unregisterScrollRoot) {
      this.unregisterScrollRoot();
      this.unregisterScrollRoot = null;
    }
    this._mosaicDisposed = true;
    unregisterListingMountController(this);
    if (this._unsubscribeScrollIdle) {
      this._unsubscribeScrollIdle();
      this._unsubscribeScrollIdle = null;
    }
    if (this._unsubscribeScrollTick) {
      this._unsubscribeScrollTick();
      this._unsubscribeScrollTick = null;
    }
    if (this._unsubscribeInteraction) {
      this._unsubscribeInteraction();
      this._unsubscribeInteraction = null;
    }
    if (this._urgentMountFrame !== null && this._urgentMountFrame !== undefined) {
      cancelAnimationFrame(this._urgentMountFrame);
      this._urgentMountFrame = null;
    }
    if (this._mountVisibilityHandler) {
      document.removeEventListener("visibilitychange", this._mountVisibilityHandler);
      this._mountVisibilityHandler = null;
    }
    this.resetMountPlan();
    this.mosaicPinch?.destroy();
    this.resetMosaicLoadingAnchor();
    window.removeEventListener('touchstart', this.mosaicUserTouchStart, true);
    window.removeEventListener('touchend', this.mosaicUserTouchEnd, true);
    window.removeEventListener('touchcancel', this.mosaicUserTouchEnd, true);
    this.$el.removeEventListener('wheel', this.mosaicUserWheel);
    clearTimeout(this.mosaicRatioSaveTimer);
    saveMosaicRatios(state.user.username, this.mosaicRatios);
    this.mosaicResizeObserver?.disconnect();
    resetTypeAheadSession();

    if (this.resizeTimeout) {
      clearTimeout(this.resizeTimeout);
      this.resizeTimeout = null;
    }

    // Remove event listeners before destroying this page.
    window.removeEventListener("keydown", this.keyEvent);
    window.removeEventListener("resize", this.windowsResize);
    window.removeEventListener("click", this.clickClear);
    window.removeEventListener("mousedown", this.trackPointerSequenceStart, true);
    window.removeEventListener("touchstart", this.trackPointerSequenceStart, true);
    window.removeEventListener("touchcancel", this.resetScrollbarClickSequence, true);
    window.removeEventListener("pointercancel", this.resetScrollbarClickSequence, true);
    resetScrollbarClickGuard();
    window.removeEventListener("dragover", this.preventDefault);
    window.removeEventListener('paste', this.handlePaste);
    document.removeEventListener('mousemove', this.updateRectangleSelection);
    document.removeEventListener('mouseup', this.endRectangleSelection);
    document.removeEventListener('dragend', this.handleGlobalDragEnd);
    this.$el.removeEventListener('mousedown', this.startRectangleSelection);
    this.$el.removeEventListener('dblclick', this.handleDoubleClick);

    this.$el.removeEventListener("touchmove", this.handleTouchMove);
    this.$el.removeEventListener("contextmenu", this.openContext);

    // If Safari, remove touch/mouse listeners
    if (state.isSafari) {
      this.$el.removeEventListener("touchstart", this.openContextForSafari);
      this.$el.removeEventListener("mousedown", this.openContextForSafari);
      this.$el.removeEventListener("touchend", this.cancelContext);
      this.$el.removeEventListener("mouseup", this.cancelContext);
    }

    // Also clean up drag/drop listeners on the component's root element
    if (state.user && this.permissions?.modify || getters.isShare()) {
      this.$el.removeEventListener("dragenter", this.dragEnter);
      this.$el.removeEventListener("dragleave", this.dragLeave);
      this.$el.removeEventListener("drop", this.drop);
    }
  },
  methods: {
    resetMosaicLoadingAnchor() {
      clearTimeout(this._mosaicLoadingAnchorTimer);
      this._mosaicLoadingAnchor = null;
    },
    mosaicUserTouchStart(event) {
      if (!this.$el.contains(event.target)) return;
      this._mosaicUserTouching = true;
      this.resetMosaicLoadingAnchor();
    },
    mosaicUserTouchEnd(event) {
      this._mosaicUserTouching = event.touches.length > 0;
      this._mosaicUserScrollUntil = performance.now() + 300;
      if (!this._mosaicUserTouching) this.wakeMount();
    },
    mosaicUserWheel() {
      this._mosaicUserScrollUntil = performance.now() + 300;
      this.resetMosaicLoadingAnchor();
    },

    // ---------------------------------------------------------------------
    // Progressive mosaic mounting (PLAN-PROGRESSIVE-CARDS).
    // Full data stays in state.req.items; only the rendered subset changes.
    // ---------------------------------------------------------------------
    diagnosticsRecorder() {
      return getDiagnosticsRuntime()?.recorder || null;
    },
    currentDiagnosticsBridge() {
      const bridge = getActiveLoadFor(state.req);
      const recorder = this.diagnosticsRecorder();
      if (!bridge || !recorder?.isStarted() || !recorder.isCurrentContext(bridge.context)) return null;
      return bridge;
    },
    resetDiagnosticsState(bridge, startedAt) {
      clearTimeout(this._diag?.stallTimer);
      clearTimeout(this._diag?.gapTimer);
      clearTimeout(this._diag?.flatTimer);
      this._diag = {
        load: bridge?.context || null,
        startedAt: bridge?.context?.startedAt ?? startedAt,
        layoutRecorded: false,
        firstBatch: false,
        done: false,
        mountStats: { batches: 0, urgentBatches: 0, sum: 0, max: 0 },
        lastSummaryAt: 0,
        lastMounted: 0,
        stallTimer: null,
        gapTimer: null,
        flat: false,
        flatPending: false,
        flatTimer: null,
      };
      this._diagScroll = {
        min: null,
        max: null,
        fastMs: 0,
        slowMs: 0,
        urgentMounted: 0,
        samples: 0,
        lastAt: performance.now(),
        lastTop: null,
        lastEmit: 0,
      };
    },
    diagActive(context = null) {
      const recorder = this.diagnosticsRecorder();
      const load = context || this._diag?.load || null;
      return Boolean(recorder && load && this._diag?.load === load && !this._diag.done
        && recorder.isStarted() && recorder.getMode() !== "off" && recorder.isCurrentContext(load));
    },
    // Flat views (small mosaic, list, gallery, empty directories) do not use
    // the progressive mount plan, but still need an honest first paint and
    // completion point. This waits for the real DOM commit and reports only
    // what was actually present.
    startFlatDiagnostics() {
      const bridge = this.currentDiagnosticsBridge();
      if (!bridge) return;
      const same = this._diag && this._diag.load === bridge.context;
      if (same && (this._diag.completed || this._diag.flatPending)) return;
      if (same && this._diag.done) return;
      if (this._diag && !this._diag.done && !this._diag.completed) {
        this.finishDiagnostics("cancel", "superseded");
      }
      this.resetDiagnosticsState(bridge, performance.now());
      this._diag.flat = true;
      this._diag.flatPending = true;
      this.scheduleFlatCommit(bridge.context, this.mosaicMountTotal(), 0);
    },
    scheduleFlatCommit(context, total, attempt) {
      if (!this._diag || this._diag.load !== context || !this._diag.flatPending) return;
      const recorder = this.diagnosticsRecorder();
      if (!recorder?.isCurrentContext(context)) {
        this._diag.flatPending = false;
        this._diag.done = true;
        clearTimeout(this._diag.flatTimer);
        this._diag.flatTimer = null;
        clearActiveLoad(context);
        return;
      }
      if (!this.diagActive(context)) {
        const status = recorder.getStatus?.();
        // Disabled/unsupported: no session will ever start for this load.
        if (status?.server_enabled === false && !status.local_only) {
          this._diag.flatPending = false;
          return;
        }
        if (attempt < 50) {
          this._diag.flatTimer = setTimeout(() => this.scheduleFlatCommit(context, total, attempt + 1), 100);
        }
        return;
      }
      this.$nextTick(() => requestAnimationFrame(() => requestAnimationFrame(() => {
        if (!this.diagActive(context) || this._diag.load !== context || !this._diag.flatPending) return;
        const committed = this.$el?.querySelectorAll?.(".listing-item")?.length || 0;
        const recorder = this.diagnosticsRecorder();
        recorder.record("listing.first_batch_paint_estimate", {
          elapsed_ms: Math.max(0, performance.now() - context.startedAt),
          queued_items: Math.min(1000000, total),
          committed_items: Math.min(1000000, committed),
          known_items: Math.min(1000000, total),
          method: "double_raf",
        }, context);
        recorder.finishLoad(context, "complete", { knownItems: total, mountedItems: committed });
        this._diag.flatPending = false;
        this._diag.completed = true;
        this._diag.done = true;
        clearActiveLoad(context);
      })));
    },
    recordListingLayout(startedAt) {
      if (!this.diagActive() || this._diag.layoutRecorded || !this._plan) return;
      this._diag.layoutRecorded = true;
      const groups = this._plan.groups || {};
      const rows = Object.values(groups).reduce((sum, group) => sum + (group?.length || 0), 0);
      const groupCount = Object.values(groups).filter((group) => group?.length).length;
      this.diagnosticsRecorder().record("listing.layout", {
        geom_items: Math.min(1000000, this._plan.total || 0),
        groups: Math.min(3, groupCount),
        rows: Math.min(1000000, rows),
        density: Number.isFinite(this.mosaicDensity) ? Math.min(1, Math.max(0, this.mosaicDensity)) : null,
        width_px: Math.min(20000, Math.max(0, Math.round(this.mosaicWidth || this.width || 0))),
        elapsed_ms: Math.max(0, performance.now() - startedAt),
        generation: Math.min(1000000, this._plan.generation || 0),
        method: "estimated",
      }, this._diag.load);
    },
    recordFirstBatch() {
      if (!this.diagActive() || this._diag.firstBatch) return;
      this._diag.firstBatch = true;
      const elapsed = Math.max(0, performance.now() - this._diag.startedAt);
      const queued = this._plan?.mountedItems?.size || this.mountMounted || 0;
      const committed = ["pinned", "dirs", "files"].reduce((sum, group) => {
        const batches = group === "pinned" ? this.visiblePinnedBatches : group === "dirs" ? this.visibleDirsBatches : this.visibleFilesBatches;
        return sum + batches.reduce((count, batch) => count + batch.items.length, 0);
      }, 0);
      this.diagnosticsRecorder().record("listing.first_batch_paint_estimate", {
        elapsed_ms: elapsed,
        queued_items: Math.min(1000000, queued),
        committed_items: Math.min(1000000, committed),
        known_items: Math.min(1000000, this._mountTotal || 0),
        method: "double_raf",
      }, this._diag.load);
      if (elapsed > DIAG_LIMITS.SLOW_FIRST_BATCH_MS) {
        this.diagAutoIncident("listing_slow", {
          duration_ms: elapsed,
          mounted: committed,
          known_items: this._mountTotal || 0,
          evidence: "plan_only",
        });
      }
      if (committed === 0 && (this._mountTotal || 0) > 0 && typeof document !== "undefined" && !document.hidden) {
        this._diag.gapTimer = setTimeout(() => {
          this._diag.gapTimer = null;
          if (!this.diagActive() || this.mountMounted > 0 || this.mountShouldPause()) return;
          this.diagAutoIncident("viewport_mount_gap", {
            duration_ms: DIAG_LIMITS.VIEWPORT_GAP_MS,
            mounted: 0,
            known_items: this._mountTotal || 0,
            evidence: "plan_only",
          });
        }, DIAG_LIMITS.VIEWPORT_GAP_MS);
      }
    },
    recordMountBatch({ size, enqueueMs, flushMs, urgent, fast }) {
      if (!this.diagActive()) return;
      const stats = this._diag.mountStats;
      stats.batches += 1;
      if (urgent) stats.urgentBatches += 1;
      if (Number.isFinite(flushMs)) {
        stats.sum += flushMs;
        stats.max = Math.max(stats.max, flushMs);
      }
      if (urgent && this._diagScroll) this._diagScroll.urgentMounted += size;
      this._diag.lastMounted = this.mountMounted;
      this.diagnosticsRecorder().record("listing.mount_batch", {
        generation: Math.min(1000000, this._plan?.generation || 0),
        urgent: Boolean(urgent),
        fast: Boolean(fast),
        size: Math.min(10000, size),
        enqueue_ms: Math.max(0, enqueueMs || 0),
        flush_ms: Number.isFinite(flushMs) ? Math.max(0, flushMs) : null,
      }, this._diag.load);
      this.recordMountSummary(false);
      this.scheduleMountStallCheck();
    },
    recordMountSummary(force) {
      if (!this.diagActive()) return;
      const stats = this._diag.mountStats;
      if (!stats.batches) return;
      const at = performance.now();
      if (!force && at - this._diag.lastSummaryAt < 1000) return;
      this._diag.lastSummaryAt = at;
      this.diagnosticsRecorder().record("listing.mount_summary", {
        mounted: Math.min(1000000, this.mountMounted || 0),
        total: Math.min(1000000, this._mountTotal || 0),
        batches: Math.min(1000000, stats.batches),
        urgent_batches: Math.min(1000000, stats.urgentBatches),
        elapsed_sum_ms: Math.max(0, stats.sum),
        elapsed_max_ms: Math.max(0, stats.max),
        paused_reason: this.diagPausedReason(),
        // The mount plan has no truthful drop counter; do not invent zero.
        dropped: null,
      }, this._diag.load);
    },
    diagPausedReason() {
      if (typeof document !== "undefined" && document.hidden) return "hidden";
      if (this.mosaicGesturePreview || this._mosaicUserTouching) return "gesture";
      if (getters.currentPromptName()) return "prompt";
      if (isInteractionActive()) return "interaction";
      if (isScrolling()) return "scroll";
      return "none";
    },
    recordScrollSample(force) {
      if (!this.diagActive() || !this._diagScroll) return;
      const scroller = this.getScrollContainer();
      if (!scroller) return;
      const top = Math.max(0, scroller.scrollTop || 0);
      const at = performance.now();
      const dt = Math.max(0, at - this._diagScroll.lastAt);
      if (this._diagScroll.lastTop !== null && dt > 0) {
        if (getScrollVelocity() > 1) this._diagScroll.fastMs += dt;
        else this._diagScroll.slowMs += dt;
      }
      this._diagScroll.min = this._diagScroll.min === null ? top : Math.min(this._diagScroll.min, top);
      this._diagScroll.max = this._diagScroll.max === null ? top : Math.max(this._diagScroll.max, top);
      this._diagScroll.samples += 1;
      this._diagScroll.lastAt = at;
      this._diagScroll.lastTop = top;
      if (force || at - this._diagScroll.lastEmit >= 1000) this.emitScrollSummary();
    },
    emitScrollSummary() {
      if (!this.diagActive() || !this._diagScroll?.samples) return;
      const scroll = this._diagScroll;
      const min = scroll.min || 0;
      const max = scroll.max || 0;
      this.diagnosticsRecorder().record("scroll.summary", {
        range_top_px: Math.min(100000000, min),
        range_height_px: Math.min(100000000, max - min),
        fast_ms: Math.min(100000000, scroll.fastMs),
        slow_ms: Math.min(100000000, scroll.slowMs),
        urgent_mounted: Math.min(1000000, scroll.urgentMounted),
        // No scrollbar-drag observer exists; null means unknown, not false.
        scrollbar_drag: null,
        samples: Math.min(1000000, scroll.samples),
      }, this._diag.load);
      scroll.lastEmit = performance.now();
    },
    recordLayoutChange(reason, oldDensity, newDensity, oldWidth, newWidth) {
      if (!this.diagActive()) return;
      this.diagnosticsRecorder().record("layout.change", {
        reason,
        old_density: Number.isFinite(oldDensity) ? Math.min(1, Math.max(0, oldDensity)) : null,
        new_density: Number.isFinite(newDensity) ? Math.min(1, Math.max(0, newDensity)) : null,
        old_width: Number.isFinite(oldWidth) ? Math.min(20000, Math.max(0, Math.round(oldWidth))) : null,
        new_width: Number.isFinite(newWidth) ? Math.min(20000, Math.max(0, Math.round(newWidth))) : null,
        generation: Math.min(1000000, this._plan?.generation || 0),
        anchor_ref: null,
        offset_before: null,
        offset_after: null,
        // This event is emitted before an asynchronous restore is confirmed.
        result: "unknown",
      }, this._diag.load);
    },
    diagAutoIncident(kind, data) {
      if (!this.diagActive()) return null;
      return this.diagnosticsRecorder().autoIncident(kind, {
        kind,
        duration_ms: Math.max(0, data.duration_ms || 0),
        mounted: Math.min(1000000, data.mounted || 0),
        known_items: Math.min(1000000, data.known_items || 0),
        generation: Math.min(1000000, this._plan?.generation || 0),
        evidence: data.evidence || "plan_only",
      }, this._diag.load);
    },
    scheduleMountStallCheck() {
      if (!this.diagActive()) return;
      clearTimeout(this._diag.stallTimer);
      this._diag.stallTimer = setTimeout(() => {
        this._diag.stallTimer = null;
        if (!this.diagActive() || this.mountComplete || this.mountShouldPause()) {
          this.scheduleMountStallCheck();
          return;
        }
        if (this.mountMounted === this._diag.lastMounted) {
          this.diagAutoIncident("mount_stalled", {
            duration_ms: DIAG_LIMITS.MOUNT_STALL_MS,
            mounted: this.mountMounted,
            known_items: this._mountTotal || 0,
            evidence: "plan_only",
          });
        } else {
          this._diag.lastMounted = this.mountMounted;
          this.scheduleMountStallCheck();
        }
      }, DIAG_LIMITS.MOUNT_STALL_MS);
    },
    finishDiagnostics(kind, reason = "unmount") {
      if (!this.diagActive()) {
        if (kind === "cancel" && this._diag?.flatPending) {
          clearTimeout(this._diag.flatTimer);
          this._diag.flatTimer = null;
          this._diag.flatPending = false;
          this._diag.done = true;
          clearActiveLoad(this._diag.load);
        }
        return;
      }
      clearTimeout(this._diag.stallTimer);
      clearTimeout(this._diag.gapTimer);
      clearTimeout(this._diag.flatTimer);
      this._diag.stallTimer = null;
      this._diag.gapTimer = null;
      this._diag.flatTimer = null;
      if (kind === "cancel" && this._diag.completed) {
        this._diag.done = true;
        clearActiveLoad(this._diag.load);
        return;
      }
      const recorder = this.diagnosticsRecorder();
      if (kind === "complete") {
        recorder.flushImageSummary();
        recorder.finishLoad(this._diag.load, "complete", {
          knownItems: this._mountTotal || 0,
          mountedItems: this.mountMounted || 0,
        });
        this._diag.completed = true;
        clearActiveLoad(this._diag.load);
        return;
      } else {
        const allowed = ["route", "reload", "sort", "view", "unmount", "superseded", "error"];
        recorder.finishLoad(this._diag.load, "cancel", {
          reason: allowed.includes(reason) ? reason : "superseded",
          mountedItems: this.mountMounted || 0,
        });
      }
      this._diag.done = true;
      clearActiveLoad(this._diag.load);
    },
    getScrollContainer() {
      return this.$el?.closest?.(".scroll-wrapper") || document.scrollingElement;
    },
    mosaicMountTotal() {
      return this.numPinned + this.numDirs + this.numFiles;
    },
    mosaicMountEligible() {
      return getters.viewMode() === "mosaic" && this.mosaicMountTotal() > MOUNT_ITEM_THRESHOLD;
    },
    currentLayoutKey() {
      return `${this.mosaicWidth}|${this.mosaicDensity}|${this._ratioVersion || 0}`;
    },
    indexMosaicRows() {
      const layout = this.mosaicLayoutState.groups;
      const groups = { pinned: [], dirs: [], files: [] };
      const rowByIndex = new Map();
      for (const group of Object.keys(groups)) {
        // eslint-disable-next-line security/detect-object-injection -- group is one of our fixed keys
        const rows = layout[group]?.rows || [];
        // eslint-disable-next-line security/detect-object-injection -- group is one of our fixed keys
        groups[group] = rows;
        rows.forEach((row, rowIndex) => {
          for (const index of row.indices) rowByIndex.set(index, { group, rowIndex });
        });
      }
      return { groups, rowByIndex };
    },
    initialTargetIndex() {
      const req = state.req;
      const items = req?.items || [];
      if (!items.length) return null;
      let name = null;
      const hash = typeof window !== "undefined" ? window.location.hash : "";
      if (hash && hash.length > 1) {
        try {
          name = decodeURIComponent(hash.slice(1));
        } catch (_e) {
          name = hash.slice(1);
        }
      }
      const previous = state.previousHistoryItem;
      if (!name && previous?.name && previous.path === req?.path && previous.source === req?.source) {
        name = previous.name;
      }
      if (!name) return null;
      const index = items.findIndex(item => item.name === name);
      return index >= 0 ? index : null;
    },
    startMountPlan({ targetIndex = null, preserveScroll = false } = {}) {
      this.cancelMountSchedule();
      clearTimeout(this._mountLayoutTimer);
      const diagStartedAt = performance.now();
      const bridge = this.currentDiagnosticsBridge();
      if (this._diag?.flat && !this._diag.done) {
        this.finishDiagnostics("cancel", "superseded");
      }
      if (this._diag && !this._diag.done && !this._diag.completed && (!bridge || bridge.context !== this._diag.load)) {
        this.finishDiagnostics("cancel", "superseded");
      }
      if (!this._diag || this._diag.load !== (bridge?.context || null)) {
        this.resetDiagnosticsState(bridge, diagStartedAt);
      }
      const previousTops = this._groupTops;
      const req = state.req;
      const total = this.mosaicMountTotal();
      if (!req?.items || total === 0) {
        this.resetMountPlan();
        return;
      }
      const { groups, rowByIndex } = this.indexMosaicRows();
      this._plan = {
        generation: (this._mountGeneration || 0) + 1,
        layoutKey: this.currentLayoutKey(),
        groups,
        rowByIndex,
        items: req.items,
        total,
        mountedRows: { pinned: new Set(), dirs: new Set(), files: new Set() },
        mountedItems: new Set(),
        queue: [],
        batchItems: MOUNT_BATCH_ITEMS,
        flushAvg: 0,
        scheduled: false,
        idleId: null,
        timerId: null,
      };
      this._mountGeneration = this._plan.generation;
      this._mountReq = req;
      this._mountTotal = total;
      this._mountItemsRef = req.items;
      // A reveal queued for the previous plan would address a different file
      // now that the data was re-sorted; only this plan's target may survive.
      this.cancelPendingReveal("superseded");
      this._groupTops = preserveScroll ? previousTops : null;
      this.mountActive = true;
      this.mountComplete = false;
      this.mountMounted = 0;
      this.visiblePinnedBatches = [];
      this.visibleDirsBatches = [];
      this.visibleFilesBatches = [];
      this._mountBatchSeq = 0;
      const scroller = this.getScrollContainer();
      const viewport = scroller?.clientHeight || window.innerHeight || 900;
      for (const group of Object.keys(groups)) {
        // eslint-disable-next-line security/detect-object-injection -- group is one of our fixed keys
        const topBand = rowsInBand(groups[group], 0, viewport + MOUNT_PRELOAD_PX);
        this.mountRowEntries(topBand.map(entry => ({ group, rowIndex: entry.rowIndex })));
      }
      if (targetIndex !== null) this.mountRowsAroundItem(targetIndex, viewport);
      if (preserveScroll && scroller) {
        // Same geometry, new item order (e.g. re-sort): mount the band under the
        // current viewport right away instead of showing an empty frame.
        this.mountBandAt(scroller.scrollTop || 0, viewport);
      }
      const generation = this._plan.generation;
      this.$nextTick(() => {
        if (!this._plan || this._plan.generation !== generation) return;
        this.measureGroupTops();
        if (targetIndex !== null) {
          this._pendingReveal = { index: targetIndex, block: "center", offsetTop: 0 };
          this.applyPendingReveal(viewport);
        }
        this.prioritizeMountQueue();
        this.recordListingLayout(diagStartedAt);
        this.announceFirstBatch();
        this.scheduleMountBatch();
        this.scheduleMountStallCheck();
      });
    },
    resetMountPlan(reason = "unmount") {
      this.cancelPendingReveal(reason);
      this.cancelMountSchedule();
      this.finishDiagnostics("cancel", reason);
      clearTimeout(this._mountLayoutTimer);
      this.visiblePinnedBatches = [];
      this.visibleDirsBatches = [];
      this.visibleFilesBatches = [];
      this._plan = null;
      this._groupTops = null;
      this.mountActive = false;
      this.mountComplete = false;
      this.mountMounted = 0;
    },
    adoptFullMountPlan() {
      // Entering mosaic from another view already paid the cost of mounting
      // everything; unmounting now would only cost another render.
      if (this._diag?.flat && !this._diag.done) {
        this.startFlatDiagnostics();
      }
      this.cancelMountSchedule();
      this._plan = null;
      this._groupTops = null;
      this.mountActive = true;
      this.mountComplete = true;
      this.visiblePinnedBatches = this.fullBatches(this.pinnedItems, "pinned");
      this.visibleDirsBatches = this.fullBatches(this.dirs, "dirs");
      this.visibleFilesBatches = this.fullBatches(this.files, "files");
      this.mountMounted = this.mosaicMountTotal();
      this._mountReq = state.req;
      this._mountTotal = this.mountMounted;
      this._mountItemsRef = state.req?.items || null;
    },
    handleListingItemsChanged() {
      const req = state.req;
      const total = this.mosaicMountTotal();
      if (!req?.items || total === 0) {
        this.resetMountPlan();
        this._mountReq = req || null;
        this._mountTotal = 0;
        this.startFlatDiagnostics();
        return;
      }
      if (!this.mosaicMountEligible()) {
        this.resetMountPlan();
        this._mountReq = req;
        this._mountTotal = total;
        this.startFlatDiagnostics();
        return;
      }
      const sameRequest = req === this._mountReq;
      const itemsReplaced = req.items !== this._mountItemsRef;
      if (sameRequest && this.mountActive && !itemsReplaced && total === this._mountTotal) return;
      if (sameRequest && this.mountActive && this._plan && !itemsReplaced && total > this._mountTotal) {
        // A page append mutates the same items array; keep mounted cards.
        this._mountTotal = total;
        this.relayoutMountPlan();
        return;
      }
      // Same request but a new items array means the store re-sorted/re-indexed
      // in place. Old mounted indexes no longer identify the same files.
      const preserveScroll = sameRequest && this.mountActive && itemsReplaced;
      const suppressed = this.consumeInitialTargetSuppression();
      this.startMountPlan({
        targetIndex: preserveScroll || suppressed ? null : this.initialTargetIndex(),
        preserveScroll,
      });
    },
    handleViewModeChanged(value, oldValue) {
      if (oldValue && oldValue !== value) {
        this.recordLayoutChange("view_change", this.mosaicDensity, this.mosaicDensity, null, null);
      }
      if (value !== "mosaic") {
        this.resetMountPlan();
        return;
      }
      if (oldValue && oldValue !== "mosaic" && this.mosaicMountTotal() > 0) {
        this.adoptFullMountPlan();
        return;
      }
      this.handleListingItemsChanged();
    },
    handleMosaicLayoutChanged() {
      clearTimeout(this._mountLayoutTimer);
      this._mountLayoutTimer = setTimeout(() => {
        this._mountLayoutTimer = null;
        if (!this.mountActive || !this._plan) return;
        this.relayoutMountPlan();
      }, MOUNT_LAYOUT_DEBOUNCE_MS);
    },
    relayoutMountPlan() {
      const plan = this._plan;
      if (!plan) return;
      const generation = plan.generation;
      const { groups, rowByIndex } = this.indexMosaicRows();
      plan.groups = groups;
      plan.rowByIndex = rowByIndex;
      plan.layoutKey = this.currentLayoutKey();
      plan.total = this.mosaicMountTotal();
      const mounted = plan.mountedItems;
      for (const group of ["pinned", "dirs", "files"]) {
        // eslint-disable-next-line security/detect-object-injection -- group is one of our fixed keys
        const rows = groups[group] || [];
        const mountedRows = new Set();
        rows.forEach((row, rowIndex) => {
          if (row.indices.every(index => mounted.has(index))) mountedRows.add(rowIndex);
        });
        // eslint-disable-next-line security/detect-object-injection -- group is one of our fixed keys
        plan.mountedRows[group] = mountedRows;
      }
      this._groupTops = null;
      this.$nextTick(() => {
        if (!this._plan || this._plan.generation !== generation) return;
        this.measureGroupTops();
        const scroller = this.getScrollContainer();
        const viewport = scroller?.clientHeight || window.innerHeight;
        this.mountBandAt(scroller?.scrollTop || 0, viewport);
        if (this._pendingReveal) this.applyPendingReveal(viewport);
        this.prioritizeMountQueue();
        this.scheduleMountBatch();
      });
    },
    mountRowEntries(entries, limit = Infinity) {
      const plan = this._plan;
      if (!plan || !entries?.length) return 0;
      const added = { pinned: [], dirs: [], files: [] };
      let count = 0;
      for (const entry of entries) {
        const rows = plan.groups[entry.group];
        if (!rows) continue;
        if (plan.mountedRows[entry.group].has(entry.rowIndex)) continue;
        const row = rows[entry.rowIndex];
        if (!row) continue;
        // Rows are atomic: stop before a row that would exceed the budget.
        if (count > 0 && count + row.indices.length > limit) break;
        for (const index of row.indices) {
          if (plan.mountedItems.has(index)) continue;
          // eslint-disable-next-line security/detect-object-injection -- index is an item.index from the plan
          const item = plan.items[index];
          if (!item) continue;
          plan.mountedItems.add(index);
          added[entry.group].push(item);
          count += 1;
        }
        plan.mountedRows[entry.group].add(entry.rowIndex);
      }
      if (added.pinned.length) this.addVisibleBatch("pinned", added.pinned);
      if (added.dirs.length) this.addVisibleBatch("dirs", added.dirs);
      if (added.files.length) this.addVisibleBatch("files", added.files);
      if (count) this.mountMounted = plan.mountedItems.size;
      return count;
    },
    addVisibleBatch(group, items) {
      if (!items.length) return;
      this._mountBatchSeq = (this._mountBatchSeq || 0) + 1;
      const batch = { key: `batch-${this._mountBatchSeq}`, items };
      if (group === "pinned") this.visiblePinnedBatches.push(batch);
      else if (group === "dirs") this.visibleDirsBatches.push(batch);
      else this.visibleFilesBatches.push(batch);
    },
    fullBatches(items, group) {
      return items.length ? [{ key: `full-${group}`, items }] : [];
    },
    mountRowsAroundItem(index, viewport) {
      const plan = this._plan;
      const location = plan?.rowByIndex.get(index);
      if (!plan || !location) return 0;
      const rows = plan.groups[location.group];
      const row = rows?.[location.rowIndex];
      if (!row) return 0;
      const extra = (viewport || window.innerHeight || 900) / 2;
      const band = rowsInBand(
        rows,
        row.top - MOUNT_PRELOAD_PX - extra,
        row.top + row.height + MOUNT_PRELOAD_PX + (viewport || window.innerHeight || 900),
      );
      return this.mountRowEntries(band.map(entry => ({ group: location.group, rowIndex: entry.rowIndex })));
    },
    mountBandAt(scrollTop, viewport, { preload = MOUNT_PRELOAD_PX, maxItems = Infinity } = {}) {
      const plan = this._plan;
      if (!plan) return 0;
      const vp = viewport || window.innerHeight || 900;
      let count = 0;
      for (const group of Object.keys(plan.groups)) {
        if (count >= maxItems) break;
        // eslint-disable-next-line security/detect-object-injection -- group is one of our fixed keys
        const groupTop = Number(this._groupTops?.[group]) || 0;
        const band = rowsInBand(
          // eslint-disable-next-line security/detect-object-injection -- group is one of our fixed keys
          plan.groups[group],
          scrollTop - preload - groupTop,
          scrollTop + vp + preload - groupTop,
        );
        count += this.mountRowEntries(
          band.map(entry => ({ group, rowIndex: entry.rowIndex })),
          maxItems - count,
        );
      }
      return count;
    },
    measureGroupTops() {
      const scroller = this.getScrollContainer();
      if (!scroller) return;
      const scrollerRect = scroller.getBoundingClientRect();
      const tops = {};
      for (const [group, selector] of [["pinned", ".pinned-items"], ["dirs", ".folder-items"], ["files", ".file-items"]]) {
        const element = this.$el.querySelector(selector);
        // eslint-disable-next-line security/detect-object-injection -- group is one of our fixed keys
        tops[group] = element
          ? element.getBoundingClientRect().top - scrollerRect.top + scroller.scrollTop
          : 0;
      }
      this._groupTops = tops;
    },
    scrollToIndexPosition(index, { block = "center", offsetTop = 0, viewport } = {}) {
      const plan = this._plan;
      const location = plan?.rowByIndex.get(index);
      if (!plan || !location) return false;
      const row = plan.groups[location.group]?.[location.rowIndex];
      const scroller = this.getScrollContainer();
      if (!row || !scroller) return false;
      const vp = viewport || scroller.clientHeight || window.innerHeight;
      const itemTop = (Number(this._groupTops?.[location.group]) || 0) + row.top;
      const itemBottom = itemTop + row.height;
      const viewTop = scroller.scrollTop;
      if (block === "nearest" && itemTop >= viewTop && itemBottom <= viewTop + vp) {
        this._lastScrollApply = { requested: viewTop, actual: viewTop, raw: viewTop, clamped: false };
        return true;
      }
      let target;
      if (block === "top") target = itemTop - offsetTop;
      else if (block === "nearest") target = itemTop < viewTop ? itemTop - 12 : itemBottom - vp + 12;
      else target = itemTop + row.height / 2 - vp / 2;
      const max = Math.max(0, scroller.scrollHeight - vp);
      const requested = Math.min(Math.max(0, target), max);
      scroller.scrollTop = requested;
      this._lastScrollApply = {
        requested,
        actual: scroller.scrollTop,
        raw: target,
        clamped: requested !== target,
      };
      return true;
    },
    revealIndex(index, { block = "center", offsetTop = 0, requestId = null, ticket = null } = {}) {
      if (!this.mountActive || !this._plan) return false;
      if (!this._plan.rowByIndex.has(index)) return false;
      const scroller = this.getScrollContainer();
      const viewport = scroller?.clientHeight || window.innerHeight;
      const planGeneration = this._plan.generation;
      this.mountRowsAroundItem(index, viewport);
      // A newer reveal replaces the old one: settle it now instead of leaving a
      // timer that could later fire the old fallback.
      if (this._pendingReveal?.requestId && this._pendingReveal.requestId !== requestId) {
        settleListingReveal(this._pendingReveal.requestId, { result: "cancelled", reason: "superseded" });
      }
      this._pendingReveal = { index, block, offsetTop, requestId, ticket, planGeneration };
      if (this._plan.layoutKey !== this.currentLayoutKey()) {
        // Width/ratio changed after this plan was laid out: scroll only after
        // the rows are rebuilt, otherwise the old row top would be wrong.
        this.relayoutMountPlan();
        return true;
      }
      if (!this._groupTops) this.measureGroupTops();
      const moved = this.applyPendingReveal(viewport);
      if (moved) {
        this.prioritizeMountQueue();
        this.scheduleMountBatch();
      } else {
        // No row geometry for this plan: reject so the caller can use its DOM
        // fallback instead of waiting for a reveal that will never apply.
        this.cancelReveal({ ticket, reason: "target_not_in_plan" });
        return false;
      }
      return true;
    },
    applyPendingReveal(viewport) {
      const pending = this._pendingReveal;
      if (!pending || !this._plan) return false;
      if (this._plan.layoutKey !== this.currentLayoutKey()) return false;
      this.mountRowsAroundItem(pending.index, viewport);
      if (!this._groupTops) this.measureGroupTops();
      const moved = this.scrollToIndexPosition(pending.index, {
        block: pending.block,
        offsetTop: pending.offsetTop,
        viewport,
      });
      if (!moved) return false;
      this._pendingReveal = null;
      const applied = this._lastScrollApply || {};
      pending.requestedOffset = Number.isFinite(applied.requested) ? applied.requested : null;
      pending.actualOffset = Number.isFinite(applied.actual) ? applied.actual : null;
      pending.clamped = Boolean(applied.clamped);
      this.verifyReveal(pending);
      return true;
    },
    /** Final success requires the card to exist at the expected offset or a boundary. */
    verifyReveal(pending) {
      if (!pending?.requestId) return;
      if (this._pendingVerify?.requestId) {
        settleListingReveal(this._pendingVerify.requestId, { result: "cancelled", reason: "superseded" });
      }
      this._pendingVerify = pending;
      const generation = pending.planGeneration;
      this._pendingVerifyFrame = requestAnimationFrame(() => {
        this._pendingVerifyFrame = null;
        if (this._pendingVerify !== pending) return;
        this._pendingVerify = null;
        if (!this._plan || this._plan.generation !== generation) {
          settleListingReveal(pending.requestId, {
            result: "cancelled", reason: "superseded",
            requestedOffset: pending.requestedOffset, actualOffset: pending.actualOffset,
          });
          return;
        }
        const scroller = this.getScrollContainer();
        const element = this.$el?.querySelector?.(`.listing-item[data-index="${pending.index}"]`);
        if (!scroller || !element) {
          settleListingReveal(pending.requestId, {
            result: "failed", reason: "target_not_mounted",
            requestedOffset: pending.requestedOffset, actualOffset: scroller?.scrollTop ?? null,
          });
          return;
        }
        const scrollerRect = scroller.getBoundingClientRect();
        const rect = element.getBoundingClientRect();
        let applied;
        if (pending.block === "top") {
          const viewportOffset = rect.top - scrollerRect.top;
          const offsetMatches = Math.abs(viewportOffset - pending.offsetTop) <= REVEAL_TOLERANCE_PX;
          const boundaryMatches = pending.clamped &&
            pending.requestedOffset !== null &&
            Math.abs(scroller.scrollTop - pending.requestedOffset) <= 0.5;
          applied = offsetMatches || boundaryMatches;
        } else {
          applied = rect.bottom > scrollerRect.top - REVEAL_TOLERANCE_PX &&
            rect.top < scrollerRect.bottom + REVEAL_TOLERANCE_PX;
        }
        settleListingReveal(pending.requestId, {
          result: applied ? "success" : "failed",
          reason: applied ? undefined : "offset_mismatch",
          requestedOffset: pending.requestedOffset,
          actualOffset: scroller.scrollTop,
        });
      });
    },
    cancelPendingReveal(reason = "cancelled") {
      const pending = this._pendingReveal;
      this._pendingReveal = null;
      if (pending?.requestId) {
        settleListingReveal(pending.requestId, { result: "cancelled", reason });
      }
      const verify = this._pendingVerify;
      this._pendingVerify = null;
      if (this._pendingVerifyFrame !== null && this._pendingVerifyFrame !== undefined) {
        cancelAnimationFrame(this._pendingVerifyFrame);
        this._pendingVerifyFrame = null;
      }
      if (verify?.requestId) {
        settleListingReveal(verify.requestId, { result: "cancelled", reason });
      }
    },
    /** Called by the bridge when one request/ticket is aborted before settling. */
    cancelReveal({ ticket = null, requestId = null, reason = "cancelled" } = {}) {
      const matches = (pending) => Boolean(pending?.requestId) && (
        requestId ? pending.requestId === requestId : (!ticket || pending.ticket === ticket)
      );
      if (matches(this._pendingReveal)) {
        const pending = this._pendingReveal;
        this._pendingReveal = null;
        settleListingReveal(pending.requestId, { result: "cancelled", reason });
      }
      if (matches(this._pendingVerify)) {
        const pending = this._pendingVerify;
        this._pendingVerify = null;
        if (this._pendingVerifyFrame !== null && this._pendingVerifyFrame !== undefined) {
          cancelAnimationFrame(this._pendingVerifyFrame);
          this._pendingVerifyFrame = null;
        }
        settleListingReveal(pending.requestId, { result: "cancelled", reason });
      }
    },
    prepareRestore({ ticket = null } = {}) {
      this._suppressInitialTarget = { ticket, until: performance.now() + 1500 };
      return true;
    },
    consumeInitialTargetSuppression() {
      const entry = this._suppressInitialTarget;
      if (!entry) return false;
      this._suppressInitialTarget = null;
      return performance.now() <= entry.until;
    },
    clearRestore() {
      this._suppressInitialTarget = null;
    },
    revealTarget({ name = null, key = null, index = null, offsetTop = 0, block = "center", requestId = null, ticket = null } = {}) {
      if (!this.mountActive || !this._plan) return false;
      let target = Number.isInteger(index) ? index : -1;
      const items = state.req?.items || [];
      if (target < 0 && key) {
        const source = state.req?.source || "";
        target = items.findIndex((item) => listingItemKey(item, source) === key);
      }
      if (target < 0 && name) {
        for (const item of items) {
          if (item.name === name) { target = item.index; break; }
        }
      }
      if (target < 0) return false;
      return this.revealIndex(target, { block, offsetTop, requestId, ticket });
    },
    prioritizeMountQueue() {
      const plan = this._plan;
      if (!plan) return;
      const scroller = this.getScrollContainer();
      const scrollTop = scroller?.scrollTop || 0;
      const viewport = scroller?.clientHeight || window.innerHeight || 900;
      const centers = {};
      for (const group of Object.keys(plan.groups)) {
        // eslint-disable-next-line security/detect-object-injection -- group is one of our fixed keys
        centers[group] = scrollTop + viewport / 2 - (Number(this._groupTops?.[group]) || 0);
      }
      plan.queue = buildRowQueue(
        plan.groups,
        centers,
        // eslint-disable-next-line security/detect-object-injection -- group is one of our fixed keys
        (group, rowIndex) => plan.mountedRows[group].has(rowIndex),
      );
    },
    wakeMount() {
      const plan = this._plan;
      if (!plan || this.mountComplete) return;
      if (this.mountShouldPause()) {
        plan.paused = true;
        return;
      }
      this.mountVisibleBandUrgent();
      this.scheduleMountBatch();
    },
    handleScrollTick() {
      if (!this.mountActive || !this._plan) return;
      this.recordScrollSample(false);
      if (this._urgentMountFrame !== null && this._urgentMountFrame !== undefined) return;
      this._urgentMountFrame = requestAnimationFrame(() => {
        this._urgentMountFrame = null;
        this.mountVisibleBandUrgent();
      });
    },
    /** The viewport band is urgent: it may mount while a scroll is still active. */
    mountVisibleBandUrgent() {
      if (!this.mountActive || !this._plan) return;
      const scroller = this.getScrollContainer();
      if (!scroller) return;
      const fast = getScrollVelocity() > URGENT_FAST_VELOCITY;
      const startedAt = performance.now();
      const items = this.mountBandAt(
        scroller.scrollTop || 0,
        scroller.clientHeight || window.innerHeight,
        fast ? { preload: 0, maxItems: URGENT_VIEWPORT_BUDGET } : {},
      );
      if (items) {
        this.recordMountBatch({
          size: items,
          enqueueMs: performance.now() - startedAt,
          flushMs: null,
          urgent: true,
          fast,
        });
      }
      if (items && typeof window !== "undefined" && Array.isArray(window.__listingUrgentStats)) {
        window.__listingUrgentStats.push({
          items,
          ms: performance.now() - startedAt,
          fast,
        });
      }
    },
    scheduleMountBatch() {
      const plan = this._plan;
      if (!plan || plan.scheduled) return;
      const generation = plan.generation;
      plan.scheduled = true;
      const run = () => {
        const current = this._plan;
        if (!current || current.generation !== generation) return;
        current.scheduled = false;
        current.idleId = null;
        current.timerId = null;
        if (this.mountShouldPause()) {
          // Woken by scroll idle / gesture end / prompt close / visibility.
          current.paused = true;
          return;
        }
        current.paused = false;
        const batchSize = current.batchItems || MOUNT_BATCH_ITEMS;
        const batch = takeBatch(
          current.queue,
          current.groups,
          // eslint-disable-next-line security/detect-object-injection -- group is one of our fixed keys
          (group, rowIndex) => current.mountedRows[group].has(rowIndex),
          batchSize,
        );
        if (!batch.length) {
          this.finishMountPlan();
          return;
        }
        const startedAt = performance.now();
        this.mountRowEntries(batch);
        const mountedAt = performance.now();
        this.$nextTick(() => {
          const flushMs = performance.now() - mountedAt;
          this.adaptMountBatchSize(current, flushMs);
          this.recordMountBatch({
            size: batch.length,
            enqueueMs: mountedAt - startedAt,
            flushMs,
            urgent: false,
            fast: false,
          });
          if (typeof window !== "undefined" && Array.isArray(window.__listingBatchStats)) {
            window.__listingBatchStats.push({
              mount: mountedAt - startedAt,
              flush: flushMs,
              size: batchSize,
              mounted: this.mountMounted,
            });
          }
          if (!this._plan || this._plan.generation !== generation) return;
          if (typeof requestAnimationFrame === "function") {
            requestAnimationFrame(() => this.scheduleMountBatch());
          } else {
            setTimeout(() => this.scheduleMountBatch(), 16);
          }
        });
      };
      if (typeof requestIdleCallback === "function") {
        plan.idleId = requestIdleCallback(run, { timeout: 250 });
      } else {
        plan.timerId = setTimeout(run, 24);
      }
    },
    adaptMountBatchSize(plan, flushMs) {
      if (!plan || !Number.isFinite(flushMs)) return;
      plan.flushAvg = plan.flushAvg ? plan.flushAvg * 0.7 + flushMs * 0.3 : flushMs;
      let size = plan.batchItems || MOUNT_BATCH_ITEMS;
      if (plan.flushAvg > MOUNT_BATCH_TARGET_FLUSH_MS) {
        size = Math.max(MOUNT_BATCH_MIN_ITEMS, Math.floor(size * (flushMs > 80 ? 0.5 : 0.7)));
      } else if (plan.flushAvg < 12) {
        size = Math.min(MOUNT_BATCH_MAX_ITEMS, Math.ceil(size * 1.2));
      }
      plan.batchItems = size;
    },
    cancelMountSchedule() {
      const plan = this._plan;
      if (!plan) return;
      if (plan.idleId !== null && typeof cancelIdleCallback === "function") cancelIdleCallback(plan.idleId);
      if (plan.timerId !== null) clearTimeout(plan.timerId);
      plan.idleId = null;
      plan.timerId = null;
      plan.scheduled = false;
    },
    mountShouldPause() {
      if (!this._plan) return true;
      if (typeof document !== "undefined" && document.hidden) return true;
      if (this.mosaicGesturePreview || this.isRectangleSelecting) return true;
      if (this._mosaicUserTouching) return true;
      if (isInteractionActive()) return true;
      if (isScrolling()) return true;
      if (getters.currentPromptName()) return true;
      return false;
    },
    handleScrollIdle() {
      if (!this.mountActive || !this._plan) return;
      if (isScrolling()) return;
      this.recordScrollSample(true);
      const scroller = this.getScrollContainer();
      if (!scroller) return;
      const viewport = scroller.clientHeight || window.innerHeight;
      this.mountBandAt(scroller.scrollTop, viewport);
      this.prioritizeMountQueue();
      this.scheduleMountBatch();
    },
    finishMountPlan() {
      this.mountComplete = true;
      if (this._plan) this._plan.queue = [];
      this.recordMountSummary(true);
      this.finishDiagnostics("complete");
      document.dispatchEvent(new CustomEvent("quantum:listing-all-mounted", {
        detail: { total: this.mountMounted },
      }));
    },
    announceFirstBatch() {
      const generation = this._plan?.generation;
      this.$nextTick(() => requestAnimationFrame(() => requestAnimationFrame(() => {
        if (!this._plan || this._plan.generation !== generation) return;
        this.recordFirstBatch();
        document.dispatchEvent(new CustomEvent("quantum:listing-first-batch", {
          detail: { mounted: this.mountMounted, total: this._mountTotal },
        }));
      })));
    },
    preserveMosaicPosition(anchor) {
      if (this._mosaicDisposed || getters.viewMode() !== "mosaic" || this.mosaicGesturePreview) return;
      if (!this._pendingMosaicAnchor) this._pendingMosaicAnchor = anchor || captureMosaicAnchor(this.$el);
      if (this._mosaicAnchorScheduled) return;
      this._mosaicAnchorScheduled = true;
      // Callers capture BEFORE updating ratios/width. Let that update enqueue Vue's
      // render first, then wait for it; an immediate nextTick here can run too early.
      queueMicrotask(() => {
        this.$nextTick(() => {
          if (!this._mosaicDisposed) restoreMosaicAnchor(this.$el, this._pendingMosaicAnchor);
          this._pendingMosaicAnchor = null;
          this._mosaicAnchorScheduled = false;
        });
      });
    },
    mosaicKey(item) { return `${this.req.source}:${item.path || item.name}:${item.modified}`; },
    onPreviewSize(item, dimensions) {
      const ratio = dimensions.width / dimensions.height;
      if (!Number.isFinite(ratio) || ratio <= 0) return;
      const key = this.mosaicKey(item);
      if (getObjectProperty(this.mosaicRatios, key) === ratio) return;
      // Keep one file identity for the whole decode burst, instead of choosing
      // a different tile after every partial layout. Do not fight a user's scroll.
      if (!this.mosaicGesturePreview && !this._mosaicUserTouching && performance.now() >= (this._mosaicUserScrollUntil || 0)) {
        if (!this._mosaicLoadingAnchor) this._mosaicLoadingAnchor = captureMosaicAnchor(this.$el);
        this.preserveMosaicPosition(this._mosaicLoadingAnchor);
        clearTimeout(this._mosaicLoadingAnchorTimer);
        this._mosaicLoadingAnchorTimer = setTimeout(() => { this._mosaicLoadingAnchor = null; }, 600);
      }
      this.mosaicRatios = { ...this.mosaicRatios, [key]: ratio };
      this._ratioVersion = (this._ratioVersion || 0) + 1;
      this.handleMosaicLayoutChanged();
      clearTimeout(this.mosaicRatioSaveTimer);
      this.mosaicRatioSaveTimer = setTimeout(() => saveMosaicRatios(state.user.username, this.mosaicRatios), 500);
    },
    handleGlobalDragEnd() {
      // Reset drag state for all items (replaces per-item dragend listeners)
      const items = this.$el?.querySelectorAll('.listing-item.drag-hover, .listing-item.half-selected');
      if (items) {
        items.forEach(el => {
          el.classList.remove('drag-hover', 'half-selected');
        });
      }
      this.dragTargets.clear();
    },
    cancelContext() {
      if (this.contextTimeout) {
        clearTimeout(this.contextTimeout);
        this.contextTimeout = null;
      }
      this.isLongPress = false;
    },
    openContextForSafari(event) {
      if (isMosaicInteractionBlocked(event.target)) return;
      if (!event.touches?.length) return;
      this.cancelContext(); // Clear any previous timeouts
      this.isLongPress = false; // Reset state
      this.isSwipe = false; // Reset swipe detection

      const touch = event.touches[0];
      this.touchStartX = touch.clientX;
      this.touchStartY = touch.clientY;

      // Start the long press detection
      this.contextTimeout = setTimeout(() => {
        if (!this.isSwipe && !isMosaicInteractionBlocked(event.target)) {
          this.isLongPress = true;
          event.preventDefault(); // Suppress Safari's callout menu
          this.openContext(event); // Open the custom context menu
        }
      }, 500); // Long press delay (adjust as needed)
    },
    handleTouchMove(event) {
      const touch = event.touches[0];
      const deltaX = Math.abs(touch.clientX - this.touchStartX);
      const deltaY = Math.abs(touch.clientY - this.touchStartY);
      // Set a threshold for movement to detect a swipe
      const movementThreshold = 10; // Adjust as needed
      if (deltaX > movementThreshold || deltaY > movementThreshold) {
        this.isSwipe = true;
        this.cancelContext(); // Cancel long press if swipe is detected
      }
    },
    handleTouchEnd() {
      this.cancelContext(); // Clear timeout
      this.isSwipe = false; // Reset swipe state
    },
    base64(name) {
      return url.base64Encode(name);
    },
    showDeletePrompt() {
      const items = [];
      for (const index of state.selected) {
        const item = state.req.items.at(index);
        if (!item) continue;
        const previewUrl = item.hasPreview
          ? resourcesApi.getPreviewURL(item.source || state.req.source, item.path, item.modified)
          : null;
        items.push({
          source: item.source || state.req.source,
          path: item.path,
          type: item.type,
          size: item.size,
          modified: item.modified,
          hasPreview: item.hasPreview,
          previewUrl: previewUrl,
        });
      }
      mutations.showPrompt({
        name: "delete",
        props: { items },
      });
    },
    // Helper method to select the first item if nothing is selected
    selectFirstItem() {
      mutations.resetSelected();
      if (this.allItems.length > 0) {
        mutations.addSelected(this.allItems[0].index);
      }
    },
    // Helper method to find the closest item in the given direction (up or down) from the current one.
    findClosestItem(selectedItem, direction) {
      const listItems = Array.from(this.$el.querySelectorAll('.listing-item'));
      const selectedBounds = selectedItem.getBoundingClientRect();
      const selectedMidX = (selectedBounds.left + selectedBounds.right) / 2;

      let closestItem = null;
      let closestDistance = Infinity;

      for (const item of listItems) {
        if (item === selectedItem) continue;
        const itemBounds = item.getBoundingClientRect();
        const itemMidX = (itemBounds.left + itemBounds.right) / 2;
        const horizontalOffset = Math.abs(itemMidX - selectedMidX);

        const verticalGap = direction === 'down'
          ? itemBounds.top - selectedBounds.bottom
          : selectedBounds.top - itemBounds.bottom;

        if (verticalGap > 0) {
          const distance = Math.hypot(horizontalOffset, verticalGap);
          if (distance < closestDistance) {
            closestDistance = distance;
            closestItem = item;
          }
        }
      }
      return closestItem;
    },
    moveSelectionBy(step) {
      const allItems = this.allItems;
      const selectedIndex = state.selected.length > 0 ? state.selected[0] : null;
      if (selectedIndex === null) {
        this.selectFirstItem();
        return false;
      }
      const currentItemIndex = allItems.findIndex(item => item.index === selectedIndex);
      if (currentItemIndex === -1) {
        this.selectFirstItem();
        return false;
      }
      const nextItemIndex = currentItemIndex + step;
      if (nextItemIndex >= 0 && nextItemIndex < allItems.length) {
        const targetItem = allItems.at(nextItemIndex);
        mutations.resetSelected();
        mutations.addSelected(targetItem.index);
        this.scrollSelectedIntoView();
        return true;
      }
      return false;
    },
    // Helper method to handle selection based on arrow keys
    navigateKeyboardArrows(arrowKey) {
      const isCardView = getters.isCardView(); // gallery, normal, icons
      const selectedIndex = state.selected.length > 0 ? state.selected[0] : null;

      if (selectedIndex === null) {
        // If nothing is selected, select the first item
        this.selectFirstItem();
        return;
      }

      // Left/Right arrows will always use the visual order that we see
      if (arrowKey === 'ArrowLeft' || arrowKey === 'ArrowRight') {
        const step = arrowKey === 'ArrowLeft' ? -1 : 1;
        this.moveSelectionBy(step);
        return;
      }

      // On list/compact views we use a simple linear navigation since all the items are aligned
      if (!isCardView) {
        const step = arrowKey === 'ArrowUp' ? -1 : 1;
        this.moveSelectionBy(step);
        return;
      }

      // But for gallery, normal, icons views, we need to find the closest item
      // because the rows aren't always consistent (some have 1 item, others 5, etc) 
      // which caused "random jumps"
      const selectedItem = this.$el.querySelector(`.listing-item[data-index="${selectedIndex}"]`);
      if (!selectedItem) return;

      let nextItem = null;

      switch (arrowKey) {
        case 'ArrowDown':
          nextItem = this.findClosestItem(selectedItem, 'down');
          break;
        case 'ArrowUp':
          nextItem = this.findClosestItem(selectedItem, 'up');
          break;
      }

      const itemIndex = parseInt(nextItem?.dataset.index, 10);
      if (!Number.isNaN(itemIndex)) {
        mutations.resetSelected();
        mutations.addSelected(itemIndex);
        this.scrollSelectedIntoView();
        return;
      }
      // The next visual row may not be mounted yet; fall back to data order and
      // let the mount plan create the target before scrolling.
      if (getters.viewMode() === "mosaic") {
        const step = arrowKey === "ArrowUp" ? -1 : 1;
        this.moveSelectionBy(step);
      }
    },
    scrollSelectedIntoView() {
      const selected = state.selected.length > 0 ? state.selected[0] : null;
      if (selected !== null && this.revealIndex(selected, { block: "nearest" })) return;
      setTimeout(() => {
        const element = document.querySelector(
          '.listing-item[aria-selected="true"]'
        );
        if (element) {
          element.scrollIntoView({
            behavior: "smooth",
            block: "nearest",
            inline: "nearest",
          });
        }
      }, 50);
    },
    keyEvent(event) {
      const { key, ctrlKey, metaKey, altKey, which } = event;
      const isArrowKey = key === 'ArrowUp' ||
                         key === 'ArrowDown' ||
                         key === 'ArrowLeft' ||
                         key === 'ArrowRight';
      if (state.isSearchActive || getters.currentView() !== "listingView" ||
        getters.currentPromptName() || (event.repeat && (!isArrowKey || altKey))) return;

      if (
        (key === ' ' || key === '/') &&
        isTypeAheadSessionActive() &&
        !state.isSearchActive &&
        !getters.currentPromptName()
      ) {
        event.preventDefault();
        return;
      }

      const isAlphanumeric = /^[a-z0-9]$/i.test(key);
      const modifierKeys = ctrlKey || metaKey;
      if (isAlphanumeric && !modifierKeys && state.selected.length <= 1) {
        const t = event.target;
        const tag = t?.tagName ? t.tagName.toLowerCase() : "";
        if (
          tag !== "input" &&
          tag !== "textarea" &&
          tag !== "select" &&
          !t.isContentEditable
        ) {
          event.preventDefault();
          this.alphanumericKeyPress(key);
          return;
        }
      }

      if (modifierKeys) {
        const charKey = String.fromCharCode(which).toLowerCase();

        switch (charKey) {
          case "c":
          case "x":
            this.copyCut(event, charKey);
            return;
          case "a":
            event.preventDefault();
            mutations.selectAllItems({ multiple: false });
            return;
          case "d":
            event.preventDefault();
            downloadFiles(state.selected);
            return;
        }
        // Don't return here - allow other modifier key combinations to propagate
      }

      // Handle key events using a switch statement
      let shortcut = key;
      if (altKey) shortcut = `Alt+${key}`;

      switch (shortcut) {
        case "Alt+ArrowUp":
          event.preventDefault();
          // fall through
        case "Backspace": {
          event.preventDefault();
          // get current path and its parent
          const currentPath = state.req.path || "/";
          const parentPath = url.removeLastDir(currentPath);
          if (parentPath === currentPath) {
            return;
          }
          const source = getters.isShare() ? state.shareInfo.hash : state.req.source;
          const newPath = url.buildItemUrl(source, parentPath);
          void router.push({ path: newPath });
          break;
        }

        case "Alt+ArrowDown":
          event.preventDefault();
          // fall through
        case "Enter": {
          event.preventDefault();
          if (this.selectedCount === 1) {
            const selected = getters.getFirstSelected();
            const selectedUrl = url.buildItemUrl(selected.source, selected.path);
            if (selectedUrl === state.route.path) return;
            void router.push({ path: selectedUrl });
          }
          break;
        }

        case "Escape":
          if (this.dragTargets.size > 0) {
            this.dragTargets.clear();
            event.preventDefault();
            return;
          }
          mutations.resetSelected();
          break;

        case "Delete":
          if (!this.permissions?.modify || state.selected.length === 0) return;
          this.showDeletePrompt();
          break;

        case "ArrowUp":
        case "ArrowDown":
        case "ArrowLeft":
        case "ArrowRight":
          event.preventDefault();
          this.navigateKeyboardArrows(key);
          break;
      }
    },
    alphanumericKeyPress(key) {
      const selectedIndex = state.selected.length === 1 ? state.selected[0] : null;
      const { matches, nextPos } = processTypeAheadKey(key, this.allItems, selectedIndex);
      if (matches.length === 0) {
        return;
      }

      const target = matches.at(nextPos);
      if (!target) return;
      mutations.resetSelected();
      mutations.addSelected(target.index);
      this.scrollSelectedIntoView();
    },
    preventDefault(event) {
      // Wrapper around prevent default.
      event.preventDefault();
    },
    copyCut(event, key) {
      if (event.target.tagName.toLowerCase() === "input") {
        return;
      }

      const items = state.selected
        .map((i) => {
          const item = state.req.items.at(i);
          if (!item) return null;
          return {
            from: item.path,
            fromSource: state.req.source,
            name: item.name,
          };
        })
        .filter(Boolean);

      if (items.length === 0) {
        return;
      }

      this.clipboard = {
        key: key,
        items: items,
        path: state.route.path,
      };
      this.internalClipboardTimestamp = Date.now();
    },
    async collectFilesFromEntry(entry, relativePath = "") {
      const files = [];
      const entryPath = relativePath ? `${relativePath}/${entry.name}` : entry.name;

      if (entry.isFile) {
        // If it's a file we get the File object and add it with its relative path
        const file = await new Promise((resolve, reject) => {
          entry.file(resolve, reject);
        });
        files.push({
          file,
          relativePath: entryPath,
        });
      } else if (entry.isDirectory) {
        // But if it's a directory, read the contents (readEntries may require
        // multiple calls — browsers often return at most ~100 entries per batch)
        const reader = entry.createReader();
        const entries = await readAllDirectoryEntries(reader, entryPath);
        // and then for each child recursively collect files
        for (const childEntry of entries) {
          const childFiles = await this.collectFilesFromEntry(
            childEntry,
            entryPath
          );
          files.push(...childFiles);
        }
      }
      return files;
    },

    async getClipboardItems(items) {
      const files = [];
      for (const item of items) {
        if (item.type === 'entry') {
          const collected = await this.collectFilesFromEntry(item.entry);
          files.push(...collected);
        } else if (item.type === 'file') {
          files.push({ file: item.file, relativePath: item.file.name });
        }
      }
      return files;
    },

    async handlePaste(event) {
      // We can paste multiple files from clipboard and upload all of them at once, but
      // Firefox has an issue https://bugzilla.mozilla.org/show_bug.cgi?id=864052  (12 years old lol)
      // the clipboard entry is always getting 1 file (the first file/folder in selection),
      // while chromium sees all and has no issues with it. Not sure about safari.
      if (getters.currentView() !== "listingView" || getters.currentPromptName()) return;

      // Check if internal clipboard is recent (20 seconds)
      const internalRecent = this.clipboard.items.length > 0 && (Date.now() - this.internalClipboardTimestamp) < 20000;

      // If internal is recent (<20s), use it immediately, in case someone have both: internal and external clipboard with a file entry.
      // After those 20s, if the OS clipboard has a file as most recent entry, will use that.
      if (internalRecent) {
        await this.handleInternalPaste();
        event.preventDefault();
        event.stopPropagation();
        return;
      }

      if (event.clipboardData?.items) {
        // Collect all items from clipboard
        const collectedItems = [];
        // And loop through all items
        for (const item of Array.from(event.clipboardData.items)) {
          if (item.kind !== 'file') continue;

          const entry = item.webkitGetAsEntry();
          if (entry) {
            collectedItems.push({ type: 'entry', entry });
          } else {
            const file = item.getAsFile();
            if (file) {
              collectedItems.push({ type: 'file', file });
            }
          }
        }

        // Then process the collected items (files and folders) asynchronously
        const itemsFromClipboard = await this.getClipboardItems(collectedItems);
        const files = itemsFromClipboard.map(item => item.file);

        // If found external files, upload them
        if (files.length > 0) {
          event.preventDefault();
          event.stopPropagation();

          const canUpload = getters.sourcePermissions()?.modify;
          if (canUpload) {
            // Pass the full array of {file, relativePath} to preserve directory structure
            mutations.showPrompt({
              name: "upload",
              props: {
                initialItems: itemsFromClipboard,
                targetPath: state.req.path,
              },
            });
          }
          return;
        }
      }
      // If internal clipboard exists but is not recent (>20s), and we don't have any file in clipboard, use the internal one
      if (this.clipboard.items.length > 0) {
        console.log('No external files, using internal clipboard.');
        await this.handleInternalPaste();
        event.preventDefault();
        event.stopPropagation();
        return;
      }
    },

    async handleInternalPaste() {
      if (!this.clipboard?.items || this.clipboard.items.length === 0) {
        return;
      }

      // Construct destination path properly (without URL prefix)
      const destPath = state.req.path.endsWith('/') ? state.req.path : `${state.req.path}/`;

      const items = this.clipboard.items.map((item) => ({
        from: item.from,
        fromSource: item.fromSource,
        to: destPath + item.name,
        toSource: state.req.source,
        name: item.name,
      }));

      const operation = this.clipboard.key === "x" ? "move" : "copy";

      // Show confirmation prompt first
      mutations.showPrompt({
        name: "CopyPasteConfirm",
        props: {
          operation: operation,
          items: items,
          onConfirm: () => {
            return new Promise((resolve, reject) => {

              const action = async (overwrite, rename) => {
                try {
                  if (getters.isShare()) {
                    await resourcesApi.moveCopyPublic(state.shareInfo.hash, items, operation, overwrite, rename);
                  } else {
                    await resourcesApi.moveCopy(items, operation, overwrite, rename);
                  }
                  if (operation === "move") {
                    this.clipboard = { items: [] };
                    this.internalClipboardTimestamp = 0;
                  }
                  mutations.setReload(true);
                  resolve();
                } catch (error) {
                  console.error("Error moving/copying items:", error);
                  reject(error);
                }
              };

              if (this.clipboard.path === state.route.path) {
                void action(false, true);
                return;
              }

              const conflict = checkConflict(items, state.req.items);

              if (conflict) {
                mutations.showPrompt({
                  name: "replace-rename",
                  pinned: true,
                  confirm: (event, option) => {
                    const overwrite = option === "overwrite";
                    const rename = option === "rename";
                    event.preventDefault();
                    mutations.closeTopPrompt();
                    void action(overwrite, rename);
                  },
                });
                return;
              }
              void action(false, false);
            });
          },
        },
      });
    },
    dragEnter(event) {
      // If in upload share mode, let the embedded Upload component handle it
      if (state.shareInfo?.shareType === 'upload') {
        return;
      }
      const isInternal = Array.from(event.dataTransfer.types).includes(
        "application/x-filebrowser-internal-drag"
      );
      if (isInternal) return;

      if (!this.$el.contains(event.relatedTarget)) {
        this.dragTargets.add(this.$el);
      }
    },
    dragLeave(event) {
      // If in upload share mode, let the embedded Upload component handle it
      if (state.shareInfo?.shareType === 'upload') {
        return;
      }
      const isInternal = Array.from(event.dataTransfer.types).includes(
        "application/x-filebrowser-internal-drag"
      );
      if (isInternal) return;

      if (!this.$el.contains(event.relatedTarget)) {
        this.dragTargets.delete(this.$el);
      }
    },
    async drop(event) {
      event.preventDefault();
      if (getters.isShare() && !state.shareInfo.allowCreate) {
        return
      }
      const isInternal = Array.from(event.dataTransfer.types).includes(
        "application/x-filebrowser-internal-drag"
      );

      if (isInternal) {
        return;
      }
      await this.handleDrop(event);
    },
    async uploadInput(event) {
      await this.handleDrop(event);
    },
    windowsResize: throttle(function () {
      if (!this.isResizing) {
        this.isResizing = true;
        this.$refs.listingView?.classList.add("resizing");
      }
      if (this.resizeTimeout) {
        clearTimeout(this.resizeTimeout);
      }
      this.width = window.innerWidth;
      this.resizeTimeout = setTimeout(() => {
        this.isResizing = false;
        this.$refs.listingView?.classList.remove("resizing");
      }, 150);
    }, 100),
    openContext(event) {
      event.preventDefault();
      event.stopPropagation();

      // Prevent opening if already open
      if (getters.currentPromptName() === "ContextMenu") {
        return;
      }
      
      mutations.showPrompt({
        name: "ContextMenu",
        props: {
          showCentered: getters.isMobile(),
          posX: event.clientX,
          posY: event.clientY,
          createOnly: this.selectedCount === 0,
        },
      });
    },
    trackPointerSequenceStart(event) {
      notePointerSequenceStart(event.target);
    },
    resetScrollbarClickSequence() {
      resetScrollbarClickGuard();
    },
    clickClear(event) {
      // A click produced by pressing or dragging the custom scrollbar belongs to
      // that interaction, not to "click blank space to clear". This also covers
      // the browser retargeting the click to a common ancestor when the pointer
      // is released outside the thumb.
      if (consumeScrollbarClick(event.target, event.detail)) return;
      if (isMosaicInteractionBlocked(event.target)) return;
      // These controls change presentation, including when only one item is selected.
      if (event.target.closest?.('.mosaic-density-control, .mosaic-image-source')) return;
      // Only process clicks if we're on the listing view
      if (getters.currentView() !== 'listingView') {
        return;
      }

      const targetClasses = event.target.className;

      if (typeof targetClasses === 'string' && targetClasses.includes('listing-item')) {
        return;
      }

      // if control, meta, or shift is pressed, do not clear the selection
      if (event.ctrlKey || event.metaKey || event.shiftKey) {
        return;
      }

      const sameAsBefore = state.selected === this.lastSelected;
      if (sameAsBefore && !state.multiple && getters.currentPromptName() === "") {
        mutations.resetSelected();
      }
      this.lastSelected = state.selected;
    },
    async handleDrop(event) {
      event.preventDefault();

      // If we're already in the embedded upload view, don't open a new prompt
      // The embedded Upload component will handle its own drops
      if (state.shareInfo?.shareType === 'upload') {
        return;
      }

      if (event.type === "drop") {
        mutations.showPrompt({
          name: "upload",
          props: {
            initialItems: Array.from(event.dataTransfer.items),
          },
        });
      } else {
        // This is for the <input type="file"> fallback
        const files = event.target.files;
        if (!files || files.length === 0) {
          return;
        }

        mutations.showPrompt({
          name: "upload",
          props: {
            // we send it as an array-like object so that it can be processed like a FileList by the Upload component
            initialItems: Array.from(files),
          },
        });
      }
      this.dragTargets.clear();
    },
    startRectangleSelection(event) {
      // Start rectangle selection when clicking on empty space - don't start if the click was in the status bar, an item or the header
      if (event.target.closest('.listing-item') || event.target.closest('.header') || event.target.closest('#status-bar')) {
        return;
      }

      // Don't start if it's a right click, this for avoid some issues with the context menu.
      if (event.button !== 0) return;

      this.isRectangleSelecting = true;

      // Get the position to the listing view container
      const listingRect = this.$refs.listingView.getBoundingClientRect();
      let startX = event.clientX - listingRect.left;
      let startY = event.clientY - listingRect.top;

      // Clamp to container bounds
      const statusBar = document.getElementById('status-bar');
      const statusBarVisible = statusBar && getComputedStyle(statusBar).display !== 'none';
      const maxX = listingRect.width;
      const maxY = statusBarVisible
        ? Math.max(0, statusBar.getBoundingClientRect().top - listingRect.top)
        : listingRect.height;

      startX = Math.max(0, Math.min(startX, maxX));
      startY = Math.max(0, Math.min(startY, maxY));

      this.rectangleStart = {
        x: startX,
        y: startY
      };
      this.rectangleEnd = {
        x: startX,
        y: startY
      };

      // Store the current selection state when starting rectangle
      this.initialSelectionState = [...state.selected];

      // Only clear selection when CTRL is not holded
      const hasModifier = event.ctrlKey || event.metaKey;
      if (!hasModifier) {
        mutations.resetSelected();
      }

      event.preventDefault();
    },

    updateRectangleSelection(event) {
      if (!this.isRectangleSelecting) return;

      // Get the position to the listing view container
      const listingRect = this.$refs.listingView.getBoundingClientRect();
      let endX = event.clientX - listingRect.left;
      let endY = event.clientY - listingRect.top;

      // Clamp to container bounds
      const statusBar = document.getElementById('status-bar');
      const statusBarVisible = statusBar && getComputedStyle(statusBar).display !== 'none';
      const maxX = listingRect.width;
      const maxY = statusBarVisible
        ? Math.max(0, statusBar.getBoundingClientRect().top - listingRect.top)
        : listingRect.height;

      endX = Math.max(0, Math.min(endX, maxX));
      endY = Math.max(0, Math.min(endY, maxY));

      this.rectangleEnd = {
        x: endX,
        y: endY
      };

      // Use requestAnimationFrame to batch updates
      if (!this.selectionUpdatePending) {
        this.selectionUpdatePending = true;
        this.rafId = requestAnimationFrame(() => {
          this.updateSelectedItemsInRectangle(event.ctrlKey || event.metaKey);
          this.selectionUpdatePending = false;
        });
      }
    },

    endRectangleSelection(event) {
      if (!this.isRectangleSelecting) return;

      // Cancel any pending animation frame
      if (this.rafId) {
        cancelAnimationFrame(this.rafId);
        this.rafId = null;
      }

      this.isRectangleSelecting = false;
      this.selectionUpdatePending = false;
      this.updateSelectedItemsInRectangle(event.ctrlKey || event.metaKey);
      this.wakeMount();

      // Clear rectangle after a short delay
      setTimeout(() => {
        this.rectangleStart = { x: 0, y: 0 };
        this.rectangleEnd = { x: 0, y: 0 };
        this.initialSelectionState = [];
      }, 100);
    },

    updateSelectedItemsInRectangle(isAdditive) {
      if (!this.isRectangleSelecting) return;

      const listingRect = this.$refs.listingView.getBoundingClientRect();
      const rect = {
        left: Math.min(this.rectangleStart.x, this.rectangleEnd.x),
        top: Math.min(this.rectangleStart.y, this.rectangleEnd.y),
        right: Math.max(this.rectangleStart.x, this.rectangleEnd.x),
        bottom: Math.max(this.rectangleStart.y, this.rectangleEnd.y)
      };

      const rectangleSelectedIndexes = [];

      // Get all item elements - use querySelectorAll with specific selector for better performance
      const itemElements = this.$el.querySelectorAll('.listing-item[data-index]');

      itemElements.forEach((element) => {
        const elementRect = element.getBoundingClientRect();

        // Convert element position to be relative to listing view, this allows selection while scrolling
        const elementRelativeRect = {
          left: elementRect.left - listingRect.left,
          top: elementRect.top - listingRect.top,
          right: elementRect.right - listingRect.left,
          bottom: elementRect.bottom - listingRect.top
        };

        // Check if the item intersects with the rectangle
        if (
          elementRelativeRect.left < rect.right &&
          elementRelativeRect.right > rect.left &&
          elementRelativeRect.top < rect.bottom &&
          elementRelativeRect.bottom > rect.top
        ) {
          const index = parseInt(element.getAttribute('data-index'), 10);
          if (!Number.isNaN(index)) {
            rectangleSelectedIndexes.push(index);
          }
        }
      });

      // Batch DOM updates to minimize reflows
      if (isAdditive) {
        // only add more items to the current selection without reset selection
        const newSelection = [...state.selected];
        rectangleSelectedIndexes.forEach(index => {
          if (!newSelection.includes(index)) {
            newSelection.push(index);
          }
        });

        mutations.resetSelected();
        newSelection.forEach(index => { mutations.addSelected(index); });
      } else {
        // Select only the items in the rectangle and reset initial selection
        // PS: If you don't want that just hold ctrl, the selection will not be reset, allowing multi select.
        mutations.resetSelected();
        rectangleSelectedIndexes.forEach(index => { mutations.addSelected(index); });
      }
    },
    handleDoubleClick(event) {
      if (event.target.closest('.listing-item')) return;
      if (getters.currentView() !== 'listingView' || getters.currentPromptName()) return;
      if (event.ctrlKey || event.metaKey || event.shiftKey) return; // Don't interfere when pressing any mod key
      mutations.selectAllItems({ multiple: getters.isMobile() });
    },
  },
};
</script>

<style scoped>
.listing-view-root {
  height: 100%;
  display: flex;
  flex-direction: column;
  padding-right: 0.5em;
}

.add-padding {
  padding-left: 0.5em;
}
.font-size-large h2 {
  font-size: 2em !important;
}

.listing-items.dropping {
  margin: 0.5em;
  overflow: hidden;
  min-height: 0;
  flex: 1 1 0;
}

.listing-items {
  position: relative;
  flex: 1;
}

.listing-item .pinned-indicator {
  font-size: 1rem;
}

.folder-items a {
  border-width: var(--borderWidth);
  border-style: solid;
}

/* Upload Share Styles */
.upload-share-embed {
  padding: 2em;
  max-width: 768px;
  margin: 0 auto;
}

.selection-rectangle {
  position: absolute;
  border: 2px solid var(--primaryColor);
  background-color: color-mix(in srgb, var(--primaryColor) 25%, transparent);
  border-radius: 8px;
  pointer-events: none;
  z-index: 10;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.15);
}

.rectangle-selecting {
  cursor: crosshair;
  user-select: none;
}

.rectangle-selecting .listing-item {
  pointer-events: none;
}

.drop-indicator {
  position: absolute;
  inset: 0;
  bottom: 1.75em;
  z-index: 50;
  border: 0.2em dashed var(--primaryColor);
  background: rgba(0, 0, 0, 0.3);
  backdrop-filter: blur(2px);
  border-radius: 1em;
  display: flex;
  align-items: center;
  justify-content: center;
  pointer-events: none;
  box-shadow: var(--primaryColor) 0 0 1em;
}

.drop-indicator-content {
  text-align: center;
  color: var(--textPrimary);
  font-size: 1.5em;
}

.drop-indicator-content i {
  font-size: 4em;
}
</style>

<style>
.mosaic-root { overflow-anchor: none; }
.listing-items.mosaic { touch-action: pan-y; }
.mosaic-zoom-preview { position: fixed; z-index: 1000; pointer-events: none; background: var(--surfacePrimary, #222); box-shadow: 0 3px 20px #0008; border: 2px solid var(--primaryColor); box-sizing: border-box; }
.mosaic-zoom-preview img { width: 100%; height: 100%; object-fit: contain; }
.mosaic-zoom-preview span { position: absolute; left: 50%; bottom: 6px; transform: translateX(-50%); background: #000b; color: white; padding: 5px 8px; border-radius: 4px; white-space: nowrap; font-size: 12px; }
</style>
