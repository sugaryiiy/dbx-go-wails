import { afterEach, describe, expect, it, vi } from "vitest";

const runtime = vi.hoisted(() => ({ wails: false }));
vi.mock("../wailsRuntime", () => ({ isWailsRuntime: () => runtime.wails }));

import { goClickHouseGridExportActions, supportsBackendTableEditing } from "../goCapabilities";
import { defaultAutoCommitForDbType, supportsTableImport, supportsTableStructureEditing, supportsTransaction } from "@/lib/database/databaseFeatureSupport";

afterEach(() => {
  runtime.wails = false;
  vi.unstubAllEnvs();
});

describe("ClickHouse backend capability boundaries", () => {
  it.each(["web", "wails"])("disables visual writers on the Go %s backend", (mode) => {
    vi.stubEnv("VITE_DBX_BACKEND", mode === "web" ? "go" : "");
    runtime.wails = mode === "wails";
    expect(supportsBackendTableEditing("clickhouse")).toBe(false);
    expect(supportsTableImport("clickhouse")).toBe(false);
    expect(supportsTableStructureEditing("clickhouse")).toBe(false);
    expect(supportsTransaction("clickhouse")).toBe(false);
    expect(defaultAutoCommitForDbType("clickhouse", "manual")).toBe(true);
    for (const dbType of ["sqlite", "mysql", "postgres"] as const) {
      expect(supportsBackendTableEditing(dbType)).toBe(true);
      expect(supportsTableImport(dbType)).toBe(true);
      expect(supportsTableStructureEditing(dbType)).toBe(true);
    }
  });

  it("preserves the existing ClickHouse UI outside Go", () => {
    vi.stubEnv("VITE_DBX_BACKEND", "");
    expect(supportsBackendTableEditing("clickhouse")).toBe(true);
    expect(supportsTableImport("clickhouse")).toBe(true);
    expect(supportsTableStructureEditing("clickhouse")).toBe(true);
    expect(supportsTransaction("clickhouse")).toBe(false);
  });
});

it("offers only verified ClickHouse export paths for each grid context", () => {
  const table = goClickHouseGridExportActions("table-data", false, false);
  expect([...table]).toContain("page-csv");
  expect([...table]).toContain("page-xlsx");
  expect([...table]).toContain("page-txt");
  for (const action of ["csv", "xlsx", "json", "txt", "sql", "page-json", "page-markdown", "page-html"]) expect(table.has(action)).toBe(false);
  const query = goClickHouseGridExportActions("results", true, false);
  for (const action of ["csv", "xlsx", "json", "txt"]) expect(query.has(action)).toBe(true);
  expect(goClickHouseGridExportActions("results", true, true).has("json")).toBe(false);
});
