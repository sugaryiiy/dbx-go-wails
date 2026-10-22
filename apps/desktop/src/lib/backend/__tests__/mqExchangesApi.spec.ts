import { beforeEach, describe, expect, it, vi } from "vitest";

const NS = { tenant: "_rabbitmq", namespace: "/" };
const PAGINATION = { page: 2, pageSize: 100, search: "orders", sort: "name" as const, sortDescending: false };

describe("mq exchanges/bindings HTTP API", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.unstubAllGlobals();
  });

  function stubFetch() {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: vi.fn().mockResolvedValue([]),
    });
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  function lastCall(fetchMock: ReturnType<typeof stubFetch>): { url: string; body: Record<string, unknown> } {
    const [url, init] = fetchMock.mock.calls.at(-1) as [string, RequestInit];
    return { url, body: JSON.parse(String(init.body)) as Record<string, unknown> };
  }

  it("posts to the exchanges endpoints", async () => {
    const fetchMock = stubFetch();
    const { mqListExchanges, mqCreateExchange, mqDeleteExchange } = await import("@/lib/backend/mq-http");

    await mqListExchanges("conn-1", NS);
    expect(lastCall(fetchMock)).toEqual({ url: "/api/mq/exchanges/list", body: { connectionId: "conn-1", ns: NS } });

    await mqCreateExchange("conn-1", NS, { name: "dbx-events", type: "fanout", durable: false, autoDelete: true });
    expect(lastCall(fetchMock)).toEqual({
      url: "/api/mq/exchanges/create",
      body: { connectionId: "conn-1", ns: NS, name: "dbx-events", exchangeType: "fanout", durable: false, autoDelete: true },
    });

    await mqDeleteExchange("conn-1", NS, "dbx-events");
    expect(lastCall(fetchMock)).toEqual({ url: "/api/mq/exchanges/delete", body: { connectionId: "conn-1", ns: NS, name: "dbx-events" } });
  });

  it("posts bounded exchange and topic list requests", async () => {
    const fetchMock = stubFetch();
    const { mqListExchangesPage, mqListTopicsPage } = await import("@/lib/backend/mq-http");

    await mqListExchangesPage("conn-1", NS, PAGINATION);
    expect(lastCall(fetchMock)).toEqual({ url: "/api/mq/exchanges/list-page", body: { connectionId: "conn-1", ns: NS, pagination: PAGINATION } });

    const opts = { includeNonPersistent: false };
    await mqListTopicsPage("conn-1", NS, opts, PAGINATION);
    expect(lastCall(fetchMock)).toEqual({ url: "/api/mq/topics/list-page", body: { connectionId: "conn-1", ns: NS, opts, pagination: PAGINATION } });
  });

  it("posts to the bindings endpoints", async () => {
    const fetchMock = stubFetch();
    const { mqListBindings, mqBind, mqUnbind } = await import("@/lib/backend/mq-http");
    const binding = { source: "dbx-events", destination: "dbx-queue", destinationType: "queue", routingKey: "orders.*" };

    await mqListBindings("conn-1", NS, { queue: "dbx-queue" });
    expect(lastCall(fetchMock)).toEqual({ url: "/api/mq/bindings/list", body: { connectionId: "conn-1", ns: NS, exchange: undefined, queue: "dbx-queue" } });

    await mqBind("conn-1", NS, binding);
    expect(lastCall(fetchMock)).toEqual({ url: "/api/mq/bindings/bind", body: { connectionId: "conn-1", ns: NS, binding } });

    await mqUnbind("conn-1", NS, binding);
    expect(lastCall(fetchMock)).toEqual({ url: "/api/mq/bindings/unbind", body: { connectionId: "conn-1", ns: NS, binding } });
  });

  it("surfaces HTTP errors with the response detail", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: false,
        status: 500,
        text: vi.fn().mockResolvedValue("cannot delete built-in exchange"),
      }),
    );
    const { mqDeleteExchange } = await import("@/lib/backend/mq-http");

    await expect(mqDeleteExchange("conn-1", NS, "amq.direct")).rejects.toThrow("cannot delete built-in exchange");
  });
});
