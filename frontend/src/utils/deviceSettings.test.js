import { describe, expect, it } from "vitest";
import {
  buildDeviceEntry,
  buildDevicePatchUser,
  computeEffectiveSingleClick,
  getDeviceEntry,
  getSingleClickOverride,
  hasSingleClickOverride,
  isSafeDeviceId,
  isSingleClickEnforced,
} from "@/utils/deviceSettings.js";

const DEVICE_ID = "11111111-2222-4333-8444-555555555555";

function makeUser(overrides = {}) {
  return {
    username: "alice",
    singleClick: false,
    ...overrides,
  };
}

describe("device singleClick helpers", () => {
  it("reads only a boolean override for this device", () => {
    const user = makeUser({
      devices: {
        [DEVICE_ID]: { overrides: { singleClick: false } },
        "other-device": { overrides: { singleClick: true } },
      },
    });
    expect(getDeviceEntry(user, DEVICE_ID)).toEqual({ overrides: { singleClick: false } });
    expect(getSingleClickOverride(user, DEVICE_ID)).toBe(false);
    expect(hasSingleClickOverride(user, DEVICE_ID)).toBe(true);
    expect(getSingleClickOverride(user, "missing-device")).toBeNull();
    expect(hasSingleClickOverride(user, "missing-device")).toBe(false);
    expect(getSingleClickOverride(makeUser(), DEVICE_ID)).toBeNull();
  });

  it("does not treat a non-boolean override as a device choice", () => {
    const user = makeUser({
      devices: { [DEVICE_ID]: { overrides: { singleClick: "true" } } },
    });
    expect(getSingleClickOverride(user, DEVICE_ID)).toBeNull();
  });

  it("computes the effective value from account default and override", () => {
    expect(computeEffectiveSingleClick(false, null)).toBe(false);
    expect(computeEffectiveSingleClick(true, null)).toBe(true);
    expect(computeEffectiveSingleClick(true, false)).toBe(false);
    expect(computeEffectiveSingleClick(false, true)).toBe(true);
    expect(computeEffectiveSingleClick(false, false)).toBe(false);
  });

  it("lets admin enforcement win over the device layer", () => {
    const enforced = { listing: { singleClick: true } };
    expect(isSingleClickEnforced(enforced, false)).toBe(true);
    expect(isSingleClickEnforced(enforced, true)).toBe(false);
    expect(isSingleClickEnforced({}, false)).toBe(false);
    expect(computeEffectiveSingleClick(true, false, true)).toBe(true);
    expect(computeEffectiveSingleClick(false, true, true)).toBe(false);
  });

  it("builds a single-device entry without a client lastSeen", () => {
    expect(buildDeviceEntry({ label: "Chrome · Linux", override: false })).toEqual({
      label: "Chrome · Linux",
      overrides: { singleClick: false },
    });
    expect(buildDeviceEntry({ override: true })).toEqual({
      overrides: { singleClick: true },
    });
    expect(buildDeviceEntry({ label: "Chrome", override: null })).toEqual({
      label: "Chrome",
      overrides: {},
    });
  });

  it("sends exactly one device in the PATCH payload", () => {
    const entry = { label: "This", overrides: { singleClick: true } };
    const payload = buildDevicePatchUser("alice", DEVICE_ID, entry);
    expect(payload.username).toBe("alice");
    expect(Object.keys(payload.devices)).toEqual([DEVICE_ID]);
    // eslint-disable-next-line security/detect-object-injection -- test-owned device id
    expect(payload.devices[DEVICE_ID]).toEqual(entry);
  });

  it("rejects unsafe device ids", () => {
    expect(isSafeDeviceId(DEVICE_ID)).toBe(true);
    expect(isSafeDeviceId("__proto__")).toBe(false);
    expect(isSafeDeviceId("short")).toBe(false);
    expect(isSafeDeviceId("")).toBe(false);
  });
});
