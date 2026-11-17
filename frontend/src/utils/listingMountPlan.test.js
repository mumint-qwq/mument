import { describe, expect, it } from "vitest";
import { buildRowQueue, findRowIndexForItem, rowsInBand, takeBatch } from "./listingMountPlan.js";

const rows = [
  { top: 0, height: 100, indices: [0, 1] },
  { top: 104, height: 100, indices: [2] },
  { top: 208, height: 100, indices: [3, 4, 5] },
  { top: 312, height: 100, indices: [6] },
];

describe("rowsInBand", () => {
  it("selects rows intersecting the band and ignores touching rows", () => {
    expect(rowsInBand(rows, 50, 250).map(r => r.rowIndex)).toEqual([0, 1, 2]);
    expect(rowsInBand(rows, 0, 100).map(r => r.rowIndex)).toEqual([0]);
    expect(rowsInBand(rows, 99, 105).map(r => r.rowIndex)).toEqual([0, 1]);
  });
  it("returns nothing for an empty or inverted band", () => {
    expect(rowsInBand(rows, 10, 10)).toEqual([]);
    expect(rowsInBand(rows, 400, 300)).toEqual([]);
    expect(rowsInBand(rows, 1000, 1200)).toEqual([]);
  });
});

describe("findRowIndexForItem", () => {
  it("maps inner and boundary indices to their row", () => {
    expect(findRowIndexForItem(rows, 0)).toBe(0);
    expect(findRowIndexForItem(rows, 1)).toBe(0);
    expect(findRowIndexForItem(rows, 2)).toBe(1);
    expect(findRowIndexForItem(rows, 5)).toBe(2);
    expect(findRowIndexForItem(rows, 6)).toBe(3);
  });
  it("returns -1 for unknown indices", () => {
    expect(findRowIndexForItem(rows, 7)).toBe(-1);
    expect(findRowIndexForItem([], 0)).toBe(-1);
  });
});

describe("buildRowQueue", () => {
  it("orders by distance from the group centre and skips mounted rows", () => {
    const groups = { files: rows };
    const queue = buildRowQueue(groups, { files: 260 }, () => false);
    expect(queue.map(e => e.rowIndex)).toEqual([2, 3, 1, 0]);
    const withMounted = buildRowQueue(groups, { files: 260 }, (group, rowIndex) => group === "files" && rowIndex === 2);
    expect(withMounted.map(e => e.rowIndex)).toEqual([3, 1, 0]);
  });
});

describe("takeBatch", () => {
  it("keeps rows whole and stops at the item budget", () => {
    const groups = { files: rows };
    const queue = buildRowQueue(groups, { files: 0 }, () => false);
    const picked = takeBatch(queue, groups, () => false, 3);
    expect(picked.map(e => e.rowIndex)).toEqual([0, 1]);
  });
  it("skips rows that were mounted since the queue was built", () => {
    const groups = { files: rows };
    const queue = buildRowQueue(groups, { files: 0 }, () => false);
    const picked = takeBatch(queue, groups, (group, rowIndex) => rowIndex === 0, 4);
    expect(picked.map(e => e.rowIndex)).toEqual([1, 2]);
  });
});
