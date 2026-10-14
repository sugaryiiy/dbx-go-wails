import { effectScope, reactive, ref, type EffectScope } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { findContentMatches, useGlobalContentSearch } from "@/composables/useGlobalContentSearch";
import * as api from "@/lib/backend/api";
import type { ConnectionConfig, QueryTab } from "@/types/database";
import type { ContentSearchResult, SavedQueryContentSearchMatch } from "@/lib/globalSearch/contentSearchTypes";

vi.mock("@/lib/backend/api", () => ({ searchObjectSources: vi.fn(), searchSavedQueries: vi.fn(), globalSearch: vi.fn(), listDatabases: vi.fn() }));
vi.mock("@/stores/queryStore", () => ({ useQueryStore: vi.fn() }));
vi.mock("@/lib/globalSearch/globalSearchSettings", async () => {
  const { ref } = await import("vue");
  return { composeGlobalSearchRoots: () => [], getGlobalSearchExtensions: () => ["sql"], globalSearchSettingsVersion: ref(0) };
});

const scopes: EffectScope[] = [];
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}
function fixture(dbType = "mysql", initialTabs: Partial<QueryTab>[] = []) {
  const scope = effectScope();
  scopes.push(scope);
  const searchQuery = ref("");
  const selectedIndex = ref(0);
  const version = ref(0);
  const connectionStore = reactive({
    activeConnectionId: "conn",
    connections: [{ id: "conn", name: "工作库", db_type: dbType, database: "biz" }] as ConnectionConfig[],
    ensureConnected: vi.fn().mockResolvedValue(undefined),
  });
  const tabs = reactive(initialTabs as QueryTab[]);
  const search = scope.run(() => useGlobalContentSearch({ searchQuery, selectedIndex, connectionStore, savedSqlVersion: () => version.value, getQueryTabs: () => tabs }))!;
  search.setSearchActive(true);
  search.setContentMode(true);
  return { search, searchQuery, connectionStore, tabs, version, scope };
}
const location = { line: 2, column: 4, line_text: "😀 订单 total", match_text: "订单", start: 3, end: 5 };

describe("global connection content search", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.mocked(api.searchSavedQueries).mockResolvedValue({ matches: [], truncated: false });
    vi.mocked(api.searchObjectSources).mockResolvedValue({ matches: [], truncated: false, warnings: [] });
    vi.mocked(api.listDatabases).mockResolvedValue([{ name: "biz" }, { name: "analytics" }, { name: "mysql" }]);
  });
  afterEach(() => {
    scopes.splice(0).forEach((scope) => scope.stop());
    vi.useRealTimers();
    vi.clearAllMocks();
  });

  it("searches routines, events and queries without configured file roots, preserving live edits", async () => {
    const { search, searchQuery } = fixture("mysql", [
      { id: "open", title: "临时查询", mode: "query", connectionId: "conn", database: "biz", sql: "SELECT '😀订单';", savedSqlId: "edited" },
      { id: "other", title: "其他连接", mode: "query", connectionId: "other", database: "biz", sql: "订单" },
    ]);
    vi.mocked(api.searchSavedQueries).mockResolvedValue({
      matches: [
        { ...location, id: "saved", name: "日报.sql", connection_id: "conn", database: "analytics" },
        { ...location, id: "edited", name: "old.sql", connection_id: "conn", database: "biz" },
      ],
      truncated: false,
    });
    vi.mocked(api.searchObjectSources).mockResolvedValue({
      matches: [
        { ...location, object_type: "PROCEDURE", object_name: "refresh_orders", database: "biz", schema: "biz" },
        { ...location, object_type: "EVENT", object_name: "nightly_orders", database: "analytics", schema: "analytics" },
      ],
      truncated: false,
      warnings: [],
    });
    searchQuery.value = "订单";
    await vi.advanceTimersByTimeAsync(200);

    expect(api.searchSavedQueries).toHaveBeenCalledWith(expect.objectContaining({ connection_id: "conn", query: "订单", exclude_ids: ["edited"] }), expect.any(AbortSignal));
    expect(api.searchObjectSources).toHaveBeenCalledWith(expect.objectContaining({ databases: ["biz", "analytics"] }), expect.any(AbortSignal));
    expect(api.globalSearch).not.toHaveBeenCalled();
    expect(search.contentGroups.value.map((group) => group.type)).toEqual(["query_tab", "saved_query", "procedure", "event"]);
    expect(search.contentGroups.value[0].matches[0]).toMatchObject({ queryTabId: "open", line: 1, column: 11, highlightIndices: [10, 12] });
    expect(search.contentGroups.value[1]).toMatchObject({ header: "日报.sql", description: "工作库 / analytics" });
    expect(search.contentGroups.value[3].matches[0]).toMatchObject({ objectName: "nightly_orders", objectType: "EVENT", highlightIndices: [3, 5] });
    expect(search.contentSearching.value).toBe(false);
  });

  it("aborts old requests and ignores their late results after query changes and dialog close", async () => {
    const first = deferred<ContentSearchResult<SavedQueryContentSearchMatch>>();
    vi.mocked(api.searchSavedQueries).mockReturnValueOnce(first.promise);
    const { search, searchQuery } = fixture("sqlite");
    searchQuery.value = "old";
    await vi.advanceTimersByTimeAsync(200);
    const firstSignal = vi.mocked(api.searchSavedQueries).mock.calls[0][1]!;
    searchQuery.value = "new";
    expect(firstSignal.aborted).toBe(true);
    vi.mocked(api.searchSavedQueries).mockResolvedValue({ matches: [{ ...location, id: "new", name: "new.sql", connection_id: "conn", database: "biz" }], truncated: false });
    await vi.advanceTimersByTimeAsync(200);
    first.resolve({ matches: [{ ...location, id: "old", name: "old.sql", connection_id: "conn", database: "biz" }], truncated: false });
    await vi.advanceTimersByTimeAsync(0);
    expect(search.contentGroups.value.map((group) => group.header)).toEqual(["new.sql"]);
    const secondSignal = vi.mocked(api.searchSavedQueries).mock.calls[1][1]!;
    search.setSearchActive(false);
    expect(secondSignal.aborted).toBe(true);
    expect(search.contentGroups.value).toEqual([]);
    searchQuery.value = "closed";
    await vi.advanceTimersByTimeAsync(1000);
    expect(api.searchSavedQueries).toHaveBeenCalledTimes(2);
  });

  it("retains the selected object when saved query results arrive ahead of it in the list", async () => {
    const saved = deferred<ContentSearchResult<SavedQueryContentSearchMatch>>();
    vi.mocked(api.searchSavedQueries).mockReturnValueOnce(saved.promise);
    vi.mocked(api.searchObjectSources).mockResolvedValue({ matches: [{ ...location, object_type: "PROCEDURE", object_name: "p", database: "biz", schema: "biz" }], truncated: false });
    const { search, searchQuery } = fixture();
    searchQuery.value = "orders";
    await vi.advanceTimersByTimeAsync(200);
    const selectedId = search.contentSelectedItem.value?.id;
    expect(search.contentSelectedItem.value?.objectName).toBe("p");
    saved.resolve({ matches: [{ ...location, id: "saved", name: "first.sql", connection_id: "conn", database: "biz" }], truncated: false });
    await vi.advanceTimersByTimeAsync(0);
    expect(search.contentGroups.value[0].type).toBe("saved_query");
    expect(search.contentSelectedItem.value?.id).toBe(selectedId);
  });

  it("keeps available query matches when object lookup fails and reports incomplete results", async () => {
    const { search, searchQuery } = fixture("mysql", [{ id: "q", title: "q", mode: "query", connectionId: "conn", database: "biz", sql: "SELECT orders" }]);
    vi.mocked(api.searchSavedQueries).mockResolvedValue({ matches: [], truncated: true });
    vi.mocked(api.searchObjectSources).mockRejectedValue(new Error("Permission denied"));
    searchQuery.value = "orders";
    await vi.advanceTimersByTimeAsync(200);
    expect(search.contentGroups.value[0].matches[0].queryTabId).toBe("q");
    expect(search.contentSearchError.value).toContain("Permission denied");
    expect(search.contentTruncated.value).toBe(true);
    expect(search.contentSearching.value).toBe(false);
  });

  it("searches visible PostgreSQL databases with at most two requests and preserves overload identity", async () => {
    const { search, searchQuery, connectionStore } = fixture("postgres");
    connectionStore.connections[0].visible_database_patterns = ["app_%"];
    connectionStore.connections[0].visible_schemas = { app_one: ["reporting"] };
    vi.mocked(api.listDatabases).mockResolvedValue(["app_one", "app_two", "app_three", "hidden"].map((name) => ({ name })));
    const first = deferred<Awaited<ReturnType<typeof api.searchObjectSources>>>();
    const second = deferred<Awaited<ReturnType<typeof api.searchObjectSources>>>();
    vi.mocked(api.searchObjectSources).mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    searchQuery.value = "orders";
    await vi.advanceTimersByTimeAsync(200);
    expect(api.searchObjectSources).toHaveBeenCalledTimes(2);
    expect(api.searchObjectSources).toHaveBeenNthCalledWith(1, expect.objectContaining({ database: "app_one", schemas: ["reporting"] }), expect.any(AbortSignal));
    first.resolve({ matches: [{ ...location, object_type: "FUNCTION", object_name: "orders", database: "app_one", schema: "reporting", signature: "customer_id integer" }], truncated: false });
    await vi.advanceTimersByTimeAsync(0);
    expect(api.searchObjectSources).toHaveBeenCalledTimes(3);
    second.resolve({ matches: [], truncated: false });
    await vi.advanceTimersByTimeAsync(0);
    expect(search.contentGroups.value[0].matches[0]).toMatchObject({ signature: "customer_id integer", objectName: "orders" });
    expect(search.contentGroups.value[0].header).toBe("orders(customer_id integer)");
    expect(api.searchObjectSources).not.toHaveBeenCalledWith(expect.objectContaining({ database: "hidden" }), expect.anything());
  });

  it("refreshes live query content and changes scope when the active connection changes", async () => {
    const { search, searchQuery, tabs, connectionStore } = fixture("sqlite", [{ id: "q", title: "q", mode: "query", connectionId: "conn", database: "biz", sql: "SELECT original" }]);
    searchQuery.value = "original";
    await vi.advanceTimersByTimeAsync(200);
    expect(search.contentGroups.value).toHaveLength(1);
    tabs[0].sql = "SELECT replacement";
    await vi.advanceTimersByTimeAsync(200);
    expect(search.contentGroups.value).toHaveLength(0);
    connectionStore.connections.push({ ...connectionStore.connections[0], id: "second", name: "另一个库" });
    connectionStore.activeConnectionId = "second";
    await vi.advanceTimersByTimeAsync(200);
    expect(api.searchSavedQueries).toHaveBeenLastCalledWith(expect.objectContaining({ connection_id: "second" }), expect.any(AbortSignal));
    expect(search.contentScopeLabel.value).toBe("另一个库");
  });

  it("caps total results and signals that a large query was truncated", async () => {
    const { search, searchQuery } = fixture("sqlite", [{ id: "q", title: "q", mode: "query", connectionId: "conn", sql: "orders\n".repeat(501) }]);
    searchQuery.value = "orders";
    await vi.advanceTimersByTimeAsync(200);
    expect(search.selectableContentItems.value).toHaveLength(500);
    expect(search.contentTruncated.value).toBe(true);
  });
});

describe("literal editor content matching", () => {
  it("preserves UTF-16 columns across emoji, CRLF and multiple occurrences", () => {
    expect(findContentMatches("😀订单 订单\r\n订单", "订单", 10)).toEqual([
      { line: 1, column: 3, lineText: "😀订单 订单", matchText: "订单", highlightIndices: [2, 4] },
      { line: 1, column: 6, lineText: "😀订单 订单", matchText: "订单", highlightIndices: [5, 7] },
      { line: 2, column: 1, lineText: "订单", matchText: "订单", highlightIndices: [0, 2] },
    ]);
  });
  it("treats regular expression metacharacters as literal text and uses original case", () => {
    expect(findContentMatches("a.b axb A.B", "a.b", 10).map((match) => [match.column, match.matchText])).toEqual([
      [1, "a.b"],
      [9, "A.B"],
    ]);
  });
});
