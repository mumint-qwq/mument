import { beforeEach, describe, expect, it } from "vitest";
import {
  beginListingLifecycle,
  currentListingLifecycleId,
  getListingLifecycle,
  invalidateListingLifecycle,
  isListingLifecycle,
  resetListingLifecycleForTests,
  updateListingLifecycleIdentity,
} from "./listingLifecycle.js";

describe("listing lifecycle vs request generation", () => {
  beforeEach(() => {
    resetListingLifecycleForTests();
  });

  it("keeps the same view id when the same route refreshes", () => {
    const first = beginListingLifecycle({ routeKey: "/files/pictures/a" });
    updateListingLifecycleIdentity({ source: "pictures", path: "/a", view: "listingView" });
    const second = beginListingLifecycle({ routeKey: "/files/pictures/a" });
    expect(second.id).toBe(first.id);
    expect(currentListingLifecycleId()).toBe(first.id);
  });

  it("changes the view id for A -> B -> A even though the path matches A", () => {
    const a1 = beginListingLifecycle({ routeKey: "/files/pictures/a" });
    updateListingLifecycleIdentity({ source: "pictures", path: "/a" });
    const b = beginListingLifecycle({ routeKey: "/files/pictures/b" });
    updateListingLifecycleIdentity({ source: "pictures", path: "/b" });
    const a2 = beginListingLifecycle({ routeKey: "/files/pictures/a" });
    expect(new Set([a1.id, b.id, a2.id]).size).toBe(3);
    expect(isListingLifecycle(a1.id)).toBe(false);
    expect(isListingLifecycle(a2.id)).toBe(true);
  });

  it("keeps the id while attaching the stored data identity", () => {
    const started = beginListingLifecycle({ routeKey: "/files/pictures/a" });
    updateListingLifecycleIdentity({ source: "pictures", path: "/a" });
    expect(getListingLifecycle()).toMatchObject({
      id: started.id, source: "pictures", path: "/a", routeKey: "/files/pictures/a",
    });
    expect(currentListingLifecycleId()).toBe(started.id);
  });

  it("invalidates work when the page goes away", () => {
    const started = beginListingLifecycle({ routeKey: "/files/pictures/a" });
    invalidateListingLifecycle();
    expect(isListingLifecycle(started.id)).toBe(false);
    expect(getListingLifecycle()).toBe(null);
  });
});
