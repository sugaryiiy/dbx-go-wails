import { describe, expect, it } from "vitest";
import { DataGridSearchMatchIndex } from "../dataGridSearchMatchIndex";

describe("data grid bitmap match index", () => {
  it("shares ordered navigation and Set membership across words and sparse blocks", () => {
    const columns = 50;
    const cells = [0, 31, 32, 49, 50, 4095, 4096, 4097, 999_999, 5_000_049];
    const keys = cells.map((cell) => Math.floor(cell / columns) * 65536 + (cell % columns));
    const index = new DataGridSearchMatchIndex(columns);
    cells.forEach((cell) => index.appendCell(cell));
    expect(index.size).toBe(keys.length);
    expect([...index]).toEqual(keys);
    expect([...index.entries()]).toEqual(keys.map((key) => [key, key]));
    keys.forEach((key, ordinal) => {
      expect(index.keyAt(ordinal)).toBe(key);
      expect(index.has(key)).toBe(true);
    });
    expect(index.has(65536 + 1)).toBe(false);
    expect(index.has(50)).toBe(false); // nonexistent header column
    expect(index.has(-1)).toBe(false);
    expect(index.keyAt(-1)).toBeUndefined();
    expect(index.keyAt(index.size)).toBeUndefined();
    const visited: number[] = [];
    index.forEach((key, duplicate, set) => {
      expect(duplicate).toBe(key);
      expect(set).toBe(index);
      visited.push(key);
    });
    expect(visited).toEqual(keys);
  });

  it("stores five million dense matches in about 625 KB without a second Set", () => {
    const index = new DataGridSearchMatchIndex(50);
    for (let cell = 50; cell < 5_000_050; cell++) index.appendCell(cell);
    expect(index.size).toBe(5_000_000);
    expect(index.bitmapBytes).toBeLessThan(630_000);
    expect(index.keyAt(0)).toBe(65536);
    expect(index.keyAt(4_999_999)).toBe(100_000 * 65536 + 49);
    expect(index.has(100_000 * 65536 + 49)).toBe(true);
  });
});
