import { describe, expect, it, vi } from "vitest";
import { createDeviceSaveQueue } from "@/utils/deviceSaveQueue.js";

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function operation(desired, extra = {}) {
  return { desired, token: 1, intentRevision: 1, ...extra };
}

describe("device save queue", () => {
  it("sends one operation and records success", async () => {
    let pending = operation(true);
    const saves = [];
    const settled = [];
    const queue = createDeviceSaveQueue({
      readOperation: () => pending,
      save: async (op) => {
        saves.push(op.desired);
        pending = null;
      },
      isActive: () => true,
      onSettled: (op, result) => settled.push({ desired: op.desired, result }),
    });
    await queue.enqueue();
    expect(saves).toEqual([true]);
    expect(settled).toEqual([{ desired: true, result: { ok: true, error: null } }]);
    expect(queue.running).toBe(false);
  });

  it("does nothing when there is no pending operation", async () => {
    const save = vi.fn();
    const queue = createDeviceSaveQueue({
      readOperation: () => null,
      save,
    });
    await queue.enqueue();
    expect(save).not.toHaveBeenCalled();
  });

  it("serializes rapid intents and persists the last one", async () => {
    const pending = deferred();
    const saves = [];
    let desired = true;
    const queue = createDeviceSaveQueue({
      readOperation: () => operation(desired),
      save: async (op) => {
        saves.push(op.desired);
        if (saves.length === 1) {
          await pending.promise;
        }
      },
    });

    const first = queue.enqueue();
    desired = false;
    void queue.enqueue();
    desired = true;
    void queue.enqueue();
    expect(queue.running).toBe(true);

    pending.resolve();
    await first;

    // The second round reads the newest intent (true); after the first write
    // the caller confirms it, so no redundant write is needed in this test.
    expect(saves).toEqual([true, true]);
    expect(queue.running).toBe(false);
  });

  it("passes the frozen operation context to every callback", async () => {
    const seen = [];
    const queue = createDeviceSaveQueue({
      readOperation: () => operation(true, { token: 7, intentRevision: 3, deviceId: "dev-1" }),
      isActive: (op) => {
        seen.push(["active", op.token, op.intentRevision]);
        return true;
      },
      save: async (op) => seen.push(["save", op.token, op.intentRevision, op.deviceId]),
      onPending: (op, flag) => seen.push(["pending", op.token, op.intentRevision, flag]),
      onSettled: (op, result) => {
        seen.push(["settled", op.token, op.intentRevision, result.ok]);
      },
    });
    await queue.enqueue();
    expect(seen).toEqual([
      ["active", 7, 3],
      ["pending", 7, 3, true],
      ["save", 7, 3, "dev-1"],
      ["pending", 7, 3, false],
      ["active", 7, 3],
      ["settled", 7, 3, true],
    ]);
  });

  it("stops after a failure that reports stop", async () => {
    const save = vi.fn(async () => {
      throw new Error("boom");
    });
    const settled = [];
    const queue = createDeviceSaveQueue({
      readOperation: () => operation(true),
      save,
      onSettled: (op, result) => {
        settled.push(result);
        return "stop";
      },
    });
    await queue.enqueue();
    expect(save).toHaveBeenCalledTimes(1);
    expect(settled[0].ok).toBe(false);
    expect(settled[0].error.message).toBe("boom");
  });

  it("keeps a newer intent after a failure that reports continue", async () => {
    let desired = true;
    const saves = [];
    const queue = createDeviceSaveQueue({
      readOperation: () => operation(desired),
      save: async (op) => {
        saves.push(op.desired);
        if (saves.length === 1) {
          desired = false;
          throw new Error("response lost");
        }
      },
      onSettled: (_op, result) => (result.ok ? "continue" : "continue"),
    });
    // The queue is enqueued again while the first save is in flight: the
    // failed operation must not drop it.
    const first = queue.enqueue();
    void queue.enqueue();
    await first;
    expect(saves).toEqual([true, false]);
  });

  it("skips an operation from an inactive session and serves the next one", async () => {
    let token = 1;
    let desired = true;
    const saves = [];
    const queue = createDeviceSaveQueue({
      readOperation: () => operation(desired, { token }),
      isActive: (op) => op.token === token,
      save: async (op) => {
        saves.push([op.token, op.desired]);
        if (saves.length === 1) {
          // Session switched while the first request was in flight.
          token = 2;
          desired = false;
        }
      },
    });
    const first = queue.enqueue();
    void queue.enqueue();
    await first;
    expect(saves).toEqual([[1, true], [2, false]]);
  });

  it("drops a queued intent on cancel", async () => {
    const pending = deferred();
    const save = vi.fn(async () => {
      await pending.promise;
    });
    const queue = createDeviceSaveQueue({
      readOperation: () => operation(true),
      save,
    });
    const first = queue.enqueue();
    void queue.enqueue();
    queue.cancel();
    pending.resolve();
    await first;
    expect(save).toHaveBeenCalledTimes(1);
  });
});
