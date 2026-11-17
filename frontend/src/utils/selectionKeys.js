/** Stable identity for list selections.
 *
 * Array indices drift whenever a listing is re-sorted, refreshed, appended by a
 * page, or loses a deleted row. A selection key is the source plus the item
 * path, which survives all of those as long as the item still exists.
 */
export function selectionKey(item) {
  if (!item || typeof item !== "object") return "";
  const path = item.path || item.name || "";
  if (!path) return "";
  return `${item.source || ""}|${path}`;
}

export function keysForIndices(items, indices) {
  if (!Array.isArray(items) || !Array.isArray(indices)) return [];
  const keys = [];
  for (const index of indices) {
    const item = items.at(index);
    const key = selectionKey(item);
    if (key && !keys.includes(key)) keys.push(key);
  }
  return keys;
}

export function selectedKeysFor(items, selected) {
  if (!Array.isArray(selected)) return [];
  const keys = [];
  for (const entry of selected) {
    const key = typeof entry === "number"
      ? selectionKey(items.at(entry))
      : selectionKey(entry);
    if (key && !keys.includes(key)) keys.push(key);
  }
  return keys;
}

export function indicesForKeys(items, keys) {
  if (!Array.isArray(items) || !Array.isArray(keys) || keys.length === 0) return [];
  const lookup = new Map();
  items.forEach((item, index) => {
    const key = selectionKey(item);
    if (key && !lookup.has(key)) lookup.set(key, index);
  });
  const indices = [];
  for (const key of keys) {
    const index = lookup.get(key);
    if (index !== undefined) indices.push(index);
  }
  return indices;
}

/** Keep only the keys that still exist, and return the matching indices. */
export function reconcileSelection(items, keys) {
  const indices = indicesForKeys(items, keys);
  const surviving = [];
  for (const index of indices) {
    const key = selectionKey(items.at(index));
    if (key) surviving.push(key);
  }
  return { indices, keys: surviving };
}
