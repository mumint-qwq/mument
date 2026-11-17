// Modified from FileBrowser Quantum for the mument derivative. See MODIFICATIONS.md.
import { notify } from "@/notify";
import { state } from "@/store";
import { getApiPath, getPublicApiPath } from "@/utils/url.js";
import { adjustedData, fetchURL } from "./utils";
import { getCachedDirMetadata, setCachedDirMetadata } from "@/utils/metadataCache.js";
import { isMediaFile } from "@/utils/mediaFile";

// GET /api/media/subtitles or /public/api/media/subtitles
export async function getSubtitleContent(source, path, subtitleName, embedded = false) {
  try {
    const hash = state.shareInfo?.hash || null
    const baseParams = {
      path,
      name: subtitleName,
      ...(embedded && { embedded: 'true' }),
    }

    let res
    if (hash) {
      const apiPath = getPublicApiPath('media/subtitles', {
        hash,
        ...baseParams,
      })
      const sharePassword = localStorage.getItem(`sharepass:${hash}`) || ''
      res = await fetchURL(apiPath, {
        headers: { 'X-SHARE-PASSWORD': encodeURIComponent(sharePassword) },
      })
    } else {
      const apiPath = getApiPath('media/subtitles', {
        source,
        ...baseParams,
      })
      res = await fetchURL(apiPath)
    }

    const content = await res.text()
    return content
  } catch (err) {
    notify.showError(err.message || `Error fetching subtitle ${subtitleName}`)
    throw err
  }
}

// GET /api/media/lyrics
export async function getLyrics(source, path) {
    const apiPath = getApiPath('media/lyrics', {
        source: source,
        path: path,
    });
    const res = await fetchURL(apiPath);
    const data = await res.json();
    return data.lyrics || [];
}

// GET /public/api/media/lyrics
export async function getLyricsPublic(path, hash, password = "") {
    const params = {
        path,
        hash,
    };
    const apiPath = getPublicApiPath("media/lyrics", params);
    const response = await fetch(apiPath, {
        headers: { "X-SHARE-PASSWORD": encodeURIComponent(password || "") },
    });
    if (!response.ok) {
        const error = new Error(response.statusText);
        const data = await response.json();
        if (data?.message) {
            error.message = data.message;
        }
        error.status = response.status;
        throw error;
    }
    const data = await response.json();
    return data.lyrics || [];
}

// GET /api/media/metadata — directory or file with metadata; optional albumArt for embedded cover extraction.
/** @param {boolean} albumArt when true, request embedded album art in audio metadata */
/** @returns {Promise<object>} resource (adjustedData) */
export async function fetchDirectoryMediaMetadata(source, path, albumArt = false) {
  const apiPath = getApiPath("media/metadata", {
    source,
    path,
    ...(albumArt ? { albumArt: "true" } : {}),
  });
  const res = await fetchURL(apiPath);
  const data = await res.json();
  return adjustedData(data);
}

// GET /public/api/media/metadata
/** @param {boolean} albumArt when true, request embedded album art in audio metadata */
/** @returns {Promise<object>} resource (adjustedData) */
export async function fetchDirectoryMediaMetadataPublic(path, hash, password = "", albumArt = false) {
  const params = {
    path,
    hash,
    ...(albumArt ? { albumArt: "true" } : {}),
  };
  const apiPath = getPublicApiPath("media/metadata", params);
  const response = await fetch(apiPath, {
    headers: { "X-SHARE-PASSWORD": encodeURIComponent(password || "") },
  });
  if (!response.ok) {
    const error = new Error(response.statusText);
    let data = null;
    try {
      data = await response.json();
    } catch (_e) {
      // ignore
    }
    if (data?.message) {
      error.message = data.message;
    }
    /** @type {any} */ (error).status = response.status;
    throw error;
  }
  const data = await response.json();
  return adjustedData(data);
}

/** Number of paths sent per POST /resources/metadata request. */
export const MEDIA_METADATA_BATCH_SIZE = 12;

function isMediaListingItem(item) {
  if (!item) {
    return false;
  }
  const type = typeof item.type === "string" ? item.type : "";
  if (type.startsWith("audio/") || type.startsWith("video/")) {
    return true;
  }
  return isMediaFile(item.name) || isMediaFile(item.path);
}

function joinMediaPath(dirPath, name) {
  if (!name) {
    return "";
  }
  const base = !dirPath || dirPath === "/" ? "" : dirPath.replace(/\/+$/, "");
  return `${base}/${name}`;
}

/**
 * POST /api/resources/metadata (or /public/api/resources/metadata) for a bounded
 * list of paths. The server resolves each path and returns per-item errors.
 * @param {string[]} paths
 * @param {{isShare?: boolean, source?: string, hash?: string, password?: string, albumArt?: boolean, signal?: AbortSignal}} opts
 * @returns {Promise<{items?: Array<{path: string, name?: string, metadata?: object, error?: string}>}>}
 */
export async function fetchMediaMetadataPaths(paths, opts = {}) {
  const { isShare = false, source, hash, password = "", albumArt = false, signal } = opts;
  const body = JSON.stringify({ source, paths, albumArt });
  if (isShare) {
    const apiPath = getPublicApiPath("resources/metadata", { hash });
    const headers = { "Content-Type": "application/json" };
    if (password) {
      headers["X-SHARE-PASSWORD"] = encodeURIComponent(password);
    }
    const response = await fetch(apiPath, {
      method: "POST",
      credentials: "same-origin",
      headers,
      body,
      signal,
    });
    if (!response.ok) {
      let message = response.statusText;
      try {
        const data = await response.json();
        if (data?.message) {
          message = data.message;
        }
      } catch (_e) {
        // ignore non-JSON error bodies
      }
      const error = new Error(message || "Error fetching media metadata");
      error.status = response.status;
      throw error;
    }
    return response.json();
  }
  const apiPath = getApiPath("resources/metadata");
  const response = await fetchURL(apiPath, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body,
    signal,
  });
  return response.json();
}

async function fetchMediaEntriesForDirectory(path, { isShare = false, source, hash, password = "" } = {}) {
  const params = { path, only: "files" };
  let apiPath;
  const headers = {};
  if (isShare) {
    apiPath = getPublicApiPath("resources/items", { ...params, hash });
    if (password) {
      headers["X-SHARE-PASSWORD"] = encodeURIComponent(password);
    }
  } else {
    apiPath = getApiPath("resources/items", { ...params, source });
  }
  const response = await fetchURL(apiPath, { headers });
  const data = await response.json();
  return (data?.files || [])
    .filter((name) => isMediaFile(name))
    .map((name) => ({ name, path: joinMediaPath(path, name), type: "" }));
}

/**
 * Fetches metadata for the media items of one directory in small batches.
 * Each batch is patched through onBatch as soon as it returns, so the first
 * listing paint never waits for every probe. A failed batch only affects that
 * batch; later batches still run.
 * @param {string} path - directory path
 * @param {{isShare?: boolean, source?: string, hash?: string, password?: string, albumArt?: boolean, items?: object[], onBatch?: (chunk: Map<string, object>, batch: object[], error?: Error) => void, signal?: AbortSignal}} opts
 * @returns {Promise<Map<string, object>>}
 */
export async function getDirectoryMetadataMap(path, opts = {}) {
  const {
    isShare = false,
    source,
    hash,
    password = "",
    albumArt = false,
    items = null,
    onBatch = null,
    signal = null,
  } = opts;
  const cacheOpts = { isShare, source, hash, path, albumArt };
  const cached = getCachedDirMetadata(cacheOpts);
  if (cached !== null) {
    return cached;
  }
  const promise = (async () => {
    const map = new Map();
    // A listing failure rejects the cached promise (metadataCache drops it) so a
    // later call can retry instead of pinning an empty result.
    const candidates = Array.isArray(items)
      ? items
      : await fetchMediaEntriesForDirectory(path, { isShare, source, hash, password });
    const mediaEntries = candidates
      .filter(isMediaListingItem)
      .map((item) => ({ ...item, path: item.path || joinMediaPath(path, item.name) }))
      .filter((item) => item.path);
    for (let i = 0; i < mediaEntries.length; i += MEDIA_METADATA_BATCH_SIZE) {
      if (signal?.aborted) {
        break;
      }
      const batch = mediaEntries.slice(i, i + MEDIA_METADATA_BATCH_SIZE);
      const chunk = new Map();
      let failure = null;
      try {
        const payload = await fetchMediaMetadataPaths(
          batch.map((item) => item.path),
          { isShare, source, hash, password, albumArt, signal }
        );
        for (const entry of payload?.items || []) {
          if (entry?.error || !entry?.metadata) {
            continue;
          }
          const name = entry.name || entry.path?.split("/").pop();
          if (name) {
            chunk.set(name, entry.metadata);
            map.set(name, entry.metadata);
          }
        }
      } catch (err) {
        failure = err;
      }
      if (signal?.aborted) {
        break;
      }
      if (onBatch) {
        onBatch(chunk, batch, failure || undefined);
      }
    }
    return map;
  })();
  setCachedDirMetadata(cacheOpts, promise);
  return promise;
}

// GET /api/media/stream — audio/video bytes via viewToken (range-based, not download-metered).
export function getStreamURL(source, path, viewToken) {
  if (!source || source === undefined || source === null) {
    throw new Error('no source provided')
  }
  if (!viewToken) {
    throw new Error('view token required')
  }
  try {
    const params = {
      source: source,
      file: path,
      viewToken: viewToken,
      sessionId: state.sessionId,
    }
    const apiPath = getApiPath('media/stream', params)
    return window.origin + apiPath
  } catch (err) {
    notify.showError(err.message || 'Error getting stream URL')
    throw err
  }
}

// GET /public/api/media/stream
export function getStreamURLPublic(share, files, viewToken) {
  if (!viewToken) {
    throw new Error('view token required')
  }
  const fileArray = Array.isArray(files) ? files : [files]
  const params = {
    file: fileArray,
    hash: share.hash,
    viewToken: viewToken,
    sessionId: state.sessionId,
  }
  const apiPath = getPublicApiPath('media/stream', params)
  return window.origin + apiPath
}
