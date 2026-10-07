/** Client templates never embed the live bearer token. */
export type GoMcpConfigFormat = "http" | "vscode" | "codex" | "stdio";
export const GO_MCP_TOKEN_ENV = "DBX_MCP_TOKEN";
export const GO_MCP_TOKEN_PLACEHOLDER = "replace-with-your-mcp-token";

export function buildGoMcpConfig(endpoint: string, format: GoMcpConfigFormat): string {
  if (format === "codex") {
    return `[mcp_servers.dbx]\nurl = ${JSON.stringify(endpoint)}\nbearer_token_env_var = "${GO_MCP_TOKEN_ENV}"`;
  }
  if (format === "stdio") {
    return JSON.stringify(
      {
        mcpServers: {
          dbx: {
            command: "/absolute/path/to/dbx-mcp-go",
            args: ["--url", endpoint],
            env: { [GO_MCP_TOKEN_ENV]: GO_MCP_TOKEN_PLACEHOLDER },
          },
        },
      },
      null,
      2,
    );
  }
  const server = {
    type: "http",
    url: endpoint,
    headers: { Authorization: `Bearer ${GO_MCP_TOKEN_PLACEHOLDER}` },
  };
  return JSON.stringify(format === "vscode" ? { servers: { dbx: server } } : { mcpServers: { dbx: server } }, null, 2);
}

export function mcpEndpointUrl(origin: string, endpointPath: string, apiPath = "/api"): string {
  // Go reports its local /mcp route; include a reverse proxy's public prefix.
  const prefix = new URL(apiPath, origin).pathname.replace(/\/api\/?$/, "");
  const path = endpointPath === "/mcp" ? `${prefix}/mcp` : endpointPath;
  return new URL(path, origin).toString();
}
