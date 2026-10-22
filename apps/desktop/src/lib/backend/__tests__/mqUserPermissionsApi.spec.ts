import { beforeEach, describe, expect, it, vi } from "vitest";

describe("mq users/permissions HTTP API", () => {
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

  it("posts to the users endpoints", async () => {
    const fetchMock = stubFetch();
    const { mqListUsers, mqCreateUser, mqDeleteUser } = await import("@/lib/backend/mq-http");

    await mqListUsers("conn-1");
    expect(lastCall(fetchMock)).toEqual({ url: "/api/mq/users/list", body: { connectionId: "conn-1" } });

    await mqCreateUser("conn-1", "dbx-app", "secret", ["monitoring"]);
    expect(lastCall(fetchMock)).toEqual({ url: "/api/mq/users/create", body: { connectionId: "conn-1", name: "dbx-app", password: "secret", tags: ["monitoring"] } });

    await mqDeleteUser("conn-1", "dbx-app");
    expect(lastCall(fetchMock)).toEqual({ url: "/api/mq/users/delete", body: { connectionId: "conn-1", name: "dbx-app" } });
  });

  it("posts to the user-permissions endpoints", async () => {
    const fetchMock = stubFetch();
    const { mqListUserPermissions, mqGrantUserPermission, mqRevokeUserPermission } = await import("@/lib/backend/mq-http");

    await mqListUserPermissions("conn-1", { virtualHost: "/" });
    expect(lastCall(fetchMock)).toEqual({ url: "/api/mq/user-permissions/list", body: { connectionId: "conn-1", virtualHost: "/", user: undefined, allVhosts: undefined } });

    await mqGrantUserPermission("conn-1", "dbx-app", "/", { configure: "^dbx-" });
    expect(lastCall(fetchMock)).toEqual({ url: "/api/mq/user-permissions/grant", body: { connectionId: "conn-1", user: "dbx-app", virtualHost: "/", configure: "^dbx-", write: undefined, read: undefined } });

    await mqRevokeUserPermission("conn-1", "dbx-app", "/");
    expect(lastCall(fetchMock)).toEqual({ url: "/api/mq/user-permissions/revoke", body: { connectionId: "conn-1", user: "dbx-app", virtualHost: "/" } });
  });

  it("surfaces HTTP errors with the response detail", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: false,
        status: 500,
        text: vi.fn().mockResolvedValue("cannot delete the current connection user"),
      }),
    );
    const { mqDeleteUser } = await import("@/lib/backend/mq-http");

    await expect(mqDeleteUser("conn-1", "jjsd")).rejects.toThrow("cannot delete the current connection user");
  });
});
