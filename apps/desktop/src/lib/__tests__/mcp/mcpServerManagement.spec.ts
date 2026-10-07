import { describe, expect, it } from "vitest";
import { checkMcpServerStatus, installMcpServer, uninstallMcpServer } from "@/lib/backend/http";
import { GO_MCP_TOOL_NAMES, goMcpToolLabel, toggleGoMcpAllowedToolName } from "@/lib/mcp/goMcpPolicy";

describe("Go MCP management capabilities", () => {
  it("labels query tools as SQL-only and batch execution as atomic", () => {
    expect(goMcpToolLabel("dbx_execute_query", true)).toBe("执行 SQL 查询");
    expect(goMcpToolLabel("dbx_execute_query", false)).toBe("Execute SQL query");
    expect(goMcpToolLabel("dbx_execute_batch", true)).toBe("原子批量执行 SQL");
    expect(GO_MCP_TOOL_NAMES.map((name) => goMcpToolLabel(name, true)).join(" ")).not.toMatch(/Mongo|Redis|插件/);
  });

  it("reports the built-in service without recommending legacy packages", async () => {
    const status = await checkMcpServerStatus();
    expect(status.installed).toBe(true);
    expect(status.update_available).toBe(false);
    expect(status.install_command).toBe("");
    expect(status.uninstall_command).toBe("");
    expect(status.npm_available).toBe(false);
    await expect(installMcpServer()).rejects.toThrow("built into DBX Go");
    await expect(uninstallMcpServer()).rejects.toThrow("Disable it in MCP settings");
  });
  it("materializes a tool allowlist using only the implemented Go tools", () => {
    const names = toggleGoMcpAllowedToolName(null, "dbx_execute_query", false);
    expect(names).toHaveLength(12);
    expect(names).not.toContain("dbx_execute_query");
    expect(names).not.toContain("dbx_*__*");
    expect(names).toContain("dbx_get_table_ddl");
    expect(names).toContain("dbx_list_indexes");
    expect(toggleGoMcpAllowedToolName(names, "dbx_execute_query", true).sort()).toEqual([...GO_MCP_TOOL_NAMES].sort());
  });
  it("does not add unsupported plugin and session tool names", () => {
    expect(toggleGoMcpAllowedToolName([], "dbx_open_session", true)).toEqual([]);
    expect(toggleGoMcpAllowedToolName(["dbx_*__*", "dbx_list_tables"], "dbx_list_tables", true)).toEqual(["dbx_list_tables"]);
  });
});
