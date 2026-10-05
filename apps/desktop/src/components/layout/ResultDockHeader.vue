<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { Loader2, Table2 } from "@lucide/vue";
import { hasQueryOutput } from "@/lib/query/queryOutput";
import { isQueryExecutionErrorResult } from "@/lib/query/queryResultError";
import type { QueryTab } from "@/types/database";

const props = defineProps<{ activeTab: QueryTab }>();
const { t } = useI18n();
const busy = computed(() => props.activeTab.isExecuting || props.activeTab.isExplaining);
const state = computed(() => (busy.value ? "running" : (props.activeTab.result && isQueryExecutionErrorResult(props.activeTab.result)) || props.activeTab.explainError || props.activeTab.explainTableError ? "error" : hasQueryOutput(props.activeTab) ? "ready" : "empty"));
const status = computed(() => (state.value === "running" ? t("executionSummary.executing") : state.value === "error" ? t("executionSummary.statuses.error") : t(`workbench.result${state.value === "ready" ? "Ready" : "Empty"}`)));
</script>

<template>
  <header class="query-result-dock-header" :aria-label="t('workbench.results')">
    <div class="result-dock-caption">
      <Table2 class="size-3.5 shrink-0" aria-hidden="true" />
      <span class="result-dock-title">{{ t("workbench.results") }}</span>
      <span class="result-dock-status" role="status" :data-state="state" :title="status" :aria-label="status">
        <Loader2 v-if="busy" class="size-3 animate-spin" aria-hidden="true" />
        <span v-else class="result-dock-status-dot" aria-hidden="true" />
        <span class="result-dock-status-label">{{ status }}</span>
      </span>
    </div>
    <!-- Existing result-run tabs, result-set navigation and actions keep a
         single owner and their original event handlers. -->
    <slot />
  </header>
</template>

<style scoped>
.query-result-dock-header {
  display: flex;
  flex: 0 0 auto;
  min-width: 0;
  height: 32px;
  align-items: center;
  gap: 4px;
  padding-inline: 8px;
  border-bottom: 1px solid var(--workbench-border, var(--border));
  background: var(--workbench-header, var(--muted));
  color: var(--workbench-muted, var(--muted-foreground));
  font-family: var(--workbench-font-ui, inherit);
  container-type: inline-size;
}
.result-dock-caption {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 6px;
  padding-right: 6px;
  font-size: var(--workbench-font-size-ui, 12px);
}
.result-dock-title {
  color: var(--workbench-text, var(--foreground));
  font-weight: var(--workbench-font-weight-heading, 600);
}
.result-dock-status {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: var(--workbench-font-size-caption, 11px);
  font-weight: 400;
  font-variant-numeric: tabular-nums;
}
.result-dock-status-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: currentColor;
  opacity: 0.8;
}
.result-dock-status[data-state="running"] {
  color: var(--workbench-accent, var(--primary));
}
.result-dock-status[data-state="ready"] {
  color: var(--workbench-success, #34834d);
}
.result-dock-status[data-state="error"] {
  color: var(--workbench-danger, var(--destructive));
}
.result-dock-status[data-state="empty"] {
  color: var(--workbench-muted, var(--muted-foreground));
}
@container (max-width: 720px) {
  .result-dock-status-label {
    display: none;
  }
}
@container (max-width: 400px) {
  .result-dock-caption > svg {
    display: none;
  }
}
</style>
