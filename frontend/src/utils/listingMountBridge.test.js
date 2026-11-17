import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  cancelAllListingReveals,
  cancelListingRevealForTicket,
  pendingListingRevealCount,
  prepareListingRestore,
  registerListingMountController,
  revealListingTarget,
  settleListingReveal,
  unregisterListingMountController,
} from "./listingMountBridge.js";

describe("listing mount bridge completion contract", () => {
  let controller;
  let acceptedTarget;
  let cancelled;

  beforeEach(() => {
    acceptedTarget = null;
    cancelled = null;
    controller = {
      revealTarget(target) {
        acceptedTarget = target;
        return true;
      },
      cancelReveal(request) {
        cancelled = request;
      },
      prepareRestore(request) {
        this.prepared = request;
        return true;
      },
    };
    registerListingMountController(controller);
  });

  afterEach(() => {
    cancelAllListingReveals();
    unregisterListingMountController(controller);
    vi.useRealTimers();
  });

  it("does not call onSettled when the listing only accepted the request", () => {
    const settled = [];
    const accepted = revealListingTarget({ index: 4, ticket: "a1", onSettled: (result) => settled.push(result) });
    expect(accepted).toBe(true);
    expect(settled).toHaveLength(0);
    expect(pendingListingRevealCount()).toBe(1);
  });

  it("settles exactly once with the measured offsets", () => {
    const settled = [];
    revealListingTarget({ index: 4, ticket: "a1", onSettled: (result) => settled.push(result) });
    const requestId = acceptedTarget.requestId;
    expect(settleListingReveal(requestId, { result: "success", requestedOffset: 460, actualOffset: 460 })).toBe(true);
    expect(settleListingReveal(requestId, { result: "success" })).toBe(false);
    expect(settled).toEqual([expect.objectContaining({ result: "success", requestedOffset: 460, actualOffset: 460 })]);
  });

  it("cancels only the matching ticket and notifies the listing", () => {
    const one = [];
    const two = [];
    revealListingTarget({ index: 4, ticket: "a1", onSettled: (result) => one.push(result) });
    revealListingTarget({ index: 8, ticket: "a2", onSettled: (result) => two.push(result) });
    expect(cancelListingRevealForTicket("a1", { reason: "user_scroll" })).toBe(true);
    expect(one[0]).toMatchObject({ result: "cancelled", reason: "user_scroll" });
    expect(two).toHaveLength(0);
    expect(cancelled).toMatchObject({ ticket: "a1", reason: "user_scroll" });
    expect(pendingListingRevealCount()).toBe(1);
  });

  it("fails a request that never settles and cancels the actual reveal", () => {
    vi.useFakeTimers();
    const settled = [];
    revealListingTarget({ index: 4, ticket: "a1", onSettled: (result) => settled.push(result) });
    const requestId = acceptedTarget.requestId;
    vi.advanceTimersByTime(2600);
    expect(settled[0]).toMatchObject({ result: "failed", reason: "settle_timeout", requestId });
    expect(pendingListingRevealCount()).toBe(0);
    expect(cancelled).toMatchObject({ requestId, ticket: "a1", reason: "settle_timeout" });
  });

  it("cancels every controller request on page teardown", () => {
    const one = [];
    const two = [];
    revealListingTarget({ index: 4, ticket: "a1", onSettled: (result) => one.push(result) });
    revealListingTarget({ index: 8, ticket: "a2", onSettled: (result) => two.push(result) });
    const ids = [];
    const originalCancel = controller.cancelReveal.bind(controller);
    controller.cancelReveal = (request) => {
      ids.push(request.requestId);
      originalCancel(request);
    };
    cancelAllListingReveals({ reason: "unmounted" });
    expect(ids).toHaveLength(2);
    expect(one[0]).toMatchObject({ result: "cancelled", reason: "unmounted" });
    expect(two[0]).toMatchObject({ result: "cancelled", reason: "unmounted" });
    expect(pendingListingRevealCount()).toBe(0);
  });

  it("asks the listing to suppress the old preview target for this refresh", () => {
    expect(prepareListingRestore({ ticket: "a1" })).toBe(true);
    expect(controller.prepared).toEqual({ ticket: "a1" });
  });
});
