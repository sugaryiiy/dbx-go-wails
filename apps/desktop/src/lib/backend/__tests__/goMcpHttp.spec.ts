import { afterEach, describe, expect, it, vi } from "vitest";
import { loadMcpHttpServerSettings, saveMcpHttpServerSettings, mcpHttpServerStatus, rotateMcpHttpServerToken, loadWebMcpHttpStatus, saveWebMcpHttpSettings, rotateWebMcpToken } from "../http";

const configuration = { enabled: true, host: "127.0.0.1", port: 5225, path: "/mcp", allowRemote: false, allowedHosts: [], allowedOrigins: [] };
const status = { enabled: true, running: true, endpoint: "http://127.0.0.1:5225/mcp", accessToken: "test-token", lastError: null, recentLogs: [] };
afterEach(() => vi.unstubAllGlobals());

function mockResponse(value: unknown, status = 200) {
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

describe("Go MCP service management transport", () => {
  it.each([
    [loadMcpHttpServerSettings, "/api/app-settings/mcp-http-server", configuration],
    [mcpHttpServerStatus, "/api/app-settings/mcp-http-server/status", status],
    [loadWebMcpHttpStatus, "/api/app-settings/mcp-http-status", { enabled: true, endpointPath: "/mcp" }],
  ] as const)("loads backend state for %s", async (load, path, response) => {
    const fetch = mockResponse(response);
    expect(await load()).toEqual(response);
    expect(fetch.mock.calls[0]?.[0]).toBe(path);
  });

  it("saves desktop settings and returns actual listener state", async () => {
    const fetch = mockResponse(status);
    expect(await saveMcpHttpServerSettings(configuration)).toEqual(status);
    expect(fetch).toHaveBeenCalledWith("/api/app-settings/mcp-http-server", expect.objectContaining({ method: "PUT", body: JSON.stringify(configuration), headers: expect.objectContaining({ "X-DBX-MCP-Settings": "1" }) }));
  });

  it("saves web settings with the explicit management header", async () => {
    const fetch = mockResponse({ enabled: true });
    const settings = { enabled: true, allowedHosts: ["dbx.example.com"], allowedOrigins: [] };
    await saveWebMcpHttpSettings(settings);
    expect(fetch).toHaveBeenCalledWith("/api/app-settings/mcp-http", expect.objectContaining({ method: "PUT", body: JSON.stringify(settings), headers: expect.objectContaining({ "X-DBX-MCP-Settings": "1" }) }));
  });

  it.each([
    [rotateMcpHttpServerToken, "/api/app-settings/mcp-http-server/rotate-token"],
    [rotateWebMcpToken, "/api/app-settings/mcp-http/rotate-token"],
  ] as const)("rotates tokens through %s", async (rotate, path) => {
    const fetch = mockResponse(status);
    expect(await rotate()).toEqual(status);
    expect(fetch).toHaveBeenCalledWith(path, { method: "POST", headers: { "X-DBX-MCP-Settings": "1" } });
  });

  it("does not turn listener failures into a successful running status", async () => {
    mockResponse({ error: "MCP port is already in use" }, 409);
    await expect(saveMcpHttpServerSettings(configuration)).rejects.toThrow("MCP port is already in use");
  });
});
