import { describe, expect, it } from "vitest";
import { availableWorkbenchAction, buildWorkbenchActions, type WorkbenchActionContext } from "../workbenchActions";
import { filterActionSearchItems } from "../actionSearch";

const query: WorkbenchActionContext = {
  canNewQuery: true, hasTabs: true, queryActive: true, tableActive: false,
  busy: false, cancelling: false, hasSql: true, hasExecutableSql: true,
  canFormat: true, canSave: true, hasResults: true, hasSidebar: true,
};
const actions = (changes: Partial<WorkbenchActionContext> = {}) => buildWorkbenchActions({ ...query, ...changes }, { executeSql: "Ctrl+Enter", formatSql: "", openSettings: "Mod+," }, (key) => key);
const get = (id: string, changes: Partial<WorkbenchActionContext> = {}) => actions(changes).find((action) => action.id === id)!;

describe("workbench action availability", () => {
  it("does not execute hidden query state while a settings page or another tab is active", () => {
    const list = actions({ queryActive: false, tableActive: false });
    for (const id of ["executeSql", "cancelQuery", "formatSql", "saveSql", "toggleResultsPane", "refreshTable"]) {
      expect(availableWorkbenchAction(id, list), id).toBeUndefined();
    }
    expect(availableWorkbenchAction("openSettings", list)).toBe("openSettings");
    expect(availableWorkbenchAction("quickOpen", list)).toBe("quickOpen");
  });

  it("offers cancellation while running, without offering duplicate execution or formatting", () => {
    const list = actions({ busy: true });
    expect(availableWorkbenchAction("cancelQuery", list)).toBe("cancelQuery");
    expect(availableWorkbenchAction("executeSql", list)).toBeUndefined();
    expect(availableWorkbenchAction("formatSql", list)).toBeUndefined();
    expect(get("cancelQuery", { busy: true, cancelling: true }).disabledReason).toBe("actionSearch.queryCancelling");
    expect(get("cancelQuery").disabledReason).toBe("actionSearch.runningQueryRequired");
  });

  it("allows saving an emptied existing SQL file but refuses to execute empty SQL", () => {
    const list = actions({ hasSql: false, hasExecutableSql: false, canSave: true });
    expect(availableWorkbenchAction("executeSql", list)).toBeUndefined();
    expect(availableWorkbenchAction("formatSql", list)).toBeUndefined();
    expect(availableWorkbenchAction("saveSql", list)).toBe("saveSql");
    expect(get("saveSql", { canSave: false }).disabledReason).toBe("actionSearch.sqlRequired");
  });

  it("does not offer unsupported formatting, missing results or unavailable sidebar", () => {
    expect(get("formatSql", { canFormat: false }).disabledReason).toBe("actionSearch.formatUnsupported");
    expect(get("toggleResultsPane", { hasResults: false }).disabled).toBe(true);
    expect(get("toggleSidebar", { hasSidebar: false }).disabled).toBe(true);
  });

  it("offers table refresh only for a table tab and respects its active query", () => {
    expect(get("refreshTable").disabledReason).toBe("actionSearch.tableRequired");
    expect(get("refreshTable", { queryActive: false, tableActive: true }).disabled).toBe(false);
    expect(get("refreshTable", { queryActive: false, tableActive: true, busy: true }).disabled).toBe(true);
  });

  it("keeps connection creation and settings usable before a query connection exists", () => {
    const list = actions({ canNewQuery: false, hasTabs: false, queryActive: false });
    expect(availableWorkbenchAction("newQuery", list)).toBeUndefined();
    expect(availableWorkbenchAction("recentTabs", list)).toBeUndefined();
    for (const id of ["newConnection", "openSettings", "openShortcuts", "openMcpSettings"]) {
      expect(availableWorkbenchAction(id, list), id).toBe(id);
    }
  });

  it("displays the actual custom shortcut and preserves an explicit unbinding", () => {
    expect(get("executeSql").shortcut).toBe("Ctrl+Enter");
    expect(get("formatSql").shortcut).toBe("");
    expect(get("toggleSqlLibrary").shortcut).toBeUndefined();
  });

  it("finds New Connection by its full English name when labels are in Chinese", () => {
    const list = buildWorkbenchActions(query, {}, (key) => key === "toolbar.newConnection" ? "新建连接" : "其他操作");
    expect(filterActionSearchItems(list, "new connection").map((action) => action.id)).toEqual(["newConnection"]);
  });

  it("rechecks a previously available action against live state and rejects unknown IDs", () => {
    expect(availableWorkbenchAction("cancelQuery", actions({ busy: true }))).toBe("cancelQuery");
    expect(availableWorkbenchAction("cancelQuery", actions())).toBeUndefined();
    expect(availableWorkbenchAction("unknown", [{ id: "unknown", label: "Unknown", group: "test" }])).toBeUndefined();
  });
});
