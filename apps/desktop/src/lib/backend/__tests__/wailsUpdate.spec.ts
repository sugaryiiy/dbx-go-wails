import { afterEach, describe, expect, it, vi } from "vitest";
import * as api from "../http";

afterEach(() => vi.unstubAllGlobals());

describe("Go Wails update bindings", () => {
  it("routes downloads and cache operations through the native bindings", async () => {
    const downloaded = { cache_id: "attempt-1", version: "0.6.34", portable_mode: false, release_url: "https://example.com/release", release_notes: "Notes", downloaded_at: 1 };
    const app = {
      GetWindowState: vi.fn(),
      DownloadUpdate: vi.fn().mockResolvedValue(downloaded),
      CancelUpdateDownload: vi.fn().mockResolvedValue(undefined),
      GetDownloadedUpdate: vi.fn().mockResolvedValue(downloaded),
      DiscardDownloadedUpdate: vi.fn().mockResolvedValue(undefined),
    };
    vi.stubGlobal("go", { main: { App: app } });

    await expect(api.downloadUpdate("official", "0.6.34", "attempt-1", "Notes")).resolves.toEqual(downloaded);
    expect(app.DownloadUpdate).toHaveBeenCalledWith("official", "0.6.34", "attempt-1", "Notes");
    await api.cancelUpdateDownload();
    expect(app.CancelUpdateDownload).toHaveBeenCalledOnce();
    await expect(api.getDownloadedUpdate()).resolves.toEqual(downloaded);
    await api.discardDownloadedUpdate("attempt-1");
    expect(app.DiscardDownloadedUpdate).toHaveBeenCalledWith("attempt-1");
  });

  it("keeps web behaviour outside the desktop runtime", async () => {
    await expect(api.downloadUpdate("official", "0.6.34", "attempt-1")).rejects.toThrow("only available in the desktop app");
    await expect(api.getDownloadedUpdate()).resolves.toBeNull();
    await expect(api.cancelUpdateDownload()).resolves.toBeUndefined();
    await expect(api.discardDownloadedUpdate("attempt-1")).resolves.toBeUndefined();
  });
});
