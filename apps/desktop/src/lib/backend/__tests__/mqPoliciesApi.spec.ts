import { beforeEach, describe, expect, it, vi } from "vitest";

describe("mq policies/overview/nodes HTTP API", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.unstubAllGlobals();
  });

  function stubFetch(payload: unknown = []) {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: vi.fn().mockResolvedValue(payload),
    });
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  function lastCall(fetchMock: ReturnType<typeof stubFetch>): { url: string; body: Record<string, unknown> } {
    const [url, init] = fetchMock.mock.calls.at(-1) as [string, RequestInit];
    return { url, body: JSON.parse(String(init.body)) as Record<string, unknown> };
  }

  it("posts to the policies endpoints", async () => {
    const fetchMock = stubFetch();
    const { mqListPolicies, mqSetPolicy, mqDeletePolicy } = await import("@/lib/backend/mq-http");

    await mqListPolicies("conn-1", { allVhosts: true });
    expect(lastCall(fetchMock)).toEqual({ url: "/api/mq/policies/list", body: { connectionId: "conn-1", virtualHost: undefined, allVhosts: true } });

    await mqSetPolicy("conn-1", "/", { name: "dbx-ttl", pattern: "^dbx-", applyTo: "queues", priority: 10, definition: { "message-ttl": 60000 } });
    expect(lastCall(fetchMock)).toEqual({
      url: "/api/mq/policies/set",
      body: { connectionId: "conn-1", virtualHost: "/", name: "dbx-ttl", pattern: "^dbx-", applyTo: "queues", priority: 10, definition: { "message-ttl": 60000 } },
    });

    await mqDeletePolicy("conn-1", "/", "dbx-ttl");
    expect(lastCall(fetchMock)).toEqual({ url: "/api/mq/policies/delete", body: { connectionId: "conn-1", virtualHost: "/", name: "dbx-ttl" } });
  });

  it("posts to the overview and nodes endpoints", async () => {
    const fetchMock = stubFetch({});
    const { mqGetOverview, mqListNodes } = await import("@/lib/backend/mq-http");

    await mqGetOverview("conn-1");
    expect(lastCall(fetchMock)).toEqual({ url: "/api/mq/overview", body: { connectionId: "conn-1" } });

    await mqListNodes("conn-1");
    expect(lastCall(fetchMock)).toEqual({ url: "/api/mq/nodes", body: { connectionId: "conn-1" } });
  });

  it("surfaces HTTP errors with the response detail", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: false,
        status: 400,
        text: vi.fn().mockResolvedValue('virtual host "*" is a listing sentinel'),
      }),
    );
    const { mqSetPolicy } = await import("@/lib/backend/mq-http");

    await expect(mqSetPolicy("conn-1", "*", { name: "dbx-ttl", pattern: ".*", definition: {} })).rejects.toThrow('virtual host "*" is a listing sentinel');
  });
});
