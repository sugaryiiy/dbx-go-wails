import { afterEach, describe, expect, it, vi } from "vitest";
import { isTauriRuntime } from "./tauriRuntime";
import { isWailsRuntime, onWailsEvent, openWailsFile, saveWailsFile, saveWailsBytes, wailsApp } from "./wailsRuntime";

afterEach(() => vi.unstubAllGlobals());

describe("Wails desktop boundary", () => {
  it("preserves binary file contents through the native bridge", async () => {
    const SaveFileBytes = vi.fn().mockResolvedValue("/tmp/export.xlsx");
    vi.stubGlobal("go", { main: { App: { SaveFileBytes } } });
    await saveWailsBytes("export.xlsx", new Uint8Array([0, 255, 127]));
    expect(SaveFileBytes).toHaveBeenCalledWith("export.xlsx", "AP9/");
  });

  it("does not report successful export when the save dialog is cancelled", async () => {
    vi.stubGlobal("go", { main: { App: { SaveFileBytes: vi.fn().mockResolvedValue("") } } });
    await expect(saveWailsBytes("export.csv", new Uint8Array())).rejects.toThrow("Export cancelled");
  });
  it("detects real Go bindings without selecting Tauri's Rust transport", () => {
    const env = { go: { main: { App: { GetWindowState: vi.fn() } } } };
    expect(isWailsRuntime(env)).toBe(true);
    expect(isTauriRuntime(env)).toBe(false);
    expect(isWailsRuntime({ runtime: {} })).toBe(false);
    expect(isWailsRuntime({})).toBe(false);
    expect(() => wailsApp({})).toThrow("unavailable");
  });

  it("forwards shell event subscriptions and their unsubscribe handle", () => {
    const unsubscribe = vi.fn();
    const EventsOn = vi.fn().mockReturnValue(unsubscribe);
    const callback = vi.fn();
    const env = { runtime: { EventsOn } };
    expect(onWailsEvent("dbx-app-close-requested", callback, env)).toBe(unsubscribe);
    expect(EventsOn).toHaveBeenCalledWith("dbx-app-close-requested", callback);
  });

  it("treats event subscription as a no-op outside the Wails runtime", () => {
    const unlisten = onWailsEvent("dbx-app-close-requested", vi.fn(), {});
    expect(() => unlisten()).not.toThrow();
  });

  it("normalizes native file dialog cancellation and multiple selection", async () => {
    const OpenFile = vi.fn().mockResolvedValueOnce(null).mockResolvedValueOnce(["/tmp/a.db"]).mockResolvedValueOnce(["/tmp/a.db", "/tmp/b.db"]);
    const SaveFile = vi.fn().mockResolvedValue("");
    vi.stubGlobal("go", { main: { App: { OpenFile, SaveFile } } });
    expect(await openWailsFile()).toBeNull();
    expect(await openWailsFile()).toBe("/tmp/a.db");
    expect(await openWailsFile({ multiple: true })).toEqual(["/tmp/a.db", "/tmp/b.db"]);
    expect(await saveWailsFile()).toBeNull();
  });
});
