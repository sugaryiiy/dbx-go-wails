// @vitest-environment happy-dom

import { createApp, defineComponent, h, nextTick, type App, type Component } from "vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import SidebarLocateButton from "../SidebarLocateButton.vue";

const mountedApps: App[] = [];

async function mount(component: Component) {
  const container = document.createElement("div");
  document.body.append(container);
  const app = createApp(component);
  mountedApps.push(app);
  app.mount(container);
  await nextTick();
  return container;
}

afterEach(() => {
  for (const app of mountedApps.splice(0)) app.unmount();
  document.body.innerHTML = "";
});

describe("sidebar toolbar animations", () => {
  it("restarts the locate pulse on every click without changing the button size", async () => {
    const locate = vi.fn();
    const container = await mount(
      defineComponent({
        setup: () => () => h(SidebarLocateButton, { label: "Locate", onLocate: locate }),
      }),
    );
    const button = container.querySelector("[data-sidebar-locate-button]");
    if (!(button instanceof HTMLButtonElement)) throw new Error("Locate button was not rendered");

    expect(button.className).toContain("h-6 w-6");
    expect(button.querySelector("[data-sidebar-locate-pulse]")).toBeNull();
    button.click();
    await nextTick();
    const firstPulse = button.querySelector("[data-sidebar-locate-pulse]");
    expect(firstPulse).not.toBeNull();
    expect(locate).toHaveBeenCalledTimes(1);

    button.click();
    await nextTick();
    expect(button.querySelector("[data-sidebar-locate-pulse]")).not.toBe(firstPulse);
    expect(locate).toHaveBeenCalledTimes(2);
  });
});
