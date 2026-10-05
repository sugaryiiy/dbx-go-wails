// @vitest-environment happy-dom

import { createApp, h, nextTick } from "vue";
import { createPinia, setActivePinia } from "pinia";
import { createI18n } from "vue-i18n";
import { afterEach, describe, expect, it, vi } from "vitest";
import AppSidebar from "../AppSidebar.vue";
import { useConnectionStore } from "@/stores/connectionStore";
import { useSettingsStore } from "@/stores/settingsStore";
import en from "@/i18n/locales/en";
import type { ConnectionConfig } from "@/types/database";

const cleanups: Array<() => void> = [];
afterEach(() => {
  for (const cleanup of cleanups.splice(0).reverse()) cleanup();
  vi.restoreAllMocks();
});

async function mountSidebar() {
  const pinia = createPinia();
  setActivePinia(pinia);
  const connections = useConnectionStore();
  const settings = useSettingsStore();
  settings.editorSettings.sidebarGlobalSearchLocal = false;
  settings.editorSettings.sidebarConnectionSortMode = "manual";
  vi.spyOn(settings, "updateEditorSettings").mockImplementation((patch) => Object.assign(settings.editorSettings, patch));
  connections.connections = [
    { id: "zulu", name: "Zulu", db_type: "sqlite", database: "/tmp/zulu.db", read_only: false },
    { id: "alpha", name: "Alpha", db_type: "sqlite", database: "/tmp/alpha.db", read_only: false },
  ] as ConnectionConfig[];
  connections.treeNodes = connections.connections.map((item) => ({ id: item.id, label: item.name, type: "connection", connectionId: item.id, expanded: false, children: [] }));
  const host = document.createElement("div");
  document.body.append(host);
  const onImport = vi.fn();
  const onExport = vi.fn();
  const app = createApp({ render: () => h(AppSidebar, { sidebarWidth: 280, classicLayout: true, onImport, onExport }) });
  app.use(pinia);
  app.use(createI18n({ legacy: false, locale: "en", messages: { en }, missingWarn: false, fallbackWarn: false }));
  app.mount(host);
  cleanups.push(() => {
    app.unmount();
    host.remove();
  });
  await nextTick();
  return { host, connections, settings, onImport, onExport };
}

async function openMore(host: HTMLElement) {
  host.querySelector<HTMLButtonElement>("[data-sidebar-more-actions] button")!.click();
  await nextTick();
  const menu = document.querySelector<HTMLElement>('[role="menu"]');
  expect(menu).not.toBeNull();
  return menu!;
}

function findAction(menu: HTMLElement, label: string) {
  const button = [...menu.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((item) => item.textContent?.trim() === label);
  expect(button, label).toBeDefined();
  return button!;
}

async function selectAction(host: HTMLElement, label: string) {
  findAction(await openMore(host), label).click();
  await nextTick();
}

function visibleConnections(host: HTMLElement) {
  return [...host.querySelectorAll<HTMLElement>(".connection-tree-content [data-node-id]")].map((node) => node.dataset.nodeId);
}

describe("Database tool window", () => {
  it("routes all four imports and export from the actual more menu", async () => {
    const { host, onImport, onExport } = await mountSidebar();
    expect(host.querySelector("[data-sidebar-tool-window-header]")?.textContent).toContain(en.connection.database);
    expect(host.querySelector("[data-sidebar-connection-count]")?.textContent).toBe("2");
    expect(host.querySelector("[data-sidebar-search] input")).not.toBeNull();
    for (const [source, label] of [
      ["dbx", en.sidebar.importDbx],
      ["navicat", en.sidebar.importNavicat],
      ["dbeaver", en.sidebar.importDbeaver],
      ["datagrip", en.sidebar.importDatagrip],
    ]) {
      await selectAction(host, label);
      expect(onImport).toHaveBeenLastCalledWith(source);
    }
    await selectAction(host, en.sidebar.export);
    expect(onExport).toHaveBeenCalledOnce();
  });

  it("applies sort and active-connection filtering to the real tree through the more menu", async () => {
    const { host, connections, settings } = await mountSidebar();
    expect(visibleConnections(host)).toEqual(["zulu", "alpha"]);
    await selectAction(host, en.sidebar.sortConnectionsAscending);
    expect(settings.editorSettings.sidebarConnectionSortMode).toBe("asc");
    expect(visibleConnections(host)).toEqual(["alpha", "zulu"]);
    connections.connectedIds.add("alpha");
    await selectAction(host, en.sidebar.showActiveConnectionsOnly);
    expect(visibleConnections(host)).toEqual(["alpha"]);
    expect(host.querySelector("[data-sidebar-more-actions]")?.className).toContain("sidebar-tool-window-more--active");
    const menu = await openMore(host);
    expect(findAction(menu, en.sidebar.disconnectAllActiveConnections).disabled).toBe(false);
    findAction(menu, en.sidebar.showActiveConnectionsOnly).click();
    await nextTick();
    expect(visibleConnections(host)).toEqual(["alpha", "zulu"]);
  });

  it("keeps local and regex search settings accessible and disables local toggling in regex mode", async () => {
    const { host, settings } = await mountSidebar();
    await selectAction(host, en.sidebar.globalLocalSearch);
    expect(settings.editorSettings.sidebarGlobalSearchLocal).toBe(true);
    await selectAction(host, en.sidebar.regexSearch);
    const menu = await openMore(host);
    expect(findAction(menu, en.sidebar.globalLocalSearch).disabled).toBe(true);
    menu.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    await nextTick();
    const search = host.querySelector<HTMLInputElement>("[data-sidebar-search] input")!;
    search.value = "[";
    search.dispatchEvent(new Event("input", { bubbles: true }));
    await nextTick();
    expect(search.getAttribute("aria-invalid")).toBe("true");
    host.querySelector<HTMLButtonElement>(".connection-tree-search-clear")!.click();
    await nextTick();
    expect(search.value).toBe("");
    expect(search.getAttribute("aria-invalid")).toBe("false");
  });
});
