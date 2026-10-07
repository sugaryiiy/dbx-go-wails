import { afterEach, describe, expect, it, vi } from "vitest";
import { analyzeEditableQueryEditability } from "../http";

afterEach(() => vi.unstubAllGlobals());

describe("Go query editability analysis", () => {
  it("uses the existing client parser without calling an unmigrated endpoint", async () => {
    vi.stubGlobal("go", { main: { App: { GetWindowState: vi.fn() } } });
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    expect(await analyzeEditableQueryEditability("SELECT id, name FROM customers")).toMatchObject({ editable: true, analysis: { tableName: "customers" } });
    expect(await analyzeEditableQueryEditability("SELECT 1")).toEqual({ editable: false, reason: "no-table" });
    expect(fetch).not.toHaveBeenCalled();
  });
});
