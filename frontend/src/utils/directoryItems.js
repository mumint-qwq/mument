import { joinPath } from "@/utils/url.js";

/**
 * Converts the narrow /resources/items response into the item shape used by
 * FileList and FileTree. The endpoint deliberately returns names only, so this
 * helper is the single place that restores stable directory paths.
 */
export function folderItemsFromResponse(response, source, path = "/") {
  const names = Array.isArray(response?.folders) ? response.folders : [];
  return names.map((name) => ({
    name,
    path: `${joinPath(path || "/", name)}/`,
    source,
    type: "directory",
  }));
}

export function folderListingFromResponse(response, source, path = "/") {
  return {
    path: path || "/",
    source,
    type: "directory",
    items: folderItemsFromResponse(response, source, path),
  };
}
