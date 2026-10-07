/** Capabilities exposed by the Go MCP implementation. Keep aligned with tools/list. */
export const GO_MCP_TOOL_NAMES = [
  "dbx_list_connections",
  "dbx_list_databases",
  "dbx_list_tables",
  "dbx_describe_table",
  "dbx_get_table_ddl",
  "dbx_list_indexes",
  "dbx_get_schema_context",
  "dbx_execute_query",
  "dbx_execute_batch",
  "dbx_list_saved_sql",
  "dbx_get_saved_sql",
  "dbx_save_sql",
  "dbx_search_history",
] as const;

export function toggleGoMcpAllowedToolName(current: readonly string[] | null, name: string, allowed: boolean): string[] {
  const selected = new Set<string>((current ?? GO_MCP_TOOL_NAMES).filter((item) => GO_MCP_TOOL_NAMES.includes(item as (typeof GO_MCP_TOOL_NAMES)[number])));
  if (allowed && GO_MCP_TOOL_NAMES.includes(name as (typeof GO_MCP_TOOL_NAMES)[number])) selected.add(name);
  else selected.delete(name);
  return [...selected];
}

export function goMcpToolLabel(name: string, chinese: boolean): string {
  const labels: Record<string, [string, string]> = {
    dbx_list_connections: ["列出连接", "List connections"],
    dbx_list_databases: ["列出数据库", "List databases"],
    dbx_list_tables: ["列出表", "List tables"],
    dbx_describe_table: ["查看表结构", "Describe table"],
    dbx_get_table_ddl: ["获取表 DDL", "Get table DDL"],
    dbx_list_indexes: ["列出索引", "List indexes"],
    dbx_get_schema_context: ["获取结构上下文", "Get schema context"],
    dbx_execute_query: ["执行 SQL 查询", "Execute SQL query"],
    dbx_execute_batch: ["原子批量执行 SQL", "Execute atomic SQL batch"],
    dbx_list_saved_sql: ["列出保存的 SQL", "List saved SQL"],
    dbx_get_saved_sql: ["读取保存的 SQL", "Get saved SQL"],
    dbx_save_sql: ["保存 SQL", "Save SQL"],
    dbx_search_history: ["搜索执行历史", "Search query history"],
  };
  return labels[name]?.[chinese ? 0 : 1] ?? name;
}
