import { beforeEach, describe, expect, it, vi } from "vitest";

const h = vi.hoisted(() => ({
  state: {
    req: null,
    selected: [],
    navigation: {},
    isSearchActive: false,
    prompts: [],
    shareInfo: {},
  },
  getters: {
    isPreviewView: () => false,
    isShare: () => false,
    currentView: () => "listingView",
    canCreateInSource: () => true,
  },
  effects: {
    navigate: [],
    reload: [],
    search: [],
    closeTop: [],
    error: [],
    success: [],
    completed: [],
    emit: [],
    log: [],
    anchor: [],
    lifecycleCurrent: true,
  },
  mutations: {
    closeTopPrompt: (...args) => h.effects.closeTop.push(args),
    setSearch: (value) => h.effects.search.push(value),
    setReload: (value) => h.effects.reload.push(value),
  },
  notify: {
    showError: (...args) => h.effects.error.push(args),
    showSuccess: (...args) => h.effects.success.push(args),
    showSuccessToast: (...args) => h.effects.success.push(args),
  },
}));

vi.mock("@/store", () => ({ state: h.state, getters: h.getters, mutations: h.mutations }));
vi.mock("@/components/files/FileList.vue", () => ({ default: { name: "FileList", render: () => null } }));
vi.mock("@/components/files/PathPickerButton.vue", () => ({ default: { name: "PathPickerButton", render: () => null } }));
vi.mock("@/components/LoadingSpinner.vue", () => ({ default: { name: "LoadingSpinner", render: () => null } }));
vi.mock("@/api", () => ({ resourcesApi: {} }));
vi.mock("@/notify", () => ({ notify: h.notify }));
vi.mock("@/utils/buttons", () => ({ default: { loading: () => {} } }));
vi.mock("@/utils/upload", () => ({ checkConflict: () => false }));
vi.mock("@/utils", () => ({ url: { goToItem: (...args) => h.effects.navigate.push(args), removeLastDir: () => "/" } }));
vi.mock("@/utils/appNotifications", async (importOriginal) => ({
  ...(await importOriginal()),
  notifyMoveCopyComplete: (...args) => h.effects.completed.push(args),
  notifyOperationError: () => {},
}));
vi.mock("@/utils/notificationActions", () => ({ goToItemNotificationButton: () => ({}) }));
vi.mock("@/store/eventBus", () => ({ eventBus: { emit: (...args) => h.effects.emit.push(args) } }));
vi.mock("@/utils/listingViewport", () => ({ captureListingAnchor: (...args) => h.effects.anchor.push(args) }));
vi.mock("@/utils/listAnchor", () => ({ isEntryInDirectory: () => true }));
vi.mock("@/utils/listingLifecycle", () => ({
  currentListingLifecycleId: () => 5,
  isListingLifecycle: () => h.effects.lifecycleCurrent,
}));
vi.mock("@/utils/moveResults", () => ({ adaptMoveCopyResult: () => ({}) }));
vi.mock("@/utils/diagnostics/telemetry", () => ({ diagnosticsOperationResult: () => {} }));
vi.mock("@/utils/diagnostics/runtime", () => ({ getDiagnosticsRuntime: () => null }));
vi.mock("@/utils/diagnostics/listingBridge", () => ({ getActiveLoad: () => null }));

const { default: MoveCopy } = await import("@/components/prompts/MoveCopy.vue");

function reset() {
  h.state.req = { type: "file", source: "pictures", path: "/a.jpg", items: [] };
  h.state.selected = [];
  h.state.navigation = { nextItem: { source: "pictures", path: "/next.jpg" } };
  h.state.isSearchActive = false;
  h.state.prompts = [];
  h.state.shareInfo = {};
  h.getters.isPreviewView = () => true;
  h.getters.isShare = () => false;
  h.getters.currentView = () => "preview";
  h.effects.navigate = [];
  h.effects.reload = [];
  h.effects.search = [];
  h.effects.closeTop = [];
  h.effects.error = [];
  h.effects.success = [];
  h.effects.completed = [];
  h.effects.emit = [];
  h.effects.log = [];
  h.effects.anchor = [];
  h.effects.lifecycleCurrent = true;
}

function instance(overrides = {}) {
  return {
    operation: "move",
    localItems: [{ from: "/a.jpg", fromSource: "pictures", name: "a.jpg" }],
    destSource: "pictures",
    destPath: "/Dest/",
    promptId: 7,
    $t: (value) => value,
    operationContextIsCurrent: MoveCopy.methods.operationContextIsCurrent,
    logOperationResult: (...args) => h.effects.log.push(args),
    rememberMoveAnchor: (...args) => h.effects.anchor.push(args),
    closeOperationPrompts: () => h.effects.closeTop.push(["own"]),
    ...overrides,
  };
}

const adapted = (overrides = {}) => ({
  outcome: "failed",
  confirmedSucceeded: [],
  confirmedFailed: [],
  confirmedSucceededCount: 0,
  confirmedFailedCount: 0,
  failureMessage: "",
  ...overrides,
});

const context = (overrides = {}) => ({
  source: "pictures",
  path: "/a.jpg",
  shareHash: "",
  lifecycleId: 5,
  isListingView: false,
  ...overrides,
});

const args = (overrides = {}) => ({
  operationContext: context(),
  operationRef: "o1",
  requestStartedAt: 1,
  selectionBefore: 1,
  submissionToken: {},
  fromError: false,
  ...overrides,
});

describe("MoveCopy preview outcome", () => {
  beforeEach(reset);

  it("keeps the preview and the dialog when the current file failed", () => {
    const cmp = instance();
    MoveCopy.methods.applyOperationOutcome.call(cmp, adapted({
      outcome: "failed",
      confirmedFailed: [{ source: "pictures", path: "/a.jpg", message: "denied" }],
      confirmedFailedCount: 1,
    }), args());
    expect(h.effects.navigate).toEqual([]);
    expect(h.effects.closeTop).toEqual([]);
    expect(h.effects.reload).toEqual([]);
    expect(h.effects.emit).toEqual([]);
  });

  it("keeps the preview and the dialog when the network result is unknown", () => {
    const cmp = instance();
    MoveCopy.methods.applyOperationOutcome.call(cmp, adapted({ outcome: "unknown" }), args({ fromError: true }));
    expect(h.effects.navigate).toEqual([]);
    expect(h.effects.closeTop).toEqual([]);
    expect(h.effects.reload).toEqual([]);
    expect(h.effects.emit).toEqual([]);
  });

  it("does not navigate when another file moved but the preview file failed", () => {
    const cmp = instance();
    MoveCopy.methods.applyOperationOutcome.call(cmp, adapted({
      outcome: "partial",
      confirmedSucceeded: [{ source: "pictures", path: "/b.jpg" }],
      confirmedSucceededCount: 1,
      confirmedFailed: [{ source: "pictures", path: "/a.jpg", message: "denied" }],
      confirmedFailedCount: 1,
    }), args());
    expect(h.effects.navigate).toEqual([]);
    expect(h.effects.reload).toEqual([]);
    expect(h.effects.emit).toEqual([["itemsMoved"]]);
  });

  it("navigates when the current preview file was confirmed moved despite other failures", () => {
    const cmp = instance();
    MoveCopy.methods.applyOperationOutcome.call(cmp, adapted({
      outcome: "partial",
      confirmedSucceeded: [{ source: "pictures", path: "/a.jpg" }],
      confirmedSucceededCount: 1,
      confirmedFailed: [{ source: "pictures", path: "/b.jpg", message: "denied" }],
      confirmedFailedCount: 1,
    }), args());
    expect(h.effects.navigate).toHaveLength(1);
    expect(h.effects.navigate[0][1]).toBe("/next.jpg");
    expect(h.effects.closeTop).toEqual([["own"]]);
  });

  it("navigates after a fully confirmed move", () => {
    const cmp = instance();
    MoveCopy.methods.applyOperationOutcome.call(cmp, adapted({
      outcome: "ok",
      confirmedSucceeded: [{ source: "pictures", path: "/a.jpg" }],
      confirmedSucceededCount: 1,
    }), args());
    expect(h.effects.navigate).toHaveLength(1);
    expect(h.effects.success).toHaveLength(1);
  });

  it("ignores a slow result after the preview lifecycle changed", () => {
    const cmp = instance();
    h.effects.lifecycleCurrent = false;
    h.state.req = { type: "file", source: "pictures", path: "/next.jpg", items: [] };
    MoveCopy.methods.applyOperationOutcome.call(cmp, adapted({
      outcome: "ok",
      confirmedSucceeded: [{ source: "pictures", path: "/a.jpg" }],
      confirmedSucceededCount: 1,
    }), args());
    expect(h.effects.navigate).toEqual([]);
    expect(h.effects.closeTop).toEqual([]);
    expect(h.effects.reload).toEqual([]);
  });
});

describe("MoveCopy prompt ownership", () => {
  beforeEach(() => {
    reset();
    h.state.prompts = [
      { id: 1, name: "move", parentId: null },
      { id: 2, name: "replace-rename", parentId: null, pinned: true },
      { id: 3, name: "download", parentId: null },
    ];
  });

  it("closes the owned conflict chooser and dialog but not an unrelated prompt", () => {
    MoveCopy.methods.closeOperationPrompts.call({
      promptId: 1,
      ownedPromptIds: [2],
    });
    expect(h.effects.closeTop).toEqual([[2], [1]]);
  });

  it("does nothing when a newer page no longer has the owned prompts", () => {
    h.state.prompts = [{ id: 3, name: "download", parentId: null }];
    MoveCopy.methods.closeOperationPrompts.call({
      promptId: 1,
      ownedPromptIds: [2],
    });
    expect(h.effects.closeTop).toEqual([]);
  });
});

describe("MoveCopy listing outcome", () => {
  beforeEach(() => {
    reset();
    h.state.req = { type: "directory", source: "pictures", path: "/", items: [] };
    h.state.selected = [];
    h.getters.isPreviewView = () => false;
    h.getters.currentView = () => "listingView";
  });

  it("refreshes the same listing after a confirmed move", () => {
    MoveCopy.methods.applyOperationOutcome.call(instance(), adapted({
      outcome: "ok",
      confirmedSucceeded: [{ source: "pictures", path: "/a.jpg" }],
      confirmedSucceededCount: 1,
    }), args({ operationContext: context({ path: "/", isListingView: true }) }));
    expect(h.effects.reload).toEqual([true]);
    expect(h.effects.closeTop).toEqual([["own"]]);
  });

  it("does not refresh or close a prompt after navigation to another directory", () => {
    h.effects.lifecycleCurrent = false;
    MoveCopy.methods.applyOperationOutcome.call(instance(), adapted({
      outcome: "ok",
      confirmedSucceeded: [{ source: "pictures", path: "/a.jpg" }],
      confirmedSucceededCount: 1,
    }), args({ operationContext: context({ path: "/", isListingView: true }) }));
    expect(h.effects.reload).toEqual([]);
    expect(h.effects.closeTop).toEqual([]);
  });
});
