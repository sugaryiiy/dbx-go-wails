// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from "vue";
import { createI18n } from "vue-i18n";
import { afterEach, describe, expect, it, vi } from "vitest";
import en from "@/i18n/locales/en";
import zhCN from "@/i18n/locales/zh-CN";
import type { ConnectionConfig } from "@/types/database";
import { formatShortcutDisplay } from "@/lib/editor/shortcutDisplay";

vi.mock("@/components/icons/AppLogo.vue", () => ({ default: { template: "<span />" } }));
vi.mock("@/components/icons/DatabaseIcon.vue", () => ({ default: { template: "<span />" } }));
vi.mock("@/components/ui/TruncatedTextTooltip.vue", () => ({ default: { props: ["text"], template: "<span>{{ text }}</span>" } }));

import WelcomeScreen from "../WelcomeScreen.vue";

const cleanup: Array<() => void> = [];
afterEach(() => {
  cleanup.splice(0).forEach((unmount) => unmount());
});

function mount(overrides: Record<string, unknown> = {}, locale = "zh-CN") {
  const onSample = vi.fn();
  const onLibrary = vi.fn();
  const onHistory = vi.fn();
  const onConnection = vi.fn();
  const onSavedSql = vi.fn();
  const onNewQuery = vi.fn();
  const props = reactive({
    connectionStats: { total: 1, connected: 0, types: 1 },
    recentConnections: [{ id: "private", name: "Private payroll connection", host: "payroll.internal", db_type: "postgres", port: 5432 } as ConnectionConfig],
    savedSqlHistoryItems: [{ id: "sensitive", name: "Private salary query", connectionName: "Payroll" }],
    welcomePageMode: "intro" as "intro" | "workspace",
    appVersion: "0.6.33",
    canNewQuery: false,
    sampleWorkspaceAvailable: true,
    openingSampleWorkspace: false,
    ...overrides,
  });
  const host = document.createElement("div");
  document.body.appendChild(host);
  const app = createApp({ render: () => h(WelcomeScreen, { ...props, onOpenSampleWorkspace: onSample, onShowSqlLibrary: onLibrary, onShowHistory: onHistory, onOpenConnectionQuery: onConnection, onOpenSavedSql: onSavedSql, onNewQuery }) });
  app.use(createI18n({ legacy: false, locale, fallbackLocale: "en", messages: { en, "zh-CN": zhCN } }));
  app.mount(host);
  cleanup.push(() => {
    app.unmount();
    host.remove();
  });
  return { host, props, onSample, onLibrary, onHistory, onConnection, onSavedSql, onNewQuery };
}

function button(host: HTMLElement, label: string) {
  return Array.from(host.querySelectorAll("button")).find((element) => element.textContent?.trim() === label);
}

describe("basic workspace welcome", () => {
  it.each(["intro", "workspace"])("opens the sample, SQL Library, and history from the %s welcome", (mode) => {
    const { host, onSample, onLibrary, onHistory } = mount({ welcomePageMode: mode });
    expect(host.querySelectorAll("ol > li")).toHaveLength(6);
    button(host, "打开示例数据库")!.click();
    button(host, "打开 SQL 库")!.click();
    button(host, zhCN.history.title)!.click();
    expect(onSample).toHaveBeenCalledOnce();
    expect(onLibrary).toHaveBeenCalledOnce();
    expect(onHistory).toHaveBeenCalledOnce();
  });

  it("keeps connection details and saved SQL private in intro mode", () => {
    const { host } = mount();
    expect(host.textContent).not.toContain("Private payroll connection");
    expect(host.textContent).not.toContain("payroll.internal");
    expect(host.textContent).not.toContain("Private salary query");
    expect(host.textContent).toContain(zhCN.welcome.introPrivacyHint);
  });

  it("disables the sample action while opening and restores it for retry", async () => {
    const { host, props, onSample } = mount({ openingSampleWorkspace: true });
    const loadingButton = button(host, "正在打开示例…")!;
    expect(loadingButton.disabled).toBe(true);
    loadingButton.click();
    expect(onSample).not.toHaveBeenCalled();
    props.openingSampleWorkspace = false;
    await nextTick();
    button(host, "打开示例数据库")!.click();
    expect(onSample).toHaveBeenCalledOnce();
  });

  it("hides the Web-only sample action on desktop and renders English guide text", () => {
    const { host } = mount({ sampleWorkspaceAvailable: false }, "en");
    expect(button(host, "Open sample database")).toBeUndefined();
    expect(host.textContent).toContain("Import and export files");
    expect(host.textContent).not.toContain("welcome.basics.");
  });

  it("opens the actual recent connection and saved SQL without creating placeholder data", () => {
    const { host, onConnection, onSavedSql } = mount({ welcomePageMode: "workspace" });
    const rows = [...host.querySelectorAll<HTMLButtonElement>("button")];
    rows.find((item) => item.textContent?.includes("Private payroll connection"))!.click();
    rows.find((item) => item.textContent?.includes("Private salary query"))!.click();
    expect(onConnection).toHaveBeenCalledExactlyOnceWith("private");
    expect(onSavedSql).toHaveBeenCalledExactlyOnceWith("sensitive");
  });

  it("respects query availability and presents customized shortcuts without inventing an unbound key", async () => {
    const { host, props, onNewQuery } = mount({ shortcuts: { newQuery: "Mod+K", executeSql: "Mod+Enter", quickOpen: "Mod+O", findAction: "" } });
    expect(button(host, zhCN.toolbar.newQuery)!.disabled).toBe(true);
    button(host, zhCN.toolbar.newQuery)!.click();
    expect(onNewQuery).not.toHaveBeenCalled();
    props.canNewQuery = true;
    await nextTick();
    button(host, zhCN.toolbar.newQuery)!.click();
    expect(onNewQuery).toHaveBeenCalledOnce();
    expect([...host.querySelectorAll("kbd")].map((item) => item.textContent)).toContain(formatShortcutDisplay("Mod+K"));
    expect([...host.querySelectorAll("dt")].some((item) => item.textContent === zhCN.actionSearch.title)).toBe(false);
  });
});
