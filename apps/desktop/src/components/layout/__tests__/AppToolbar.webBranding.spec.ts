// @vitest-environment happy-dom
import { createApp, nextTick } from "vue";
import { createPinia, setActivePinia } from "pinia";
import { createI18n } from "vue-i18n";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

let mockIsTauriRuntime = false;

vi.mock("@/lib/backend/tauriRuntime", () => ({
  isTauriRuntime: () => mockIsTauriRuntime,
}));
vi.mock("@tauri-apps/api/core", () => ({ invoke: vi.fn(async () => undefined) }));
vi.mock("@tauri-apps/api/event", () => ({
  listen: vi.fn(async () => () => {}),
  emit: vi.fn(async () => undefined),
}));
vi.mock("@tauri-apps/api/webviewWindow", () => ({
  getAllWebviewWindows: vi.fn(async () => []),
  WebviewWindow: { getByLabel: vi.fn(async () => null) },
}));
vi.mock("@tauri-apps/api/window", () => ({
  getCurrentWindow: () => ({
    label: "main",
    isMaximized: async () => false,
    isFullscreen: async () => false,
    isAlwaysOnTop: async () => false,
    setAlwaysOnTop: async () => {},
    onResized: async () => () => {},
    onFocusChanged: async () => () => {},
    minimize: async () => {},
    toggleMaximize: async () => {},
  }),
}));

vi.mock("@/components/ui/button", () => ({
  Button: { name: "ButtonStub", props: ["ariaLabel"], template: `<button :aria-label="ariaLabel"><slot /></button>` },
}));
vi.mock("@/components/ui/tooltip", () => ({
  Tooltip: { name: "TooltipStub", template: `<span><slot /></span>` },
  TooltipTrigger: { name: "TooltipTriggerStub", template: `<span><slot /></span>` },
  TooltipContent: { name: "TooltipContentStub", template: `<span><slot /></span>` },
}));
vi.mock("@/components/ui/LightDropdown.vue", () => ({
  default: { name: "LightDropdownStub", template: `<div />` },
}));
vi.mock("@/components/layout/WindowControls.vue", () => ({
  default: { name: "WindowControlsStub", template: `<div />` },
}));
vi.mock("@/components/export/ExportProgressPopover.vue", () => ({
  default: { name: "ExportProgressPopoverStub", template: `<div />` },
}));
vi.mock("@/components/layout/ToolbarUpdateIcon.vue", () => ({
  default: { name: "ToolbarUpdateIconStub", template: `<span />` },
}));

import AppToolbar from "../AppToolbar.vue";

const defaultToolbarProps = {
  isDark: false,
  themeMode: "light" as const,
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
};

let pinia: ReturnType<typeof createPinia>;

function mount(props: Record<string, unknown> = {}) {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const app = createApp(AppToolbar, {
    ...defaultToolbarProps,
    ...props,
  });
  app.use(pinia);
  app.use(
    createI18n({
      legacy: false,
      locale: "en",
      missingWarn: false,
      fallbackWarn: false,
      messages: {
        en: {
          app: { name: "Database Workbench" },
          toolbar: {
            newConnection: "New Connection",
            newQuery: "New Query",
          },
        },
      },
    }),
  );
  app.mount(host);
  return {
    host,
    unmount: () => {
      app.unmount();
      host.remove();
    },
  };
}

describe("AppToolbar web branding (#8703)", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    mockIsTauriRuntime = false;
    pinia = createPinia();
    setActivePinia(pinia);
  });

  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("renders the workbench name and icon without a promotional link in web mode", async () => {
    mockIsTauriRuntime = false;
    const { host, unmount } = mount();
    await nextTick();

    const brandEl = host.querySelector('[data-testid="workbench-brand"]');
    expect(brandEl).not.toBeNull();
    expect(brandEl?.querySelector("a")).toBeNull();
    expect(brandEl?.getAttribute("href")).toBeNull();
    expect(brandEl?.textContent).toContain("Database Workbench");

    const logoImg = brandEl?.querySelector("img");
    expect(logoImg).not.toBeNull();
    expect(logoImg?.getAttribute("alt")).toBe("Database Workbench");
    expect(logoImg?.getAttribute("src")).toContain("workbench.svg");

    unmount();
  });

  it("keeps the workbench name and icon without the upstream website link in desktop mode", async () => {
    mockIsTauriRuntime = true;
    const { host, unmount } = mount();
    await nextTick();

    const brandEl = host.querySelector('[data-testid="web-brand-logo"]');
    expect(brandEl).toBeNull();
    expect(host.querySelector('[data-testid="workbench-brand"]')?.textContent).toContain("Database Workbench");

    unmount();
  });
});
