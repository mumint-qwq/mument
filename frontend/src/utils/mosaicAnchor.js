/** Preserve a point inside a stable DOM tile when rows change height or grouping. */
export function captureMosaicAnchor(root, point) {
  if (!root?.isConnected) return null;
  const rect = root.getBoundingClientRect();
  const focus = point || { x: rect.left + rect.width / 2, y: Math.min(window.innerHeight - 80, Math.max(120, window.innerHeight / 3)) };
  let element = document.elementFromPoint(focus.x, focus.y)?.closest('.listing-item');
  if (!element || !root.contains(element)) {
    let nearest = Infinity;
    element = null;
    for (const tile of root.querySelectorAll('.listing-item')) {
      const box = tile.getBoundingClientRect();
      if (box.bottom <= 80 || box.top >= window.innerHeight - 40) continue;
      const dx = Math.max(box.left - focus.x, 0, focus.x - box.right);
      const dy = Math.max(box.top - focus.y, 0, focus.y - box.bottom);
      const distance = dx * dx + dy * dy;
      if (distance < nearest) { nearest = distance; element = tile; }
    }
  }
  if (!element) return null;
  const box = element.getBoundingClientRect();
  const fraction = Math.max(0, Math.min(1, (focus.y - box.top) / Math.max(1, box.height)));
  return { element, fraction, screenY: box.top + box.height * fraction };
}
export function restoreMosaicAnchor(root, anchor) {
  if (!anchor?.element.isConnected || !root?.contains(anchor.element)) return;
  const box = anchor.element.getBoundingClientRect();
  const delta = box.top + box.height * anchor.fraction - anchor.screenY;
  const main = root.closest('#main');
  const scroller = main && main.scrollHeight > main.clientHeight ? main : document.scrollingElement;
  if (scroller && Number.isFinite(delta)) scroller.scrollTop += delta;
}
