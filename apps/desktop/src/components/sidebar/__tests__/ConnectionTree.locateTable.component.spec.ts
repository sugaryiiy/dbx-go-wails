// @vitest-environment happy-dom

import { createApp, h, nextTick, ref } from "vue";
import { createPinia, setActivePinia } from "pinia";
import { createI18n } from "vue-i18n";
import { afterEach, describe, expect, it, vi } from "vitest";
import ConnectionTree from "../ConnectionTree.vue";
import { useConnectionStore } from "@/stores/connectionStore";
import { useSettingsStore } from "@/stores/settingsStore";
import en from "@/i18n/locales/en";
import type { ConnectionConfig, QueryTab, TreeNode } from "@/types/database";

const cleanups: Array<() => void> = [];
afterEach(() => {
  for (const cleanup of cleanups.splice(0).reverse()) cleanup();
  vi.restoreAllMocks();
});

async function mountTree(localSearch: boolean) {
  const pinia = createPinia();
  setActivePinia(pinia);
  const store = useConnectionStore();
  const settings = useSettingsStore();
  settings.editorSettings.sidebarObjectDisplay = "simple";
  settings.editorSettings.sidebarTableSearchEnabled = true;
  settings.editorSettings.sidebarTableSearchLocal = localSearch;
  store.connections = [{ id: "sqlite", name: "SQLite", db_type: "sqlite", database: "app" }] as ConnectionConfig[];
  store.connectedIds.add("sqlite");
  const tables: TreeNode[] = Array.from({ length: 15 }, (_, index) => ({ id: `sqlite:app:table_${index}`, label: `table_${index}`, type: "table", connectionId: "sqlite", database: "app" }));
  const target = { id: "sqlite:app:orders.with.dots", label: "orders.with.dots", type: "table", connectionId: "sqlite", database: "app" } as TreeNode;
  const database: TreeNode = { id: "sqlite:app", label: "app", type: "database", connectionId: "sqlite", database: "app", isExpanded: false, children: [...tables, target] };
  store.treeNodes = [{ id: "sqlite", label: "SQLite", type: "connection", connectionId: "sqlite", isExpanded: false, children: [database] }];
  store.setSidebarTableSearchQuery(database.id, "table_");
  store.setSidebarTableSearchQuery("unrelated:database", "keep-this-filter");
  store.connectionMultiSelectActive = true;
  store.selectedTreeNodeIds = ["sqlite"];
  vi.spyOn(store, "canUseLoadedTreeNodeToggle").mockImplementation((node) => !!node.children?.length);
  vi.spyOn(store, "loadSidebarTableStorage").mockResolvedValue(undefined);
  const refresh = vi.spyOn(store, "refreshSidebarTableSearch").mockImplementation(async () => {
    // A remote clear restores page one, which may omit the requested table.
    store.treeNodes[0]!.children![0]!.children = [...tables];
  });
  const loadTarget = vi.spyOn(store, "loadTableForLocate").mockImplementation(async () => {
    store.treeNodes[0]!.children![0]!.children!.push(target);
    return true;
  });
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(100);
  vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(1000);
  const tree = ref<InstanceType<typeof ConnectionTree>>();
  const host = document.createElement("div");
  document.body.append(host);
  const app = createApp({ render: () => h(ConnectionTree, { ref: tree }) });
  app.use(pinia);
  app.use(createI18n({ legacy: false, locale: "en", messages: { en }, missingWarn: false, fallbackWarn: false }));
  app.mount(host);
  cleanups.push(() => {
    app.unmount();
    host.remove();
  });
  await nextTick();
  return { store, tree, host, target, refresh, loadTarget };
}

describe("locating a table from its details", () => {
  it.each([
    [true, "structure"],
    [false, "structure"],
    [true, "data"],
    [false, "data"],
  ] as const)("never selects another catalog's same-named table when loaded = %s from %s", async (targetLoaded, mode) => {
    const { store, tree, loadTarget } = await mountTree(true);
    store.connections[0]!.db_type = "doris";
    store.sidebarTableSearchQueries = {};
    const catalogNode = (catalog: string, loaded: boolean): TreeNode => ({
      id: catalog,
      label: catalog,
      type: "doris-catalog",
      connectionId: "sqlite",
      catalog,
      children: [
        {
          id: `${catalog}:app`,
          label: "app",
          type: "database",
          connectionId: "sqlite",
          database: "app",
          catalog,
          children: loaded ? [{ id: `${catalog}:orders`, label: "orders", type: "table", connectionId: "sqlite", database: "app", catalog }] : [],
        },
      ],
    });
    store.treeNodes[0]!.children = [catalogNode("other", true), catalogNode("hive", targetLoaded)];
    const loadDatabases = vi.spyOn(store, "loadDatabases").mockResolvedValue(undefined);
    vi.spyOn(store, "loadDorisCatalogDatabases").mockResolvedValue(undefined);
    const loadCatalog = vi.spyOn(store, "loadDorisCatalogTables").mockResolvedValue(undefined);
    await tree.value!.locateTabInSidebar({
      id: "details",
      mode,
      connectionId: "sqlite",
      database: "app",
      ...(mode === "structure" ? { catalog: "hive", structureTableName: "orders" } : { tableMeta: { tableName: "orders", catalog: "hive", columns: [], primaryKeys: [] } }),
    } as QueryTab);
    expect(store.selectedTreeNodeId).not.toBe("other:orders");
    expect(loadTarget).not.toHaveBeenCalled();
    if (targetLoaded) {
      expect(store.selectedTreeNodeId).toBe("hive:orders");
    } else {
      expect(loadDatabases).toHaveBeenCalled();
      expect(loadCatalog).toHaveBeenCalled();
      expect(loadCatalog.mock.calls.every(([node]) => node.catalog === "hive")).toBe(true);
    }
  });

  it.each([true, false])("reveals a filtered table and expands its parents with local search = %s", async (localSearch) => {
    const { store, tree, host, target, refresh, loadTarget } = await mountTree(localSearch);
    expect(host.querySelector(`[data-node-id="${target.id}"]`)).toBeNull();
    await tree.value!.locateTabInSidebar({ id: "details", mode: "structure", connectionId: "sqlite", database: "app", structureTableName: target.label } as QueryTab);
    await nextTick();
    expect(store.sidebarTableSearchQueries).toEqual({ "unrelated:database": "keep-this-filter" });
    expect(store.treeNodes[0]!.isExpanded).toBe(true);
    expect(store.treeNodes[0]!.children![0]!.isExpanded).toBe(true);
    expect(store.selectedTreeNodeIds).toEqual([target.id]);
    expect(store.selectedTreeNodeId).toBe(target.id);
    expect(store.treeSelectionAnchorId).toBe(target.id);
    expect(store.connectionMultiSelectActive).toBe(false);
    expect(host.querySelector(`[data-node-id="${target.id}"]`)).not.toBeNull();
    expect(host.querySelector<HTMLElement>(".connection-tree-scroller")!.scrollTop).toBeGreaterThan(0);
    expect(refresh).toHaveBeenCalledTimes(localSearch ? 0 : 1);
    expect(loadTarget).toHaveBeenCalledTimes(localSearch ? 0 : 1);
  });
});
