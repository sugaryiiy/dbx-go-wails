import { isMacShortcutPlatform, parseShortcutStrokes } from "@/lib/editor/shortcutDisplay";
import { normalizeShortcutSettings, SHORTCUT_DEFINITIONS, type ShortcutActionId, type ShortcutSettings } from "@/lib/editor/shortcutRegistry";

export type ShortcutKeymap = "datagrip-macos" | "dbx";
export const SHORTCUT_KEYMAP_VERSION = 2;

// DataGrip's macOS keymap (not the separate macOS System / IntelliJ Classic
// variants). Only map actions backed by an existing DBX operation.
// References: https://www.jetbrains.com/help/datagrip/mastering-keyboard-shortcuts.html
// https://www.jetbrains.com/help/datagrip/run-a-query.html
const DATAGRIP_MACOS_V1: Partial<ShortcutSettings> = {
  sendSelectionToAi: "Mod+Shift+A",
  executeSql: "Mod+Enter",
  cancelQuery: "Mod+F2",
  toggleQueryHistory: "Mod+Alt+E",
  recentTabs: "Mod+E",
  // Cmd+E belongs to Recent Files in DataGrip. Explain remains in the toolbar.
  explainSql: "",
  formatSql: "Mod+Alt+L",
  toggleLineComment: "Mod+/",
  toggleBlockComment: "Mod+Alt+/",
  triggerCompletion: "Ctrl+Space",
  insertLineBelow: "Shift+Enter",
  joinLines: "Ctrl+Shift+J",
  duplicateLine: "Mod+D",
  deleteLine: "Mod+Backspace",
  moveLineUp: "Alt+Shift+ArrowUp",
  moveLineDown: "Alt+Shift+ArrowDown",
  // These DBX-only defaults occupy DataGrip's Move Line bindings.
  copyLineUp: "",
  copyLineDown: "",
  extendSelection: "Alt+ArrowUp",
  addNextSelectionOccurrence: "Ctrl+G",
  selectAllSelectionOccurrences: "Ctrl+Mod+G",
  toggleCaseSelection: "Mod+Shift+U",
  foldAll: "Mod+Shift+-",
  unfoldAll: "Mod+Shift+Plus",
  newQuery: "Mod+Shift+L",
  quickOpen: "Mod+O",
  globalSearch: "Mod+Shift+F",
  navigateTabHistoryBack: "Mod+[",
  navigateTabHistoryForward: "Mod+]",
  gotoLine: "Mod+L",
  find: "Mod+F",
  replace: "Mod+R",
  toggleSidebar: "Mod+1",
  // A global action cannot share Cmd+1 with the Database tool window.
  switchToTab1: "",
  sqlIntentionActions: "Alt+Enter",
};

const DATAGRIP_MACOS: Partial<ShortcutSettings> = {
  ...DATAGRIP_MACOS_V1,
  findAction: "Mod+Shift+A",
  shrinkSelection: "Alt+ArrowDown",
  // Cmd+Shift+A opens Find Action in DataGrip. Sending SQL to AI remains in its menu.
  sendSelectionToAi: "",
};

export function defaultShortcutKeymap(platform = globalThis.navigator?.platform || ""): ShortcutKeymap {
  return isMacShortcutPlatform(platform) ? "datagrip-macos" : "dbx";
}

export function getShortcutPreset(keymap: ShortcutKeymap, platform = globalThis.navigator?.platform || ""): ShortcutSettings {
  return shortcutPresetAtVersion(keymap, platform, SHORTCUT_KEYMAP_VERSION);
}

function shortcutPresetAtVersion(keymap: ShortcutKeymap, platform: string, version: number): ShortcutSettings {
  const defaults = normalizeShortcutSettings(undefined, platform);
  const overrides = version < 2 ? DATAGRIP_MACOS_V1 : DATAGRIP_MACOS;
  return normalizeShortcutSettings(keymap === "datagrip-macos" && isMacShortcutPlatform(platform) ? { ...defaults, ...overrides } : defaults, platform);
}

type SavedKeymap = {
  shortcuts?: Partial<ShortcutSettings>;
  shortcutKeymap?: ShortcutKeymap;
  shortcutKeymapVersion?: number;
};

function keyIdentity(shortcut: string, platform: string): string {
  return parseShortcutStrokes(shortcut)
    .map((parts) => {
      const key = parts[parts.length - 1]?.toLowerCase() ?? "";
      const modifiers = parts
        .slice(0, -1)
        .map((part) => {
          if (part === "Mod") return isMacShortcutPlatform(platform) ? "meta" : "ctrl";
          if (part === "Cmd") return "meta";
          if (part === "Control") return "ctrl";
          return part.toLowerCase();
        })
        .sort();
      return [...modifiers, key].join("+");
    })
    .join(" ");
}

/** Adoption and version upgrades: saved custom keys (including explicit unbinding) win.
 * Clear an incoming default if it would shadow an existing custom action.
 * Scope overlaps with global actions matter here because global listeners run
 * after the focused editor/grid has had a chance to consume the event. */
export function migrateShortcutKeymap(saved: SavedKeymap = {}, platform = globalThis.navigator?.platform || "") {
  if (saved.shortcutKeymap === "datagrip-macos" && !isMacShortcutPlatform(platform)) {
    // A synced Mac preset is not offered on Windows/Linux. Translate its
    // unchanged defaults back to DBX, retaining independently customized keys.
    const macDefaults = shortcutPresetAtVersion("datagrip-macos", "MacIntel", saved.shortcutKeymapVersion ?? 1);
    const shortcuts = getShortcutPreset("dbx", platform);
    const custom = new Set<ShortcutActionId>();
    for (const definition of SHORTCUT_DEFINITIONS) {
      const configured = saved.shortcuts?.[definition.id];
      if (typeof configured === "string" && keyIdentity(configured, "MacIntel") !== keyIdentity(macDefaults[definition.id], "MacIntel")) {
        shortcuts[definition.id] = configured;
        custom.add(definition.id);
      }
    }
    clearConflictingDefaults(shortcuts, custom, platform);
    return { shortcutKeymap: "dbx" as const, shortcutKeymapVersion: SHORTCUT_KEYMAP_VERSION, shortcuts: normalizeShortcutSettings(shortcuts, platform) };
  }
  const shortcutKeymap = saved.shortcutKeymap === "dbx" || saved.shortcutKeymap === "datagrip-macos" ? saved.shortcutKeymap : defaultShortcutKeymap(platform);
  const defaults = getShortcutPreset(shortcutKeymap, platform);
  if ((saved.shortcutKeymapVersion ?? 0) >= SHORTCUT_KEYMAP_VERSION || shortcutKeymap === "dbx") {
    return { shortcutKeymap, shortcutKeymapVersion: Math.max(SHORTCUT_KEYMAP_VERSION, saved.shortcutKeymapVersion ?? 0), shortcuts: normalizeShortcutSettings({ ...defaults, ...saved.shortcuts }, platform) };
  }

  const legacy =
    saved.shortcutKeymap === "datagrip-macos" || saved.shortcutKeymapVersion === 1
      ? shortcutPresetAtVersion("datagrip-macos", platform, 1)
      : normalizeShortcutSettings(undefined, platform);
  const previous = normalizeShortcutSettings(saved.shortcuts, platform);
  const custom = new Set<ShortcutActionId>();
  for (const definition of SHORTCUT_DEFINITIONS) {
    const value = saved.shortcuts?.[definition.id];
    // Inspect the raw saved object: normalizing first adds empty placeholders
    // for new actions, which are not evidence that the user unbound them.
    if (typeof value === "string" && (value === "" || keyIdentity(previous[definition.id], platform) !== keyIdentity(legacy[definition.id], platform))) custom.add(definition.id);
  }
  const shortcuts = { ...defaults };
  for (const actionId of custom) shortcuts[actionId] = previous[actionId];
  clearConflictingDefaults(shortcuts, custom, platform, legacy);
  return { shortcutKeymap, shortcutKeymapVersion: SHORTCUT_KEYMAP_VERSION, shortcuts: normalizeShortcutSettings(shortcuts, platform) };
}

function clearConflictingDefaults(shortcuts: ShortcutSettings, custom: Set<ShortcutActionId>, platform: string, legacy?: ShortcutSettings): void {
  for (const definition of SHORTCUT_DEFINITIONS) {
    const id = definition.id;
    if (custom.has(id) || !shortcuts[id] || (legacy && keyIdentity(shortcuts[id], platform) === keyIdentity(legacy[id], platform))) continue;
    const conflict = SHORTCUT_DEFINITIONS.some((other) => custom.has(other.id) && (definition.scope === other.scope || definition.scope === "global" || other.scope === "global") && keyIdentity(shortcuts[other.id], platform) === keyIdentity(shortcuts[id], platform));
    if (conflict) shortcuts[id] = "";
  }
}
