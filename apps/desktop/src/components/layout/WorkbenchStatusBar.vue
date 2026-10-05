<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { Database, LoaderCircle, Waypoints, ChevronRight } from "@lucide/vue";

const props = defineProps<{
  connectionName?: string;
  databaseName?: string;
  driverLabel?: string;
  connected: boolean;
  connectedCount: number;
  runningCount: number;
  cancelling?: boolean;
  position?: { line: number; column: number };
}>();
const emit = defineEmits<{ "open-mcp": [] }>();
const { t } = useI18n();
const status = computed(() => props.cancelling ? t('workbenchLayout.cancelling') : props.runningCount ? t('workbenchLayout.running', { count: props.runningCount }) : t('workbenchLayout.ready'));
</script>

<template>
  <footer class="workbench-status-bar" :aria-label="t('workbenchLayout.statusBar')">
    <div class="workbench-status-operation" :data-state="cancelling ? 'cancelling' : runningCount ? 'running' : 'ready'" role="status">
      <LoaderCircle v-if="runningCount || cancelling" class="size-3 animate-spin" aria-hidden="true" />
      <span v-else class="workbench-status-indicator" aria-hidden="true" />
      <span>{{ status }}</span>
    </div>
    <span class="workbench-status-divider" aria-hidden="true" />
    <div v-if="connectionName" class="workbench-status-context" :title="[connectionName, databaseName].filter(Boolean).join(' / ')">
      <Database class="size-3 shrink-0" :class="{ 'is-connected': connected }" aria-hidden="true" />
      <span class="truncate">{{ connectionName }}</span>
      <template v-if="databaseName"><ChevronRight class="size-3 shrink-0 opacity-50" aria-hidden="true" /><span class="truncate">{{ databaseName }}</span></template>
      <span class="workbench-status-connection-state" :class="{ 'is-connected': connected }">{{ t(connected ? 'workbenchLayout.connected' : 'workbenchLayout.disconnected') }}</span>
    </div>
    <span v-else class="truncate">{{ t('workbenchLayout.noConnection') }}</span>
    <div class="workbench-status-spacer" />
    <span class="workbench-status-count">{{ t('sidebar.activeConnectionsStatus', { count: connectedCount }) }}</span>
    <span v-if="position" class="workbench-status-position">{{ t('workbenchLayout.position', position) }}</span>
    <span v-if="driverLabel" class="workbench-status-driver">{{ driverLabel }}</span>
    <button type="button" class="workbench-status-mcp" :title="t('settings.openMcpSettings')" :aria-label="t('settings.openMcpSettings')" @click="emit('open-mcp')"><Waypoints class="size-3" aria-hidden="true" /> MCP</button>
  </footer>
</template>

<style scoped>
.workbench-status-bar { display: flex; align-items: center; gap: 12px; flex: 0 0 25px; height: 25px; min-width: 0; padding: 0 10px; border-top: 1px solid var(--workbench-border); background: var(--workbench-status); color: var(--workbench-muted); font-size: var(--workbench-font-size-caption, 11px); font-variant-numeric: tabular-nums; }
.workbench-status-operation, .workbench-status-context, .workbench-status-mcp { display: flex; align-items: center; gap: 6px; min-width: 0; }
.workbench-status-operation, .workbench-status-position, .workbench-status-driver, .workbench-status-count { flex-shrink: 0; white-space: nowrap; }
.workbench-status-context { max-width: 42%; color: var(--workbench-secondary); }
.workbench-status-spacer { flex: 1; }
.workbench-status-divider { height: 11px; width: 1px; background: var(--workbench-border); }
.workbench-status-indicator { width: 5px; height: 5px; border-radius: 50%; background: currentColor; }
.is-connected { color: var(--workbench-success); }
.workbench-status-operation[data-state="running"] { color: var(--workbench-accent); }
.workbench-status-operation[data-state="cancelling"] { color: var(--workbench-warning); }
.workbench-status-position, .workbench-status-driver { font-family: var(--workbench-font-mono); }
.workbench-status-connection-state { margin-left: 3px; font-size: var(--workbench-font-size-caption, 11px); white-space: nowrap; }
.workbench-status-mcp { align-self: stretch; padding: 0 7px; margin-right: -6px; border-radius: 3px; }
.workbench-status-mcp:hover { background: var(--workbench-hover); color: var(--workbench-text); }
.workbench-status-mcp:focus-visible { outline: 1px solid var(--workbench-accent); outline-offset: -2px; }
@media (max-width: 1050px) { .workbench-status-count, .workbench-status-connection-state { display: none; } .workbench-status-context { max-width: 46%; } }
</style>
