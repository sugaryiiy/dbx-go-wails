import { describe, expect, it } from "vitest";
import { buildGoMcpConfig, GO_MCP_TOKEN_PLACEHOLDER, mcpEndpointUrl } from "@/lib/mcp/goMcpConfigTemplates";
const endpoint = "https://dbx.example.com/tools/dbx/mcp";

describe("Go MCP client templates", () => {
  it("generates standard HTTP JSON with an explicit token placeholder", () => {
    const value = JSON.parse(buildGoMcpConfig(endpoint, "http"));
    expect(value.mcpServers.workbench).toEqual({ type: "http", url: endpoint, headers: { Authorization: `Bearer ${GO_MCP_TOKEN_PLACEHOLDER}` } });
    expect(value.mcpServers.workbench).not.toHaveProperty("command");
  });
  it("uses the VS Code servers root", () => {
    expect(JSON.parse(buildGoMcpConfig(endpoint, "vscode")).servers.workbench.url).toBe(endpoint);
  });
  it("uses an environment token for Codex instead of inline secrets", () => {
    expect(buildGoMcpConfig(endpoint, "codex")).toBe(`[mcp_servers.workbench]\nurl = "${endpoint}"\nbearer_token_env_var = "DBX_MCP_TOKEN"`);
  });
  it("launches only the Go bridge for stdio, never the legacy npm wrapper", () => {
    const config = buildGoMcpConfig(endpoint, "stdio");
    expect(JSON.parse(config).mcpServers.workbench).toEqual({ command: "/absolute/path/to/dbx-mcp-go", args: ["--url", endpoint], env: { DBX_MCP_TOKEN: GO_MCP_TOKEN_PLACEHOLDER } });
    expect(config).not.toMatch(/npm|npx|DBX_WEB_PASSWORD|dbx-mcp-server/);
  });
  it("preserves reverse-proxy base paths and safely quotes endpoint characters", () => {
    expect(mcpEndpointUrl("https://dbx.example.com", "/tools/dbx/mcp")).toBe(endpoint);
    expect(mcpEndpointUrl("https://dbx.example.com", "/mcp", "/tools/dbx/api")).toBe(endpoint);
    expect(JSON.parse(buildGoMcpConfig('https://dbx.example.com/"mcp', "http")).mcpServers.workbench.url).toBe('https://dbx.example.com/"mcp');
  });
});
