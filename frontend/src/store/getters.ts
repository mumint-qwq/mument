// Modified from FileBrowser Quantum for the mument derivative. See MODIFICATIONS.md.
import { detectLocale } from '@/i18n';
import { mutations } from './mutations';
import { state } from './state';
import { url } from '@/utils';
import { globalVars, previewViews, tools } from '@/utils/constants';
import { getFileExtension } from '@/utils/files.js';
import { getTypeInfo, isHtmlMimeType, isRichTextPreviewMimeType } from '@/utils/mimetype';
import { fromNow } from '@/utils/moment';
import { getObjectProperty } from '@/utils/object.js';
import { buildItemUrl, extractSourceFromPath, getParentDir, removeLeadingSlash, removePrefix } from '@/utils/url.js';
import { defaultDarkMode } from '@/utils/theme';
import { isMobileLayout } from '@/utils/viewport.js';
import {
  folderPreferenceTarget,
  hasRawPreferenceField,
  normalizeSorting,
  readFolderPreference,
  resolveEffectivePreferences,
} from '@/utils/folderDisplayPreferences.js';
import type { DisplayPreference, FileListItem, FolderDisplayContext } from './types';

function decodeOnce(value: string): string | null {
  try {
    return decodeURIComponent(value);
  } catch {
    return null;
  }
}

function comparablePath(path: unknown): string {
  return String(path ?? '').replace(/^\/+|\/+$/g, '');
}

function parseShareRoute(routePath: string) {
  const prefix = '/public/share/';
  if (!routePath.startsWith(prefix)) {
    return null;
  }
  const rest = routePath.slice(prefix.length);
  const slash = rest.indexOf('/');
  const hashPart = slash === -1 ? rest : rest.slice(0, slash);
  const subPathPart = slash === -1 ? '' : rest.slice(slash + 1);
  if (hashPart === '') {
    return null;
  }
  const hash = decodeOnce(hashPart);
  const subPath = decodeOnce(subPathPart);
  if (hash === null || subPath === null) {
    return null;
  }
  return { isShare: true, source: hash, path: subPath === '' ? '/' : `/${subPath}` };
}

function parseFilesRoute(routePath: string) {
  const extracted = extractSourceFromPath(routePath, Object.keys(state.sources?.info || {}));
  const source = decodeOnce(extracted.source);
  const path = decodeOnce(extracted.path || '/');
  if (source === null || path === null) {
    return null;
  }
  return { isShare: false, source, path: path || '/' };
}

/**
 * One folder identity shared by reads and writes.
 *
 * The key still looks like the historical route path, but it is derived from
 * the loaded request (source + decoded path) and verified against the current
 * route. A click made while the router is between directories, or while
 * loading still shows the previous request, therefore cannot be written to the
 * old folder. Preview routes resolve to the parent folder for reads, and
 * isDirectory=false prevents any write there.
 */
function computeFolderDisplayContext(): FolderDisplayContext | null {
  const user = state.user;
  if (!user?.username || state.navigation?.isTransitioning) {
    return null;
  }
  const routePath = state.route?.path || '';
  const isFilesRoute = routePath === '/files' || routePath.startsWith('/files/');
  const identity = routePath.startsWith('/public/share/')
    ? parseShareRoute(routePath)
    : isFilesRoute
      ? parseFilesRoute(routePath)
      : null;
  if (!identity || (!identity.isShare && identity.source === '')) {
    return null;
  }
  const req = state.req;
  if (!req || typeof req !== 'object' || typeof req.type !== 'string') {
    return null;
  }
  const reqPath = typeof req.path === 'string' ? req.path : null;
  if (reqPath === null) {
    return null;
  }
  if (comparablePath(identity.path) !== comparablePath(reqPath)) {
    return null;
  }
  const isDirectory = req.type === 'directory';
  let source;
  if (identity.isShare) {
    source = state.shareInfo?.hash || identity.source;
  } else {
    const reqSource = typeof req.source === 'string' && req.source !== ''
      ? req.source
      : (state.sources?.current || identity.source);
    if (reqSource && identity.source && reqSource !== identity.source) {
      return null;
    }
    source = reqSource;
  }
  const folderPath = isDirectory ? reqPath : getParentDir(reqPath);
  const target = identity.isShare
    ? folderPreferenceTarget({ isShare: true, shareHash: source, path: folderPath })
    : folderPreferenceTarget({ source, path: folderPath });
  if (!target) {
    return null;
  }
  return {
    ...target,
    path: folderPath,
    isDirectory,
    isShare: identity.isShare,
    canPersist: user.username !== 'anonymous',
  };
}

let folderContextCacheKey = '';
let folderContextCache: FolderDisplayContext | null = null;

export const getters = {
  displayPreferenceFor: (source: string, path: string): DisplayPreference | null => {
    if (!source || !path) {
      return null;
    }
    const found = readFolderPreference(state.displayPreferences, source, [path, `${path}/`]);
    return found ? (found.preference as DisplayPreference) : null;
  },
  /** The verified, route/request-consistent folder identity (or null). */
  folderDisplayContext: () => {
    const key = [
      state.route?.path || '',
      state.req?.type || '',
      state.req?.path || '',
      state.req?.source || '',
      state.sources?.current || '',
      Object.keys(state.sources?.info || {}).sort().join(','),
      state.shareInfo?.hash || '',
      state.user?.username || '',
      state.navigation?.isTransitioning ? '1' : '0',
    ].join('\u0000');
    if (key === folderContextCacheKey) {
      return folderContextCache;
    }
    folderContextCacheKey = key;
    folderContextCache = computeFolderDisplayContext();
    return folderContextCache;
  },
  /** Raw folder record (including invalid legacy fields) for reset availability. */
  folderPreferenceRecord: () => {
    const context = getters.folderDisplayContext();
    if (!context) {
      return null;
    }
    const found = readFolderPreference(state.displayPreferences, context.sourceKey, context.candidateKeys);
    if (!found) {
      return null;
    }
    return {
      ...found,
      sourceKey: context.sourceKey,
      isDirectory: context.isDirectory,
    };
  },
  hasFolderViewPreference: () => {
    const record = getters.folderPreferenceRecord();
    return Boolean(record && record.isDirectory && hasRawPreferenceField(record.rawPreference, 'viewMode'));
  },
  hasFolderSortingPreference: () => {
    const record = getters.folderPreferenceRecord();
    return Boolean(record && record.isDirectory && hasRawPreferenceField(record.rawPreference, 'sorting'));
  },
  /** Page-local view/sorting for result lists that are not a real folder. */
  listingContextOverride: () => {
    if (getters.folderDisplayContext()) {
      return null;
    }
    const override = state.listingContextOverride;
    if (override?.username !== state.user?.username) {
      return null;
    }
    if (override.key !== (state.route?.path || '')) {
      return null;
    }
    if (!override.viewMode && !override.sorting) {
      return null;
    }
    return {
      viewMode: override.viewMode,
      sorting: override.sorting,
    };
  },
  eventTheme: () => {
    if (getters.isShare()) {
      return "";
    }
    if (!globalVars.eventBasedThemes) {
      return ""
    }
    if (state.disableEventThemes) {
      return ""
    }
    // if date is halloween october 31st, return halloween
    if (new Date().getMonth() === 9 && new Date().getDate() === 31) {
      return "halloween";
    }
    return "";
  },
  getTime: timestamp => {
    if (state.user?.dateFormat) {
      // Truncate the fractional seconds to 3 digits (milliseconds)
      const sanitizedString = timestamp.replace(/\.\d+/, match =>
        match.slice(0, 4)
      )
      // Parse the sanitized string into a Date object
      const date = new Date(sanitizedString)
      return date.toLocaleString()
    }
    return fromNow(timestamp, state.user?.locale)
  },
  isPreviewView: () => {
    const cv = getters.currentView()
    return previewViews.includes(cv)
  },
  isScrollable: () => {
    if (getters.currentView() === 'markdownViewer') {
      if (isHtmlMimeType(state.req?.type)) {
        return false;
      }
      return true;
    }
    if (getters.isPreviewView()) {
      return false
    }
    return true
  },
  /** Valid folder override for the current, verified folder (null when none). */
  displayPreference: () => {
    const context = getters.folderDisplayContext();
    if (!context) {
      return null;
    }
    const found = readFolderPreference(state.displayPreferences, context.sourceKey, context.candidateKeys);
    return found ? (found.preference as DisplayPreference) : null;
  },
  viewModeChangeLocked: () => {
    if (getters.isAdmin()) {
      return false;
    }
    if (state.enforcedUserDefaults?.listing?.viewMode) {
      return true;
    }
    if (getters.isShare()) {
      const shareView = String(state.shareInfo?.viewMode ?? "").trim();
      if (shareView !== "") {
        return true;
      }
    }
    return false;
  },
  viewMode: () => {
    if (!state.user || state.user?.username === "") {
      return "normal";
    }
    const context = getters.folderDisplayContext();
    const folder = context
      ? readFolderPreference(state.displayPreferences, context.sourceKey, context.candidateKeys)
      : null;
    const transient = getters.listingContextOverride();
    const resolved = resolveEffectivePreferences(state.user, folder?.preference, {
      isAdmin: getters.isAdmin(),
      isShare: getters.isShare(),
      shareViewMode: state.shareInfo?.viewMode,
      enforcedViewMode: Boolean(state.enforcedUserDefaults?.listing?.viewMode),
      transientViewMode: transient?.viewMode,
    });
    return resolved.viewMode;
  },
  sorting: () => {
    const context = getters.folderDisplayContext();
    const folder = context
      ? readFolderPreference(state.displayPreferences, context.sourceKey, context.candidateKeys)
      : null;
    const transient = getters.listingContextOverride();
    const resolved = resolveEffectivePreferences(state.user, folder?.preference, {
      isAdmin: getters.isAdmin(),
      isShare: getters.isShare(),
      shareViewMode: state.shareInfo?.viewMode,
      enforcedViewMode: Boolean(state.enforcedUserDefaults?.listing?.viewMode),
      transientViewMode: transient?.viewMode,
      transientSorting: transient?.sorting,
    });
    return resolved.sorting;
  },
  /** Account default sorting without any folder/tool context (popups such as FileList). */
  defaultSorting: () => {
    return normalizeSorting(state.user?.sorting) || { by: "name", asc: true };
  },
  /**
   * Effective sorting for a target folder whose request has not been loaded
   * yet (paged listing fetches before state.req switches). This must read the
   * same folder memory as the normal getter, otherwise the first paged request
   * would be ordered by the account default while the UI later sorts by the
   * folder choice.
   */
  sortingForTarget: (source: string, path: string) => {
    const isShare = getters.isShare();
    const target = folderPreferenceTarget({
      isShare,
      shareHash: state.shareInfo?.hash,
      source,
      path,
    });
    const folder = target
      ? readFolderPreference(state.displayPreferences, target.sourceKey, target.candidateKeys)
      : null;
    const resolved = resolveEffectivePreferences(state.user, folder?.preference, {
      isAdmin: getters.isAdmin(),
      isShare,
      shareViewMode: state.shareInfo?.viewMode,
      enforcedViewMode: Boolean(state.enforcedUserDefaults?.listing?.viewMode),
    });
    return resolved.sorting;
  },
  /**
   * Sorting for replaceRequest. During the first load of a folder the request
   * is not in state.req yet, so the normal folder context is still null. Use
   * the response's own source/path in that case; for a tool result list keep
   * the page-local choice.
   */
  sortingForRequest: (value?: { source?: string; path?: string; type?: string } | null) => {
    if (getters.folderDisplayContext()) {
      return getters.sorting();
    }
    const transient = getters.listingContextOverride();
    if (transient?.sorting) {
      return transient.sorting;
    }
    const target = value;
    if (
      target?.type === "directory"
      && typeof target.source === "string"
      && typeof target.path === "string"
    ) {
      return getters.sortingForTarget(target.source, target.path);
    }
    return getters.sorting();
  },
  previewType: () => getTypeInfo(state.req.type).simpleType,
  /** Audio/video preview uses folder listing nav links unless a multi-item playback queue is active. */
  isPreviewPlaybackQueueNavMode: () => {
    const previewType = getters.previewType();
    const isMediaView = previewType === 'audio' || previewType === 'video';
    const mode = state.playbackQueue.mode || 'single';
    const queueLength = state.playbackQueue.queue.length || 0;
    return (
      isMediaView &&
      mode !== 'single' &&
      queueLength > 1
    );
  },
  playbackQueueCanGoPrevious: () => {
    const queue = state.playbackQueue.queue;
    const currentIndex = state.playbackQueue.currentIndex ?? -1;
    const loop = state.playbackQueue.loop || 'off';
    if (queue.length <= 1 || currentIndex < 0) {
      return false;
    }
    if (currentIndex === 0 && loop !== 'all') {
      return false;
    }
    return true;
  },
  playbackQueueCanGoNext: () => {
    const queue = state.playbackQueue.queue;
    const currentIndex = state.playbackQueue.currentIndex ?? -1;
    const loop = state.playbackQueue.loop || 'off';
    if (queue.length <= 1 || currentIndex < 0) {
      return false;
    }
    if (currentIndex >= queue.length - 1 && loop !== 'all') {
      return false;
    }
    return true;
  },
  isCardView: () =>
    getters.viewMode() === 'mosaic' ||
    getters.viewMode() === 'gallery' ||
    getters.viewMode() === 'normal' ||
    getters.viewMode() === 'icons',
  currentHash: () => state.shareInfo?.hash,
  isMobile: () => isMobileLayout.value,
  isLoading: () => Object.keys(state.loading).length > 0,
  isSettings: () => getters.currentView() === 'settings',
  isDarkMode: () => {
    if (state.shareInfo?.enforceDarkLightMode === "dark") {
      return true
    }
    if (state.shareInfo?.enforceDarkLightMode === "light") {
      return false
    }
    if (!getters.isShare() && getters.eventTheme() === "halloween") {
      return true
    }
    if (!getters.isLoggedIn()) {
      return defaultDarkMode()
    }
    return state.user.darkMode === true
  },
  isLoggedIn: () => {
    if (state.user === null) {
      return false
    }
    if (state.user.locale === undefined || state.user.locale === null) {
      let savedLocale = localStorage.getItem('userLocale')
      if (!savedLocale) {
        savedLocale = detectLocale()
      }
      void mutations.updateCurrentUser({ locale: savedLocale })
    }
    if (globalVars.noAuth) {
      return true
    }
    if (
      state.user !== null &&
      state.user?.username !== '' &&
      state.user?.username !== 'anonymous'
    ) {
      return true
    }
    return false
  },
  isAdmin: () => state.user.permissions?.admin === true,
  isFiles: () => state.route.path.startsWith('/files'),
  isListing: () => getters.isFiles() || (getters.isShare() && state.req.type === 'directory'),
  selectedCount: () =>
    Array.isArray(state.selected) ? state.selected.length : 0,
  getFirstSelected: () => {
    const first = state.selected[0];
    // eslint-disable-next-line security/detect-object-injection -- first is a numeric array index, not a property lookup
    return typeof first === 'number' ? state.req.items[first] : first;
  },
  isSingleFileSelected: () =>
    getters.selectedCount() === 1 &&
    getters.getFirstSelected()?.type !== 'directory',
  selectedDownloadUrl () {
    if (!state.selected || state.selected.length === 0) return "";

    if (state.isSearchActive) {
      const first = state.selected[0] as FileListItem;
      return buildItemUrl(first.source, first.path)
    }
    const first = state.selected[0] as number;
    // eslint-disable-next-line security/detect-object-injection -- first is a numeric array index, not a property lookup
    const item = state.req.items[first];
    return item ? buildItemUrl(item.source, item.path) : "";
  },
  reqNumDirs: () => {
    let dirCount = 0
    if (!state.req.items) {
      return 0
    }
    state.req.items.forEach(item => {
      // Check if the item is a directory
      if (item.type === 'directory') {
        // Otherwise, count this directory
        dirCount++
      }
    })
    // Return the directory count
    return dirCount
  },
  reqNumFiles: () => {
    let fileCount = 0
    if (!state.req.items) {
      return 0
    }
    state.req.items.forEach(item => {
      // Check if the item is a directory
      if (item.type !== 'directory') {
        // Otherwise, count this directory
        fileCount++
      }
    })
    // Return the directory count
    return fileCount
  },
  reqItems: () => {
    if (state.user === null) return { pinned: [], dirs: [], files: [] };
    const pinned = [];
    const dirs = [];
    const files = [];
    if (!state.req?.items) return { pinned, dirs, files };

    for (const item of state.req.items) {
      if (item.pinned) {
        pinned.push(item);
        continue;
      }
      if (item.type === 'directory') {
        dirs.push(item);
      } else {
        // Pre-existing: capitalized "Path" (not FileListItem's "path"), never read elsewhere. Left as-is.
        (item as FileListItem & { Path?: string }).Path = state.req.path;
        files.push(item);
      }
    }
    return { pinned, dirs, files };
  },
  isSidebarVisible: () => {
    if (globalVars.disableSidebar || getters.isInvalidShare()) {
      return false
    }
    const cv = getters.currentView()
    if (cv === 'onlyOfficeEditor') {
      return false
    }
    let visible = (state.showSidebar || getters.isStickySidebar())
    if (getters.currentPromptName() && !getters.isStickySidebar()) {
      visible = false
    }
    if (previewViews.includes(cv) && !getters.previewPerms().disableHideSidebar) {
      visible = false
    }
    if (state.shareInfo?.singleFileShare) {
      visible = state.showSidebar
    }
    return visible
  },
  sidebarWidth: () => state.sidebar.width,
  sidebarMode: () => state.sidebar.mode,
  isStickySidebar: () => {
    let sticky = state.user?.stickySidebar
    const currentView = getters.currentView()
    if (currentView === 'settings') {
      sticky = true
    }
    if (currentView === '' && !getters.isLoading()) {
      sticky = true
    }
    if (getters.isMobile()) {
      sticky = false
    }
    return sticky
  },
  showOverlay: () => {
    const hasPrompt =
      getters.currentPrompt() !== null && getters.currentPromptName() !== 'more'
    const showForSidebar =
      getters.isSidebarVisible() && !getters.isStickySidebar()
    return hasPrompt || showForSidebar || state.isSearchActive
  },
  showBreadCrumbs: () => {
    return getters.currentView() === 'listingView'
  },
  routePath: (trimModifier = '') => {
    return removePrefix(state.route.path, trimModifier)
  },
  shareHash: () => {
    if (!state.route.path.startsWith('/public/share')) {
      return ""
    }
    const urlPath = getters.routePath('/public/share')
    const urlPathParts = urlPath.split('/')
    if (urlPathParts.length < 1) {
      return ""
    }
    return urlPathParts[1]
  },
  sharePathBase: () => {
    return `/public/share/${getters.shareHash()}/`
  },
  getSharePath: (subPath = "") => {
    if (!state.route.path.startsWith('/public/share')) {
      return ""
    }
    let urlPath = getters.routePath('/public/share')
    if (urlPath === "/" || urlPath === "") {
      return "";
    }
    // remove hash from path and decode each part
    const parts = urlPath.split('/').slice(2);
    urlPath = parts.map(part => decodeURIComponent(part)).join('/')
    if (subPath !== "") {
      urlPath = url.joinPath(urlPath, removeLeadingSlash(subPath))
    }
    return urlPath
  },
  isShare: () => {
    return getters.shareHash() !== ""
  },
  currentView: (): string => {
    if (state.navigation.isTransitioning) return 'loading';
    const pathname = getters.routePath();
    if (!state.user || state.user?.username === "") return 'login';
    if (pathname.startsWith(`/settings`)) return 'settings';
    if (pathname.startsWith(`/tools`)) return 'tools';

    if (state.req.type !== undefined) {
      const ext = `.${state.req.name.split(".").pop().toLowerCase()}`;
      if (state.user.disableViewingExt?.includes(ext)) return 'preview';
      if (state.req.type === 'directory') return 'listingView';
      if (state.req.onlyOfficeId && !getters.officeViewingDisabled(state.req.name)) return 'onlyOfficeEditor';
      if (getTypeInfo(state.req.type).simpleType === '3d-model') return 'threeJsViewer';

      if ('content' in state.req && isRichTextPreviewMimeType(state.req.type)) {
        const hash = window.location.hash;
        const preferEditor = state.user.preferEditorForMarkdown;

        switch (hash) {
          case '#edit': return 'editor';
          case '#preview': return 'markdownViewer';
        }
        if (state.req.type === 'text/markdown' && preferEditor) return 'editor';
        return 'markdownViewer';
      }

      if ('content' in state.req) return 'editor';
      if (state.req.type.startsWith('application/epub')) return 'epubViewer';
      if (state.req.type.startsWith('application/vnd.openxmlformats-officedocument.wordprocessingml.document')) return 'docViewer';
      return 'preview';
    }
    return 'listingView';
  },
  progress: () => {
    // Check if state.upload is defined and valid
    if (
      !state.upload ||
      !Array.isArray(state.upload.progress) ||
      !Array.isArray(state.upload.sizes)
    ) {
      return 0
    }

    // Handle cases where progress or sizes arrays might be empty
    if (state.upload.progress.length === 0 || state.upload.sizes.length === 0) {
      return 0
    }

    // Calculate totalSize
    const totalSize = state.upload.sizes.reduce((a, b) => a + b, 0)

    // Calculate sum of progress
    const sum = state.upload.progress.reduce((acc, val) => acc + val, 0)

    // Return progress as a percentage
    return Math.ceil((sum / totalSize) * 100)
  },
  filesInUploadCount: () => {
    const uploadsCount = Object.keys(state.upload.uploads || {}).length
    const queueCount = state.upload.queue?.length || 0
    return uploadsCount + queueCount
  },
  currentPrompt: () => {
    // Ensure state.prompts is an array
    if (!Array.isArray(state.prompts)) {
      return null
    }
    if (state.prompts.length === 0) {
      return null
    }
    return state.prompts[state.prompts.length - 1]
  },
  currentPromptName: () => {
    // Ensure state.prompts is an array
    if (!Array.isArray(state.prompts) || state.prompts.length === 0) {
      return ""
    }
    // Check if the name property is a string
    const lastPrompt = state.prompts[state.prompts.length - 1] as { name?: string };
    if (typeof lastPrompt?.name !== 'string') {
      return ""
    }
    return lastPrompt.name
  },
  isUploading: () => state.upload.isUploading,
  filesInUpload: () => {
    // Ensure state.upload.uploads is an object and state.upload.sizes is an array
    if (
      typeof state.upload.uploads !== 'object' ||
      !Array.isArray(state.upload.sizes)
    ) {
      return []
    }

    const files = []

    for (const index of Object.keys(state.upload.uploads)) {
      const upload = getObjectProperty(state.upload.uploads, index) as
        { id: string | number; type: string; file: { name: string; type: string } } | undefined
      if (!upload) continue
      const id = upload.id
      const type = upload.type
      const name = upload.file.name
      const size = (getObjectProperty(state.upload.sizes, id) as number | undefined) ?? 0 // Default to 0 if size is undefined
      const isDir = upload.file.type === 'directory'
      const loaded = (getObjectProperty(state.upload.progress, id) as number | undefined) || 0 // Default to 0 if progress is undefined
      const safeSize = size > 0 ? size : 1
      const progress = isDir
        ? 100
        : Math.ceil((loaded / safeSize) * 100)
      files.push({
        id,
        name,
        progress,
        type,
        isDir
      })
    }
    return files.sort((a, b) => a.progress - b.progress)
  },
  fileViewingDisabled: filename => {
    if (getters.isShare()) {
      if (state.shareInfo?.disableFileViewer || state.shareInfo?.shareType === "upload") {
        return true
      }
    } else {
      if (!getters.sourcePermissions().view) {
        return true
      }
    }
    const ext = ` ${getFileExtension(filename)}`;
    if (state.user.disableViewingExt) {
      const disabledExts = ` ${state.user.disableViewingExt.toLowerCase()}`;
      if (disabledExts.includes(ext.toLowerCase())) {
        return true
      }
    }
    return false
  },
  officeViewingDisabled: filename => {
    const ext = ` ${getFileExtension(filename)}`;
    const disabledList = state.user.disableOnlyOfficeExt || ''
    if (disabledList === '*') {
      return true
    }
    if (disabledList !== '') {
      const disabledExts = ` ${disabledList.toLowerCase()}`;
      if (disabledExts.includes(ext.toLowerCase())) {
        return true
      }
    }
    return false
  },
  shouldFetchFileContent: (fileInfo: { name: string; source?: string; onlyOfficeId?: string }) => {
    if (getters.fileViewingDisabled(fileInfo.name)) return false
    if (fileInfo.onlyOfficeId && !getters.officeViewingDisabled(fileInfo.name)) return false
    if (!getters.sourcePermissions(fileInfo.source).download) return false
    return true
  },
  anonymous: () => {
    return {
      id: 0,
      username: "anonymous",
      locale: detectLocale(),
      sorting: {
        by: "name",
        asc: true
      },
      viewMode: "normal",
      singleClick: true,
      quickDownload: false,
      gallerySize: 5,
      permissions: {
        share: false,
        modify: false,
        api: false,
        admin: false,
        realtime: false
      },
      preview: {
        video: true,
        audio: true,
        image: true,
        popup: true,
        models: true,
        autoplayMedia: true,
      },
      disableSettings: true,
      disableQuickToggles: false,
      disableSearchOptions: false,
      deleteWithoutConfirming: false,
      deleteAfterArchive: true,
      stickySidebar: true,
      hideFilesInTree: false,
      darkMode: defaultDarkMode(),
      dateFormat: false,
      disableViewingExt: "",
      disableOfficePreviewExt: "",
      disablePreviewExt: "",
      preferEditorForMarkdown: false,
      fileLoading: {
        maxConcurrent: 1,
        chunkSizeMb: 5,
      }
    }
  },
  multibuttonState: () => {
    const cv = getters.currentView()
    const isSidebarVisible = getters.isSidebarVisible()
    if (isSidebarVisible) {
      if (cv === "tools") {
        if (state.user.stickySidebar) {
          return "menu";
        }
        return "back";
      }
      if (cv === "settings") {
        if (getters.isMobile()) {
          return "back";
        }
        return "close";
      }
      if (getters.isMobile()) {
        return "back";
      }
      if (cv === "listingView" || state.shareInfo?.singleFileShare) {
        if (state.user?.stickySidebar) {
          return "menu";
        }
        return "back";
      }
      return "close";
    }
    if (cv === "tools") {
      return "menu";
    }
    if (state.shareInfo?.singleFileShare) {
      return "menu";
    }
    if (cv === "settings") {
      if (getters.isMobile()) {
        return "menu";
      }
    }
    if (cv === "listingView") {
      return "menu";
    }
    return "close";
  },
  isInvalidShare: () => {
    return getters.shareHash() !== "" && state.shareInfo.hash === "" && globalVars.shareHash === "";
  },
  isValidShare: () => {
    return getters.shareHash() !== "" && (state.shareInfo.hash !== "" || globalVars.shareHash !== "");
  },
  currentTool: () => {
    if (getters.currentView() !== "tools") {
      return null;
    }
    // Match by path instead of route name
    const tool = tools().find(t => t.path === state.route.path);
    // Return null when at /tools (list view) to avoid circular component rendering
    return tool;
  },
  isEditorOrMarkdownView: () => {
    return getters.currentView() === 'editor' || getters.currentView() === 'markdownViewer';
  },
  showStatusBar: () => {
    if (getters.isShare() && state.shareInfo.shareType === "upload") {
      return false;
    }
    return (
      getters.currentView() === "listingView" ||
      getters.isEditorOrMarkdownView() ||
      getters.currentTool()?.component === "AdvancedSearch"
    );
  },
  showGallerySizeSlider: () => {
    return (
      getters.currentView() === "listingView" ||
      getters.currentTool()?.component === "AdvancedSearch"
    );
  },
  globalPermissions: () => {
    if (getters.isShare()) {
      return {
        share: false,
        admin: false,
        api: false,
        realtime: false,
        archive: false,
      };
    }
    const globalPerms = state.user?.permissions ?? {};
    return {
      share: !!(globalPerms.share || globalPerms.admin),
      admin: !!globalPerms.admin,
      api: !!globalPerms.api,
      realtime: !!globalPerms.realtime,
      archive: !!globalPerms.archive,
    };
  },
  sourcePermissions: (source?: string) => {
    if (getters.isShare()) {
      return {
        view: !state.shareInfo?.disableFileViewer,
        modify: !!state.shareInfo?.allowModify,
        create: !!state.shareInfo?.allowCreate,
        delete: !!state.shareInfo?.allowDelete,
        download: !state.shareInfo?.disableDownload,
      };
    }
    const activeSource =
      source ?? state.req?.source ?? state.sources?.current ?? "";
    const denyFile = {
      view: false,
      download: false,
      modify: false,
      create: false,
      delete: false,
    };
    if ((globalVars.devMode || globalVars.playwrightTest) && activeSource === 'mockData') {
      return {
        view: true,
        download: false,
        modify: false,
        create: false,
        delete: false,
      };
    }
    if (!activeSource || !Array.isArray(state.user?.scopes)) {
      return denyFile;
    }
    const scopeEntry = state.user.scopes.find((entry) => entry?.name === activeSource);
    return scopeEntry?.permissions ?? denyFile;
  },
  /** Whether the current user may create files/folders in the given source (share-aware). */
  canCreateInSource: (source) => {
    if (getters.isShare()) {
      return !!state.shareInfo?.allowCreate;
    }
    const activeSource = source ?? state.req?.source ?? state.sources?.current ?? "";
    return !!getters.sourcePermissions(activeSource)?.create;
  },
  apiTokenPermissionCaps: () => {
    const globalPerms = state.user?.permissions ?? {};
    return {
      admin: !!globalPerms.admin,
      api: !!globalPerms.api,
      share: !!globalPerms.share,
      realtime: !!globalPerms.realtime,
    };
  },
  previewPerms: () => {
    if (getters.isShare()) {
      // For shares, use defaults for preview settings (shares don't have per-share preview config)
      return {
        audio: state.user?.preview?.audio ?? true,
        video: state.user?.preview?.video ?? true,
        image: state.user?.preview?.image ?? true,
        office: state.user?.preview?.office ?? true,
        folder: state.user?.preview?.folder ?? true,
        popup: state.user?.preview?.popup ?? true,
        models: state.user?.preview?.models ?? true,
        motionVideoPreview: state.user?.preview?.motionVideoPreview ?? false,
        disableHideSidebar: state.user?.preview?.disableHideSidebar ?? false,
        autoplayMedia: state.user?.preview?.autoplayMedia ?? true,
        showHidden: state.shareInfo?.showHidden !== undefined ? state.shareInfo?.showHidden : false,
      };
    }
    // For regular users, use their preview settings -- unless is share
    return {
      audio: state.user?.preview?.audio ?? true,
      video: state.user?.preview?.video ?? true,
      image: state.user?.preview?.image ?? true,
      office: state.user?.preview?.office ?? true,
      folder: state.user?.preview?.folder ?? true,
      popup: state.user?.preview?.popup ?? true,
      models: state.user?.preview?.models ?? true,
      motionVideoPreview: state.user?.preview?.motionVideoPreview ?? false,
      disableHideSidebar: state.user?.preview?.disableHideSidebar ?? false,
      autoplayMedia: state.user?.preview?.autoplayMedia ?? true,
      showHidden: false, // Backend handles this now, but kept for API compatibility
    };
  }
};
