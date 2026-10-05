// @vitest-environment happy-dom

import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getShortcutPreset, SHORTCUT_KEYMAP_VERSION } from "@/lib/editor/shortcutPresets";
import { normalizeEditorSettings, useSettingsStore } from "../settingsStore";

const backend = vi.hoisted(() => ({ loadEditorSettings: vi.fn(), saveEditorSettings: vi.fn() }));
vi.mock("@/lib/backend/api", () => backend);

let persisted: Record<string, unknown> | null;

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  vi.spyOn(navigator, "platform", "get").mockReturnValue("MacIntel");
  setActivePinia(createPinia());
  persisted = null;
  backend.loadEditorSettings.mockImplementation(async () => structuredClone(persisted));
  backend.saveEditorSettings.mockImplementation(async (settings) => {
    persisted = JSON.parse(JSON.stringify(settings));
  });
});

afterEach(() => vi.restoreAllMocks());

describe("shortcut keymap persistence", () => {
  it("persists the Mac default once after a successful empty read", async () => {
    const store = useSettingsStore();
    await store.initEditorSettings();
    expect(store.editorSettings.shortcutKeymap).toBe("datagrip-macos");
    expect(store.editorSettings.shortcuts.formatSql).toBe("Mod+Alt+L");
    expect(persisted).toMatchObject({ shortcutKeymap: "datagrip-macos", shortcutKeymapVersion: SHORTCUT_KEYMAP_VERSION });
    expect(backend.saveEditorSettings).toHaveBeenCalledOnce();
    await store.initEditorSettings();
    setActivePinia(createPinia());
    await useSettingsStore().initEditorSettings();
    expect(backend.saveEditorSettings).toHaveBeenCalledOnce();
  });

  it("migrates old defaults while retaining custom and explicitly unbound keys", async () => {
    const { cancelQuery: _cancel, toggleQueryHistory: _history, recentTabs: _recent, ...legacyShortcuts } = getShortcutPreset("dbx");
    persisted = { shortcuts: { ...legacyShortcuts, newQuery: "Ctrl+Alt+Q", formatSql: "" } };
    const store = useSettingsStore();
    await store.initEditorSettings();
    expect(store.editorSettings.shortcuts.newQuery).toBe("Ctrl+Alt+Q");
    expect(store.editorSettings.shortcuts.formatSql).toBe("");
    expect(store.editorSettings.shortcuts.gotoLine).toBe("Mod+L");
    expect(store.editorSettings.shortcuts.cancelQuery).toBe("Mod+F2");
    expect(store.editorSettings.shortcuts.toggleQueryHistory).toBe("Mod+Alt+E");
    expect(store.editorSettings.shortcuts.recentTabs).toBe("Mod+E");
    expect(backend.saveEditorSettings).toHaveBeenCalledOnce();
    setActivePinia(createPinia());
    const restored = useSettingsStore();
    await restored.initEditorSettings();
    expect(restored.editorSettings.shortcuts.formatSql).toBe("");
    expect(backend.saveEditorSettings).toHaveBeenCalledOnce();
  });

  it("does not remigrate an explicit old binding after Apply or a later reload", async () => {
    persisted = normalizeEditorSettings({}) as unknown as Record<string, unknown>;
    const store = useSettingsStore();
    await store.initEditorSettings();
    const oldFormat = getShortcutPreset("dbx").formatSql;
    await store.updateEditorSettingsAndPersist({ shortcuts: { ...store.editorSettings.shortcuts, formatSql: oldFormat } });
    await store.updateEditorSettingsAndPersist({ fontSize: 17 });
    setActivePinia(createPinia());
    const restored = useSettingsStore();
    await restored.initEditorSettings();
    expect(restored.editorSettings.shortcuts.formatSql).toBe(oldFormat);
    expect(restored.editorSettings.shortcutKeymap).toBe("datagrip-macos");
  });

  it("keeps DBX defaults on non-Mac platforms", async () => {
    vi.spyOn(navigator, "platform", "get").mockReturnValue("Win32");
    const store = useSettingsStore();
    await store.initEditorSettings();
    expect(store.editorSettings.shortcutKeymap).toBe("dbx");
    expect(store.editorSettings.shortcuts).toEqual(getShortcutPreset("dbx", "Win32"));
  });

  it("persists a synced Mac preset conversion once on Windows", async () => {
    persisted = normalizeEditorSettings({
      shortcutKeymap: "datagrip-macos",
      shortcuts: { ...getShortcutPreset("datagrip-macos"), newQuery: "Ctrl+Alt+Q" },
    }) as unknown as Record<string, unknown>;
    vi.spyOn(navigator, "platform", "get").mockReturnValue("Win32");
    const store = useSettingsStore();
    await store.initEditorSettings();
    expect(store.editorSettings.shortcutKeymap).toBe("dbx");
    expect(store.editorSettings.shortcuts.formatSql).toBe(getShortcutPreset("dbx", "Win32").formatSql);
    expect(store.editorSettings.shortcuts.newQuery).toBe("Ctrl+Alt+Q");
    expect(backend.saveEditorSettings).toHaveBeenCalledOnce();
    setActivePinia(createPinia());
    await useSettingsStore().initEditorSettings();
    expect(backend.saveEditorSettings).toHaveBeenCalledOnce();
  });

  it("never overwrites settings when the initial read fails and allows retry", async () => {
    backend.loadEditorSettings.mockRejectedValueOnce(new Error("storage unavailable"));
    const store = useSettingsStore();
    await expect(store.initEditorSettings()).rejects.toThrow("storage unavailable");
    expect(store.isEditorSettingsLoaded).toBe(false);
    expect(backend.saveEditorSettings).not.toHaveBeenCalled();
    persisted = { shortcuts: { formatSql: "Ctrl+Alt+F" } };
    await store.initEditorSettings();
    expect(store.editorSettings.shortcuts.formatSql).toBe("Ctrl+Alt+F");
    expect(backend.saveEditorSettings).toHaveBeenCalledOnce();
  });
});
