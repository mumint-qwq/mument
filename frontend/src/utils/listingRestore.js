/** Apply a claimed listing anchor to the refreshed data.
 *
 * Business rules live here so Files.vue only supplies the current DOM/state:
 * resolve the anchor by stable source+path identity (then bounded backups),
 * prefer the progressive mount bridge, fall back to a bounded DOM retry, and
 * finally clamp the captured scroll offset. A user gesture cancels the pending
 * correction instead of pulling them back to the old position.
 */
import { listingItemKey } from "./listAnchor.js";
import { cancelListingRevealForTicket, revealListingTarget } from "./listingMountBridge.js";

export const RESTORE_TOLERANCE_PX = 8;
const DOM_RETRY_LIMIT = 6;

export function findItemIndexForKey(items, source, key) {
  if (!Array.isArray(items) || !key) return -1;
  return items.findIndex((item) => listingItemKey(item, source) === key);
}

/** Resolve the preferred identity; a missing identity means pixel fallback. */
export function resolveAnchorTarget(items, anchor, source) {
  if (!Array.isArray(items) || items.length === 0) {
    return { index: -1, strategy: "empty", offsetTop: 0, key: null };
  }
  const primary = findItemIndexForKey(items, source, anchor?.anchorKey);
  if (primary >= 0) {
    // A ticket captured as neighbor_after/before already chose a survivor
    // outside the old visible block. Finding it again is not "identity": its
    // strategy (and the fact that it belongs at the viewport top) must survive.
    const neighborAnchor = anchor?.anchorKind === "neighbor_after" || anchor?.anchorKind === "neighbor_before";
    return {
      index: primary,
      strategy: neighborAnchor ? "neighbor" : "identity",
      offsetTop: neighborAnchor ? 0 : (Number.isFinite(anchor?.offsetTop) ? anchor.offsetTop : 0),
      key: anchor.anchorKey,
    };
  }
  for (const key of Array.isArray(anchor?.candidates) ? anchor.candidates : []) {
    const index = findItemIndexForKey(items, source, key);
    if (index >= 0) {
      // The old primary is gone: start at the top of the viewport rather than
      // with an offset that could keep the neighbour above the visible area.
      return { index, strategy: "neighbor", offsetTop: 0, key };
    }
  }
  return { index: -1, strategy: "fallback_pixels", offsetTop: 0, key: null };
}

export function clampScrollTop(value, max) {
  const safeMax = Number.isFinite(max) ? Math.max(0, max) : 0;
  if (!Number.isFinite(value)) return 0;
  return Math.min(Math.max(0, value), safeMax);
}

function contentTop(scroller, element) {
  const scrollerRect = scroller.getBoundingClientRect();
  const rect = element.getBoundingClientRect();
  return rect.top - scrollerRect.top + (scroller.scrollTop || 0);
}

/**
 * @param {object} options
 * @param {object} options.anchor claimed ticket
 * @param {Array} options.items current listing items
 * @param {string} options.source listing source for identity fallback
 * @param {HTMLElement} options.scroller active scroll root
 * @param {(index:number)=>HTMLElement|null} options.getElement main-listing card by index
 * @param {(result:object)=>void} options.onSettled exactly once, terminal result
 * @param {()=>boolean} [options.isActive] business request guard
 * @param {Function} [options.reveal] bridge reveal (injectable for tests)
 * @param {Function} [options.cancelReveal] bridge cancel (injectable for tests)
 * @param {Function} [options.raf] requestAnimationFrame (injectable for tests)
 * @param {Function} [options.cancelFrame]
 */
export function createAnchorRestore({
  anchor,
  items,
  source,
  scroller,
  getElement,
  onSettled,
  isActive = () => true,
  reveal = revealListingTarget,
  cancelReveal = cancelListingRevealForTicket,
  raf = typeof requestAnimationFrame === "function" ? requestAnimationFrame : (fn) => setTimeout(fn, 16),
  cancelFrame = typeof cancelAnimationFrame === "function" ? cancelAnimationFrame : (id) => clearTimeout(id),
}) {
  let done = false;
  let usedBridge = false;
  let frameId = null;
  let strategy = "fallback_pixels";
  let targetKey = anchor?.anchorKey || null;
  let lastRequested = null;
  let lastActual = null;
  let preMountedAtStart = false;

  const maxScrollTop = () => Math.max(0, (scroller?.scrollHeight || 0) - (scroller?.clientHeight || 0));

  function finish(result, detail = {}) {
    if (done) return;
    done = true;
    if (frameId !== null) {
      cancelFrame(frameId);
      frameId = null;
    }
    onSettled({
      result,
      strategy,
      targetKey,
      preMounted: preMountedAtStart,
      requestedOffset: lastRequested,
      actualOffset: lastActual,
      ...detail,
    });
  }

  function isBusinessActive() {
    try {
      return isActive() !== false;
    } catch (_error) {
      return false;
    }
  }

  function cancelIfInactive() {
    if (isBusinessActive()) return false;
    lastActual = scroller?.scrollTop ?? null;
    finish("cancelled", { reason: "superseded" });
    return true;
  }

  function applyFallbackPixels(result, reason) {
    // A request that was superseded must not write a pixel guess. This covers
    // empty/no-identity/bridge-failure paths, not only the DOM retry loop.
    if (cancelIfInactive()) return;
    strategy = "fallback_pixels";
    const target = clampScrollTop(anchor?.fallbackScrollTop || 0, maxScrollTop());
    if (scroller) scroller.scrollTop = target;
    lastRequested = target;
    lastActual = scroller?.scrollTop ?? null;
    finish(result, { reason, finalStrategy: "fallback_pixels" });
  }

  function attemptDom(resolved, attempt, preMounted) {
    if (done) return;
    if (!isActive()) {
      lastActual = scroller?.scrollTop ?? null;
      finish("cancelled", { reason: "superseded" });
      return;
    }
    const element = getElement(resolved.index);
    if (!element) {
      if (attempt >= DOM_RETRY_LIMIT) {
        applyFallbackPixels("failed", "target_not_mounted");
        return;
      }
      frameId = raf(() => attemptDom(resolved, attempt + 1, preMounted));
      return;
    }
    const max = maxScrollTop();
    const raw = contentTop(scroller, element) - resolved.offsetTop;
    const requested = clampScrollTop(raw, max);
    scroller.scrollTop = requested;
    lastRequested = requested;
    lastActual = scroller.scrollTop;
    frameId = raf(() => {
      frameId = null;
      if (done) return;
      if (!isActive()) return finish("cancelled", { reason: "superseded" });
      const finalElement = getElement(resolved.index);
      if (!finalElement) {
        applyFallbackPixels("failed", "target_lost_after_scroll");
        return;
      }
      const actual = scroller.scrollTop;
      const offset = finalElement.getBoundingClientRect().top - scroller.getBoundingClientRect().top;
      const clamped = requested !== raw;
      const offsetMatches = Math.abs(offset - resolved.offsetTop) <= RESTORE_TOLERANCE_PX;
      const boundaryMatches = clamped && Math.abs(actual - requested) <= 0.5;
      lastActual = actual;
      if (offsetMatches || boundaryMatches) {
        finish("success", { preMounted });
        return;
      }
      if (attempt < DOM_RETRY_LIMIT) {
        attemptDom(resolved, attempt + 1, preMounted);
        return;
      }
      applyFallbackPixels("failed", "offset_mismatch");
    });
  }

  function begin() {
    if (cancelIfInactive()) return;
    const resolved = resolveAnchorTarget(items, anchor, source);
    strategy = resolved.strategy;
    targetKey = resolved.key;
    if (!resolved.key || resolved.index < 0) {
      if (resolved.strategy === "empty") {
        if (cancelIfInactive()) return;
        if (scroller) scroller.scrollTop = 0;
        lastRequested = 0;
        lastActual = scroller?.scrollTop ?? 0;
        finish("success", { reason: "empty_directory" });
      } else {
        applyFallbackPixels("success", "no_identity");
      }
      return;
    }
    if (cancelIfInactive()) return;
    const preMounted = Boolean(getElement(resolved.index));
    preMountedAtStart = preMounted;
    const accepted = reveal({
      index: resolved.index,
      offsetTop: resolved.offsetTop,
      block: "top",
      ticket: anchor?.ticket || null,
      onSettled: (info = {}) => {
        if (done) return;
        lastRequested = Number.isFinite(info.requestedOffset) ? info.requestedOffset : null;
        lastActual = Number.isFinite(info.actualOffset) ? info.actualOffset : (scroller?.scrollTop ?? null);
        // Timeouts/failures can arrive after navigation. The business request
        // decides whether a terminal reveal may still touch the page.
        if (cancelIfInactive()) return;
        if (info.result === "success") {
          finish("success", { strategy: resolved.strategy, reason: info.reason });
        } else if (info.result === "cancelled") {
          finish("cancelled", { reason: info.reason || "cancelled" });
        } else {
          applyFallbackPixels("failed", info.reason || "reveal_failed");
        }
      },
    });
    if (accepted) {
      usedBridge = true;
      return;
    }
    attemptDom(resolved, 0, preMounted);
  }

  return {
    begin,
    /** A real wheel/touch/key gesture wins over a late automatic correction. */
    handleUserIntent() {
      if (done) return false;
      if (usedBridge && anchor?.ticket) {
        cancelReveal(anchor.ticket, { reason: "user_scroll" });
        if (!done) {
          lastActual = scroller?.scrollTop ?? null;
          finish("cancelled", { reason: "user_scroll" });
        }
        return true;
      }
      lastActual = scroller?.scrollTop ?? null;
      finish("cancelled", { reason: "user_scroll" });
      return true;
    },
    /** Page/navigation went away; do not scroll anything. */
    cancel(reason = "unmounted") {
      if (done) return false;
      lastActual = scroller?.scrollTop ?? null;
      // Settle first so a synchronous bridge callback cannot record a second
      // terminal result; then make the listing drop the pending controller plan.
      finish("cancelled", { reason });
      if (usedBridge && anchor?.ticket) {
        cancelReveal(anchor.ticket, { reason });
      }
      return true;
    },
    isDone() {
      return done;
    },
  };
}
