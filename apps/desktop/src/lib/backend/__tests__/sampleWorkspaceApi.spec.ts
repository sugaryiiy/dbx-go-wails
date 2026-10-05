import { afterEach, describe, expect, it, vi } from "vitest";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.resetModules();
});

describe("sample workspace Web API", () => {
  it("creates a sample using the existing HTTP transport and returns its connection and SQL", async () => {
    const sample = { connection: { id: "sample", db_type: "sqlite", database: "main" }, sql: "SELECT 1;" };
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => sample });
    vi.stubGlobal("fetch", fetchMock);
    const { createSampleWorkspace } = await import("../http");
    await expect(createSampleWorkspace()).resolves.toEqual(sample);
    expect(fetchMock).toHaveBeenCalledWith("/api/workspace/sample", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
  });
});
