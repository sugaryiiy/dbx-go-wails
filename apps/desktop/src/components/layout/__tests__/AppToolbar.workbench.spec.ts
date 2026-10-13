// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from "vue";
import { createPinia } from "pinia";
import { createI18n } from "vue-i18n";
import { afterEach, describe, expect, it, vi } from "vitest";
import en from "@/i18n/locales/en";
import { useSettingsStore } from "@/stores/settingsStore";
import { formatShortcutDisplay } from "@/lib/editor/shortcutDisplay";

vi.mock("@/composables/useWindowControls", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/composables/useWindowControls")>();
  const { ref } = await import("vue");
  return { ...original, useWindowControls: () => ({ isMac: false, isDesktop: false, showControls: false, isMaximized: ref(false), isFullscreen: ref(false), isAlwaysOnTop: ref(false), minimize: vi.fn(), toggleMaximize: vi.fn(), toggleAlwaysOnTop: vi.fn(), close: vi.fn() }) };
});
vi.mock("@/composables/useToast", () => ({ useToast: () => ({ toast: vi.fn() }) }));
vi.mock("@/components/ui/tooltip", () => ({
  Tooltip: { template: "<span><slot /></span>" },
  TooltipTrigger: { template: "<span><slot /></span>" },
  TooltipContent: { template: "<span hidden><slot /></span>" },
}));
vi.mock("@/components/plugins/PluginShortcutToolbar.vue", () => ({ default: { props: { dropdownOnly: Boolean }, template: '<button aria-label="Plugin shortcuts" :data-menu-only="dropdownOnly">Plugins</button>' } }));
vi.mock("@/components/export/ExportProgressPopover.vue", () => ({ default: { template: "<span />" } }));
vi.mock("@/components/layout/ToolbarUpdateIcon.vue", () => ({ default: { props: ["available"], template: '<span :data-update-available="available" />' } }));

import AppToolbar from "../AppToolbar.vue";

const cleanup: Array<() => void> = [];
afterEach(() => {
  cleanup.splice(0).forEach((dispose) => dispose());
  document.body.innerHTML = "";
});

async function mount(overrides: Record<string, unknown> = {}) {
  const props = reactive({
    isDark: false,
    themeMode: "system" as const,
    showSidebarExpand: false,
    showAiPanel: false,
    activeAiRunCount: 0,
    awaitingAiRunCount: 0,
    showHistory: false,
    showSqlLibrary: false,
    sqlLibrarySaveFeedbackId: 0,
    showSqlFilePanel: false,
    showDriverStore: false,
    showPluginCenter: false,
    showSettingsPage: false,
    checkingUpdates: false,
    hasUpdateAvailable: false,
    isDownloadingUpdate: false,
    downloadProgress: null,
    updateReadyToInstall: false,
    updateReady: false,
    agentDriverUpdateCount: 0,
    hasMcpUpdateAvailable: false,
    hasConnections: false,
    canNewQuery: false,
    hasSqlFileConnections: false,
    workspaceName: "Orders · Local",
    databaseName: "sales",
    toolWindowsInRail: false,
    ...overrides,
  });
  const events = { find: vi.fn(), connection: vi.fn(), query: vi.fn(), transfer: vi.fn(), drivers: vi.fn(), library: vi.fn(), history: vi.fn(), files: vi.fn(), ai: vi.fn(), sidebar: vi.fn(), expand: vi.fn() };
  const pinia = createPinia();
  const settings = useSettingsStore(pinia);
  settings.editorSettings.shortcuts.findAction = "Mod+Shift+K";
  Object.assign(settings.editorSettings.toolbarItems, { sqlLibrary: true, sqlFileTree: true, history: true, ai: true, theme: true, dataTransfer: false, driverManager: false, pluginCenter: false, schemaDiff: false, dataCompare: false, sqlFile: false });
  const host = document.createElement("div");
  document.body.append(host);
  const app = createApp({
    render: () =>
      h(AppToolbar, {
        ...props,
        onFindAction: events.find,
        onNewConnection: events.connection,
        onNewQuery: events.query,
        onOpenTransfer: events.transfer,
        onOpenDriverStore: events.drivers,
        onToggleSqlLibrary: events.library,
        onToggleHistory: events.history,
        onToggleSqlFilePanel: events.files,
        onToggleAi: events.ai,
        onToggleSidebar: events.sidebar,
        onExpandSidebar: events.expand,
      }),
  });
  app.use(pinia);
  app.use(createI18n({ legacy: false, locale: "en", messages: { en: { ...en, workbenchLayout: { workspace: "Workspace", tools: "Tools" } } } }));
  app.mount(host);
  await nextTick();
  cleanup.push(() => {
    app.unmount();
    host.remove();
  });
  return { host, props, settings, events };
}

function button(host: ParentNode, label: string) {
  return [...host.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.getAttribute("aria-label") === label)!;
}

async function openTools(host: HTMLElement) {
  button(host, "Tools").click();
  await nextTick();
}

function menuItem(label: string) {
  return [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((item) => item.textContent?.trim() === label)!;
}

describe("IDE command toolbar", () => {
  it("shows workspace context and the configured action-search shortcut, and dispatches the primary actions", async () => {
    const { host, props, settings, events } = await mount();
    expect(host.textContent).toContain("Orders · Local");
    expect(host.textContent).toContain("sales");
    const search = button(host, en.actionSearch.title);
    expect(search.querySelector("kbd")?.textContent).toBe(formatShortcutDisplay("Mod+Shift+K"));
    search.click();
    expect(events.find).toHaveBeenCalledOnce();
    button(host, en.toolbar.newConnection).click();
    expect(events.connection).toHaveBeenCalledOnce();
    expect(button(host, en.toolbar.newQuery).disabled).toBe(true);
    button(host, en.toolbar.newQuery).click();
    expect(events.query).not.toHaveBeenCalled();
    props.canNewQuery = true;
    settings.editorSettings.shortcuts.findAction = "";
    await nextTick();
    button(host, en.toolbar.newQuery).click();
    expect(events.query).toHaveBeenCalledOnce();
    expect(search.querySelector("kbd")).toBeNull();
  });

  it("keeps secondary capabilities in the grouped tools menu with their actual availability", async () => {
    const { host, props, events } = await mount();
    await openTools(host);
    for (const label of [en.transfer.dataTransfer, en.sqlFile.title, en.diff.title, en.dataCompare.title, en.databaseBackup.title, en.toolbar.driverManager, en.toolbar.pluginCenter, en.settings.openMcpSettings]) expect(menuItem(label)).toBeTruthy();
    expect(menuItem(en.transfer.dataTransfer).disabled).toBe(true);
    expect(menuItem(en.sqlFile.title).disabled).toBe(true);
    menuItem(en.transfer.dataTransfer).click();
    expect(events.transfer).not.toHaveBeenCalled();
    props.hasConnections = true;
    props.hasSqlFileConnections = true;
    await nextTick();
    expect(menuItem(en.transfer.dataTransfer).disabled).toBe(false);
    expect(menuItem(en.sqlFile.title).disabled).toBe(false);
    menuItem(en.transfer.dataTransfer).click();
    await nextTick();
    expect(events.transfer).toHaveBeenCalledOnce();
    await openTools(host);
    menuItem(en.toolbar.driverManager).click();
    expect(events.drivers).toHaveBeenCalledOnce();
  });

  it("moves tool windows to the rail on request while retaining standalone toolbar behavior and plugin access", async () => {
    const { host, props, settings, events } = await mount();
    button(host, en.sqlLibrary.title).click();
    button(host, en.sqlFileTree.title).click();
    expect(events.library).toHaveBeenCalledOnce();
    expect(events.files).toHaveBeenCalledOnce();
    expect(host.querySelector("[data-sql-library-trigger]")).toBeTruthy();
    settings.editorSettings.pluginShortcuts.enabled = true;
    settings.editorSettings.pluginShortcuts.position = "toolbar";
    props.toolWindowsInRail = true;
    await nextTick();
    expect(host.querySelector("[data-sql-library-trigger]")).toBeNull();
    expect(button(host, en.sqlFileTree.title)).toBeUndefined();
    expect(button(host, "Plugin shortcuts").getAttribute("data-menu-only")).toBe("true");
    props.toolWindowsInRail = false;
    await nextTick();
    expect(host.querySelector("[data-sql-library-trigger]")).toBeTruthy();
  });

  it("toggles the unified resource navigation in both states while keeping global search available", async () => {
    const { host, props, events } = await mount({ unifiedNavigation: true });
    const toggle = host.querySelector<HTMLButtonElement>("[data-workbench-sidebar-toggle]")!;
    expect(toggle.getAttribute("aria-label")).toBe(en.sidebar.collapse);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    toggle.click();
    expect(events.sidebar).toHaveBeenCalledOnce();
    expect(events.expand).not.toHaveBeenCalled();
    props.showSidebarExpand = true;
    await nextTick();
    expect(toggle.getAttribute("aria-label")).toBe(en.sidebar.expand);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    toggle.click();
    expect(events.sidebar).toHaveBeenCalledTimes(2);
    expect(button(host, en.toolbar.newConnection)).toBeUndefined();
    expect(button(host, en.toolbar.newQuery)).toBeUndefined();
    expect(host.querySelector(".workbench-legacy-windows")).toBeNull();
    button(host, en.actionSearch.title).click();
    expect(events.find).toHaveBeenCalledOnce();
  });

  it("keeps AI independently accessible in unified mode and prioritizes confirmations over active task counts", async () => {
    const { host, props, settings, events } = await mount({ unifiedNavigation: true, activeAiRunCount: 12, awaitingAiRunCount: 2 });
    settings.editorSettings.toolbarItems.ai = false;
    settings.editorSettings.toolbarItems.history = false;
    settings.editorSettings.toolbarItems.sqlLibrary = false;
    await nextTick();
    const ai = host.querySelector<HTMLButtonElement>("[data-workbench-ai-toggle]")!;
    expect(ai.getAttribute("aria-pressed")).toBe("false");
    const badge = ai.querySelector(".workbench-count")!;
    expect(badge.textContent).toBe("2");
    expect(badge.classList.contains("is-awaiting")).toBe(true);
    expect(badge.getAttribute("title")).toContain("awaiting your confirmation");
    ai.click();
    expect(events.ai).toHaveBeenCalledOnce();
    props.showAiPanel = true;
    props.awaitingAiRunCount = 0;
    await nextTick();
    expect(ai.getAttribute("aria-pressed")).toBe("true");
    expect(badge.textContent).toBe("9+");
    expect(badge.classList.contains("is-awaiting")).toBe(false);
    await openTools(host);
    expect(menuItem("AI")).toBeUndefined();
    expect(menuItem(en.history.title)).toBeUndefined();
    expect(menuItem(en.sqlLibrary.title)).toBeUndefined();
    expect(menuItem(en.toolbar.driverManager)).toBeTruthy();
  });

  it("retains the legacy expand-only sidebar action outside unified mode", async () => {
    const { host, events } = await mount({ showSidebarExpand: true });
    button(host, en.sidebar.expand).click();
    expect(events.expand).toHaveBeenCalledOnce();
    expect(events.sidebar).not.toHaveBeenCalled();
  });

  it.each([
    ["sqlLibrary", en.sqlLibrary.title, "library"],
    ["sqlFileTree", en.sqlFileTree.title, "files"],
    ["history", en.history.title, "history"],
    ["ai", "AI", "ai"],
  ] as const)("keeps the hidden %s tool window available through Tools", async (key, label, event) => {
    const { host, settings, events } = await mount({ toolWindowsInRail: true });
    const findItem = () => [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((item) => item.textContent?.trim().startsWith(label));
    await openTools(host);
    expect(findItem()).toBeUndefined();
    settings.editorSettings.toolbarItems[key] = false;
    await nextTick();
    expect(findItem()).toBeTruthy();
    findItem()!.click();
    await nextTick();
    expect(events[event]).toHaveBeenCalledOnce();
    settings.editorSettings.toolbarItems[key] = true;
    await openTools(host);
    expect(findItem()).toBeUndefined();
  });
});
