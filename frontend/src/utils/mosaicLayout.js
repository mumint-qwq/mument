/** Proportional mosaic rows, with an explicit full-width single-column endpoint. */
export const MOSAIC_GAP = 8;
export const MOSAIC_PADDING = 2;
export const mosaicGap = density => normalizeMosaicDensity(density) === 1 ? MOSAIC_GAP : 4;
export const MOSAIC_MIN_FRACTION = 1 / 8;

export function normalizeMosaicDensity(value) {
  if (value === null || value === undefined || value === "") return 1;
  const number = Number(value);
  return Number.isFinite(number) ? Math.max(0, Math.min(1, number)) : 1;
}

export function densityToExtent(width, density) {
  return width * MOSAIC_MIN_FRACTION ** (1 - normalizeMosaicDensity(density));
}

export function extentToDensity(width, extent) {
  if (!(width > 0) || !(extent > 0)) return 1;
  return normalizeMosaicDensity(1 - Math.log(extent / width) / Math.log(MOSAIC_MIN_FRACTION));
}

// A decoded client value can correct uncommon formats; otherwise the server
// index gives first-visit layout a stable ratio before image bytes arrive.
export function resolveMosaicItemRatio(item, decodedRatio) {
  for (const value of [decodedRatio, item?.displayAspectRatio]) {
    if (Number.isFinite(value) && value > 0) return value;
  }
  return 1;
}

// Aves' row acceptance rule: if the current row would be too tall, keep
// accumulating items. Only the final row may be capped and left incomplete.
export function mosaicGeometry(items, width, { density = 1, gap = mosaicGap(density) } = {}) {
  if (!Number.isFinite(width) || width <= 0) return { boxes: {}, rows: [], height: 0 };
  const normalized = items.map(item => ({
    index: item.index,
    ratio: Number.isFinite(item.ratio) && item.ratio > 0 ? item.ratio : 1,
  }));
  const spacing = Number.isFinite(gap) && gap >= 0 ? gap : mosaicGap(density);
  const rows = [], boxes = [];
  let top = 0;
  const append = (row, height, complete) => {
    let left = 0;
    for (const item of row) {
      const itemWidth = height * item.ratio;
      boxes.push([item.index, { width: `${itemWidth}px`, height: `${height}px`,
        insetInlineStart: `${MOSAIC_PADDING + left}px`, top: `${top}px` }]);
      left += itemWidth + spacing;
    }
    rows.push({ indices: row.map(item => item.index), top, height, width: left - spacing, complete });
    top += height + spacing;
  };
  if (normalizeMosaicDensity(density) === 1) {
    for (const item of normalized) append([item], width / item.ratio, true);
  } else {
    const target = densityToExtent(width, density), heightMax = target * 2.4;
    let row = [], ratioSum = 0, ratioMin = Infinity;
    const flush = (complete, force = false) => {
      if (!row.length) return;
      const available = width - (row.length - 1) * spacing;
      const height = available / ratioSum;
      if (complete && !force && height > heightMax + 0.000001) return;
      append(row, complete ? height : Math.min(height, heightMax), complete);
      row = []; ratioSum = 0; ratioMin = Infinity;
    };
    for (const item of normalized) {
      const nextAvailable = width - row.length * spacing;
      const nextSum = ratioSum + item.ratio;
      const nextHeight = nextAvailable / nextSum;
      const nextMinWidth = nextHeight * Math.min(ratioMin, item.ratio);
      if (row.length && nextAvailable <= 0) flush(true, true);
      else if (row.length && (nextMinWidth < target || nextHeight < target)) flush(true);
      row.push(item);
      ratioSum += item.ratio;
      ratioMin = Math.min(ratioMin, item.ratio);
    }
    flush(false);
  }
  return { boxes: Object.fromEntries(boxes), rows, height: rows.length ? top - spacing : 0 };
}

// Keep the size-only API used by existing callers and baseline ratio checks.
export function mosaicLayout(items, width, options) {
  return Object.fromEntries(Object.entries(mosaicGeometry(items, width, options).boxes)
    .map(([index, box]) => [index, { width: box.width, height: box.height }]));
}
