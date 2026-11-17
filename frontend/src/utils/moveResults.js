/** Normalize a move/copy API response into confirmed file identities.
 *
 * The move endpoint returns `{ fromSource, fromPath, toSource, toPath, message }`
 * while the delete endpoint returns `{ source, path }`. This adapter is the
 * single place that understands the move shape; the anchor code only receives
 * normalized `{ source, path }` entries.
 *
 * HTTP 200 alone is never treated as proof that every requested item moved:
 * every requested identity must be echoed exactly once by the server. Missing,
 * duplicated, contradictory or foreign results produce `unknown` and only the
 * confirmed entries are counted. Public shares may omit identities entirely;
 * then only an unambiguous all-succeeded/all-failed count is trusted.
 */

function requestKey(source, path) {
  return `${source || ""}|${path}`;
}

function normalizeRequested(requested) {
  const entries = [];
  const seen = new Map();
  for (const item of Array.isArray(requested) ? requested : []) {
    const source = item?.source || "";
    const path = item?.path || "";
    if (!path) continue;
    const key = requestKey(source, path);
    const count = (seen.get(key) || 0) + 1;
    seen.set(key, count);
    entries.push({ source, path, key });
  }
  return { entries, duplicatedRequests: [...seen.values()].some((count) => count > 1) };
}

function resultEntry(entry, fallbackSource, trustFallbackSource) {
  if (!entry || typeof entry !== "object" || Array.isArray(entry)) return null;
  const path = typeof entry.fromPath === "string" ? entry.fromPath : "";
  if (!path) return null;
  const source = typeof entry.fromSource === "string" && entry.fromSource
    ? entry.fromSource
    : trustFallbackSource
      ? (fallbackSource || "")
      : "";
  if (!source) return null;
  return {
    source,
    path,
    message: typeof entry.message === "string" && entry.message ? entry.message : "",
  };
}

export function adaptMoveCopyResult({
  requested = [],
  response = null,
  fallbackSource = "",
  isShare = false,
} = {}) {
  const { entries: ask, duplicatedRequests } = normalizeRequested(requested);
  const requestedByKey = new Map(ask.map((entry) => [entry.key, entry]));
  const succeededRaw = Array.isArray(response?.succeeded) ? response.succeeded : null;
  const failedRaw = Array.isArray(response?.failed) ? response.failed : null;

  if (!succeededRaw || !failedRaw) {
    return {
      outcome: "unknown",
      confirmedSucceeded: [],
      confirmedFailed: [],
      confirmedSucceededCount: 0,
      confirmedFailedCount: 0,
      unmatchedRequests: ask.length,
      failureMessage: "",
      contradictory: Boolean(response),
    };
  }

  const unidentifiable = [...succeededRaw, ...failedRaw].some((entry) => !entry?.fromPath);
  if (isShare && unidentifiable) {
    return adaptCountOnlyShare(ask, succeededRaw, failedRaw, duplicatedRequests);
  }

  const succeededKeys = new Map();
  const failedKeys = new Map();
  const confirmedSucceeded = [];
  const confirmedFailed = [];
  let contradictory = duplicatedRequests || succeededRaw.length + failedRaw.length !== ask.length;
  let foreign = false;

  const classify = (raw, bucket, list) => {
    for (const rawEntry of raw) {
      const normalized = resultEntry(rawEntry, fallbackSource, isShare);
      if (!normalized) {
        contradictory = true;
        continue;
      }
      const key = requestKey(normalized.source, normalized.path);
      const request = requestedByKey.get(key);
      if (!request) {
        foreign = true;
        contradictory = true;
        continue;
      }
      if (bucket.has(key)) {
        // Same identity reported twice: cannot count it twice.
        contradictory = true;
        continue;
      }
      if ((bucket === succeededKeys ? failedKeys : succeededKeys).has(key)) {
        // Present on both sides: keep both so the clean-up below can drop it
        // from both sets instead of silently preferring success.
        contradictory = true;
      }
      bucket.set(key, normalized);
      list.push(normalized);
    }
  };
  classify(succeededRaw, succeededKeys, confirmedSucceeded);
  classify(failedRaw, failedKeys, confirmedFailed);

  // Entries appearing on both sides are contradictory, not successes.
  const both = [...succeededKeys.keys()].filter((key) => failedKeys.has(key));
  if (both.length > 0) contradictory = true;
  const cleanSucceeded = confirmedSucceeded.filter((entry) => {
    const key = requestKey(entry.source, entry.path);
    return !failedKeys.has(key);
  });
  const cleanFailed = confirmedFailed.filter((entry) => {
    const key = requestKey(entry.source, entry.path);
    return !succeededKeys.has(key);
  });

  const matched = new Set([...cleanSucceeded, ...cleanFailed].map((entry) => requestKey(entry.source, entry.path)));
  const missing = ask.filter((entry) => !matched.has(entry.key)).length;
  if (missing > 0) contradictory = true;

  let outcome;
  if (contradictory || foreign || missing > 0) {
    outcome = "unknown";
  } else if (cleanSucceeded.length === ask.length) {
    outcome = "ok";
  } else if (cleanFailed.length === ask.length) {
    outcome = "failed";
  } else if (cleanSucceeded.length > 0 && cleanFailed.length > 0) {
    outcome = "partial";
  } else {
    outcome = "unknown";
  }

  return {
    outcome,
    confirmedSucceeded: cleanSucceeded,
    confirmedFailed: cleanFailed,
    confirmedSucceededCount: cleanSucceeded.length,
    confirmedFailedCount: cleanFailed.length,
    unmatchedRequests: missing,
    failureMessage: cleanFailed.find((entry) => entry.message)?.message || "",
    contradictory,
  };
}

/** Public shares strip paths; trust only a complete, unambiguous count split. */
function adaptCountOnlyShare(ask, succeededRaw, failedRaw, duplicatedRequests) {
  const succeeded = succeededRaw.length;
  const failed = failedRaw.length;
  const complete = !duplicatedRequests && succeeded + failed === ask.length;
  const base = {
    confirmedSucceeded: [],
    confirmedFailed: [],
    confirmedSucceededCount: succeeded,
    confirmedFailedCount: failed,
    unmatchedRequests: complete ? 0 : Math.max(0, ask.length - succeeded - failed),
    failureMessage: "",
    countOnly: true,
  };
  if (!complete) {
    return { ...base, outcome: "unknown", contradictory: true };
  }
  if (succeeded === ask.length && failed === 0) {
    // The server explicitly reports that every requested item succeeded; only
    // the paths are stripped, so the request identities are still trustworthy.
    return {
      ...base,
      outcome: "ok",
      confirmedSucceeded: ask.map((entry) => ({ source: entry.source, path: entry.path })),
    };
  }
  if (failed === ask.length && succeeded === 0) {
    return { ...base, outcome: "failed" };
  }
  // Counts are known but identities are not: each item cannot be verified.
  return { ...base, outcome: "unknown", contradictory: false };
}
