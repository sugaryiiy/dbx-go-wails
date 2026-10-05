// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive, type App } from "vue";
import { createI18n } from "vue-i18n";
import { afterEach, describe, expect, it, vi } from "vitest";
import ActionSearchDialog from "../ActionSearchDialog.vue";
import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog";
import type { ActionSearchItem } from "@/lib/app/actionSearch";
import { formatShortcutDisplay } from "@/lib/editor/shortcutDisplay";

const mountedApps: App[] = [];
const actions: ActionSearchItem[] = [
  { id: "run", label: "执行 SQL", group: "查询", keywords: ["Run", "Execute"], shortcut: "Mod+Enter" },
  { id: "stop", label: "停止查询", group: "查询", keywords: ["Stop"], disabled: true, disabledReason: "没有正在运行的查询" },
  { id: "format", label: "格式化 SQL", group: "编辑器", keywords: ["Format", "Reformat"], shortcut: "Mod+Alt+L" },
];

async function flush() {
  for (let turn = 0; turn < 5; turn += 1) {
    await nextTick();
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
}

afterEach(async () => {
  for (const app of mountedApps.splice(0)) app.unmount();
  await flush();
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

async function mountDialog(options: { items?: ActionSearchItem[]; openFollowup?: boolean } = {}) {
  const trigger = document.createElement("button");
  trigger.textContent = "Find Action";
  document.body.append(trigger);
  const host = document.createElement("div");
  document.body.append(host);
  const state = reactive({ open: false, followup: false, items: (options.items ?? actions).map((item) => ({ ...item })) });
  const events: string[] = [];
  const selected = vi.fn(async (id: string) => {
    events.push(`select:${id}`);
    expect(state.open).toBe(false);
    expect(document.activeElement).toBe(trigger);
    if (options.openFollowup) {
      await nextTick();
      state.followup = true;
    }
  });
  const app = createApp({
    setup: () => () => [
      h(ActionSearchDialog, {
        open: state.open,
        actions: state.items,
        "onUpdate:open": (open: boolean) => {
          events.push(`open:${open}`);
          state.open = open;
        },
        onSelect: selected,
      }),
      h(
        Dialog,
        {
          open: state.followup,
          "onUpdate:open": (open: boolean) => {
            state.followup = open;
          },
        },
        {
          default: () =>
            h(
              DialogContent,
              {},
              {
                default: () => [h(DialogTitle, {}, { default: () => "New Connection" }), h("input", { "aria-label": "Connection name" })],
              },
            ),
        },
      ),
    ],
  });
  app.use(
    createI18n({
      legacy: false,
      locale: "en",
      messages: { en: { actionSearch: { title: "Find Action", placeholder: "Search actions", empty: "No matching actions", hint: "Arrows select, Enter runs, Escape closes" } } },
    }),
  );
  app.mount(host);
  mountedApps.push(app);
  trigger.focus();
  state.open = true;
  await flush();
  return { state, events, selected, trigger };
}

function input() {
  return document.querySelector<HTMLInputElement>('input[role="combobox"]')!;
}

function press(key: string, options: KeyboardEventInit = {}) {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...options });
  input().dispatchEvent(event);
  return event;
}

async function search(value: string) {
  input().value = value;
  input().dispatchEvent(new Event("input", { bubbles: true }));
  await nextTick();
}

function row(id: string) {
  return document.querySelector<HTMLElement>(`[data-action-id="${id}"]`)!;
}

describe("ActionSearchDialog", () => {
  it("uses accessible dialog/combobox semantics and keyboard navigation skips disabled actions", async () => {
    const { selected, events } = await mountDialog();
    expect(document.activeElement).toBe(input());
    const dialog = document.querySelector('[role="dialog"]')!;
    expect(document.getElementById(dialog.getAttribute("aria-labelledby")!)?.textContent).toBe("Find Action");
    expect(input().getAttribute("aria-label")).toBe("Find Action");
    expect(document.getElementById(input().getAttribute("aria-controls")!)?.getAttribute("role")).toBe("listbox");
    expect(row("run").getAttribute("aria-selected")).toBe("true");
    expect(row("run").querySelector("kbd")?.textContent).toBe(formatShortcutDisplay("Mod+Enter"));
    expect(row("stop").getAttribute("aria-disabled")).toBe("true");
    expect(row("stop").textContent).toContain("没有正在运行的查询");

    press("ArrowDown");
    await nextTick();
    expect(input().getAttribute("aria-activedescendant")).toBe(row("format").id);
    press("ArrowDown");
    await nextTick();
    expect(row("run").getAttribute("aria-selected")).toBe("true");
    press("ArrowUp");
    await nextTick();
    expect(row("format").getAttribute("aria-selected")).toBe("true");
    press("Enter");
    expect(events).toEqual(["open:false"]);
    await flush();
    expect(events).toEqual(["open:false", "select:format"]);
    expect(selected).toHaveBeenCalledExactlyOnceWith("format");
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("searches Chinese, English aliases and groups, and supports mouse selection", async () => {
    const { selected } = await mountDialog();
    await search("格式化");
    expect(document.querySelectorAll('[role="option"]')).toHaveLength(1);
    expect(row("format")).toBeTruthy();
    await search("  FORMAT 编辑器  ");
    expect(document.querySelectorAll('[role="option"]')).toHaveLength(1);
    row("format").dispatchEvent(new MouseEvent("mouseenter"));
    row("format").click();
    await flush();
    expect(selected).toHaveBeenCalledExactlyOnceWith("format");
  });

  it("keeps disabled results visible and blocks keyboard and mouse execution", async () => {
    const { selected, state } = await mountDialog();
    await search("stop");
    expect(row("stop").textContent).toContain("没有正在运行的查询");
    expect(input().hasAttribute("aria-activedescendant")).toBe(false);
    press("ArrowDown");
    press("Enter");
    row("stop").click();
    await flush();
    expect(selected).not.toHaveBeenCalled();
    expect(state.open).toBe(true);
  });

  it("announces empty results and resets the query and selection after closing", async () => {
    const { state, selected, trigger } = await mountDialog();
    await search("does not exist");
    expect(document.querySelector('[role="status"]')?.textContent).toBe("No matching actions");
    expect(document.querySelectorAll('[role="option"]')).toHaveLength(0);
    press("ArrowDown");
    press("Enter");
    expect(selected).not.toHaveBeenCalled();
    press("Escape");
    await flush();
    expect(state.open).toBe(false);
    expect(document.activeElement).toBe(trigger);
    state.open = true;
    await flush();
    expect(input().value).toBe("");
    expect(document.activeElement).toBe(input());
    expect(row("run").getAttribute("aria-selected")).toBe("true");
  });

  it("ignores IME confirmation and repeated Enter, and prevents shortcuts from reaching the workbench", async () => {
    const { selected, state } = await mountDialog();
    const globalShortcut = vi.fn();
    window.addEventListener("keydown", globalShortcut);
    try {
      press("Enter", { isComposing: true });
      press("Enter", { keyCode: 229 });
      press("Enter", { repeat: true });
      press("l", { metaKey: true, shiftKey: true });
      press("f", { ctrlKey: true });
      await nextTick();
      expect(state.open).toBe(true);
      expect(selected).not.toHaveBeenCalled();
      expect(globalShortcut).not.toHaveBeenCalled();
      press("Enter");
      press("Enter");
      await flush();
      expect(selected).toHaveBeenCalledExactlyOnceWith("run");
    } finally {
      window.removeEventListener("keydown", globalShortcut);
    }
  });

  it("finishes restoring focus before an action opens another dialog", async () => {
    const { selected, state } = await mountDialog({ openFollowup: true });
    press("Enter");
    await flush();
    expect(selected).toHaveBeenCalledExactlyOnceWith("run");
    expect(state.followup).toBe(true);
    const nextInput = document.querySelector<HTMLInputElement>('input[aria-label="Connection name"]');
    expect(nextInput).toBeTruthy();
    expect(document.activeElement).toBe(nextInput);
    await flush();
    expect(document.activeElement).toBe(nextInput);
  });

  it("restores focus when the dialog close button is used without selecting an action", async () => {
    const { selected, state, trigger } = await mountDialog();
    document.querySelector<HTMLButtonElement>('[data-slot="dialog-close"]')!.click();
    await flush();
    expect(state.open).toBe(false);
    expect(selected).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(trigger);
  });

  it("preserves the chosen action when background availability refreshes the list", async () => {
    const { selected, state } = await mountDialog();
    press("ArrowDown");
    await nextTick();
    expect(row("format").getAttribute("aria-selected")).toBe("true");
    state.items = state.items.map((item) => (item.id === "stop" ? { ...item, disabled: false, disabledReason: undefined } : { ...item }));
    await nextTick();
    expect(row("format").getAttribute("aria-selected")).toBe("true");
    expect(row("stop").getAttribute("aria-disabled")).toBe("false");
    press("Enter");
    await flush();
    expect(selected).toHaveBeenCalledExactlyOnceWith("format");
  });

  it("tracks selection by id across reordered results and resets only for a new search", async () => {
    const { selected, state } = await mountDialog();
    press("ArrowDown");
    await nextTick();
    state.items = [state.items[2], state.items[1], state.items[0]];
    await nextTick();
    expect(row("format").getAttribute("aria-selected")).toBe("true");
    expect(input().getAttribute("aria-activedescendant")).toBe(row("format").id);
    press("ArrowDown");
    await nextTick();
    expect(row("run").getAttribute("aria-selected")).toBe("true");
    await search("sql");
    expect(row("format").getAttribute("aria-selected")).toBe("true");
    press("Enter");
    await flush();
    expect(selected).toHaveBeenCalledExactlyOnceWith("format");
  });

  it("moves to a nearby enabled action if the selected action becomes unavailable", async () => {
    const { selected, state } = await mountDialog({ items: [actions[0], actions[2], { ...actions[1], disabled: false }] });
    press("ArrowDown");
    await nextTick();
    expect(row("format").getAttribute("aria-selected")).toBe("true");
    state.items[1].disabled = true;
    state.items[1].disabledReason = "Query started";
    await nextTick();
    expect(row("stop").getAttribute("aria-selected")).toBe("true");
    press("Enter");
    await flush();
    expect(selected).toHaveBeenCalledExactlyOnceWith("stop");
  });

  it("handles removal of the selected action, all-disabled and empty lists without stale execution", async () => {
    const { selected, state } = await mountDialog({ items: [actions[0], actions[2], { ...actions[1], disabled: false }] });
    press("ArrowDown");
    await nextTick();
    state.items = state.items.filter((item) => item.id !== "format");
    await nextTick();
    expect(row("stop").getAttribute("aria-selected")).toBe("true");
    state.items = state.items.map((item) => ({ ...item, disabled: true }));
    await nextTick();
    expect(input().hasAttribute("aria-activedescendant")).toBe(false);
    press("Enter");
    expect(selected).not.toHaveBeenCalled();
    state.items = [];
    await nextTick();
    expect(document.querySelector('[role="status"]')?.textContent).toBe("No matching actions");
    press("Enter");
    expect(selected).not.toHaveBeenCalled();
    expect(state.open).toBe(true);
  });
});
