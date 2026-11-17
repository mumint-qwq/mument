/**
 * Stable per-browser identity for device-scoped settings.
 *
 * The ID is generated once per browser storage area and persisted in
 * localStorage. It is an opaque random value, never a fingerprint of the user
 * agent, screen or network. Server-side settings are stored per user and per
 * device ID; clearing site data therefore starts a new device.
 */

const DEVICE_ID_STORAGE_KEY = "quantumDeviceId";
const DEVICE_ID_PATTERN = /^[A-Za-z0-9_-]{8,64}$/;

/** Keys that would hit special behaviour in a JavaScript object/map. */
const RESERVED_DEVICE_IDS = ["__proto__", "prototype", "constructor"];

let ephemeralDeviceId = null;
/** null = not attempted yet, true = persisted, false = page-only fallback. */
let storageWritable = null;

/** True when an ID is safe to send to the backend. */
export function isValidDeviceId(id) {
  if (typeof id !== "string" || !DEVICE_ID_PATTERN.test(id)) {
    return false;
  }
  return !RESERVED_DEVICE_IDS.includes(id.toLowerCase());
}

/**
 * Generate a random opaque device ID.
 * Returns null when Web Crypto is unusable; callers then stay page-only instead
 * of inventing a weak identifier from Math.random.
 */
export function generateDeviceId() {
  const cryptoApi = globalThis.crypto;
  if (!cryptoApi) {
    return null;
  }
  try {
    if (typeof cryptoApi.randomUUID === "function") {
      const id = cryptoApi.randomUUID();
      return isValidDeviceId(id) ? id : null;
    }
  } catch {
    // Fall through to the byte-based generator.
  }
  if (typeof cryptoApi.getRandomValues !== "function") {
    return null;
  }
  try {
    const bytes = new Uint8Array(16);
    cryptoApi.getRandomValues(bytes);
    // RFC 4122 version 4 / variant 10xx bits.
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
  } catch {
    return null;
  }
}

/** Read the persisted ID without creating one. Invalid/corrupt values return null. */
export function readStoredDeviceId() {
  try {
    const stored = localStorage.getItem(DEVICE_ID_STORAGE_KEY);
    return isValidDeviceId(stored) ? stored : null;
  } catch {
    return null;
  }
}

/** Persist a candidate ID. Returns true only when it can be read back. */
function persistDeviceId(candidate) {
  try {
    localStorage.setItem(DEVICE_ID_STORAGE_KEY, candidate);
    return localStorage.getItem(DEVICE_ID_STORAGE_KEY) === candidate;
  } catch {
    return false;
  }
}

/**
 * Resolve the device ID used by the current page.
 * @returns {{id: string|null, persistent: boolean}}
 *   `persistent: false` means the ID (if any) lives only for this page and no
 *   device write must be sent.
 */
export function resolveDeviceId() {
  const stored = readStoredDeviceId();
  if (stored) {
    storageWritable = true;
    return { id: stored, persistent: true };
  }
  if (ephemeralDeviceId) {
    // Keep the same page-only ID instead of creating a new one per call.
    storageWritable = false;
    return { id: ephemeralDeviceId, persistent: false };
  }
  const created = generateDeviceId();
  if (!created) {
    storageWritable = false;
    return { id: null, persistent: false };
  }
  if (persistDeviceId(created)) {
    storageWritable = true;
    return { id: created, persistent: true };
  }
  storageWritable = false;
  ephemeralDeviceId = created;
  return { id: created, persistent: false };
}

/**
 * Re-read the persisted ID immediately before a write. Another tab may have
 * replaced it (for example after the user cleared site data in that tab);
 * holding the replaced ID would keep writing to a phantom device.
 * @returns {{id: string|null, persistent: boolean}}
 */
export function resolveDeviceIdForSave(fallbackId = "") {
  const stored = readStoredDeviceId();
  if (stored) {
    storageWritable = true;
    return { id: stored, persistent: true };
  }
  const candidate = isValidDeviceId(fallbackId) ? fallbackId : generateDeviceId();
  if (!candidate) {
    storageWritable = false;
    return { id: null, persistent: false };
  }
  if (persistDeviceId(candidate)) {
    storageWritable = true;
    return { id: candidate, persistent: true };
  }
  storageWritable = false;
  ephemeralDeviceId = candidate;
  return { id: candidate, persistent: false };
}

/** Test/diagnostic helper: has a storage failure already been observed? */
export function isDeviceStorageAvailable() {
  return storageWritable !== false;
}

/** Test helper: forget module-level fallback state. */
export function resetDeviceIdentityForTests() {
  ephemeralDeviceId = null;
  storageWritable = null;
}

/** Derive a compact human-readable label from UA/platform strings. */
export function deviceLabelFrom(ua = "", platform = "") {
  const source = `${ua} ${platform}`;
  let os = "Unknown OS";
  if (/Android/i.test(source)) {
    os = "Android";
  } else if (/iPhone|iPad|iPod/i.test(source)) {
    os = "iOS";
  } else if (/Windows/i.test(source)) {
    os = "Windows";
  } else if (/Mac OS X|Macintosh/i.test(source)) {
    os = "macOS";
  } else if (/Linux/i.test(source)) {
    os = "Linux";
  }

  let browser = "Browser";
  if (/Edg\//i.test(ua)) {
    browser = "Edge";
  } else if (/OPR\//i.test(ua)) {
    browser = "Opera";
  } else if (/Firefox\//i.test(ua)) {
    browser = "Firefox";
  } else if (/CriOS\//i.test(ua)) {
    browser = "Chrome";
  } else if (/FxiOS\//i.test(ua)) {
    browser = "Firefox";
  } else if (/Chrome\//i.test(ua)) {
    browser = "Chrome";
  } else if (/Safari\//i.test(ua)) {
    browser = "Safari";
  }
  const label = `${browser} · ${os}`;
  // Control characters and overlong labels are never sent.
  const cleaned = Array.from(label)
    .filter((char) => {
      const code = char.codePointAt(0) ?? 0;
      return code >= 32 && code !== 127;
    })
    .join("");
  return cleaned.slice(0, 80);
}

/** Human-readable label for the current browser (display only). */
export function getDeviceLabel() {
  try {
    return deviceLabelFrom(
      navigator.userAgent || "",
      navigator.userAgentData?.platform || navigator.platform || ""
    );
  } catch {
    return "Browser";
  }
}

/** Compact ID for display; the full ID stays internal. */
export function getShortDeviceId(id = "") {
  if (typeof id !== "string" || id.length === 0) {
    return "";
  }
  return id.length > 12 ? `${id.slice(0, 8)}…` : id;
}
