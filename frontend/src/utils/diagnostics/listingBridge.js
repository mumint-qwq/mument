// Current directory load context, shared between Files.vue and ListingView.vue
// without putting diagnostics state into Vue's reactive store.
let active = null;

export function setActiveLoad(context, meta) {
  active = context ? { context, meta } : null;
}

export function getActiveLoad() {
  return active;
}

export function getActiveLoadFor(req) {
  if (!active || !req) return null;
  if (req.source !== active.meta.source || req.path !== active.meta.path) return null;
  return active;
}

export function clearActiveLoad(context) {
  if (!active) return;
  if (!context || active.context === context) active = null;
}
