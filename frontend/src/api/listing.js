import { fetchURL } from "@/api/utils";
import { getApiPath, getPublicApiPath } from "@/utils/url";

/** The server does not expose GET /resources/list (older build) — caller falls back. */
export class ListingUnsupportedError extends Error {
  constructor(message = "Paged listing is not supported by this server") {
    super(message);
    this.name = "ListingUnsupportedError";
  }
}

/** The directory changed between pages; the caller must restart the listing. */
export class ListingRevisionError extends Error {
  constructor(message = "Directory changed while paging") {
    super(message);
    this.name = "ListingRevisionError";
    this.status = 409;
  }
}

function listingParams({ path, sort = "name", order = "asc", limit = 100, cursor = "", revision = "" }) {
  const params = { path, sort, order, limit };
  if (cursor) params.cursor = cursor;
  if (revision) params.revision = revision;
  return params;
}

/**
 * Fetch one page of a directory listing.
 * @returns {Promise<{items: object[], nextCursor?: string, revision: string, hasMore: boolean, filtered?: number, pinnedItems?: string[], source?: string, name?: string, type?: string}>}
 */
export async function fetchListingPage(opts, { signal, fetchImpl = fetchURL } = {}) {
  const {
    source, path, isShare = false, hash = "", password = "",
    sort = "name", order = "asc", limit = 100, cursor = "", revision = "",
  } = opts;
  const params = listingParams({ path, sort, order, limit, cursor, revision });
  const url = isShare
    ? getPublicApiPath("resources/list", { ...params, hash })
    : getApiPath("resources/list", { ...params, source });
  const headers = {};
  if (isShare && password) {
    headers["X-SHARE-PASSWORD"] = encodeURIComponent(password);
  }
  let response;
  try {
    response = await fetchImpl(url, { headers, signal });
  } catch (error) {
    if (error?.name === "AbortError") throw error;
    if (error?.status === 404 || error?.status === 405) throw new ListingUnsupportedError();
    if (error?.status === 409) throw new ListingRevisionError();
    throw error;
  }
  return response.json();
}
