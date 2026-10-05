import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it } from "vitest";
import { DEFAULT_EDITOR_SETTINGS, normalizeEditorSettings, useSettingsStore } from "../settingsStore";

describe("completion casing settings", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("defaults completion to lowercase independently from saved formatter settings", () => {
    expect(DEFAULT_EDITOR_SETTINGS).toMatchObject({ completionKeywordCase: "lower", completionFunctionCase: "lower" });
    const formatter = { ...DEFAULT_EDITOR_SETTINGS.sqlFormatter, keywordCase: "upper" as const, functionCase: "upper" as const };
    const settings = normalizeEditorSettings({ sqlFormatter: formatter });
    expect(settings).toMatchObject({ completionKeywordCase: "lower", completionFunctionCase: "lower", sqlFormatter: formatter });
  });

  it.each(["lower", "upper", "preserve"] as const)("retains explicit %s preferences through normalization and live store updates", (casing) => {
    expect(normalizeEditorSettings({ completionKeywordCase: casing, completionFunctionCase: casing })).toMatchObject({ completionKeywordCase: casing, completionFunctionCase: casing });
    const store = useSettingsStore();
    store.updateEditorSettings({ completionKeywordCase: casing, completionFunctionCase: casing });
    store.updateEditorSettings({ fontSize: 15 });
    expect(store.editorSettings).toMatchObject({ completionKeywordCase: casing, completionFunctionCase: casing });
    expect(store.editorSettings.sqlFormatter).toEqual(DEFAULT_EDITOR_SETTINGS.sqlFormatter);
  });

  it.each([null, "invalid", 1, false])("normalizes invalid persisted and live values %s", (value) => {
    const patch = { completionKeywordCase: value, completionFunctionCase: value } as never;
    expect(normalizeEditorSettings(patch)).toMatchObject({ completionKeywordCase: "lower", completionFunctionCase: "lower" });
    const store = useSettingsStore();
    store.updateEditorSettings({ completionKeywordCase: "upper", completionFunctionCase: "upper" });
    store.updateEditorSettings(patch);
    expect(store.editorSettings).toMatchObject({ completionKeywordCase: "lower", completionFunctionCase: "lower" });
  });
});
