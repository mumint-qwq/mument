// Diagnostics HTTP calls. Failures stay silent for the browser UI; the
// transport decides whether to retry. Never records its own errors.
import { getApiPath } from "@/utils/url.js";
import { state } from "@/store";

export async function getDiagnosticsStatus(options = {}) {
  const timeoutMs = Number(options.timeoutMs) > 0 ? Number(options.timeoutMs) : 5000;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  const external = options.signal;
  const onAbort = () => controller.abort();
  if (external?.addEventListener) external.addEventListener("abort", onAbort, { once: true });
  try {
    // Read non-2xx bodies too: 403 unsupported_auth is a capability answer,
    // not a transient network failure.
    const response = await fetch(getApiPath("diagnostics/status"), {
      method: "GET",
      credentials: "same-origin",
      signal: controller.signal,
      headers: { Accept: "application/json", sessionId: state.sessionId },
    });
    let body = null;
    try {
      body = await response.json();
    } catch {
      body = null;
    }
    return { status: response.status, body };
  } catch (error) {
    return { status: error?.status || 0, body: null, errorName: error?.name || "network" };
  } finally {
    clearTimeout(timer);
    if (external?.removeEventListener) external.removeEventListener("abort", onAbort);
  }
}

export async function postDiagnosticsBatch(payload, options = {}) {
  const keepalive = Boolean(options.keepalive);
  const timeoutMs = Number(options.timeoutMs) > 0 ? Number(options.timeoutMs) : 5000;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  const external = options.signal;
  const onAbort = () => controller.abort();
  if (external?.addEventListener) external.addEventListener("abort", onAbort, { once: true });
  try {
    const response = await fetch(getApiPath("diagnostics/client"), {
      method: "POST",
      credentials: "same-origin",
      keepalive,
      signal: controller.signal,
      headers: {
        "Content-Type": "application/json",
        "X-Quantum-Diagnostics": "1",
        sessionId: state.sessionId,
      },
      body: JSON.stringify(payload),
    });
    let body = null;
    try {
      body = await response.json();
    } catch {
      body = null;
    }
    return {
      status: response.status,
      body,
      retryAfter: Number(response.headers.get("Retry-After")) || 0,
    };
  } catch (error) {
    return { status: 0, body: null, errorName: error?.name || "network" };
  } finally {
    clearTimeout(timer);
    if (external?.removeEventListener) external.removeEventListener("abort", onAbort);
  }
}
