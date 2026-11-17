/** Event-driven catch-up for thumbnail candidates inside the 600px prefetch band.
 *
 * The IntersectionObserver already reports every enter/leave. The only states
 * it cannot report are "the fling slowed down but I never left the band" and
 * "a gesture ended on top of the row", so this module keeps the currently
 * in-band candidates and re-checks them once when one of those transitions
 * happens. There is no periodic walk over cards: scroll ticks only sample
 * velocity to detect the fast -> slow edge.
 */
import {
  getScrollVelocity,
  isScrolling,
  onScrollIdle,
  onScrollTick,
} from "./scrollActivity.js";
import { isInteractionActive, onInteractionChange } from "./interactionGuard.js";
import { SLOW_SCROLL_PX_PER_MS } from "./thumbnailLoadPolicy.js";

const candidates = new Set();
const pending = new Set();
let flushAll = false;
let scheduled = false;
let unsubscribe = null;
let wasFast = false;

function slowEnough() {
  if (isInteractionActive()) return false;
  return !isScrolling() || getScrollVelocity() <= SLOW_SCROLL_PX_PER_MS;
}

function runFlush() {
  scheduled = false;
  // Keep the pending set so the transition/idle that makes loading legal
  // still covers these rows. Duplicate requests are prevented by each item's
  // own wanted flag and by dropping unregistered rows here.
  if (!slowEnough()) return;
  const items = flushAll ? [...candidates] : [...pending];
  flushAll = false;
  pending.clear();
  for (const item of items) {
    if (candidates.has(item)) item.evaluateThumbnailNeed?.();
  }
}

function scheduleFlush(item) {
  if (item) pending.add(item);
  else flushAll = true;
  if (scheduled) return;
  scheduled = true;
  const run = () => runFlush();
  if (typeof requestAnimationFrame === "function") requestAnimationFrame(run);
  else setTimeout(run, 0);
}

function handleTick(velocity) {
  const fast = isScrolling() && velocity > SLOW_SCROLL_PX_PER_MS;
  if (wasFast && !fast) scheduleFlush();
  wasFast = fast;
}

function handleIdle() {
  wasFast = false;
  scheduleFlush();
}

function handleInteraction(active) {
  if (!active) scheduleFlush();
}

function ensureSubscribed() {
  if (unsubscribe) return;
  const offTick = onScrollTick(handleTick);
  const offIdle = onScrollIdle(handleIdle);
  const offInteraction = onInteractionChange(handleInteraction);
  unsubscribe = () => {
    offTick();
    offIdle();
    offInteraction();
    unsubscribe = null;
  };
}

export function registerThumbnailCandidate(item) {
  candidates.add(item);
  ensureSubscribed();
  scheduleFlush(item);
}

export function unregisterThumbnailCandidate(item) {
  candidates.delete(item);
  pending.delete(item);
  if (candidates.size === 0) {
    pending.clear();
    flushAll = false;
    if (unsubscribe) unsubscribe();
    wasFast = false;
  }
}

/** Test-only reset. */
export function resetThumbnailScheduler() {
  candidates.clear();
  pending.clear();
  flushAll = false;
  scheduled = false;
  wasFast = false;
  if (unsubscribe) unsubscribe();
}
