<!-- Modified from FileBrowser Quantum for the mument derivative. See MODIFICATIONS.md. -->
<template>
  <div class="card-content">
    <!-- Loading spinner overlay -->
    <!-- changed to v-show (for keep the loading spinner), otherwise the path showed in the prompt will be always "/" -->
    <div v-show="isLoading" class="loading-content">
      <LoadingSpinner size="small" mode="placeholder" />
      <p class="loading-text">{{ $t("prompts.operationInProgress") }}</p>
    </div>
    <div v-show="!isLoading">
      <PathPickerButton
        v-if="!isShareContext"
        v-model:path="destPath"
        v-model:source="destSource"
        class="move-copy-path-picker"
        :show-files="false"
        :show-folders="true"
        :placeholder="$t('sidebar.chooseSource')"
        @navigate="syncFileListFromPicker"
      />
      <file-list
        ref="fileList"
        :hide-path-chrome="!isShareContext"
        @update:selected="updateDestination"
      >
      </file-list>
    </div>
  </div>
  <div class="card-actions split-buttons" >
    <button
      type="button"
      v-if="canCreateFolder && showNewDirInput"
      class="button button--flat"
      :disabled="isLoading"
      @click="cancelNewDir"
      :aria-label="$t('general.cancel')"
      :title="$t('general.cancel')"
    >
      {{ $t("general.cancel") }}
    </button>
    <button
      type="button"
      v-if="canCreateFolder && !showNewDirInput"
      class="button button--flat"
      :disabled="isLoading"
      @click="createNewDir"
      :aria-label="$t('files.newFolder')"
      :title="$t('files.newFolder')"
    >
      <span>{{ $t("files.newFolder") }}</span>
    </button>
    <input v-if="showNewDirInput" ref="newDirInput" class="input new-dir-input" :class="{ 'form-invalid': !isDirNameValid }"
    v-model.trim="newDirName" :placeholder="$t('files.newFolderMessage')" @keydown.enter="handleEnter" />
    <button
      type="button"
      v-else
      :disabled="destContainsSrc || isLoading"
      class="button button--flat"
      @click="performOperation"
      :aria-label="operation === 'move' ? $t('general.move') : $t('general.copy')"
      :title="operation === 'move' ? $t('general.move') : $t('general.copy')"
    >
      {{ operation === 'move' ? $t('general.move') : $t('general.copy') }}
    </button>
    <button
      type="button"
      v-if="showNewDirInput"
      class="button button--flat"
      @click="createDirectory"
      :disabled="isLoading || !newDirName || !isDirNameValid"
    >
      {{ $t("general.create") }}
    </button>
  </div>
</template>

<script>
import { mutations, state, getters } from "@/store";
import FileList from "../files/FileList.vue";
import { resourcesApi } from "@/api";
import buttons from "@/utils/buttons";
import { checkConflict } from "@/utils/upload";
import { url } from "@/utils";
import { notify } from "@/notify";
import {
  notifyMoveCopyComplete,
  notifyOperationError,
} from "@/utils/appNotifications";
import { goToItemNotificationButton } from "@/utils/notificationActions";
import LoadingSpinner from "@/components/LoadingSpinner.vue";
import PathPickerButton from "@/components/files/PathPickerButton.vue";
import { eventBus } from '@/store/eventBus';
import { captureListingAnchor } from "@/utils/listingViewport.js";
import { isEntryInDirectory, normalizeListingPath } from "@/utils/listAnchor.js";
import { currentListingLifecycleId, isListingLifecycle } from "@/utils/listingLifecycle.js";
import { adaptMoveCopyResult } from "@/utils/moveResults.js";
import { diagnosticsOperationResult } from "@/utils/diagnostics/telemetry.js";
import { getDiagnosticsRuntime } from "@/utils/diagnostics/runtime.js";
import { getActiveLoad } from "@/utils/diagnostics/listingBridge.js";

export default {
  name: "move-copy",
  components: { FileList, LoadingSpinner, PathPickerButton },
  props: {
    operation: {
      type: String,
      required: true,
      validator: (value) => ["move", "copy"].includes(value),
    },
    // When true, immediately shows loading state (for drag and drop operations)
    operationInProgress: {
      type: Boolean,
      default: false,
    },
    items: {
      type: Array,
      default: null,
    },
  },
  data: () => ({
    current: window.location.pathname,
    destPath: "/", // Start at root of selected source
    destSource: null, // Will be set by FileList component
    localItems: [], // Will hold the items to operate on
    isLoading: false, // Track loading state for spinner
    submitting: false, // One submission per click; conflict confirmation is async
    operationContext: null, // Business source/lifecycle captured before the request
    promptId: null, // The prompt this instance owns; late results must not close a newer one
    ownedPromptIds: [], // Child prompts (conflict chooser) created by this submission
    showNewDirInput: false, // When true will replace the new folder button with a input field
    newDirName: "",
  }),
  computed: {
    destContainsSrc() {
      if (!this.destPath) {
        return false; // If dest is not set, we can't check containment
      }
      // Add null checks to prevent undefined errors
      if (!this.localItems || this.localItems.length === 0) {
        return false;
      }
      const itemPath = this.localItems[0]?.from;
      if (!itemPath) {
        return false; // If itemPath is undefined, we can't check containment
      }
      // For different sources, allow move to root path
      if (this.destSource !== this.localItems[0]?.fromSource) {
        return false;
      }
      // For move, prevent moving to the same directory, but for copy allow it.
      if (this.operation === "move") {
        const parentDir = `${url.removeLastDir(itemPath)}/`;
        // Only disable if moving to the exact same directory
        if (this.destPath === parentDir) {
          return true;
        }
      }
      // Prevent move/copy into itself or subdirectories (into itself too)
      return this.destPath.startsWith(`${itemPath}/`) || this.destPath === itemPath;
    },
    canCreateFolder() {
      const destSource =
        this.destSource || this.localItems[0]?.fromSource || state.req?.source;
      return getters.canCreateInSource(destSource);
    },
    isDirNameValid() {
      return this.validateDirName(this.newDirName);
    },
    isShareContext() {
      return getters.isShare();
    },
  },
  mounted() {
    // Capture where the operation started before any asynchronous work. A
    // result is only allowed to refresh the page if this lifecycle is still
    // current (A -> B -> A is a different lifecycle even for the same path).
    this.operationContext = this.captureOperationContext();
    this.promptId = state.prompts?.[state.prompts.length - 1]?.id ?? null;
    // If props.items is provided, use it. Otherwise use state
    if (this.items && this.items.length > 0) {
      this.localItems = this.items.map(item => ({
        from: item.path,
        fromSource: item.source,
        name: item.name,
      }));
    } else if (state.isSearchActive) {
      // Add null checks to prevent undefined values
      if (state.selected[0]?.path) {
        this.localItems = [
          {
            from: state.selected[0].path,
            fromSource: state.selected[0].source,
            name: state.selected[0].name,
          },
        ];
      }
    } else {
      if (state.selected && state.req?.items) {
        for (const item of state.selected) {
          const reqItem = state.req.items.at(item);
          if (reqItem?.path) {
            this.localItems.push({
              from: reqItem.path,
              fromSource: state.req.source,
              name: reqItem.name,
            });
          }
        }
      }
    }
    if (this.operationInProgress) {
      this.isLoading = true;
    }
  },
  methods: {
    syncFileListFromPicker() {
      this.$nextTick(() => {
        if (this.$refs.fileList && this.destSource && this.destPath) {
          this.$refs.fileList.jumpTo(this.destSource, this.destPath);
        }
      });
    },
    createNewDir() {
      this.showNewDirInput = true;
      this.newDirName = "";
      // Focus the new dir input automatically
      this.$nextTick(() => {
        this.$refs.newDirInput.focus();
      });
    },
    validateDirName(value) {
      // Check if a folder with the same name already exists in current directory
      if (this.$refs.fileList?.items) {
        const currentItems = this.$refs.fileList.items.filter(item => item.name !== '..');
        return !currentItems.some(item => item.name.toLowerCase() === value.toLowerCase());
      }
      return true;
    },
    cancelNewDir() {
      // Clicking cancel will return the buttons to their normal state
      this.showNewDirInput = false;
      this.newDirName = "";
    },
    handleEnter(event) {
      // Trigger create if you press enter instead of move/copy
      event.stopPropagation();
      event.preventDefault();
      if (this.newDirName && this.isDirNameValid) {
        void this.createDirectory();
      }
    },
    async createDirectory() {
      if (!this.newDirName || !this.isDirNameValid) return;
      try {
        this.isLoading = true;
        // Get current navigation from FileList
        const currentPath = this.$refs.fileList.path;
        const currentSource = this.$refs.fileList.source;
        const fullPath = currentPath.endsWith('/') ? `${currentPath + this.newDirName}/` : `${currentPath}/${this.newDirName}/`;
        if (getters.isShare()) {
          await resourcesApi.postPublic(state.shareInfo?.hash, fullPath, "", false, undefined, {}, true);
        } else {
          await resourcesApi.post(currentSource, fullPath, "", false, undefined, {}, true);
        }
        // Refresh the file list while keeping the current navigation that we did in the prompt
        if (getters.isShare()) {
          await resourcesApi.fetchFilesPublic(currentPath, state.shareInfo.hash)
            .then((req) => this.$refs.fileList.fillOptions(req, true));
        } else {
          await resourcesApi.fetchFiles(currentSource, currentPath)
            .then((req) => this.$refs.fileList.fillOptions(req, true));
        }
        // Only the source listing that actually shows this new folder needs a
        // refresh. Creating in an unrelated destination must not reload (and
        // lose the scroll position of) the user's current directory.
        this.reloadListingAfterCreate(currentSource, currentPath);
        // Clicking create will also return the buttons to their normal state
        this.showNewDirInput = false;
        this.newDirName = "";
      } catch (error) {
        console.error('Error creating directory:', error);
      } finally {
        this.isLoading = false;
      }
    },
    /** Refresh the main listing only when the created folder belongs to it. */
    reloadListingAfterCreate(createdSource, createdPath) {
      const context = this.operationContext;
      if (!context?.isListingView) return;
      if (!context.lifecycleId || !isListingLifecycle(context.lifecycleId)) return;
      if (state.req?.type !== "directory" || state.isSearchActive) return;
      if ((state.req.source || "") !== (context.source || "")) return;
      if (normalizeListingPath(state.req.path) !== normalizeListingPath(context.path)) return;
      if ((createdSource || "") !== (context.source || "")) return;
      if (normalizeListingPath(createdPath) !== normalizeListingPath(context.path)) return;
      captureListingAnchor({
        reason: "refresh",
        source: context.source,
        path: context.path,
        shareHash: context.shareHash,
        removed: [],
        operationRef: null,
        lifecycleId: context.lifecycleId,
      });
      mutations.setReload(true);
    },
    updateDestination(pathOrData) {
      // Handle both old format (just path) and new format (object with path and source)
      if (typeof pathOrData === 'string') {
        this.destPath = pathOrData;
        // For backward compatibility, keep the current source
        // This will be updated when FileList is modified to emit both
      } else if (pathOrData?.path) {
        this.destPath = pathOrData.path;
        // Update destSource from FileList's selection
        this.destSource = pathOrData.source;
      }
    },
    performOperation: async function (event) {
      event.preventDefault();
      if (this.isLoading || this.submitting) {
        return;
      }
      this.submitting = true;
      this.isLoading = true; // Show loading spinner
      const operationContext = this.resolveSubmissionContext();
      this.operationContext = operationContext;
      const submissionToken = {};
      const diagSelectionBefore = state.selected.length;
      let operationRef = null;
      let requestStartedAt = 0;
      try {
        // Ensure destination source is set
        if (!this.destSource) {
          // If no destination source yet, use the source from the first item (or current source)
          this.destSource = this.localItems[0]?.fromSource || state.req.source;
        }
        // Build items with destination paths
        const itemsToProcess = this.localItems.map(item => {
          // Ensure proper path construction without double slashes
          const destPath = this.destPath.endsWith('/') ? this.destPath : `${this.destPath}/`;
          return {
            ...item,
            to: destPath + item.name,
            toSource: this.destSource,
          };
        });
        // Define the action function
        const action = async (overwrite, rename) => {
          buttons.loading(this.operation);
          operationRef = this.nextOperationRef();
          requestStartedAt = performance.now();
          let result;
          if (getters.isShare()) {
            result = await resourcesApi.moveCopyPublic(state.shareInfo.hash, itemsToProcess, this.operation, overwrite, rename);
          } else {
            result = await resourcesApi.moveCopy(itemsToProcess, this.operation, overwrite, rename);
          }
          return result; // Return the result to check for failures
        };
        let conflict = false;
        let dstResp = null;
        if (getters.isShare()) {
          dstResp = await resourcesApi.fetchFilesPublic(this.destPath, state.shareInfo?.hash);
        } else {
          dstResp = await resourcesApi.fetchFiles(this.destSource, this.destPath);
        }
        conflict = checkConflict(itemsToProcess, dstResp.items);
        let overwrite = false;
        let rename = false;
        let result = null;

        if (conflict) {
          this.isLoading = false;
          // Check if any item is being copied/moved to itself
          const isSameFile = itemsToProcess.some(item => {
            const destPath = this.destPath.endsWith('/') ? this.destPath : `${this.destPath}/`;
            const targetPath = destPath + item.name;
            return item.from === targetPath && item.fromSource === this.destSource;
          });

          await new Promise((resolve, reject) => {
            const existingPrompts = new Set((state.prompts || []).map((prompt) => prompt.id));
            mutations.showPrompt({
              name: "replace-rename",
              pinned: true,
              props: {
                isSameFile: isSameFile,
                operation: this.operation
              },
              confirm: async (event, option) => {
                overwrite = option === "overwrite";
                rename = option === "rename";
                event.preventDefault();
                try {
                  this.isLoading = true;
                  result = await action(overwrite, rename);
                  resolve(); // Resolve the promise if action succeeds
                } catch (e) {
                  reject(e); // Reject the promise if an error occurs
                } finally {
                  this.isLoading = false;
                }
              },
            });
            const createdPrompt = (state.prompts || []).find((prompt) => !existingPrompts.has(prompt.id));
            if (createdPrompt) this.ownedPromptIds.push(createdPrompt.id);
          });
        } else {
          // Await the action call for non-conflicting cases
          result = await action(overwrite, rename);
        }

        const adapted = this.adaptResult(result);
        this.applyOperationOutcome(adapted, {
          operationContext,
          operationRef,
          requestStartedAt,
          selectionBefore: diagSelectionBefore,
          submissionToken,
          fromError: false,
        });
      } catch (error) {
        // Handle errors thrown by the API (e.g., 500 errors)
        // DON'T close the prompt on error - let user try again or cancel manually

        // Try to extract error message from the error response
        let errorMessage = null;

        // Check if error has a response body with failed items
        if (error?.failed && error.failed.length > 0 && error.failed[0]?.message) {
          errorMessage = error.failed[0].message;
        } else if (error?.message) {
          errorMessage = error.message;
        } else if (typeof error === 'string') {
          errorMessage = error;
        }

        // Only use fallback if we couldn't extract a message
        if (!errorMessage) {
          errorMessage = this.$t("prompts.operationFailed");
        }

        // A thrown request may still carry confirmed succeeded/failed arrays.
        // Never report the whole batch as failed without checking them.
        this.applyOperationOutcome(this.adaptErrorResult(error), {
          operationContext,
          operationRef,
          requestStartedAt,
          selectionBefore: diagSelectionBefore,
          submissionToken,
          fromError: true,
        });
        notify.showError(errorMessage);
        notifyOperationError(errorMessage);
      } finally {
        this.isLoading = false; // Hide loading spinner
        this.submitting = false;
      }
    },
    /**
     * Re-check the view captured at mount against the real submit. A same-view
     * reload (e.g. create-folder) keeps the lifecycle id and is allowed; a
     * navigation/A->B->A keeps the stale captured context and is rejected.
     */
    resolveSubmissionContext() {
      const captured = this.operationContext || this.captureOperationContext();
      const current = this.captureOperationContext();
      const sameView = Boolean(captured.lifecycleId) &&
        captured.lifecycleId === current.lifecycleId &&
        (captured.source || "") === (current.source || "") &&
        (captured.path || "/") === (current.path || "/") &&
        (captured.shareHash || "") === (current.shareHash || "");
      return sameView ? current : captured;
    },
    /** Business identity of the view that started this operation. */
    captureOperationContext() {
      const recorder = getDiagnosticsRuntime()?.recorder || null;
      const source = state.req?.source || "";
      const path = state.req?.path || "/";
      const active = getActiveLoad();
      const loadContext = active && active.meta?.source === source && active.meta?.path === path
        ? active.context
        : null;
      return {
        source,
        path,
        shareHash: state.shareInfo?.hash || "",
        lifecycleId: currentListingLifecycleId(),
        isListingView: this.isActualListingView(),
        diagnostics: recorder
          ? { generation: recorder.getGeneration?.(), loadRef: loadContext?.loadRef || null }
          : null,
      };
    },
    isActualListingView() {
      // getters.isListing() also matches a preview under /files; require real
      // directory data and a listing view, and never a search result overlay.
      return state.req?.type === "directory" &&
        getters.currentView() === "listingView" &&
        !state.isSearchActive;
    },
    nextOperationRef() {
      const recorder = getDiagnosticsRuntime()?.recorder;
      return recorder?.nextOperationRef?.() || null;
    },
    diagnosticContextFor(operationRef) {
      const captured = this.operationContext?.diagnostics;
      if (!captured) return null;
      return {
        generation: captured.generation,
        loadRef: captured.loadRef || null,
        operationRef: operationRef || null,
      };
    },
    adaptResult(response) {
      return adaptMoveCopyResult({
        requested: this.localItems.map((item) => ({ source: item.fromSource, path: item.from })),
        response,
        fallbackSource: this.operationContext?.source || state.req?.source || "",
        isShare: getters.isShare(),
      });
    },
    adaptErrorResult(error) {
      const hasArrays = error && (Object.hasOwn(error, "succeeded") || Object.hasOwn(error, "failed"));
      return this.adaptResult(hasArrays ? { succeeded: error.succeeded, failed: error.failed } : null);
    },
    /** Is this result still allowed to touch the view that started the operation? */
    operationContextIsCurrent(operationContext) {
      if (!operationContext) return false;
      if (operationContext.lifecycleId && !isListingLifecycle(operationContext.lifecycleId)) return false;
      if ((state.req?.source || "") !== (operationContext.source || "")) return false;
      if ((state.req?.path ?? "/") !== (operationContext.path ?? "/")) return false;
      if ((state.shareInfo?.hash || "") !== (operationContext.shareHash || "")) return false;
      return true;
    },
    /** Close only prompts owned by this component and its children. */
    closeOperationPrompts() {
      const roots = new Set([this.promptId, ...(this.ownedPromptIds || [])].filter(Boolean));
      if (roots.size === 0) return;
      const owned = [];
      for (const prompt of state.prompts || []) {
        let cursor = prompt;
        let guard = 0;
        while (cursor && guard < 20) {
          if (roots.has(cursor.id)) {
            owned.push(prompt.id);
            break;
          }
          cursor = (state.prompts || []).find((entry) => entry.id === cursor.parentId);
          guard += 1;
        }
      }
      for (const id of owned.reverse()) {
        if ((state.prompts || []).some((prompt) => prompt.id === id)) mutations.closeTopPrompt(id);
      }
    },
    /** One terminal decision per submission: anchor, log, notify, reload. */
    applyOperationOutcome(adapted, {
      operationContext,
      operationRef,
      requestStartedAt,
      selectionBefore,
      submissionToken,
      fromError,
    }) {
      const succeeded = adapted.confirmedSucceededCount;
      const failed = adapted.confirmedFailedCount;
      const outcome = adapted.outcome;
      const anySuccess = succeeded > 0;
      const contextCurrent = this.operationContextIsCurrent(operationContext);
      const previewMove = this.operation === "move" && getters.isPreviewView();
      const previewSource = state.req?.source || "";
      const previewPath = state.req?.path || "";
      const previewRequested = previewMove && this.localItems.some((item) =>
        item.from === previewPath && (item.fromSource || "") === previewSource
      );
      const currentPreviewMoved = previewRequested && adapted.confirmedSucceeded.some((entry) =>
        entry.source === previewSource && entry.path === previewPath
      );

      if (previewMove) {
        if (currentPreviewMoved && contextCurrent) {
          this.logOperationResult(adapted, {
            operationRef, requestStartedAt, selectionBefore, refreshed: false, submissionToken,
          });
          eventBus.emit("itemsMoved");
          this.closeOperationPrompts();
          notify.showSuccessToast(this.$t("prompts.moveSuccess"));
          notifyMoveCopyComplete("move", succeeded);
          const next = state.navigation.nextItem;
          const prev = state.navigation.previousItem;
          if (next) {
            url.goToItem(next.source, next.path, undefined, false, getters.isShare());
          } else if (prev) {
            url.goToItem(prev.source, prev.path, undefined, false, getters.isShare());
          } else {
            url.goToItem(state.req.source, url.removeLastDir(state.req.path), {}, false, getters.isShare());
          }
          return;
        }

        this.logOperationResult(adapted, {
          operationRef, requestStartedAt, selectionBefore, refreshed: false, submissionToken,
        });
        // A stale request may still have moved files, but it must not close a
        // prompt, navigate or refresh the page the user is looking at now.
        if (!contextCurrent) return;
        if (anySuccess) {
          // Other files in the batch moved while the previewed file did not.
          // Keep the current preview; only update listing/tree consumers.
          eventBus.emit("itemsMoved");
          this.closeOperationPrompts();
          if (!fromError) {
            notify.showError(this.$t("prompts.partialSuccess", { succeeded, failed }));
          }
          return;
        }
        if (!fromError) {
          const message = adapted.failureMessage || this.$t("prompts.operationFailed");
          notify.showError(message);
          notifyOperationError(message);
        }
        return;
      }

      const listingOperation = Boolean(operationContext?.isListingView);
      const listingMatches = listingOperation && contextCurrent &&
        state.req?.type === "directory" &&
        state.req?.source === operationContext.source &&
        state.req?.path === operationContext.path &&
        !state.isSearchActive;
      // A same-view listing refresh is allowed; leaving that lifecycle
      // (A -> B, or F4 during a slow request) must not reload B.
      const canReload = contextCurrent && (!listingOperation || listingMatches);
      const willRefresh = anySuccess && canReload;
      if (willRefresh && this.operation === "move" && listingMatches && this._anchoredSubmission !== submissionToken) {
        // Capture the current viewport before anything can clear the listing.
        this.rememberMoveAnchor(adapted, operationContext, operationRef);
        this._anchoredSubmission = submissionToken;
      }
      this.logOperationResult(adapted, {
        operationRef,
        requestStartedAt,
        selectionBefore,
        // setReload(true) is a request, not a completed refresh.
        refreshed: willRefresh ? null : false,
        submissionToken,
      });

      if (!contextCurrent) {
        if (anySuccess) eventBus.emit("itemsMoved");
        return;
      }

      if (!anySuccess) {
        if (!fromError) {
          const message = adapted.failureMessage || this.$t("prompts.operationFailed");
          notify.showError(message);
          notifyOperationError(message);
        }
        return;
      }

      if (!fromError) {
        if (outcome === "partial") {
          notify.showError(this.$t("prompts.partialSuccess", { succeeded, failed }));
        } else if (outcome === "unknown") {
          // Confirmed successes exist, but the rest cannot be verified.
          notify.showError(adapted.failureMessage || this.$t("prompts.operationFailed"));
        }
        if (outcome === "ok" || outcome === "partial") {
          const destSource = this.destSource;
          const destPath = this.destPath;
          const buttonProps = {
            icon: "folder",
            buttons: destPath
              ? [
                  goToItemNotificationButton(
                    this.$t("buttons.goToItem"),
                    destSource || state.shareInfo?.hash,
                    destPath,
                    getters.isShare()
                  ),
                ]
              : undefined,
          };
          if (this.operation === "move") {
            notify.showSuccess(this.$t("prompts.moveSuccess"), buttonProps);
          } else {
            notify.showSuccess(this.$t("prompts.copySuccess"), buttonProps);
          }
          notifyMoveCopyComplete(this.operation, succeeded);
        }
      }

      this.closeOperationPrompts();
      if (willRefresh) {
        mutations.setSearch(false);
        mutations.setReload(true);
      }
    },
    rememberMoveAnchor(adapted, operationContext, operationRef) {
      if (this.operation !== "move") return null;
      const affected = adapted.confirmedSucceeded.filter((entry) =>
        isEntryInDirectory(entry, operationContext.source, operationContext.path)
      );
      if (affected.length === 0) return null;
      return captureListingAnchor({
        reason: "move",
        source: operationContext.source,
        path: operationContext.path,
        shareHash: operationContext.shareHash,
        removed: affected,
        operationRef,
        lifecycleId: operationContext.lifecycleId,
      });
    },
    logOperationResult(adapted, {
      operationRef, requestStartedAt, selectionBefore, refreshed, submissionToken,
    }) {
      if (this._loggedSubmission === submissionToken) return;
      this._loggedSubmission = submissionToken;
      const started = Number.isFinite(requestStartedAt) && requestStartedAt > 0
        ? requestStartedAt
        : performance.now();
      diagnosticsOperationResult({
        operation: this.operation,
        requested: this.localItems.length,
        succeeded: adapted.confirmedSucceededCount,
        failed: adapted.confirmedFailedCount,
        elapsed_ms: Math.max(0, performance.now() - started),
        refreshed,
        selection_before: selectionBefore,
        selection_after: state.selected.length,
        outcome: adapted.outcome,
      }, this.diagnosticContextFor(operationRef));
    },
  },
};
</script>

<style scoped>
.loading-content {
  text-align: center;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 16px;
  padding-top: 2em;
}

.loading-text {
  padding: 1em;
  margin: 0;
  font-size: 1em;
  font-weight: 500;
}

/* Make card-content position relative for absolute positioning of overlay */
.card-content {
  position: relative;
}

.new-dir-input {
  justify-self: left
}

.split-buttons {
  justify-content: space-between;
}

.move-copy-path-picker {
  margin-bottom: 1rem;
}
</style>