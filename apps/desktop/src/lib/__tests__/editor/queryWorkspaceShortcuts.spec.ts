// @vitest-environment happy-dom
import { describe, expect, it, vi } from "vitest";
import {
  handleQueryWorkspaceShortcut,
  handleTabHistoryNavigationShortcut,
  isCancelQueryShortcut,
  isRecentTabsShortcut,
  isToggleQueryHistoryShortcut,
  type QueryWorkspaceShortcutContext,
} from "@/lib/editor/keyboardShortcuts";

const shortcuts = { cancelQuery: "Mod+F2", toggleQueryHistory: "Mod+Alt+E", recentTabs: "Mod+E" };
const cancelEvent = { key: "F2", code: "F2", metaKey: true };
const recentTabsEvent = { key: "e", code: "KeyE", metaKey: true };
// Option+E can report a dead key on a Mac keyboard layout.
const historyEvent = { key: "Dead", code: "KeyE", metaKey: true, altKey: true };

function context(overrides: Partial<QueryWorkspaceShortcutContext> = {}): QueryWorkspaceShortcutContext {
  return {
    inactiveSurface: false,
    hasOpenModal: () => false,
    activeTab: { id: "active-query", isExecuting: true },
    cancelQuery: vi.fn(),
    toggleQueryHistory: vi.fn(),
    openRecentTabs: vi.fn(),
    ...overrides,
  };
}

describe("query workspace shortcut dispatch", () => {
  it("matches configured Mac keys, including Option characters, without adding a legacy default", () => {
    expect(isCancelQueryShortcut(cancelEvent, shortcuts, "MacIntel")).toBe(true);
    expect(isToggleQueryHistoryShortcut(historyEvent, shortcuts, "MacIntel")).toBe(true);
    expect(isRecentTabsShortcut(recentTabsEvent, shortcuts, "MacIntel")).toBe(true);
    expect(isCancelQueryShortcut(cancelEvent, undefined, "MacIntel")).toBe(false);
    expect(isToggleQueryHistoryShortcut(historyEvent, undefined, "MacIntel")).toBe(false);
    expect(isRecentTabsShortcut(recentTabsEvent, undefined, "MacIntel")).toBe(false);
    expect(isCancelQueryShortcut({ ...cancelEvent, ctrlKey: true }, shortcuts, "MacIntel")).toBe(false);
    expect(isToggleQueryHistoryShortcut({ ...historyEvent, shiftKey: true }, shortcuts, "MacIntel")).toBe(false);
    expect(isRecentTabsShortcut({ ...recentTabsEvent, altKey: true }, shortcuts, "MacIntel")).toBe(false);
    expect(isCancelQueryShortcut({ ...cancelEvent, isComposing: true }, shortcuts, "MacIntel")).toBe(false);
    expect(isToggleQueryHistoryShortcut({ ...historyEvent, isComposing: true }, shortcuts, "MacIntel")).toBe(false);
    expect(isRecentTabsShortcut({ ...recentTabsEvent, isComposing: true }, shortcuts, "MacIntel")).toBe(false);
  });

  it.each(["isExecuting", "isExplaining"] as const)("cancels only the active tab while %s", (state) => {
    const callbacks = context({ activeTab: { id: "target-tab", [state]: true } });
    expect(handleQueryWorkspaceShortcut(cancelEvent, shortcuts, callbacks, "MacIntel")).toBe(true);
    expect(callbacks.cancelQuery).toHaveBeenCalledExactlyOnceWith("target-tab");
    expect(callbacks.toggleQueryHistory).not.toHaveBeenCalled();
  });

  it.each([
    { id: "idle" },
    { id: "cancelling", isExecuting: true, isCancelling: true },
    { id: "cancelling-explain", isExplaining: true, isCancelling: true },
    null,
  ])("consumes Stop without dispatching when the active tab cannot be cancelled (%j)", (activeTab) => {
    const callbacks = context({ activeTab });
    expect(handleQueryWorkspaceShortcut(cancelEvent, shortcuts, callbacks, "MacIntel")).toBe(true);
    expect(callbacks.cancelQuery).not.toHaveBeenCalled();
    expect(callbacks.toggleQueryHistory).not.toHaveBeenCalled();
  });

  it("toggles the existing history panel without executing or cancelling a query", () => {
    let historyOpen = false;
    const callbacks = context({ toggleQueryHistory: () => { historyOpen = !historyOpen; } });
    expect(handleQueryWorkspaceShortcut(historyEvent, shortcuts, callbacks, "MacIntel")).toBe(true);
    expect(historyOpen).toBe(true);
    expect(handleQueryWorkspaceShortcut(historyEvent, shortcuts, callbacks, "MacIntel")).toBe(true);
    expect(historyOpen).toBe(false);
    expect(callbacks.cancelQuery).not.toHaveBeenCalled();
    expect(callbacks.openRecentTabs).not.toHaveBeenCalled();
  });

  it("opens recent tabs without toggling query history or cancelling a query", () => {
    const callbacks = context();
    expect(handleQueryWorkspaceShortcut(recentTabsEvent, shortcuts, callbacks, "MacIntel")).toBe(true);
    expect(callbacks.openRecentTabs).toHaveBeenCalledExactlyOnceWith();
    expect(callbacks.toggleQueryHistory).not.toHaveBeenCalled();
    expect(callbacks.cancelQuery).not.toHaveBeenCalled();
  });

  it.each(["inactiveSurface", "modal", "shortcutRecorder", "sqlShortcutRecorder", "dialog", "alertdialog", "composition"])("leaves %s in control of workspace shortcuts", (blocked) => {
    const target = document.createElement("input");
    const parent = document.createElement("div");
    parent.append(target);
    if (blocked === "shortcutRecorder") target.setAttribute("data-shortcut-input", "cancelQuery");
    if (blocked === "sqlShortcutRecorder") target.setAttribute("data-sql-shortcut-input", "dialog");
    if (blocked === "dialog" || blocked === "alertdialog") parent.setAttribute("role", blocked);
    const callbacks = context({
      target,
      inactiveSurface: blocked === "inactiveSurface",
      hasOpenModal: () => blocked === "modal",
    });
    for (const event of [cancelEvent, historyEvent, recentTabsEvent]) {
      expect(handleQueryWorkspaceShortcut({ ...event, isComposing: blocked === "composition" }, shortcuts, callbacks, "MacIntel")).toBe(false);
    }
    expect(callbacks.cancelQuery).not.toHaveBeenCalled();
    expect(callbacks.toggleQueryHistory).not.toHaveBeenCalled();
    expect(callbacks.openRecentTabs).not.toHaveBeenCalled();
  });

  it("ignores unrelated keys without querying the document for modal state", () => {
    const callbacks = context({ hasOpenModal: vi.fn(() => false) });
    expect(handleQueryWorkspaceShortcut({ key: "x" }, shortcuts, callbacks, "MacIntel")).toBe(false);
    expect(callbacks.hasOpenModal).not.toHaveBeenCalled();
    expect(callbacks.cancelQuery).not.toHaveBeenCalled();
    expect(callbacks.toggleQueryHistory).not.toHaveBeenCalled();
  });
});

describe("Mac navigation key boundaries", () => {
  it.each([["[", -1], ["]", 1]] as const)("consumes Cmd+%s even when navigation cannot move", (key, direction) => {
    const navigate = vi.fn(() => false);
    const navigation = { navigateTabHistoryBack: "Mod+[", navigateTabHistoryForward: "Mod+]" };
    expect(handleTabHistoryNavigationShortcut({ key, metaKey: true }, navigation, navigate, "MacIntel")).toBe(true);
    expect(navigate).toHaveBeenCalledExactlyOnceWith(direction);
  });

  it("does not consume an unrelated or composing navigation key", () => {
    const navigate = vi.fn(() => false);
    const navigation = { navigateTabHistoryBack: "Mod+[", navigateTabHistoryForward: "Mod+]" };
    expect(handleTabHistoryNavigationShortcut({ key: "[" }, navigation, navigate, "MacIntel")).toBe(false);
    expect(handleTabHistoryNavigationShortcut({ key: "[", metaKey: true, isComposing: true }, navigation, navigate, "MacIntel")).toBe(false);
    expect(navigate).not.toHaveBeenCalled();
  });
});
