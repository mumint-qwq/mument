/** Shared IntersectionObservers for thumbnail load and release margins.
 *
 * One observer per scroll root (not per row): a 4000-item listing must not own
 * 8000 observers. The root must be the actual scroll container; with root:null
 * the browser clips the expanded band to that container's scrollport and the
 * 600px prefetch margin collapses to the visible rows (checked 2026-10-29).
 */
export const THUMBNAIL_PREFETCH_MARGIN = 600;
export const THUMBNAIL_RELEASE_MARGIN = 1200;

const bandRegistries = new Map();
const releaseRegistries = new Map();

/** Nearest scrolling ancestor. The usual ".scroll-wrapper" is checked by class
 * first: getComputedStyle would force a style/layout read for every mounted row
 * of a 4000-item listing. Other layouts fall back to the computed overflow.
 * Null means document scrolling. */
export function findThumbnailScrollRoot(element) {
  const wrapper = element?.closest?.(".scroll-wrapper");
  if (wrapper) return wrapper;
  for (let node = element?.parentElement; node; node = node.parentElement) {
    const overflowY = getComputedStyle(node).overflowY;
    if (overflowY === "auto" || overflowY === "scroll") return node;
  }
  return null;
}

function createRegistry(store, root, rootMargin, mode) {
  const existing = store.get(root);
  if (existing) return existing;
  const handlers = new Map();
  const observer = new IntersectionObserver((records) => {
    for (const record of records) {
      const entryHandlers = handlers.get(record.target);
      if (!entryHandlers) continue;
      if (mode === "band") {
        if (record.isIntersecting) entryHandlers.onEnter?.();
        else entryHandlers.onLeave?.();
      } else if (!record.isIntersecting) {
        entryHandlers.onRelease?.();
      }
    }
  }, { root, rootMargin: `${rootMargin}px`, threshold: 0 });
  const registry = { observer, handlers };
  store.set(root, registry);
  return registry;
}

function watch(store, mode, margin, element, root, entryHandlers) {
  if (!element) return () => {};
  const registry = createRegistry(store, root, margin, mode);
  registry.handlers.set(element, entryHandlers);
  registry.observer.observe(element);
  return () => {
    registry.handlers.delete(element);
    registry.observer.unobserve(element);
    if (registry.handlers.size === 0 && store.get(root) === registry) {
      registry.observer.disconnect();
      store.delete(root);
    }
  };
}

/** Calls onEnter/onLeave as the row crosses the 600px band. */
export function watchThumbnailBand(element, root, entryHandlers) {
  return watch(bandRegistries, "band", THUMBNAIL_PREFETCH_MARGIN, element, root, entryHandlers);
}

/** Calls onRelease only when the row leaves the 1200px release margin. */
export function watchThumbnailRelease(element, root, entryHandlers) {
  return watch(releaseRegistries, "release", THUMBNAIL_RELEASE_MARGIN, element, root, entryHandlers);
}

/** Test-only reset. */
export function resetThumbnailVisibility() {
  for (const registry of bandRegistries.values()) registry.observer.disconnect();
  for (const registry of releaseRegistries.values()) registry.observer.disconnect();
  bandRegistries.clear();
  releaseRegistries.clear();
}
