import { afterEach, describe, expect, it, vi } from "vitest";
import { startQueryResultExport } from "../http";
import type { QueryResultExportRequest, TableExportProgress } from "../backendTypes";

afterEach(() => vi.unstubAllGlobals());

const request = { exportId: "query-export", format: "csv" } as QueryResultExportRequest;
const completed: TableExportProgress = { exportId: request.exportId, tableName: "query", rowsExported: 2, totalRows: 2, status: "Done" };

function setup(save: () => Promise<string>) {
  vi.stubGlobal("go", { main: { App: { GetWindowState: vi.fn(), SaveQueryResultExport: save } } });
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(new Response("{}"))
    .mockResolvedValueOnce(new Response(JSON.stringify(completed)));
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

describe("Wails query export file delivery", () => {
  it("only reports Done after the native file save completes", async () => {
    let finishSave!: (path: string) => void;
    const save = vi.fn(
      () =>
        new Promise<string>((resolve) => {
          finishSave = resolve;
        }),
    );
    const fetch = setup(save);
    const progress = vi.fn();
    const pending = startQueryResultExport(request, progress);
    await vi.waitFor(() => expect(save).toHaveBeenCalled());
    expect(progress.mock.calls.map(([value]) => value.status)).toEqual(["Writing"]);
    finishSave("/tmp/query.csv");
    expect(await pending).toEqual(completed);
    expect(progress.mock.calls.map(([value]) => value.status)).toEqual(["Writing", "Done"]);
    expect(fetch.mock.calls[1]?.[0]).toBe("/api/export/query-result/progress/query-export?poll=1");
  });

  it("reports cancellation when the native save dialog is dismissed", async () => {
    setup(async () => "");
    const progress = vi.fn();
    expect((await startQueryResultExport(request, progress)).status).toBe("Cancelled");
    expect(progress.mock.calls.map(([value]) => value.status)).toEqual(["Writing", "Cancelled"]);
  });

  it("reports a native save error and never signals successful delivery", async () => {
    setup(async () => {
      throw new Error("Disk full");
    });
    const progress = vi.fn();
    await expect(startQueryResultExport(request, progress)).rejects.toThrow("Disk full");
    expect(progress.mock.calls.map(([value]) => value.status)).toEqual(["Writing", "Error"]);
  });
});
