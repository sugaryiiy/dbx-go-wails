// @vitest-environment happy-dom

import { createApp, defineComponent, h, nextTick, type App } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const backend = vi.hoisted(() => ({
  go: false,
  documentFindDocuments: vi.fn(),
  documentCountDocuments: vi.fn(),
  cancelQuery: vi.fn(),
  ensureConnected: vi.fn(),
  documentInsertDocument: vi.fn(),
  documentUpdateDocument: vi.fn(),
  documentDeleteDocument: vi.fn(),
  documentSaveMeilisearchBatch: vi.fn(),
}));

vi.mock("@/lib/backend/goCapabilities", () => ({ isGoBackend: () => backend.go }));

const dataGrid = vi.hoisted(() => ({
  reload: undefined as (() => Promise<void>) | undefined,
  rows: [] as unknown[],
  columns: [] as string[],
}));

const settings = vi.hoisted(() => ({
  editorSettings: {
    pageSize: 2,
    tableOpenPageSize: 5,
    infiniteScroll: true,
    mongoViewMode: "table" as "document" | "table",
    columnWidthDensity: "standard" as "compact" | "standard" | "comfortable",
    dataGridRenderMode: "canvas" as "canvas" | "dom",
    tableFontFamily: "system-ui",
    tableFontSize: 12,
    numericColumnRightAlign: true,
    confirmDangerousSqlExecution: true,
    exportBatchSize: 2,
    exportRowLimitEnabled: false,
    exportRowLimit: 100_000,
  },
  updateEditorSettings: vi.fn(),
}));

vi.mock("vue-i18n", async (importOriginal) => ({
  ...(await importOriginal<typeof import("vue-i18n")>()),
  useI18n: () => ({ t: (key: string) => key }),
}));

vi.mock("@/lib/backend/api", () => ({
  getColumns: vi.fn(),
  documentFindDocuments: backend.documentFindDocuments,
  documentCountDocuments: backend.documentCountDocuments,
  dynamodbDescribeTable: vi.fn(),
  cancelQuery: backend.cancelQuery,
  documentInsertDocument: backend.documentInsertDocument,
  documentUpdateDocument: backend.documentUpdateDocument,
  documentDeleteDocument: backend.documentDeleteDocument,
  documentSaveMeilisearchBatch: backend.documentSaveMeilisearchBatch,
}));

vi.mock("@/stores/connectionStore", () => ({
  useConnectionStore: () => ({ ensureConnected: backend.ensureConnected }),
}));

vi.mock("@/stores/settingsStore", () => ({
  TABLE_FONT_SIZE_MIN: 8,
  TABLE_FONT_SIZE_MAX: 16,
  useSettingsStore: () => settings,
}));

vi.mock("@/components/grid/DataGrid.vue", () => {
  return {
    default: defineComponent({
      name: "DataGridStub",
      inheritAttrs: false,
      props: {
        result: { type: Object, required: true },
        customSaveHandler: { type: Object, required: false },
        pageOffset: { type: Number, required: false },
        pageLimit: { type: Number, required: false },
        pageSizePreference: { type: String, required: false },
      },
      setup(props, { attrs, expose }) {
        dataGrid.reload = attrs.onReload as typeof dataGrid.reload;
        expose({
          visibleColumnCount: 0,
          displayableColumnCount: 0,
          hiddenColumnCount: 0,
          orderedColumnLayoutOptions: [],
          filteredColumnLayoutOptions: () => [],
          toggleColumnVisibility: vi.fn(),
          showAllColumns: vi.fn(),
          invertColumnVisibility: vi.fn(),
          hasCustomColumnOrder: false,
          moveDisplayableColumn: vi.fn(),
          resetColumnOrder: vi.fn(),
          nullColumnsHidden: false,
          canToggleAllNullColumns: false,
          allNullColumnCount: 0,
          toggleAllNullColumns: vi.fn(),
          multiRowTranspose: false,
          setMultiRowTranspose: vi.fn(),
          resetInfiniteScrollState: vi.fn(),
        });
        return () => {
          const result = props.result as { rows: unknown[]; columns?: string[] };
          dataGrid.rows = result.rows;
          dataGrid.columns = result.columns ?? [];
          return h("div", { "data-testid": "data-grid" });
        };
      },
    }),
  };
});

vi.mock("@/components/redis/RedisJsonEditor.vue", async () => {
  const { defineComponent, h } = await import("vue");
  return {
    default: defineComponent({
      props: { modelValue: { type: String, required: true } },
      setup(props) {
        return () => h("div", {}, props.modelValue);
      },
    }),
  };
});

vi.mock("@/components/ui/popover", async () => {
  const { defineComponent, h } = await import("vue");
  const passthrough = (name: string) =>
    defineComponent({
      name,
      setup(_, { slots }) {
        return () => h("div", slots.default?.());
      },
    });
  return { Popover: passthrough("PopoverStub"), PopoverTrigger: passthrough("PopoverTriggerStub"), PopoverContent: passthrough("PopoverContentStub") };
});

vi.mock("@/components/ui/select", async () => {
  const { defineComponent, h } = await import("vue");
  const passthrough = (name: string) =>
    defineComponent({
      name,
      setup(_, { slots }) {
        return () => h("div", slots.default?.());
      },
    });
  return { Select: passthrough("SelectStub"), SelectContent: passthrough("SelectContentStub"), SelectItem: passthrough("SelectItemStub"), SelectTrigger: passthrough("SelectTriggerStub"), SelectValue: passthrough("SelectValueStub") };
});

import DocumentBrowser from "@/components/document/DocumentBrowser.vue";

let app: App<Element> | null = null;
let root: HTMLDivElement | null = null;

async function flushUi() {
  for (let index = 0; index < 4; index++) {
    await Promise.resolve();
    await nextTick();
  }
}

async function mountBrowser() {
  app = createApp(DocumentBrowser, {
    connectionId: "mongo-1",
    database: "test",
    collection: "docs",
    databaseType: "mongodb",
  });
  app.mount(root!);
  await flushUi();
}

function documentResult(start: number, count: number, total: number) {
  return {
    documents: Array.from({ length: count }, (_, index) => {
      const id = start + index;
      return { _id: String(id), name: `row_${id}` };
    }),
    total,
    total_is_exact: true,
  };
}

beforeEach(async () => {
  backend.go = false;
  vi.stubGlobal("localStorage", {
    getItem: () => null,
    setItem: () => undefined,
    removeItem: () => undefined,
  });
  backend.documentFindDocuments.mockReset();
  backend.documentCountDocuments.mockReset();
  backend.cancelQuery.mockReset();
  backend.ensureConnected.mockReset();
  backend.documentInsertDocument.mockReset();
  backend.documentUpdateDocument.mockReset();
  backend.documentDeleteDocument.mockReset();
  backend.documentSaveMeilisearchBatch.mockReset();
  backend.ensureConnected.mockResolvedValue(undefined);
  backend.documentCountDocuments.mockResolvedValue(0);
  dataGrid.reload = undefined;
  dataGrid.rows = [];
  dataGrid.columns = [];

  root = document.createElement("div");
  document.body.appendChild(root);
});

afterEach(() => {
  app?.unmount();
  root?.remove();
  app = null;
  root = null;
  vi.unstubAllGlobals();
});

describe("DocumentBrowser empty collection refresh entry (issue #8093)", () => {
  it("rejects unsafe compound ID order before any rekey insert or delete", async () => {
    backend.go = true;
    const id = JSON.parse('{"2":"a","1":"b"}');
    backend.documentFindDocuments.mockResolvedValue({ documents: [{ _id: id, name: "source" }], extended_documents: [{ _id: id, name: "source" }], total: 1, total_is_exact: true });
    await mountBrowser();
    const state = (app as any)._instance.setupState;
    state.selectDoc(0);
    state.startEdit();
    state.editJson = JSON.stringify({ _id: "new-id", name: "source" });
    await state.saveDoc();
    expect(state.error).toContain("integer-index keys");
    expect(backend.documentInsertDocument).not.toHaveBeenCalled();
    expect(backend.documentUpdateDocument).not.toHaveBeenCalled();
    expect(backend.documentDeleteDocument).not.toHaveBeenCalled();
  });

  it("Go JSON editing retains numeric BSON IDs and replaces without an unintended rekey", async () => {
    backend.go = true;
    const canonical = { _id: { $numberInt: "1" }, amount: { $numberLong: "42" }, ratio: { $numberDouble: "42.0" } };
    backend.documentFindDocuments.mockResolvedValue({ documents: [{ _id: 1, amount: 42, ratio: 42 }], extended_documents: [canonical], total: 1, total_is_exact: true });
    backend.documentUpdateDocument.mockResolvedValue(1);
    await mountBrowser();
    const state = (app as any)._instance.setupState;
    state.selectDoc(0);
    state.startEdit();
    expect(JSON.parse(state.editJson)).toEqual(canonical);
    await state.saveDoc();
    expect(backend.documentUpdateDocument).toHaveBeenCalledOnce();
    expect(backend.documentUpdateDocument.mock.calls[0]?.[3]).toBe('{"$numberInt":"1"}');
    expect(JSON.parse(backend.documentUpdateDocument.mock.calls[0]?.[4])).toEqual({ amount: canonical.amount, ratio: canonical.ratio });
    expect(backend.documentInsertDocument).not.toHaveBeenCalled();
    expect(backend.documentDeleteDocument).not.toHaveBeenCalled();
  });

  it("still reports non-empty columns for a collection that has never returned any document", async () => {
    backend.documentFindDocuments.mockResolvedValueOnce(documentResult(1, 0, 0));

    await mountBrowser();

    // `DataGrid.vue` only renders its toolbar (and thus the refresh button)
    // when `result.columns.length > 0`. A collection that was empty from the
    // very first load must not leave that array empty forever.
    expect(dataGrid.rows).toHaveLength(0);
    expect(dataGrid.columns.length).toBeGreaterThan(0);
  });

  it("keeps reporting non-empty columns across a second empty reload of the same collection", async () => {
    backend.documentFindDocuments.mockResolvedValueOnce(documentResult(1, 0, 0)).mockResolvedValueOnce(documentResult(1, 0, 0));

    await mountBrowser();
    expect(dataGrid.columns.length).toBeGreaterThan(0);

    await dataGrid.reload!();
    await flushUi();

    expect(dataGrid.rows).toHaveLength(0);
    expect(dataGrid.columns.length).toBeGreaterThan(0);
  });

  it("keeps the real columns (not the empty-collection fallback) after a collection that had data is emptied and reloaded", async () => {
    backend.documentFindDocuments.mockResolvedValueOnce(documentResult(1, 2, 2)).mockResolvedValueOnce(documentResult(1, 0, 0));

    await mountBrowser();
    expect(dataGrid.columns).toEqual(["_id", "name"]);

    await dataGrid.reload!();
    await flushUi();

    // Reproduces the reported "recovers only after closing and reopening the
    // tab" case: the columns learned while data existed must survive an
    // in-place reload that now returns zero rows.
    expect(dataGrid.rows).toHaveLength(0);
    expect(dataGrid.columns).toEqual(["_id", "name"]);
  });
});
