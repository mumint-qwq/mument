import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  deviceLabelFrom,
  generateDeviceId,
  getShortDeviceId,
  isValidDeviceId,
  readStoredDeviceId,
  resetDeviceIdentityForTests,
  resolveDeviceId,
  resolveDeviceIdForSave,
} from "@/utils/deviceIdentity.js";

const STORAGE_KEY = "quantumDeviceId";
const ID_A = "11111111-2222-4333-8444-555555555555";
const ID_B = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee";

describe("device identity", () => {
  beforeEach(() => {
    localStorage.clear();
    resetDeviceIdentityForTests();
  });
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    resetDeviceIdentityForTests();
  });

  it("generates an ID accepted by the backend validator", () => {
    const id = generateDeviceId();
    expect(isValidDeviceId(id)).toBe(true);
    expect(id.length).toBeGreaterThanOrEqual(8);
    expect(id.length).toBeLessThanOrEqual(64);
  });

  it("persists and reuses one ID for the same browser storage", () => {
    const first = resolveDeviceId();
    const second = resolveDeviceId();
    expect(first.persistent).toBe(true);
    expect(first.id).toBe(second.id);
    expect(localStorage.getItem(STORAGE_KEY)).toBe(first.id);
  });

  it("replaces a malformed stored ID", () => {
    localStorage.setItem(STORAGE_KEY, "bad id with spaces");
    const resolved = resolveDeviceId();
    expect(isValidDeviceId(resolved.id)).toBe(true);
    expect(localStorage.getItem(STORAGE_KEY)).toBe(resolved.id);
  });

  it("rejects reserved keys and unsafe IDs", () => {
    expect(isValidDeviceId("__proto__")).toBe(false);
    expect(isValidDeviceId("Prototype")).toBe(false);
    expect(isValidDeviceId("constructor")).toBe(false);
    expect(isValidDeviceId("short")).toBe(false);
    expect(isValidDeviceId("has space here")).toBe(false);
    expect(isValidDeviceId("a".repeat(65))).toBe(false);
    expect(isValidDeviceId("a".repeat(8))).toBe(true);
  });

  it("falls back to a stable page-only ID when storage writes fail", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    const first = resolveDeviceId();
    const second = resolveDeviceId();
    expect(first.persistent).toBe(false);
    expect(first.id).toBeTruthy();
    expect(second.id).toBe(first.id);
    expect(readStoredDeviceId()).toBeNull();
  });

  it("uses a page-only ID when reading storage throws", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    const resolved = resolveDeviceId();
    expect(resolved.persistent).toBe(false);
    expect(resolved.id).toBeTruthy();
  });

  it("does not invent an ID without Web Crypto", () => {
    vi.stubGlobal("crypto", undefined);
    expect(generateDeviceId()).toBeNull();
    const resolved = resolveDeviceId();
    expect(resolved).toEqual({ id: null, persistent: false });
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull();
  });

  it("adopts a persisted ID replaced by another tab before saving", () => {
    localStorage.setItem(STORAGE_KEY, ID_A);
    expect(resolveDeviceId().id).toBe(ID_A);
    localStorage.setItem(STORAGE_KEY, ID_B);
    const forSave = resolveDeviceIdForSave(ID_A);
    expect(forSave.id).toBe(ID_B);
    expect(forSave.persistent).toBe(true);
  });

  it("persists the current ID before saving when storage is still empty", () => {
    const forSave = resolveDeviceIdForSave(ID_A);
    expect(forSave).toEqual({ id: ID_A, persistent: true });
    expect(localStorage.getItem(STORAGE_KEY)).toBe(ID_A);
  });

  it("labels desktop and mobile clients", () => {
    expect(
      deviceLabelFrom(
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0.0.0 Safari/537.36",
        "Windows"
      )
    ).toBe("Chrome · Windows");
    expect(
      deviceLabelFrom(
        "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1"
      )
    ).toBe("Safari · iOS");
    expect(
      deviceLabelFrom(
        "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/120.0.0.0 Mobile Safari/537.36"
      )
    ).toBe("Chrome · Android");
  });

  it("strips control characters from labels", () => {
    expect(deviceLabelFrom("Chrome/120\u0007", "Linux")).toBe("Chrome · Linux");
  });

  it("shortens long IDs for display but keeps short ones", () => {
    expect(getShortDeviceId(ID_A)).toBe("11111111…");
    expect(getShortDeviceId("short-id")).toBe("short-id");
    expect(getShortDeviceId("")).toBe("");
  });
});
