// @vitest-environment happy-dom

import { createApp, h, nextTick, ref } from "vue";
import { createPinia, setActivePinia } from "pinia";
import { createI18n } from "vue-i18n";
import { afterEach, describe, expect, it, vi } from "vitest";
import ConnectionTree from "../ConnectionTree.vue";
import { useConnectionStore } from "@/stores/connectionStore";
import { useSettingsStore } from "@/stores/settingsStore";
import en from "@/i18n/locales/en";
import type { ConnectionConfig } from "@/types/database";

const cleanups: Array<() => void> = [];
afterEach(() => {
  cleanups
    .splice(0)
    .reverse()
    .forEach((cleanup) => cleanup());
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

async function mountTree() {
  const pinia = createPinia();
  setActivePinia(pinia);
  const store = useConnectionStore();
  const settings = useSettingsStore();
  settings.editorSettings.sidebarGlobalSearchLocal = true;
  settings.editorSettings.shortcuts.editSidebarConnection = "Ctrl+E";
  settings.editorSettings.shortcuts.disconnectSidebarConnection = "Ctrl+D";
  store.connections = [{ id: "sqlite", name: "Orders", db_type: "sqlite", database: "/tmp/orders.db", read_only: false }] as ConnectionConfig[];
  store.treeNodes = [{ id: "sqlite", label: "Orders", type: "connection", connectionId: "sqlite", isExpanded: false, children: [] }];
  store.selectedTreeNodeId = "sqlite";
  store.selectedTreeNodeIds = ["sqlite"];
  store.connectedIds.add("sqlite");
  const edit = vi.spyOn(store, "startEditing").mockImplementation(() => {});
  const disconnect = vi.spyOn(store, "disconnect").mockResolvedValue(undefined);
  const visible = ref(true);
  const host = document.createElement("div");
  document.body.append(host);
  const app = createApp({ render: () => h("section", { style: { display: visible.value ? "" : "none" } }, [h(ConnectionTree)]) });
  app.use(pinia);
  app.use(createI18n({ legacy: false, locale: "en", messages: { en }, missingWarn: false, fallbackWarn: false }));
  app.mount(host);
  cleanups.push(() => {
    app.unmount();
    host.remove();
  });
  await nextTick();
  const root = host.querySelector("section")!.firstElementChild as HTMLElement;
  // happy-dom has no layout; model the rectangles produced by the real v-show parent.
  vi.spyOn(root, "getClientRects").mockImplementation(() => (visible.value ? [{ width: 260, height: 600 }] : []) as unknown as DOMRectList);
  root.dispatchEvent(new Event("pointerenter"));
  const search = host.querySelector<HTMLInputElement>("[data-sidebar-search] input")!;
  return { host, root, search, visible, edit, disconnect };
}

function key(target: EventTarget, value: string, options: KeyboardEventInit = {}) {
  const event = new KeyboardEvent("keydown", { key: value, bubbles: true, cancelable: true, ...options });
  target.dispatchEvent(event);
  return event;
}

describe("Mounted connection tree shortcut visibility", () => {
  it("ignores pointer-based type search while hidden and keeps the previous search when reopened", async () => {
    const { root, search, visible } = await mountTree();
    expect(key(document.body, "o").defaultPrevented).toBe(true);
    await nextTick();
    expect(search.value).toBe("o");
    search.blur();
    visible.value = false;
    await nextTick();
    // No pointerleave occurs when a keyboard shortcut changes the resource section.
    expect(key(document.body, "x").defaultPrevented).toBe(false);
    expect(key(document.body, "Escape").defaultPrevented).toBe(false);
    await nextTick();
    expect(search.value).toBe("o");
    expect(document.activeElement).not.toBe(search);

    visible.value = true;
    await nextTick();
    root.dispatchEvent(new Event("pointerenter"));
    expect(key(document.body, "r").defaultPrevented).toBe(true);
    await nextTick();
    expect(search.value).toBe("or");
    expect(document.activeElement).toBe(search);
  });

  it("blocks editing and disconnecting a hidden selected connection without changing visible shortcuts", async () => {
    const { root, visible, edit, disconnect } = await mountTree();
    expect(key(document.body, "e", { ctrlKey: true }).defaultPrevented).toBe(true);
    expect(edit).toHaveBeenLastCalledWith("sqlite");
    edit.mockClear();
    visible.value = false;
    await nextTick();
    expect(key(document.body, "e", { ctrlKey: true }).defaultPrevented).toBe(false);
    expect(key(root, "d", { ctrlKey: true }).defaultPrevented).toBe(false);
    expect(edit).not.toHaveBeenCalled();
    expect(disconnect).not.toHaveBeenCalled();

    visible.value = true;
    await nextTick();
    expect(key(root, "e", { ctrlKey: true }).defaultPrevented).toBe(true);
    expect(edit).toHaveBeenCalledOnce();
    expect(key(root, "d", { ctrlKey: true }).defaultPrevented).toBe(true);
    expect(disconnect).toHaveBeenLastCalledWith("sqlite");
  });
});
