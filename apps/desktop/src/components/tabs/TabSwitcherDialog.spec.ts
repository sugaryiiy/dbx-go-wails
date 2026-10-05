// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from "vitest";
import { createApp, h, nextTick, reactive } from "vue";
import { createPinia } from "pinia";
import { createI18n } from "vue-i18n";
import TabSwitcherDialog from "./TabSwitcherDialog.vue";
import type { QueryTab } from "@/types/database";

const apps: ReturnType<typeof createApp>[] = [];

afterEach(() => {
  for (const app of apps.splice(0)) app.unmount();
  document.body.innerHTML = "";
});

describe("tab switcher opening mode", () => {
  it("shows recent-tab instructions for sticky mode and restores hold-to-switch instructions", async () => {
    const mode = reactive({ commitOnRelease: false, shortcutHint: "⌘ E" });
    const onSelect = vi.fn();
    const tab = { id: "query-1", title: "Report", mode: "query", connectionId: "", sql: "SELECT 1" } as QueryTab;
    const host = document.createElement("div");
    document.body.append(host);
    const app = createApp({
      setup: () => () => h(TabSwitcherDialog, { open: true, tabs: [tab], selectedIndex: 0, ...mode, onSelect }),
    });
    apps.push(app);
    app.use(createPinia());
    app.use(createI18n({
      legacy: false,
      locale: "en",
      messages: {
        en: {
          tabs: {
            switcherTitle: "Switch Tabs",
            switcherHint: "Keep pressing to select; release the modifier to switch",
            recentTabsTitle: "Recent Tabs",
            recentTabsHint: "↑↓ select, Return to confirm",
          },
          quickOpen: { close: "Close" },
        },
      },
    }));
    app.mount(host);
    await nextTick();

    const listbox = document.querySelector<HTMLElement>('[role="listbox"]')!;
    expect(listbox.getAttribute("aria-label")).toBe("Recent Tabs");
    expect(listbox.textContent).toContain("↑↓ select, Return to confirm");
    expect(listbox.textContent).toContain("⌘ E");
    expect(listbox.textContent).not.toContain("release the modifier");
    listbox.querySelector<HTMLElement>('[role="option"]')!.click();
    expect(onSelect).toHaveBeenCalledExactlyOnceWith("query-1");

    mode.commitOnRelease = true;
    mode.shortcutHint = "⌃ Tab";
    await nextTick();
    expect(listbox.getAttribute("aria-label")).toBe("Switch Tabs");
    expect(listbox.textContent).toContain("release the modifier to switch");
    expect(listbox.textContent).toContain("⌃ Tab");
    expect(listbox.textContent).not.toContain("Return to confirm");
  });
});
