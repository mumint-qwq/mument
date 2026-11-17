/** One-shot viewport ticket for a listing refresh after a file operation.
 *
 * Deleting or moving files removes cards above/inside the viewport, so the
 * scroll offset alone cannot describe "where the user was". A ticket records
 * the topmost surviving card plus its screen offset, and a few bounded
 * neighbour identities in case that card disappears before the reload lands.
 *
 * The mailbox is deliberately tiny and non-reactive: the ticket is claimed by
 * exactly one request, then owned by that request until it reports a final
 * result. TTL only expires tickets nobody claimed; it is not a recovery
 * strategy. Paths stay in memory and never enter diagnostics events.
 */
import { getParentDir } from "./url.js";

export const LIST_ANCHOR_TTL_MS = 10_000;
export const MAX_BACKUP_ANCHORS = 5;

let pending = null;
let ticketCounter = 0;

/** Normalize a directory path only for comparisons (root stays "/"). */
export function normalizeListingPath(path) {
  if (!path) return "/";
  const value = String(path);
  const trimmed = value.replace(/\/+$/, "");
  return trimmed === "" ? "/" : trimmed;
}

/** Stable item identity: source + in-app path, never array index or name alone. */
export function listingItemKey(item, fallbackSource = "") {
  if (!item || typeof item !== "object") return "";
  const path = item.path || item.name || "";
  if (!path) return "";
  return `${item.source || fallbackSource || ""}|${path}`;
}

/** True when a reported operation result is about an entry of this directory. */
export function isEntryInDirectory(entry, source, path) {
  if (!entry || typeof entry.path !== "string" || !entry.path) return false;
  if (!entry.source || entry.source !== source) return false;
  return getParentDir(entry.path) === normalizeListingPath(path);
}

/**
 * Keys confirmed to have left this directory. Only successful results passed by
 * the caller are considered; failed entries stay eligible as anchors.
 */
export function removedListingKeys(removed, source, path) {
  const keys = new Set();
  for (const entry of Array.isArray(removed) ? removed : []) {
    if (!isEntryInDirectory(entry, source, path)) continue;
    keys.add(listingItemKey(entry, source));
  }
  return keys;
}

function backupKeys(keys, index, removedKeys, limit) {
  const out = [];
  const push = (position) => {
    if (out.length >= limit) return;
    // eslint-disable-next-line security/detect-object-injection -- numeric position in our own array
    const key = keys[position];
    if (!key || removedKeys.has(key) || out.includes(key)) return;
    out.push(key);
  };
  for (let i = index + 1; i < keys.length && out.length < limit; i += 1) push(i);
  for (let i = index - 1; i >= 0 && out.length < limit; i -= 1) push(i);
  return out;
}

/**
 * Pure anchor choice over visible entries sorted by (top, left).
 *
 * 1. topmost visible entry that is not confirmed moved out;
 * 2. otherwise the next survivor after the old visible block;
 * 3. otherwise the nearest previous survivor;
 * 4. no identity -> caller falls back to a clamped scroll offset.
 *
 * An off-screen neighbour is placed at the viewport top (offsetTop 0) instead
 * of preserving an offset that would keep it off-screen.
 */
export function chooseListingAnchor(entries, keys, removedKeys = new Set()) {
  const visible = Array.isArray(entries) ? entries : [];
  const order = Array.isArray(keys) ? keys : [];
  const survivors = visible.filter((entry) => entry?.key && !removedKeys.has(entry.key));
  if (survivors.length > 0) {
    const primary = survivors[0];
    return {
      key: primary.key,
      index: primary.index,
      offsetTop: primary.top,
      kind: "identity",
      backups: backupKeys(order, primary.index, removedKeys, MAX_BACKUP_ANCHORS),
    };
  }
  if (visible.length === 0 || order.length === 0) return null;
  let minIndex = Infinity;
  let maxIndex = -1;
  for (const entry of visible) {
    if (!Number.isInteger(entry?.index)) continue;
    minIndex = Math.min(minIndex, entry.index);
    maxIndex = Math.max(maxIndex, entry.index);
  }
  if (maxIndex < 0) return null;
  for (let i = maxIndex + 1; i < order.length; i += 1) {
    // eslint-disable-next-line security/detect-object-injection -- i is a bounded numeric index
    const key = order[i];
    if (!key || removedKeys.has(key)) continue;
    return {
      key,
      index: i,
      offsetTop: 0,
      kind: "neighbor_after",
      backups: backupKeys(order, i, removedKeys, MAX_BACKUP_ANCHORS),
    };
  }
  for (let i = minIndex - 1; i >= 0; i -= 1) {
    // eslint-disable-next-line security/detect-object-injection -- i is a bounded numeric index
    const key = order[i];
    if (!key || removedKeys.has(key)) continue;
    return {
      key,
      index: i,
      offsetTop: 0,
      kind: "neighbor_before",
      backups: backupKeys(order, i, removedKeys, MAX_BACKUP_ANCHORS),
    };
  }
  return null;
}

/** Publish a ticket. A newer operation replaces an unclaimed ticket. */
export function rememberListAnchor(anchor) {
  ticketCounter += 1;
  pending = {
    ...anchor,
    ticket: anchor?.ticket || `a${ticketCounter}`,
    createdAt: anchor?.createdAt || Date.now(),
  };
  return pending;
}

export function peekListAnchor() {
  return pending;
}

/**
 * Claim the ticket for one listing request. Returns null when the ticket is
 * stale, belongs to another directory, or to an older lifecycle (A -> B -> A).
 */
export function takeListAnchor({
  source,
  path,
  shareHash = "",
  lifecycleId = null,
  now = Date.now(),
} = {}) {
  if (!pending) return null;
  if (now - pending.createdAt > LIST_ANCHOR_TTL_MS) {
    pending = null;
    return null;
  }
  if (isListingLifecycleMismatch(pending, lifecycleId)) {
    pending = null;
    return null;
  }
  const sameDirectory =
    pending.source === source &&
    normalizeListingPath(pending.path) === normalizeListingPath(path) &&
    (pending.shareHash || "") === (shareHash || "");
  if (!sameDirectory) return null;
  const anchor = pending;
  pending = null;
  return anchor;
}

function isListingLifecycleMismatch(anchor, lifecycleId) {
  if (anchor.lifecycleId === null || anchor.lifecycleId === undefined) return false;
  if (lifecycleId === null || lifecycleId === undefined) return false;
  return anchor.lifecycleId !== lifecycleId;
}

/** Clear the unclaimed ticket, optionally only when it is the caller's own. */
export function clearListAnchor(ticket) {
  if (!pending) return;
  if (ticket === undefined || pending.ticket === ticket) pending = null;
}
