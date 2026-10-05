// @vitest-environment happy-dom

import { createApp, h, nextTick, reactive } from "vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import { TooltipProvider } from "@/components/ui/tooltip";
import { formatShortcutTooltip } from "@/lib/editor/shortcutDisplay";
import WorkbenchToolRail, { type WorkbenchToolItem } from "../WorkbenchToolRail.vue";

const cleanups: Array<() => void> = [];
afterEach(() => {
  for (const cleanup of cleanups.splice(0).reverse()) cleanup();
});

async function mountRail(side: "left" | "right" = "left") {
  // Deliberately interleave the bottom action: navigation must follow rendered
  // order rather than the incoming array order.
  const props = reactive({
    label: "Database tools",
    side,
    items: [
      { id: "settings", label: "Settings", icon: "settings", bottom: true },
      { id: "database", label: "Database", icon: "database", active: true, shortcut: "Mod+1" },
      { id: "search", label: "Search", icon: "search", disabled: true },
      { id: "history", label: "Query history", icon: "history", badge: 12, attention: true },
    ] as WorkbenchToolItem[],
  });
  const onSelect = vi.fn();
  const host = document.createElement("div");
  document.body.append(host);
  const app = createApp({ render: () => h(TooltipProvider, {}, { default: () => h(WorkbenchToolRail, { ...props, onSelect }) }) });
  app.mount(host);
  cleanups.push(() => {
    app.unmount();
    host.remove();
  });
  await nextTick();
  const button = (id: string) => host.querySelector<HTMLButtonElement>(`[data-workbench-tool="${id}"]`)!;
  return { host, props, onSelect, button };
}

function press(target: HTMLElement, key: string, init: KeyboardEventInit = {}) {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  target.dispatchEvent(event);
  return event;
}

describe("Workbench tool rail", () => {
  it.each(["left", "right"] as const)("activates real %s rail buttons while native disabled buttons remain inert", async (side) => {
    const { host, props, onSelect, button } = await mountRail(side);
    expect(host.querySelector("nav")?.getAttribute("aria-label")).toBe("Database tools");
    expect(button("database").getAttribute("title")).toBe(formatShortcutTooltip("Database", "Mod+1"));
    expect(button("database").getAttribute("aria-pressed")).toBe("true");
    button("database").click();
    button("search").click();
    button("settings").click();
    expect(onSelect.mock.calls).toEqual([["database"], ["settings"]]);

    props.items.find((item) => item.id === "database")!.active = false;
    props.items.find((item) => item.id === "search")!.disabled = false;
    props.items.find((item) => item.id === "search")!.active = true;
    await nextTick();
    expect(button("database").getAttribute("aria-pressed")).toBe("false");
    expect(button("search").getAttribute("aria-pressed")).toBe("true");
    expect(button("search").classList.contains("is-active")).toBe(true);
    button("search").click();
    expect(onSelect).toHaveBeenLastCalledWith("search");
  });

  it("moves focus in rendered order, skips disabled tools, and wraps across the bottom group", async () => {
    const { button, onSelect } = await mountRail();
    button("database").focus();
    expect(press(button("database"), "ArrowDown").defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(button("history"));
    press(button("history"), "ArrowDown");
    expect(document.activeElement).toBe(button("settings"));
    press(button("settings"), "ArrowDown");
    expect(document.activeElement).toBe(button("database"));
    press(button("database"), "ArrowUp");
    expect(document.activeElement).toBe(button("settings"));
    press(button("settings"), "ArrowUp");
    expect(document.activeElement).toBe(button("history"));
    press(button("history"), "ArrowUp");
    expect(document.activeElement).toBe(button("database"));
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("uses Home and End for enabled endpoints and includes newly enabled tools", async () => {
    const { props, button } = await mountRail();
    button("history").focus();
    expect(press(button("history"), "Home").defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(button("database"));
    expect(press(button("database"), "End").defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(button("settings"));

    props.items.find((item) => item.id === "settings")!.disabled = true;
    props.items.find((item) => item.id === "search")!.disabled = false;
    await nextTick();
    button("database").focus();
    press(button("database"), "ArrowDown");
    expect(document.activeElement).toBe(button("search"));
    press(button("search"), "End");
    expect(document.activeElement).toBe(button("history"));
  });

  it.each(["altKey", "ctrlKey", "metaKey", "shiftKey", "isComposing"] as const)("does not consume navigation during %s", async (modifier) => {
    const { button, onSelect } = await mountRail();
    button("database").focus();
    for (const key of ["ArrowUp", "ArrowDown", "Home", "End"]) {
      const event = press(button("database"), key, { [modifier]: true });
      expect(event.defaultPrevented).toBe(false);
      expect(document.activeElement).toBe(button("database"));
    }
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("leaves unrelated keys and events without a focused rail button untouched", async () => {
    const { host, button } = await mountRail();
    button("database").focus();
    expect(press(button("database"), "ArrowLeft").defaultPrevented).toBe(false);
    expect(press(button("database"), "F8").defaultPrevented).toBe(false);
    button("database").blur();
    expect(press(host.querySelector("nav")!, "ArrowDown").defaultPrevented).toBe(false);
    expect(document.activeElement).not.toBe(button("history"));
  });

  it("updates count badges and falls back to attention dots without showing zero", async () => {
    const { props, button } = await mountRail();
    expect(button("history").querySelector(".workbench-tool-badge")?.textContent).toBe("9+");
    const history = props.items.find((item) => item.id === "history")!;
    history.badge = 2;
    await nextTick();
    expect(button("history").querySelector(".workbench-tool-badge")?.textContent).toBe("2");
    history.badge = 0;
    await nextTick();
    expect(button("history").querySelector(".workbench-tool-badge")).toBeNull();
    expect(button("history").querySelector(".workbench-tool-dot")).not.toBeNull();
    history.attention = false;
    await nextTick();
    expect(button("history").querySelector(".workbench-tool-dot")).toBeNull();
  });
});
