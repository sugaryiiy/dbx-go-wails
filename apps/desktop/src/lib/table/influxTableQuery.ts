import type { ColumnInfo, ConnectionConfig } from "@/types/database";

/** Prefer the configured protocol when a proxy does not report a server version. */
export function influxTableQueryOptions(config?: ConnectionConfig, columns: readonly Pick<ColumnInfo, "name" | "extra">[] = []): { influxDbVersion?: "1" | "2"; influxDbTagRenames?: Record<string, string>; influxDbFieldRenames?: Record<string, string> } {
  if (config?.db_type !== "influxdb") return {};
  const external = config.external_config;
  const version = external && typeof external === "object" && "version" in external ? String(external.version).trim().replace(/^v/i, "") : undefined;
  const protocol = version === "1" || version === "2" ? version : /^v?2\./i.test(config.database_info?.productVersion ?? "") ? "2" : "1";
  const tags: Record<string, string> = Object.create(null);
  const fields: Record<string, string> = Object.create(null);
  for (const column of columns) {
    if (column.extra?.startsWith("influxdb:tag:")) tags[column.extra.slice("influxdb:tag:".length)] = column.name;
    if (column.extra?.startsWith("influxdb:field:")) fields[column.extra.slice("influxdb:field:".length)] = column.name;
  }
  return { influxDbVersion: protocol, ...(Object.keys(tags).length ? { influxDbTagRenames: tags } : {}), ...(Object.keys(fields).length ? { influxDbFieldRenames: fields } : {}) };
}
