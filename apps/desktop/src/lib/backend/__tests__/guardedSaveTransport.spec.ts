import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../goCapabilities", () => ({ isGoBackend: () => true }));
import { executeInManualTransaction, executeInTransaction } from "../http";

afterEach(() => vi.unstubAllGlobals());

describe("guarded grid save transport", () => {
  it("sends row limits with both automatic and manual transactions", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response("{}", { headers: { "content-type": "application/json" } }));
    vi.stubGlobal("fetch", fetch);
    await executeInTransaction("connection", "main", ["UPDATE items SET n=2"], "main", undefined, [1]);
    expect(JSON.parse(fetch.mock.calls[0]![1].body)).toMatchObject({ statements: ["UPDATE items SET n=2"], maxAffectedRowsPerStatement: [1] });

    fetch.mockResolvedValue(new Response("[]", { headers: { "content-type": "application/json" } }));
    await executeInManualTransaction("manual-session", "UPDATE items SET n=2", "main", undefined, undefined, undefined, undefined, undefined, undefined, [1]);
    expect(JSON.parse(fetch.mock.calls[1]![1].body)).toMatchObject({ txnSessionId: "manual-session", maxAffectedRowsPerStatement: [1] });
  });
});
