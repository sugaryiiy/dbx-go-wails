import { isWailsRuntime } from "./wailsRuntime";

export const GO_CONNECTION_PROFILES = new Set(["sqlite", "mysql", "mariadb", "postgres", "redis", "mongodb", "clickhouse"]);

export function isGoBackend(): boolean {
  return import.meta.env.VITE_DBX_BACKEND === "go" || isWailsRuntime();
}

/** Availability of visual table writers; executing SQL has its own permission checks. */
export function supportsBackendTableEditing(dbType?: string): boolean {
  return !isGoBackend() || dbType !== "clickhouse";
}

/** Formats whose current-page writer works without a backend formatting route. */
export function goClickHouseGridExportActions(context: string | undefined, hasQueryExport: boolean, completeLocalResult: boolean): ReadonlySet<string> {
  const actions = new Set(["page-csv", "page-xlsx", "page-xlsx-with-sql", "page-txt", "selected-csv", "selected-xlsx", "selected-xlsx-with-sql", "selected-txt", "all-results-xlsx", "all-results-xlsx-with-sql"]);
  if (context !== "table-data") {
    actions.add("csv");
    actions.add("xlsx");
    actions.add("txt");
    if (hasQueryExport && !completeLocalResult) actions.add("json");
  }
  return actions;
}

/** Whole-table/database export uses routes not yet implemented for Go ClickHouse. */
export function supportsBackendTableExport(dbType?: string): boolean {
  return !isGoBackend() || dbType !== "clickhouse";
}
