import { describe, expect, it } from "vitest";
import { DEFAULT_DATA_GRID_FONT_FAMILY, DEFAULT_MONO_FONT_FAMILY, DEFAULT_UI_FONT_FAMILY, LEGACY_PRESET_FONT_MAP } from "@/lib/app/appFonts";
import { normalizeEditorSettings } from "../settingsStore";

describe("editor font preset migration", () => {
  it("upgrades saved built-in presets when optional code fonts are not installed", () => {
    for (const [legacy, current] of Object.entries(LEGACY_PRESET_FONT_MAP)) {
      expect(normalizeEditorSettings({ fontFamily: legacy }).fontFamily).toBe(current);
    }
  });

  it("preserves explicit custom stacks, including ones containing the old fallback", () => {
    for (const custom of ["'User Mono', monospace", `'User Mono', "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", monospace`, "PingFang SC", "toString"]) {
      expect(normalizeEditorSettings({ fontFamily: custom }).fontFamily).toBe(custom);
    }
  });

  it("keeps regular UI and data fonts independent from the editor default", () => {
    expect(normalizeEditorSettings({})).toMatchObject({
      fontFamily: DEFAULT_MONO_FONT_FAMILY,
      uiFontFamily: DEFAULT_UI_FONT_FAMILY,
      tableFontFamily: DEFAULT_DATA_GRID_FONT_FAMILY,
    });
    expect(normalizeEditorSettings({ uiFontFamily: "'User UI', sans-serif", tableFontFamily: "'User Table', sans-serif" })).toMatchObject({
      uiFontFamily: "'User UI', sans-serif",
      tableFontFamily: "'User Table', sans-serif",
    });
  });
});
