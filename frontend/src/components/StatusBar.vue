<!-- Modified from FileBrowser Quantum for the mument derivative. See MODIFICATIONS.md. -->
<template>
  <div id="status-bar" :style="moveWithSidebar" :class="{ 'dark-mode-header': isDarkMode, 'active': showStatusBar, 'mosaic-status': isMosaic && currentView === 'listingView' }" @contextmenu.prevent.stop @touchstart.stop @touchend.stop>
    <div class="status-content" @contextmenu.prevent.stop @touchstart.stop @touchend.stop>
      <!-- Left side: selection/directory info and stats for the editor and markdown viewer -->
      <div class="status-info">
        <template v-if="isEditorOrMarkdownView">
          <span>{{ editorStatsText }}</span>
        </template>
        <template v-else>
          <span v-if="selectedCount > 0" class="button">{{ selectedCount }}</span>
          <span v-if="selectedCount > 0">{{ selectedItemsText }}</span>
          <span v-else>{{ directoryInfoText }}</span>
        </template>
      </div>
      <!-- Right side: selection actions, then a slider for listing size / font size -->
      <div class="status-controls">
        <button
          v-if="selectedCount > 0 && currentView === 'listingView'"
          id="selection-actions"
          type="button"
          class="selection-actions-button"
          :aria-label="$t('buttons.moreActions')"
          :title="$t('buttons.moreActions')"
          @click="openSelectionActions"
          @keydown.enter.stop
          @keydown.space.stop.prevent
        >
          <i class="material-symbols" aria-hidden="true">more_horiz</i>
        </button>
        <button
          v-if="currentView === 'listingView'"
          id="reset-folder-view"
          type="button"
          class="folder-preference-reset"
          data-testid="reset-folder-view"
          :disabled="!canResetFolderView"
          :title="folderViewResetTitle"
          :aria-label="$t('files.resetFolderView')"
          @click="resetFolderView"
        >
          <i class="material-symbols" aria-hidden="true">settings_backup_restore</i>
          <span class="reset-label">{{ $t("files.resetFolderView") }}</span>
        </button>
        <button
          v-if="currentView === 'listingView'"
          id="reset-folder-sorting"
          type="button"
          class="folder-preference-reset"
          data-testid="reset-folder-sorting"
          :disabled="!canResetFolderSorting"
          :title="folderSortingResetTitle"
          :aria-label="$t('files.resetFolderSorting')"
          @click="resetFolderSorting"
        >
          <i class="material-symbols" aria-hidden="true">sort</i>
          <span class="reset-label">{{ $t("files.resetFolderSorting") }}</span>
        </button>
        <label v-if="isMosaic && currentView === 'listingView'" class="mosaic-density-control">
          <span>{{ $t("general.size") }}</span>
          <input id="mosaic-density" v-model="mosaicDensityPercent" type="range" min="0" max="100" step="1" :aria-label="$t('files.mosaicDensity')" :title="$t('files.mosaicPinchHint')" :aria-valuetext="mosaicDensityPercent === 100 ? $t('files.mosaicSingleColumn') : `${mosaicDensityPercent}%`" />
        </label>
        <label v-if="isMosaic && currentView === 'listingView'" class="mosaic-image-source">
          <span>{{ $t("files.mosaicImageSource") }}</span>
          <select id="mosaic-image-source" v-model="mosaicImageSource" :aria-label="$t('files.mosaicImageSource')" :title="$t('files.mosaicImageSourceHint')">
            <option value="thumbnail">{{ $t("files.mosaicThumbnail") }}</option>
            <option value="original">{{ $t("files.mosaicOriginal") }}</option>
          </select>
        </label>
        <div v-if="showGallerySizeSlider && !isMosaic" class="gallery-size-control">
          <span class="size-label">{{ $t("general.size") }}</span>
          <input
            v-model="gallerySize"
            type="range"
            id="gallery-size"
            name="gallery-size"
            min="1"
            max="9"
            @input="updateGallerySize"
            @change="commitGallerySize"
          />
        </div>
        <div v-if="currentView === 'editor'" class="gallery-size-control">
          <span class="size-label">{{ $t("general.size") }}</span>
          <input
            v-model="editorFontSize"
            type="range"
            min="8"
            max="24"
            step="1"
          />
          <span class="size-label">{{ editorFontSize }}px</span> <!-- eslint-disable-line @intlify/vue-i18n/no-raw-text -->
        </div>
      </div>
    </div>
  </div>
</template>

<script>
import { getMosaicDensity, setMosaicDensity } from "@/utils/mosaicDensity.js";
import { getMosaicImageSource, setMosaicImageSource } from "@/utils/mosaicImageSource.js";
import { getters, mutations, state } from "@/store";
import { getHumanReadableFilesize } from "@/utils/filesizes";
import { notify } from "@/notify";

export default {
  name: "StatusBar",
  data() {
    return {
      gallerySize: state.user?.gallerySize,
    };
  },
  computed: {
    mosaicDensityPercent: {
      get() { return Math.round(getMosaicDensity(state.user?.username) * 100); },
      set(value) { setMosaicDensity(state.user?.username, Number(value) / 100); },
    },
    mosaicImageSource: {
      get() { return getMosaicImageSource(state.user?.username); },
      set(value) { setMosaicImageSource(state.user?.username, value); },
    },
    isMosaic() { return getters.viewMode() === "mosaic"; },
    canResetFolderView() {
      return getters.hasFolderViewPreference();
    },
    canResetFolderSorting() {
      return getters.hasFolderSortingPreference();
    },
    folderViewResetTitle() {
      return `${this.$t("files.resetFolderView")} — ${this.$t("files.resetFolderViewHint")}`;
    },
    folderSortingResetTitle() {
      return `${this.$t("files.resetFolderSorting")} — ${this.$t("files.resetFolderSortingHint")}`;
    },
    currentView() {
      return getters.currentView();
    },
    isEditorOrMarkdownView() {
      return getters.isEditorOrMarkdownView();
    },
    showStatusBar() {
      return getters.showStatusBar();
    },
    showGallerySizeSlider() {
      return getters.showGallerySizeSlider();
    },
    isDarkMode() {
      return getters.isDarkMode();
    },
    selectedCount() {
      return getters.selectedCount();
    },
    numDirs() {
      return getters.reqNumDirs();
    },
    numFiles() {
      return getters.reqNumFiles();
    },
    totalDirectorySize() {
      if (!Array.isArray(state.req?.items)) return 0;
      return state.req.items.reduce((total, item) => total + (item.size || 0), 0);
    },
    // Calculate total size of selected items
    totalSelectedSize() {
      if (this.selectedCount === 0) return 0;
      if (!Array.isArray(state.req?.items)) {
        return 0;
      }
      let total = 0;
      state.selected.forEach(index => {
        if (index >= 0 && index < state.req?.items.length) {
          const item = state.req.items.at(index);
          if (item?.size) {
            total += item.size;
          }
        }
      });
      return total;
    },
    // Total size
    displayTotalSize() {
      const size = this.selectedCount > 0 ? this.totalSelectedSize : this.totalDirectorySize;
      return getHumanReadableFilesize(size);
    },
    // i18n labels with compile-time checked keys
    itemsSelectedLabel() {
      if (this.selectedCount === 1) {
        return this.$t("general.selectionSingle");
      }
      return this.$t("general.selectionMultiple");
    },
    foldersLabel() {
      return this.numDirs === 1
        ? this.$t("general.folder")
        : this.$t("general.folders");
    },
    filesLabel() {
      return this.numFiles === 1
        ? this.$t("general.file")
        : this.$t("general.files");
    },
    // Complete text with raw characters moved from template
    selectedItemsText() {
      return `${this.itemsSelectedLabel} (${this.displayTotalSize})`;
    },
    directoryInfoText() {
      const dirs = this.numDirs;
      const files = this.numFiles;
      const sizeText = `(${this.displayTotalSize})`;

      if (dirs === 0 && files === 0) {
        return this.$t('files.lonely');
      }
      const parts = [];
      if (dirs > 0) parts.push(`${dirs} ${this.foldersLabel}`);
      if (files > 0) parts.push(`${files} ${this.filesLabel}`);

      return `${parts.join(' | ')} ${sizeText}`;
    },
    moveWithSidebar() {
      if (getters.isStickySidebar() && getters.isSidebarVisible()) {
        return {
          left: `${state.sidebar.width}em`,
        };
      }
      return {};
    },
    editorStatsText() {
      const { lines, words, chars } = state.editorStats;
      const parts = [];
      if (words !== null) parts.push(this.$t('editor.words', { count: words }));
      if (chars !== null) parts.push(this.$t('editor.chars', { count: chars }));
      if (lines !== null) parts.push(this.$t('editor.lines', { count: lines }));
      return parts.join(' | ');
    },
    editorFontSize: {
      get() {
        return state.editorFontSize;
      },
      set(value) {
        mutations.setEditorFontSize(value);
      }
    },
  },
  mounted() {
    window.addEventListener('wheel', this.handleWheel, { passive: false });
  },
  beforeUnmount() {
    window.removeEventListener('wheel', this.handleWheel);
  },
  methods: {
    /** Open the existing context menu for the current selection, centered for touch. */
    openSelectionActions() {
      if (getters.selectedCount() === 0) return;
      mutations.showPrompt({
        name: "ContextMenu",
        props: { showCentered: true },
      });
    },
    updateGallerySize(event) {
      const newValue = parseInt(event.target.value, 10);
      this.gallerySize = newValue;
    },
    commitGallerySize() {
      mutations.setGallerySize(this.gallerySize);
      // Automatically adjust view mode based on gallery size
      this.adjustViewMode();
    },
    adjustViewMode() {
      const currentMode = getters.viewMode();
      let newMode = currentMode;
      const size = this.gallerySize;

      // Gallery/Icons family - switch based on size
      if (currentMode === "gallery" || currentMode === "icons") {
        if (size <= 4) {
          newMode = "icons";
        } else {
          newMode = "gallery";
        }
      }

      // List/Compact family - switch based on size
      if (currentMode === "list" || currentMode === "compact") {
        if (size <= 3) {
          newMode = "compact";
        } else {
          newMode = "list";
        }
      }

      // Only update if the mode actually changed; the mutation writes only the
      // current folder (or the page-local advanced-search view).
      if (newMode !== currentMode) {
        mutations.setCurrentListingViewMode(newMode);
      }
    },
    resetFolderView() {
      if (mutations.resetFolderViewMode()) {
        notify.showSuccessToast(this.$t("files.folderViewRestored"));
      }
    },
    resetFolderSorting() {
      if (mutations.resetFolderSorting()) {
        mutations.updateListingItems();
        notify.showSuccessToast(this.$t("files.folderSortingRestored"));
      }
    },
    // Ctrl + Mouse Wheel to adjust the slider sizes
    handleWheel(event) {
      if (!(event.ctrlKey || event.metaKey)) return;

      if (this.isMosaic) return;
      const delta = event.deltaY > 0 ? 1 : -1; // Scroll down increases, up decreases

      const advSearch = (state.route.path || "").startsWith("/tools/advancedSearch");
      if (this.currentView === "listingView" || advSearch) {
        event.preventDefault();
        const newSize = Math.min(9, Math.max(1, this.gallerySize - delta));
        if (newSize !== this.gallerySize) {
          this.gallerySize = newSize;
          mutations.setGallerySize(newSize);
          this.adjustViewMode();
        }
      } else if (this.currentView === 'editor') {
        event.preventDefault();
        const newSize = Math.min(24, Math.max(8, this.editorFontSize - delta));
        if (newSize !== this.editorFontSize) {
          this.editorFontSize = newSize;
        }
      }
    }
  },
};
</script>

<style scoped>
#status-bar {
  background-color: color-mix(in srgb, var(--alt-background) 15%, transparent);
  height: 2.5em;
  display: flex;
  align-items: center;
  position: fixed;
  bottom: -2.5em;
  left: 0;
  right: 0;
  z-index: 2;
  border-radius: 2px;
  overflow: hidden;
  margin: 0;
  padding: 0;
  transition: bottom 0.5s ease, left 0.2s ease, width 0.2s ease;
  pointer-events: none;
}

#status-bar.active {
  bottom: 0;
  pointer-events: auto;
}

.status-content {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  padding: 0 1em;
  height: 100%;
  font-size: 0.85em;
}

.status-info {
  display: flex;
  align-items: center;
  color: var(--textSecondary);
  font-weight: 500;
  gap: 0.25em;
}

.button {
  padding: 0 0.5em;
  font-size: 0.9em;
  font-weight: bold;
  cursor: unset;
}

.status-controls {
  display: flex;
  align-items: center;
  gap: 1.5em;
}

.selection-actions-button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 2.5em;
  min-height: 2.5em;
  padding: 0 0.35em;
  border: 0;
  border-radius: 4px;
  background: transparent;
  color: inherit;
  cursor: pointer;
}

.selection-actions-button:hover,
.selection-actions-button:focus-visible {
  background-color: color-mix(in srgb, currentColor 12%, transparent);
}

.selection-actions-button .material-symbols {
  font-size: 1.4em;
}

.folder-preference-reset {
  display: inline-flex;
  align-items: center;
  gap: 0.25em;
  min-height: 2.5em;
  padding: 0 0.35em;
  border: 0;
  border-radius: 4px;
  background: transparent;
  color: inherit;
  font-size: 0.85em;
  cursor: pointer;
  white-space: nowrap;
}

.folder-preference-reset:hover:not(:disabled),
.folder-preference-reset:focus-visible {
  background-color: color-mix(in srgb, currentColor 12%, transparent);
}

.folder-preference-reset:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.folder-preference-reset .material-symbols {
  font-size: 1.3em;
}

.gallery-size-control {
  display: flex;
  align-items: center;
  gap: 0.5em;
}

.size-label {
  color: var(--textSecondary);
  font-size: 0.875em;
  white-space: nowrap;
}

input[type="range"] {
  accent-color: var(--primaryColor);
  width: 8em;
}

/* Backdrop filter support */
@supports (backdrop-filter: none) {
  #status-bar {
    backdrop-filter: blur(16px) invert(0.1);
  }
}

/* Mobile styles */
@media (max-width: 768px) {
  #status-bar {
    height: 3em;
    bottom: -3em;
    font-size: 0.9em;
    box-shadow: 0 -2px 10px rgba(0, 0, 0, 0.1);
  }

  #status-bar.active {
    bottom: 0;
    pointer-events: auto;
  }

  .status-content {
    padding: 0 0.8em;
  }

  .status-controls {
    gap: 1.2em;
  }

  /* Touch target: at least 44px even though the bar is only 3em tall. */
  .selection-actions-button {
    min-width: 44px;
    min-height: 44px;
    margin-left: -0.35em;
  }

  .folder-preference-reset {
    min-width: 40px;
    min-height: 44px;
    justify-content: center;
    padding: 0 0.2em;
  }

  .folder-preference-reset .reset-label {
    display: none;
  }

  input[type="range"] {
    width: 7em;
  }

  .status-info {
    font-size: 1em;
  }

  .size-label {
    font-size: 0.9em;
  }
}
</style>
<style>
.mosaic-density-control { display: flex; align-items: center; gap: 5px; font-size: 12px; white-space: nowrap; }
.mosaic-density-control input[type="range"] { width: 90px; margin: 0; }
.mosaic-status .status-info { min-width: 0; overflow: hidden; }
.mosaic-status .status-info > span:last-child { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mosaic-status .status-controls { flex: none; gap: 10px; }
@media (max-width: 480px) {
  .mosaic-status .status-content { gap: 8px; padding: 0 8px; }
  .mosaic-status .mosaic-image-source > span { display: none; }
  .mosaic-density-control input[type="range"] { width: 76px; }
}
.mosaic-image-source { display: flex; align-items: center; gap: 6px; font-size: 12px; white-space: nowrap; }
.mosaic-image-source select { width: auto; max-width: 110px; margin: 0; padding: 3px 6px; color: var(--textPrimary); background: var(--surfacePrimary); border: 1px solid var(--divider); border-radius: 4px; }
</style>
