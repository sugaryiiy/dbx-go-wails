import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { buildExplainSql, parseExplainResult, supportsExplainPlan } from "@/lib/diagram/explainPlan";

const mocks = vi.hoisted(() => ({ buildExplainSql: vi.fn(), executeQuery: vi.fn(), closeClientSession: vi.fn(), saveOpenTabsState: vi.fn() }));
vi.mock("@/lib/backend/api", () => ({
  buildExplainSql: mocks.buildExplainSql,
  executeQuery: mocks.executeQuery,
  closeClientConnectionSession: mocks.closeClientSession,
  saveOpenTabsState: mocks.saveOpenTabsState,
}));
vi.mock("@/stores/connectionStore", () => ({ useConnectionStore: () => ({ getConfig: () => ({ id: "ch", name: "ClickHouse", db_type: "clickhouse", query_timeout_secs: 30 }), recordConnectionLostError: vi.fn() }) }));
vi.mock("@/stores/settingsStore", () => ({ useSettingsStore: () => ({ editorSettings: { pageSize: 100, openTabsRestoreMode: "all", confirmUnsavedSqlClose: false } }) }));

const sql = "SELECT number FROM numbers(10)";
const result = { columns: ["explain"], rows: [["Expression ((Project names + Projection))"], ["  ReadFromSystemNumbers"]], affected_rows: 0, execution_time_ms: 1 };

beforeEach(() => {
  vi.clearAllMocks();
  vi.stubEnv("VITE_DBX_BACKEND", "go");
  const data = new Map<string, string>();
  vi.stubGlobal("localStorage", { getItem: (key: string) => data.get(key) ?? null, setItem: (key: string, value: string) => data.set(key, value), removeItem: (key: string) => data.delete(key) });
  setActivePinia(createPinia());
  mocks.buildExplainSql.mockImplementation(async (options) => options.format === "standard" ? { ok: true, sql: `EXPLAIN ${options.sql}` } : { ok: false, reason: "unsupported" });
  mocks.executeQuery.mockResolvedValue(result);
  mocks.closeClientSession.mockResolvedValue(undefined);
  mocks.saveOpenTabsState.mockResolvedValue(undefined);
});
afterEach(() => { vi.unstubAllEnvs(); vi.unstubAllGlobals(); });

describe("Go ClickHouse EXPLAIN toolbar path", () => {
  it("requests a plain plan and renders its text hierarchy through the query store", async () => {
    const { useQueryStore } = await import("@/stores/queryStore");
    const store = useQueryStore();
    const tabId = store.createTab("ch", "default", "Query", "query", "default");
    expect(supportsExplainPlan("clickhouse")).toBe(true);
    await store.explainTabSql(tabId, sql, "clickhouse");
    expect(mocks.buildExplainSql).toHaveBeenCalledWith({ databaseType: "clickhouse", sql, format: "standard" });
    expect(mocks.executeQuery).toHaveBeenCalledWith("ch", "default", `EXPLAIN ${sql}`, "default", expect.any(String), expect.any(Object));
    const tab = store.tabs.find((item) => item.id === tabId)!;
    expect(tab.explainError).toBeUndefined();
    expect(tab.isExplaining).toBe(false);
    expect(tab.explainPlan?.databaseType).toBe("clickhouse");
    expect(tab.explainPlan?.raw).toBe("Expression ((Project names + Projection))\n  ReadFromSystemNumbers");
    expect(tab.explainPlan?.nodes[0].children[0].title).toBe("ReadFromSystemNumbers");
    expect(tab.explainPlan?.nodes[0].cost).toBeUndefined();
  });
  it("does not silently substitute plain mode for requested JSON or ANALYZE", async () => {
    expect(await buildExplainSql("clickhouse", sql, "json")).toEqual({ ok: false, reason: "unsupported" });
    expect(await buildExplainSql("clickhouse", sql, "standard", true)).toEqual({ ok: false, reason: "unsupported" });
    expect(mocks.buildExplainSql).not.toHaveBeenCalled();
  });
  it("preserves multiline plans and sibling indentation", () => {
    const plan = parseExplainResult("clickhouse", { ...result, rows: [["Union\n  ReadFromMergeTree (a)\n  ReadFromMergeTree (b)"]] });
    expect(plan.nodes[0].children.map((node) => node.title)).toEqual(["ReadFromMergeTree (a)", "ReadFromMergeTree (b)"]);
  });
});
