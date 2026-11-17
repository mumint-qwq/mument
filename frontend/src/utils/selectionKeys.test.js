import { describe, expect, it } from "vitest";
import {
  indicesForKeys,
  keysForIndices,
  reconcileSelection,
  selectedKeysFor,
  selectionKey,
} from "./selectionKeys.js";

const item = (source, path) => ({ source, path, name: path.split("/").pop() });

describe("selection keys", () => {
  it("distinguishes the same path in different sources", () => {
    expect(selectionKey(item("a", "/p.jpg"))).toBe("a|/p.jpg");
    expect(selectionKey(item("b", "/p.jpg"))).not.toBe(selectionKey(item("a", "/p.jpg")));
  });

  it("derives keys from indices and object selections", () => {
    const items = [item("s", "/a"), item("s", "/b"), item("s", "/c")];
    expect(keysForIndices(items, [0, 2])).toEqual(["s|/a", "s|/c"]);
    expect(selectedKeysFor(items, [1, { source: "s", path: "/c" }])).toEqual(["s|/b", "s|/c"]);
  });

  it("resolves keys after the listing is re-sorted", () => {
    const before = [item("s", "/a"), item("s", "/b"), item("s", "/c")];
    const keys = keysForIndices(before, [1]);
    const after = [item("s", "/c"), item("s", "/b"), item("s", "/a")];
    expect(indicesForKeys(after, keys)).toEqual([1]);
  });

  it("drops keys whose item disappeared", () => {
    const before = [item("s", "/a"), item("s", "/b")];
    const keys = keysForIndices(before, [0, 1]);
    const after = [item("s", "/b")];
    expect(reconcileSelection(after, keys)).toEqual({ indices: [0], keys: ["s|/b"] });
  });

  it("keeps duplicate paths unique", () => {
    const items = [item("s", "/a"), item("s", "/a")];
    expect(keysForIndices(items, [0, 1])).toEqual(["s|/a"]);
  });
});
