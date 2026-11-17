/** Bridge between Files.vue scroll restoration and the progressive listing mount plan.
 *
 * `revealListingTarget` returning true only means "the active listing accepted
 * this request", not that the scroll has been applied. A caller that needs the
 * final state passes `onSettled`; ListingView reports success only after the
 * target card exists in the DOM and the measured offset matches the requested
 * one (or the scroll is clamped at a legal boundary). Failed/cancelled requests
 * always get a terminal callback so logs cannot claim an unfinished restore.
 */
let controller = null;
let requestCounter = 0;
const pending = new Map();
const SETTLE_TIMEOUT_MS = 2500;

function now() {
  return typeof performance !== "undefined" ? performance.now() : Date.now();
}

export function registerListingMountController(next) {
  controller = next;
}

export function unregisterListingMountController(current) {
  if (controller === current) controller = null;
}

/**
 * Ask the active listing view to mount the target row and scroll to it.
 * Callers keep their DOM fallback when this returns false.
 */
export function revealListingTarget(target = {}) {
  if (!controller?.revealTarget) return false;
  const { onSettled, ticket = null, ...request } = target;
  requestCounter += 1;
  const requestId = `r${requestCounter}`;
  let accepted;
  try {
    accepted = controller.revealTarget({ ...request, requestId, ticket }) === true;
  } catch (_error) {
    return false;
  }
  if (!accepted) return false;
  // Track even callback-less requests: a timeout still has to invalidate the
  // listing's pending plan, otherwise it could scroll long after the caller
  // gave up waiting.
  const entry = {
    onSettled: typeof onSettled === "function" ? onSettled : null,
    ticket,
    startedAt: now(),
    timerId: null,
  };
  entry.timerId = setTimeout(() => {
    abortPendingReveal(requestId, { result: "failed", reason: "settle_timeout" });
  }, SETTLE_TIMEOUT_MS);
  pending.set(requestId, entry);
  return true;
}

/** Abort one request: drop the timer, invalidate the controller plan, report once. */
function abortPendingReveal(requestId, result) {
  const entry = pending.get(requestId);
  if (!entry) return false;
  // Remove first so a synchronous controller settle cannot call back twice.
  pending.delete(requestId);
  if (entry.timerId) clearTimeout(entry.timerId);
  try {
    controller?.cancelReveal?.({
      requestId,
      ticket: entry.ticket,
      ...result,
    });
  } catch (_error) {
    // The listing may already be gone; the pending callback is what matters.
  }
  entry.onSettled?.({ ...result, requestId });
  return true;
}

/** Called by ListingView on the frame where the final position was verified. */
export function settleListingReveal(requestId, result = {}) {
  const entry = pending.get(requestId);
  if (!entry) return false;
  pending.delete(requestId);
  if (entry.timerId) clearTimeout(entry.timerId);
  entry.onSettled({ ...result, requestId });
  return true;
}

/** Abort a pending ticket (user gesture, navigation, plan replacement). */
export function cancelListingRevealForTicket(ticket, result = {}) {
  if (!ticket) return false;
  const requestIds = [];
  for (const [requestId, entry] of pending) {
    if (entry.ticket === ticket) requestIds.push(requestId);
  }
  for (const requestId of requestIds) {
    abortPendingReveal(requestId, { result: "cancelled", ...result });
  }
  return requestIds.length > 0;
}

/** Drop every pending request (page unmount/navigation). */
export function cancelAllListingReveals(result = {}) {
  for (const requestId of [...pending.keys()]) {
    abortPendingReveal(requestId, { result: "cancelled", ...result });
  }
}

/** Ask the next mount plan to ignore the old preview/hash target for this refresh. */
export function prepareListingRestore(request = {}) {
  return controller?.prepareRestore?.(request) === true;
}

/** Drop an unconsumed suppression (empty directory, aborted refresh). */
export function clearListingRestorePreparation() {
  controller?.clearRestore?.();
}

export function pendingListingRevealCount() {
  return pending.size;
}
