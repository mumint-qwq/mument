import { beforeEach, describe, expect, it, vi } from "vitest";

// The shared test setup mocks the store; this suite exercises the real mutations.
vi.unmock("@/store");

import { state, mutations } from "@/store";

const file = (name) => ({ name, path: `/${name}`, source: "s", type: "text/plain", size: 1 });
const listing = (items, path = "/", source = "s") => ({
  name: "dir", path, source, type: "directory",
  items: items.map((item, index) => ({ ...item, index })),
});

describe("selection across listing refreshes", () => {
  beforeEach(() => {
    state.route = { path: "/files", query: {}, params: {}, hash: "" };
    state.user = { ...(state.user || {}), username: "tester", sorting: { by: "name", asc: true } };
    state.req = { name: "dir", path: "/", source: "s", type: "directory", items: [] };
    state.selected = [];
    state.selectedKeys = [];
  });

  it("keeps the selected item after a same-directory refresh", () => {
    const items = [file("a.txt"), file("b.txt"), file("c.txt")];
    mutations.replaceRequest(listing(items));
    mutations.addSelected(1);
    mutations.replaceRequest(listing([file("a.txt"), file("b.txt"), file("c.txt")]));
    expect(state.selectedKeys).toEqual(["s|/b.txt"]);
    expect(state.selected).toEqual([1]);
  });

  it("drops a selection whose item disappeared", () => {
    mutations.replaceRequest(listing([file("a.txt"), file("b.txt")]));
    mutations.addSelected(1);
    mutations.replaceRequest(listing([file("a.txt")]));
    expect(state.selected).toEqual([]);
    expect(state.selectedKeys).toEqual([]);
  });

  it("clears the selection when navigating to another directory", () => {
    mutations.replaceRequest(listing([file("a.txt")]));
    mutations.addSelected(0);
    mutations.replaceRequest(listing([file("a.txt")], "/sub/"));
    expect(state.selected).toEqual([]);
    expect(state.selectedKeys).toEqual([]);
  });

  it("re-maps keys when items are re-ordered", () => {
    const items = [file("a.txt"), file("b.txt"), file("c.txt")];
    mutations.replaceRequest(listing(items));
    mutations.addSelected(1);
    mutations.reconcileSelection([file("c.txt"), file("b.txt"), file("a.txt")]);
    expect(state.selected).toEqual([1]);
    expect(state.selectedKeys).toEqual(["s|/b.txt"]);
  });
});
