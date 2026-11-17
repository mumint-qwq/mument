/** One passive scroll tracker for the whole app.
 *
 * Thumbnail loading needs three signals:
 *  - "is the user still scrolling" (idle),
 *  - "how fast" (velocity), so a slow browse may keep loading while a fling does not,
 *  - periodic ticks while scrolling, so a fast -> slow transition can re-evaluate
 *    items whose IntersectionObserver state did not change.
 *
 * Only scroll containers registered by the listing are counted; unrelated panel
 * scrolling must not freeze thumbnails.
 */
export const SCROLL_IDLE_MS = 140;
export const SCROLL_TICK_MS = 60;
export const SCROLL_VELOCITY_WINDOW_MS = 120;

let scrolling = false;
let timer = null;
let lastTickAt = 0;
let velocity = 0;
let lastSample = null;
let listening = false;
let trackedRoot = null;

const idleListeners = new Set();
const startListeners = new Set();
const tickListeners = new Set();

// jsdom and some embedders expose a different Window object than the global, so
// compare structurally instead of with ===.
function isDocumentLike(target) {
  if (!target || target === document || target === window) return true;
  if (typeof target !== "object") return false;
  if (target.window === target) return true;
  return target.document === document;
}

function scrollPosition(target) {
  if (isDocumentLike(target)) {
    const scroller = typeof document !== "undefined" ? document.scrollingElement || document.documentElement : null;
    return scroller ? scroller.scrollTop : 0;
  }
  return typeof target.scrollTop === "number" ? target.scrollTop : null;
}

function isTracked(target) {
  if (!trackedRoot) return true;
  if (isDocumentLike(target)) return true;
  if (target === trackedRoot) return true;
  return typeof trackedRoot.contains === "function" && trackedRoot.contains(target);
}

function notify(listeners, ...args) {
  for (const listener of [...listeners]) listener(...args);
}

function now() {
  return typeof performance !== "undefined" && typeof performance.now === "function"
    ? performance.now()
    : Date.now();
}

function handleScroll(event) {
  if (!isTracked(event.target)) return;
  const position = scrollPosition(event.target);
  if (position === null) return;
  const time = now();
  if (lastSample) {
    const dt = time - lastSample.t;
    if (dt > 0 && dt <= SCROLL_VELOCITY_WINDOW_MS * 2) {
      const instant = Math.abs(position - lastSample.y) / dt;
      velocity = velocity === 0 ? instant : velocity * 0.35 + instant * 0.65;
    }
  }
  lastSample = { y: position, t: time };
  const started = !scrolling;
  if (started) {
    scrolling = true;
    notify(startListeners);
  }
  if (timer !== null) clearTimeout(timer);
  timer = setTimeout(finishScroll, SCROLL_IDLE_MS);
  // Leading tick plus a bounded tick rate: a fast -> slow transition is noticed
  // without running one callback per scroll event.
  if (started || time - lastTickAt >= SCROLL_TICK_MS) {
    lastTickAt = time;
    notify(tickListeners, velocity);
  }
}

function finishScroll() {
  timer = null;
  scrolling = false;
  velocity = 0;
  lastSample = null;
  notify(idleListeners);
}

function ensureListening() {
  if (listening || typeof window === "undefined") return;
  listening = true;
  // Capture catches scroll events from inner listing containers, which do not bubble.
  window.addEventListener("scroll", handleScroll, { capture: true, passive: true });
}

/** Restrict counting to one listing container (and the document); null means "any container". */
export function registerScrollRoot(element) {
  trackedRoot = element || null;
  ensureListening();
  return () => {
    if (trackedRoot === (element || null)) trackedRoot = null;
  };
}

export function isScrolling() {
  return scrolling;
}

/** Smoothed pixels per millisecond over the recent scroll samples. */
export function getScrollVelocity() {
  return velocity;
}

export function onScrollIdle(listener) {
  ensureListening();
  idleListeners.add(listener);
  return () => idleListeners.delete(listener);
}

export function onScrollStart(listener) {
  ensureListening();
  startListeners.add(listener);
  return () => startListeners.delete(listener);
}

/** Called at most once per SCROLL_TICK_MS while scrolling. */
export function onScrollTick(listener) {
  ensureListening();
  tickListeners.add(listener);
  return () => tickListeners.delete(listener);
}

/** Test-only reset; no production caller. */
export function resetScrollActivity() {
  if (timer !== null) clearTimeout(timer);
  timer = null;
  scrolling = false;
  velocity = 0;
  lastSample = null;
  lastTickAt = 0;
  trackedRoot = null;
}
