import { beforeEach, describe, expect, it, vi } from "vitest";

describe("listDialectDataTypes backend adapter", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.unstubAllGlobals();
  });

  it("uses the matching Web dialect route", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: vi.fn().mockResolvedValue(["INTEGER", "TEXT"]),
    });
    vi.stubGlobal("fetch", fetchMock);
    const { listDialectDataTypes } = await import("@/lib/backend/http");

    await expect(listDialectDataTypes("PostgreSQL")).resolves.toEqual(["INTEGER", "TEXT"]);
    expect(fetchMock).toHaveBeenCalledWith("/api/dialect/data-types?dialect_name=PostgreSQL");
  });
});
