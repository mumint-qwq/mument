/**
 * Serializes per-device singleClick writes with "latest intent wins" semantics.
 *
 * Why: the UI can be toggled faster than the network answers. If requests ran
 * in parallel, an older response could be committed last and leave the server
 * with a value the user no longer wants. One worker sends at most one request
 * at a time and always starts the next round from the newest operation.
 *
 * Every operation is an opaque context supplied by the caller (session token,
 * intent revision, device id, username). Callbacks receive that frozen context
 * instead of reading mutable globals, so a late response can never be
 * re-attributed to a newer session.
 *
 * The module holds no store/network knowledge; callers inject the callbacks,
 * which keeps it unit-testable and free of Vue reactivity.
 */
/**
 * @typedef {{
 *   desired: boolean | null,
 *   token: number,
 *   intentRevision: number,
 *   deviceId: string,
 *   username: string,
 * }} DeviceSaveOperation
 */
/**
 * @param {{
 *   readOperation: () => (DeviceSaveOperation | null),
 *   save: (operation: DeviceSaveOperation) => Promise<void>,
 *   isActive?: (operation: DeviceSaveOperation) => boolean,
 *   onPending?: (operation: DeviceSaveOperation, pending: boolean) => void,
 *   onSettled?: (operation: DeviceSaveOperation, result: {
 *     ok: boolean,
 *     error: Error | null,
 *   }) => ("continue" | "stop" | void),
 * }} options
 */
export function createDeviceSaveQueue({
  readOperation,
  save,
  isActive = () => true,
  onPending = () => {},
  onSettled = () => {},
}) {
  let running = false;
  let queued = false;
  let chain = Promise.resolve();

  async function drain() {
    running = true;
    try {
      while (queued) {
        queued = false;
        const operation = readOperation();
        if (!operation) {
          continue;
        }
        if (!isActive(operation)) {
          // A newer session owns the queue now; skip this operation but keep
          // the worker alive so an already-queued new operation is not lost.
          continue;
        }
        onPending(operation, true);
        let failure = null;
        try {
          await save(operation);
        } catch (error) {
          failure = error;
        } finally {
          onPending(operation, false);
        }
        if (!isActive(operation)) {
          continue;
        }
        const decision = onSettled(operation, {
          ok: failure === null,
          error: failure,
        });
        if (decision === "stop") {
          queued = false;
          return;
        }
      }
    } finally {
      running = false;
    }
  }

  return {
    /**
     * Request a drain. Concurrent calls share the running worker.
     * @returns {Promise<void>}
     */
    enqueue() {
      queued = true;
      if (running) {
        return chain;
      }
      chain = drain();
      return chain;
    },
    /** Drop any not-yet-started intent (used on logout / account switch). */
    cancel() {
      queued = false;
    },
    get running() {
      return running;
    },
  };
}
