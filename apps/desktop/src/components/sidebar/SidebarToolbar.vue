<script setup lang="ts">
import { useI18n } from "vue-i18n";
import { Plus, RefreshCw, Ellipsis, ChevronsLeft, LocateFixed } from "@lucide/vue";
import LightDropdown, { type LightDropdownItem } from "@/components/ui/LightDropdown.vue";
import LightTooltip from "@/components/ui/LightTooltip.vue";

withDefaults(
  defineProps<{
    connectionCount: number;
    items: LightDropdownItem[];
    selectedValues?: string[];
    activeOptions?: boolean;
    refreshing?: boolean;
    embedded?: boolean;
  }>(),
  { selectedValues: () => [], activeOptions: false, refreshing: false },
);

const emit = defineEmits<{
  newConnection: [];
  refresh: [];
  collapse: [];
  action: [value: string];
}>();
const { t } = useI18n();
</script>

<template>
  <div class="sidebar-tool-window-header" data-sidebar-tool-window-header>
    <span class="sidebar-tool-window-title">{{ t("connection.database") }}</span>
    <span class="sidebar-tool-window-count" :title="t('sidebar.connections')" data-sidebar-connection-count>{{ connectionCount }}</span>
    <div class="sidebar-tool-window-actions" role="toolbar" :aria-label="t('connection.database')">
      <LightTooltip v-if="!embedded" :text="t('toolbar.newConnection')" side="bottom" :delay="300" nowrap>
        <button type="button" class="sidebar-tool-window-button" data-sidebar-new-connection :title="t('toolbar.newConnection')" :aria-label="t('toolbar.newConnection')" @click="emit('newConnection')"><Plus /></button>
      </LightTooltip>
      <LightTooltip :text="t('contextMenu.refreshChildren')" side="bottom" :delay="300" nowrap>
        <button type="button" class="sidebar-tool-window-button" data-sidebar-refresh :disabled="refreshing" :aria-busy="refreshing" :title="t('contextMenu.refreshChildren')" :aria-label="t('contextMenu.refreshChildren')" @click="emit('refresh')">
          <RefreshCw :class="{ 'animate-spin': refreshing }" />
        </button>
      </LightTooltip>
      <LightTooltip :text="t('sidebar.locateActiveTab')" side="bottom" :delay="300" nowrap>
        <button type="button" class="sidebar-tool-window-button" data-sidebar-locate-active :title="t('sidebar.locateActiveTab')" :aria-label="t('sidebar.locateActiveTab')" @click="emit('action', 'locate-active')"><LocateFixed /></button>
      </LightTooltip>
      <span data-sidebar-more-actions class="sidebar-tool-window-more" :class="{ 'sidebar-tool-window-more--active': activeOptions }">
        <LightDropdown
          model-value=""
          :items="items"
          :selected-values="selectedValues"
          :aria-label="t('toolbar.moreActions')"
          :trigger-title="t('toolbar.moreActions')"
          :trigger-icon="Ellipsis"
          trigger-class="sidebar-tool-window-button"
          trigger-icon-class="h-3.5 w-3.5"
          item-icon-class="h-3.5 w-3.5"
          item-class="!text-xs"
          content-class="w-64"
          :show-trigger-label="false"
          :show-chevron="false"
          :highlight-selected="false"
          check-position="right"
          align="start"
          @update:model-value="emit('action', $event)"
        />
      </span>
      <LightTooltip v-if="!embedded" :text="t('sidebar.collapse')" side="bottom" :delay="300" nowrap>
        <button type="button" class="sidebar-tool-window-button" data-sidebar-collapse :title="t('sidebar.collapse')" :aria-label="t('sidebar.collapse')" @click="emit('collapse')"><ChevronsLeft /></button>
      </LightTooltip>
    </div>
  </div>
</template>

<style scoped>
.sidebar-tool-window-header {
  display: flex;
  align-items: center;
  flex: 0 0 34px;
  min-width: 0;
  gap: 7px;
  padding: 0 7px 0 12px;
  border-bottom: 1px solid var(--workbench-border, var(--border));
  background: var(--workbench-sidebar, var(--sidebar));
  font-size: 12px;
}
.sidebar-tool-window-title {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 600;
}
.sidebar-tool-window-count {
  color: var(--workbench-muted, var(--muted-foreground));
  font-size: 10px;
  font-variant-numeric: tabular-nums;
}
.sidebar-tool-window-actions {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 1px;
  margin-left: auto;
}
.sidebar-tool-window-header :deep(.sidebar-tool-window-button) {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 25px;
  height: 26px;
  padding: 0;
  border: 0;
  border-radius: 4px;
  background: transparent;
  color: var(--workbench-muted, var(--muted-foreground));
}
.sidebar-tool-window-header :deep(.sidebar-tool-window-button svg) {
  width: 14px;
  height: 14px;
}
.sidebar-tool-window-header :deep(.sidebar-tool-window-button:hover:not(:disabled)) {
  background: var(--workbench-surface, var(--muted));
  color: inherit;
}
.sidebar-tool-window-header :deep(.sidebar-tool-window-button:focus-visible) {
  outline: 1px solid var(--workbench-accent, var(--ring));
  outline-offset: -1px;
}
.sidebar-tool-window-header :deep(.sidebar-tool-window-button:disabled) {
  opacity: 0.5;
}
.sidebar-tool-window-more {
  position: relative;
  display: inline-flex;
}
.sidebar-tool-window-more--active::after {
  content: "";
  position: absolute;
  right: 3px;
  top: 3px;
  width: 4px;
  height: 4px;
  border-radius: 50%;
  pointer-events: none;
  background: var(--workbench-accent, var(--primary));
}
</style>
