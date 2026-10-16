// @vitest-environment happy-dom
import { createApp, defineComponent, h, nextTick, reactive, type App, type Ref } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { QuickOpenItem } from "@/composables/useQuickOpen";

const mocks = vi.hoisted(() => ({
  state: {} as Record<string, Ref<any>>,
  setSearchActive: vi.fn(),
  refresh: vi.fn(),
  setContentMode: vi.fn(),
}));

vi.mock("@/composables/useQuickOpen", async () => {
  const { ref, computed } = await import("vue");
  Object.assign(mocks.state, {
    searchQuery: ref(""),
    filteredItems: ref([]),
    selectedIndex: ref(0),
    selectedItem: computed(() => mocks.state.filteredItems.value[mocks.state.selectedIndex.value]),
    contentMode: ref(true),
    contentGroups: ref([]),
    contentSearching: ref(false),
    contentScopeLabel: ref("Shop connection"),
    contentSearchError: ref([]),
    contentTruncated: ref(false),
  });
  return {
    useQuickOpen: () => ({
      ...mocks.state,
      contentSelectedItem: computed(() => mocks.state.contentGroups.value.flatMap((group: any) => group.matches)[mocks.state.selectedIndex.value]),
      setQuery: (query: string) => {
        mocks.state.searchQuery.value = query;
      },
      setContentMode: (value: boolean) => {
        mocks.setContentMode(value);
        mocks.state.contentMode.value = value;
      },
      setSearchActive: mocks.setSearchActive,
      selectNext: () => {
        mocks.state.selectedIndex.value++;
      },
      selectPrevious: () => {
        mocks.state.selectedIndex.value--;
      },
      loadExternalSqlFiles: vi.fn(),
      loadPluginWorkbenches: vi.fn(),
    }),
  };
});
vi.mock("@/lib/plugins/pluginCommandPalette", async () => {
  const { ref } = await import("vue");
  return { usePluginCommandPalette: () => ({ items: ref([]), refresh: mocks.refresh }) };
});
vi.mock("@/composables/useToast", () => ({ useToast: () => ({ toast: vi.fn() }) }));
vi.mock("@/stores/settingsStore", () => ({ useSettingsStore: () => ({ editorSettings: { shortcuts: {} } }) }));
vi.mock("@/lib/globalSearch/globalSearchSettings", () => ({ getGlobalSearchRoots: () => [], getGlobalSearchExtensions: () => ["sql"], saveGlobalSearchRoots: vi.fn(), saveGlobalSearchExtensions: vi.fn() }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string, params?: { connection: string }) => (params ? `${key}: ${params.connection}` : key) }) }));
vi.mock("@/components/ui/dialog", () => {
  const slot = defineComponent({
    setup:
      (_, { slots }) =>
      () =>
        h("div", slots.default?.()),
  });
  return { Dialog: slot, DialogContent: slot };
});
vi.mock("@/components/ui/input", () => ({
  Input: defineComponent({
    setup:
      (_, { attrs }) =>
      () =>
        h("input", attrs),
  }),
}));
vi.mock("@/components/plugins/PluginIcon.vue", () => ({ default: defineComponent({ setup: () => () => h("span") }) }));

import QuickOpenDialog from "@/components/quick-open/QuickOpenDialog.vue";

let app: App;
let host: HTMLElement;
let props: { open: boolean; initialContentMode: boolean };
let selected: ReturnType<typeof vi.fn>;

const match: QuickOpenItem = {
  id: "event-hit",
  type: "content_match",
  contentSource: "object",
  connectionId: "shop",
  objectType: "EVENT",
  objectName: "daily_cleanup",
  database: "orders",
  label: "daily_cleanup",
  searchText: "DELETE FROM audit",
  line: 5,
  column: 1,
  lineText: "DELETE FROM audit",
  highlightIndices: [0, 6],
};
const table: QuickOpenItem = { id: "audit-table", type: "table", connectionId: "shop", database: "orders", label: "audit", tableName: "audit", searchText: "audit" };

async function flush() {
  await nextTick();
  await nextTick();
}

beforeEach(async () => {
  vi.clearAllMocks();
  for (const [key, value] of Object.entries({ searchQuery: "", filteredItems: [], selectedIndex: 0, contentGroups: [], contentSearching: false, contentSearchError: [], contentTruncated: false })) {
    mocks.state[key].value = value;
  }
  props = reactive({ open: true, initialContentMode: true });
  selected = vi.fn();
  host = document.createElement("div");
  document.body.append(host);
  app = createApp({
    render: () =>
      h(QuickOpenDialog, {
        ...props,
        onSelect: selected,
        "onUpdate:open": (value: boolean) => {
          props.open = value;
        },
      }),
  });
  app.mount(host);
  await flush();
});

afterEach(() => {
  app.unmount();
  host.remove();
});

describe("QuickOpenDialog content sources", () => {
  it("shows connection scope, object kind and namespace while other sources are still searching", async () => {
    mocks.state.contentGroups.value = [{ key: "object:shop:orders:daily_cleanup", header: "daily_cleanup", description: "Shop connection / orders", type: "event", matches: [match] }];
    mocks.state.contentSearching.value = true;
    await flush();

    expect(host.querySelector("[data-content-search-scope]")?.textContent).toContain("Shop connection");
    expect(host.textContent).toContain("common.event");
    expect(host.textContent).toContain("Shop connection / orders");
    expect(host.textContent).toContain("DELETE");
    host.querySelector<HTMLElement>('[data-selected="true"]')!.click();
    expect(selected).toHaveBeenCalledWith(match);
  });

  it("shows source failures and truncation without reporting an empty successful search", async () => {
    mocks.state.searchQuery.value = "cleanup";
    mocks.state.contentSearchError.value = ["Shop connection: access denied"];
    mocks.state.contentTruncated.value = true;
    await flush();

    expect(host.querySelector("[data-content-search-error]")?.textContent).toContain("access denied");
    expect(host.querySelector("[data-content-search-truncated]")?.textContent).toContain("quickOpen.contentTruncated");
    expect(host.textContent).not.toContain("quickOpen.noResults");
  });

  it.each([
    { item: { ...match, contentGroupType: "procedure", objectType: "PROCEDURE" }, badge: "common.procedure" },
    { item: match, badge: "common.event" },
    { item: { ...match, contentSource: "saved_query", objectType: undefined, contentGroupType: "saved_query" }, badge: "quickOpen.savedQuery" },
    { item: { ...match, contentSource: "query_tab", objectType: undefined, contentGroupType: "query_tab" }, badge: "quickOpen.openQuery" },
  ] as Array<{ item: QuickOpenItem; badge: string }>)("shows a SQL body hit as $badge in ordinary search and opens it on click", async ({ item, badge }) => {
    props.initialContentMode = false;
    mocks.state.searchQuery.value = "audit";
    const bodyHit = { ...item, column: 13, matchText: "audit", highlightIndices: [12, 17] as [number, number] };
    mocks.state.filteredItems.value = [table, bodyHit];
    await flush();

    const snippet = host.querySelector<HTMLElement>("[data-content-search-match]")!;
    expect(host.textContent).toContain(item.label);
    expect(host.textContent).toContain(badge);
    expect(host.textContent).not.toContain("content_match");
    expect(snippet.textContent).toContain("DELETE FROM audit");
    expect(snippet.querySelector("[data-content-search-line]")?.textContent).toBe("5");
    expect(snippet.querySelector(".bg-yellow-200")?.textContent).toBe("audit");
    snippet.closest<HTMLElement>("[data-selected]")!.click();
    expect(selected).toHaveBeenCalledWith(bodyHit);
  });

  it("opens the selected SQL body hit with Enter in ordinary search", async () => {
    props.initialContentMode = false;
    mocks.state.filteredItems.value = [table, match];
    await flush();

    host.querySelector("input")!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true, cancelable: true }));
    const enter = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    host.querySelector("input")!.dispatchEvent(enter);

    expect(enter.defaultPrevented).toBe(true);
    expect(selected).toHaveBeenCalledWith(match);
    expect(props.open).toBe(false);
  });

  it("shows progress and incomplete source warnings in ordinary search without premature no results", async () => {
    props.initialContentMode = false;
    mocks.state.searchQuery.value = "audit";
    mocks.state.contentSearching.value = true;
    await flush();

    expect(host.querySelector("[data-content-search-loading]")?.textContent).toContain("quickOpen.searchingContent");
    expect(host.textContent).not.toContain("quickOpen.noResults");

    mocks.state.contentSearching.value = false;
    mocks.state.contentSearchError.value = ["Shop connection: access denied"];
    mocks.state.contentTruncated.value = true;
    await flush();

    expect(host.querySelector("[data-content-search-error]")?.textContent).toContain("access denied");
    expect(host.querySelector("[data-content-search-truncated]")?.textContent).toContain("quickOpen.contentTruncated");
    expect(host.textContent).not.toContain("quickOpen.noResults");
  });

  it("cancels searches when closed and resumes when reopened", async () => {
    expect(mocks.setSearchActive).toHaveBeenLastCalledWith(true);
    props.open = false;
    await flush();
    expect(mocks.setSearchActive).toHaveBeenLastCalledWith(false);
    props.open = true;
    await flush();
    expect(mocks.setSearchActive).toHaveBeenLastCalledWith(true);
  });
});
