/**
 * Incremental enumeration for folder uploads.
 *
 * A dropped folder can contain tens of thousands of entries, and the File System
 * API only returns directory listings in small readEntries() batches. Collecting
 * the complete tree before the first upload therefore delays every upload until
 * the whole folder has been walked. These helpers yield entries as they are
 * discovered; streamUploadBatches() forwards small batches to an upload session,
 * which applies queue backpressure before the next batch is read.
 */

export const UPLOAD_BATCH_SIZE = 32;
const FILE_LIST_CHUNK_SIZE = 128;

function relativePathOf(entry) {
  const path = entry.fullPath || entry.name || "";
  return path.startsWith("/") ? path.slice(1) : path;
}

function readEntryBatch(reader) {
  return new Promise((resolve, reject) => {
    reader.readEntries(resolve, reject);
  });
}

function fileItem(entry, onError) {
  return new Promise((resolve) => {
    entry.file(
      (file) => resolve(file ? { file, relativePath: relativePathOf(entry) } : null),
      (error) => {
        // One unreadable file must not abort the upload of its siblings.
        onError?.(error, relativePathOf(entry));
        resolve(null);
      }
    );
  });
}

// Only one reader batch is ever materialised at a time; a whole subtree is not
// kept in memory before the first upload starts.
async function fileItems(entries, signal, onError) {
  if (entries.length === 0 || signal?.aborted) {
    return [];
  }
  const items = await Promise.all(
    entries.map((entry) => fileItem(entry, onError))
  );
  return items.filter(Boolean);
}

function directoryMarker(directory) {
  const path = relativePathOf(directory).replace(/\/+$/, "");
  return { relativePath: `${path}/`, isDirectory: true };
}

/**
 * Walk dropped FileSystemEntry objects in reader-batch order.
 *
 * Root files are yielded first so loose drops do not wait for sibling folders.
 * Directories are walked breadth-first, one readEntries() batch at a time.
 *
 * @param {FileSystemEntry[]} entries
 * @param {{
 *   signal?: AbortSignal,
 *   onError?: (error: unknown, path: string) => void,
 * }} [options]
 * @returns {AsyncGenerator<{file?: File, relativePath: string, isDirectory?: boolean}>}
 */
export async function* walkDroppedEntries(entries, { signal, onError } = {}) {
  const directories = [];
  const rootFiles = [];
  for (const entry of entries) {
    if (!entry) continue;
    if (entry.isFile) rootFiles.push(entry);
    else if (entry.isDirectory) directories.push(entry);
  }

  for (const item of await fileItems(rootFiles, signal, onError)) {
    if (signal?.aborted) return;
    yield item;
  }

  // for..of reflects directories appended during the walk, so this stays
  // breadth-first without an O(n) shift or indexed object access.
  for (const directory of directories) {
    if (signal?.aborted) return;
    const reader = directory.createReader();
    const directoryPath = relativePathOf(directory);
    let readAnyEntry = false;
    let readFailed = false;
    try {
      while (true) {
        const batch = await readEntryBatch(reader);
        if (signal?.aborted) return;
        if (batch.length === 0) break;
        readAnyEntry = true;
        const batchFiles = [];
        for (const entry of batch) {
          if (entry.isDirectory) directories.push(entry);
          else if (entry.isFile) batchFiles.push(entry);
        }
        for (const item of await fileItems(batchFiles, signal, onError)) {
          if (signal?.aborted) return;
          yield item;
        }
      }
    } catch (error) {
      // Skip an unreadable directory instead of aborting the whole drop.
      readFailed = true;
      onError?.(error, directoryPath);
    }
    if (!readAnyEntry && !readFailed && !signal?.aborted) {
      yield directoryMarker(directory);
    }
  }
}

/**
 * Walk a FileList from <input webkitdirectory> in chunks. The browser has
 * already materialised the file list, but mapping and enqueueing it in one
 * synchronous pass still blocks the first upload, so entries are yielded in
 * bounded chunks.
 *
 * @param {FileList|File[]} fileList
 * @param {{signal?: AbortSignal, chunkSize?: number}} [options]
 * @returns {AsyncGenerator<{file: File, relativePath: string}>}
 */
export async function* walkFileList(fileList, { signal, chunkSize = FILE_LIST_CHUNK_SIZE } = {}) {
  let items = [];
  for (const file of fileList || []) {
    if (signal?.aborted) return;
    if (!file) continue;
    items.push({ file, relativePath: file.webkitRelativePath || file.name });
    if (items.length >= chunkSize) {
      for (const item of items) {
        if (signal?.aborted) return;
        yield item;
      }
      items = [];
    }
  }
  for (const item of items) {
    if (signal?.aborted) return;
    yield item;
  }
}

/**
 * Describe dropped roots for the upload manager's pre-upload conflict check.
 * Directory roots are represented as "name/" so they are detected as top-level
 * folders; loose files keep their own names.
 */
export function describeDroppedEntries(entries) {
  const roots = [];
  for (const entry of entries) {
    if (!entry) continue;
    if (entry.isDirectory) {
      roots.push({
        name: entry.name,
        relativePath: `${entry.name}/`,
        isDirectory: true,
      });
    } else if (entry.isFile) {
      roots.push({
        name: entry.name,
        relativePath: entry.name,
        isDirectory: false,
        file: { name: entry.name, size: 0 },
      });
    }
  }
  return roots;
}

/**
 * Describe a FileList's top-level folders and loose files for conflict checks.
 * Only the first path segment is needed, so a large folder list is summarised
 * without retaining every file.
 */
export function describeFileList(fileList) {
  const roots = new Map();
  for (const file of fileList || []) {
    if (!file) continue;
    const relativePath = file.webkitRelativePath || file.name;
    const slash = relativePath.indexOf("/");
    if (slash === -1) {
      roots.set(`file:${file.name}`, {
        name: file.name,
        relativePath: file.name,
        isDirectory: false,
        file,
      });
    } else {
      const name = relativePath.slice(0, slash);
      roots.set(`dir:${name}`, {
        name,
        relativePath: `${name}/`,
        isDirectory: true,
      });
    }
  }
  return [...roots.values()];
}

/**
 * Feed an entry source to an upload session in small batches.
 *
 * The session's addItems() enqueues the batch and then waits for queue capacity,
 * which is what stops a 20,000-file folder from materialising as 20,000 pending
 * jobs at once. The first batch is enqueued as soon as it is available, without
 * waiting for the rest of the tree.
 *
 * @param {AsyncIterable<{file: File, relativePath: string}>} source
 * @param {{addItems: (items: object[]) => Promise<boolean>, finish: () => void}} session
 * @param {{batchSize?: number, signal?: AbortSignal}} [options]
 */
export async function streamUploadBatches(source, session, { batchSize = UPLOAD_BATCH_SIZE, signal } = {}) {
  let batch = [];
  let files = 0;
  let batches = 0;
  try {
    for await (const item of source) {
      if (signal?.aborted) break;
      batch.push(item);
      if (batch.length >= batchSize) {
        if (!(await session.addItems(batch))) {
          return { files, batches, cancelled: true };
        }
        files += batch.filter((entry) => entry.isDirectory !== true).length;
        batches += 1;
        batch = [];
      }
    }
    if (batch.length > 0 && !signal?.aborted) {
      if (!(await session.addItems(batch))) {
        return { files, batches, cancelled: true };
      }
      files += batch.filter((entry) => entry.isDirectory !== true).length;
      batches += 1;
    }
    return { files, batches, cancelled: false };
  } finally {
    session.finish();
  }
}