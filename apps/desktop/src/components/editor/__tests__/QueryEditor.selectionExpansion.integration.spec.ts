// @vitest-environment happy-dom

import { createApp, h, reactive } from "vue";
import { createPinia, setActivePinia } from "pinia";
import { createI18n } from "vue-i18n";
import { EditorSelection } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { afterEach, describe, expect, it, vi } from "vitest";
import QueryEditor from "../QueryEditor.vue";
import { useSettingsStore } from "@/stores/settingsStore";

const cleanups: Array<() => void> = [];
afterEach(() => {
  for (const cleanup of cleanups.splice(0).reverse()) cleanup();
});

async function mountEditor(doc: string, databaseType: "mysql" | "qdrant" = "mysql", customKeys = false) {
  const pinia = createPinia();
  setActivePinia(pinia);
  const settings = useSettingsStore();
  settings.editorSettings.completionTriggerMode = "manual";
  settings.editorSettings.shortcuts.extendSelection = customKeys ? "F7" : "Alt+ArrowUp";
  settings.editorSettings.shortcuts.shrinkSelection = customKeys ? "F8" : "Alt+ArrowDown";
  const content = reactive({ value: doc });
  const host = document.createElement("div");
  document.body.append(host);
  const app = createApp({
    render: () =>
      h(QueryEditor, {
        modelValue: content.value,
        databaseType,
        autoFocus: false,
        "onUpdate:modelValue": (value: string) => {
          content.value = value;
        },
      }),
  });
  app.use(pinia);
  app.use(createI18n({ legacy: false, locale: "en", messages: { en: {} }, missingWarn: false, fallbackWarn: false }));
  app.mount(host);
  cleanups.push(() => {
    app.unmount();
    host.remove();
  });
  await vi.waitFor(() => expect(host.querySelector(".cm-editor")).not.toBeNull(), { timeout: 5000 });
  return EditorView.findFromDOM(host.querySelector(".cm-editor") as HTMLElement)!;
}

function press(view: EditorView, key: string, altKey = true) {
  const event = new KeyboardEvent("keydown", { key, code: key, altKey, bubbles: true, cancelable: true });
  view.contentDOM.dispatchEvent(event);
  expect(event.defaultPrevented).toBe(true);
}

describe("mounted QueryEditor selection expansion", () => {
  it.each(["mysql", "qdrant"] as const)("restores the actual %s editor selection with Alt+Up then Alt+Down", async (databaseType) => {
    const doc = "select * from records where code = '001';\nselect 2;";
    const view = await mountEditor(doc, databaseType);
    const initial = EditorSelection.single(doc.indexOf("001") + 1);
    view.dispatch({ selection: initial });
    press(view, "ArrowUp");
    const first = view.state.selection;
    expect(view.state.sliceDoc(first.main.from, first.main.to)).toBe("001");
    press(view, "ArrowUp");
    expect(view.state.selection.eq(first)).toBe(false);
    press(view, "ArrowDown");
    expect(view.state.selection.eq(first, true)).toBe(true);
    press(view, "ArrowDown");
    expect(view.state.selection.eq(initial, true)).toBe(true);
    press(view, "ArrowDown");
    expect(view.state.selection.eq(initial, true)).toBe(true);
    expect(view.state.doc.toString()).toBe(doc);
  });

  it("consumes Alt+Down without history instead of falling through to CodeMirror move-line", async () => {
    const doc = "select first;\nselect second;\nselect third;";
    const view = await mountEditor(doc);
    view.dispatch({ selection: EditorSelection.single(3) });
    const initial = view.state.selection;
    press(view, "ArrowDown");
    expect(view.state.selection.eq(initial, true)).toBe(true);
    expect(view.state.doc.toString()).toBe(doc);

    press(view, "ArrowUp");
    view.dispatch({ selection: EditorSelection.single(doc.indexOf("second") + 2) });
    const manual = view.state.selection;
    press(view, "ArrowDown");
    expect(view.state.selection.eq(manual, true)).toBe(true);
    expect(view.state.doc.toString()).toBe(doc);
  });

  it("uses the configured non-Alt keymap bindings for both directions", async () => {
    const view = await mountEditor("select customer from records", "mysql", true);
    const initial = EditorSelection.single(10);
    view.dispatch({ selection: initial });
    press(view, "F7", false);
    expect(view.state.selection.main.empty).toBe(false);
    press(view, "F8", false);
    expect(view.state.selection.eq(initial, true)).toBe(true);
  });
});
