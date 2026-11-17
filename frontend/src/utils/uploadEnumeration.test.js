import { describe, expect, it, vi } from "vitest";
import {
  describeDroppedEntries,
  describeFileList,
  streamUploadBatches,
  walkDroppedEntries,
  walkFileList,
} from "./uploadEnumeration";

function deferred() {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
}

function fileEntry(path, size = 1, error = null) {
  const name = path.split("/").pop();
  return {
    isFile: true,
    isDirectory: false,
    name,
    fullPath: `/${path}`,
    file: (resolve, reject) => {
      if (error) {
        reject(error);
      } else {
        resolve({ name, size });
      }
    },
  };
}

function directoryEntry(path, reader) {
  return {
    isFile: false,
    isDirectory: true,
    name: path.split("/").pop(),
    fullPath: `/${path}`,
    createReader: () => reader,
  };
}

function readerWithBatches(batches, onRead = () => {}) {
  let index = 0;
  return {
    readEntries(resolve) {
      const batch = batches.at(index) || [];
      index += 1;
      onRead(index, batch);
      resolve(batch);
    },
  };
}

describe("walkDroppedEntries", () => {
  it("walks reader batches and yields the first file before the whole tree is read", async () => {
    const secondBatch = deferred();
    let firstBatchRead = false;
    const reader = {
      calls: 0,
      readEntries(resolve) {
        this.calls += 1;
        if (this.calls === 1) {
          firstBatchRead = true;
          resolve([fileEntry("Top/a.jpg"), directoryEntry("Top/sub", readerWithBatches([]))]);
          return;
        }
        if (this.calls === 2) {
          void secondBatch.promise.then(() => resolve([fileEntry("Top/b.jpg")]));
          return;
        }
        resolve([]);
      },
    };
    const iterator = walkDroppedEntries([directoryEntry("Top", reader)]);

    const first = await iterator.next();
    expect(first.value.relativePath).toBe("Top/a.jpg");
    expect(firstBatchRead).toBe(true);
    // The first file was available from the first reader batch; the second
    // readEntries() call has not even been issued yet.
    expect(reader.calls).toBe(1);

    secondBatch.resolve();
    const rest = [];
    for (let next = await iterator.next(); !next.done; next = await iterator.next()) {
      rest.push(next.value.relativePath);
    }
    // The second reader batch brings Top/b.jpg; the empty Top/sub directory is
    // reported as a directory marker so it can still be created remotely.
    expect(rest).toEqual(["Top/b.jpg", "Top/sub/"]);
  });

  it("yields root files before walking sibling directories", async () => {
    const reader = readerWithBatches([[fileEntry("Folder/inner.txt")]]);
    const items = [];
    for await (const item of walkDroppedEntries([
      fileEntry("loose.txt"),
      directoryEntry("Folder", reader),
    ])) {
      items.push(item.relativePath);
    }
    expect(items).toEqual(["loose.txt", "Folder/inner.txt"]);
  });

  it("recurses through nested directories one batch at a time", async () => {
    const childReader = readerWithBatches([[fileEntry("Top/sub/deep.txt")]]);
    const topReader = readerWithBatches([
      [directoryEntry("Top/sub", childReader)],
      [fileEntry("Top/shallow.txt")],
    ]);
    const items = [];
    for await (const item of walkDroppedEntries([directoryEntry("Top", topReader)])) {
      items.push(item.relativePath);
    }
    expect(items.sort()).toEqual(["Top/shallow.txt", "Top/sub/deep.txt"]);
  });

  it("stops between batches when the signal is aborted", async () => {
    const controller = new AbortController();
    const reader = readerWithBatches([
      [fileEntry("Top/a.txt")],
      [fileEntry("Top/b.txt")],
    ]);
    const iterator = walkDroppedEntries([directoryEntry("Top", reader)], {
      signal: controller.signal,
    });
    expect((await iterator.next()).value.relativePath).toBe("Top/a.txt");
    controller.abort();
    expect((await iterator.next()).done).toBe(true);
  });

  it("yields an empty-directory marker so the folder can still be created", async () => {
    const reader = readerWithBatches([
      [directoryEntry("Top/empty", readerWithBatches([]))],
    ]);
    const items = [];
    for await (const item of walkDroppedEntries([directoryEntry("Top", reader)])) {
      items.push(item);
    }
    expect(items).toEqual([
      { relativePath: "Top/empty/", isDirectory: true },
    ]);
  });

  it("skips one unreadable file and keeps walking its siblings", async () => {
    const error = new Error("permission denied");
    const reader = readerWithBatches([
      [
        fileEntry("Top/a.txt"),
        fileEntry("Top/bad.txt", 1, error),
        fileEntry("Top/c.txt"),
      ],
    ]);
    const unreadable = [];
    const items = [];
    for await (const item of walkDroppedEntries([directoryEntry("Top", reader)], {
      onError: (reason, path) => unreadable.push([reason, path]),
    })) {
      items.push(item.relativePath);
    }
    expect(items).toEqual(["Top/a.txt", "Top/c.txt"]);
    expect(unreadable).toEqual([[error, "Top/bad.txt"]]);
  });

  it("skips an unreadable directory and continues with its siblings", async () => {
    const error = new Error("unreadable directory");
    const failingReader = {
      readEntries(_resolve, reject) {
        reject(error);
      },
    };
    const goodReader = readerWithBatches([[fileEntry("Top/good/ok.txt")]]);
    const topReader = readerWithBatches([
      [
        directoryEntry("Top/bad", failingReader),
        directoryEntry("Top/good", goodReader),
      ],
    ]);
    const unreadable = [];
    const items = [];
    for await (const item of walkDroppedEntries([directoryEntry("Top", topReader)], {
      onError: (reason, path) => unreadable.push([reason, path]),
    })) {
      items.push(item.relativePath);
    }
    expect(items).toEqual(["Top/good/ok.txt"]);
    expect(unreadable).toEqual([[error, "Top/bad"]]);
  });

  it("walks deeply nested directories without recursive calls", async () => {
    function build(depth, prefix) {
      if (depth === 0) {
        const path = `${prefix}/file.txt`;
        return { entry: fileEntry(path), expected: path };
      }
      const path = `${prefix}/d${depth}`;
      const child = build(depth - 1, path);
      return {
        entry: directoryEntry(path, readerWithBatches([[child.entry]])),
        expected: child.expected,
      };
    }

    const { entry, expected } = build(300, "root");
    const items = [];
    for await (const item of walkDroppedEntries([entry])) {
      items.push(item.relativePath);
    }
    expect(items).toEqual([expected]);
    expect(expected.split("/")).toHaveLength(302);
  });
});

describe("walkFileList", () => {
  it("yields FileList entries in chunks with their relative paths", async () => {
    const files = [
      { name: "a.txt", webkitRelativePath: "Top/a.txt" },
      { name: "b.txt", webkitRelativePath: "Top/b.txt" },
      { name: "c.txt", webkitRelativePath: "Top/c.txt" },
    ];
    const items = [];
    for await (const item of walkFileList(files, { chunkSize: 2 })) {
      items.push(item.relativePath);
    }
    expect(items).toEqual(["Top/a.txt", "Top/b.txt", "Top/c.txt"]);
  });

  it("falls back to the file name for loose files", async () => {
    const items = [];
    for await (const item of walkFileList([{ name: "loose.txt" }])) {
      items.push(item.relativePath);
    }
    expect(items).toEqual(["loose.txt"]);
  });
});

describe("describeDroppedEntries", () => {
  it("summarises directory roots and loose files", () => {
    expect(describeDroppedEntries([
      directoryEntry("Trip", readerWithBatches([])),
      fileEntry("cover.jpg"),
    ])).toEqual([
      { name: "Trip", relativePath: "Trip/", isDirectory: true },
      {
        name: "cover.jpg",
        relativePath: "cover.jpg",
        isDirectory: false,
        file: { name: "cover.jpg", size: 0 },
      },
    ]);
  });
});

describe("describeFileList", () => {
  it("keeps one root per top-level folder and each loose file", () => {
    const roots = describeFileList([
      { name: "a.jpg", webkitRelativePath: "Trip/a.jpg" },
      { name: "b.jpg", webkitRelativePath: "Trip/day2/b.jpg" },
      { name: "pick.jpg" },
    ]);
    expect(roots.map((root) => root.relativePath)).toEqual(["Trip/", "pick.jpg"]);
    expect(roots[0].isDirectory).toBe(true);
    expect(roots[1].isDirectory).toBe(false);
  });
});

describe("streamUploadBatches", () => {
  it("enqueues the first batch before the source finishes", async () => {
    const gate = deferred();
    async function* source() {
      yield { relativePath: "a.txt" };
      yield { relativePath: "b.txt" };
      await gate.promise;
      yield { relativePath: "c.txt" };
    }
    const session = { addItems: vi.fn(async () => true), finish: vi.fn() };
    const running = streamUploadBatches(source(), session, { batchSize: 2 });

    await vi.waitFor(() => expect(session.addItems).toHaveBeenCalledTimes(1));
    expect(session.addItems.mock.calls[0][0]).toHaveLength(2);
    expect(session.finish).not.toHaveBeenCalled();

    gate.resolve();
    await expect(running).resolves.toEqual({ files: 3, batches: 2, cancelled: false });
    expect(session.finish).toHaveBeenCalledTimes(1);
  });

  it("stops and finishes when the session is cancelled", async () => {
    async function* source() {
      yield { relativePath: "a.txt" };
      yield { relativePath: "b.txt" };
      yield { relativePath: "c.txt" };
    }
    const session = {
      addItems: vi.fn(async () => true).mockResolvedValueOnce(true).mockResolvedValueOnce(false),
      finish: vi.fn(),
    };
    const result = await streamUploadBatches(source(), session, { batchSize: 1 });
    expect(result.cancelled).toBe(true);
    expect(result.files).toBe(1);
    expect(session.addItems).toHaveBeenCalledTimes(2);
    expect(session.finish).toHaveBeenCalledTimes(1);
  });

  it("does not enqueue anything once the signal is aborted", async () => {
    const controller = new AbortController();
    controller.abort();
    async function* source() {
      yield { relativePath: "a.txt" };
    }
    const session = { addItems: vi.fn(async () => true), finish: vi.fn() };
    const result = await streamUploadBatches(source(), session, {
      batchSize: 1,
      signal: controller.signal,
    });
    expect(session.addItems).not.toHaveBeenCalled();
    expect(result.files).toBe(0);
    expect(session.finish).toHaveBeenCalledTimes(1);
  });
});