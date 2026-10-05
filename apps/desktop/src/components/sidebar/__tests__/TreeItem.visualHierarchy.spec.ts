// @vitest-environment happy-dom

import { createApp, h, nextTick, ref } from "vue";
import { createPinia, setActivePinia } from "pinia";
import { createI18n } from "vue-i18n";
import { afterEach, describe, expect, it, vi } from "vitest";
import TreeItem from "../TreeItem.vue";
import { useConnectionStore } from "@/stores/connectionStore";
import { useSettingsStore } from "@/stores/settingsStore";
import { createSidebarTreeRuntime, sidebarTreeRuntimeKey } from "@/lib/sidebar/sidebarTreeRuntime";
import type { ConnectionConfig, TreeNode } from "@/types/database";
import en from "@/i18n/locales/en";

const cleanups: Array<() => void> = [];
afterEach(() => {
  for (const cleanup of cleanups.splice(0).reverse()) cleanup();
});

async function mountItem(initialNode: TreeNode) {
  const pinia = createPinia();
  setActivePinia(pinia);
  const connectionStore = useConnectionStore();
  const settings = useSettingsStore();
  settings.editorSettings.sidebarObjectInfoMode = "comment-inline";
  const connection = { id: "local", name: "Local warehouse", db_type: "sqlite", database: "/tmp/local.db", color: "#16a34a" } as ConnectionConfig;
  connectionStore.connections = [connection];
  connectionStore.connectedIds.add(connection.id);
  connectionStore.activeConnectionId = connection.id;
  connectionStore.selectedTreeNodeId = initialNode.id;
  const node = ref(initialNode);
  const runtime = createSidebarTreeRuntime();
  const onRowClick = vi.fn();
  runtime.bindHost({
    buildContextMenu: vi.fn(() => []),
    handleRowClick: onRowClick,
    handleRowDoubleClick: vi.fn(),
    handleRowKeydown: vi.fn(),
    openPrimaryVisibleFilter: vi.fn(),
    openDataInNewTab: vi.fn(),
    requestPaste: vi.fn(() => false),
    toggleNode: vi.fn(),
  });
  const host = document.createElement("div");
  document.body.append(host);
  const app = createApp({ render: () => h(TreeItem, { node: node.value, depth: node.value.type === "connection" ? 0 : 2 }) });
  app.use(pinia);
  app.use(createI18n({ legacy: false, locale: "en", messages: { en }, missingWarn: false, fallbackWarn: false }));
  app.provide(sidebarTreeRuntimeKey, runtime);
  app.mount(host);
  cleanups.push(() => {
    app.unmount();
    host.remove();
    runtime.dispose();
  });
  await nextTick();
  return { host, node, connectionStore, onRowClick };
}

function connectionNode(): TreeNode {
  return { id: "local", label: "Local warehouse", type: "connection", connectionId: "local", children: [] };
}

describe("TreeItem visual hierarchy", () => {
  it("keeps custom color on a connection marker while preserving selection and the connected indicator", async () => {
    const { host, connectionStore, onRowClick } = await mountItem(connectionNode());
    const row = host.querySelector<HTMLElement>('[data-node-id="local"]')!;
    const marker = row.querySelector<HTMLElement>("[data-tree-connection-color]")!;
    expect(marker.style.backgroundColor).toBe("#16a34a");
    expect(marker.getAttribute("aria-hidden")).toBe("true");
    expect(row.classList.contains("tree-item-active")).toBe(true);
    expect(row.querySelector(".tree-connection-label")?.textContent).toBe("Local warehouse");
    expect(row.querySelector("[data-tree-connected-indicator]")).not.toBeNull();
    expect(row.style.getPropertyValue("--tree-connection-active-bg")).toBe("");
    expect(row.style.getPropertyValue("--tree-connection-row-bg")).toBe("");
    row.click();
    expect(onRowClick).toHaveBeenCalledOnce();

    connectionStore.connectedIds.delete("local");
    connectionStore.connections[0].color = "#dc2626";
    await nextTick();
    expect(row.querySelector("[data-tree-connected-indicator]")).toBeNull();
    expect(marker.style.backgroundColor).toBe("#dc2626");
    expect(row.classList.contains("tree-item-active")).toBe(true);
  });

  it("does not leak a connection's color marker or title weight when its virtual row is reused for a table", async () => {
    const { host, node, connectionStore } = await mountItem(connectionNode());
    expect(host.querySelector("[data-tree-connection-color]")).not.toBeNull();
    node.value = { id: "orders", label: "orders", type: "table", connectionId: "local", database: "main", comment: "Order records" };
    connectionStore.selectedTreeNodeId = "orders";
    await nextTick();
    const row = host.querySelector<HTMLElement>('[data-node-id="orders"]')!;
    expect(row.querySelector("[data-tree-connection-color]")).toBeNull();
    expect(row.querySelector(".tree-connection-label")).toBeNull();
    expect(row.querySelector(".tree-object-label")?.textContent).toBe("orders");
    expect(row.querySelector(".sidebar-object-comment")?.textContent).toBe("Order records");
    expect(row.classList.contains("tree-item-active")).toBe(true);
    expect(row.style.cssText).not.toContain("#16a34a");
    expect(row.style.cssText).not.toContain("tree-connection");
  });

  it("retains container labels, metadata counts, and multi-selection independently of color", async () => {
    const { host, connectionStore } = await mountItem({ id: "tables", label: "tree.tables", type: "group-tables", connectionId: "local", database: "main", objectCount: 42, children: [] });
    connectionStore.connectionMultiSelectActive = true;
    connectionStore.selectedTreeNodeIds = ["tables", "orders"];
    await nextTick();
    const row = host.querySelector<HTMLElement>('[data-node-id="tables"]')!;
    expect(row.querySelector(".tree-container-label")).not.toBeNull();
    expect(row.querySelector(".tree-object-count")?.textContent).toBe("42");
    expect(row.classList.contains("tree-item-active--selection-set")).toBe(true);
    expect(row.querySelector("[data-tree-connection-color]")).toBeNull();
    expect(row.style.cssText).not.toContain("tree-connection");
  });

  it("keeps invalid and disabled object indicators alongside neutral object text", async () => {
    const { host } = await mountItem({ id: "audit", label: "audit", type: "trigger", connectionId: "local", database: "main", valid: false, meta: { name: "audit", timing: "AFTER", event: "UPDATE", enabled: false } });
    expect(host.querySelector("[data-invalid-object-indicator]")).not.toBeNull();
    expect(host.querySelector("[data-disabled-trigger-indicator]")).not.toBeNull();
    expect(host.querySelector("[data-tree-connection-color]")).toBeNull();
    expect(host.querySelector(".tree-object-label")?.textContent).toContain("audit");
  });

  it("keeps per-table search neutral under a colored connection", async () => {
    const { host } = await mountItem({ id: "table-search", label: "grid.search", type: "table-search-control", connectionId: "local", tableSearchParentId: "tables" });
    const search = host.querySelector<HTMLElement>("[data-sidebar-table-search-control]")!;
    expect(search.style.cssText).not.toContain("#16a34a");
    expect(search.style.getPropertyValue("--tree-table-search-row-bg")).toBe("");
    expect(search.querySelector("input")).not.toBeNull();
  });
});
