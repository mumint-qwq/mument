/* eslint-disable security/detect-object-injection -- row/group indices come from our own layout data */
/** Pure helpers for the progressive mosaic mount plan (see PLAN-PROGRESSIVE-CARDS). */

/**
 * Rows whose vertical range intersects [start, end) in group-local pixels.
 * Rows are sorted by `top`, so this stops at the first row below the band.
 */
export function rowsInBand(rows, start, end) {
  const picked = [];
  if (!Array.isArray(rows) || !(end > start)) return picked;
  for (let rowIndex = 0; rowIndex < rows.length; rowIndex += 1) {
    const row = rows[rowIndex];
    if (row.top + row.height <= start) continue;
    if (row.top >= end) break;
    picked.push({ rowIndex, row });
  }
  return picked;
}

/** Binary search: index -> row position inside a group's row array. */
export function findRowIndexForItem(rows, index) {
  let low = 0;
  let high = rows.length - 1;
  while (low <= high) {
    const mid = (low + high) >> 1;
    const row = rows[mid];
    if (index < row.indices[0]) high = mid - 1;
    else if (index > row.indices[row.indices.length - 1]) low = mid + 1;
    else return mid;
  }
  return -1;
}

/**
 * Unmounted rows ordered by their distance to the current viewport centre.
 * `centers` maps group name -> centre in that group's local coordinates.
 */
export function buildRowQueue(groups, centers, isRowMounted) {
  const queue = [];
  for (const [group, rows] of Object.entries(groups)) {
    const center = Number.isFinite(centers[group]) ? centers[group] : 0;
    for (let rowIndex = 0; rowIndex < rows.length; rowIndex += 1) {
      if (isRowMounted(group, rowIndex)) continue;
      const row = rows[rowIndex];
      const distance = Math.abs(row.top + row.height / 2 - center);
      queue.push({ group, rowIndex, distance });
    }
  }
  queue.sort((a, b) => a.distance - b.distance);
  return queue;
}

/**
 * Take whole rows from the queue until the item budget is reached. A row is
 * never split: mosaic rows must appear as a unit to avoid gaps.
 */
export function takeBatch(queue, groups, isRowMounted, maxItems) {
  const picked = [];
  let count = 0;
  for (const entry of queue) {
    if (isRowMounted(entry.group, entry.rowIndex)) continue;
    const row = groups[entry.group][entry.rowIndex];
    if (!row) continue;
    picked.push(entry);
    count += row.indices.length;
    if (count >= maxItems) break;
  }
  return picked;
}
