import { describe, expect, it } from "vitest";
import { buildSqlCompletionItems, recordCompletionSelection } from "@/lib/sql/sqlCompletion";

// Locks the user-facing ranking contract for mixed completion pools:
// previously selected items first (most-selected wins), then tables,
// and functions last among peer groups at equal match quality.
describe("SQL completion ranking order", () => {
  it("ranks tables ahead of table functions in a FROM pool", () => {
    const tables = [
      { name: "counters", type: "table" as const },
      { name: "companies", type: "table" as const },
      { name: "orders", type: "table" as const },
    ];
    const items = buildSqlCompletionItems("SELECT * FROM co", 16, { tables, columnsByTable: new Map(), databaseType: "clickhouse" });
    const indexOf = (label: string) => items.findIndex((item) => item.label === label);
    // ClickHouse offers table functions (cosn) alongside real tables here.
    expect(indexOf("cosn")).toBeGreaterThanOrEqual(0);
    expect(indexOf("counters")).toBeGreaterThanOrEqual(0);
    expect(indexOf("counters")).toBeLessThan(indexOf("cosn"));
    expect(indexOf("companies")).toBeLessThan(indexOf("cosn"));
  });

  it("floats previously selected items to the top, most-selected first", () => {
    const tables = [
      { name: "customers", type: "table" as const },
      { name: "companies", type: "table" as const },
      { name: "orders", type: "table" as const },
    ];
    const columnsByTable = new Map([
      [
        "customers",
        [
          { name: "company", table: "customers" },
          { name: "country", table: "customers" },
          { name: "id", table: "customers" },
        ],
      ],
    ]);
    const build = () => buildSqlCompletionItems("SELECT co FROM customers", 9, { tables, columnsByTable, databaseType: "mysql" });
    const indexOf = (items: ReturnType<typeof build>, label: string) => items.findIndex((item) => item.label === label);

    // Baseline: with no selection history, the column outranks the function.
    const baseline = build();
    expect(indexOf(baseline, "company")).toBeLessThan(indexOf(baseline, "COUNT"));

    recordCompletionSelection("COUNT", "function");
    recordCompletionSelection("COUNT", "function");
    recordCompletionSelection("COUNT", "function");
    recordCompletionSelection("company", "column");

    const ranked = build();
    // Most-selected first, then the other selected item, then items with no
    // history at all — regardless of their base boosts.
    expect(ranked[0]).toMatchObject({ label: "COUNT", type: "function" });
    expect(ranked[1]).toMatchObject({ label: "company", type: "column" });
    expect(indexOf(ranked, "country")).toBeGreaterThan(indexOf(ranked, "company"));
    expect(indexOf(ranked, "COS")).toBeGreaterThan(indexOf(ranked, "company"));
  });
});
