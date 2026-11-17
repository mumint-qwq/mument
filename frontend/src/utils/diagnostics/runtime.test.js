import { describe, expect, it } from "vitest";
import { readClientBuild } from "./runtime.js";

function docWith(version, commit) {
  const query = (selector) => {
    if (selector.includes("quantum-mobile-ui")) return { getAttribute: () => version };
    if (selector.includes("quantum-source-commit")) return { getAttribute: () => commit };
    return null;
  };
  return { querySelector: query };
}

describe("client build identity", () => {
  it("reads only allowed page markers", () => {
    const build = readClientBuild(docWith("diag-rc1", "77f46d36dff09f7cb20b32fb3f98daa9e47e23c3"));
    expect(build).toEqual({ version: "diag-rc1", commit: "77f46d36dff09f7cb20b32fb3f98daa9e47e23c3" });
  });

  it("returns null instead of inventing values", () => {
    expect(readClientBuild(docWith("__QUANTUM_SOURCE_COMMIT__", "__QUANTUM_SOURCE_COMMIT__"))).toEqual({ version: null, commit: null });
    expect(readClientBuild(docWith("bad/version", "abc"))).toEqual({ version: null, commit: null });
    expect(readClientBuild(null)).toEqual({ version: null, commit: null });
  });
});
