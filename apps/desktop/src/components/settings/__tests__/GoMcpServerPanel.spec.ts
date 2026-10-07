// @vitest-environment happy-dom
import { createApp, nextTick, ref, type App } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
const state = vi.hoisted(() => ({ desktop: true }));
const api = vi.hoisted(() => ({ loadMcpHttpServerSettings: vi.fn(), saveMcpHttpServerSettings: vi.fn(), mcpHttpServerStatus: vi.fn(), rotateMcpHttpServerToken: vi.fn(), loadWebMcpHttpStatus: vi.fn(), saveWebMcpHttpSettings: vi.fn(), rotateWebMcpToken: vi.fn() }));
vi.mock("@/lib/backend/api", () => api);
vi.mock("@/lib/backend/wailsRuntime", () => ({ isWailsRuntime: () => state.desktop }));
vi.mock("@/lib/common/clipboard", () => ({ copyToClipboard: vi.fn().mockResolvedValue(undefined) }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ locale: ref("en") }) }));
import GoMcpServerPanel from "../GoMcpServerPanel.vue";
const configuration = { enabled: false, host: "127.0.0.1", port: 5225, path: "/mcp", allowRemote: false, allowedHosts: [], allowedOrigins: [] };
const stopped = { enabled: false, running: false, endpoint: null, accessToken: null, lastError: null, recentLogs: [] };
const running = { ...stopped, enabled: true, running: true, endpoint: "http://127.0.0.1:5225/mcp", accessToken: "private-test-token" };
const web = { enabled: false, endpointPath: "/tools/dbx/mcp", allowedHosts: ["localhost:3000"], allowedOrigins: [], tokenSource: "managed", managementAvailable: true, deploymentManaged: false, accessToken: null };
let app: App | undefined;
let root: HTMLElement;
const flush = async () => {
  await Promise.resolve();
  await nextTick();
  await Promise.resolve();
  await nextTick();
};
async function mount(configured = true) {
  root = document.createElement("div");
  document.body.appendChild(root);
  app = createApp(GoMcpServerPanel, { policyConfigured: configured });
  app.mount(root);
  await flush();
  return root;
}
function button(selector: string) {
  return root.querySelector<HTMLButtonElement>(selector)!;
}
async function toggle() {
  button("#go-mcp-enabled").click();
  await flush();
}
async function edit(id: string, value: string) {
  const input = root.querySelector<HTMLInputElement>(id)!;
  input.value = value;
  input.dispatchEvent(new Event("input", { bubbles: true }));
  await flush();
}
beforeEach(() => {
  vi.clearAllMocks();
  state.desktop = true;
  api.loadMcpHttpServerSettings.mockResolvedValue({ ...configuration });
  api.mcpHttpServerStatus.mockResolvedValue({ ...stopped });
  api.saveMcpHttpServerSettings.mockResolvedValue({ ...running });
  api.loadWebMcpHttpStatus.mockResolvedValue({ ...web });
  api.saveWebMcpHttpSettings.mockResolvedValue({ ...web, enabled: true, accessToken: "web-test-token" });
  api.rotateMcpHttpServerToken.mockResolvedValue({ ...running, accessToken: "rotated-token" });
});
afterEach(() => {
  app?.unmount();
  app = undefined;
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("Go MCP service panel", () => {
  it("loads the Wails listener rather than misclassifying it as Web", async () => {
    await mount();
    expect(api.loadMcpHttpServerSettings).toHaveBeenCalledOnce();
    expect(api.mcpHttpServerStatus).toHaveBeenCalledOnce();
    expect(api.loadWebMcpHttpStatus).not.toHaveBeenCalled();
    expect(root.textContent).toContain("Stopped");
  });
  it("requires applying a policy before the listener can start", async () => {
    await mount(false);
    await toggle();
    expect(button('[data-testid="mcp-save"]').disabled).toBe(true);
    expect(root.textContent).toContain("Choose and apply the access policy");
    expect(api.saveMcpHttpServerSettings).not.toHaveBeenCalled();
  });
  it("starts the desktop listener and produces secret-free HTTP configuration", async () => {
    await mount();
    await toggle();
    button('[data-testid="mcp-save"]').click();
    await flush();
    expect(api.saveMcpHttpServerSettings).toHaveBeenCalledWith({ ...configuration, enabled: true });
    expect(root.textContent).toContain("Running");
    const config = root.querySelector('[data-testid="mcp-client-config"]')!.textContent!;
    expect(config).toContain(running.endpoint);
    expect(config).not.toContain(running.accessToken);
    expect(root.querySelector<HTMLInputElement>("#go-mcp-token")!.type).toBe("password");
  });
  it("rejects a remote desktop binding before attempting to save it", async () => {
    await mount();
    await toggle();
    await edit("#go-mcp-host", "0.0.0.0");
    expect(button('[data-testid="mcp-save"]').disabled).toBe(true);
    expect(root.textContent).toContain("loopback addresses only");
    expect(api.saveMcpHttpServerSettings).not.toHaveBeenCalled();
  });
  it("keeps the stopped status when the port cannot be bound", async () => {
    api.saveMcpHttpServerSettings.mockRejectedValueOnce(new Error("Address already in use"));
    await mount();
    await toggle();
    button('[data-testid="mcp-save"]').click();
    await flush();
    expect(root.querySelector('[role="alert"]')!.textContent).toContain("Address already in use");
    expect(root.querySelector('[role="status"]')!.textContent).toContain("Stopped");
  });
  it("supports Web settings and preserves a reverse-proxy endpoint path", async () => {
    state.desktop = false;
    await mount();
    await toggle();
    button('[data-testid="mcp-save"]').click();
    await flush();
    expect(api.loadMcpHttpServerSettings).not.toHaveBeenCalled();
    expect(api.saveWebMcpHttpSettings).toHaveBeenCalledWith({ enabled: true, allowedHosts: ["localhost:3000"], allowedOrigins: [] });
    expect(root.querySelector('[data-testid="mcp-client-config"]')!.textContent).toContain("/tools/dbx/mcp");
  });
  it("rotates only after confirmation and hides the new token", async () => {
    api.loadMcpHttpServerSettings.mockResolvedValueOnce({ ...configuration, enabled: true });
    api.mcpHttpServerStatus.mockResolvedValueOnce(running);
    const confirm = vi.fn().mockReturnValue(false);
    window.confirm = confirm;
    await mount();
    button('[data-testid="mcp-rotate"]').click();
    await flush();
    expect(api.rotateMcpHttpServerToken).not.toHaveBeenCalled();
    confirm.mockReturnValue(true);
    button('[data-testid="mcp-rotate"]').click();
    await flush();
    expect(api.rotateMcpHttpServerToken).toHaveBeenCalledOnce();
    expect(root.querySelector<HTMLInputElement>("#go-mcp-token")!.value).toBe("rotated-token");
    expect(root.querySelector<HTMLInputElement>("#go-mcp-token")!.type).toBe("password");
  });
});
