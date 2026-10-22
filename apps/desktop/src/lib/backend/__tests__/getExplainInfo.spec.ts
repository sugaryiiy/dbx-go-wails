import { beforeEach, describe, expect, it, vi } from "vitest";

describe("getExplainInfo backend error propagation", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.unstubAllGlobals();
  });

  it("preserves HTTP response errors", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: false,
        text: vi.fn().mockResolvedValue("Agent error: PLAN_TABLE is not accessible"),
      }),
    );
    const { getExplainInfo } = await import("@/lib/backend/http");

    await expect(getExplainInfo("oracle-1", "ORCL", "APP", "SELECT * FROM DUAL", "explain")).rejects.toThrow("Agent error: PLAN_TABLE is not accessible");
  });
});
