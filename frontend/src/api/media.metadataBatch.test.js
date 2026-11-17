import { describe, expect, it, vi, beforeEach } from "vitest";

const { fetchURLMock, stateMock } = vi.hoisted(() => ({
  fetchURLMock: vi.fn(),
  stateMock: {
    sessionId: "test-session",
    shareInfo: { subPath: "", hash: "", token: "" },
  },
}));

vi.mock("@/utils/constants", () => ({
  globalVars: {
    baseURL: "/",
    externalUrl: "",
    devMode: false,
    playwrightTest: false,
  },
}));

vi.mock("@/store", () => ({
  getters: { isShare: () => false },
  mutations: {},
  state: stateMock,
}));

vi.mock("@/notify", () => ({
  notify: { showError: vi.fn() },
}));

vi.mock("./utils", async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    fetchURL: (...args) => fetchURLMock(...args),
  };
});

import {
  getDirectoryMetadataMap,
  fetchMediaMetadataPaths,
  MEDIA_METADATA_BATCH_SIZE,
} from "@/api/media";
import { invalidateDirMetadataCache } from "@/utils/metadataCache.js";

function mediaItems(count) {
  return Array.from({ length: count }, (_, i) => ({
    name: `video-${i}.mp4`,
    path: `/videos/video-${i}.mp4`,
    type: "video/mp4",
  }));
}

function metadataResultFor(paths) {
  return {
    items: paths.map((path) => ({
      path,
      name: path.split("/").pop(),
      metadata: { duration: path.length },
    })),
  };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

async function flush() {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

describe("getDirectoryMetadataMap batching", () => {
  beforeEach(() => {
    invalidateDirMetadataCache();
    fetchURLMock.mockReset();
  });

  it("splits media items into bounded batches and merges results", async () => {
    const items = mediaItems(30);
    fetchURLMock.mockImplementation((_url, opts) => {
      const body = JSON.parse(opts.body);
      return Promise.resolve({
        json: async () => metadataResultFor(body.paths),
      });
    });
    const batches = [];

    const map = await getDirectoryMetadataMap("/videos/", {
      source: "src",
      items,
      onBatch: (chunk, batch) => batches.push({ size: chunk.size, count: batch.length }),
    });

    expect(fetchURLMock).toHaveBeenCalledTimes(3);
    expect(batches.map((b) => b.count)).toEqual([
      MEDIA_METADATA_BATCH_SIZE,
      MEDIA_METADATA_BATCH_SIZE,
      30 - MEDIA_METADATA_BATCH_SIZE * 2,
    ]);
    expect(map.size).toBe(30);
    expect(map.get("video-0.mp4")).toEqual({ duration: "/videos/video-0.mp4".length });
  });

  it("patches the first batch before later batches finish", async () => {
    const items = mediaItems(MEDIA_METADATA_BATCH_SIZE * 2);
    const first = deferred();
    const second = deferred();
    fetchURLMock
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise);
    const batches = [];

    const promise = getDirectoryMetadataMap("/videos/", {
      source: "src",
      items,
      onBatch: (chunk) => batches.push(chunk.size),
    });
    await flush();
    expect(batches).toEqual([]);
    expect(fetchURLMock).toHaveBeenCalledTimes(1);

    first.resolve({ json: async () => metadataResultFor(items.slice(0, MEDIA_METADATA_BATCH_SIZE).map((i) => i.path)) });
    await flush();
    expect(batches).toEqual([MEDIA_METADATA_BATCH_SIZE]);
    expect(fetchURLMock).toHaveBeenCalledTimes(2);

    second.resolve({ json: async () => metadataResultFor(items.slice(MEDIA_METADATA_BATCH_SIZE).map((i) => i.path)) });
    const map = await promise;
    expect(batches).toEqual([MEDIA_METADATA_BATCH_SIZE, MEDIA_METADATA_BATCH_SIZE]);
    expect(map.size).toBe(MEDIA_METADATA_BATCH_SIZE * 2);
  });

  it("does not wait for all 50 videos before the first batch patches", async () => {
    const items = mediaItems(50);
    const pending = [];
    fetchURLMock.mockImplementation(() => {
      const request = deferred();
      pending.push(request);
      return request.promise;
    });
    const batches = [];

    const promise = getDirectoryMetadataMap("/videos/", {
      source: "src",
      items,
      onBatch: (chunk) => batches.push(chunk.size),
    });
    await flush();
    expect(fetchURLMock).toHaveBeenCalledTimes(1);
    expect(batches).toEqual([]);

    pending[0].resolve({
      json: async () => metadataResultFor(items.slice(0, MEDIA_METADATA_BATCH_SIZE).map((i) => i.path)),
    });
    await flush();
    expect(batches).toEqual([MEDIA_METADATA_BATCH_SIZE]);
    expect(fetchURLMock).toHaveBeenCalledTimes(2);

    for (let batch = 1; batch < 5; batch++) {
      pending.at(batch).resolve({
        json: async () =>
          metadataResultFor(
            items
              .slice(batch * MEDIA_METADATA_BATCH_SIZE, (batch + 1) * MEDIA_METADATA_BATCH_SIZE)
              .map((i) => i.path)
          ),
      });
      await flush();
    }
    const map = await promise;
    expect(map.size).toBe(50);
  });

  it("keeps later batches when an earlier batch fails", async () => {
    const items = mediaItems(MEDIA_METADATA_BATCH_SIZE * 2);
    let call = 0;
    fetchURLMock.mockImplementation((_url, opts) => {
      call += 1;
      if (call === 1) {
        return Promise.reject(new Error("probe unavailable"));
      }
      const body = JSON.parse(opts.body);
      return Promise.resolve({
        json: async () => metadataResultFor(body.paths),
      });
    });
    const errors = [];

    const map = await getDirectoryMetadataMap("/videos/", {
      source: "src",
      items,
      onBatch: (_chunk, _batch, error) => errors.push(error?.message || null),
    });

    expect(errors).toEqual(["probe unavailable", null]);
    expect(map.size).toBe(MEDIA_METADATA_BATCH_SIZE);
  });

  it("ignores non-media items and sends albumArt with the subset", async () => {
    fetchURLMock.mockResolvedValue({
      json: async () => metadataResultFor(["/videos/a.mp4"]),
    });
    const map = await getDirectoryMetadataMap("/videos/", {
      source: "src",
      albumArt: true,
      items: [
        { name: "a.mp4", path: "/videos/a.mp4", type: "video/mp4" },
        { name: "notes.txt", path: "/videos/notes.txt", type: "text/plain" },
        { name: "folder", path: "/videos/folder/", type: "directory" },
      ],
    });

    expect(fetchURLMock).toHaveBeenCalledTimes(1);
    const body = JSON.parse(fetchURLMock.mock.calls[0][1].body);
    expect(body.paths).toEqual(["/videos/a.mp4"]);
    expect(body.albumArt).toBe(true);
    expect(map.size).toBe(1);
  });
});

describe("fetchMediaMetadataPaths", () => {
  beforeEach(() => {
    invalidateDirMetadataCache();
    fetchURLMock.mockReset();
  });

  it("posts to the resources metadata endpoint", async () => {
    fetchURLMock.mockResolvedValue({ json: async () => ({ items: [] }) });
    await fetchMediaMetadataPaths(["/music/a.mp3"], { source: "src" });

    expect(fetchURLMock).toHaveBeenCalledTimes(1);
    const [url, opts] = fetchURLMock.mock.calls[0];
    expect(url).toContain("/api/resources/metadata");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body)).toEqual({
      source: "src",
      paths: ["/music/a.mp3"],
      albumArt: false,
    });
  });
});
