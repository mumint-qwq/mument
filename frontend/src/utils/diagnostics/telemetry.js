// Tiny sink used by non-diagnostics modules (store, prompts). The runtime
// registers the real recorder; without it every call is a no-op.
let sink = null;

export function setDiagnosticsSink(nextSink) {
  sink = nextSink || null;
}

export function diagnosticsSelectionChange(data) {
  try {
    sink?.selectionChange?.(data);
  } catch {
    // Diagnostics must never break a selection update.
  }
}

export function diagnosticsOperationResult(data, context = undefined) {
  try {
    sink?.operationResult?.(data, context);
  } catch {
    // Diagnostics must never break a file operation.
  }
}

export function diagnosticsRuntime() {
  return sink;
}
