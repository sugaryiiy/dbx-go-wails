<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import {
  Play,
  SquarePlay,
  CirclePlay,
  Loader2,
  Square,
  Check,
  AlignLeft,
  GitBranch,
  Save,
  FolderOpen,
  X,
  Shield,
  Download,
  RotateCcw,
  AlertTriangle,
  ClipboardPaste,
  Minimize2,
  FoldVertical,
  UnfoldVertical,
  SpellCheck2,
  MoreHorizontal,
  BetweenVerticalStart,
  Eye,
  WrapText,
  RefreshCw,
} from "@lucide/vue";
import { supportsInsertValueHints } from "@/lib/editor/codemirrorInsertValueHints";
import QueryWorkspaceContext from "./QueryWorkspaceContext.vue";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuCheckboxItem, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Tooltip, TooltipTrigger, TooltipContent } from "@/components/ui/tooltip";
import { useQueryStore } from "@/stores/queryStore";
import { useSettingsStore } from "@/stores/settingsStore";
import { supportsSqlInListPaste, supportsTransaction as supportsTransactionFeature } from "@/lib/database/databaseCapabilities";
import { supportsQueryExecution } from "@/lib/database/databaseFeatureSupport";
import { productionContextForDatabase } from "@/lib/database/productionSafety";
import { formatShortcutDisplay, formatShortcutTooltip } from "@/lib/editor/shortcutDisplay";
import { resolveNextEditorToolbarTier, type EditorToolbarTier } from "@/lib/tabs/editorToolbarLayout";
import { canSaveSqlTab } from "@/lib/tabs/sqlTabSaveTarget";
import { looksLikeDmlStatement } from "@/lib/sql/dmlChangePreview";
import { canFormatSqlForDatabaseType } from "@/lib/sql/sqlFormatter";
import type { QueryTab, ConnectionConfig } from "@/types/database";

const props = defineProps<{
  activeTab: QueryTab;
  activeConnection?: ConnectionConfig;
  executableSql: string;
  /** 来自 QueryEditor 的实时“当前语句是否为可预览 DML”信号；未提供时退回可编辑文档启发式。 */
  canPreviewChanges?: boolean;
  explainMode?: string;
  blockDangerousRedisCommands?: boolean;
  sqlKeywordCase: "preserve" | "upper" | "lower";
  databaseRequiredSignal?: number;
  autoCommit?: boolean;
  txnSessionId?: string;
  txnAutoRolledBack?: boolean;
  /** Oracle-only: whether the current manual Oracle session executed a statement
   *  DBX cannot prove read-only. Commit/Rollback are hidden while false. */
  txnPossiblyDirty?: boolean;
  /** Oracle manual mode derived from the resolved database type (not raw
   *  db_type, which can be the agent transport). */
  stickyProvenReadOnlyState?: boolean;
  /** Auto-commit tabs (`Tx:A`): the tab's own connection holds a transaction
   *  the user opened explicitly (`BEGIN` / `START TRANSACTION`) that DBX kept
   *  open. Commit/Rollback act on that transaction. */
  autoCommitOpenTransaction?: boolean;
  /** Auto-commit tab: the backend rolled back an explicit transaction this tab
   *  left open (the tab did not opt into keeping them). */
  autoCommitTxnRolledBack?: boolean;
  /** Auto-commit tab: the backend rolled back an implicitly opened transaction
   *  (`SET autocommit = 0`), reported once per connection. */
  autoCommitSessionTxnRolledBack?: boolean;
}>();

const emit = defineEmits<{
  toolbarExecute: [source: "pointer" | "keyboard"];
  toolbarExecuteInNewResultTab: [source: "pointer" | "keyboard"];
  executePointerDown: [];
  cancel: [];
  previewChanges: [];
  explain: [];
  "update:explainMode": [mode: "explain" | "autotrace"];
  formatSql: [];
  compressSql: [];
  foldAll: [];
  unfoldAll: [];
  toggleSqlKeywordCase: [];
  saveSql: [tabId: string];
  openSql: [];
  importResultArchive: [];
  pasteSqlInCondition: [];
  multiExecute: [];
  changeConnection: [connectionId: string];
  changeCatalog: [catalog: string | undefined, database: string];
  changeDatabase: [database: string];
  changeSchema: [schema: string | undefined];
  setDefaultDatabase: [];
  clearDefaultDatabase: [];
  "update:blockDangerousRedisCommands": [value: boolean];
  "update:autoCommit": [value: boolean];
  commit: [];
  rollback: [];
  dismissTxnRolledBack: [];
  dismissAutoCommitTxnRolledBack: [];
  dismissAutoCommitSessionTxnRolledBack: [];
}>();

const { t } = useI18n();
const queryStore = useQueryStore();
const settingsStore = useSettingsStore();

const toolbarRootRef = ref<HTMLElement | null>(null);
const toolbarActionsRef = ref<HTMLElement | null>(null);
// Keep secondary editor actions in More even on a wide pane. The remaining
// tiers use the measured action-row width; context wraps independently above.
const toolbarTier = ref<EditorToolbarTier>(1);
// Available width when the current tier was condensed into; anchors the
// step-down hysteresis so a static narrow layout cannot oscillate.
const condensedAtWidth = ref(0);
const expandedTierRequiredWidths: Partial<Record<EditorToolbarTier, number>> = {};
let toolbarResizeObserver: ResizeObserver | undefined;
let toolbarMeasureRaf = 0;

function measureToolbarTier() {
  const element = toolbarActionsRef.value;
  const root = toolbarRootRef.value;
  if (!element || !root) {
    return;
  }
  const availableWidth = root.clientWidth;
  // Use the stable full-row coordinate space, including the right controls.
  // Tier-dependent helpers must not appear to grow the pane when they hide.
  const contentWidth = element.scrollWidth + availableWidth - element.clientWidth;
  const next = Math.max(
    1,
    resolveNextEditorToolbarTier({
      tier: toolbarTier.value,
      availableWidth,
      contentWidth,
      condensedAtWidth: condensedAtWidth.value,
      expandedTierRequiredWidths,
    }),
  ) as EditorToolbarTier;
  if (next !== toolbarTier.value) {
    if (next > toolbarTier.value) {
      const currentTier = toolbarTier.value;
      expandedTierRequiredWidths[currentTier] = Math.max(expandedTierRequiredWidths[currentTier] ?? 0, contentWidth);
      condensedAtWidth.value = availableWidth;
    }
    toolbarTier.value = next;
  }
}

function scheduleToolbarMeasurement() {
  if (toolbarMeasureRaf) {
    return;
  }
  toolbarMeasureRaf = requestAnimationFrame(() => {
    toolbarMeasureRaf = 0;
    measureToolbarTier();
  });
}

// Hiding or restoring controls changes the row content without resizing the
// toolbar box, so every tier change re-measures until the row settles.
watch(toolbarTier, () => {
  void nextTick(scheduleToolbarMeasurement);
});

// The visible control set also changes with connection type and transaction
// state; re-measure when those do.
watch(
  () => [props.activeConnection?.id, props.activeConnection?.db_type, props.txnSessionId, props.activeTab.isExecuting, props.activeTab.isExplaining] as const,
  () => {
    void nextTick(scheduleToolbarMeasurement);
  },
);

watch(
  toolbarRootRef,
  (element) => {
    toolbarResizeObserver?.disconnect();
    toolbarResizeObserver = undefined;
    if (element && typeof ResizeObserver !== "undefined") {
      toolbarResizeObserver = new ResizeObserver(scheduleToolbarMeasurement);
      toolbarResizeObserver.observe(element);
    }
    void nextTick(scheduleToolbarMeasurement);
  },
  { flush: "post" },
);

onUnmounted(() => {
  if (toolbarMeasureRaf) {
    cancelAnimationFrame(toolbarMeasureRaf);
    toolbarMeasureRaf = 0;
  }
  toolbarResizeObserver?.disconnect();
  toolbarResizeObserver = undefined;
});

const activeProductionContext = computed(() => productionContextForDatabase(props.activeConnection, props.activeTab.database));
const supportsExplain = computed(() => {
  const dbType = props.activeConnection?.db_type;
  return (
    dbType !== "redis" &&
    dbType !== "mongodb" &&
    dbType !== "elasticsearch" &&
    dbType !== "easysearch" &&
    dbType !== "meilisearch" &&
    dbType !== "solr" &&
    dbType !== "qdrant" &&
    dbType !== "milvus" &&
    dbType !== "weaviate" &&
    dbType !== "chromadb" &&
    dbType !== "etcd" &&
    dbType !== "zookeeper" &&
    dbType !== "consul" &&
    dbType !== "mq" &&
    dbType !== "nacos" &&
    dbType !== "victoriametrics" &&
    dbType !== "salesforce"
  );
});
const supportsExPaste = computed(() => supportsSqlInListPaste(props.activeConnection?.db_type));
const supportsTransaction = computed(() => supportsTransactionFeature(props.activeConnection?.db_type));
const saveTooltip = computed(() => {
  let label = t("toolbar.saveSql");
  if (props.activeTab.objectSource) label = t("objects.saveSource");
  else if (props.activeTab.externalSqlPath) label = t("toolbar.saveSqlFile");
  return formatShortcutTooltip(label, settingsStore.editorSettings.shortcuts.saveSql);
});
const explainPlanTooltip = computed(() => formatShortcutTooltip(t("toolbar.explainPlan"), settingsStore.editorSettings.shortcuts.explainSql));
const formatSqlTooltip = computed(() => formatShortcutTooltip(t("toolbar.formatSql"), settingsStore.editorSettings.shortcuts.formatSql));
const exPasteSqlInConditionTooltip = computed(() => formatShortcutTooltip(t("toolbar.exPasteSqlInCondition"), settingsStore.editorSettings.shortcuts.exPasteSqlInCondition));
const isObjectSourceTab = computed(() => !!props.activeTab.objectSource || !!props.activeTab.sourceLoad);
const objectSourceRefreshing = computed(() => !!props.activeTab.sourceLoad && !props.activeTab.sourceLoad.error);

function refreshObjectSource() {
  if (!isObjectSourceTab.value) return;
  if (queryStore.isTabDirty(props.activeTab) && !window.confirm(t("objects.refreshDiscardConfirm"))) return;
  queryStore.refreshObjectSourceTab(props.activeTab.id);
}
const executeShortcutDisplay = computed(() => formatShortcutDisplay(settingsStore.editorSettings.shortcuts.executeSql));
const executeShortcutTooltip = computed(() => t("toolbar.executeShortcut", { shortcut: executeShortcutDisplay.value }));
const stopShortcutTooltip = computed(() => formatShortcutTooltip(t("toolbar.stopQuery"), settingsStore.editorSettings.shortcuts.cancelQuery));
const executeInNewResultTabTooltip = computed(() => formatShortcutTooltip(t("settings.shortcutExecuteSqlInNewResultTab"), settingsStore.editorSettings.shortcuts.executeSqlInNewResultTab));
// executableSql 在无选区时可能是整篇文档；只要有 DML 语句出现就显示预览按钮，
// 具体"当前语句"由编辑器（QueryEditor）按执行模式解析。
const DML_KEYWORD_RE = /(^|\s)(update|insert|delete)\s/i;
const canPreviewDml = computed(() => looksLikeDmlStatement(props.executableSql) || DML_KEYWORD_RE.test(props.executableSql));
const previewButtonVisible = computed(() => props.canPreviewChanges ?? canPreviewDml.value);
// DM calls it autotrace, Postgres EXPLAIN ANALYZE, SQL Server the actual execution
// plan (SET STATISTICS XML); all three execute the statement.
const supportsExplainAnalyze = computed(() => {
  const dbType = props.activeConnection?.db_type;
  return dbType === "dameng" || dbType === "postgres" || dbType === "sqlserver";
});
const explainAnalyzeTooltip = computed(() => {
  const dbType = props.activeConnection?.db_type;
  if (dbType === "postgres") return t("toolbar.explainAnalyze");
  if (dbType === "sqlserver") return t("toolbar.actualPlan");
  return t("toolbar.autotrace");
});
const canSaveSql = computed(() => canSaveSqlTab(props.activeTab));
const keywordCaseIsLower = computed(() => props.sqlKeywordCase === "lower");
const keywordCaseToggleTooltip = computed(() => (keywordCaseIsLower.value ? t("toolbar.keywordCaseUpper") : t("toolbar.keywordCaseLower")));
const wordWrapEnabled = computed(() => props.activeTab.forceWordWrap === true || settingsStore.editorSettings.wordWrap);
function toggleWordWrap() {
  if (props.activeTab.forceWordWrap) return;
  settingsStore.updateEditorSettings({ wordWrap: !wordWrapEnabled.value });
}
const sqlSemanticDiagnosticsEnabled = computed(() => settingsStore.editorSettings.sqlSemanticDiagnosticsEnabled);
const sqlSemanticDiagnosticsToggleTooltip = computed(() => (sqlSemanticDiagnosticsEnabled.value ? t("toolbar.sqlSemanticDiagnosticsToggleOn") : t("toolbar.sqlSemanticDiagnosticsToggleOff")));
const supportsSqlSemanticDiagnosticsToggle = computed(() => {
  const dbType = props.activeConnection?.db_type;
  return dbType !== "redis" && dbType !== "victoriametrics" && dbType !== "salesforce";
});
function toggleSqlSemanticDiagnostics() {
  settingsStore.updateEditorSettings({
    sqlSemanticDiagnosticsMode: sqlSemanticDiagnosticsEnabled.value ? "disabled" : "enabled",
  });
}
const insertValueHintsEnabled = computed(() => settingsStore.editorSettings.showInsertValueHints);
const insertValueHintsToggleTooltip = computed(() => (insertValueHintsEnabled.value ? t("toolbar.insertValueHintsToggleOn") : t("toolbar.insertValueHintsToggleOff")));
const supportsInsertValueHintsToggle = computed(() => supportsInsertValueHints(props.activeConnection?.db_type));
function toggleInsertValueHints() {
  settingsStore.updateEditorSettings({ showInsertValueHints: !insertValueHintsEnabled.value });
}
const isTransactionActive = computed(() => !!props.txnSessionId);
/** Auto-commit tab whose connection holds a transaction the user opened with
 *  `BEGIN` / `START TRANSACTION` and that DBX kept open (`Tx:A`). */
const hasOpenAutoCommitTransaction = computed(() => props.autoCommitOpenTransaction === true);
const isManualTransactionMode = computed(() => props.autoCommit === false || isTransactionActive.value);
const transactionModeBadge = computed(() => (isManualTransactionMode.value ? "M" : "A"));
// Sticky proven-read-only dialects (Oracle/OceanBase-Oracle/MySQL/PostgreSQL)
// hide Commit/Rollback while the session is clean (no unproven statement
// executed). Every other database keeps the existing rule. An auto-commit tab
// that kept the user's explicit transaction always offers both actions: that
// transaction exists only because the user asked for it.
const showTxnActions = computed(() => {
  if (hasOpenAutoCommitTransaction.value) return true;
  if (props.stickyProvenReadOnlyState) return isTransactionActive.value && props.txnPossiblyDirty === true;
  return isTransactionActive.value;
});
const transactionTooltip = computed(() => {
  if (hasOpenAutoCommitTransaction.value) return t("settings.keepExplicitTransactionInAutoCommitDescription");
  const isAgent = (props.activeConnection?.db_type as string) === "agent";
  const isManual = isManualTransactionMode.value;
  if (isAgent && isManual) return t("toolbar.manualTransactionAgent");
  if (isAgent) return t("toolbar.autoCommitAgent");
  return isManual ? t("toolbar.manualTransaction") : t("toolbar.autoCommit");
});
const executeButtonClass = computed(() => {
  if (props.activeTab.isExecuting) return "";
  return activeProductionContext.value.active ? "bg-red-700 text-white hover:bg-red-800 hover:text-white" : "bg-emerald-700 text-white hover:bg-emerald-800 hover:text-white";
});

const canMultiExecute = computed(() => {
  if (!supportsQueryExecution(props.activeConnection?.db_type)) return false;
  if (props.activeTab.isExecuting || props.activeTab.isExplaining || props.activeTab.isCancelling) return false;
  if (isTransactionActive.value) return false;
  return !!props.executableSql.trim();
});

// Narrow panes (multi-group splits) condense the toolbar by measured width
// instead of overflowing or scrolling. See resolveEditorToolbarTier for the
// tier contract.

const showOverflowMenu = computed(() => toolbarTier.value >= 1);
const canFormatSql = computed(() => canFormatSqlForDatabaseType(props.activeConnection?.db_type));
const showFormatButton = computed(() => canFormatSql.value && toolbarTier.value < 2);
const showExplainAnalyzeToggle = computed(() => supportsExplainAnalyze.value && toolbarTier.value < 3);
const showCompressButton = computed(() => toolbarTier.value < 1);
const showFoldButtons = computed(() => toolbarTier.value < 1);
const foldAllTooltip = computed(() => {
  const shortcut = formatShortcutDisplay(settingsStore.editorSettings.shortcuts.foldAll);
  return shortcut ? `${t("toolbar.foldAll")} (${shortcut})` : t("toolbar.foldAll");
});
const unfoldAllTooltip = computed(() => {
  const shortcut = formatShortcutDisplay(settingsStore.editorSettings.shortcuts.unfoldAll);
  return shortcut ? `${t("toolbar.unfoldAll")} (${shortcut})` : t("toolbar.unfoldAll");
});
const showKeywordCaseButton = computed(() => toolbarTier.value < 1);
const showWordWrapButton = computed(() => toolbarTier.value < 1);
const showSemanticDiagnosticsButton = computed(() => supportsSqlSemanticDiagnosticsToggle.value && toolbarTier.value < 1);
const showPreviewButton = computed(() => previewButtonVisible.value && toolbarTier.value < 1);
const showInsertValueHintsButton = computed(() => supportsInsertValueHintsToggle.value && toolbarTier.value < 1);
const showOpenSqlButton = computed(() => toolbarTier.value < 1);
const showImportArchiveButton = computed(() => toolbarTier.value < 1);
const showPasteSqlButton = computed(() => toolbarTier.value < 1);
const showMultiExecuteButton = computed(() => toolbarTier.value < 1);

function onExecutePointerDown(event: MouseEvent) {
  if (props.activeTab.isExecuting || event.button !== 0) return;
  emit("executePointerDown");
}

function onExecuteClick(event: MouseEvent) {
  if (props.activeTab.isExecuting) {
    emit("cancel");
    return;
  }
  emit("toolbarExecute", event.detail > 0 ? "pointer" : "keyboard");
}

function onExecuteInNewResultTabClick(event: MouseEvent) {
  emit("toolbarExecuteInNewResultTab", event.detail > 0 ? "pointer" : "keyboard");
}
</script>

<template>
  <div ref="toolbarRootRef" class="app-editor-toolbar" :data-toolbar-tier="toolbarTier">
    <QueryWorkspaceContext
      :active-tab="activeTab"
      :active-connection="activeConnection"
      :database-required-signal="databaseRequiredSignal"
      @change-connection="emit('changeConnection', $event)"
      @change-catalog="(catalog, database) => emit('changeCatalog', catalog, database)"
      @change-database="emit('changeDatabase', $event)"
      @change-schema="emit('changeSchema', $event)"
      @set-default-database="emit('setDefaultDatabase')"
      @clear-default-database="emit('clearDefaultDatabase')"
    />
    <div ref="toolbarActionsRef" class="workbench-editor-actions" role="toolbar" :aria-label="t('workbench.queryActions')">
      <div class="workbench-action-group workbench-execution-group" role="group" :aria-label="t('toolbar.runLabel')">
        <Tooltip>
          <TooltipTrigger as-child>
            <Button
              :variant="activeTab.isExecuting ? 'destructive' : 'ghost'"
              size="sm"
              class="workbench-run-button h-7 gap-1.5 shrink-0"
              :class="executeButtonClass"
              :data-tone="activeTab.isExecuting || activeProductionContext.active ? 'danger' : 'success'"
              :aria-label="activeTab.isExecuting ? stopShortcutTooltip : executeShortcutTooltip"
              :disabled="activeTab.isCancelling || activeTab.isExplaining || (!activeTab.isExecuting && !executableSql.trim())"
              @mousedown.prevent="onExecutePointerDown"
              @click="onExecuteClick"
            >
              <Loader2 v-if="activeTab.isCancelling" class="h-3.5 w-3.5 animate-spin" />
              <Square v-else-if="activeTab.isExecuting" class="h-3.5 w-3.5 fill-current" />
              <Play v-else class="h-3.5 w-3.5" />
              <span>{{ activeTab.isExecuting ? t("toolbar.stopLabel") : t("toolbar.runLabel") }}</span>
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ activeTab.isExecuting ? stopShortcutTooltip : executeShortcutTooltip }}</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger as-child>
            <Button
              variant="ghost"
              size="icon"
              class="h-7 w-7"
              :disabled="activeTab.isExecuting || activeTab.isCancelling || activeTab.isExplaining || !executableSql.trim()"
              data-tone="success"
              :aria-label="t('settings.shortcutExecuteSqlInNewResultTab')"
              @mousedown.prevent="onExecutePointerDown"
              @click="onExecuteInNewResultTabClick"
            >
              <SquarePlay class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ executeInNewResultTabTooltip }}</TooltipContent>
        </Tooltip>
        <Tooltip v-if="showPreviewButton">
          <TooltipTrigger as-child>
            <Button variant="ghost" size="icon" class="h-6 w-6" :disabled="activeTab.isExecuting || activeTab.isCancelling" :aria-label="t('editor.previewChanges')" @mousedown.prevent @click="emit('previewChanges')">
              <Eye class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ t("editor.previewChanges") }}</TooltipContent>
        </Tooltip>
        <Tooltip v-if="supportsExplain">
          <TooltipTrigger as-child>
            <Button
              :variant="activeTab.isExplaining ? 'destructive' : 'ghost'"
              size="icon"
              class="h-7 w-7"
              :aria-label="activeTab.isExplaining ? t('toolbar.stopExplain') : explainPlanTooltip"
              :data-tone="activeTab.isExplaining ? 'danger' : undefined"
              :disabled="activeTab.isExecuting || (!activeTab.isExplaining && !executableSql.trim())"
              @click="activeTab.isExplaining ? emit('cancel') : emit('explain')"
            >
              <Square v-if="activeTab.isExplaining" class="h-3.5 w-3.5 fill-current" />
              <GitBranch v-else class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ activeTab.isExplaining ? t("toolbar.stopExplain") : explainPlanTooltip }}</TooltipContent>
        </Tooltip>
        <!-- Autotrace (DM) / EXPLAIN ANALYZE (Postgres) / actual plan (SQL Server) toggle -->
        <Tooltip v-if="showExplainAnalyzeToggle">
          <TooltipTrigger as-child>
            <Button
              variant="ghost"
              size="icon"
              class="h-6 w-6"
              :data-active="props.explainMode === 'autotrace'"
              :disabled="activeTab.isExecuting"
              :aria-label="explainAnalyzeTooltip"
              :aria-pressed="props.explainMode === 'autotrace'"
              @click="emit('update:explainMode', props.explainMode === 'autotrace' ? 'explain' : 'autotrace')"
            >
              <span class="font-semibold" style="font-size: 9px">A</span>
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ explainAnalyzeTooltip }}</TooltipContent>
        </Tooltip>
      </div>
      <div class="workbench-action-group workbench-edit-group" role="group" :aria-label="t('workbench.editActions')">
        <Tooltip v-if="showFormatButton">
          <TooltipTrigger as-child>
            <Button variant="ghost" size="icon" class="h-7 w-7" :disabled="activeTab.isExecuting || activeTab.isExplaining || !activeTab.sql.trim()" :aria-label="t('toolbar.formatSql')" @click="emit('formatSql')">
              <AlignLeft class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ formatSqlTooltip }}</TooltipContent>
        </Tooltip>
        <Tooltip v-if="showCompressButton">
          <TooltipTrigger as-child>
            <Button variant="ghost" size="icon" class="h-6 w-6" :disabled="activeTab.isExecuting || activeTab.isExplaining || !activeTab.sql.trim()" @click="emit('compressSql')">
              <Minimize2 class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ t("toolbar.compressSql") }}</TooltipContent>
        </Tooltip>
        <Tooltip v-if="showFoldButtons">
          <TooltipTrigger as-child>
            <Button variant="ghost" size="icon" class="h-6 w-6" :disabled="activeTab.isExecuting || activeTab.isExplaining || !activeTab.sql.trim()" :aria-label="t('toolbar.foldAll')" @click="emit('foldAll')">
              <FoldVertical class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ foldAllTooltip }}</TooltipContent>
        </Tooltip>
        <Tooltip v-if="showFoldButtons">
          <TooltipTrigger as-child>
            <Button variant="ghost" size="icon" class="h-6 w-6" :disabled="activeTab.isExecuting || activeTab.isExplaining || !activeTab.sql.trim()" :aria-label="t('toolbar.unfoldAll')" @click="emit('unfoldAll')">
              <UnfoldVertical class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ unfoldAllTooltip }}</TooltipContent>
        </Tooltip>
        <Tooltip v-if="showKeywordCaseButton">
          <TooltipTrigger as-child>
            <Button variant="ghost" size="icon" class="h-6 w-6 font-mono text-sm font-semibold leading-none" :data-active="keywordCaseIsLower" :aria-label="keywordCaseToggleTooltip" @click="emit('toggleSqlKeywordCase')">
              {{ keywordCaseIsLower ? "a" : "A" }}
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ keywordCaseToggleTooltip }}</TooltipContent>
        </Tooltip>
        <Tooltip v-if="showWordWrapButton">
          <TooltipTrigger as-child>
            <Button variant="ghost" size="icon" class="h-6 w-6" :data-active="wordWrapEnabled" :disabled="activeTab.forceWordWrap === true" :aria-label="t('settings.wordWrap')" :aria-pressed="wordWrapEnabled" @click="toggleWordWrap">
              <WrapText class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ t("settings.wordWrap") }}</TooltipContent>
        </Tooltip>
        <Tooltip v-if="showSemanticDiagnosticsButton">
          <TooltipTrigger as-child>
            <Button variant="ghost" size="icon" class="h-6 w-6" :data-active="sqlSemanticDiagnosticsEnabled" :aria-label="sqlSemanticDiagnosticsToggleTooltip" @click="toggleSqlSemanticDiagnostics">
              <SpellCheck2 class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ sqlSemanticDiagnosticsToggleTooltip }}</TooltipContent>
        </Tooltip>
        <Tooltip v-if="showInsertValueHintsButton">
          <TooltipTrigger as-child>
            <Button variant="ghost" size="icon" class="h-6 w-6" :data-active="insertValueHintsEnabled" :aria-label="insertValueHintsToggleTooltip" :aria-pressed="insertValueHintsEnabled" @click="toggleInsertValueHints">
              <BetweenVerticalStart class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ insertValueHintsToggleTooltip }}</TooltipContent>
        </Tooltip>
        <Tooltip v-if="activeConnection?.db_type === 'redis'">
          <TooltipTrigger as-child>
            <Button
              variant="ghost"
              size="icon"
              class="h-6 w-6"
              :data-active="blockDangerousRedisCommands !== false"
              :data-tone="blockDangerousRedisCommands !== false ? 'warning' : undefined"
              :aria-label="t('toolbar.blockDangerousRedisCommands')"
              :aria-pressed="blockDangerousRedisCommands !== false"
              @click="emit('update:blockDangerousRedisCommands', blockDangerousRedisCommands === false)"
            >
              <Shield class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ t("toolbar.blockDangerousRedisCommands") }}</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger as-child>
            <Button variant="ghost" size="sm" class="workbench-save-button h-7 gap-1.5 shrink-0" :class="toolbarTier >= 2 ? 'w-7 px-0' : 'px-2'" :disabled="!canSaveSql" :aria-label="saveTooltip" @click="emit('saveSql', props.activeTab.id)">
              <Save class="h-3.5 w-3.5" />
              <span v-if="toolbarTier < 2">{{ t("common.save") }}</span>
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ saveTooltip }}</TooltipContent>
        </Tooltip>
        <Tooltip v-if="isObjectSourceTab">
          <TooltipTrigger as-child>
            <Button variant="ghost" size="icon" class="h-6 w-6" :disabled="objectSourceRefreshing || activeTab.isExecuting" :aria-label="t('structureEditor.refresh')" @click="refreshObjectSource">
              <Loader2 v-if="objectSourceRefreshing" class="h-3.5 w-3.5 animate-spin" />
              <RefreshCw v-else class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ t("structureEditor.refresh") }}</TooltipContent>
        </Tooltip>
        <Tooltip v-if="showOpenSqlButton">
          <TooltipTrigger as-child>
            <Button variant="ghost" size="icon" class="h-6 w-6" @click="emit('openSql')">
              <FolderOpen class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ t("toolbar.openSql") }}</TooltipContent>
        </Tooltip>
        <Tooltip v-if="showImportArchiveButton">
          <TooltipTrigger as-child>
            <Button variant="ghost" size="icon" class="h-6 w-6" @click="emit('importResultArchive')">
              <Download class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ t("tabs.importResultArchive") }}</TooltipContent>
        </Tooltip>
        <Tooltip v-if="showPasteSqlButton">
          <TooltipTrigger as-child>
            <Button variant="ghost" size="icon" class="h-6 w-6" @click="emit('pasteSqlInCondition')">
              <ClipboardPaste class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ exPasteSqlInConditionTooltip }}</TooltipContent>
        </Tooltip>
        <Tooltip v-if="showMultiExecuteButton">
          <TooltipTrigger as-child>
            <Button variant="ghost" size="icon" class="h-6 w-6" :disabled="!canMultiExecute" data-tone="success" :aria-label="t('toolbar.multiDbExecute')" @click="emit('multiExecute')">
              <CirclePlay class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ t("toolbar.multiDbExecute") }}</TooltipContent>
        </Tooltip>
        <DropdownMenu v-if="showOverflowMenu">
          <DropdownMenuTrigger as-child>
            <Button variant="ghost" size="icon" class="h-6 w-6" :aria-label="t('toolbar.moreActions')" :title="t('toolbar.moreActions')">
              <MoreHorizontal class="h-3.5 w-3.5" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" class="w-max min-w-56 max-w-[calc(100vw-1rem)]">
            <DropdownMenuItem :disabled="activeTab.isExecuting || activeTab.isExplaining || !activeTab.sql.trim()" @select="emit('compressSql')">
              <Minimize2 class="h-3.5 w-3.5" />
              {{ t("toolbar.compressSql") }}
            </DropdownMenuItem>
            <DropdownMenuItem :disabled="activeTab.isExecuting || activeTab.isExplaining || !activeTab.sql.trim()" @select="emit('foldAll')">
              <FoldVertical class="h-3.5 w-3.5" />
              {{ t("toolbar.foldAll") }}
            </DropdownMenuItem>
            <DropdownMenuItem :disabled="activeTab.isExecuting || activeTab.isExplaining || !activeTab.sql.trim()" @select="emit('unfoldAll')">
              <UnfoldVertical class="h-3.5 w-3.5" />
              {{ t("toolbar.unfoldAll") }}
            </DropdownMenuItem>
            <DropdownMenuItem @select="emit('toggleSqlKeywordCase')">
              <span class="inline-flex h-4 w-4 shrink-0 items-center justify-center font-mono text-xs font-semibold" aria-hidden="true">
                {{ keywordCaseIsLower ? "A" : "a" }}
              </span>
              {{ keywordCaseToggleTooltip }}
            </DropdownMenuItem>
            <DropdownMenuCheckboxItem :model-value="wordWrapEnabled" :disabled="activeTab.forceWordWrap === true" @select.prevent="toggleWordWrap">
              <WrapText class="h-3.5 w-3.5" />
              {{ t("settings.wordWrap") }}
            </DropdownMenuCheckboxItem>
            <DropdownMenuCheckboxItem v-if="supportsSqlSemanticDiagnosticsToggle" :model-value="sqlSemanticDiagnosticsEnabled" @select.prevent="toggleSqlSemanticDiagnostics">
              <SpellCheck2 class="h-3.5 w-3.5" />
              {{ t("settings.sqlSemanticDiagnosticsEnabled") }}
            </DropdownMenuCheckboxItem>
            <DropdownMenuItem @select="emit('openSql')">
              <FolderOpen class="h-3.5 w-3.5" />
              {{ t("toolbar.openSql") }}
            </DropdownMenuItem>
            <DropdownMenuItem @select="emit('importResultArchive')">
              <Download class="h-3.5 w-3.5" />
              {{ t("tabs.importResultArchive") }}
            </DropdownMenuItem>
            <DropdownMenuItem v-if="supportsExPaste" @select="emit('pasteSqlInCondition')">
              <ClipboardPaste class="h-3.5 w-3.5" />
              {{ t("toolbar.exPasteSqlInCondition") }}
            </DropdownMenuItem>
            <DropdownMenuItem :disabled="!canMultiExecute" @select="emit('multiExecute')">
              <CirclePlay class="h-3.5 w-3.5" />
              {{ t("toolbar.multiDbExecute") }}
            </DropdownMenuItem>
            <DropdownMenuItem v-if="previewButtonVisible" :disabled="activeTab.isExecuting || activeTab.isCancelling" @select="emit('previewChanges')">
              <Eye class="h-3.5 w-3.5" />
              {{ t("editor.previewChanges") }}
            </DropdownMenuItem>
            <DropdownMenuCheckboxItem v-if="supportsInsertValueHintsToggle" :model-value="insertValueHintsEnabled" @select.prevent="toggleInsertValueHints">
              <BetweenVerticalStart class="h-3.5 w-3.5" />
              {{ t("settings.showInsertValueHints") }}
            </DropdownMenuCheckboxItem>
            <template v-if="toolbarTier >= 2">
              <DropdownMenuItem v-if="canFormatSql" :disabled="activeTab.isExecuting || activeTab.isExplaining || !activeTab.sql.trim()" @select="emit('formatSql')">
                <AlignLeft class="h-3.5 w-3.5" />
                {{ t("toolbar.formatSql") }}
              </DropdownMenuItem>
            </template>
            <DropdownMenuCheckboxItem v-if="toolbarTier >= 3 && supportsExplainAnalyze" :model-value="props.explainMode === 'autotrace'" @select.prevent="emit('update:explainMode', props.explainMode === 'autotrace' ? 'explain' : 'autotrace')">
              <span class="font-semibold" style="font-size: 9px">A</span>
              {{ explainAnalyzeTooltip }}
            </DropdownMenuCheckboxItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <div v-if="supportsTransaction" class="workbench-transaction-actions ml-1 flex items-center gap-1 border-l border-border/60 pl-2" role="group" :aria-label="transactionTooltip">
        <!-- Transaction toggle -->
        <Tooltip>
          <TooltipTrigger as-child>
            <Button
              variant="ghost"
              size="icon"
              class="h-6 w-8 px-1"
              data-tone="warning"
              :data-active="isManualTransactionMode"
              :disabled="activeTab.isExecuting || activeTab.isExplaining"
              :aria-label="transactionTooltip"
              :aria-pressed="isManualTransactionMode || hasOpenAutoCommitTransaction"
              @click="emit('update:autoCommit', autoCommit === false)"
            >
              <span class="inline-flex items-center gap-px leading-none" aria-hidden="true">
                <span class="text-[11px] font-medium">Tx:</span>
                <span class="inline-flex h-3 min-w-3 items-center justify-center rounded-[3px] border border-current px-px text-[8px] font-semibold leading-none">{{ transactionModeBadge }}</span>
                <!-- Auto-commit tab with an uncommitted explicit transaction:
                     the commit/rollback actions next to the badge act on it. -->
                <span v-if="hasOpenAutoCommitTransaction" data-toolbar-open-transaction-dot class="ml-px h-1.5 w-1.5 rounded-full bg-current" />
              </span>
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ transactionTooltip }}</TooltipContent>
        </Tooltip>
        <!-- Commit button (only when a transaction action is warranted) -->
        <Tooltip v-if="showTxnActions">
          <TooltipTrigger as-child>
            <Button variant="ghost" size="icon" class="h-6 w-6" :disabled="activeTab.isExecuting" data-tone="success" :aria-label="t('toolbar.commit')" @click="emit('commit')">
              <Check class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ t("toolbar.commit") }}</TooltipContent>
        </Tooltip>

        <!-- Rollback button (only when a transaction action is warranted) -->
        <Tooltip v-if="showTxnActions">
          <TooltipTrigger as-child>
            <Button variant="ghost" size="icon" class="h-6 w-6" :disabled="activeTab.isExecuting" data-tone="danger" :aria-label="t('toolbar.rollback')" @click="emit('rollback')">
              <RotateCcw class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ t("toolbar.rollback") }}</TooltipContent>
        </Tooltip>
      </div>
    </div>
  </div>
  <div v-if="autoCommitTxnRolledBack" data-auto-commit-txn-rolled-back class="workbench-transaction-notice flex items-center gap-2 px-3 py-1 text-xs bg-amber-500/10 text-amber-700 dark:text-amber-300 border-b border-amber-500/20">
    <AlertTriangle class="h-3.5 w-3.5 shrink-0" />
    <span>{{ t("toolbar.autoCommitTxnRolledBack") }}</span>
    <Button variant="ghost" size="icon" class="h-5 w-5 ml-auto" @click="emit('dismissAutoCommitTxnRolledBack')">
      <X class="h-3 w-3" />
    </Button>
  </div>
  <div v-else-if="autoCommitSessionTxnRolledBack" data-auto-commit-session-txn-rolled-back class="workbench-transaction-notice flex items-center gap-2 px-3 py-1 text-xs bg-amber-500/10 text-amber-700 dark:text-amber-300 border-b border-amber-500/20">
    <AlertTriangle class="h-3.5 w-3.5 shrink-0" />
    <span>{{ t("toolbar.autoCommitSessionTxnRolledBack") }}</span>
    <Button variant="ghost" size="icon" class="h-5 w-5 ml-auto" @click="emit('dismissAutoCommitSessionTxnRolledBack')">
      <X class="h-3 w-3" />
    </Button>
  </div>
  <div v-if="txnAutoRolledBack" class="workbench-transaction-notice flex items-center gap-2 px-3 py-1 text-xs bg-amber-500/10 text-amber-700 dark:text-amber-300 border-b border-amber-500/20">
    <AlertTriangle class="h-3.5 w-3.5 shrink-0" />
    <span>{{ t("toolbar.txnAutoRolledBack") }}</span>
    <Button variant="ghost" size="icon" class="h-5 w-5 ml-auto" @click="emit('dismissTxnRolledBack')">
      <X class="h-3 w-3" />
    </Button>
  </div>
</template>

<style scoped>
.app-editor-toolbar {
  position: relative;
  z-index: 10;
  min-width: 0;
  flex: 0 0 auto;
  border-bottom: 1px solid var(--workbench-border, var(--border));
  color: var(--workbench-secondary, var(--muted-foreground));
  background: var(--workbench-chrome, var(--muted));
  font-family: var(--workbench-font-ui, inherit);
  font-size: var(--workbench-font-size-ui, 12px);
}
.workbench-editor-actions {
  display: flex;
  min-width: 0;
  min-height: 36px;
  align-items: center;
  gap: 6px;
  overflow-x: auto;
  overflow-y: hidden;
  padding: 4px 8px;
  scrollbar-width: thin;
}
.workbench-action-group {
  display: flex;
  align-items: center;
  gap: 2px;
  flex: 0 0 auto;
}
.workbench-edit-group {
  border-left: 1px solid var(--workbench-border, var(--border));
  padding-left: 6px;
}
.workbench-transaction-actions {
  flex: 0 0 auto;
  margin-left: auto;
  border-color: var(--workbench-border, var(--border));
}
.workbench-run-button {
  min-width: 68px;
  padding-inline: 9px;
  border-radius: 4px;
  font-weight: var(--workbench-font-weight-heading, 600);
}
.workbench-editor-actions :deep(button) {
  border-radius: 3px;
  box-shadow: none;
  color: var(--workbench-secondary, var(--muted-foreground));
  font-family: var(--workbench-font-ui, inherit);
  font-size: var(--workbench-font-size-ui, 12px);
  font-weight: 500;
}
.workbench-editor-actions :deep(button:focus-visible) {
  outline: 2px solid var(--workbench-accent, var(--ring));
  outline-offset: -2px;
}
.workbench-editor-actions :deep(button:hover) {
  color: var(--workbench-text, var(--foreground));
  background: var(--workbench-hover, var(--accent));
}
.workbench-editor-actions :deep(button[data-active="true"]) {
  color: var(--workbench-accent, var(--primary));
  background: var(--workbench-accent-soft, var(--accent));
}
.workbench-editor-actions :deep(button[data-tone="success"]) {
  --action-tone: var(--workbench-success, #34834d);
}
.workbench-editor-actions :deep(button[data-tone="danger"]) {
  --action-tone: var(--workbench-danger, var(--destructive));
}
.workbench-editor-actions :deep(button[data-tone="warning"]) {
  --action-tone: var(--workbench-warning, #a15c10);
}
.workbench-editor-actions :deep(button[data-tone]) {
  color: var(--action-tone);
}
.workbench-editor-actions :deep(button[data-tone]:hover),
.workbench-editor-actions :deep(button[data-tone][data-active="true"]) {
  color: var(--action-tone);
  background: color-mix(in oklch, var(--action-tone) 12%, transparent);
}
.workbench-editor-actions :deep(.workbench-run-button[data-tone]) {
  color: var(--action-tone);
  background: color-mix(in oklch, var(--action-tone) 14%, transparent);
  border-color: color-mix(in oklch, var(--action-tone) 24%, transparent);
  font-weight: var(--workbench-font-weight-heading, 600);
}
.workbench-editor-actions :deep(.workbench-run-button[data-tone]:hover) {
  background: color-mix(in oklch, var(--action-tone) 21%, transparent);
}
.workbench-editor-actions :deep(.font-mono) {
  font-family: var(--workbench-font-mono, monospace);
}
.workbench-transaction-notice {
  color: var(--workbench-warning, #a15c10);
  background: color-mix(in oklch, var(--workbench-warning, #a15c10) 10%, transparent);
  border-color: color-mix(in oklch, var(--workbench-warning, #a15c10) 22%, transparent);
  font-family: var(--workbench-font-ui, inherit);
  font-size: var(--workbench-font-size-ui, 12px);
}
</style>
