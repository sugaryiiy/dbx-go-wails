// @vitest-environment happy-dom

import * as langSql from "@codemirror/lang-sql";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { afterEach, describe, expect, it } from "vitest";
import { createDbxCodeMirrorSqlDialect } from "@/lib/editor/codemirrorSqlDialect";
import { DEFAULT_APP_EDITOR_THEMES, IDE_EDITOR_THEMES, loadEditorTheme } from "@/lib/editor/editorThemes";
import { wcagContrastRatio, type AppThemeAppearance, type AppThemePalette } from "@/lib/app/appTheme";
import { DEFAULT_CUSTOM_THEME_COLORS, type CustomThemeColors, type EditorTheme } from "@/stores/settingsStore";

let view: EditorView | undefined;

afterEach(() => {
  view?.destroy();
  view = undefined;
});

async function mount(theme: EditorTheme, appearance: AppThemeAppearance, palette: AppThemePalette = "pearl", colors?: CustomThemeColors) {
  const dialect = createDbxCodeMirrorSqlDialect(langSql, "postgres", "postgres");
  view = new EditorView({
    state: EditorState.create({
      doc: "-- Recent customers\nSELECT customer_name, count(*) FROM customers WHERE status = 'active' AND total > 42",
      extensions: [dialect.language, await loadEditorTheme(theme, appearance, colors, palette)],
    }),
    parent: document.body,
  });
  return view;
}

function tokenColor(editor: EditorView, token: string): string {
  const element = [...editor.contentDOM.querySelectorAll("span")].find((span) => span.textContent === token);
  expect(element, token).toBeDefined();
  return getComputedStyle(element!).color;
}

function cssColor(color: string): string {
  const element = document.createElement("span");
  element.style.color = color;
  return element.style.color;
}

describe("default SQL editor theme", () => {
  it.each(["light", "dark"] as const)("renders distinct SQL syntax with neutral identifiers in %s mode", async (appearance) => {
    const editor = await mount("app", appearance);
    const colors = DEFAULT_APP_EDITOR_THEMES[appearance];
    expect(tokenColor(editor, "SELECT")).toBe(cssColor(colors.keyword));
    expect(tokenColor(editor, "'active'")).toBe(cssColor(colors.string));
    expect(tokenColor(editor, "42")).toBe(cssColor(colors.number));
    expect(tokenColor(editor, "-- Recent customers")).toBe(cssColor(colors.comment));
    expect(tokenColor(editor, "customer_name")).toBe(getComputedStyle(editor.dom).color);
    expect(tokenColor(editor, "customers")).toBe(getComputedStyle(editor.dom).color);
    expect(tokenColor(editor, "count")).toBe(cssColor(colors.builtin));
    expect(new Set(["SELECT", "'active'", "42", "-- Recent customers", "customer_name"].map((token) => tokenColor(editor, token))).size).toBe(5);

    for (const color of [colors.foreground, colors.keyword, colors.string, colors.number, colors.function]) {
      expect(wcagContrastRatio(color, colors.background)).toBeGreaterThanOrEqual(4.5);
    }
    expect(wcagContrastRatio(colors.comment, colors.background)).toBeGreaterThanOrEqual(4.5);
    expect(wcagContrastRatio(colors.foreground, colors.selection)).toBeGreaterThanOrEqual(4.5);
  });

  it("preserves an explicitly selected editor preset", async () => {
    const editor = await mount("jetbrains-dark", "light");
    expect(tokenColor(editor, "SELECT")).toBe(cssColor(IDE_EDITOR_THEMES.jetbrainsDark.keyword));
    expect(tokenColor(editor, "42")).toBe(cssColor(IDE_EDITOR_THEMES.jetbrainsDark.number));
  });

  it("follows an explicitly selected IDE app palette", async () => {
    const editor = await mount("app", "light", "jetbrains");
    expect(tokenColor(editor, "SELECT")).toBe(cssColor(IDE_EDITOR_THEMES.jetbrainsLight.keyword));
    expect(tokenColor(editor, "42")).toBe(cssColor(IDE_EDITOR_THEMES.jetbrainsLight.number));
  });

  it("preserves user-defined syntax colors", async () => {
    const colors = { ...DEFAULT_CUSTOM_THEME_COLORS, keyword: "#aabbcc", string: "#12ab34", number: "#eeaa55" };
    const editor = await mount("custom", "dark", "pearl", colors);
    expect(tokenColor(editor, "SELECT")).toBe(cssColor(colors.keyword));
    expect(tokenColor(editor, "'active'")).toBe(cssColor(colors.string));
    expect(tokenColor(editor, "42")).toBe(cssColor(colors.number));
  });
});
