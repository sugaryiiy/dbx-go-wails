import { describe, expect, it } from "vitest";
import { filterActionSearchItems, nextEnabledActionIndex, type ActionSearchItem } from "../actionSearch";

const actions: readonly ActionSearchItem[] = [
  { id: "execute", label: "执行 SQL", group: "查询", keywords: ["Run", "Execute Query"] },
  { id: "format", label: "格式化 SQL", group: "编辑器", keywords: ["Format SQL", "Reformat"] },
  { id: "stop", label: "停止查询", group: "查询", keywords: ["Stop", "Cancel Query"], disabled: true, disabledReason: "没有正在运行的查询" },
];

describe("action search", () => {
  it("matches Chinese labels and groups as well as case-insensitive English aliases", () => {
    expect(filterActionSearchItems(actions, "格式化").map((item) => item.id)).toEqual(["format"]);
    expect(filterActionSearchItems(actions, "查询").map((item) => item.id)).toEqual(["stop", "execute"]);
    expect(filterActionSearchItems(actions, "  ＥＸＥＣＵＴＥ   query  ").map((item) => item.id)).toEqual(["execute"]);
  });

  it("requires every search term while allowing terms to match different fields", () => {
    expect(filterActionSearchItems(actions, "编辑器 format").map((item) => item.id)).toEqual(["format"]);
    expect(filterActionSearchItems(actions, "编辑器 run")).toEqual([]);
    expect(filterActionSearchItems(actions, "not available")).toEqual([]);
  });

  it("ranks exact labels, prefixes, substrings, aliases and groups with stable ties", () => {
    const items: ActionSearchItem[] = [
      { id: "group", label: "Other", group: "Run" },
      { id: "keyword", label: "Execute", group: "SQL", keywords: ["Run"] },
      { id: "substring", label: "Dry run", group: "SQL" },
      { id: "prefix", label: "Run query", group: "SQL" },
      { id: "exact", label: "Run", group: "SQL" },
      { id: "same", label: "Run query", group: "SQL" },
    ];
    expect(filterActionSearchItems(items, "run").map((item) => item.id)).toEqual(["exact", "prefix", "same", "substring", "keyword", "group"]);
    expect(items[0].id).toBe("group");
  });

  it("preserves disabled items and caller order for an empty query without mutating input", () => {
    const found = filterActionSearchItems(actions, " \n ");
    expect(found).toEqual(actions);
    expect(found).not.toBe(actions);
    expect(filterActionSearchItems(actions, "cancel")[0]).toBe(actions[2]);
  });
});

describe("action search keyboard navigation", () => {
  const items = [actions[0], actions[2], actions[1]];

  it("skips disabled actions in both directions and wraps", () => {
    expect(nextEnabledActionIndex(items, -1, 1)).toBe(0);
    expect(nextEnabledActionIndex(items, 0, 1)).toBe(2);
    expect(nextEnabledActionIndex(items, 2, 1)).toBe(0);
    expect(nextEnabledActionIndex(items, 2, -1)).toBe(0);
    expect(nextEnabledActionIndex(items, 0, -1)).toBe(2);
    expect(nextEnabledActionIndex(items, -1, -1)).toBe(2);
  });

  it("does not select an item when the list is empty or all results are disabled", () => {
    expect(nextEnabledActionIndex([], -1, 1)).toBe(-1);
    expect(nextEnabledActionIndex([actions[2]], 0, 1)).toBe(-1);
    expect(nextEnabledActionIndex([actions[2]], 0, -1)).toBe(-1);
  });
});
