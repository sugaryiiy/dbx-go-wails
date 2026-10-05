import { describe, expect, it } from "vitest";
import { buildLineStarts, cursorPositionFromLineStarts } from "@/lib/editor/workbenchCursorPosition";

describe("workbench cursor position", () => {
  it("indexes empty, consecutive, and trailing lines without treating CR as a newline", () => {
    expect(buildLineStarts("")).toEqual([0]);
    expect(buildLineStarts("SELECT 1")).toEqual([0]);
    expect(buildLineStarts("a\n\nb\n")).toEqual([0, 2, 3, 5]);
    expect(buildLineStarts("a\r\nb\rc")).toEqual([0, 3]);
  });

  it.each(["", "a", "a\nb", "a\n", "\n\n", "SELECT 😀x\n汉字\n", "a\r\nb", "a\rb", "a\u2028b"])("matches LF-based UTF-16 coordinates at every offset in %j", (sql) => {
    const starts = Object.freeze(buildLineStarts(sql));
    for (let position = 0; position <= sql.length; position += 1) {
      const prefixLines = sql.slice(0, position).split("\n");
      expect(cursorPositionFromLineStarts(starts, sql.length, position)).toEqual({
        line: prefixLines.length,
        column: prefixLines[prefixLines.length - 1].length + 1,
      });
    }
  });

  it("counts surrogate pairs as two UTF-16 columns and advances only after LF", () => {
    const sql = "😀x\n汉字";
    const starts = buildLineStarts(sql);
    expect(starts).toEqual([0, 4]);
    expect(cursorPositionFromLineStarts(starts, sql.length, 2)).toEqual({ line: 1, column: 3 });
    expect(cursorPositionFromLineStarts(starts, sql.length, 3)).toEqual({ line: 1, column: 4 });
    expect(cursorPositionFromLineStarts(starts, sql.length, 4)).toEqual({ line: 2, column: 1 });
    expect(cursorPositionFromLineStarts(starts, sql.length, 6)).toEqual({ line: 2, column: 3 });
  });

  it.each([
    [-10, { line: 1, column: 1 }],
    [-Infinity, { line: 1, column: 1 }],
    [NaN, { line: 1, column: 1 }],
    [1.9, { line: 1, column: 2 }],
    [2.9, { line: 2, column: 1 }],
    [100, { line: 2, column: 3 }],
    [Infinity, { line: 2, column: 3 }],
  ])("clamps cursor offset %s to an integer within the document", (position, expected) => {
    const sql = "a\nbc";
    expect(cursorPositionFromLineStarts(buildLineStarts(sql), sql.length, position)).toEqual(expected);
  });

  it("handles an empty index and an unavailable document length without invalid coordinates", () => {
    expect(cursorPositionFromLineStarts([], 5, 2)).toEqual({ line: 1, column: 3 });
    expect(cursorPositionFromLineStarts([0], -1, 20)).toEqual({ line: 1, column: 1 });
    expect(cursorPositionFromLineStarts([0], NaN, 20)).toEqual({ line: 1, column: 1 });
  });

  it("uses logarithmic index lookups near the end of a large document", () => {
    const sql = "x\n".repeat(65536);
    const starts = buildLineStarts(sql);
    let indexReads = 0;
    const observedStarts = new Proxy(starts, {
      get(target, property, receiver) {
        if (typeof property === "string" && /^\d+$/.test(property)) indexReads += 1;
        return Reflect.get(target, property, receiver);
      },
    });
    expect(cursorPositionFromLineStarts(observedStarts, sql.length, sql.length - 1)).toEqual({ line: 65536, column: 2 });
    // A linear search would visit ~65K entries on every cursor update. Avoid a
    // wall-clock assertion so this remains stable on busy CI machines.
    expect(indexReads).toBeLessThanOrEqual(20);
  });
});
