import { describe, expect, it } from "vitest";
import { adaptMoveCopyResult } from "./moveResults.js";

const requested = [
  { source: "pictures", path: "/a.jpg" },
  { source: "pictures", path: "/b.jpg" },
];

const success = (path, source = "pictures", extra = {}) => ({
  fromSource: source,
  fromPath: path,
  toSource: "pictures",
  toPath: "/Dest/" + path.split("/").pop(),
  ...extra,
});

describe("adaptMoveCopyResult", () => {
  it("confirms all requested items only when every identity is echoed", () => {
    const adapted = adaptMoveCopyResult({
      requested,
      response: { succeeded: [success("/a.jpg"), success("/b.jpg")], failed: [] },
    });
    expect(adapted.outcome).toBe("ok");
    expect(adapted.confirmedSucceededCount).toBe(2);
    expect(adapted.confirmedSucceeded.map((entry) => entry.path)).toEqual(["/a.jpg", "/b.jpg"]);
  });

  it("keeps failures as their own set for a 207 partial response", () => {
    const adapted = adaptMoveCopyResult({
      requested,
      response: { succeeded: [success("/a.jpg")], failed: [{ fromSource: "pictures", fromPath: "/b.jpg", message: "denied" }] },
    });
    expect(adapted.outcome).toBe("partial");
    expect(adapted.confirmedSucceeded.map((entry) => entry.path)).toEqual(["/a.jpg"]);
    expect(adapted.confirmedFailed.map((entry) => entry.path)).toEqual(["/b.jpg"]);
    expect(adapted.failureMessage).toBe("denied");
  });

  it("reports an all-failure response as failed", () => {
    const adapted = adaptMoveCopyResult({
      requested,
      response: {
        succeeded: [],
        failed: [
          { fromSource: "pictures", fromPath: "/a.jpg", message: "nope" },
          { fromSource: "pictures", fromPath: "/b.jpg", message: "nope" },
        ],
      },
    });
    expect(adapted.outcome).toBe("failed");
    expect(adapted.confirmedSucceededCount).toBe(0);
  });

  it("never treats a 200 with a missing result as success", () => {
    const adapted = adaptMoveCopyResult({
      requested,
      response: { succeeded: [success("/a.jpg")], failed: [] },
    });
    expect(adapted.outcome).toBe("unknown");
    expect(adapted.unmatchedRequests).toBe(1);
  });

  it("rejects duplicated and contradictory identities", () => {
    const duplicate = adaptMoveCopyResult({
      requested,
      response: { succeeded: [success("/a.jpg"), success("/a.jpg")], failed: [] },
    });
    expect(duplicate.outcome).toBe("unknown");
    const both = adaptMoveCopyResult({
      requested,
      response: {
        succeeded: [success("/a.jpg"), success("/b.jpg")],
        failed: [{ fromSource: "pictures", fromPath: "/a.jpg", message: "x" }],
      },
    });
    expect(both.outcome).toBe("unknown");
    expect(both.confirmedSucceeded.map((entry) => entry.path)).toEqual(["/b.jpg"]);
  });

  it("ignores results whose source/path was not requested", () => {
    const adapted = adaptMoveCopyResult({
      requested,
      response: { succeeded: [success("/a.jpg"), success("/b.jpg"), success("/other.jpg")], failed: [] },
    });
    expect(adapted.outcome).toBe("unknown");
  });

  it("reads fromPath, not an auto-renamed toPath, as the source identity", () => {
    const adapted = adaptMoveCopyResult({
      requested: [requested[0]],
      response: {
        succeeded: [{ fromSource: "pictures", fromPath: "/a.jpg", toSource: "pictures", toPath: "/Dest/a(1).jpg" }],
        failed: [],
      },
    });
    expect(adapted.outcome).toBe("ok");
    expect(adapted.confirmedSucceeded[0].path).toBe("/a.jpg");
  });

  it("fills a missing share source from the trusted operation context", () => {
    const adapted = adaptMoveCopyResult({
      requested: [{ source: "share-source", path: "/a.jpg" }],
      response: { succeeded: [{ fromPath: "/a.jpg", toPath: "/Dest/a.jpg" }], failed: [] },
      fallbackSource: "share-source",
      isShare: true,
    });
    expect(adapted.outcome).toBe("ok");
    expect(adapted.confirmedSucceeded[0].source).toBe("share-source");
  });

  it("trusts a complete all-success share count even though paths are stripped", () => {
    const adapted = adaptMoveCopyResult({
      requested: [{ source: "share-source", path: "/a.jpg" }, { source: "share-source", path: "/b.jpg" }],
      response: { succeeded: [{}, {}], failed: [] },
      fallbackSource: "share-source",
      isShare: true,
    });
    expect(adapted.outcome).toBe("ok");
    expect(adapted.confirmedSucceededCount).toBe(2);
    expect(adapted.confirmedSucceeded.map((entry) => entry.path)).toEqual(["/a.jpg", "/b.jpg"]);
  });

  it("cannot map a partial share response back to individual files", () => {
    const adapted = adaptMoveCopyResult({
      requested: [{ source: "share-source", path: "/a.jpg" }, { source: "share-source", path: "/b.jpg" }],
      response: { succeeded: [{}], failed: [{ message: "denied" }] },
      fallbackSource: "share-source",
      isShare: true,
    });
    expect(adapted.outcome).toBe("unknown");
    expect(adapted.confirmedSucceededCount).toBe(1);
    expect(adapted.confirmedFailedCount).toBe(1);
    expect(adapted.confirmedSucceeded).toEqual([]);
    expect(adapted.countOnly).toBe(true);
  });

  it("returns unknown when the response body has no arrays", () => {
    expect(adaptMoveCopyResult({ requested, response: {} }).outcome).toBe("unknown");
    expect(adaptMoveCopyResult({ requested, response: null }).outcome).toBe("unknown");
  });
});
