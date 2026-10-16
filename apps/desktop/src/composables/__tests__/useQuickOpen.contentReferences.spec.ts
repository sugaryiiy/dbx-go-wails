import { effectScope, reactive, type EffectScope } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useQuickOpen } from "@/composables/useQuickOpen";
import { useConnectionStore } from "@/stores/connectionStore";
import { useSavedSqlStore } from "@/stores/savedSqlStore";
import { useQueryStore } from "@/stores/queryStore";
import * as api from "@/lib/backend/api";
import type { QuickOpenItem } from "@/composables/useQuickOpen";

vi.mock("@/stores/connectionStore", () => ({ useConnectionStore: vi.fn() }));
vi.mock("@/stores/savedSqlStore", () => ({ useSavedSqlStore: vi.fn() }));
vi.mock("@/stores/queryStore", () => ({ useQueryStore: vi.fn() }));
vi.mock("@/lib/backend/api", () => ({
  listPlugins: vi.fn(),
  listSqlFilesInFolder: vi.fn(),
  searchSavedQueries: vi.fn(),
  searchObjectSources: vi.fn(),
  listDatabases: vi.fn(),
}));
vi.mock("@/lib/globalSearch/globalSearchSettings", async () => {
  const { ref } = await import("vue");
  return { composeGlobalSearchRoots: () => [], getGlobalSearchExtensions: () => ["sql"], globalSearchSettingsVersion: ref(0) };
});
vi.mock("@/lib/sqlFile/sqlFileFolders", async () => {
  const { ref } = await import("vue");
  return { getSqlFileFolderPaths: () => [], getSqlFileFilter: () => "*.sql", sqlFileFoldersVersion: ref(0) };
});

const scopes: EffectScope[] = [];
const location = { line: 3, column: 15, line_text: "SELECT * FROM orders;", match_text: "orders", start: 14, end: 20 };
function fixture(nodes: any[] = [{ type: "table", label: "orders", database: "biz" }]) {
  const store = reactive({
    activeConnectionId: "conn",
    connectedIds: new Set(["conn"]),
    connections: [{ id: "conn", name: "Business", db_type: "mysql", database: "biz" }],
    treeNodes: [{ id: "conn", type: "connection", connectionId: "conn", children: nodes.map((node) => ({ ...node, connectionId: "conn" })) }],
    ensureConnected: vi.fn().mockResolvedValue(undefined),
    listCompletionTables: vi.fn().mockResolvedValue([]),
  });
  vi.mocked(useConnectionStore).mockReturnValue(store as any);
  const scope = effectScope();
  scopes.push(scope);
  const search = scope.run(() => useQuickOpen())!;
  search.setSearchActive(true);
  return search;
}

describe("table references in ordinary global search", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.mocked(useSavedSqlStore).mockReturnValue({ allFiles: [], version: 0 } as any);
    vi.mocked(useQueryStore).mockReturnValue({ tabs: [] } as any);
    vi.mocked(api.listPlugins).mockResolvedValue([]);
    vi.mocked(api.listDatabases).mockResolvedValue([{ name: "biz" }]);
    vi.mocked(api.searchSavedQueries).mockResolvedValue({ matches: [], truncated: false });
    vi.mocked(api.searchObjectSources).mockResolvedValue({ matches: [], warnings: [], truncated: false });
  });
  afterEach(() => {
    scopes.splice(0).forEach((scope) => scope.stop());
    vi.useRealTimers();
    vi.clearAllMocks();
  });

  it("lists the table and SQL references even when the procedure, event and query names differ", async () => {
    vi.mocked(useQueryStore).mockReturnValue({
      tabs: [
        { id: "draft", title: "Draft", mode: "query", connectionId: "conn", sql: "SELECT * FROM orders;" },
        { id: "foreign", title: "Other connection", mode: "query", connectionId: "other", sql: "SELECT * FROM orders;" },
      ],
    } as any);
    vi.mocked(api.searchSavedQueries).mockResolvedValue({ matches: [{ ...location, id: "report", name: "Daily report", connection_id: "conn", database: "biz" }], truncated: false });
    vi.mocked(api.searchObjectSources).mockResolvedValue({
      matches: [
        { ...location, object_type: "PROCEDURE", object_name: "refresh_daily", database: "biz", schema: "biz" },
        { ...location, line: 8, object_type: "PROCEDURE", object_name: "refresh_daily", database: "biz", schema: "biz" },
        { ...location, object_type: "EVENT", object_name: "nightly_task", database: "biz", schema: "biz" },
      ],
      warnings: [],
      truncated: false,
    });
    const search = fixture();
    search.setQuery("orders");
    await vi.advanceTimersByTimeAsync(200);

    expect(search.contentMode.value).toBe(false);
    expect(search.filteredItems.value.map((item) => item.label)).toEqual(["orders", "Draft", "Daily report", "nightly_task", "refresh_daily"]);
    expect(search.filteredItems.value.find((item) => item.label === "refresh_daily")).toMatchObject({ type: "content_match", objectType: "PROCEDURE", line: 3, lineText: "SELECT * FROM orders;", highlightIndices: [14, 20] });
    expect(api.searchObjectSources).toHaveBeenCalledWith(expect.objectContaining({ connection_id: "conn", query: "orders" }), expect.any(AbortSignal));
    expect(api.searchSavedQueries).toHaveBeenCalledWith(expect.objectContaining({ first_match_only: true }), expect.any(AbortSignal));
    expect(search.contentSearching.value).toBe(false);
  });

  it("upgrades a matching procedure name to a single source result with its reveal position", async () => {
    vi.mocked(api.searchObjectSources).mockResolvedValue({ matches: [{ ...location, object_type: "PROCEDURE", object_name: "orders_report", database: "biz", schema: "biz" }], warnings: [], truncated: false });
    const search = fixture([{ type: "procedure", label: "orders_report", database: "biz" }]);
    search.setQuery("orders");
    await vi.advanceTimersByTimeAsync(200);

    expect(search.filteredItems.value).toHaveLength(1);
    expect(search.filteredItems.value[0]).toMatchObject({ label: "orders_report", type: "content_match", objectName: "orders_report", line: 3 });
  });

  it("keeps the selected named result when content sources publish empty or partial results", async () => {
    const search = fixture(["orders", "orders_a", "orders_b"].map((label) => ({ type: "table", label, database: "biz" })));
    search.setQuery("orders");
    search.selectedIndex.value = 2;
    const selectedId = search.selectedItem.value?.id;
    await vi.advanceTimersByTimeAsync(200);

    expect(search.selectedItem.value?.id).toBe(selectedId);
    expect(search.selectedIndex.value).toBe(2);
  });

  it("retains the selected reference as later sources insert rows before it", async () => {
    let resolveSaved!: (result: Awaited<ReturnType<typeof api.searchSavedQueries>>) => void;
    vi.mocked(api.searchSavedQueries).mockReturnValue(
      new Promise((resolve) => {
        resolveSaved = resolve;
      }),
    );
    vi.mocked(api.searchObjectSources).mockResolvedValue({ matches: [{ ...location, object_type: "PROCEDURE", object_name: "refresh_daily", database: "biz", schema: "biz" }], warnings: [], truncated: false });
    const search = fixture();
    search.setQuery("orders");
    await vi.advanceTimersByTimeAsync(200);
    search.selectNext();
    const selected = search.selectedItem.value;
    expect(selected?.label).toBe("refresh_daily");
    resolveSaved({ matches: [{ ...location, id: "report", name: "A report", connection_id: "conn", database: "biz" }], truncated: false });
    await vi.advanceTimersByTimeAsync(0);

    expect(search.selectedItem.value?.id).toBe(selected?.id);
    expect(search.selectedIndex.value).toBe(2);
    search.selectPrevious();
    expect(search.selectedItem.value?.label).toBe("A report");
  });

  it("does no SQL definition work for an empty or closed search and aborts on close", async () => {
    const search = fixture();
    await vi.advanceTimersByTimeAsync(200);
    expect(api.searchObjectSources).not.toHaveBeenCalled();
    search.setQuery("orders");
    await vi.advanceTimersByTimeAsync(200);
    const signal = vi.mocked(api.searchObjectSources).mock.calls[0][1]!;
    search.setSearchActive(false);
    expect(signal.aborted).toBe(true);
    search.setQuery("customers");
    await vi.advanceTimersByTimeAsync(200);
    expect(api.searchObjectSources).toHaveBeenCalledTimes(1);
    expect(search.filteredItems.value.some((item: QuickOpenItem) => item.type === "content_match")).toBe(false);
  });

  it("keeps SQL references visible when table-name matches fill the result limit", async () => {
    vi.mocked(api.searchObjectSources).mockResolvedValue({ matches: [{ ...location, object_type: "PROCEDURE", object_name: "refresh_daily", database: "biz", schema: "biz" }], warnings: [], truncated: false });
    const search = fixture(Array.from({ length: 210 }, (_, index) => ({ type: "table", label: `orders_${index}`, database: "biz" })));
    search.setQuery("orders");
    await vi.advanceTimersByTimeAsync(200);

    expect(search.filteredItems.value).toHaveLength(200);
    expect(search.filteredItems.value.at(-1)).toMatchObject({ type: "content_match", objectName: "refresh_daily" });
  });

  it("keeps other sources visible when one draft repeats the table name over 500 times", async () => {
    vi.mocked(useQueryStore).mockReturnValue({ tabs: [{ id: "draft", title: "Large draft", mode: "query", connectionId: "conn", sql: "SELECT * FROM orders;\n".repeat(501) }] } as any);
    vi.mocked(api.searchObjectSources).mockResolvedValue({ matches: [{ ...location, object_type: "PROCEDURE", object_name: "refresh_daily", database: "biz", schema: "biz" }], warnings: [], truncated: false });
    const search = fixture();
    search.setQuery("orders");
    await vi.advanceTimersByTimeAsync(200);

    expect(search.filteredItems.value.map((item) => item.label)).toEqual(["orders", "Large draft", "refresh_daily"]);
    expect(search.contentTruncated.value).toBe(false);
    expect(api.searchObjectSources).toHaveBeenCalledWith(expect.objectContaining({ first_match_only: true }), expect.any(AbortSignal));
    search.setContentMode(true);
    await vi.advanceTimersByTimeAsync(200);
    expect(search.contentGroups.value[0].matches).toHaveLength(500);
    expect(search.contentTruncated.value).toBe(true);
    expect(api.searchObjectSources).toHaveBeenLastCalledWith(expect.not.objectContaining({ first_match_only: true }), expect.any(AbortSignal));
  });

  it("keeps the selected table in the list when late references reserve result slots", async () => {
    let resolveObjects!: (result: Awaited<ReturnType<typeof api.searchObjectSources>>) => void;
    vi.mocked(api.searchObjectSources).mockReturnValue(
      new Promise((resolve) => {
        resolveObjects = resolve;
      }),
    );
    const search = fixture(Array.from({ length: 210 }, (_, index) => ({ type: "table", label: `orders_${index}`, database: "biz" })));
    search.setQuery("orders");
    await vi.advanceTimersByTimeAsync(200);
    search.selectedIndex.value = 199;
    const selectedId = search.selectedItem.value?.id;
    resolveObjects({ matches: Array.from({ length: 50 }, (_, index) => ({ ...location, object_type: "PROCEDURE" as const, object_name: `refresh_daily_${index}`, database: "biz", schema: "biz" })), warnings: [], truncated: false });
    await vi.advanceTimersByTimeAsync(0);

    expect(search.filteredItems.value).toHaveLength(200);
    expect(search.selectedItem.value?.id).toBe(selectedId);
    expect(search.selectedIndex.value).toBe(149);
    expect(search.filteredItems.value.filter((item) => item.type === "content_match")).toHaveLength(50);
  });
});
