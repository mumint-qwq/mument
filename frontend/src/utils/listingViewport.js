/** DOM capture for the shared listing viewport anchor.
 *
 * Only the main listing viewport is read: `.listing-view-root` inside the
 * active scroll container. Destination pickers, file trees and other prompts
 * use different roots and must never contribute an anchor. Geometry is
 * captured once, immediately before a refresh clears the listing; callers keep
 * the returned ticket in memory only.
 */
import { state } from "@/store";
import {
  chooseListingAnchor,
  listingItemKey,
  normalizeListingPath,
  rememberListAnchor,
  removedListingKeys,
} from "./listAnchor.js";
import { currentListingLifecycleId } from "./listingLifecycle.js";

export function activeScrollRoot() {
  if (typeof document === "undefined") return null;
  return document.querySelector(".scroll-wrapper") || document.scrollingElement || null;
}

/** Visible main-listing cards in viewport coordinates, sorted by top then left. */
export function readVisibleListingEntries(scroller, items, source = "") {
  if (!scroller || !Array.isArray(items)) return [];
  const root = document.querySelector(".listing-view-root");
  if (!root) return [];
  const viewport = scroller.getBoundingClientRect();
  const entries = [];
  for (const element of root.querySelectorAll(".listing-item[data-index]")) {
    const index = Number(element.dataset.index);
    const item = items.at(index);
    if (!item) continue;
    const rect = element.getBoundingClientRect();
    if (rect.width === 0 && rect.height === 0) continue;
    if (rect.bottom <= viewport.top || rect.top >= viewport.bottom) continue;
    entries.push({
      index,
      key: listingItemKey(item, source),
      top: rect.top - viewport.top,
      left: rect.left - viewport.left,
    });
  }
  entries.sort((left, right) => left.top - right.top || left.left - right.left);
  return entries;
}

/**
 * Capture a ticket for the next refresh of this directory.
 * Returns null when the current view is not a real directory listing.
 */
export function captureListingAnchor({
  reason,
  source,
  path,
  shareHash = "",
  removed = [],
  operationRef = null,
  lifecycleId = currentListingLifecycleId(),
  items = state.req?.items,
  type = state.req?.type,
  scroller = activeScrollRoot(),
} = {}) {
  if (typeof document === "undefined") return null;
  if (reason !== "move" && reason !== "delete" && reason !== "refresh") return null;
  if (type !== "directory" || !Array.isArray(items) || !scroller) return null;
  const normalizedSource = source || "";
  const removedKeys = removedListingKeys(removed, normalizedSource, path);
  const keys = items.map((item) => listingItemKey(item, normalizedSource));
  const entries = readVisibleListingEntries(scroller, items, normalizedSource);
  const choice = chooseListingAnchor(entries, keys, removedKeys);
  const anchor = {
    reason,
    source: normalizedSource,
    path: normalizeListingPath(path),
    shareHash: shareHash || "",
    lifecycleId: lifecycleId ?? null,
    operationRef: operationRef || null,
    anchorKey: choice?.key || null,
    anchorKind: choice?.kind || "fallback",
    offsetTop: Number.isFinite(choice?.offsetTop) ? choice.offsetTop : 0,
    candidates: choice?.backups || [],
    fallbackScrollTop: Number.isFinite(scroller.scrollTop) ? scroller.scrollTop : 0,
  };
  return rememberListAnchor(anchor);
}
