/** Opt-in switch for the paged listing UI (disabled by default).
 *
 * Enable with localStorage or, for phone testing where a console is awkward,
 * with ?pagedListing=1 in the URL (persisted). ?pagedListing=0 turns it back off.
 */
const STORAGE_KEY = "quantum.pagedListing";

function urlParam() {
  try {
    return new URLSearchParams(globalThis.location?.search || "").get("pagedListing");
  } catch (_error) {
    return null;
  }
}

export function isPagedListingEnabled() {
  const param = urlParam();
  try {
    if (param === "1") {
      globalThis.localStorage?.setItem(STORAGE_KEY, "1");
      return true;
    }
    if (param === "0") {
      globalThis.localStorage?.removeItem(STORAGE_KEY);
      return false;
    }
    return globalThis.localStorage?.getItem(STORAGE_KEY) === "1";
  } catch (_error) {
    return param === "1";
  }
}
