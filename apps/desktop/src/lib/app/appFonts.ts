export const APP_FONT_SANS_CSS_VAR = "--font-sans";
export const FONT_MONO_CSS_VAR = "--font-mono";
export const DATA_GRID_FONT_FAMILY_CSS_VAR = "--dbx-data-grid-font-family";

export const DEFAULT_UI_FONT_FAMILY = `"Geist Variable", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", "Segoe UI", system-ui, sans-serif`;
// Prefer installed system monospace faces before CJK fonts, which also contain
// proportional Latin glyphs. This keeps SQL aligned when optional coding fonts
// are absent while retaining readable Chinese/CJK glyph fallbacks.
export const DEFAULT_MONO_FALLBACK_FONTS = `ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", monospace`;
const LEGACY_CJK_MONO_FALLBACK_FONTS = `"PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", monospace`;

// Keep in sync with the editor font default in settingsStore; also the baseline
// `--font-mono` value in tokens.css, which plugin sandboxes consume via the bridge.
export const DEFAULT_MONO_FONT_FAMILY = `'Fira Code', 'Cascadia Code', 'Cascadia Mono', 'JetBrains Mono', ${DEFAULT_MONO_FALLBACK_FONTS}`;
export const DEFAULT_DATA_GRID_FONT_FAMILY = `"Geist Variable Tabular", "Geist Variable", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif`;

// Native-feeling UI option without DBX's bundled/brand font at the front of the stack.
export const SYSTEM_UI_FONT_FAMILY = `system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif`;

export const FONT_FAMILIES: { value: string; label: string }[] = [
  { value: `'Fira Code', 'Cascadia Code', 'Cascadia Mono', 'JetBrains Mono', ${DEFAULT_MONO_FALLBACK_FONTS}`, label: "Fira Code" },
  { value: `'JetBrains Mono', 'Fira Code', ${DEFAULT_MONO_FALLBACK_FONTS}`, label: "JetBrains Mono" },
  { value: `'Cascadia Code', 'Cascadia Mono', ${DEFAULT_MONO_FALLBACK_FONTS}`, label: "Cascadia Code" },
  { value: `'Source Code Pro', ${DEFAULT_MONO_FALLBACK_FONTS}`, label: "Source Code Pro" },
  { value: `'SF Mono', 'Menlo', ${DEFAULT_MONO_FALLBACK_FONTS}`, label: "SF Mono / Menlo" },
  { value: `'Consolas', 'Courier New', ${DEFAULT_MONO_FALLBACK_FONTS}`, label: "Consolas" },
  { value: DEFAULT_MONO_FALLBACK_FONTS, label: "System Monospace" },
];

export const LEGACY_PRESET_FONT_MAP: Record<string, string> = {
  // Upgrade only exact built-in presets; user-written stacks remain intact.
  ...Object.fromEntries(FONT_FAMILIES.map(({ value }) => [value.replace(DEFAULT_MONO_FALLBACK_FONTS, LEGACY_CJK_MONO_FALLBACK_FONTS), value])),
  "'Fira Code', 'Cascadia Code', 'Cascadia Mono', 'JetBrains Mono', monospace": DEFAULT_MONO_FONT_FAMILY,
  "'JetBrains Mono', 'Fira Code', monospace": `'JetBrains Mono', 'Fira Code', ${DEFAULT_MONO_FALLBACK_FONTS}`,
  "'Cascadia Code', 'Cascadia Mono', monospace": `'Cascadia Code', 'Cascadia Mono', ${DEFAULT_MONO_FALLBACK_FONTS}`,
  "'Source Code Pro', monospace": `'Source Code Pro', ${DEFAULT_MONO_FALLBACK_FONTS}`,
  "'SF Mono', 'Menlo', monospace": `'SF Mono', 'Menlo', ${DEFAULT_MONO_FALLBACK_FONTS}`,
  "'Consolas', 'Courier New', monospace": `'Consolas', 'Courier New', ${DEFAULT_MONO_FALLBACK_FONTS}`,
  monospace: DEFAULT_MONO_FALLBACK_FONTS,
};

export function cssFontFamilyForName(name: string): string {
  return `'${name.replace(/\\/g, "\\\\").replace(/'/g, "\\'")}', ${DEFAULT_MONO_FALLBACK_FONTS}`;
}

export function readableFontFamily(value: string): string {
  const firstFamily = value.split(",")[0]?.trim() ?? value;
  return firstFamily.replace(/^['"]|['"]$/g, "").replace(/\\'/g, "'");
}

export function normalizeCustomFontFamilyInput(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) return "";
  if (trimmed.includes(",") || trimmed.includes("'") || trimmed.includes('"')) return trimmed;
  return cssFontFamilyForName(trimmed);
}
