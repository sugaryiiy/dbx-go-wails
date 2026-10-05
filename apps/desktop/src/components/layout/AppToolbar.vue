<script setup lang="ts">
import { computed, ref, onMounted, onBeforeUnmount, watch } from "vue";
import { useI18n } from "vue-i18n";
import { invoke } from "@tauri-apps/api/core";
import { ChevronsRight, DatabaseZap, FilePlus2, Moon, Sun, SunMoon, History, Bot, ArrowLeftRight, FileCode, BookMarked, GitCompareArrows, TableProperties, Settings, LogOut, Package, PlugZap, FolderTree, Pin, PinOff, CalendarClock, Waypoints, Search, Wrench, Type } from "@lucide/vue";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import LightDropdown, { type LightDropdownItem } from "@/components/ui/LightDropdown.vue";
import WindowControls from "@/components/layout/WindowControls.vue";
import ExportProgressPopover from "@/components/export/ExportProgressPopover.vue";
import PluginShortcutToolbar from "@/components/plugins/PluginShortcutToolbar.vue";
import AppLogo from "@/components/icons/AppLogo.vue";
import { MAC_TRAFFIC_LIGHT_X, macTrafficLightInsetPaddingForScale, shouldReserveMacTrafficLightInset, useWindowControls } from "@/composables/useWindowControls";
import { useToast } from "@/composables/useToast";
import { useSettingsStore } from "@/stores/settingsStore";
import { isSystemAppThemeMode, type AppThemeMode } from "@/lib/app/appTheme";
import { formatShortcutDisplay, formatShortcutTooltip } from "@/lib/editor/shortcutDisplay";

const props = defineProps<{
  workspaceName?: string;
  databaseName?: string;
  toolWindowsInRail?: boolean;
  isDark: boolean;
  themeMode: AppThemeMode;
  showSidebarExpand?: boolean;
  showAiPanel: boolean;
  activeAiRunCount: number;
  /** Runs awaiting a write confirmation; the badge turns amber to outrank the
   *  plain active count (parent PRD §4 line 71). */
  awaitingAiRunCount: number;
  showHistory: boolean;
  showSqlLibrary: boolean;
  sqlLibrarySaveFeedbackId: number;
  showSqlFilePanel: boolean;
  showDriverStore: boolean;
  showPluginCenter: boolean;
  showSettingsPage: boolean;
  checkingUpdates: boolean;
  updateVersion?: string;
  hasUpdateAvailable: boolean;
  isDownloadingUpdate: boolean;
  downloadProgress: number | null;
  updateReadyToInstall: boolean;
  updateReady: boolean;
  agentDriverUpdateCount: number;
  hasMcpUpdateAvailable: boolean;
  hasConnections: boolean;
  canNewQuery: boolean;
  hasSqlFileConnections: boolean;
  showLogout?: boolean;
}>();

const emit = defineEmits<{
  "expand-sidebar": [];
  "new-connection": [];
  "new-query": [];
  "set-theme-mode": [mode: AppThemeMode];
  "toggle-ai": [];
  "toggle-history": [];
  "toggle-sql-library": [];
  "toggle-sql-file-panel": [];
  "open-settings": [];
  "open-font-settings": [];
  "open-driver-store": [];
  "open-plugin-center": [];
  "open-transfer": [];
  "open-sql-file": [];
  "open-schema-diff": [];
  "open-data-compare": [];
  "open-backups": [];
  "open-mcp-settings": [];
  "find-action": [];
  logout: [];
}>();

const { t } = useI18n();
const { toast } = useToast();
const settingsStore = useSettingsStore();
const toolbarItems = computed(() => settingsStore.editorSettings.toolbarItems);
const { isMac, isDesktop, showControls, isMaximized, isFullscreen, isAlwaysOnTop, minimize, toggleMaximize, toggleAlwaysOnTop, close } = useWindowControls();
const showAlwaysOnTopButton = computed(() => isDesktop && (toolbarItems.value.alwaysOnTop || isAlwaysOnTop.value));
const workspaceLabel = computed(() => props.workspaceName?.trim() || t("workbenchLayout.workspace"));
const actionSearchShortcut = computed(() => (settingsStore.editorSettings.shortcuts.findAction ? formatShortcutDisplay(settingsStore.editorSettings.shortcuts.findAction) : ""));
const sidebarExpandTooltip = computed(() => formatShortcutTooltip(t("sidebar.expand"), settingsStore.editorSettings.shortcuts.toggleSidebar));
const newQueryTooltip = computed(() => formatShortcutTooltip(t("toolbar.newQuery"), settingsStore.editorSettings.shortcuts.newQuery));
const settingsTooltip = computed(() => (props.hasMcpUpdateAvailable ? t("toolbar.mcpUpdateAvailable") : formatShortcutTooltip(t("settings.title"), settingsStore.editorSettings.shortcuts.openSettings)));
const themeTriggerIcon = computed(() => (isSystemAppThemeMode(props.themeMode) ? SunMoon : props.isDark ? Moon : Sun));
function cycleThemeMode() {
  const modes: AppThemeMode[] = ["light", "dark", "system"];
  const next = modes[(modes.indexOf(props.themeMode) + 1) % modes.length];
  emit("set-theme-mode", next);
  const label = next === "light" ? "toolbar.themeLight" : next === "dark" ? "toolbar.themeDark" : "toolbar.themeSystem";
  toast(`${t("toolbar.theme")}: ${t(label)}`, 1600);
}

type ToolbarMenuItem = LightDropdownItem & { action: () => void };
const toolWindowItems = computed(() => [
  { id: "sqlLibrary", label: t("sqlLibrary.title"), icon: BookMarked, active: props.showSqlLibrary, visible: toolbarItems.value.sqlLibrary, action: () => emit("toggle-sql-library") },
  { id: "sqlFileTree", label: t("sqlFileTree.title"), icon: FolderTree, active: props.showSqlFilePanel, visible: toolbarItems.value.sqlFileTree, action: () => emit("toggle-sql-file-panel") },
  { id: "history", label: formatShortcutTooltip(t("history.title"), settingsStore.editorSettings.shortcuts.toggleQueryHistory), icon: History, active: props.showHistory, visible: toolbarItems.value.history, action: () => emit("toggle-history") },
  { id: "ai", label: formatShortcutTooltip("AI", settingsStore.editorSettings.shortcuts.toggleAiPanel), icon: Bot, active: props.showAiPanel, visible: toolbarItems.value.ai, action: () => emit("toggle-ai") },
]);
const toolMenuItems = computed<ToolbarMenuItem[]>(() => [
  { value: "find-action", label: formatShortcutTooltip(t("actionSearch.title"), settingsStore.editorSettings.shortcuts.findAction), icon: Search, action: () => emit("find-action") },
  { value: "font-settings", label: t("settings.fontSettingsTitle"), icon: Type, action: () => emit("open-font-settings") },
  { value: "transfer", label: t("transfer.dataTransfer"), groupLabel: t("welcome.basics.transfer"), separatorBefore: true, icon: ArrowLeftRight, disabled: !props.hasConnections, action: () => emit("open-transfer") },
  { value: "sql-file", label: t("sqlFile.title"), icon: FileCode, disabled: !props.hasSqlFileConnections, action: () => emit("open-sql-file") },
  { value: "schema-diff", label: t("diff.title"), icon: GitCompareArrows, disabled: !props.hasConnections, action: () => emit("open-schema-diff") },
  { value: "data-compare", label: t("dataCompare.title"), icon: TableProperties, disabled: !props.hasConnections, action: () => emit("open-data-compare") },
  { value: "database-backups", label: t("databaseBackup.title"), icon: CalendarClock, action: () => emit("open-backups") },
  { value: "driver-store", label: props.agentDriverUpdateCount > 0 ? `${t("toolbar.driverManager")} (${props.agentDriverUpdateCount})` : t("toolbar.driverManager"), groupLabel: t("workbenchLayout.tools"), separatorBefore: true, icon: Package, action: () => emit("open-driver-store") },
  { value: "plugin-center", label: t("toolbar.pluginCenter"), icon: PlugZap, action: () => emit("open-plugin-center") },
  { value: "mcp-settings", label: t("settings.openMcpSettings"), icon: Waypoints, action: () => emit("open-mcp-settings") },
  ...toolWindowItems.value.filter((item) => !item.visible).map((item, index) => ({ value: `window-${item.id}`, label: item.label, icon: item.icon, action: item.action, separatorBefore: index === 0, groupLabel: index === 0 ? t("workbenchLayout.workspace") : undefined })),
]);
const activeToolValues = computed(() => [props.showDriverStore && "driver-store", props.showPluginCenter && "plugin-center", ...toolWindowItems.value.filter((item) => !item.visible && item.active).map((item) => `window-${item.id}`)].filter((value): value is string => !!value));
function runTool(value: string) {
  const item = toolMenuItems.value.find((item) => item.value === value);
  if (item && !item.disabled) item.action();
}
const legacyToolWindows = computed(() => toolWindowItems.value.filter((item) => item.visible));
const showPluginShortcuts = computed(() => settingsStore.editorSettings.pluginShortcuts.enabled && ["toolbar", "plugin-center"].includes(settingsStore.editorSettings.pluginShortcuts.position));
const sqlLibrarySaveFeedbackActive = ref(false);
let saveFeedbackTimer = 0;
watch(
  () => props.sqlLibrarySaveFeedbackId,
  (id, previous) => {
    if (id <= 0 || id === previous) return;
    sqlLibrarySaveFeedbackActive.value = true;
    window.clearTimeout(saveFeedbackTimer);
    saveFeedbackTimer = window.setTimeout(() => {
      sqlLibrarySaveFeedbackActive.value = false;
    }, 850);
  },
);

const toolbarEl = ref<HTMLElement>();
const compact = ref(false);
const measuredTrafficLightInset = ref<number | null>(null);
const shouldReserveTrafficLightInset = computed(() => shouldReserveMacTrafficLightInset(isMac, isFullscreen.value, isDesktop));
const toolbarStyle = computed(() => (shouldReserveTrafficLightInset.value ? { paddingLeft: `${measuredTrafficLightInset.value ?? parseInt(macTrafficLightInsetPaddingForScale(settingsStore.editorSettings.uiScale), 10)}px` } : undefined));
let layoutFrame = 0;
let scaleSyncTimer = 0;
let resizeObserver: ResizeObserver | undefined;
async function syncToolbarLayout() {
  const element = toolbarEl.value;
  if (!element) return;
  compact.value = element.clientWidth > 0 && element.clientWidth < 1000;
  if (!shouldReserveTrafficLightInset.value) return;
  const targetCenterY = element.getBoundingClientRect().height / 2;
  try {
    const layout = await invoke<{ reserved_inset: number }>("set_macos_traffic_light_position", { x: MAC_TRAFFIC_LIGHT_X, y: targetCenterY, scale: settingsStore.editorSettings.uiScale });
    if (layout) measuredTrafficLightInset.value = Math.ceil(layout.reserved_inset / settingsStore.editorSettings.uiScale);
  } catch (error) {
    console.warn("[DBX] Failed to sync macOS traffic light position", { targetCenterY, error });
  }
}
function scheduleToolbarLayout() {
  if (layoutFrame) cancelAnimationFrame(layoutFrame);
  layoutFrame = requestAnimationFrame(() => {
    layoutFrame = 0;
    void syncToolbarLayout();
  });
}
watch(shouldReserveTrafficLightInset, scheduleToolbarLayout);
watch(
  () => settingsStore.editorSettings.uiScale,
  () => {
    measuredTrafficLightInset.value = null;
    scheduleToolbarLayout();
    window.clearTimeout(scaleSyncTimer);
    scaleSyncTimer = window.setTimeout(scheduleToolbarLayout, 120);
  },
);
onMounted(() => {
  if (typeof ResizeObserver !== "undefined") {
    resizeObserver = new ResizeObserver(scheduleToolbarLayout);
    if (toolbarEl.value) resizeObserver.observe(toolbarEl.value);
  }
  window.addEventListener("resize", scheduleToolbarLayout);
  scheduleToolbarLayout();
});
onBeforeUnmount(() => {
  if (layoutFrame) cancelAnimationFrame(layoutFrame);
  window.clearTimeout(saveFeedbackTimer);
  window.clearTimeout(scaleSyncTimer);
  resizeObserver?.disconnect();
  window.removeEventListener("resize", scheduleToolbarLayout);
});
</script>

<template>
  <header ref="toolbarEl" class="app-toolbar workbench-toolbar" :class="{ 'is-compact': compact }" :style="toolbarStyle" data-tauri-drag-region>
    <div class="workbench-identity" data-tauri-drag-region>
      <span class="workbench-brand" :title="t('app.name')" data-testid="workbench-brand" data-tauri-drag-region
        ><AppLogo class="size-5 pointer-events-none" /><span>{{ t("app.name") }}</span></span
      >
      <div class="workbench-context" data-tauri-drag-region>
        <span class="workbench-workspace-name" :title="workspaceLabel">{{ workspaceLabel }}</span>
        <span v-if="databaseName" class="workbench-database-name" :title="databaseName">{{ databaseName }}</span>
      </div>
    </div>
    <Tooltip v-if="showSidebarExpand">
      <TooltipTrigger as-child
        ><Button variant="ghost" size="icon" class="workbench-icon-button" :aria-label="t('sidebar.expand')" @click="emit('expand-sidebar')"><ChevronsRight class="size-4" /></Button
      ></TooltipTrigger>
      <TooltipContent>{{ sidebarExpandTooltip }}</TooltipContent>
    </Tooltip>
    <div class="workbench-primary-actions" role="group" :aria-label="t('toolbar.newConnection')">
      <Button variant="ghost" size="sm" class="workbench-command" :title="t('toolbar.newConnection')" :aria-label="t('toolbar.newConnection')" @click="emit('new-connection')"
        ><DatabaseZap class="size-4" /><span v-if="!compact">{{ t("toolbar.newConnection") }}</span></Button
      >
      <Tooltip>
        <TooltipTrigger as-child
          ><Button variant="ghost" size="sm" class="workbench-command workbench-new-query" :disabled="!canNewQuery" :title="newQueryTooltip" :aria-label="t('toolbar.newQuery')" @click="emit('new-query')"
            ><FilePlus2 class="size-4" /><span v-if="!compact">{{ t("toolbar.newQuery") }}</span></Button
          ></TooltipTrigger
        >
        <TooltipContent>{{ newQueryTooltip }}</TooltipContent>
      </Tooltip>
    </div>
    <div class="workbench-search-region" data-tauri-drag-region>
      <button type="button" class="workbench-action-search" :title="t('actionSearch.title')" :aria-label="t('actionSearch.title')" @click="emit('find-action')">
        <Search class="size-3.5 shrink-0" /><span>{{ t("actionSearch.title") }}</span
        ><kbd v-if="actionSearchShortcut">{{ actionSearchShortcut }}</kbd>
      </button>
    </div>
    <div class="workbench-tools">
      <LightDropdown
        model-value=""
        :items="toolMenuItems"
        :selected-values="activeToolValues"
        :aria-label="t('workbenchLayout.tools')"
        :trigger-label="t('workbenchLayout.tools')"
        :trigger-icon="Wrench"
        :show-trigger-label="!compact"
        trigger-class="workbench-tools-trigger"
        :show-chevron="true"
        check-position="none"
        align="end"
        content-class="min-w-56"
        @update:model-value="runTool"
      />
      <PluginShortcutToolbar v-if="showPluginShortcuts" dropdown-only @layout-change="scheduleToolbarLayout" />
    </div>
    <div v-if="!toolWindowsInRail" class="workbench-legacy-windows" role="group" :aria-label="t('workbenchLayout.workspace')">
      <Tooltip v-for="item in legacyToolWindows" :key="item.id">
        <TooltipTrigger as-child>
          <Button
            variant="ghost"
            size="icon"
            class="workbench-icon-button relative"
            :class="{ 'is-active': item.active, 'save-feedback': item.id === 'sqlLibrary' && sqlLibrarySaveFeedbackActive }"
            :data-sql-library-trigger="item.id === 'sqlLibrary' ? '' : undefined"
            :aria-label="item.label"
            :aria-pressed="item.active"
            @click="item.action()"
          >
            <component :is="item.icon" class="size-4" />
            <span v-if="item.id === 'ai' && (awaitingAiRunCount || activeAiRunCount)" class="workbench-count" :class="{ 'is-awaiting': awaitingAiRunCount > 0 }" :title="awaitingAiRunCount > 0 ? t('ai.toolbarAwaitingConfirmation', { count: awaitingAiRunCount }) : undefined"
              >{{ Math.min(awaitingAiRunCount || activeAiRunCount, 9) }}{{ (awaitingAiRunCount || activeAiRunCount) > 9 ? "+" : "" }}</span
            >
          </Button>
        </TooltipTrigger>
        <TooltipContent>{{ item.label }}</TooltipContent>
      </Tooltip>
    </div>
    <div class="workbench-window-actions">
      <ExportProgressPopover />
      <Tooltip v-if="showAlwaysOnTopButton">
        <TooltipTrigger as-child
          ><Button variant="ghost" size="icon" class="workbench-icon-button" :class="{ 'is-active': isAlwaysOnTop }" :aria-pressed="isAlwaysOnTop" :aria-label="isAlwaysOnTop ? t('toolbar.alwaysOnTopOff') : t('toolbar.alwaysOnTop')" @click="toggleAlwaysOnTop"
            ><Pin v-if="isAlwaysOnTop" class="size-4 fill-current" /><PinOff v-else class="size-4" /></Button
        ></TooltipTrigger>
        <TooltipContent>{{ isAlwaysOnTop ? t("toolbar.alwaysOnTopOff") : t("toolbar.alwaysOnTop") }}</TooltipContent>
      </Tooltip>
      <Tooltip v-if="toolbarItems.theme">
        <TooltipTrigger as-child
          ><Button variant="ghost" size="icon" class="workbench-icon-button" :aria-label="t('toolbar.theme')" @click="cycleThemeMode"><component :is="themeTriggerIcon" class="size-4" /></Button
        ></TooltipTrigger>
        <TooltipContent>{{ t("toolbar.theme") }}</TooltipContent>
      </Tooltip>
      <Tooltip>
        <TooltipTrigger as-child
          ><Button variant="ghost" size="icon" class="workbench-icon-button relative" :class="{ 'is-active': showSettingsPage }" :aria-label="t('settings.title')" :aria-pressed="showSettingsPage" @click="emit('open-settings')"
            ><Settings class="size-4" /><span v-if="hasMcpUpdateAvailable" class="workbench-notice" :title="t('toolbar.mcpUpdateAvailable')" /></Button
        ></TooltipTrigger>
        <TooltipContent>{{ settingsTooltip }}</TooltipContent>
      </Tooltip>
      <Tooltip v-if="showLogout">
        <TooltipTrigger as-child
          ><Button variant="ghost" size="icon" class="workbench-icon-button" :aria-label="t('auth.logout')" @click="emit('logout')"><LogOut class="size-4" /></Button
        ></TooltipTrigger>
        <TooltipContent>{{ t("auth.logout") }}</TooltipContent>
      </Tooltip>
      <WindowControls v-if="showControls" :is-maximized="isMaximized" @minimize="minimize" @toggle-maximize="toggleMaximize" @close="close" />
    </div>
  </header>
</template>

<style scoped>
.workbench-toolbar {
  display: flex;
  flex: 0 0 44px;
  height: 44px;
  min-width: 0;
  align-items: center;
  gap: 8px;
  padding: 0 10px;
  border-bottom: 1px solid var(--workbench-border, var(--border));
  background: var(--workbench-chrome, var(--muted));
  color: var(--workbench-text, var(--foreground));
}
.workbench-identity {
  display: flex;
  min-width: 90px;
  max-width: 260px;
  align-items: center;
  gap: 12px;
  flex: 0 1 230px;
}
.workbench-brand {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  font-weight: var(--workbench-font-weight-heading, 600);
  text-decoration: none;
  color: inherit;
}
.workbench-context {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 1px;
  padding-left: 12px;
  border-left: 1px solid var(--workbench-border, var(--border));
}
.workbench-workspace-name,
.workbench-database-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.workbench-workspace-name {
  font-size: 12px;
  line-height: 16px;
  font-weight: var(--workbench-font-weight-heading, 600);
}
.workbench-database-name {
  color: var(--workbench-secondary, var(--muted-foreground));
  font-family: var(--workbench-font-mono, monospace);
  font-size: var(--workbench-font-size-caption, 11px);
  line-height: 13px;
}
.workbench-primary-actions,
.workbench-tools,
.workbench-legacy-windows,
.workbench-window-actions {
  display: flex;
  flex: none;
  align-items: center;
  gap: 2px;
}
.workbench-primary-actions {
  padding-right: 6px;
  border-right: 1px solid var(--workbench-border, var(--border));
}
.workbench-command,
.workbench-icon-button {
  height: 28px;
  border-radius: 3px;
  box-shadow: none;
  font-size: 12px;
  font-weight: 400;
}
.workbench-command {
  color: var(--workbench-secondary, var(--foreground));
  gap: 5px;
  padding: 0 7px;
}
.workbench-icon-button {
  width: 28px;
  padding: 0;
}
.workbench-new-query {
  color: var(--workbench-accent, var(--primary));
}
.workbench-search-region {
  display: flex;
  min-width: 90px;
  flex: 1 1 260px;
  justify-content: center;
}
.workbench-action-search {
  display: flex;
  width: 100%;
  max-width: 360px;
  height: 28px;
  min-width: 0;
  align-items: center;
  gap: 8px;
  border: 1px solid var(--workbench-border, var(--border));
  border-radius: 3px;
  padding: 0 8px;
  background: var(--workbench-surface, var(--background));
  color: var(--workbench-muted, var(--muted-foreground));
  font-size: 12px;
  cursor: pointer;
}
.workbench-action-search > span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.workbench-action-search kbd {
  font-family: var(--workbench-font-mono, monospace);
  margin-left: auto;
  flex-shrink: 0;
  font-size: 10px;
  color: var(--workbench-muted, var(--muted-foreground));
}
.workbench-tools :deep(.workbench-tools-trigger) {
  display: flex;
  height: 28px;
  align-items: center;
  gap: 5px;
  padding: 0 6px;
  border-radius: 3px;
  font-size: 12px;
}
.workbench-tools :deep(.workbench-tools-trigger:hover),
.workbench-command:hover:not(:disabled),
.workbench-icon-button:hover {
  background: var(--workbench-hover, var(--accent));
}
.workbench-window-actions {
  border-left: 1px solid var(--workbench-border, var(--border));
  padding-left: 6px;
}
.workbench-icon-button.is-active,
.workbench-icon-button.save-feedback {
  background: var(--workbench-accent-soft, var(--accent));
  color: var(--workbench-accent, var(--primary));
}
.workbench-notice {
  position: absolute;
  top: 3px;
  right: 3px;
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--workbench-accent, var(--primary));
}
.workbench-count {
  position: absolute;
  top: 0;
  right: 0;
  padding: 0 2px;
  min-width: 11px;
  border-radius: 3px;
  background: var(--workbench-accent, var(--primary));
  color: var(--primary-foreground);
  font-size: 8px;
  line-height: 11px;
}
.workbench-count.is-awaiting {
  background: #b87922;
  color: white;
}
.workbench-toolbar :is(button, a):focus-visible {
  outline: 1px solid var(--workbench-accent, var(--primary));
  outline-offset: 1px;
}
.is-compact {
  gap: 5px;
  padding-inline: 6px;
}
.is-compact .workbench-identity {
  flex-basis: 155px;
  min-width: 95px;
  gap: 7px;
}
.is-compact .workbench-context {
  padding-left: 7px;
}
.is-compact .workbench-command {
  width: 28px;
  padding: 0;
}
</style>
