// @vitest-environment happy-dom

import { createApp, h, nextTick, reactive } from "vue";
import { createI18n } from "vue-i18n";
import { afterEach, describe, expect, it, vi } from "vitest";
import WorkbenchStatusBar from "../WorkbenchStatusBar.vue";

const cleanups: Array<() => void> = [];
afterEach(() => {
  for (const cleanup of cleanups.splice(0).reverse()) cleanup();
});

async function mountStatus() {
  const props = reactive({
    connectionName: undefined as string | undefined,
    databaseName: undefined as string | undefined,
    driverLabel: undefined as string | undefined,
    connected: false,
    connectedCount: 0,
    runningCount: 0,
    cancelling: false,
    position: undefined as { line: number; column: number } | undefined,
  });
  const onOpenMcp = vi.fn();
  const host = document.createElement("div");
  document.body.append(host);
  const app = createApp({ render: () => h(WorkbenchStatusBar, { ...props, onOpenMcp }) });
  app.use(
    createI18n({
      legacy: false,
      locale: "en",
      messages: {
        en: {
          workbenchLayout: { statusBar: "Workspace status", ready: "Ready", running: "Running {count}", cancelling: "Cancelling", connected: "Connected", disconnected: "Disconnected", noConnection: "No connection selected", position: "Ln {line}, Col {column}" },
          sidebar: { activeConnectionsStatus: "{count} active connections" },
          settings: { openMcpSettings: "Open MCP settings" },
        },
      },
    }),
  );
  app.mount(host);
  cleanups.push(() => {
    app.unmount();
    host.remove();
  });
  await nextTick();
  return { host, props, onOpenMcp };
}

describe("Workbench status bar", () => {
  it("shows the idle empty workspace without stale driver or cursor information", async () => {
    const { host } = await mountStatus();
    expect(host.querySelector("footer")?.getAttribute("aria-label")).toBe("Workspace status");
    expect(host.querySelector('[role="status"]')?.textContent?.trim()).toBe("Ready");
    expect(host.textContent).toContain("No connection selected");
    expect(host.querySelector(".workbench-status-count")?.textContent).toBe("0 active connections");
    expect(host.querySelector(".workbench-status-context")).toBeNull();
    expect(host.querySelector(".workbench-status-driver")).toBeNull();
    expect(host.querySelector(".workbench-status-position")).toBeNull();
    expect(host.querySelector('[role="status"] .animate-spin')).toBeNull();
  });

  it("updates connection, database, driver, connection state, count, and cursor from live props", async () => {
    const { host, props } = await mountStatus();
    Object.assign(props, { connectionName: "Orders production", databaseName: "orders", driverLabel: "PostgreSQL", connected: true, connectedCount: 3, position: { line: 12, column: 7 } });
    await nextTick();
    const context = host.querySelector(".workbench-status-context")!;
    expect(context.getAttribute("title")).toBe("Orders production / orders");
    expect(context.textContent).toContain("Orders production");
    expect(context.textContent).toContain("orders");
    expect(context.querySelector(".workbench-status-connection-state")?.textContent).toBe("Connected");
    expect(context.querySelector("svg.is-connected")).not.toBeNull();
    expect(host.querySelector(".workbench-status-count")?.textContent).toBe("3 active connections");
    expect(host.querySelector(".workbench-status-driver")?.textContent).toBe("PostgreSQL");
    expect(host.querySelector(".workbench-status-position")?.textContent).toBe("Ln 12, Col 7");

    Object.assign(props, { databaseName: undefined, connected: false, connectedCount: 2, position: { line: 1, column: 1 } });
    await nextTick();
    expect(context.getAttribute("title")).toBe("Orders production");
    expect(context.querySelector(".workbench-status-connection-state")?.textContent).toBe("Disconnected");
    expect(context.querySelector("svg.is-connected")).toBeNull();
    expect(host.querySelector(".workbench-status-position")?.textContent).toBe("Ln 1, Col 1");
    expect(host.querySelector(".workbench-status-count")?.textContent).toBe("2 active connections");

    Object.assign(props, { connectionName: undefined, driverLabel: undefined, position: undefined });
    await nextTick();
    expect(host.querySelector(".workbench-status-context")).toBeNull();
    expect(host.querySelector(".workbench-status-driver")).toBeNull();
    expect(host.querySelector(".workbench-status-position")).toBeNull();
  });

  it("announces running counts and gives cancellation precedence until it finishes", async () => {
    const { host, props } = await mountStatus();
    const status = () => host.querySelector('[role="status"]')!;
    props.runningCount = 2;
    await nextTick();
    expect(status().textContent?.trim()).toBe("Running 2");
    expect(status().querySelector(".animate-spin")).not.toBeNull();
    props.cancelling = true;
    await nextTick();
    expect(status().textContent?.trim()).toBe("Cancelling");
    props.runningCount = 0;
    await nextTick();
    expect(status().textContent?.trim()).toBe("Cancelling");
    expect(status().querySelector(".animate-spin")).not.toBeNull();
    props.cancelling = false;
    await nextTick();
    expect(status().textContent?.trim()).toBe("Ready");
    expect(status().querySelector(".animate-spin")).toBeNull();
  });

  it("opens MCP settings from its named button without requiring a connection", async () => {
    const { host, onOpenMcp } = await mountStatus();
    const button = host.querySelector<HTMLButtonElement>('button[aria-label="Open MCP settings"]')!;
    expect(button.getAttribute("title")).toBe("Open MCP settings");
    button.click();
    expect(onOpenMcp).toHaveBeenCalledOnce();
  });
});
