/** Incremental reader for GET /resources/list.
 *
 * Owns the cursor/revision pair, cancels the previous request when the listing is
 * reset, and ignores late responses from an older generation so a fast directory
 * switch cannot append the wrong items.
 */
export function createListingFeed(fetchPage, { limit = 100 } = {}) {
  let generation = 0;
  let controller = null;
  let options = null;

  const state = {
    items: [],
    cursor: "",
    revision: "",
    hasMore: false,
    loading: false,
    filtered: 0,
    pinnedItems: [],
    source: "",
    path: "",
  };

  async function request({ reset }) {
    if (!options) return state;
    const gen = generation;
    if (reset) {
      controller?.abort();
      controller = new AbortController();
      state.items = [];
      state.cursor = "";
      state.revision = "";
      state.filtered = 0;
      state.pinnedItems = [];
    }
    state.loading = true;
    try {
      const page = await fetchPage({
        ...options,
        limit,
        cursor: reset ? "" : state.cursor,
        revision: reset ? "" : state.revision,
      }, { signal: controller?.signal });
      if (gen !== generation) return state;
      const items = Array.isArray(page.items) ? page.items : [];
      state.items = reset ? items : [...state.items, ...items];
      state.cursor = page.nextCursor || "";
      state.revision = page.revision || state.revision;
      // A short page is not completion: access rules can filter rows, so the
      // server's hasMore flag and a cursor are authoritative.
      state.hasMore = Boolean(page.hasMore && state.cursor);
      state.filtered += Number(page.filtered) || 0;
      state.pinnedItems = Array.isArray(page.pinnedItems) ? page.pinnedItems : state.pinnedItems;
      return state;
    } finally {
      if (gen === generation) state.loading = false;
    }
  }

  async function reset(nextOptions) {
    generation += 1;
    options = nextOptions;
    state.source = nextOptions?.source || "";
    state.path = nextOptions?.path || "";
    return request({ reset: true });
  }

  async function loadMore() {
    if (!state.hasMore || state.loading) return state;
    return request({ reset: false });
  }

  function dispose() {
    generation += 1;
    controller?.abort();
    controller = null;
  }

  return { state, reset, loadMore, dispose, get generation() { return generation; } };
}
