import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  desktop: false,
  createSampleWorkspace: vi.fn(),
  connectionStore: { reloadFromDisk: vi.fn(), ensureConnected: vi.fn(), activeConnectionId: "previous" },
  queryStore: { createTab: vi.fn() },
}));
vi.mock("@/lib/backend/api", () => ({ createSampleWorkspace: mocks.createSampleWorkspace }));
vi.mock("@/lib/backend/tauriRuntime", () => ({ isTauriRuntime: () => mocks.desktop }));
vi.mock("@/stores/connectionStore", () => ({ useConnectionStore: () => mocks.connectionStore }));
vi.mock("@/stores/queryStore", () => ({ useQueryStore: () => mocks.queryStore }));

import { useSampleWorkspace } from "../useSampleWorkspace";

const sample = { connection: { id: "sample-sqlite", database: "main" }, sql: "SELECT * FROM customers;" };

beforeEach(() => {
  vi.resetAllMocks();
  mocks.desktop = false;
  mocks.connectionStore.activeConnectionId = "previous";
  mocks.createSampleWorkspace.mockResolvedValue(sample);
  mocks.connectionStore.reloadFromDisk.mockResolvedValue(undefined);
  mocks.connectionStore.ensureConnected.mockResolvedValue(undefined);
});

describe("open sample workspace", () => {
  it("refreshes the saved connection and opens sample SQL only after connecting", async () => {
    let finishConnecting!: () => void;
    mocks.connectionStore.ensureConnected.mockReturnValue(
      new Promise<void>((resolve) => {
        finishConnecting = resolve;
      }),
    );
    const onError = vi.fn();
    const workspace = useSampleWorkspace({ queryTitle: () => "Sample query", onError });
    const opening = workspace.openSampleWorkspace();
    await vi.waitFor(() => expect(mocks.connectionStore.ensureConnected).toHaveBeenCalledWith("sample-sqlite"));
    expect(mocks.connectionStore.reloadFromDisk).toHaveBeenCalledOnce();
    expect(workspace.openingSampleWorkspace.value).toBe(true);
    expect(mocks.queryStore.createTab).not.toHaveBeenCalled();
    finishConnecting();
    await opening;
    expect(mocks.connectionStore.activeConnectionId).toBe("sample-sqlite");
    expect(mocks.queryStore.createTab).toHaveBeenCalledWith("sample-sqlite", "main", "Sample query", "query", undefined, sample.sql);
    expect(onError).not.toHaveBeenCalled();
    expect(workspace.openingSampleWorkspace.value).toBe(false);
  });

  it("ignores repeated clicks while creating the sample", async () => {
    let finishCreating!: (value: typeof sample) => void;
    mocks.createSampleWorkspace.mockReturnValue(
      new Promise((resolve) => {
        finishCreating = resolve;
      }),
    );
    const workspace = useSampleWorkspace({ queryTitle: () => "Sample query", onError: vi.fn() });
    const opening = workspace.openSampleWorkspace();
    await workspace.openSampleWorkspace();
    expect(mocks.createSampleWorkspace).toHaveBeenCalledOnce();
    finishCreating(sample);
    await opening;
    expect(mocks.queryStore.createTab).toHaveBeenCalledOnce();
  });

  it.each(["create", "reload", "connect"])("reports %s errors and allows retry without opening a broken tab", async (stage) => {
    const failure = new Error("Server unavailable");
    const boundary = stage === "create" ? mocks.createSampleWorkspace : stage === "reload" ? mocks.connectionStore.reloadFromDisk : mocks.connectionStore.ensureConnected;
    boundary.mockRejectedValueOnce(failure);
    const onError = vi.fn();
    const workspace = useSampleWorkspace({ queryTitle: () => "Sample query", onError });
    await workspace.openSampleWorkspace();
    expect(onError).toHaveBeenCalledWith(failure);
    expect(workspace.openingSampleWorkspace.value).toBe(false);
    expect(mocks.connectionStore.activeConnectionId).toBe("previous");
    expect(mocks.queryStore.createTab).not.toHaveBeenCalled();
    await workspace.openSampleWorkspace();
    expect(mocks.queryStore.createTab).toHaveBeenCalledOnce();
  });

  it("does not call a Web-only API in the desktop runtime", async () => {
    mocks.desktop = true;
    const workspace = useSampleWorkspace({ queryTitle: () => "Sample query", onError: vi.fn() });
    await workspace.openSampleWorkspace();
    expect(mocks.createSampleWorkspace).not.toHaveBeenCalled();
    expect(workspace.openingSampleWorkspace.value).toBe(false);
  });
});
