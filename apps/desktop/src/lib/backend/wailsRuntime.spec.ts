import { afterEach, describe, expect, it, vi } from "vitest";
import { isTauriRuntime } from "./tauriRuntime";
import { isWailsRuntime, openWailsFile, saveWailsFile, saveWailsBytes, wailsApp } from "./wailsRuntime";

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
