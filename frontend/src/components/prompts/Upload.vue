<!-- Modified from FileBrowser Quantum for the mument derivative. See MODIFICATIONS.md. -->
<template>
  <div class="card-content">
    <p style="text-align: center">{{ uploadSettingsDescription }}</p>
    <div>
      <SettingsItem :title="$t('fileLoading.uploadSettings')" :collapsable="true" :start-collapsed="true">
        <div class="settings-items upload-settings">
          <div class="settings-number-input item">
            <div class="no-padding">
              <label for="maxConcurrentUpload">{{ $t("fileLoading.maxConcurrentUpload") }}</label>
              <HelpTooltipIcon :text="$t('fileLoading.maxConcurrentUploadHelp')" />
            </div>
            <div>
              <input v-model.number="maxConcurrentUpload" type="range" min="1" max="10" @change="updateUploadSettings" />
              <span class="range-value">{{ maxConcurrentUpload }}</span>
            </div>
          </div>
          <div class="settings-number-input item">
            <div class="no-padding">
              <label for="uploadChunkSizeMb">{{ $t("fileLoading.uploadChunkSizeMb") }}</label>
              <HelpTooltipIcon :text="$t('fileLoading.uploadChunkSizeMbHelp')" />
            </div>
            <div class="no-padding">
              <input class="sizeInput input" v-model.number="uploadChunkSizeMb" type="number" min="0" @change="updateUploadSettings" />
            </div>
          </div>
          <ToggleSwitch class="item" v-model="clearAll" @change="updateUploadSettings"
            :name="$t('fileLoading.clearAll')"
            :description="$t('fileLoading.clearAllDescription')" />
        </div>
      </SettingsItem>
      <div class="upload-prompt" :class="{ dropping: isDragging }"
        @dragenter.prevent="onDragEnter"
        @dragover.prevent="onDragOver" @dragleave.prevent="onDragLeave" @drop.prevent="onDrop">
        <div class="upload-prompt-container">
          <i v-if="files.length === 0" class="material-symbols">cloud_upload</i>
          <p v-if="files.length === 0">{{ $t("prompts.dragAndDrop") }}</p>
          <ButtonGroup
            class="upload-action-buttons"
            :buttons="uploadActionButtons"
            action-mode
            @button-clicked="onUploadAction"
          />
        </div>
      </div>
    </div>
    <div v-if="showEnumerationProgress" class="upload-progress-summary" role="status" aria-live="polite">
      <span class="upload-progress-count">
        {{ $t("prompts.uploadProgressEnqueued", {
          enqueued: enumeration.enqueued,
          uploaded: enumeration.uploaded,
        }) }}
      </span>
      <span v-if="enumeration.active > 0" class="upload-scanning">
        {{ $t("prompts.uploadScanningFolder") }}
      </span>
      <button
        v-if="enumeration.active > 0"
        type="button"
        class="button button--flat upload-stop-enumeration"
        @click="stopEnumeration"
        :aria-label="$t('prompts.uploadStopScanning')"
        :title="$t('prompts.uploadStopScanning')"
      >
        {{ $t("prompts.uploadStopScanning") }}
      </button>
    </div>
    <div v-if="files.length > 0" style="position: relative;">
      <div v-if="showConflictPrompt" class="conflict-overlay">
        <div class="card">
          <div class="card-content">
            <p>{{ $t("prompts.conflictsDetected") }}</p>
          </div>
          <div class="card-actions">
            <button
              type="button"
              @click="resolveConflict(true)"
              class="button button--flat button--red"
            >
              {{ $t("general.replace") }}
            </button>
          </div>
        </div>
      </div>
      <div class="upload-list">
        <div v-for="file in files" :key="file.id" class="upload-item">
          <i class="material-symbols file-icon">{{ file.type === "directory" ? "folder" : "insert_drive_file" }}</i> <!-- eslint-disable-line @intlify/vue-i18n/no-raw-text -->
          <div class="file-info">
            <p class="file-name">{{ file.name }}</p>
            <progress-bar v-if="file.type !== 'directory'" :val="file.status === 'completed'
                ? $t('prompts.completed')
                : file.status === 'error'
                  ? $t('prompts.error')
                  : file.status === 'conflict'
                    ? $t('prompts.conflictsDetected')
                    : (file.progress / 100) * file.size
              " :unit="file.status === 'completed' || file.status === 'error' ? '' : 'bytes'" :max="file.size"
              :status="file.status" text-position="inside" size="20"
              :help-text="getHelpText(file)">
            </progress-bar>
            <div v-else class="status-label">
              <span>{{ getStatusText(file.status, file) }}</span>
            </div>
          </div>
          <div class="file-actions">
            <button
              v-if="file.status === 'uploading'"
              type="button"
              @click="uploadManager.pause(file.id)"
              class="action"
              :aria-label="$t('general.pause')"
              :title="$t('general.pause')"
            >
              <i class="material-symbols">pause</i>
            </button>
            <button
              v-if="file.status === 'paused'"
              type="button"
              @click="uploadManager.resume(file.id)"
              class="action"
              :aria-label="$t('general.resume')"
              :title="$t('general.resume')"
            >
              <i class="material-symbols">play_arrow</i>
            </button>
            <button
              v-if="file.status === 'error'"
              type="button"
              @click="uploadManager.retry(file.id)"
              class="action"
              :aria-label="$t('general.retry')"
              :title="$t('general.retry')"
            >
              <i class="material-symbols">replay</i>
            </button>
            <button
              v-if="file.status === 'conflict'"
              type="button"
              @click="handleConflictAction(file)"
              class="action"
              :aria-label="$t('general.replace')"
              :title="$t('general.replace')"
            >
              <i class="material-symbols">sync_problem</i>
            </button>
            <button
              type="button"
              @click="cancelUpload(file.id)"
              class="action"
              :aria-label="$t('general.cancel')"
              :title="$t('general.cancel')"
            >
              <i class="material-symbols">close</i>
            </button>
          </div>
        </div>
      </div>
    </div>
  </div>

  <div class="card-actions">
    <button
      v-if="canPauseAll"
      type="button"
      @click="uploadManager.pauseAll"
      class="button button--flat"
      :aria-label="$t('buttons.pauseAll')"
      :title="$t('buttons.pauseAll')"
    >
      {{ $t("buttons.pauseAll") }}
    </button>
    <button
      v-if="canResumeAll"
      type="button"
      @click="uploadManager.resumeAll"
      class="button button--flat"
      :aria-label="$t('buttons.resumeAll')"
      :title="$t('buttons.resumeAll')"
    >
      {{ $t("buttons.resumeAll") }}
    </button>
    <button
      type="button"
      @click="clearCompleted"
      class="button button--flat"
      :disabled="!hasClearable"
      :aria-label="$t('buttons.clearCompleted')"
      :title="$t('buttons.clearCompleted')"
    >
      {{ $t("buttons.clearCompleted") }}
    </button>
  </div>

  <input ref="fileInput" @change="onFilePicked" type="file" multiple style="display: none" />
  <input ref="folderInput" @change="onFolderPicked" type="file" webkitdirectory directory multiple
    style="display: none" />
  <input ref="cameraInput" @change="onFilePicked" type="file" accept="image/*" capture="environment"
    style="display: none" />
  <input ref="videoInput" @change="onFilePicked" type="file" accept="video/*" capture="environment"
    style="display: none" />
</template>

<script>
import { ref, reactive, computed, onMounted, onUnmounted, watch } from "vue";
import { uploadManager } from "@/utils/upload";
import {
  describeDroppedEntries,
  describeFileList,
  streamUploadBatches,
  walkDroppedEntries,
  walkFileList,
} from "@/utils/uploadEnumeration";
import { getters, mutations, state } from "@/store";
import { notify } from "@/notify";
import HelpTooltipIcon from "@/components/HelpTooltipIcon.vue";
import ProgressBar from "@/components/ProgressBar.vue";
import SettingsItem from "@/components/settings/SettingsItem.vue";
import ToggleSwitch from "@/components/settings/ToggleSwitch.vue";
import ButtonGroup from "@/components/ButtonGroup.vue";
import i18n from "@/i18n";

export default {
  name: "UploadFiles",
  components: {
    HelpTooltipIcon,
    ProgressBar,
    SettingsItem,
    ToggleSwitch,
    ButtonGroup,
  },
  props: {
    initialItems: {
      type: Object,
      default: null,
    },
    filesToReplace: {
      type: Array,
      default: () => [],
    },
    targetPath: {
      type: String,
      default: null,
    },
    targetSource: {
      type: String,
      default: null,
    },
  },
  computed: {
    shareInfo() {
      return state.shareInfo;
    },
    uploadSettingsDescription() {
      const maxConcurrentUpload = state.user.fileLoading?.maxConcurrentUpload;
      const uploadChunkSizeMb = state.user.fileLoading?.uploadChunkSizeMb;
      if (uploadChunkSizeMb === 0) {
        return this.$t("prompts.uploadSettingsNoChunk", { maxConcurrentUpload });
      }
      return this.$t("prompts.uploadSettingsChunked", {
        maxConcurrentUpload,
        uploadChunkSizeMb
      });
    }
  },
  setup(props) {
    const fileInput = ref(null);
    const folderInput = ref(null);
    const cameraInput = ref(null);
    const videoInput = ref(null);
    const files = computed(() => uploadManager.queue);
    const isDragging = ref(false);
    const showConflictPrompt = ref(false);
    let conflictResolver = null;

    // Unknown-total progress for progressive folder enumeration. Files are
    // counted when they enter the queue and when they finish; no percentage is
    // invented while the walker is still discovering the tree.
    const enumeration = reactive({
      active: 0,
      enqueued: 0,
      uploaded: 0,
    });
    const activeEnumerations = new Set();

    let wakeLock = null;

    // Upload settings
    const maxConcurrentUpload = ref(state.user.fileLoading?.maxConcurrentUpload);
    const uploadChunkSizeMb = ref(state.user.fileLoading?.uploadChunkSizeMb);
    const clearAll = ref(state.user.fileLoading?.clearAll || false);

    const updateUploadSettings = async () => {
      try {
        const data = {
          ...state.user,
          fileLoading: {
            maxConcurrentUpload: maxConcurrentUpload.value,
            uploadChunkSizeMb: uploadChunkSizeMb.value,
            clearAll: clearAll.value,
          },
        };
        void mutations.updateCurrentUser(data);
        notify.showSuccessToast("Upload settings updated");
      } catch (e) {
        console.error(e);
      }
    };

    const handleConflict = (resolver, options = {}) => {
      conflictResolver = resolver;
      mutations.showPrompt({
        name: "replace-rename",
        pinned: true,
        props: {
          allowSkip: !getters.isShare(),
          allowRename: options.allowRename !== false,
          ...(getters.isShare() ? {} : { title: i18n.global.t("prompts.uploadConflictTitle") }),
        },
        confirm: (_event, option) => {
          if (option === "overwrite") {
            resolveConflict(true);
          } else if (option === "skip") {
            resolveConflict({ skip: true });
          } else if (option === "rename") {
            showRenamePrompt();
          } else {
            resolveConflict(false);
          }
        },
        // Dismissing the prompt must release a streamed session instead of
        // leaving its first batch waiting forever.
        cancel: cancelConflict,
      });
    };

    const cancelConflict = () => {
      const resolver = conflictResolver;
      conflictResolver = null;
      if (resolver) {
        resolver(false);
      }
    };

    const resolveConflict = (overwrite) => {
      const resolver = conflictResolver;
      conflictResolver = null;
      if (resolver) {
        resolver(overwrite);
      }
      mutations.closeTopPrompt(); // Only close the conflict prompt, return to upload prompt
    };

    const showRenamePrompt = () => {
      mutations.closeTopPrompt(); // Only close the replace-rename prompt, keep upload prompt open
      // Get the conflicting folder name from the upload queue
      const conflictingFolder = uploadManager.getConflictingFolder();
      if (!conflictingFolder) {
        console.error("No conflicting folder found for rename");
        cancelConflict();
        return;
      }

      mutations.showPrompt({
        name: "rename",
        cancel: cancelConflict,
        confirm: (newName) => {
          void renameUploadFolder(conflictingFolder, newName);
        },
        props: { folderName: conflictingFolder }
      });
    };

    const renameUploadFolder = async (oldName, newName) => {
      try {
        // Check if the new name already exists
        const existingItems = new Set(state.req.items.map(i => i.name));
        if (existingItems.has(newName)) {
          notify.showError(new Error(`A folder named "${newName}" already exists`));
          cancelConflict();
          return;
        }

        // Update upload manager with the new folder name
        await uploadManager.renameFolder(oldName, newName);

        // Resolve the conflict and continue upload
        const resolver = conflictResolver;
        conflictResolver = null;
        if (resolver) {
          resolver({ rename: newName });
        }
        mutations.closeTopPrompt(); // Only close the rename prompt, return to upload prompt
      } catch (error) {
        console.error(error);
        cancelConflict();
      }
    };

    const acquireWakeLock = async () => {
      if (!("wakeLock" in navigator)) {
        return;
      }
      try {
        if (wakeLock !== null) return; // Already locked
        wakeLock = await navigator.wakeLock.request("screen");
        wakeLock.addEventListener("release", () => {
          wakeLock = null;
        });
      } catch (err) {
        console.error(`Wake Lock failed: ${err.name}, ${err.message}`);
      }
    };

    const releaseWakeLock = () => {
      if (wakeLock !== null) {
        wakeLock.release();
        wakeLock = null;
      }
    };

    const isUploading = computed(() => state.upload.isUploading);

    watch(isUploading, (active) => {
      if (active) {
        void acquireWakeLock();
      } else {
        releaseWakeLock();
      }
    });

    const hasCompleted = computed(() =>
      files.value.some((file) => file.status === "completed")
    );

    const hasClearable = computed(() => {
      if (state.user.fileLoading?.clearAll) {
        // For "clear all" mode: check for completed, error, conflict, or paused uploads
        return files.value.some((file) => 
          file.status === "completed" || 
          file.status === "error" || 
          file.status === "conflict" || 
          file.status === "paused"
        );
      } else {
        // For "clear completed" mode: only check for completed uploads
        return files.value.some((file) => file.status === "completed");
      }
    });

    const canPauseAll = computed(() =>
      files.value.some((file) => file.status === "uploading")
    );

    const canResumeAll = computed(
      () =>
        !canPauseAll.value &&
        files.value.some((file) => file.status === "paused")
    );

    // The total is unknown while a folder is being walked. Show queued and
    // finished file counts instead of a computed percentage.
    const showEnumerationProgress = computed(
      () =>
        enumeration.active > 0 ||
        (enumeration.enqueued > enumeration.uploaded &&
          files.value.some(
            (file) =>
              file.status === "pending" ||
              file.status === "uploading" ||
              file.status === "paused"
          ))
    );

    const close = () => {
      mutations.closeTopPrompt();
    };

    const clearCompleted = () => {
      uploadManager.clearCompleted();
    };

    const handleVisibilityChange = async () => {
      if (document.visibilityState === "visible" && isUploading.value) {
        void acquireWakeLock();
      }
    };

    // Helper to get the destination path (from prop or fallback to current request)
    const getDestinationPath = () => props.targetPath || state.req.path;

    const processItems = async (items) => {
      const destination = getDestinationPath();
      // When items are passed as a prop from ListingView, they can be either
      // an array of DataTransferItem (from drag and drop) or an array of File (from input).
      // or an array of objects (from clipboard paste of the OS).
      if (Array.isArray(items)) {
        if (items.length === 0) return;
        const first = items[0];
        if (first instanceof File) {
          // This is an array of File objects from the input fallback in ListingView
          void processFileList(items, destination);
        } else if (typeof first.getAsFile === 'function') {
          // This is an array of DataTransferItem from drag and drop in ListingView
          await processDroppedItems(items, destination);
        } else if (first.file instanceof File && typeof first.relativePath === 'string') {
          // This is an array of objects {file, relativePath} for paste from clipboard OS.
          void uploadManager.add(destination, items);
        }
      } else if (items) {
        // This case handles a FileList object from the upload prompt's own input fields.
        // It is not an array, so we convert it.
        await processDroppedItems(Array.from(items), destination);
      }
    };

    onMounted(async () => {
      document.addEventListener("visibilitychange", void handleVisibilityChange);
      uploadManager.setOnConflict(handleConflict);
      if (props.initialItems) {
        await processItems(props.initialItems);
      }
    });

    onUnmounted(() => {
      document.removeEventListener("visibilitychange", void handleVisibilityChange);
      // Closing the prompt stops discovery; already queued uploads keep going
      // through the global upload manager, as before.
      stopEnumeration();
      uploadManager.setOnConflict(() => {}); // cleanup
      releaseWakeLock();
    });

    const triggerFilePicker = () => {
      if (fileInput.value) fileInput.value.click();
    };

    const triggerFolderPicker = () => {
      if (folderInput.value) folderInput.value.click();
    };

    const triggerCameraPicker = () => {
      if (cameraInput.value) cameraInput.value.click();
    };

    const triggerVideoPicker = () => {
      if (videoInput.value) videoInput.value.click();
    };

    const uploadActionButtons = computed(() => [
      { label: i18n.global.t("general.file"), value: "file" },
      { label: i18n.global.t("general.folder"), value: "folder" },
      { label: i18n.global.t("general.camera"), value: "camera" },
      { label: i18n.global.t("general.video"), value: "video" },
    ]);

    const onUploadAction = (value) => {
      switch (value) {
        case "file":
          triggerFilePicker();
          break;
        case "folder":
          triggerFolderPicker();
          break;
        case "camera":
          triggerCameraPicker();
          break;
        case "video":
          triggerVideoPicker();
          break;
        default:
          break;
      }
    };

    // Capture a stable snapshot before touching input.value. In Chromium a FileList
    // is a live view of its input: clearing the input empties the very object we
    // just read, so an asynchronous enumeration would later see zero files.
    // Copying the references is cheap (an array of File handles; file contents are
    // never read here) and keeps selection, conflict probes and the walker intact.
    const snapshotPickedFiles = (input) => {
      try {
        return input?.files ? Array.from(input.files) : [];
      } finally {
        // Safe to clear once the snapshot exists: repeated selections on the same
        // input start from an empty element, and a newer selection is never
        // cleared by an older asynchronous task because it owns its own array.
        if (input) input.value = '';
      }
    };

    const onFilePicked = (event) => {
      const pickedFiles = snapshotPickedFiles(event.target);
      if (pickedFiles.length === 0) return;
      void processFileList(pickedFiles, getDestinationPath());
    };

    const onFolderPicked = (event) => {
      const pickedFiles = snapshotPickedFiles(event.target);
      if (pickedFiles.length === 0) return;
      void processFileList(pickedFiles, getDestinationPath());
    };

    const onDrop = async (event) => {
      isDragging.value = false;
      const destination = getDestinationPath();
      if (event.dataTransfer.items) {
        const items = Array.from(event.dataTransfer.items);
        await processDroppedItems(items, destination);
      } else {
        const droppedFiles = event.dataTransfer.files;
        console.log(
          "Upload.vue: Processing dropped files (fallback):",
          droppedFiles
        );
        void processFileList(droppedFiles, destination);
      }
    };

    const onDragEnter = () => {
      isDragging.value = true;
    };

    const onDragOver = () => {
      isDragging.value = true;
    };

    const onDragLeave = () => {
      isDragging.value = false;
    };

    const reportUploadError = (error) => {
      console.error("Upload enumeration failed:", error);
      notify.showError(error instanceof Error ? error : new Error(String(error)));
    };

    const resetEnumerationProgress = () => {
      if (enumeration.active === 0 && !uploadManager.hasOngoingUploads()) {
        enumeration.enqueued = 0;
        enumeration.uploaded = 0;
      }
    };

    const runEnumeration = async (destination, roots, createSource) => {
      resetEnumerationProgress();
      enumeration.active += 1;
      let session = null;
      try {
        session = await uploadManager.beginBulkUpload(destination, roots, {
          progressTracker: enumeration,
        });
        activeEnumerations.add(session);
        await streamUploadBatches(createSource(session), session, {
          signal: session.signal,
        });
      } catch (error) {
        reportUploadError(error);
      } finally {
        if (session) {
          activeEnumerations.delete(session);
        }
        enumeration.active -= 1;
      }
    };

    const stopEnumeration = () => {
      for (const session of [...activeEnumerations]) {
        session.cancel();
      }
    };

    const processDroppedItems = async (items, destination) => {
      // Capture every entry before the first await: DataTransferItem objects are
      // only valid during the drop event. The tree itself is walked lazily.
      const entries = [];
      const fallbackFiles = [];
      for (const item of items) {
        const entry =
          typeof item.webkitGetAsEntry === "function"
            ? item.webkitGetAsEntry()
            : null;
        if (entry) {
          entries.push(entry);
        } else if (typeof item.getAsFile === "function") {
          const file = item.getAsFile();
          if (file) fallbackFiles.push(file);
        }
      }

      if (fallbackFiles.length > 0) {
        await processFileList(fallbackFiles, destination);
      }
      if (entries.length === 0) return;

      let unreadable = 0;
      await runEnumeration(
        destination,
        describeDroppedEntries(entries),
        (session) =>
          walkDroppedEntries(entries, {
            signal: session.signal,
            onError: () => {
              unreadable += 1;
            },
          })
      );
      if (unreadable > 0) {
        notify.showErrorToast(
          i18n.global.t("prompts.uploadEnumerationErrors", {
            count: unreadable,
          })
        );
      }
    };

    const processFileList = async (fileList, destination) => {
      if (!fileList || fileList.length === 0) return;
      await runEnumeration(
        destination,
        describeFileList(fileList),
        (session) =>
          walkFileList(fileList, { signal: session.signal })
      );
    };

    const handleConflictAction = (file) => {
      mutations.showPrompt({
        name: "replace",
        pinned: true,
        confirm: () => {
          uploadManager.retry(file.id, true);
          mutations.closeTopPrompt();
        },
      });
    };

    const cancelUpload = (id) => {
      uploadManager.cancel(id);
    };

    const getStatusText = (status, file) => {
      // Show connection issue in status for paused uploads
      if (status === 'paused' && file?.connectionIssue) {
        return 'Paused (connection issue)';
      }
      
      switch (status) {
        case 'uploading':
          return i18n.global.t('general.uploading', { suffix: '...' });
        case 'completed':
          return i18n.global.t('prompts.completed');
        case 'error':
          return i18n.global.t('prompts.error');
        case 'paused':
          return i18n.global.t('general.paused');
        case 'conflict':
          return i18n.global.t('general.conflict');
        default:
          return status;
      }
    };

    const getHelpText = (file) => {
      if (file.status === 'error' && file.errorDetails) {
        return file.errorDetails;
      }
      if (file.status === 'paused' && file.connectionIssue) {
        return 'Connection stalled - upload paused. Click resume to retry.';
      }
      if (file.connectionIssue && file.status === 'error') {
        return file.errorDetails || 'Connection issue detected. Click retry to resume.';
      }
      return '';
    };

    return {
      triggerFilePicker,
      triggerFolderPicker,
      triggerCameraPicker,
      triggerVideoPicker,
      uploadActionButtons,
      onUploadAction,
      onFilePicked,
      onFolderPicked,
      fileInput,
      folderInput,
      cameraInput,
      videoInput,
      files,
      isDragging,
      onDragEnter,
      onDragLeave,
      onDragOver,
      onDrop,
      cancelUpload,
      uploadManager,
      close,
      clearCompleted,
      hasCompleted,
      hasClearable,
      showConflictPrompt,
      resolveConflict,
      showRenamePrompt,
      renameUploadFolder,
      canPauseAll,
      canResumeAll,
      handleConflictAction,
      maxConcurrentUpload,
      uploadChunkSizeMb,
      clearAll,
      enumeration,
      showEnumerationProgress,
      stopEnumeration,
      getStatusText,
      getHelpText,
      updateUploadSettings,
    };
  },
};
</script>

<style scoped>
.upload-prompt {
  text-align: center;
  padding: 2em;
  border: 2px dashed #ccc;
  border-radius: 8px;
  margin: 1em;
}

.dropping {
  transform: scale(0.97);
  border-radius: 1em;
  box-shadow: var(--primaryColor) 0 0 1em;
}

.upload-prompt-container {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  width: 100%;
  max-width: 100%;
  box-sizing: border-box;
}

.upload-action-buttons {
  margin: 0.5em 0 0;
}

.upload-prompt i {
  font-size: 4em;
  color: #ccc;
}

.upload-progress-summary {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5em;
  margin: 0 1em 0.5em;
  font-size: 0.9em;
}

.upload-progress-count {
  font-weight: bold;
}

.upload-scanning {
  color: #777;
}

.upload-stop-enumeration {
  margin-left: auto;
  font-size: 0.9em;
}

.upload-list {
  height: 100%;
  display: flex;
  flex-direction: column-reverse;
  min-height: 0;
}

.upload-item {
  display: flex;
  align-items: center;
}

.file-icon {
  margin-right: 0.5em;
  color: #999;
}

.file-info {
  flex-grow: 1;
}

.file-name {
  margin: 0;
  font-size: 0.9em;
}

.status-label {
  color: #777;
  font-size: 0.8em;
  margin-top: 5px;
}

.file-actions .action {
  background: none;
  border: none;
  cursor: pointer;
  padding: 0.2em;
}

.file-actions .action i {
  font-size: 1.2em;
}

.spacer {
  flex-grow: 1;
}

.card.floating {
  display: flex !important;
  flex-direction: column;
}

.card-content {
  flex-grow: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  position: relative;
  padding: 1em;
}

.conflict-overlay {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background-color: rgba(0, 0, 0, 0.7);
  z-index: 999;
  display: flex;
  justify-content: center;
  align-items: center;
}

.conflict-overlay .card {
  background-color: var(--card-background-color);
  padding: 1em;
  border-radius: 8px;
}

/* Upload settings styles */
.upload-settings {
  margin: 0 1em;
}

.settings-number-input {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  padding: 0.5em 0;
}

.settings-number-input div {
  display: flex;
  padding: 0.25em;
  align-items: center;
}

.settings-number-input .no-padding {
  padding: 0;
}

.range-value {
  margin-left: 1em;
  min-width: 2ch;
  text-align: center;
  font-weight: bold;
}

.sizeInput {
  max-width: 100px;
}
.card-actions {
  display: flex;
  justify-content: flex-end;
}
</style>