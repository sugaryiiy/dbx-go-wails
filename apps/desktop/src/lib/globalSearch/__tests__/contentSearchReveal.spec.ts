import { describe, expect, it } from "vitest";
import { resolveContentSearchReveal } from "@/lib/globalSearch/contentSearchReveal";

describe("content search navigation in edited source", () => {
  it("relocates an unchanged preview line after unsaved lines are inserted", () => {
    expect(resolveContentSearchReveal("-- orders\n-- extra\nCREATE PROCEDURE p()\nSELECT orders;", { line: 2, column: 8, lineText: "SELECT orders;", matchText: "orders" })).toEqual({ line: 4, column: 8 });
  });
  it("uses UTF-16 columns and original case-insensitive matching in changed lines", () => {
    expect(resolveContentSearchReveal("SELECT '😀ORDERS';", { line: 1, column: 8, lineText: "SELECT orders;", matchText: "orders" })).toEqual({ line: 1, column: 11 });
  });
  it("keeps the chosen occurrence when a line contains multiple matches", () => {
    expect(resolveContentSearchReveal("-- added\norders orders", { line: 1, column: 8, lineText: "orders orders", matchText: "orders" })).toEqual({ line: 2, column: 8 });
  });
  it("reports a removed match instead of navigating using a stale line number", () => {
    expect(resolveContentSearchReveal("SELECT customers;", { line: 2, column: 8, lineText: "SELECT orders;", matchText: "orders" })).toBeUndefined();
  });
});
