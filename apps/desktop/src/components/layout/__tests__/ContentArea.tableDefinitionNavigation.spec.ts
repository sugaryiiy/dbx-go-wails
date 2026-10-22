// @vitest-environment happy-dom

import { createApp, defineComponent, h, nextTick, type App } from "vue";
import { createPinia, setActivePinia } from "pinia";
import { createI18n } from "vue-i18n";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ConnectionConfig, QueryTab } from "@/types/database";
import type { SqlObjectNavigationTarget } from "@/lib/sql/sqlNavigation";

const target: SqlObjectNavigationTarget = { name: "order.details", database: "warehouse", schema: "sales", type: "table" };

vi.mock("splitpanes", () => ({
  Splitpanes: { template: "<div><slot /></div>" },
  Pane: { template: "<div><slot /></div>" },
}));
vi.mock("@/components/editor/QueryEditor.vue", () => ({
  __esModule: true,
  default: defineComponent({
    props: { connectionId: String },
    emits: ["click-table"],
    setup(props, { emit }) {
      return () => h("button", { "data-editor-connection": props.connectionId, onClick: () => emit("click-table", target) }, "Open table details");
    },
  }),
}));
vi.mock("@/components/structure/TableStructureEditor.vue", () => ({
  __esModule: true,
  default: defineComponent({
    props: { tableName: String },
    emits: ["locate-table"],
    setup(props, { emit }) {
      return () => h("button", { "data-locate-definition": props.tableName, onClick: () => emit("locate-table") }, "Locate table");
    },
  }),
}));
vi.mock("@/components/grid/DataGrid.vue", () => ({ default: { render: () => null } }));
vi.mock("@/components/layout/EditorToolbar.vue", () => ({ default: { render: () => null } }));
vi.mock("@/components/layout/EditorGroupTabBar.vue", () => ({ default: { render: () => null } }));
vi.mock("@/components/layout/QueryResultSurface.vue", () => ({ default: { render: () => null } }));

import SqlEditorWorkspace from "@/components/layout/SqlEditorWorkspace.vue";
import { useConnectionStore } from "@/stores/connectionStore";
import { useQueryStore } from "@/stores/queryStore";
import { activeTabSidebarTarget } from "@/lib/sidebar/sidebarActiveTabTarget";

const mounted: Array<{ app: App; host: HTMLElement }> = [];

afterEach(() => {
  for (const { app, host } of mounted.splice(0)) {
    app.unmount();
    host.remove();
  }
  localStorage.clear();
});

async function mountWorkspace(mode: "query" | "structure") {
  const pinia = createPinia();
  setActivePinia(pinia);
  const sourceConnection = { id: "source-connection", name: "Source", db_type: "postgres" } as ConnectionConfig;
  const focusedConnection = { id: "focused-connection", name: "Focused", db_type: "postgres" } as ConnectionConfig;
  useConnectionStore().connections = [sourceConnection, focusedConnection];
  const store = useQueryStore();
  const sourceTab: QueryTab = {
    id: "source-tab",
    title: "Source tab",
    connectionId: sourceConnection.id,
    database: "warehouse",
    schema: "sales",
    catalog: "source-catalog",
    mode,
    sql: 'select * from sales."order.details"',
    isExecuting: false,
    ...(mode === "structure" ? { structureTableName: "order.details" } : {}),
  };
  const focusedTab: QueryTab = { id: "focused-tab", title: "Other tab", connectionId: focusedConnection.id, database: "other-database", mode: "query", sql: "", isExecuting: false };
  store.tabs = [sourceTab, focusedTab];
  store.groups = [
    { id: "source-group", tabIds: [sourceTab.id], activeTabId: sourceTab.id },
    { id: "focused-group", tabIds: [focusedTab.id], activeTabId: focusedTab.id },
  ];
  store.sizes = [50, 50];
  store.activeTabId = focusedTab.id;
  store.focusedGroupId = "focused-group";
  const onClickTable = vi.fn();
  const onLocateTab = vi.fn();
  const host = document.createElement("div");
  document.body.append(host);
  const app = createApp(SqlEditorWorkspace, {
    activeTab: focusedTab,
    activeConnection: focusedConnection,
    activeOutputView: "result",
    executableSql: "",
    formatSqlRequest: null,
    compressSqlRequest: null,
    selectedSql: "",
    cursorPos: 0,
    blockDangerousRedisCommands: false,
    onClickTable,
    onLocateTab,
  });
  app.use(pinia);
  app.use(createI18n({ legacy: false, locale: "en", messages: { en: {} }, missingWarn: false, fallbackWarn: false }));
  mounted.push({ app, host });
  app.mount(host);
  const selector = mode === "query" ? '[data-editor-connection="source-connection"]' : '[data-locate-definition="order.details"]';
  await vi.waitFor(() => expect(host.querySelector(selector)).not.toBeNull());
  return { store, sourceTab, focusedTab, button: host.querySelector<HTMLButtonElement>(selector)!, onClickTable, onLocateTab };
}

describe("table definition navigation through the workspace", () => {
  it("forwards the clicked editor's source tab and quoted table identity when another group is focused", async () => {
    const { store, sourceTab, focusedTab, button, onClickTable, onLocateTab } = await mountWorkspace("query");
    button.click();
    await nextTick();

    expect(onClickTable).toHaveBeenCalledExactlyOnceWith(sourceTab.id, target);
    expect(onLocateTab).not.toHaveBeenCalled();
    expect(store.activeTabId).toBe(focusedTab.id);
  });

  it("locates the structure tab's table when another group is focused, without opening a data tab", async () => {
    const { store, sourceTab, focusedTab, button, onClickTable, onLocateTab } = await mountWorkspace("structure");
    button.click();
    await nextTick();

    expect(onLocateTab).toHaveBeenCalledExactlyOnceWith(sourceTab);
    expect(activeTabSidebarTarget(onLocateTab.mock.calls[0]![0])).toEqual({ type: "table", catalog: "source-catalog", connectionId: "source-connection", database: "warehouse", schema: "sales", tableName: "order.details" });
    expect(onClickTable).not.toHaveBeenCalled();
    expect(store.tabs).toHaveLength(2);
    expect(store.activeTabId).toBe(focusedTab.id);
    expect(sourceTab.catalog).toBe("source-catalog");
  });
});
