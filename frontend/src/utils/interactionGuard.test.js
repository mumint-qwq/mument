import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  beginInteraction,
  endInteraction,
  isInteractionActive,
  onInteractionChange,
  resetInteractionGuard,
} from "./interactionGuard.js";

describe("interaction guard", () => {
  beforeEach(() => resetInteractionGuard());

  it("stays active through nested gestures and only clears at the end", () => {
    expect(isInteractionActive()).toBe(false);
    beginInteraction();
    beginInteraction();
    expect(isInteractionActive()).toBe(true);
    endInteraction();
    expect(isInteractionActive()).toBe(true);
    endInteraction();
    expect(isInteractionActive()).toBe(false);
  });

  it("never drops below zero", () => {
    endInteraction();
    expect(isInteractionActive()).toBe(false);
  });

  it("notifies subscribers when the gesture ends", () => {
    const listener = vi.fn();
    const unsubscribe = onInteractionChange(listener);
    beginInteraction();
    beginInteraction();
    endInteraction();
    endInteraction();
    expect(listener.mock.calls.map(([active]) => active)).toEqual([true, true, true, false]);
    unsubscribe();
    beginInteraction();
    endInteraction();
    expect(listener).toHaveBeenCalledTimes(4);
  });
});
