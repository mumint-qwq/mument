/**
 * Pure helpers for the singleClick per-device override.
 *
 * Three values are kept apart:
 *  - account default: the value stored in the user profile (server-owned)
 *  - device override: this browser's explicit boolean, or absent
 *  - effective value: what the listing actually uses
 */

/** The only device-scoped profile field in this release. */
export const DEVICE_SINGLE_CLICK = "singleClick";

/** The stored record for one device, or null when this device has no record. */
export function getDeviceEntry(user, deviceId) {
  const devices = user?.devices;
  if (!devices || typeof devices !== "object" || !deviceId) {
    return null;
  }
  // eslint-disable-next-line security/detect-object-injection -- device ids are validated opaque browser ids
  const entry = devices[deviceId];
  return entry && typeof entry === "object" ? entry : null;
}

/** This device's explicit singleClick value, or null when it has no opinion. */
export function getSingleClickOverride(user, deviceId) {
  const overrides = getDeviceEntry(user, deviceId)?.overrides;
  if (!overrides || typeof overrides !== "object") {
    return null;
  }
  return typeof overrides.singleClick === "boolean" ? overrides.singleClick : null;
}

export function hasSingleClickOverride(user, deviceId) {
  return getSingleClickOverride(user, deviceId) !== null;
}

/** Admin enforcement wins over the device layer (mirrors EnforcementAppliesToUser). */
export function isSingleClickEnforced(enforcedUserDefaults, isAdmin = false) {
  if (isAdmin) {
    return false;
  }
  return enforcedUserDefaults?.listing?.singleClick === true;
}

/**
 * Effective opening mode. An explicit false is a real choice, so use a type
 * check instead of a truthiness fallback.
 */
export function computeEffectiveSingleClick(accountValue, override, enforced = false) {
  if (enforced) {
    return accountValue === true;
  }
  return typeof override === "boolean" ? override : accountValue === true;
}

/**
 * Build the single device entry sent to the server. The full override object
 * for this device is sent, so clearing one value cannot resurrect another.
 * lastSeen is deliberately omitted: the server stamps it.
 */
export function buildDeviceEntry({ label = "", override = null } = {}) {
  const entry = { overrides: {} };
  if (typeof label === "string" && label.length > 0) {
    entry.label = label;
  }
  if (typeof override === "boolean") {
    entry.overrides.singleClick = override;
  }
  return entry;
}

/** Payload for usersApi.update(..., ["devices"]); exactly one device entry. */
export function buildDevicePatchUser(username, deviceId, entry) {
  return {
    username,
    devices: {
      [deviceId]: entry,
    },
  };
}

/** True when a device id is safe to use as an object key. */
export function isSafeDeviceId(deviceId) {
  return typeof deviceId === "string"
    && /^[A-Za-z0-9_-]{8,64}$/.test(deviceId)
    && !["__proto__", "prototype", "constructor"].includes(deviceId.toLowerCase());
}
