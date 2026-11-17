/** Suppress background thumbnail work while a direct two-finger gesture is active. */
let depth = 0;
const listeners = new Set();

function notify() {
  const active = depth > 0;
  for (const listener of [...listeners]) listener(active);
}

export function beginInteraction() {
  depth += 1;
  notify();
}

export function endInteraction() {
  depth = Math.max(0, depth - 1);
  notify();
}

export function isInteractionActive() {
  return depth > 0;
}

/** Fires on every begin/end so callers can catch up after a gesture ends. */
export function onInteractionChange(listener) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** Test-only reset. */
export function resetInteractionGuard() {
  depth = 0;
  listeners.clear();
}
