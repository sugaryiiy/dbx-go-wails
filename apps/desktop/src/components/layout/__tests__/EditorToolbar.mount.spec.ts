// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive, ref } from "vue";
import { createPinia, setActivePinia } from "pinia";
import { createI18n } from "vue-i18n";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/components/ui/button", () => ({
  Button: {
    name: "ButtonStub",
    template: `<button><slot /></button>`,
  },
}));

vi.mock("@/components/ui/searchable-select", () => ({
  SearchableSelect: {
    name: "SearchableSelectStub",
    props: ["placeholder"],
    emits: ["update:modelValue"],
    template: `<button :data-context-select="placeholder" @click="$emit('update:modelValue', 'chosen')" />`,
  },
}));

vi.mock("@/components/ui/tooltip", () => ({
  Tooltip: {
    name: "TooltipStub",
    template: `<span><slot /></span>`,
  },
  TooltipTrigger: {
    name: "TooltipTriggerStub",
    template: `<span><slot /></span>`,
  },
  TooltipContent: {
    name: "TooltipContentStub",
    template: `<span><slot /></span>`,
  },
}));

vi.mock("@/components/ui/TruncatedTextTooltip.vue", () => ({
  default: {
    name: "TruncatedTextTooltipStub",
    template: `<span />`,
  },
}));

vi.mock("@/components/icons/DatabaseIcon.vue", () => ({
  default: {
    name: "DatabaseIconStub",
    template: `<span />`,
  },
}));

vi.mock("@/components/connection/ConnectionTreeSelect.vue", () => ({
  default: {
    name: "ConnectionTreeSelectStub",
    emits: ["update:modelValue"],
    template: `<button data-context-select="connection" @click="$emit('update:modelValue', 'conn-2')" />`,
  },
}));

vi.mock("@/components/common/ProductionContextBadge.vue", () => ({
  default: {
    name: "ProductionContextBadgeStub",
    template: `<span />`,
  },
}));

import EditorToolbar from "../EditorToolbar.vue";
import { useConnectionStore } from "@/stores/connectionStore";
import { useSettingsStore } from "@/stores/settingsStore";
import { resolveExecutableSql, type SqlExecutionSnapshot } from "@/lib/sql/sqlExecutionTarget";
import QueryEditor from "@/components/editor/QueryEditor.vue";
import { EditorView } from "@codemirror/view";

function createHost(): HTMLDivElement {
  const host = document.createElement("div");
  document.body.appendChild(host);
  return host;
}

async function openMoreMenu(host: HTMLElement): Promise<HTMLElement> {
  const trigger = host.querySelector<HTMLButtonElement>('button[aria-label="toolbar.moreActions"]');
  expect(trigger).not.toBeNull();
  trigger!.click();
  await vi.waitFor(() => expect(document.querySelector('[role="menu"]')).not.toBeNull());
  return document.querySelector<HTMLElement>('[role="menu"]')!;
}

function menuItem(menu: HTMLElement, label: string, role = "menuitem"): HTMLElement {
  const item = [...menu.querySelectorAll<HTMLElement>(`[role="${role}"]`)].find((element) => element.textContent?.trim() === label);
  expect(item).toBeDefined();
  return item!;
}

describe("EditorToolbar mount contract", () => {
  let pinia: ReturnType<typeof createPinia>;
  let i18n: ReturnType<typeof createI18n>;

  beforeEach(() => {
    document.body.innerHTML = "";
    pinia = createPinia();
    setActivePinia(pinia);
    i18n = createI18n({
      legacy: false,
      locale: "en",
      messages: { en: {} },
    });
  });

  it("forwards connection, database and schema changes through the extracted context row", async () => {
    const connectionStore = useConnectionStore();
    connectionStore.connections = [{ id: "conn-1", name: "postgres", db_type: "postgres" } as never];
    const host = createHost();
    const onChangeConnection = vi.fn();
    const onChangeDatabase = vi.fn();
    const onChangeSchema = vi.fn();
    const onSetDefaultDatabase = vi.fn();
    const app = createApp(EditorToolbar, {
      activeTab: { id: "tab-1", title: "SQL", connectionId: "conn-1", database: "app", schema: "public", sql: "SELECT 1", mode: "query", isExecuting: false },
      activeConnection: connectionStore.getConfig("conn-1"),
      executableSql: "SELECT 1",
      sqlKeywordCase: "preserve",
      onChangeConnection,
      onChangeDatabase,
      onChangeSchema,
      onSetDefaultDatabase,
    });
    app.use(pinia);
    app.use(i18n);
    app.mount(host);
    await nextTick();

    host.querySelector<HTMLButtonElement>('[data-context-select="connection"]')!.click();
    host.querySelector<HTMLButtonElement>('[data-context-select="editor.selectDatabase"]')!.click();
    host.querySelector<HTMLButtonElement>('[data-context-select="editor.selectSchema"]')!.click();
    host.querySelector<HTMLButtonElement>('button[aria-label="editor.setDefaultDatabase"]')!.click();
    host.querySelector<HTMLButtonElement>('button[aria-label="editor.clearDatabase"]')!.click();
    expect(onChangeConnection).toHaveBeenCalledExactlyOnceWith("conn-2");
    expect(onChangeDatabase.mock.calls).toEqual([["chosen"], [""]]);
    expect(onChangeSchema).toHaveBeenCalledExactlyOnceWith("chosen");
    expect(onSetDefaultDatabase).toHaveBeenCalledOnce();

    app.unmount();
    host.remove();
  });

  it("emits toolbarExecute instead of execute when the run button is clicked", async () => {
    const connectionStore = useConnectionStore();
    connectionStore.connections = [
      {
        id: "conn-1",
        name: "conn",
        db_type: "mysql",
        color: "",
      } as never,
    ];

    const host = createHost();
    const onToolbarExecute = vi.fn();
    const app = createApp(EditorToolbar, {
      activeTab: {
        id: "tab-1",
        title: "SQL",
        connectionId: "conn-1",
        database: "db",
        sql: "SELECT 1",
        mode: "query",
        isExecuting: false,
        isCancelling: false,
        isExplaining: false,
      },
      activeConnection: connectionStore.getConfig("conn-1"),
      executableSql: "SELECT 1",
      explainMode: "explain",
      blockDangerousRedisCommands: false,
      sqlKeywordCase: "preserve",
      databaseRequiredSignal: 0,
      autoCommit: true,
      txnSessionId: undefined,
      txnAutoRolledBack: false,
      txnPossiblyDirty: false,
      stickyProvenReadOnlyState: false,
      onToolbarExecute,
    });
    app.use(pinia);
    app.use(i18n);
    app.mount(host);
    await nextTick();

    const runButton = host.querySelector<HTMLButtonElement>('button[aria-label="toolbar.executeShortcut"]')!;
    runButton.dispatchEvent(new MouseEvent("click", { detail: 1, bubbles: true }));
    await nextTick();

    expect(onToolbarExecute).toHaveBeenCalledTimes(1);
    expect(onToolbarExecute.mock.calls[0]?.[0]).toBe("pointer");

    app.unmount();
    host.remove();
  });

  it("keeps the manually selected SQL from pointer-down through toolbar execution", async () => {
    const connectionStore = useConnectionStore();
    connectionStore.connections = [
      {
        id: "conn-1",
        name: "conn",
        db_type: "postgres",
        color: "",
      } as never,
    ];

    const fullSql = "SELECT first_value;\nSELECT selected_value;";
    const selectedSql = "SELECT selected_value;";
    const snapshot = {
      fullSql,
      selectedSql,
      cursorPos: fullSql.length,
      selectionFrom: fullSql.indexOf(selectedSql),
      selectionTo: fullSql.length,
    };
    let pendingSnapshot: typeof snapshot | undefined;
    let executedSql = "";
    const onExecutePointerDown = vi.fn(() => {
      pendingSnapshot = snapshot;
    });
    const onToolbarExecute = vi.fn((source: "pointer" | "keyboard") => {
      executedSql = source === "pointer" && pendingSnapshot ? resolveExecutableSql(pendingSnapshot.fullSql, pendingSnapshot.selectedSql, { mode: "current", cursorPos: pendingSnapshot.cursorPos }) : fullSql;
    });

    const host = createHost();
    const app = createApp(EditorToolbar, {
      activeTab: {
        id: "tab-1",
        title: "SQL",
        connectionId: "conn-1",
        database: "db",
        sql: fullSql,
        mode: "query",
        isExecuting: false,
        isCancelling: false,
        isExplaining: false,
      },
      activeConnection: connectionStore.getConfig("conn-1"),
      executableSql: selectedSql,
      explainMode: "explain",
      blockDangerousRedisCommands: false,
      sqlKeywordCase: "preserve",
      databaseRequiredSignal: 0,
      autoCommit: true,
      txnSessionId: undefined,
      txnAutoRolledBack: false,
      txnPossiblyDirty: false,
      stickyProvenReadOnlyState: false,
      onExecutePointerDown,
      onToolbarExecute,
    });
    app.use(pinia);
    app.use(i18n);
    app.mount(host);
    await nextTick();

    const runButton = host.querySelector<HTMLButtonElement>('button[aria-label="toolbar.executeShortcut"]')!;
    runButton?.dispatchEvent(new MouseEvent("mousedown", { button: 0, bubbles: true, cancelable: true }));
    runButton?.dispatchEvent(new MouseEvent("click", { detail: 1, bubbles: true }));
    await nextTick();

    expect(onExecutePointerDown).toHaveBeenCalledTimes(1);
    expect(onToolbarExecute).toHaveBeenCalledWith("pointer");
    expect(onExecutePointerDown.mock.invocationCallOrder[0]).toBeLessThan(onToolbarExecute.mock.invocationCallOrder[0]!);
    expect(executedSql).toBe(selectedSql);

    app.unmount();
    host.remove();
  });

  it("keeps the exact CodeMirror selection for regular and new-result toolbar execution", async () => {
    const connectionStore = useConnectionStore();
    const connection = {
      id: "conn-1",
      name: "conn",
      db_type: "postgres",
      color: "",
    } as never;
    connectionStore.connections = [connection];

    const fullSql = "SELECT first_value;\nSELECT selected_value;";
    const selectedSql = "SELECT selected_value;";
    const selectionFrom = fullSql.indexOf(selectedSql);
    const editorRef = ref<{ captureExecutionSnapshot?: () => SqlExecutionSnapshot | undefined } | null>(null);
    const state = reactive({ sql: fullSql });
    let pendingSnapshot: SqlExecutionSnapshot | undefined;
    let executedSql = "";
    let executedInNewResultSql = "";

    const host = createHost();
    const app = createApp({
      setup() {
        return () =>
          h("div", [
            h(QueryEditor, {
              ref: editorRef,
              modelValue: state.sql,
              tabId: "tab-1",
              connectionId: "conn-1",
              database: "db",
              databaseType: "postgres",
              dialect: "postgres",
              autoFocus: false,
              "onUpdate:modelValue": (value: string) => (state.sql = value),
            }),
            h(EditorToolbar, {
              activeTab: {
                id: "tab-1",
                title: "SQL",
                connectionId: "conn-1",
                database: "db",
                sql: fullSql,
                mode: "query",
                isExecuting: false,
                isCancelling: false,
                isExplaining: false,
              },
              activeConnection: connection,
              executableSql: fullSql,
              explainMode: "explain",
              blockDangerousRedisCommands: false,
              sqlKeywordCase: "preserve",
              databaseRequiredSignal: 0,
              autoCommit: true,
              txnSessionId: undefined,
              txnAutoRolledBack: false,
              txnPossiblyDirty: false,
              stickyProvenReadOnlyState: false,
              onExecutePointerDown: () => {
                pendingSnapshot = editorRef.value?.captureExecutionSnapshot?.();
              },
              onToolbarExecute: (source: "pointer" | "keyboard") => {
                executedSql = source === "pointer" && pendingSnapshot ? resolveExecutableSql(pendingSnapshot.fullSql, pendingSnapshot.selectedSql, { mode: "current", cursorPos: pendingSnapshot.cursorPos }) : fullSql;
              },
              onToolbarExecuteInNewResultTab: (source: "pointer" | "keyboard") => {
                executedInNewResultSql = source === "pointer" && pendingSnapshot ? resolveExecutableSql(pendingSnapshot.fullSql, pendingSnapshot.selectedSql, { mode: "current", cursorPos: pendingSnapshot.cursorPos }) : fullSql;
              },
            }),
          ]);
      },
    });
    app.use(pinia);
    app.use(i18n);
    app.mount(host);
    await vi.waitFor(() => expect(host.querySelector(".cm-editor")).not.toBeNull(), { timeout: 5000 });

    const view = EditorView.findFromDOM(host.querySelector(".cm-editor") as HTMLElement)!;
    view.dispatch({ selection: { anchor: selectionFrom, head: fullSql.length } });
    expect(view.state.sliceDoc(view.state.selection.main.from, view.state.selection.main.to)).toBe(selectedSql);

    const runButton = host.querySelector<HTMLElement>('button[aria-label="toolbar.executeShortcut"]');
    runButton?.dispatchEvent(new MouseEvent("mousedown", { button: 0, bubbles: true, cancelable: true }));
    runButton?.dispatchEvent(new MouseEvent("click", { detail: 1, bubbles: true }));
    await nextTick();

    expect(pendingSnapshot?.selectedSql).toBe(selectedSql);
    expect(executedSql).toBe(selectedSql);

    const newResultButton = host.querySelector<HTMLButtonElement>('button[aria-label="settings.shortcutExecuteSqlInNewResultTab"]');
    expect(newResultButton).not.toBeNull();
    expect(newResultButton?.disabled).toBe(false);
    newResultButton?.dispatchEvent(new MouseEvent("mousedown", { button: 0, bubbles: true, cancelable: true }));
    newResultButton?.dispatchEvent(new MouseEvent("click", { detail: 1, bubbles: true }));
    await nextTick();

    expect(pendingSnapshot?.selectedSql).toBe(selectedSql);
    expect(executedInNewResultSql).toBe(selectedSql);

    app.unmount();
    host.remove();
  });

  it("disables new-result execution when there is no executable SQL", async () => {
    const connectionStore = useConnectionStore();
    connectionStore.connections = [
      {
        id: "conn-empty",
        name: "empty",
        db_type: "mysql",
        color: "",
      } as never,
    ];

    const host = createHost();
    const onToolbarExecuteInNewResultTab = vi.fn();
    const app = createApp(EditorToolbar, {
      activeTab: {
        id: "tab-empty",
        title: "SQL",
        connectionId: "conn-empty",
        database: "db",
        sql: "",
        mode: "query",
        isExecuting: false,
        isCancelling: false,
        isExplaining: false,
      },
      activeConnection: connectionStore.getConfig("conn-empty"),
      executableSql: "",
      explainMode: "explain",
      blockDangerousRedisCommands: false,
      sqlKeywordCase: "preserve",
      databaseRequiredSignal: 0,
      autoCommit: true,
      txnSessionId: undefined,
      txnAutoRolledBack: false,
      txnPossiblyDirty: false,
      stickyProvenReadOnlyState: false,
      onToolbarExecuteInNewResultTab,
    });
    app.use(pinia);
    app.use(i18n);
    app.mount(host);
    await nextTick();

    const newResultButton = host.querySelector<HTMLButtonElement>('button[aria-label="settings.shortcutExecuteSqlInNewResultTab"]');
    expect(newResultButton).not.toBeNull();
    expect(newResultButton?.disabled).toBe(true);
    newResultButton?.click();
    expect(onToolbarExecuteInNewResultTab).not.toHaveBeenCalled();

    app.unmount();
    host.remove();
  });

  it("toggles word wrap from the More menu and updates the mounted editor", async () => {
    const connectionStore = useConnectionStore();
    const settingsStore = useSettingsStore();
    const connection = {
      id: "conn-1",
      name: "conn",
      db_type: "mysql",
      color: "",
    } as never;
    connectionStore.connections = [connection];
    settingsStore.editorSettings.wordWrap = false;

    const sql = "SELECT this_is_a_very_long_column_name FROM this_is_a_very_long_table_name";
    const forceWordWrap = ref(false);
    const host = createHost();
    const app = createApp({
      setup() {
        return () =>
          h("div", [
            h(QueryEditor, {
              modelValue: sql,
              tabId: "tab-1",
              connectionId: "conn-1",
              database: "db",
              databaseType: "mysql",
              dialect: "mysql",
              forceWordWrap: forceWordWrap.value,
              autoFocus: false,
            }),
            h(EditorToolbar, {
              activeTab: {
                id: "tab-1",
                title: "SQL",
                connectionId: "conn-1",
                database: "db",
                sql,
                mode: "query",
                isExecuting: false,
                isCancelling: false,
                isExplaining: false,
                forceWordWrap: forceWordWrap.value,
              },
              activeConnection: connection,
              executableSql: sql,
              explainMode: "explain",
              blockDangerousRedisCommands: false,
              sqlKeywordCase: "preserve",
              databaseRequiredSignal: 0,
              autoCommit: true,
              txnSessionId: undefined,
              txnAutoRolledBack: false,
              txnPossiblyDirty: false,
              stickyProvenReadOnlyState: false,
            }),
          ]);
      },
    });
    app.use(pinia);
    app.use(i18n);
    app.mount(host);
    await vi.waitFor(() => expect(host.querySelector(".cm-editor")).not.toBeNull(), { timeout: 5000 });

    const view = EditorView.findFromDOM(host.querySelector(".cm-editor") as HTMLElement)!;
    expect(view.contentDOM.classList.contains("cm-lineWrapping")).toBe(false);

    let menu = await openMoreMenu(host);
    let wordWrapItem = menuItem(menu, "settings.wordWrap", "menuitemcheckbox");
    expect(wordWrapItem.getAttribute("aria-checked")).toBe("false");
    wordWrapItem.click();

    await vi.waitFor(() => expect(settingsStore.editorSettings.wordWrap).toBe(true));
    await nextTick();
    await vi.waitFor(() => expect(view.contentDOM.classList.contains("cm-lineWrapping")).toBe(true));
    expect(wordWrapItem.getAttribute("aria-checked")).toBe("true");

    menu.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    await vi.waitFor(() => expect(document.querySelector('[role="menu"]')).toBeNull());
    menu = await openMoreMenu(host);
    wordWrapItem = menuItem(menu, "settings.wordWrap", "menuitemcheckbox");
    expect(wordWrapItem.getAttribute("aria-checked")).toBe("true");
    wordWrapItem.click();
    await vi.waitFor(() => expect(settingsStore.editorSettings.wordWrap).toBe(false));
    await vi.waitFor(() => expect(view.contentDOM.classList.contains("cm-lineWrapping")).toBe(false));
    expect(wordWrapItem.getAttribute("aria-checked")).toBe("false");

    forceWordWrap.value = true;
    await nextTick();
    await vi.waitFor(() => expect(view.contentDOM.classList.contains("cm-lineWrapping")).toBe(true));
    expect(wordWrapItem.getAttribute("aria-disabled")).toBe("true");
    expect(wordWrapItem.getAttribute("aria-checked")).toBe("true");
    wordWrapItem.click();
    expect(settingsStore.editorSettings.wordWrap).toBe(false);

    app.unmount();
    host.remove();
  });

  it("does not expose SQL formatting for Redis connections", async () => {
    const connectionStore = useConnectionStore();
    connectionStore.connections = [
      {
        id: "conn-redis",
        name: "redis",
        db_type: "redis",
        host: "localhost",
        port: 6379,
      } as never,
    ];

    const host = createHost();
    const app = createApp(EditorToolbar, {
      activeTab: {
        id: "tab-redis",
        title: "Redis",
        connectionId: "conn-redis",
        database: "0",
        sql: 'SET user:1 "hello world"',
        mode: "query",
        isExecuting: false,
        isCancelling: false,
        isExplaining: false,
      },
      activeConnection: connectionStore.getConfig("conn-redis"),
      executableSql: 'SET user:1 "hello world"',
      explainMode: "explain",
      blockDangerousRedisCommands: true,
      sqlKeywordCase: "preserve",
      databaseRequiredSignal: 0,
      autoCommit: true,
      txnSessionId: undefined,
      txnAutoRolledBack: false,
      txnPossiblyDirty: false,
      stickyProvenReadOnlyState: false,
    });
    app.use(pinia);
    app.use(i18n);
    app.mount(host);
    await nextTick();

    expect(host.textContent).not.toContain("toolbar.formatSql");

    app.unmount();
    host.remove();
  });

  it("mounts for a schema-aware connection whose watchEffect reads tier-gated selectors", async () => {
    // Regression: the tier-gated showSchemaSelector is read eagerly by the
    // schema-loading watchEffect, so the tier refs must be declared before any
    // computed that references them (a Postgres connection takes this branch;
    // MySQL never did, which is why the TDZ crash only reproduced live).
    const connectionStore = useConnectionStore();
    connectionStore.connections = [
      {
        id: "conn-pg",
        name: "pg",
        db_type: "postgres",
        host: "localhost",
        port: 5432,
        username: "root",
        password: "",
      } as never,
    ];

    const host = createHost();
    const errors: unknown[] = [];
    const app = createApp(EditorToolbar, {
      activeTab: {
        id: "tab-pg",
        title: "SQL",
        connectionId: "conn-pg",
        database: "app",
        sql: "SELECT 1",
        mode: "query",
        isExecuting: false,
        isCancelling: false,
        isExplaining: false,
      },
      activeConnection: connectionStore.getConfig("conn-pg"),
      executableSql: "SELECT 1",
      explainMode: "explain",
      blockDangerousRedisCommands: false,
      sqlKeywordCase: "preserve",
      databaseRequiredSignal: 0,
      autoCommit: true,
      txnSessionId: undefined,
      txnAutoRolledBack: false,
      txnPossiblyDirty: false,
      stickyProvenReadOnlyState: false,
    });
    app.config.errorHandler = (err) => {
      errors.push(err);
    };
    app.use(pinia);
    app.use(i18n);
    app.mount(host);
    await nextTick();
    await nextTick();

    expect(errors).toEqual([]);
    expect(host.querySelector(".app-editor-toolbar")).not.toBeNull();

    app.unmount();
    host.remove();
  });

  it("emits foldAll and unfoldAll from the More menu", async () => {
    const connectionStore = useConnectionStore();
    connectionStore.connections = [
      {
        id: "conn-1",
        name: "conn",
        db_type: "mysql",
        color: "",
      } as never,
    ];

    const host = createHost();
    const onFoldAll = vi.fn();
    const onUnfoldAll = vi.fn();
    const app = createApp(EditorToolbar, {
      activeTab: {
        id: "tab-1",
        title: "SQL",
        connectionId: "conn-1",
        database: "db",
        sql: "SELECT 1;\nSELECT 2;",
        mode: "query",
        isExecuting: false,
        isCancelling: false,
        isExplaining: false,
      },
      activeConnection: connectionStore.getConfig("conn-1"),
      executableSql: "SELECT 1;\nSELECT 2;",
      explainMode: "explain",
      blockDangerousRedisCommands: false,
      sqlKeywordCase: "preserve",
      databaseRequiredSignal: 0,
      autoCommit: true,
      txnSessionId: undefined,
      txnAutoRolledBack: false,
      txnPossiblyDirty: false,
      stickyProvenReadOnlyState: false,
      onFoldAll,
      onUnfoldAll,
    });
    app.use(pinia);
    app.use(i18n);
    app.mount(host);
    await nextTick();

    const foldMenu = await openMoreMenu(host);
    menuItem(foldMenu, "toolbar.foldAll").click();
    await nextTick();
    expect(onFoldAll).toHaveBeenCalledTimes(1);

    await vi.waitFor(() => expect(document.querySelector('[role="menu"]')).toBeNull());
    const unfoldMenu = await openMoreMenu(host);
    menuItem(unfoldMenu, "toolbar.unfoldAll").click();
    await nextTick();
    expect(onUnfoldAll).toHaveBeenCalledTimes(1);

    app.unmount();
    host.remove();
  });

  it("renders shortcut hints on button hover tooltips", async () => {
    const connectionStore = useConnectionStore();
    connectionStore.connections = [
      {
        id: "conn-shortcut",
        name: "test",
        db_type: "mysql",
        color: "",
      } as never,
    ];
    const settingsStore = useSettingsStore();
    settingsStore.updateEditorSettings({
      shortcuts: {
        ...settingsStore.editorSettings.shortcuts,
        explainSql: "Mod+E",
        formatSql: "Shift+Mod+F",
        saveSql: "Mod+S",
      },
    });

    const host = createHost();
    const app = createApp(EditorToolbar, {
      activeTab: {
        id: "tab-shortcut",
        title: "SQL",
        connectionId: "conn-shortcut",
        sql: "SELECT 1",
        mode: "query",
        isExecuting: false,
        isCancelling: false,
        isExplaining: false,
      },
      activeConnection: connectionStore.getConfig("conn-shortcut"),
      executableSql: "SELECT 1",
      explainMode: "explain",
      blockDangerousRedisCommands: false,
      sqlKeywordCase: "preserve",
      databaseRequiredSignal: 0,
      autoCommit: true,
      txnSessionId: undefined,
      txnAutoRolledBack: false,
      txnPossiblyDirty: false,
      stickyProvenReadOnlyState: false,
    });
    app.use(pinia);
    app.use(i18n);
    app.mount(host);
    await nextTick();
    await nextTick();

    const tooltips = [...host.querySelectorAll(".app-editor-toolbar span")].map((el) => el.textContent?.trim() ?? "");
    expect(tooltips.some((text) => text.includes("toolbar.explainPlan (") && text.includes("E)"))).toBe(true);
    expect(tooltips.some((text) => text.includes("toolbar.formatSql (") && text.includes("F)"))).toBe(true);
    expect(tooltips.some((text) => text.includes("toolbar.saveSql (") && text.includes("S)"))).toBe(true);

    app.unmount();
    host.remove();
  });
});
