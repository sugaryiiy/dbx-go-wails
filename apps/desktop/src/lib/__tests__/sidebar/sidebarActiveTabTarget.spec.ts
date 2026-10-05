import { describe, expect, it } from "vitest";
import { activeTabSidebarTarget, matchesTarget } from "@/lib/sidebar/sidebarActiveTabTarget";
import type { QueryTab, TreeNode } from "@/types/database";

describe("sidebar active-tab targets", () => {
  it("locates an existing table from its structure details without using the decorated title", () => {
    const target = activeTabSidebarTarget({ mode: "structure", connectionId: "pg-1", database: "app", schema: "public", structureTableName: "orders.with.dots", title: "public.orders.with.dots (Structure)" } as QueryTab);
    expect(target).toEqual({ type: "table", connectionId: "pg-1", database: "app", schema: "public", tableName: "orders.with.dots" });
    expect(matchesTarget({ id: "orders", label: "orders.with.dots", type: "table", connectionId: "pg-1", database: "app", schema: "public" }, target!)).toBe(true);
    expect(activeTabSidebarTarget({ mode: "structure", connectionId: "pg-1", database: "app", title: "Create table" } as QueryTab)).toBeNull();
  });

  it("selects the connection node for a database-browser tab", () => {
    const target = activeTabSidebarTarget({ mode: "databases", connectionId: "pg-1" } as QueryTab);

    expect(target).toEqual({ type: "connection", connectionId: "pg-1" });
    expect(matchesTarget({ id: "connection:pg-1", label: "Postgres", type: "connection", connectionId: "pg-1" } as TreeNode, target!)).toBe(true);
    expect(matchesTarget({ id: "connection:pg-2", label: "Other", type: "connection", connectionId: "pg-2" } as TreeNode, target!)).toBe(false);
  });

  it("keeps structure navigation in its explicitly selected catalog", () => {
    const target = activeTabSidebarTarget({ mode: "structure", connectionId: "doris", catalog: "hive", database: "analytics", structureTableName: "orders" } as QueryTab);
    expect(target).toMatchObject({ type: "table", catalog: "hive" });
    const node = { id: "hive:orders", label: "orders", type: "table", connectionId: "doris", catalog: "hive", database: "analytics" } as TreeNode;
    expect(matchesTarget(node, target!)).toBe(true);
    expect(matchesTarget({ ...node, catalog: "iceberg" }, target!)).toBe(false);
    expect(matchesTarget({ ...node, catalog: undefined }, target!)).toBe(false);
  });

  it("selects the Meilisearch system-management leaf for its dedicated tab", () => {
    const target = activeTabSidebarTarget({ mode: "meilisearch-system", connectionId: "meili-1" } as QueryTab);
    expect(target).toEqual({ type: "meilisearch-system", connectionId: "meili-1" });
    expect(matchesTarget({ id: "meili-system", label: "meilisearch.systemManagement", type: "meilisearch-system", connectionId: "meili-1" }, target!)).toBe(true);
  });

  it.each([undefined, "hive"])("preserves the data table catalog, preferring metadata when present (%s)", (metadataCatalog) => {
    const target = activeTabSidebarTarget({
      mode: "data",
      connectionId: "doris",
      catalog: "iceberg",
      database: "analytics",
      title: "orders",
      tableMeta: { tableName: "orders", catalog: metadataCatalog, columns: [], primaryKeys: [] },
    } as QueryTab);
    const catalog = metadataCatalog || "iceberg";
    expect(target).toMatchObject({ type: "table", catalog });
    const node = { id: "orders", label: "orders", type: "table", connectionId: "doris", database: "analytics", catalog } as TreeNode;
    expect(matchesTarget(node, target!)).toBe(true);
    expect(matchesTarget({ ...node, catalog: "other" }, target!)).toBe(false);
  });

  it("locates a query context only in its exact catalog", () => {
    const target = activeTabSidebarTarget({ mode: "query", connectionId: "doris-1", catalog: "hive", database: "analytics" } as QueryTab);
    const hiveDatabase = { id: "hive:analytics", label: "analytics", type: "database", connectionId: "doris-1", catalog: "hive", database: "analytics" } as TreeNode;
    const icebergDatabase = { ...hiveDatabase, id: "iceberg:analytics", catalog: "iceberg" };
    const defaultDatabase = { ...hiveDatabase, id: "default:analytics", catalog: undefined };

    expect(matchesTarget(hiveDatabase, target!)).toBe(true);
    expect(matchesTarget(icebergDatabase, target!)).toBe(false);
    expect(matchesTarget(defaultDatabase, target!)).toBe(false);
  });

  it("uses data-tab table metadata as the locate identity after a reload", () => {
    const target = activeTabSidebarTarget({
      mode: "data",
      connectionId: "dm-1",
      database: "SERVICE_DB",
      schema: "APP_OWNER",
      title: "ORDERS",
      sql: "SELECT * FROM APP_OWNER.ORDERS",
      tableMeta: {
        database: "APP_OWNER",
        schema: "APP_OWNER",
        tableName: "ORDERS",
        tableType: "TABLE",
        columns: [{ name: "ID", data_type: "INTEGER", is_nullable: false, column_default: null, is_primary_key: true, extra: null }],
        primaryKeys: ["ID"],
      },
    } as QueryTab);

    expect(target).toEqual({ type: "table", connectionId: "dm-1", database: "APP_OWNER", schema: "APP_OWNER", tableName: "ORDERS" });
    expect(matchesTarget({ id: "dm-1:APP_OWNER:APP_OWNER:tables:ORDERS", label: "ORDERS", type: "table", connectionId: "dm-1", database: "APP_OWNER", schema: "APP_OWNER" } as TreeNode, target!)).toBe(true);
    expect(matchesTarget({ id: "dm-1:SERVICE_DB", label: "SERVICE_DB", type: "database", connectionId: "dm-1", database: "SERVICE_DB" } as TreeNode, target!)).toBe(false);
  });
});
