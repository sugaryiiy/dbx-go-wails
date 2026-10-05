import type { ActionSearchItem } from "@/lib/app/actionSearch";
import type { ShortcutActionId, ShortcutSettings } from "@/lib/editor/shortcutRegistry";

export interface WorkbenchActionContext {
  canNewQuery: boolean;
  hasTabs: boolean;
  queryActive: boolean;
  tableActive: boolean;
  busy: boolean;
  cancelling: boolean;
  hasSql: boolean;
  hasExecutableSql: boolean;
  canFormat: boolean;
  canSave: boolean;
  hasResults: boolean;
  hasSidebar: boolean;
}

type Translate = (key: string) => string;
type ActionDefinition = {
  id: string;
  labelKey: string;
  group: "query" | "navigation" | "settings";
  shortcut?: ShortcutActionId;
  keywords: readonly string[];
  unavailable?: (context: WorkbenchActionContext) => string | undefined;
};

function queryUnavailable(context: WorkbenchActionContext): string | undefined {
  return !context.queryActive ? "queryRequired" : context.busy ? "queryBusy" : undefined;
}

const DEFINITIONS = [
  { id: "newConnection", labelKey: "toolbar.newConnection", group: "navigation", keywords: ["新建连接", "new connection", "connect"] },
  { id: "newQuery", labelKey: "toolbar.newQuery", group: "query", shortcut: "newQuery", keywords: ["新建查询", "new query", "console"], unavailable: (c) => !c.canNewQuery ? "connectionRequired" : undefined },
  { id: "executeSql", labelKey: "settings.shortcutExecuteSql", group: "query", shortcut: "executeSql", keywords: ["执行查询", "execute", "run sql"], unavailable: (c) => queryUnavailable(c) ?? (!c.hasExecutableSql ? "sqlRequired" : undefined) },
  { id: "cancelQuery", labelKey: "settings.shortcutCancelQuery", group: "query", shortcut: "cancelQuery", keywords: ["停止取消查询", "stop", "cancel"], unavailable: (c) => !c.queryActive ? "queryRequired" : c.cancelling ? "queryCancelling" : !c.busy ? "runningQueryRequired" : undefined },
  { id: "formatSql", labelKey: "settings.shortcutFormatSql", group: "query", shortcut: "formatSql", keywords: ["格式化", "format", "reformat"], unavailable: (c) => queryUnavailable(c) ?? (!c.canFormat ? "formatUnsupported" : !c.hasSql ? "sqlRequired" : undefined) },
  { id: "saveSql", labelKey: "settings.shortcutSaveSql", group: "query", shortcut: "saveSql", keywords: ["保存查询", "save sql"], unavailable: (c) => !c.queryActive ? "queryRequired" : !c.canSave ? "sqlRequired" : undefined },
  { id: "refreshTable", labelKey: "actionSearch.refreshTable", group: "query", shortcut: "refreshData", keywords: ["刷新表数据", "refresh table", "reload"], unavailable: (c) => !c.tableActive ? "tableRequired" : c.busy ? "queryBusy" : undefined },
  { id: "toggleResultsPane", labelKey: "settings.shortcutToggleResultsPane", group: "query", shortcut: "toggleResultsPane", keywords: ["查询结果面板", "results pane"], unavailable: (c) => !c.queryActive || !c.hasResults ? "resultsRequired" : undefined },
  { id: "quickOpen", labelKey: "settings.shortcutQuickOpen", group: "navigation", shortcut: "quickOpen", keywords: ["搜索数据库对象", "find object", "table", "navigate"] },
  { id: "globalSearch", labelKey: "settings.shortcutGlobalSearch", group: "navigation", shortcut: "globalSearch", keywords: ["文件内容搜索", "find in files", "search text"] },
  { id: "recentTabs", labelKey: "settings.shortcutRecentTabs", group: "navigation", shortcut: "recentTabs", keywords: ["最近页签", "recent tabs", "recent files"], unavailable: (c) => !c.hasTabs ? "tabRequired" : undefined },
  { id: "toggleSidebar", labelKey: "settings.shortcutToggleSidebar", group: "navigation", shortcut: "toggleSidebar", keywords: ["数据库侧栏", "database explorer", "sidebar"], unavailable: (c) => !c.hasSidebar ? "mainWindowRequired" : undefined },
  { id: "toggleQueryHistory", labelKey: "settings.shortcutToggleQueryHistory", group: "navigation", shortcut: "toggleQueryHistory", keywords: ["查询历史", "query history"] },
  { id: "toggleSqlLibrary", labelKey: "savedSql.title", group: "navigation", keywords: ["SQL库收藏", "sql library", "saved queries", "favorites"] },
  { id: "openSettings", labelKey: "settings.title", group: "settings", shortcut: "openSettings", keywords: ["设置", "settings", "preferences"] },
  { id: "openFontSettings", labelKey: "settings.fontSettingsTitle", group: "settings", keywords: ["显示字体", "字体设置", "font", "typography", "display font"] },
  { id: "openShortcuts", labelKey: "settings.shortcutsTab", group: "settings", keywords: ["快捷键键位", "keymap", "shortcuts", "datagrip"] },
  { id: "openMcpSettings", labelKey: "settings.openMcpSettings", group: "settings", keywords: ["MCP设置", "mcp settings", "tools"] },
] as const satisfies readonly ActionDefinition[];

export type WorkbenchActionId = (typeof DEFINITIONS)[number]["id"];

export function buildWorkbenchActions(context: WorkbenchActionContext, shortcuts: Partial<ShortcutSettings>, t: Translate): ActionSearchItem[] {
  return DEFINITIONS.map((definition: ActionDefinition) => {
    const reason = definition.unavailable?.(context);
    return {
      id: definition.id,
      label: t(definition.labelKey),
      group: t(`actionSearch.groups.${definition.group}`),
      shortcut: definition.shortcut ? shortcuts[definition.shortcut] : undefined,
      keywords: [...definition.keywords],
      disabled: !!reason,
      disabledReason: reason ? t(`actionSearch.${reason}`) : undefined,
    };
  });
}

/** Re-read availability after the dialog has closed: queries can finish or
 * the active surface can change while its closing transition is in flight. */
export function availableWorkbenchAction(id: string, actions: readonly ActionSearchItem[]): WorkbenchActionId | undefined {
  return actions.some((action) => action.id === id && !action.disabled) && DEFINITIONS.some((action) => action.id === id) ? id as WorkbenchActionId : undefined;
}
