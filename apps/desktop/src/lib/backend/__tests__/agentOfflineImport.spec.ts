import { afterEach, describe, expect, it, vi } from "vitest";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe("offline Agent and standalone JRE import", () => {
  it("uploads standalone JRE packages and preserves both result counts", async () => {
    const failures = [{ key: "21", is_jre: true, error: "Failed to extract JRE archive: Access is denied. (os error 5)" }];
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ count: 0, jreCount: 1, failures })));
    vi.stubGlobal("fetch", fetchMock);
    const { importAgentsFromZip } = await import("../http");
    const file = new File(["archive"], "renamed.tar.zst");
    expect(await importAgentsFromZip(file, "import-2")).toEqual({ count: 0, jreCount: 1, failures });
    const [url, request] = fetchMock.mock.calls[0];
    expect(url).toContain("/api/agents/import-offline");
    expect(request.method).toBe("POST");
    expect(request.body.get("operationId")).toBe("import-2");
    expect(request.body.get("file").name).toBe(file.name);
  });

  it("remains compatible with web backends that only return a driver count", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ count: 2 }))));
    const { importAgentsFromZip } = await import("../http");
    // Older web backends report no per-item failures; the caller must still see an empty list.
    expect(await importAgentsFromZip(new File(["archive"], "drivers.zip"))).toEqual({
      count: 2,
      jreCount: 0,
      failures: [],
    });
  });
});
