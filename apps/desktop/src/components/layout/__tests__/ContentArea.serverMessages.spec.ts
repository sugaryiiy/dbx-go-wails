// @vitest-environment happy-dom
import { createApp, defineComponent, h, nextTick, reactive } from "vue";
import { createPinia, setActivePinia } from "pinia";
import { createI18n } from "vue-i18n";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { QueryResult, QueryTab } from "@/types/database";

vi.mock("@/components/editor/QueryEditor.vue", () => ({ default: { render: () => null } }));
vi.mock("@/components/grid/DataGrid.vue", () => ({ default: { render: () => h("div", { "data-test": "data-grid" }) } }));

import ContentArea from "../ContentArea.vue";
import { useConnectionStore } from "@/stores/connectionStore";
import { useQueryStore } from "@/stores/queryStore";

const cleanups: Array<() => void> = [];
afterEach(() => {
  cleanups.splice(0).forEach((cleanup) => cleanup());
  document.body.replaceChildren();
  window.localStorage?.clear();
});

async function mountResults(results: QueryResult[], activeIndex: number) {
  const pinia = createPinia();
  setActivePinia(pinia);
  const connection = { id: "sqlserver", name: "SQL Server", db_type: "sqlserver" as const, host: "localhost", port: 1433, username: "", password: "" };
  useConnectionStore().connections = [connection];
  const tab = reactive<QueryTab>({ id: "query", title: "Query", connectionId: connection.id, database: "app", mode: "query", sql: "EXEC demo", isExecuting: false, results, result: results[activeIndex], activeResultIndex: activeIndex });
  useQueryStore().tabs.push(tab);
  const state = reactive({ view: "result" as "result" | "messages" });
  const onToggleResultsPane = vi.fn();
  const host = document.createElement("div");
  document.body.appendChild(host);
  const app = createApp(
    defineComponent({
      setup: () => () =>
        h(ContentArea, {
          activeTab: tab,
          activeConnection: connection,
          activeOutputView: state.view,
          executableSql: "",
          formatSqlRequest: null,
          compressSqlRequest: null,
          selectedSql: "",
          cursorPos: 0,
          resultOnly: true,
          onToggleResultsPane,
          blockDangerousRedisCommands: false,
          "onUpdate:activeOutputView": (_tabId: string, view: string) => {
            state.view = view as typeof state.view;
          },
        }),
    }),
  );
  app.use(pinia);
  app.use(createI18n({ legacy: false, locale: "en", messages: { en: { tabs: { resultN: "Result {n}", allResults: "All results ({count})" } } }, missingWarn: false, fallbackWarn: false }));
  app.mount(host);
  cleanups.push(() => app.unmount());
  await nextTick();
  await nextTick();
  return { host, state, tab, onToggleResultsPane };
}

describe("SQL Server messages in the result surface", () => {
  const message: QueryResult = { columns: ["Message"], rows: [["before notice"]], affected_rows: 0, execution_time_ms: 1, server_message: true };
  const data: QueryResult = { columns: ["Message"], rows: [["real data"]], affected_rows: 0, execution_time_ms: 1 };

  it("shows only three result tabs and preserves both message blocks in Messages", async () => {
    const { host, state } = await mountResults([message, data, { ...message, rows: [["after notice"]] }, { ...data, rows: [] }, data], 1);
    expect([...host.querySelectorAll(".result-set-scroll button")].map((button) => button.textContent?.trim())).toEqual(["Result 1", "Result 2", "Result 3"]);
    state.view = "messages";
    await nextTick();
    expect(host.textContent).toContain("before notice");
    expect(host.textContent).toContain("after notice");
    expect(host.textContent).not.toContain("real data");
  });

  it("keeps one result header and its collapse action while execution status changes", async () => {
    const { host, tab, onToggleResultsPane } = await mountResults([data], 0);
    const header = host.querySelector('header[aria-label="workbench.results"]')!;
    expect(header).not.toBeNull();
    expect(header.querySelector('[role="status"]')?.getAttribute("aria-label")).toBe("workbench.resultReady");
    const collapse = header.querySelector<HTMLButtonElement>('button[aria-label="editor.hideResultsPane"]')!;
    collapse.click();
    expect(onToggleResultsPane).toHaveBeenCalledOnce();

    tab.isExecuting = true;
    await nextTick();
    expect(header.querySelector('[role="status"]')?.getAttribute("aria-label")).toBe("executionSummary.executing");
    tab.isExecuting = false;
    tab.result = { ...data, execution_error: true };
    await nextTick();
    expect(header.querySelector('[role="status"]')?.getAttribute("aria-label")).toBe("executionSummary.statuses.error");
    expect(host.querySelectorAll('header[aria-label="workbench.results"]')).toHaveLength(1);
    expect(host.querySelectorAll('button[aria-label="editor.hideResultsPane"]')).toHaveLength(1);
  });

  it.each([false, true])("redirects restored message output without a fake grid (mixed=%s)", async (mixed) => {
    const { host, state } = await mountResults(mixed ? [message, data] : [message], 0);
    expect(state.view).toBe("messages");
    expect(host.textContent).toContain("before notice");
    expect(host.querySelector('[data-test="data-grid"]')).toBeNull();
  });
});
