<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { ArrowRight, DatabaseZap, Download, FilePlus2, History, Plus, Search, Settings2, BookMarked, LoaderCircle, Keyboard, Waypoints } from "@lucide/vue";
import DatabaseIcon from "@/components/icons/DatabaseIcon.vue";
import AppLogo from "@/components/icons/AppLogo.vue";
import TruncatedTextTooltip from "@/components/ui/TruncatedTextTooltip.vue";
import { connectionDriverLabel, connectionIconType, connectionRedactedNameLabel, connectionRedactedOptionSubtitle } from "@/lib/connection/connectionPresentation";
import { formatShortcutDisplay } from "@/lib/editor/shortcutDisplay";
import { defaultShortcutKeymap, getShortcutPreset } from "@/lib/editor/shortcutPresets";
import type { ShortcutSettings } from "@/lib/editor/shortcutRegistry";
import type { WelcomePageMode } from "@/stores/settingsStore";
import type { ConnectionConfig } from "@/types/database";

export interface WelcomeSavedSqlHistoryItem {
  id: string;
  name: string;
  connectionName: string;
  database?: string;
  folderName?: string;
  openCount?: number;
}

const props = defineProps<{
  shortcuts?: Partial<ShortcutSettings>;
  connectionStats: { total: number; connected: number; types: number };
  recentConnections: ConnectionConfig[];
  savedSqlHistoryItems: WelcomeSavedSqlHistoryItem[];
  welcomePageMode: WelcomePageMode;
  appVersion: string;
  canNewQuery: boolean;
  sampleWorkspaceAvailable?: boolean;
  openingSampleWorkspace?: boolean;
}>();

const emit = defineEmits<{
  "open-connection-query": [connectionId: string];
  "open-saved-sql": [fileId: string];
  "new-connection": [];
  "new-query": [];
  "show-history": [];
  "show-sql-library": [];
  "open-sample-workspace": [];
  "import-config": [];
  "open-mcp-settings": [];
  "open-settings": [];
}>();

const { t } = useI18n();
function welcomeConnectionSubtitle(connection: ConnectionConfig): string {
  return connectionRedactedOptionSubtitle(connection) || connectionDriverLabel(connection);
}
const activeShortcuts = computed(() => props.shortcuts ?? getShortcutPreset(defaultShortcutKeymap()));
const keyboardItems = computed(() =>
  [
    { label: t("toolbar.newQuery"), shortcut: activeShortcuts.value.newQuery },
    { label: t("settings.shortcutExecuteSql"), shortcut: activeShortcuts.value.executeSql },
    { label: t("settings.shortcutQuickOpen"), shortcut: activeShortcuts.value.quickOpen },
    { label: t("actionSearch.title"), shortcut: activeShortcuts.value.findAction },
  ].filter((item) => item.shortcut),
);
const workflowSteps = ["connections", "browse", "query", "edit", "transfer", "reuse"] as const;
</script>

<template>
  <section class="workbench-start" aria-labelledby="workbench-start-title">
    <div class="workbench-start-content">
      <header class="workbench-start-heading">
        <AppLogo class="size-7 shrink-0" />
        <div class="min-w-0">
          <h1 id="workbench-start-title">{{ t("workbenchLayout.startTitle") }}</h1>
          <p>{{ t("workbenchLayout.startHint") }}</p>
        </div>
        <span class="workbench-start-version">{{ appVersion ? "v" + appVersion : "" }}</span>
      </header>

      <div class="workbench-start-actions" role="group" :aria-label="t('welcome.shortcuts')">
        <button v-if="sampleWorkspaceAvailable" type="button" class="start-action start-action-primary" :disabled="openingSampleWorkspace" :aria-busy="openingSampleWorkspace" @click="emit('open-sample-workspace')">
          <LoaderCircle v-if="openingSampleWorkspace" class="size-4 animate-spin" /><DatabaseZap v-else class="size-4" />{{ openingSampleWorkspace ? t("welcome.basics.sampleOpening") : t("welcome.basics.sampleOpen") }}
        </button>
        <button type="button" class="start-action" @click="emit('new-connection')"><Plus class="size-4" />{{ t("toolbar.newConnection") }}</button>
        <button type="button" class="start-action" :disabled="!canNewQuery" @click="emit('new-query')"><FilePlus2 class="size-4" />{{ t("toolbar.newQuery") }}</button>
        <button type="button" class="start-text-action" @click="emit('import-config')"><Download class="size-3.5" />{{ t("sidebar.import") }}</button>
      </div>

      <template v-if="welcomePageMode === 'workspace'">
        <div class="workbench-start-status" :aria-label="t('workbenchLayout.workspace')">
          <span
            >{{ t("welcome.connections") }} <strong>{{ connectionStats.total }}</strong></span
          >
          <span
            >{{ t("welcome.connected") }} <strong>{{ connectionStats.connected }}</strong></span
          >
          <span
            >{{ t("welcome.databaseTypes") }} <strong>{{ connectionStats.types }}</strong></span
          >
        </div>
        <div class="workbench-start-lists">
          <section class="start-section" :aria-label="t('welcome.quickConnections')">
            <div class="start-section-heading">
              <h2>{{ t("welcome.quickConnections") }}</h2>
              <span>{{ recentConnections.length }}</span>
            </div>
            <div class="start-record-list">
              <button v-for="connection in recentConnections" :key="connection.id" type="button" class="start-record" @click="emit('open-connection-query', connection.id)">
                <DatabaseIcon :db-type="connectionIconType(connection)" class="size-4 shrink-0" />
                <span v-if="connection.color" class="start-connection-color" :style="{ backgroundColor: connection.color }" aria-hidden="true" />
                <div class="min-w-0 flex-1">
                  <TruncatedTextTooltip :text="connectionRedactedNameLabel(connection)" class="start-record-name" /><TruncatedTextTooltip
                    :text="welcomeConnectionSubtitle(connection)"
                    class="start-record-detail"
                    tooltip-class="max-w-[min(42rem,calc(100vw-2rem))] whitespace-pre-wrap break-all px-3 py-2 text-left leading-5"
                  />
                </div>
                <ArrowRight class="size-3.5 shrink-0 text-muted-foreground" />
              </button>
              <p v-if="!recentConnections.length" class="start-empty">{{ t("sidebar.noConnections") }}</p>
            </div>
          </section>
          <section class="start-section" :aria-label="t('welcome.sqlHistory')">
            <div class="start-section-heading">
              <h2>{{ t("welcome.sqlHistory") }}</h2>
              <button type="button" class="start-text-action" @click="emit('show-sql-library')"><BookMarked class="size-3.5" />{{ t("welcome.basics.openLibrary") }}</button>
            </div>
            <div class="start-record-list">
              <button v-for="item in savedSqlHistoryItems" :key="item.id" type="button" class="start-record" @click="emit('open-saved-sql', item.id)">
                <FilePlus2 class="size-4 shrink-0 text-muted-foreground" />
                <div class="min-w-0 flex-1">
                  <span class="start-record-name">{{ item.name }}</span
                  ><span class="start-record-detail"
                    >{{ item.connectionName }}<template v-if="item.database"> · {{ item.database }}</template
                    ><template v-if="item.folderName"> · {{ item.folderName }}</template
                    ><template v-if="item.openCount"> · {{ t("welcome.sqlHistoryOpenCount", { count: item.openCount }) }}</template></span
                  >
                </div>
                <ArrowRight class="size-3.5 shrink-0 text-muted-foreground" />
              </button>
              <p v-if="!savedSqlHistoryItems.length" class="start-empty">{{ t("welcome.sqlHistoryEmpty") }}</p>
            </div>
          </section>
        </div>
      </template>
      <div v-else class="start-intro-note">
        <p>{{ t("welcome.introPrivacyHint") }}</p>
        <button type="button" class="start-text-action" @click="emit('open-settings')"><Settings2 class="size-3.5" />{{ t("welcome.introSettings") }}</button>
      </div>

      <div class="workbench-start-reference">
        <section class="start-section">
          <div class="start-section-heading">
            <h2>{{ t("workbenchLayout.workflow") }}</h2>
          </div>
          <ol class="start-workflow">
            <li v-for="(step, index) in workflowSteps" :key="step">
              <span class="start-step-index">{{ String(index + 1).padStart(2, "0") }}</span>
              <div>
                <span class="start-step-label">{{ t(`welcome.basics.${step}`) }}</span>
                <p>{{ t(`welcome.basics.${step}Description`) }}</p>
              </div>
            </li>
          </ol>
        </section>
        <section class="start-section">
          <div class="start-section-heading">
            <h2><Keyboard class="size-3.5" />{{ t("workbenchLayout.keyboard") }}</h2>
          </div>
          <dl class="start-shortcuts">
            <div v-for="item in keyboardItems" :key="item.label">
              <dt>{{ item.label }}</dt>
              <dd>
                <kbd>{{ formatShortcutDisplay(item.shortcut!) }}</kbd>
              </dd>
            </div>
          </dl>
          <p class="start-tip"><Search class="size-3.5 shrink-0" />{{ t("welcome.tip") }}</p>
        </section>
      </div>

      <footer class="workbench-start-footer">
        <button v-if="welcomePageMode !== 'workspace'" type="button" class="start-text-action" @click="emit('show-sql-library')"><BookMarked class="size-3.5" />{{ t("welcome.basics.openLibrary") }}</button>
        <button type="button" class="start-text-action" @click="emit('show-history')"><History class="size-3.5" />{{ t("history.title") }}</button>
        <button type="button" class="start-text-action" @click="emit('open-mcp-settings')"><Waypoints class="size-3.5" />MCP</button>
        <button type="button" class="start-text-action" @click="emit('open-settings')"><Settings2 class="size-3.5" />{{ t("settings.title") }}</button>
        <span class="flex-1" />
      </footer>
    </div>
  </section>
</template>

<style scoped>
.workbench-start {
  flex: 1;
  min-width: 0;
  min-height: 0;
  overflow: auto;
  background: var(--workbench-surface, var(--background));
  color: var(--workbench-text, var(--foreground));
}
.workbench-start-content {
  width: 100%;
  max-width: 1000px;
  margin: 0 auto;
  padding: 32px 36px 20px;
}
.workbench-start-heading {
  display: flex;
  align-items: center;
  gap: 12px;
  padding-bottom: 20px;
}
.workbench-start-heading h1 {
  margin: 0;
  font-size: 18px;
  line-height: 25px;
  font-weight: 550;
}
.workbench-start-heading p {
  margin: 3px 0 0;
  font-size: 12px;
  color: var(--workbench-muted, var(--muted-foreground));
}
.workbench-start-version {
  margin-left: auto;
  font: 11px var(--font-mono, monospace);
  color: var(--workbench-muted, var(--muted-foreground));
}
.workbench-start-actions {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  padding-bottom: 20px;
}
.start-action {
  display: inline-flex;
  height: 30px;
  align-items: center;
  justify-content: center;
  gap: 6px;
  border: 1px solid var(--workbench-border, var(--border));
  border-radius: 3px;
  padding: 0 10px;
  font-size: 12px;
  background: var(--workbench-chrome, var(--muted));
  cursor: pointer;
}
.start-action-primary {
  border-color: var(--workbench-accent, var(--primary));
  color: var(--workbench-accent, var(--primary));
  background: var(--workbench-accent-soft, var(--accent));
}
.start-action:disabled {
  cursor: default;
  opacity: 0.45;
}
.start-action:not(:disabled):hover,
.start-record:hover {
  background: var(--workbench-hover, var(--accent));
}
.start-text-action {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 11px;
  color: var(--workbench-muted, var(--muted-foreground));
  cursor: pointer;
}
.start-text-action:hover {
  color: var(--workbench-accent, var(--primary));
}
.workbench-start-status {
  display: flex;
  flex-wrap: wrap;
  gap: 18px;
  padding: 10px 0;
  border-top: 1px solid var(--workbench-border, var(--border));
  font-size: 11px;
  color: var(--workbench-muted, var(--muted-foreground));
}
.workbench-start-status strong {
  margin-left: 5px;
  font-weight: 500;
  color: var(--workbench-text, var(--foreground));
}
.workbench-start-lists,
.workbench-start-reference {
  display: grid;
  min-width: 0;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  column-gap: 28px;
}
.workbench-start-reference {
  margin-top: 24px;
}
.start-section {
  min-width: 0;
}
.start-section-heading {
  display: flex;
  min-height: 34px;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  border-bottom: 1px solid var(--workbench-border, var(--border));
  color: var(--workbench-muted, var(--muted-foreground));
  font-size: 11px;
}
.start-section-heading h2 {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0;
  font-size: 12px;
  font-weight: 550;
  color: var(--workbench-text, var(--foreground));
}
.start-record-list {
  max-height: 250px;
  overflow-y: auto;
  padding: 4px 0;
}
.start-record {
  display: flex;
  width: 100%;
  min-width: 0;
  align-items: center;
  gap: 9px;
  padding: 9px 5px;
  border-radius: 2px;
  text-align: left;
  cursor: pointer;
}
.start-record-name,
.start-record-detail {
  display: block;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.start-record-name {
  font-size: 12px;
  line-height: 17px;
  font-weight: 450;
}
.start-record-detail {
  margin-top: 2px;
  font-size: 11px;
  line-height: 15px;
  color: var(--workbench-muted, var(--muted-foreground));
}
.start-connection-color {
  width: 2px;
  height: 25px;
  flex: none;
}
.start-empty {
  margin: 0;
  padding: 20px 4px;
  font-size: 12px;
  color: var(--workbench-muted, var(--muted-foreground));
}
.start-intro-note {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 12px 0;
  border-top: 1px solid var(--workbench-border, var(--border));
  font-size: 12px;
  color: var(--workbench-muted, var(--muted-foreground));
}
.start-workflow {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  list-style: none;
  margin: 0;
  padding: 12px 0 0;
  gap: 12px;
}
.start-workflow li {
  display: flex;
  align-items: baseline;
  gap: 10px;
  min-width: 0;
}
.start-step-index {
  font: 10px var(--font-mono, monospace);
  color: var(--workbench-muted, var(--muted-foreground));
}
.start-step-label {
  font-size: 12px;
}
.start-workflow p {
  margin: 2px 0 0;
  font-size: 11px;
  line-height: 17px;
  color: var(--workbench-muted, var(--muted-foreground));
}
.start-shortcuts {
  margin: 5px 0 0;
}
.start-shortcuts > div {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 9px 0;
  font-size: 12px;
}
.start-shortcuts kbd {
  font-size: 11px;
  color: var(--workbench-muted, var(--muted-foreground));
}
.start-tip {
  display: flex;
  align-items: baseline;
  gap: 6px;
  margin: 12px 0;
  border-top: 1px solid var(--workbench-border, var(--border));
  padding-top: 12px;
  font-size: 11px;
  line-height: 18px;
  color: var(--workbench-muted, var(--muted-foreground));
}
.workbench-start-footer {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 15px;
  border-top: 1px solid var(--workbench-border, var(--border));
  padding-top: 14px;
  margin-top: 24px;
}
.workbench-start button:focus-visible {
  outline: 1px solid var(--workbench-accent, var(--primary));
  outline-offset: 2px;
}
@media (max-width: 720px) {
  .workbench-start-content {
    padding: 22px 20px 18px;
  }
  .workbench-start-lists,
  .workbench-start-reference {
    grid-template-columns: minmax(0, 1fr);
    gap: 20px;
  }
  .workbench-start-reference {
    margin-top: 18px;
  }
}
</style>
