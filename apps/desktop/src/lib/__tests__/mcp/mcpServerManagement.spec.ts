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
    await expect(installMcpServer()).rejects.toThrow("built into Database Workbench Go");
    await expect(uninstallMcpServer()).rejects.toThrow("Disable it in MCP settings");
  });
  it("materializes a tool allowlist using only the implemented Go tools", () => {
    const names = toggleGoMcpAllowedToolName(null, "dbx_execute_query", false);
    expect(names).toHaveLength(GO_MCP_TOOL_NAMES.length - 1);
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
  it("keeps existing explicit permissions exact when object tools become available", () => {
    const existing = ["dbx_list_connections", "dbx_list_tables", "dbx_execute_query"];
    const next = toggleGoMcpAllowedToolName(existing, "dbx_execute_query", false);
    expect(next).toEqual(["dbx_list_connections", "dbx_list_tables"]);
    expect(next).not.toContain("dbx_list_routines");
    expect(next).not.toContain("dbx_get_routine_source");
    expect(next).not.toContain("dbx_list_events");
    expect(next).not.toContain("dbx_get_object_source");
    expect(toggleGoMcpAllowedToolName(next, "dbx_list_routines", true)).toEqual([...next, "dbx_list_routines"]);
    expect(existing).toEqual(["dbx_list_connections", "dbx_list_tables", "dbx_execute_query"]);
  });
  it("permits granting event details independently from event source and SQL execution", () => {
    expect(toggleGoMcpAllowedToolName([], "dbx_get_event_info", true)).toEqual(["dbx_get_event_info"]);
    expect(toggleGoMcpAllowedToolName(["dbx_list_events"], "dbx_get_object_source", false)).toEqual(["dbx_list_events"]);
    expect(goMcpToolLabel("dbx_list_events", true)).toBe("列出 MySQL 定时事件");
    expect(goMcpToolLabel("dbx_list_events", false)).toBe("List MySQL scheduled events");
  });
});
