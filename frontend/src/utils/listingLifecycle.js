/** Identity of the listing load currently on screen.
 *
 * File operations finish asynchronously. A result captured while the user was
 * looking at directory A must not refresh or scroll directory B, and an A -> B
 * -> A round trip is a *new* lifecycle even though source/path look the same.
 * The id changes when the visible route/view changes (A -> B -> A is a new
 * lifecycle), but a same-directory reload keeps it: creating a folder in the
 * move dialog must not make its later move look like an operation from an old
 * page. Response staleness is a separate concern owned by the request
 * generation (_requestSeq in Files.vue).
 *
 * This is business state: diagnostics may be off and must never decide whether
 * a late operation result is still allowed to touch the page.
 */
let sequence = 0;
let current = null;

/** Start a listing request. The view id changes only when the route identity changes. */
export function beginListingLifecycle({ routeKey = null } = {}) {
  const nextRouteKey = routeKey ?? current?.routeKey ?? null;
  const sameView = Boolean(current) &&
    (routeKey === null || current.routeKey === routeKey);
  if (!sameView) sequence += 1;
  current = Object.freeze({
    id: sameView ? current.id : sequence,
    source: current?.source || "",
    path: current?.path || "/",
    shareHash: current?.shareHash || "",
    view: current?.view || "unknown",
    routeKey: nextRouteKey,
  });
  return current;
}

/** Attach the identity of the data that was actually stored for this lifecycle. */
export function updateListingLifecycleIdentity({ source = "", path = "/", shareHash = "", view = "listingView" } = {}) {
  if (!current) {
    sequence += 1;
    current = null;
  }
  current = Object.freeze({
    id: current?.id ?? sequence,
    source: source || "",
    path: path || "/",
    shareHash: shareHash || "",
    view: view || "listingView",
    routeKey: current?.routeKey ?? null,
  });
  return current;
}

export function getListingLifecycle() {
  return current;
}

export function currentListingLifecycleId() {
  return current?.id ?? 0;
}

export function isListingLifecycle(id) {
  return Boolean(id) && current?.id === id;
}

/** Leaving the page invalidates work started against it. */
export function invalidateListingLifecycle() {
  sequence += 1;
  current = null;
}

export function resetListingLifecycleForTests() {
  sequence = 0;
  current = null;
}
