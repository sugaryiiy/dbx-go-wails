<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { translateBackendError } from "@/i18n/backend-errors";
import { Upload, Download, FolderPlus, FolderOpen, ChevronsDownUp, Trash2, FolderInput, Check, Minus, Square, X } from "@lucide/vue";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import LightDropdown from "@/components/ui/LightDropdown.vue";
import LightTooltip from "@/components/ui/LightTooltip.vue";
import PluginShortcutBar from "@/components/plugins/PluginShortcutBar.vue";
import { useSettingsStore } from "@/stores/settingsStore";
import ConnectionTree from "@/components/sidebar/ConnectionTree.vue";
import SidebarToolbar from "@/components/sidebar/SidebarToolbar.vue";
import { applyConnectionMultiSelection, emptyConnectionMultiSelection, isExitConnectionMultiSelectionShortcut } from "@/lib/sidebar/sidebarConnectionMultiSelect";
import { connectionGroupDestinationRows } from "@/lib/sidebar/sidebarLayout";
import { useConnectionStore } from "@/stores/connectionStore";
import { useToast } from "@/composables/useToast";
import type { QueryTab, TreeNode } from "@/types/database";

defineProps<{
  sidebarWidth: number;
  classicLayout?: boolean;
  embedded?: boolean;
}>();

const emit = defineEmits<{
  import: [source: "dbx" | "navicat" | "dbeaver" | "datagrip"];
  export: [];
  startResize: [event: PointerEvent];
  collapse: [];
  "new-connection": [];
  "open-settings": [initialTab: string];
  "add-to-ai": [nodes: TreeNode | TreeNode[]];
}>();

type ImportSource = "dbx" | "navicat" | "dbeaver" | "datagrip";

const { t } = useI18n();
const connectionStore = useConnectionStore();
const settingsStore = useSettingsStore();
const { toast } = useToast();
const connectionTreeRef = ref<InstanceType<typeof ConnectionTree>>();
const refreshingTree = ref(false);
const showDeleteSelectedConfirm = ref(false);
const showCreateSelectedGroupDialog = ref(false);
const selectedGroupName = ref("");
const UNGROUPED_GROUP_VALUE = "__ungrouped";
const importSourceItems = computed(() => [
  { value: "dbx", label: t("sidebar.importDbx") },
  { value: "navicat", label: t("sidebar.importNavicat") },
  { value: "dbeaver", label: t("sidebar.importDbeaver") },
  { value: "datagrip", label: t("sidebar.importDatagrip") },
]);
const connectionTransferItems = computed(() => [
  ...importSourceItems.value.map((item, index) => ({
    ...item,
    value: `import:${item.value}`,
    icon: Download,
    separatorBefore: index === 0,
    groupLabel: index === 0 ? t("sidebar.importExport") : undefined,
  })),
  {
    value: "export",
    label: t("sidebar.export"),
    icon: Upload,
    separatorBefore: true,
  },
]);
const connectionToolbarItems = computed(() => [
  { value: "create-group", label: t("connectionGroup.createGroup"), icon: FolderPlus },
  { value: "collapse-all", label: t("sidebar.collapseAll"), icon: ChevronsDownUp },
]);
const connectionIdSet = computed(() => new Set(connectionStore.connections.map((connection) => connection.id)));
const allConnectionIds = computed(() => connectionStore.connections.map((connection) => connection.id));
const selectedConnectionIds = computed(() => (connectionStore.connectionMultiSelectActive ? connectionStore.selectedTreeNodeIds.filter((id) => connectionIdSet.value.has(id)) : []));
const selectedConnectionCount = computed(() => selectedConnectionIds.value.length);
const showConnectionMultiSelectToolbar = computed(() => connectionStore.connectionMultiSelectActive && selectedConnectionCount.value > 0);
const allConnectionsSelected = computed(() => allConnectionIds.value.length > 0 && selectedConnectionCount.value === allConnectionIds.value.length);
const selectAllIcon = computed(() => (allConnectionsSelected.value ? Check : selectedConnectionCount.value > 0 ? Minus : Square));
const selectAllLabel = computed(() => (allConnectionsSelected.value ? t("connectionGroup.deselectAllConnections") : t("connectionGroup.selectAllConnections")));
const moveGroupItems = computed(() => [
  ...connectionGroupDestinationRows(connectionStore.sidebarLayout).map((group) => ({
    value: group.id,
    label: group.name,
    title: group.path.join(" / "),
    icon: FolderOpen,
    indentLevel: group.depth,
  })),
  {
    value: UNGROUPED_GROUP_VALUE,
    label: t("connectionGroup.ungrouped"),
    separatorBefore: connectionStore.sidebarLayout.groups.length > 0,
  },
]);

async function refreshTree() {
  if (refreshingTree.value) return;
  refreshingTree.value = true;
  try {
    await connectionStore.reloadFromDisk();
    await connectionStore.refreshAllTree();
  } catch (e: any) {
    toast(t("connection.connectFailed", { message: translateBackendError(t, e) }), 5000);
  } finally {
    refreshingTree.value = false;
  }
}

function createNewGroup() {
  void connectionTreeRef.value?.createNewGroup();
}

function selectImportSource(source: string) {
  emit("import", source as ImportSource);
}

function selectConnectionToolbarAction(action: string, selectTreeOption: (value: string) => void) {
  if (action === "create-group") {
    createNewGroup();
    return;
  }
  if (action === "collapse-all") {
    collapseAllTreeNodes();
    return;
  }
  if (action === "export") {
    emit("export");
    return;
  }
  if (action.startsWith("import:")) {
    selectImportSource(action.slice("import:".length));
    return;
  }
  selectTreeOption(action);
}

function collapseAllTreeNodes() {
  connectionTreeRef.value?.collapseAllTreeNodes();
}

function focusSearch(target: Element | null = null): boolean {
  return connectionTreeRef.value?.focusSearch(target) ?? false;
}

function locateTabInSidebar(tab: QueryTab) {
  return connectionTreeRef.value?.locateTabInSidebar(tab);
}

function clearConnectionMultiSelection() {
  applyConnectionMultiSelection(connectionStore, emptyConnectionMultiSelection());
}

function isEditableSidebarTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement || target.isContentEditable || !!target.closest("[contenteditable='true'], [role='textbox']");
}

function onSidebarKeydown(event: KeyboardEvent) {
  if (event.defaultPrevented || !connectionStore.connectionMultiSelectActive || !isExitConnectionMultiSelectionShortcut(event) || isEditableSidebarTarget(event.target)) return;
  event.preventDefault();
  event.stopPropagation();
  clearConnectionMultiSelection();
}

function toggleAllConnectionsSelected() {
  if (allConnectionsSelected.value) {
    clearConnectionMultiSelection();
    return;
  }

  const ids = allConnectionIds.value;
  connectionStore.connectionMultiSelectActive = ids.length > 0;
  connectionStore.selectedTreeNodeIds = ids;
  connectionStore.selectedTreeNodeId = ids[0] ?? null;
  connectionStore.treeSelectionAnchorId = ids[0] ?? null;
}

async function confirmDeleteSelectedConnections() {
  const ids = selectedConnectionIds.value;
  if (ids.length === 0) return;
  try {
    await connectionStore.removeConnections(ids);
    for (const connectionId of ids) {
      // 页签已由 removeConnections 按「删除连接」策略处理，这里只清会话。
      connectionStore.disconnect(connectionId, { skipTabHandling: true }).catch((error) => {
        console.warn("[DBX][connection:delete:disconnect-failed]", { connectionId, error });
      });
    }
    clearConnectionMultiSelection();
    showDeleteSelectedConfirm.value = false;
    toast(t("connection.deletedSelected", { count: ids.length }), 2000);
  } catch (e: any) {
    toast(t("connection.saveFailed", { message: e?.message || String(e) }), 5000);
  }
}

function moveSelectedConnectionsToGroup(value: string) {
  const groupId = value === UNGROUPED_GROUP_VALUE ? null : value;
  const ids = selectedConnectionIds.value;
  for (const connectionId of ids) {
    connectionStore.moveConnectionToGroup(connectionId, groupId);
  }
  // The moved connections stay selected otherwise, so the next batch would be
  // moved together with them (issue #5758).
  clearConnectionMultiSelection();
}

function openCreateSelectedGroupDialog() {
  selectedGroupName.value = "";
  showCreateSelectedGroupDialog.value = true;
}

function confirmCreateSelectedGroup() {
  const name = selectedGroupName.value.trim();
  const ids = selectedConnectionIds.value;
  if (!name || ids.length === 0) return;
  const groupId = connectionStore.createConnectionGroup(name);
  for (const connectionId of ids) {
    connectionStore.moveConnectionToGroup(connectionId, groupId);
  }
  clearConnectionMultiSelection();
  showCreateSelectedGroupDialog.value = false;
}

defineExpose({ focusSearch, locateTabInSidebar });
</script>

<template>
  <div :data-app-sidebar="embedded ? undefined : ''" class="app-sidebar-panel h-full shrink-0 relative select-none" :class="{ 'app-sidebar-panel--floating': !classicLayout }" :style="{ width: embedded ? '100%' : sidebarWidth + 'px' }" @keydown="onSidebarKeydown">
    <div class="h-full flex flex-col overflow-hidden">
      <div class="flex-1 min-h-0" data-sidebar-tree-region>
        <ConnectionTree ref="connectionTreeRef" @open-settings="(initialTab) => emit('open-settings', initialTab)" @add-to-ai="(nodes) => emit('add-to-ai', nodes)">
          <template #toolbar="{ items, selectedValues, activeOptions, selectOption }">
            <SidebarToolbar
              :connection-count="allConnectionIds.length"
              :items="[...connectionToolbarItems, ...items, ...connectionTransferItems]"
              :selected-values="selectedValues"
              :active-options="activeOptions"
              :refreshing="refreshingTree"
              :embedded="embedded"
              @new-connection="emit('new-connection')"
              @refresh="refreshTree"
              @collapse="emit('collapse')"
              @action="selectConnectionToolbarAction($event, selectOption)"
            />
            <div v-if="showConnectionMultiSelectToolbar" class="sidebar-multi-selection-toolbar" data-sidebar-selection-toolbar>
              <span class="flex min-w-0 flex-1 items-center">
                <span class="truncate">{{ t("sidebar.connections") }}</span>
                <span class="ml-1.5 truncate" data-connection-selection-count>{{ t("connectionGroup.selectedConnections", { count: selectedConnectionCount }) }}</span>
              </span>

              <LightTooltip :text="t('connectionGroup.createGroup')" side="bottom" :delay="0" :close-delay="0" nowrap>
                <Button variant="ghost" size="icon" class="h-5 w-5" :aria-label="t('connectionGroup.createGroup')" :title="t('connectionGroup.createGroup')" @click="openCreateSelectedGroupDialog">
                  <FolderPlus class="h-3 w-3" />
                </Button>
              </LightTooltip>
              <LightTooltip :text="t('connectionGroup.moveToGroup')" side="bottom" :delay="0" :close-delay="0" nowrap>
                <span class="inline-flex">
                  <LightDropdown
                    model-value=""
                    :items="moveGroupItems"
                    :aria-label="t('connectionGroup.moveToGroup')"
                    :trigger-icon="FolderInput"
                    trigger-class="inline-flex h-5 w-5 items-center justify-center rounded-md outline-none hover:bg-muted hover:text-foreground focus-visible:ring-0"
                    trigger-icon-class="h-3.5 w-3.5"
                    content-class="w-44"
                    :show-trigger-label="false"
                    :show-chevron="false"
                    :highlight-selected="false"
                    check-position="none"
                    align="end"
                    @update:model-value="moveSelectedConnectionsToGroup"
                  />
                </span>
              </LightTooltip>
              <LightTooltip :text="t('contextMenu.deleteSelectedConnections', { count: selectedConnectionCount })" side="bottom" :delay="0" :close-delay="0" nowrap>
                <Button variant="ghost" size="icon" class="h-5 w-5 text-destructive hover:text-destructive" :aria-label="t('contextMenu.deleteSelectedConnections', { count: selectedConnectionCount })" @click="showDeleteSelectedConfirm = true">
                  <Trash2 class="h-3 w-3" />
                </Button>
              </LightTooltip>
              <LightTooltip :text="selectAllLabel" side="bottom" :delay="0" :close-delay="0" nowrap>
                <Button variant="ghost" size="icon" class="h-5 w-5" :aria-label="selectAllLabel" :title="selectAllLabel" @click="toggleAllConnectionsSelected">
                  <component :is="selectAllIcon" class="h-3 w-3" />
                </Button>
              </LightTooltip>
              <LightTooltip :text="t('connectionGroup.exitMultiSelect')" side="bottom" :delay="0" :close-delay="0" nowrap>
                <Button variant="ghost" size="icon" class="h-5 w-5" :aria-label="t('connectionGroup.exitMultiSelect')" :title="t('connectionGroup.exitMultiSelect')" @click="clearConnectionMultiSelection">
                  <X class="h-3 w-3" />
                </Button>
              </LightTooltip>
            </div>
          </template>
        </ConnectionTree>
      </div>
      <PluginShortcutBar v-if="settingsStore.editorSettings.pluginShortcuts.enabled && settingsStore.editorSettings.pluginShortcuts.position === 'sidebar-bottom'" position="sidebar-bottom" />
    </div>
    <div v-if="!embedded" class="panel-resize-handle panel-resize-handle--right" @pointerdown="emit('startResize', $event)" />
    <Dialog v-model:open="showDeleteSelectedConfirm">
      <DialogContent class="sm:max-w-[400px]">
        <DialogHeader>
          <DialogTitle>{{ t("contextMenu.confirmDeleteTitle") }}</DialogTitle>
        </DialogHeader>
        <p class="text-sm text-muted-foreground">
          {{ t("contextMenu.confirmDeleteSelectedMessage", { count: selectedConnectionCount }) }}
        </p>
        <DialogFooter>
          <Button variant="outline" @click="showDeleteSelectedConfirm = false">{{ t("dangerDialog.cancel") }}</Button>
          <Button variant="destructive" @click="confirmDeleteSelectedConnections">{{ t("contextMenu.deleteSelectedConnections", { count: selectedConnectionCount }) }}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
    <Dialog v-model:open="showCreateSelectedGroupDialog">
      <DialogContent class="sm:max-w-[360px]">
        <DialogHeader>
          <DialogTitle>{{ t("connectionGroup.createGroup") }}</DialogTitle>
        </DialogHeader>
        <Input v-model="selectedGroupName" :placeholder="t('connectionGroup.groupNamePlaceholder')" @keydown.enter.prevent="confirmCreateSelectedGroup" />
        <DialogFooter>
          <Button variant="outline" @click="showCreateSelectedGroupDialog = false">{{ t("dangerDialog.cancel") }}</Button>
          <Button :disabled="!selectedGroupName.trim()" @click="confirmCreateSelectedGroup">{{ t("connectionGroup.createGroup") }}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>

<style scoped>
.app-sidebar-panel {
  background: var(--workbench-sidebar, var(--sidebar));
}
.app-sidebar-panel--floating {
  border: 1px solid var(--workbench-border, var(--border));
}
.sidebar-multi-selection-toolbar {
  display: flex;
  align-items: center;
  flex-shrink: 0;
  min-width: 0;
  gap: 3px;
  min-height: 31px;
  padding: 3px 8px 3px 12px;
  border-bottom: 1px solid var(--workbench-border, var(--border));
  background: var(--workbench-surface, var(--muted));
  color: var(--workbench-muted, var(--muted-foreground));
  font-size: 11px;
}
.sidebar-multi-selection-toolbar :deep(button) {
  min-height: 24px;
  min-width: 24px;
}
.sidebar-multi-selection-toolbar :deep(button:focus-visible) {
  outline: 1px solid var(--workbench-accent, var(--ring));
}
</style>
