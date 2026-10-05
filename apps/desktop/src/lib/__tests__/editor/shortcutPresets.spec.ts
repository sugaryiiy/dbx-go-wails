import { describe, expect, it } from "vitest";
import { getShortcutPreset, migrateShortcutKeymap, defaultShortcutKeymap, SHORTCUT_KEYMAP_VERSION } from "@/lib/editor/shortcutPresets";
import { findShortcutConflict, normalizeShortcutSettings, SHORTCUT_DEFINITIONS, type ShortcutSettings } from "@/lib/editor/shortcutRegistry";

const mac = "MacIntel";

function versionOneMacSettings(overrides: Partial<ShortcutSettings> = {}) {
  const { findAction: _find, shrinkSelection: _shrink, ...previous } = getShortcutPreset("datagrip-macos", mac);
  return {
    shortcutKeymap: "datagrip-macos" as const,
    shortcutKeymapVersion: 1,
    shortcuts: { ...previous, sendSelectionToAi: "Mod+Shift+A", ...overrides },
  };
}

describe("DataGrip macOS keymap", () => {
  it("is the initial Mac preset and keeps other platforms on DBX", () => {
    expect(defaultShortcutKeymap(mac)).toBe("datagrip-macos");
    expect(defaultShortcutKeymap("Win32")).toBe("dbx");
    expect(migrateShortcutKeymap({}, mac).shortcuts).toMatchObject({ formatSql: "Mod+Alt+L", newQuery: "Mod+Shift+L", cancelQuery: "Mod+F2", toggleQueryHistory: "Mod+Alt+E", findAction: "Mod+Shift+A", shrinkSelection: "Alt+ArrowDown", sendSelectionToAi: "" });
    expect(migrateShortcutKeymap({}, "Linux").shortcuts).toEqual(normalizeShortcutSettings(undefined, "Linux"));
  });

  it("has no same-scope conflicts and survives normal save normalization", () => {
    const shortcuts = getShortcutPreset("datagrip-macos", mac);
    expect(normalizeShortcutSettings(shortcuts, mac)).toEqual(shortcuts);
    for (const definition of SHORTCUT_DEFINITIONS) expect(findShortcutConflict(definition.id, shortcuts[definition.id], shortcuts, mac), definition.id).toBeNull();
    expect(shortcuts.switchToTab1).toBe("");
    expect(shortcuts.toggleSidebar).toBe("Mod+1");
    expect(shortcuts.copyLineUp).toBe("");
    expect(shortcuts.moveLineUp).toBe("Alt+Shift+ArrowUp");
  });

  it("migrates already persisted DBX defaults, including modifier-order aliases", () => {
    const { cancelQuery: _cancel, toggleQueryHistory: _history, recentTabs: _recent, findAction: _find, shrinkSelection: _shrink, ...legacy } = normalizeShortcutSettings(undefined, mac);
    const shortcuts = { ...legacy, formatSql: "Mod+Shift+F" };
    expect(migrateShortcutKeymap({ shortcuts }, mac).shortcuts).toEqual(getShortcutPreset("datagrip-macos", mac));
  });

  it("preserves customized and explicitly cleared keys across restart", () => {
    const shortcuts = { ...normalizeShortcutSettings(undefined, mac), formatSql: "Alt+F8", deleteLine: "", toggleSidebar: "Ctrl+Alt+B" };
    const result = migrateShortcutKeymap({ shortcuts }, mac);
    expect(result.shortcuts).toMatchObject({ formatSql: "Alt+F8", deleteLine: "", toggleSidebar: "Ctrl+Alt+B", newQuery: "Mod+Shift+L" });
    expect(migrateShortcutKeymap(result, mac)).toEqual(result);
  });

  it("respects raw explicit unbinding even for a previously unassigned action", () => {
    const result = migrateShortcutKeymap({ shortcuts: { cancelQuery: "", recentTabs: "", toggleQueryHistory: "" } }, mac);
    expect(result.shortcuts).toMatchObject({ cancelQuery: "", recentTabs: "", toggleQueryHistory: "" });
  });

  it("does not let a new editor default shadow a custom editor or global action", () => {
    const editorConflict = migrateShortcutKeymap({ shortcuts: { saveSql: "Mod+Alt+L" } }, mac);
    expect(editorConflict.shortcuts.saveSql).toBe("Mod+Alt+L");
    expect(editorConflict.shortcuts.formatSql).toBe("");
    const globalConflict = migrateShortcutKeymap({ shortcuts: { toggleAiPanel: "Mod+Alt+L" } }, mac);
    expect(globalConflict.shortcuts.toggleAiPanel).toBe("Mod+Alt+L");
    expect(globalConflict.shortcuts.formatSql).toBe("");
  });

  it("does not let a new global default intercept a custom focused action", () => {
    const result = migrateShortcutKeymap({ shortcuts: { deleteLine: "Mod+1" } }, mac);
    expect(result.shortcuts.deleteLine).toBe("Mod+1");
    expect(result.shortcuts.toggleSidebar).toBe("");
  });

  it("leaves an explicitly chosen DBX preset unchanged", () => {
    const result = migrateShortcutKeymap({ shortcutKeymap: "dbx", shortcuts: normalizeShortcutSettings(undefined, mac) }, mac);
    expect(result.shortcuts.formatSql).toBe("Shift+Mod+F");
    expect(result.shortcuts.toggleSidebar).toBe("Mod+B");
    expect(migrateShortcutKeymap(result, mac)).toEqual(result);
  });

  it("does not remigrate a later customization back to a legacy default", () => {
    const result = migrateShortcutKeymap({}, mac);
    result.shortcuts.formatSql = "Shift+Mod+F";
    result.shortcuts.toggleQueryHistory = "";
    expect(migrateShortcutKeymap(result, mac)).toEqual(result);
  });

  it("maps synced Mac preset defaults back to DBX on Windows, keeping custom keys", () => {
    const saved = migrateShortcutKeymap({}, mac);
    saved.shortcuts.formatSql = "Alt+F8";
    const result = migrateShortcutKeymap(saved, "Win32");
    expect(result.shortcutKeymap).toBe("dbx");
    expect(result.shortcuts).toMatchObject({ formatSql: "Alt+F8", newQuery: "Mod+T", toggleSidebar: "Mod+B", switchToTab1: "Mod+1" });
    expect(migrateShortcutKeymap(result, "Win32")).toEqual(result);
  });

  it("lets a synced custom key win over a Windows default", () => {
    const saved = migrateShortcutKeymap({}, mac);
    saved.shortcuts.quickOpen = "Mod+T";
    const result = migrateShortcutKeymap(saved, "Win32");
    expect(result.shortcuts.quickOpen).toBe("Mod+T");
    expect(result.shortcuts.newQuery).toBe("");
    expect(findShortcutConflict("quickOpen", "Mod+T", result.shortcuts, "Win32")).toBeNull();
  });

  it("upgrades the shipped v1 DataGrip defaults without treating newly normalized empty fields as unbinding", () => {
    const saved = versionOneMacSettings();
    expect(saved.shortcuts).not.toHaveProperty("findAction");
    expect(saved.shortcuts).not.toHaveProperty("shrinkSelection");
    const result = migrateShortcutKeymap(saved, mac);
    expect(result.shortcutKeymapVersion).toBe(SHORTCUT_KEYMAP_VERSION);
    expect(result.shortcuts).toEqual(getShortcutPreset("datagrip-macos", mac));
    expect(result.shortcuts).toMatchObject({ findAction: "Mod+Shift+A", shrinkSelection: "Alt+ArrowDown", sendSelectionToAi: "" });
    expect(migrateShortcutKeymap(result, mac)).toEqual(result);
  });

  it("retains v1 customized keys and explicit old/new unbinding during the version upgrade", () => {
    const result = migrateShortcutKeymap(versionOneMacSettings({
      formatSql: "Shift+Mod+F",
      deleteLine: "",
      toggleQueryHistory: "",
      findAction: "",
      shrinkSelection: "",
      sendSelectionToAi: "Ctrl+Alt+A",
    }), mac);
    expect(result.shortcuts).toMatchObject({
      formatSql: "Shift+Mod+F",
      deleteLine: "",
      toggleQueryHistory: "",
      findAction: "",
      shrinkSelection: "",
      sendSelectionToAi: "Ctrl+Alt+A",
    });
    expect(migrateShortcutKeymap(result, mac)).toEqual(result);
  });

  it("gives v1 custom editor and global shortcuts priority over newly introduced defaults", () => {
    const result = migrateShortcutKeymap(versionOneMacSettings({
      quickOpen: "Shift+Mod+A",
      deleteLine: "Alt+ArrowDown",
    }), mac);
    expect(result.shortcuts).toMatchObject({ quickOpen: "Shift+Mod+A", findAction: "", deleteLine: "Alt+ArrowDown", shrinkSelection: "" });
    const editorConflict = migrateShortcutKeymap(versionOneMacSettings({ saveSql: "Mod+Shift+A" }), mac);
    expect(editorConflict.shortcuts).toMatchObject({ saveSql: "Mod+Shift+A", findAction: "" });
    expect(migrateShortcutKeymap(result, mac)).toEqual(result);
  });

  it("keeps DBX behavior when its v1 preset is explicitly selected", () => {
    const { findAction: _find, shrinkSelection: _shrink, ...previous } = getShortcutPreset("dbx", mac);
    const result = migrateShortcutKeymap({ shortcutKeymap: "dbx", shortcutKeymapVersion: 1, shortcuts: previous }, mac);
    expect(result.shortcuts).toEqual(getShortcutPreset("dbx", mac));
    expect(result.shortcuts).toMatchObject({ findAction: "", shrinkSelection: "", sendSelectionToAi: "Mod+Shift+A" });
    expect(migrateShortcutKeymap(result, mac)).toEqual(result);
  });

  it.each(["Win32", "Linux x86_64"])("converts v1 DataGrip to %s using its historical defaults", (platform) => {
    const result = migrateShortcutKeymap(versionOneMacSettings(), platform);
    expect(result.shortcutKeymap).toBe("dbx");
    expect(result.shortcuts).toEqual(getShortcutPreset("dbx", platform));
    expect(result.shortcuts).toMatchObject({ findAction: "", shrinkSelection: "", sendSelectionToAi: "Mod+Shift+A" });
    expect(migrateShortcutKeymap(result, platform)).toEqual(result);
  });

  it("preserves v1 unbinding and custom keys when syncing to Windows", () => {
    const result = migrateShortcutKeymap(versionOneMacSettings({ quickOpen: "Mod+T", sendSelectionToAi: "", shrinkSelection: "Alt+F9" }), "Win32");
    expect(result.shortcuts).toMatchObject({ quickOpen: "Mod+T", newQuery: "", sendSelectionToAi: "", shrinkSelection: "Alt+F9", findAction: "" });
    expect(migrateShortcutKeymap(result, "Win32")).toEqual(result);
  });

  it("preserves v2 custom action keys and clears conflicting Windows defaults", () => {
    const saved = migrateShortcutKeymap({}, mac);
    saved.shortcuts.findAction = "Mod+T";
    saved.shortcuts.shrinkSelection = "";
    const result = migrateShortcutKeymap(saved, "Win32");
    expect(result.shortcuts).toMatchObject({ findAction: "Mod+T", newQuery: "", shrinkSelection: "", sendSelectionToAi: "Mod+Shift+A" });
    expect(migrateShortcutKeymap(result, "Win32")).toEqual(result);
  });
});
