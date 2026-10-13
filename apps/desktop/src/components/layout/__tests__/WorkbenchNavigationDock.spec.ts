// @vitest-environment happy-dom

import { createApp, defineComponent, h, nextTick, onMounted, onUnmounted, reactive } from "vue";
import { createI18n } from "vue-i18n";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { WorkbenchNavigationSection } from "@/composables/useWorkbenchNavigation";
import { usePanelResize } from "@/composables/usePanelResize";
import WorkbenchNavigationDock from "../WorkbenchNavigationDock.vue";

const cleanups: Array<() => void> = [];
afterEach(() => {
  cleanups
    .splice(0)
    .reverse()
    .forEach((cleanup) => cleanup());
  document.body.replaceChildren();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

async function mountDock() {
  const mounted = vi.fn();
  const unmounted = vi.fn();
  const objects = defineComponent({
    setup() {
      onMounted(mounted);
      onUnmounted(unmounted);
      return () => h("div", { "data-objects-tree": "" }, [h("input", { "aria-label": "Find object" })]);
    },
  });
  const props = reactive({ section: "objects" as WorkbenchNavigationSection, connectionCount: 3, canNewQuery: false, maximized: false, settingsNotice: true, sqlLibrarySaveFeedbackId: 0 });
  const resize = usePanelResize();
  const events = {
    select: vi.fn((section: WorkbenchNavigationSection) => {
      props.section = section;
    }),
    resize: vi.fn(resize.startSidebarResize),
    collapse: vi.fn(),
    connection: vi.fn(),
    query: vi.fn(),
    settings: vi.fn(),
  };
  const host = document.createElement("div");
  document.body.append(host);
  const app = createApp({
    render: () =>
      h(
        WorkbenchNavigationDock,
        {
          ...props,
          width: resize.sidebarWidth.value,
          onSelect: events.select,
          onResize: events.resize,
          onCollapse: events.collapse,
          onNewConnection: events.connection,
          onNewQuery: events.query,
          onSettings: events.settings,
        },
        {
          objects: () => h(objects),
          history: () => h("div", { "data-history": "" }, "Query history"),
          sqlLibrary: () => h("div", { "data-sql-library": "" }, "Saved SQL"),
          sqlFile: () => h("div", { "data-sql-files": "" }, "SQL files"),
        },
      ),
  });
  app.use(
    createI18n({
      legacy: false,
      locale: "en",
      messages: {
        en: {
          workbenchLayout: { navigation: "Workspace navigation", resources: "Resources", resourceConnections: "{count} connections", objects: "Objects", history: "History" },
          sidebar: { collapse: "Collapse navigation" },
          toolbar: { newConnection: "New connection", newQuery: "New query" },
          sqlLibrary: { title: "Saved SQL" },
          sqlFileTree: { title: "SQL files" },
          history: { title: "History" },
          settings: { title: "Settings" },
        },
      },
    }),
  );
  app.mount(host);
  cleanups.push(() => {
    app.unmount();
    host.remove();
  });
  await nextTick();
  return { host, props, events, resize, mounted, unmounted };
}

function sectionButton(host: HTMLElement, section: string) {
  return host.querySelector<HTMLButtonElement>(`[data-navigation-section="${section}"]`)!;
}

function button(host: HTMLElement, label: string) {
  return [...host.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.getAttribute("aria-label") === label)!;
}

describe("Unified navigation dock", () => {
  it("retains the connection tree and its search state while opening secondary resource panels", async () => {
    const { host, props, mounted, unmounted } = await mountDock();
    const input = host.querySelector<HTMLInputElement>('input[aria-label="Find object"]')!;
    input.value = "orders";
    const treePanel = input.closest("section")!;
    expect(mounted).toHaveBeenCalledOnce();
    expect(host.querySelector("[data-history]")).toBeNull();

    sectionButton(host, "history").click();
    await nextTick();
    expect(treePanel.style.display).toBe("none");
    const history = host.querySelector("[data-history]")!;
    expect(history).not.toBeNull();
    expect(unmounted).not.toHaveBeenCalled();
    sectionButton(host, "sqlLibrary").click();
    await nextTick();
    expect(host.querySelector("[data-history]")).toBe(history);
    expect(history.closest("section")!.style.display).toBe("none");
    expect(host.querySelector("[data-sql-library]")).not.toBeNull();
    button(host, "SQL files").click();
    await nextTick();
    expect(props.section).toBe("sqlFile");
    expect(sectionButton(host, "sqlLibrary").getAttribute("aria-pressed")).toBe("true");
    expect(host.querySelector("[data-sql-files]")).not.toBeNull();
    sectionButton(host, "objects").click();
    await nextTick();
    expect(host.querySelector("input")).toBe(input);
    expect(input.value).toBe("orders");
    expect(treePanel.style.display).toBe("");
    expect(mounted).toHaveBeenCalledOnce();
  });

  it("initializes secondary panels only on first visit, then retains their state while hidden", async () => {
    const { host } = await mountDock();
    for (const selector of ["[data-history]", "[data-sql-library]", "[data-sql-files]"]) expect(host.querySelector(selector)).toBeNull();

    sectionButton(host, "history").click();
    await nextTick();
    const history = host.querySelector<HTMLElement>("[data-history]")!;
    history.dataset.pendingTransaction = "manual-session";
    expect(host.querySelector("[data-sql-library]")).toBeNull();

    sectionButton(host, "sqlLibrary").click();
    await nextTick();
    const library = host.querySelector<HTMLElement>("[data-sql-library]")!;
    library.dataset.search = "orders";
    expect(host.querySelector("[data-history]")).toBe(history);
    expect(history.closest("section")!.style.display).toBe("none");

    button(host, "SQL files").click();
    await nextTick();
    const files = host.querySelector<HTMLElement>("[data-sql-files]")!;
    files.dataset.expandedFolder = "queries";
    sectionButton(host, "objects").click();
    await nextTick();
    for (const panel of [history, library, files]) expect(panel.closest("section")!.style.display).toBe("none");

    sectionButton(host, "history").click();
    await nextTick();
    expect(host.querySelector("[data-history]")).toBe(history);
    expect(history.dataset.pendingTransaction).toBe("manual-session");
    expect(history.closest("section")!.style.display).toBe("");
    sectionButton(host, "sqlLibrary").click();
    await nextTick();
    expect(host.querySelector("[data-sql-library]")).toBe(library);
    expect(library.dataset.search).toBe("orders");
    button(host, "SQL files").click();
    await nextTick();
    expect(host.querySelector("[data-sql-files]")).toBe(files);
    expect(files.dataset.expandedFolder).toBe("queries");
  });

  it("keeps saved-SQL feedback anchored to the dock and clears the success flash", async () => {
    vi.useFakeTimers();
    const { host, props } = await mountDock();
    const trigger = host.querySelector<HTMLButtonElement>("[data-sql-library-trigger]")!;
    expect(trigger).toBe(sectionButton(host, "sqlLibrary"));
    expect(trigger.classList.contains("save-feedback")).toBe(false);
    props.sqlLibrarySaveFeedbackId = 1;
    await nextTick();
    expect(trigger.classList.contains("save-feedback")).toBe(true);
    vi.advanceTimersByTime(500);
    props.sqlLibrarySaveFeedbackId = 2;
    await nextTick();
    vi.advanceTimersByTime(849);
    await nextTick();
    expect(trigger.classList.contains("save-feedback")).toBe(true);
    vi.advanceTimersByTime(1);
    await nextTick();
    expect(trigger.classList.contains("save-feedback")).toBe(false);
  });

  it("suspends resizing while history is expanded and restores the user's normal dock width", async () => {
    const { host, props, resize } = await mountDock();
    const dock = host.querySelector<HTMLElement>("[data-app-sidebar]")!;
    resize.sidebarWidth.value = 320;
    await nextTick();
    expect(dock.style.width).toBe("320px");
    expect(host.querySelector(".panel-resize-handle")).not.toBeNull();
    props.maximized = true;
    await nextTick();
    expect(host.querySelector(".panel-resize-handle")).toBeNull();
    expect(resize.sidebarWidth.value).toBe(320);
    props.maximized = false;
    await nextTick();
    expect(dock.style.width).toBe("320px");
    expect(host.querySelector(".panel-resize-handle")).not.toBeNull();
  });

  it("moves focus and selection with arrow keys, Home, and End while leaving shortcuts untouched", async () => {
    const { host, events } = await mountDock();
    const objects = sectionButton(host, "objects");
    objects.focus();
    function key(value: string, options: KeyboardEventInit = {}) {
      const event = new KeyboardEvent("keydown", { key: value, bubbles: true, cancelable: true, ...options });
      document.activeElement!.dispatchEvent(event);
      return event;
    }
    expect(key("ArrowRight").defaultPrevented).toBe(true);
    await nextTick();
    expect(document.activeElement).toBe(sectionButton(host, "sqlLibrary"));
    expect(events.select).toHaveBeenLastCalledWith("sqlLibrary");
    key("End");
    expect(document.activeElement).toBe(sectionButton(host, "history"));
    key("ArrowRight");
    expect(document.activeElement).toBe(objects);
    key("ArrowLeft");
    expect(document.activeElement).toBe(sectionButton(host, "history"));
    key("Home");
    expect(document.activeElement).toBe(objects);
    const calls = events.select.mock.calls.length;
    expect(key("ArrowRight", { ctrlKey: true }).defaultPrevented).toBe(false);
    expect(key("ArrowRight", { metaKey: true }).defaultPrevented).toBe(false);
    expect(key("ArrowRight", { altKey: true }).defaultPrevented).toBe(false);
    expect(events.select).toHaveBeenCalledTimes(calls);
  });

  it("provides connection, query, settings, and collapse controls with actual query availability", async () => {
    const { host, props, events } = await mountDock();
    expect(host.textContent).toContain("3 connections");
    const connection = [...host.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.textContent?.trim() === "New connection")!;
    connection.click();
    expect(events.connection).toHaveBeenCalledOnce();
    const query = button(host, "New query");
    expect(query.disabled).toBe(true);
    query.click();
    expect(events.query).not.toHaveBeenCalled();
    props.canNewQuery = true;
    await nextTick();
    query.click();
    expect(events.query).toHaveBeenCalledOnce();
    button(host, "Settings").click();
    button(host, "Collapse navigation").click();
    expect(events.settings).toHaveBeenCalledOnce();
    expect(events.collapse).toHaveBeenCalledOnce();
  });

  it("owns the single sidebar resize surface and persists its width without resizing the nested tree", async () => {
    const { host, events, resize } = await mountDock();
    expect(host.querySelectorAll("[data-app-sidebar]")).toHaveLength(1);
    const dock = host.querySelector<HTMLElement>("[data-app-sidebar]")!;
    const tree = host.querySelector<HTMLElement>("[data-objects-tree]")!;
    const handle = host.querySelector<HTMLElement>(".panel-resize-handle")!;
    vi.spyOn(dock, "getBoundingClientRect").mockReturnValue({ width: 260 } as DOMRect);
    handle.setPointerCapture = vi.fn();
    handle.hasPointerCapture = vi.fn(() => false);
    handle.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, clientX: 260, pointerId: 1 }));
    document.dispatchEvent(new PointerEvent("pointermove", { clientX: 350, pointerId: 1 }));
    document.dispatchEvent(new PointerEvent("pointerup", { clientX: 350, pointerId: 1 }));
    await nextTick();

    expect(events.resize).toHaveBeenCalledOnce();
    expect(dock.style.width).toBe("350px");
    expect(tree.style.width).toBe("");
    expect(resize.sidebarWidth.value).toBe(350);
    expect(localStorage.getItem("dbx-sidebar-width")).toBe("350");
  });
});
