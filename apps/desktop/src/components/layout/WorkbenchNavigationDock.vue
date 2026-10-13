<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Database, FileCode, History, FolderTree, Plus, FilePlus2, Settings, PanelLeftClose } from "@lucide/vue";
import type { WorkbenchNavigationSection } from "@/composables/useWorkbenchNavigation";

const props = defineProps<{
  section: WorkbenchNavigationSection;
  width: number;
  connectionCount: number;
  canNewQuery: boolean;
  maximized?: boolean;
  settingsNotice?: boolean;
  sqlLibrarySaveFeedbackId?: number;
}>();
const emit = defineEmits<{
  select: [section: WorkbenchNavigationSection];
  resize: [event: PointerEvent];
  collapse: [];
  newConnection: [];
  newQuery: [];
  settings: [];
}>();
const { t } = useI18n();
const initializedSections = ref(new Set<WorkbenchNavigationSection>([props.section]));
watch(
  () => props.section,
  (section) => initializedSections.value.add(section),
);
const sqlLibrarySaveFeedbackActive = ref(false);
let saveFeedbackTimer = 0;
watch(
  () => props.sqlLibrarySaveFeedbackId,
  (id, previous) => {
    if (id == null || id <= 0 || id === previous) return;
    sqlLibrarySaveFeedbackActive.value = true;
    window.clearTimeout(saveFeedbackTimer);
    saveFeedbackTimer = window.setTimeout(() => {
      sqlLibrarySaveFeedbackActive.value = false;
    }, 850);
  },
);
onBeforeUnmount(() => window.clearTimeout(saveFeedbackTimer));
const items = computed(() => [
  { id: "objects" as const, icon: Database, label: t("workbenchLayout.objects") },
  { id: "sqlLibrary" as const, icon: FileCode, label: "SQL" },
  { id: "history" as const, icon: History, label: t("workbenchLayout.history") },
]);

function navigate(event: KeyboardEvent) {
  if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key) || event.altKey || event.metaKey || event.ctrlKey) return;
  const buttons = Array.from((event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>("button"));
  const index = buttons.indexOf(document.activeElement as HTMLButtonElement);
  if (index < 0) return;
  const next = event.key === "Home" ? 0 : event.key === "End" ? buttons.length - 1 : (index + (event.key === "ArrowRight" ? 1 : -1) + buttons.length) % buttons.length;
  event.preventDefault();
  buttons[next]?.focus();
  buttons[next]?.click();
}
</script>

<template>
  <aside data-workbench-navigation data-app-sidebar class="workbench-navigation-dock" :style="{ width: maximized ? 'min(600px, 55vw)' : width + 'px' }" :aria-label="t('workbenchLayout.navigation')">
    <header class="navigation-dock-heading">
      <span>{{ t("workbenchLayout.resources") }}</span>
      <span class="navigation-dock-count">{{ t("workbenchLayout.resourceConnections", { count: connectionCount }) }}</span>
      <button type="button" class="navigation-dock-icon" :aria-label="t('sidebar.collapse')" :title="t('sidebar.collapse')" @click="emit('collapse')"><PanelLeftClose /></button>
    </header>
    <nav class="navigation-dock-switch" :aria-label="t('workbenchLayout.navigation')" @keydown="navigate">
      <button
        v-for="item in items"
        :key="item.id"
        type="button"
        :data-navigation-section="item.id"
        :data-sql-library-trigger="item.id === 'sqlLibrary' ? '' : undefined"
        :class="{ 'save-feedback': item.id === 'sqlLibrary' && sqlLibrarySaveFeedbackActive }"
        :aria-pressed="section === item.id || (item.id === 'sqlLibrary' && section === 'sqlFile')"
        @click="emit('select', item.id)"
      >
        <component :is="item.icon" /><span>{{ item.label }}</span>
      </button>
    </nav>
    <div class="navigation-dock-content">
      <section v-show="section === 'objects'" class="navigation-dock-panel" :aria-label="t('workbenchLayout.objects')"><slot name="objects" /></section>
      <section v-if="initializedSections.has('sqlLibrary')" v-show="section === 'sqlLibrary'" class="navigation-dock-panel" :aria-label="t('sqlLibrary.title')"><slot name="sqlLibrary" /></section>
      <section v-if="initializedSections.has('history')" v-show="section === 'history'" class="navigation-dock-panel" :aria-label="t('history.title')"><slot name="history" /></section>
      <section v-if="initializedSections.has('sqlFile')" v-show="section === 'sqlFile'" class="navigation-dock-panel" :aria-label="t('sqlFileTree.title')"><slot name="sqlFile" /></section>
    </div>
    <footer class="navigation-dock-footer">
      <button type="button" class="navigation-dock-add" @click="emit('newConnection')">
        <Plus /><span>{{ t("toolbar.newConnection") }}</span>
      </button>
      <button type="button" class="navigation-dock-icon" :disabled="!canNewQuery" :aria-label="t('toolbar.newQuery')" :title="t('toolbar.newQuery')" @click="emit('newQuery')"><FilePlus2 /></button>
      <button type="button" class="navigation-dock-icon" :aria-pressed="section === 'sqlFile'" :aria-label="t('sqlFileTree.title')" :title="t('sqlFileTree.title')" @click="emit('select', 'sqlFile')"><FolderTree /></button>
      <button type="button" class="navigation-dock-icon" :class="{ 'has-notice': settingsNotice }" :aria-label="t('settings.title')" :title="t('settings.title')" @click="emit('settings')"><Settings /></button>
    </footer>
    <div v-if="!maximized" class="panel-resize-handle panel-resize-handle--right" @pointerdown="emit('resize', $event)" />
  </aside>
</template>

<style scoped>
.workbench-navigation-dock {
  position: relative;
  display: flex;
  flex-direction: column;
  flex-shrink: 0;
  min-width: 240px;
  max-width: min(55vw, 800px);
  height: 100%;
  min-height: 0;
  background: var(--workbench-sidebar);
  border-right: 1px solid var(--workbench-border);
  color: var(--workbench-text);
}
.navigation-dock-heading {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 44px;
  padding: 8px 10px 5px 16px;
  font-weight: 500;
}
.navigation-dock-count {
  margin-left: auto;
  color: var(--workbench-muted);
  font-size: 11px;
  font-weight: 400;
  white-space: nowrap;
}
.navigation-dock-switch {
  display: flex;
  flex-shrink: 0;
  gap: 3px;
  margin: 2px 12px 10px;
  padding: 3px;
  border-radius: 7px;
  background: var(--workbench-hover);
}
.navigation-dock-switch button {
  display: flex;
  flex: 1;
  min-width: 0;
  justify-content: center;
  align-items: center;
  gap: 6px;
  padding: 6px 4px;
  border-radius: 5px;
  font-size: 12px;
  white-space: nowrap;
  color: var(--workbench-secondary);
}
.navigation-dock-switch button[aria-pressed="true"] {
  background: var(--workbench-surface);
  color: var(--workbench-accent);
  box-shadow: 0 1px 3px color-mix(in srgb, var(--workbench-text) 9%, transparent);
}
.navigation-dock-switch button.save-feedback {
  color: var(--workbench-accent);
  background: var(--workbench-accent-soft);
  animation: navigation-save-pulse 850ms ease-out;
}
@keyframes navigation-save-pulse {
  0%,
  40% {
    box-shadow: 0 0 0 2px var(--workbench-accent);
  }
  100% {
    box-shadow: 0 0 0 0 transparent;
  }
}
@media (prefers-reduced-motion: reduce) {
  .navigation-dock-switch button.save-feedback {
    animation: none;
  }
}
.navigation-dock-switch svg,
.navigation-dock-footer svg,
.navigation-dock-icon svg {
  width: 15px;
  height: 15px;
  flex-shrink: 0;
}
.navigation-dock-content {
  flex: 1;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
}
.navigation-dock-panel {
  width: 100%;
  height: 100%;
  min-width: 0;
  min-height: 0;
}
.navigation-dock-footer {
  display: flex;
  align-items: center;
  gap: 3px;
  flex-shrink: 0;
  min-height: 45px;
  padding: 7px 9px;
  border-top: 1px solid var(--workbench-border);
}
.navigation-dock-add {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  padding: 5px 6px;
  margin-right: auto;
  font-size: 12px;
  color: var(--workbench-secondary);
  border-radius: 5px;
}
.navigation-dock-icon {
  position: relative;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  width: 28px;
  height: 28px;
  color: var(--workbench-muted);
  border-radius: 5px;
}
button:hover:not(:disabled) {
  background: var(--workbench-hover);
  color: var(--workbench-text);
}
button:focus-visible {
  outline: 2px solid var(--workbench-accent);
  outline-offset: -2px;
}
button:disabled {
  opacity: 0.4;
}
.navigation-dock-icon[aria-pressed="true"] {
  background: var(--workbench-accent-soft);
  color: var(--workbench-accent);
}
.has-notice::after {
  content: "";
  position: absolute;
  right: 3px;
  top: 3px;
  width: 5px;
  height: 5px;
  background: var(--workbench-accent);
  border-radius: 50%;
}
</style>
