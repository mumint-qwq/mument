import { densityToExtent, extentToDensity, MOSAIC_MIN_FRACTION } from './mosaicLayout.js';
import { beginInteraction, endInteraction } from './interactionGuard.js';

const interactions = new WeakMap();
const SUPPRESS_MS = 550;
export function isMosaicInteractionBlocked(target) {
  const element = target instanceof Element ? target : target?.parentElement;
  const root = element?.closest('.listing-view-root');
  const state = root && interactions.get(root);
  return Boolean(state && (state.active || state.draining || performance.now() < state.until));
}

/** Own only two-finger gestures that begin inside the mosaic; leave single-finger scroll native. */
export function attachMosaicPinch(root, { isEnabled, getDensity, getWidth, onStart, onPreview, onCommit, onCancel }) {
  const doc = root.ownerDocument;
  const win = doc.defaultView;
  const state = { active: false, draining: false, until: 0 };
  interactions.set(root, state);
  let gesture = null;
  let frame = 0;
  const consume = event => {
    if (event.cancelable) event.preventDefault();
    event.stopImmediatePropagation();
  };
  const pair = touches => {
    const points = gesture.ids.map(id => Array.from(touches).find(t => t.identifier === id));
    return points.every(Boolean) ? points : null;
  };
  const measure = points => ({
    distance: Math.hypot(points[1].clientX - points[0].clientX, points[1].clientY - points[0].clientY),
    center: { x: (points[0].clientX + points[1].clientX) / 2, y: (points[0].clientY + points[1].clientY) / 2 },
  });
  const preview = () => {
    frame = 0;
    if (gesture) onPreview({ density: gesture.density, center: gesture.center });
  };
  const finish = (commit, remaining = 0) => {
    if (!gesture) return;
    if (frame) { win.cancelAnimationFrame(frame); frame = 0; }
    const density = gesture.density;
    if (gesture.guarded) {
      gesture.guarded = false;
      endInteraction();
    }
    gesture = null;
    state.active = false;
    state.draining = remaining > 0;
    state.until = performance.now() + SUPPRESS_MS;
    root.removeAttribute('data-mosaic-pinching');
    if (commit) onCommit(density);
    else onCancel();
  };
  const start = event => {
    if (state.draining) { consume(event); return; }
    if (!isEnabled() || event.touches.length < 2 || !event.target.closest?.('.listing-items.mosaic')) return;
    consume(event);
    if (gesture) return;
    const initial = measure(Array.from(event.touches).slice(0, 2));
    const width = getWidth();
    if (!(width > 0) || initial.distance < 8) return;
    const density = getDensity();
    gesture = { ids: Array.from(event.touches).slice(0, 2).map(t => t.identifier),
      width, startDistance: initial.distance, startExtent: densityToExtent(width, density),
      density, center: initial.center };
    state.active = true;
    gesture.guarded = true;
    beginInteraction();
    root.setAttribute('data-mosaic-pinching', 'true');
    onStart(initial.center);
    preview();
  };
  const move = event => {
    if (state.draining) { consume(event); return; }
    if (!gesture) return;
    consume(event);
    const points = pair(event.touches);
    if (!points) { finish(false, event.touches.length); return; }
    const measured = measure(points);
    const extent = Math.max(gesture.width * MOSAIC_MIN_FRACTION,
      Math.min(gesture.width, gesture.startExtent * measured.distance / gesture.startDistance));
    gesture.density = extentToDensity(gesture.width, extent);
    gesture.center = measured.center;
    if (!frame) frame = win.requestAnimationFrame(preview);
  };
  const end = event => {
    if (gesture) {
      consume(event);
      if (event.type === 'touchcancel') finish(false, event.touches.length);
      else if (event.touches.length < 2) finish(true, event.touches.length);
      else if (!pair(event.touches)) finish(false, event.touches.length);
    } else if (state.draining) {
      consume(event);
      if (event.touches.length === 0) { state.draining = false; state.until = performance.now() + SUPPRESS_MS; }
    }
  };
  const blockClick = event => {
    if (root.contains(event.target) && isMosaicInteractionBlocked(event.target)) consume(event);
  };
  const cancel = () => { finish(false); state.draining = false; };
  const pointerCancel = event => { if (event.pointerType === 'touch' && gesture) cancel(); };
  const visibility = () => { if (doc.hidden) cancel(); };
  const activeOptions = { capture: true, passive: false };
  root.addEventListener('touchstart', start, activeOptions);
  doc.addEventListener('touchmove', move, activeOptions);
  doc.addEventListener('touchend', end, activeOptions);
  doc.addEventListener('touchcancel', end, activeOptions);
  for (const type of ['click', 'dblclick', 'contextmenu', 'mousedown']) doc.addEventListener(type, blockClick, true);
  win.addEventListener('blur', cancel);
  win.addEventListener('resize', cancel);
  doc.addEventListener('visibilitychange', visibility);
  doc.addEventListener('pointercancel', pointerCancel, true);
  return {
    cancel,
    destroy() {
      cancel();
      root.removeEventListener('touchstart', start, true);
      doc.removeEventListener('touchmove', move, true);
      doc.removeEventListener('touchend', end, true);
      doc.removeEventListener('touchcancel', end, true);
      for (const type of ['click', 'dblclick', 'contextmenu', 'mousedown']) doc.removeEventListener(type, blockClick, true);
      win.removeEventListener('blur', cancel);
      win.removeEventListener('resize', cancel);
      doc.removeEventListener('visibilitychange', visibility);
      doc.removeEventListener('pointercancel', pointerCancel, true);
      interactions.delete(root);
    },
  };
}
