<script setup lang="ts">
import { computed, ref, watch, watchEffect } from "vue";
import { useI18n } from "vue-i18n";
import { Check, ChevronRight, Database, Layers, Shield, Star, Table2, UserRound, X } from "@lucide/vue";
import { Button } from "@/components/ui/button";
import { SearchableSelect } from "@/components/ui/searchable-select";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import TruncatedTextTooltip from "@/components/ui/TruncatedTextTooltip.vue";
import DatabaseIcon from "@/components/icons/DatabaseIcon.vue";
import ConnectionTreeSelect from "@/components/connection/ConnectionTreeSelect.vue";
import ProductionContextBadge from "@/components/common/ProductionContextBadge.vue";
import { useConnectionStore } from "@/stores/connectionStore";
import { catalogDatabaseOptionsKey, databaseAfterCatalogChange, normalizedQueryTabCatalog, queryCatalogSelectorVisible, selectedQueryCatalogName, useDatabaseOptions } from "@/composables/useDatabaseOptions";
import { useSchemaOptions } from "@/composables/useSchemaOptions";
import { connectionIconType } from "@/lib/connection/connectionPresentation";
import { formatDatabaseLabel, isDefaultDatabase } from "@/lib/database/defaultDatabase";
import { isSingleDatabase, supportsClearableQuerySchema } from "@/lib/database/databaseCapabilities";
import { connectionIsDorisFamilyCatalogCapable } from "@/lib/database/databaseFeatureSupport";
import { productionContextForDatabase } from "@/lib/database/productionSafety";
import type { QueryTab, ConnectionConfig } from "@/types/database";

const props = defineProps<{ activeTab: QueryTab; activeConnection?: ConnectionConfig; databaseRequiredSignal?: number }>();
const emit = defineEmits<{
  changeConnection: [connectionId: string];
  changeCatalog: [catalog: string | undefined, database: string];
  changeDatabase: [database: string];
  changeSchema: [schema: string | undefined];
  setDefaultDatabase: [];
  clearDefaultDatabase: [];
}>();
const { t } = useI18n();
const connectionStore = useConnectionStore();
const { databaseOptions, loadingDatabaseOptions, loadDatabaseOptions, catalogOptions, loadingCatalogOptions, loadCatalogOptions, catalogDatabaseOptions, loadingCatalogDatabaseOptions, loadCatalogDatabaseOptions } = useDatabaseOptions();
const { loadSchemaOptions, getSchemaOptionsForDb, isLoadingSchemas, isSchemaAware } = useSchemaOptions();
const activeCatalogs = computed(() => {
  const connection = props.activeConnection;
  return connection ? (catalogOptions.value[connection.id] ?? []) : [];
});
const activeCatalogNames = computed(() => activeCatalogs.value.map((catalog) => catalog.name));
const catalogSelectorAvailable = computed(() => connectionIsDorisFamilyCatalogCapable(props.activeConnection) && queryCatalogSelectorVisible(activeCatalogs.value));
// Context selectors have their own wrapping row, independent of action
// condensation, so changing pane width never removes a target selector.
const showCatalogSelector = computed(() => catalogSelectorAvailable.value);
const activeCatalogValue = computed(() => selectedQueryCatalogName(activeCatalogs.value, props.activeTab.catalog));
const activeCatalogDatabaseKey = computed(() => (props.activeConnection && props.activeTab.catalog ? catalogDatabaseOptionsKey(props.activeConnection.id, props.activeTab.catalog) : ""));
const activeDatabaseOptions = computed(() => {
  const connection = props.activeConnection;
  if (!connection) return [];
  if (props.activeTab.catalog) return catalogDatabaseOptions.value[activeCatalogDatabaseKey.value] ?? [];
  return databaseOptions.value[connection.id] ?? [];
});
const loadingActiveDatabaseOptions = computed(() => {
  const connection = props.activeConnection;
  if (!connection) return false;
  if (props.activeTab.catalog) return loadingCatalogDatabaseOptions.value[activeCatalogDatabaseKey.value] ?? false;
  return loadingDatabaseOptions.value[connection.id] ?? false;
});
const switchingCatalog = ref(false);

const activeDatabaseValue = computed(() => props.activeTab.database || "");
const activeProductionContext = computed(() => productionContextForDatabase(props.activeConnection, props.activeTab.database));
const showConnectionProductionBadge = computed(() => activeProductionContext.value.reason === "connection");
const showDatabaseProductionBadge = computed(() => activeProductionContext.value.reason === "database");
const activeConnectionValue = computed(() => props.activeConnection?.id || "");
const activeSchemaValue = computed(() => props.activeTab.schema || "");
// Salesforce identity badge. A Salesforce tab talks to one live org and every grid
// save is a real REST write, so the signed-in user stays visible next to the
// connection controls. Advisory only: Salesforce enforces the actual permissions.
const isSalesforceTab = computed(() => props.activeConnection?.db_type === "salesforce");
const salesforceIdentity = computed(() => (props.activeConnection ? connectionStore.salesforceCurrentUser(props.activeConnection.id) : null));
const salesforceIdentityLabel = computed(() => {
  const identity = salesforceIdentity.value;
  if (!identity) return "";
  const user = identity.username || identity.name || identity.email;
  const org = identity.orgName || identity.organizationId;
  if (user && org) return `${user} @ ${org}`;
  return user || org;
});
const salesforceIdentityTooltip = computed(() => {
  const identity = salesforceIdentity.value;
  if (!identity) return "";
  const lines = [identity.name || identity.username || identity.email, identity.profileName || t("toolbar.salesforceIdentityUnknownProfile")];
  if (identity.isAdmin === true) lines.push(t("toolbar.salesforceIdentityAdmin"));
  else if (identity.isAdmin === false) lines.push(t("toolbar.salesforceIdentityNonAdmin"));
  else lines.push(t("toolbar.salesforceIdentityUnknownRights"));
  return lines.filter((line) => !!line).join("\n");
});
watch(
  () => [isSalesforceTab.value, props.activeConnection?.id ?? "", connectionStore.connectedIds.has(props.activeConnection?.id ?? "")] as const,
  ([salesforce, connectionId, connected]) => {
    // Never connect on a tab switch: only resolve the identity once the user has
    // established the connection themselves. A failed lookup hides the badge.
    if (!salesforce || !connected || !connectionId) return;
    void connectionStore.loadSalesforceCurrentUser(connectionId);
  },
  { immediate: true },
);
const isSingleDb = computed(() => isSingleDatabase(props.activeConnection?.db_type));
const hasDefaultDatabaseOption = computed(() => activeDatabaseOptions.value.includes(""));
const schemaDatabaseKey = computed(() => props.activeTab.database || (isSingleDb.value ? "_" : ""));
const schemaSelectorAvailable = computed(() => {
  const connection = props.activeConnection;
  return connection && isSchemaAware(connection.id) && (props.activeTab.database || isSingleDb.value || hasDefaultDatabaseOption.value);
});
const showSchemaSelector = computed(() => schemaSelectorAvailable.value);

const activeSchemaOptions = computed(() => {
  const connection = props.activeConnection;
  if (!connection) return [];
  return getSchemaOptionsForDb(connection.id, schemaDatabaseKey.value);
});
const databaseRequiredVisible = ref(false);

watch(
  () => props.databaseRequiredSignal,
  (signal) => {
    if (!signal) return;
    databaseRequiredVisible.value = false;
    requestAnimationFrame(() => {
      databaseRequiredVisible.value = true;
    });
  },
);

watch(activeDatabaseValue, (database) => {
  if (database) databaseRequiredVisible.value = false;
});

watchEffect(() => {
  const connection = props.activeConnection;
  if (connection && showSchemaSelector.value) {
    loadSchemaOptions(connection.id, schemaDatabaseKey.value).catch(() => {});
  }
});
watchEffect(() => {
  const connection = props.activeConnection;
  if (!connection || !connectionIsDorisFamilyCatalogCapable(connection)) return;
  void loadCatalogOptions(connection.id).catch(() => {});
});

watchEffect(() => {
  const connection = props.activeConnection;
  const catalog = props.activeTab.catalog;
  if (!connection || !catalog) return;
  void loadCatalogDatabaseOptions(connection.id, catalog).catch(() => {});
});

const isActiveDatabaseDefault = computed(() => isDefaultDatabase(props.activeConnection, activeDatabaseValue.value));

function databaseDisplayName(database: string): string {
  return formatDatabaseLabel(props.activeConnection, database, {
    defaultDatabase: t("editor.defaultDatabase"),
    noDatabase: t("editor.noDatabase"),
  });
}

function databaseOptionIsProduction(database: string): boolean {
  if (!database || props.activeConnection?.is_production) return false;
  return productionContextForDatabase(props.activeConnection, database).reason === "database";
}

async function changeCatalog(selectedCatalog: string) {
  const connection = props.activeConnection;
  if (!connection) return;
  switchingCatalog.value = true;
  try {
    const catalog = normalizedQueryTabCatalog(activeCatalogs.value, selectedCatalog);
    const databases = catalog ? await loadCatalogDatabaseOptions(connection.id, selectedCatalog) : await loadDatabaseOptions(connection.id).then(() => databaseOptions.value[connection.id] ?? []);
    emit("changeCatalog", catalog, databaseAfterCatalogChange(props.activeTab.database, databases));
  } finally {
    switchingCatalog.value = false;
  }
}
</script>

<template>
  <nav class="query-workspace-context" :aria-label="t('workbench.queryContext')">
    <div class="query-context-path">
      <div class="workbench-query-selector flex shrink-0 items-center gap-1">
        <span v-if="activeConnection?.color" class="h-4 w-1 rounded-full shrink-0" :style="{ backgroundColor: activeConnection.color }" />
        <ConnectionTreeSelect
          :model-value="activeConnectionValue"
          :connections="connectionStore.connections"
          :layout="connectionStore.sidebarLayout"
          :placeholder="t('editor.selectConnection')"
          :search-placeholder="t('editor.searchConnection')"
          :empty-text="t('grid.noSearchResults')"
          trigger-class="workbench-context-trigger workbench-context-connection-trigger"
          trigger-icon-class="h-3 w-3"
          list-class="w-96 max-w-[calc(100vw-2rem)]"
          @update:model-value="(connectionId) => emit('changeConnection', connectionId)"
        >
          <template #trigger-label="{ label }">
            <div v-if="activeConnection" class="flex min-w-0 items-center gap-1.5">
              <DatabaseIcon :db-type="connectionIconType(activeConnection)" class="h-3.5 w-3.5 shrink-0" />
              <span class="query-context-connection truncate">{{ label }}</span>
              <ProductionContextBadge v-if="showConnectionProductionBadge" compact />
            </div>
            <span v-else class="truncate text-muted-foreground">{{ t("editor.selectConnection") }}</span>
          </template>
        </ConnectionTreeSelect>
      </div>
      <div v-if="showCatalogSelector" class="workbench-query-selector flex shrink-0 items-center gap-1">
        <ChevronRight class="query-context-separator" aria-hidden="true" />
        <SearchableSelect
          :model-value="activeCatalogValue"
          :options="activeCatalogNames"
          :placeholder="t('editor.selectCatalog')"
          :search-placeholder="t('editor.searchCatalog')"
          :empty-text="t('grid.noSearchResults')"
          :loading-text="t('common.loading')"
          :loading="loadingCatalogOptions[activeConnection?.id || ''] || switchingCatalog"
          trigger-variant="ghost"
          trigger-class="workbench-context-trigger gap-1.5"
          trigger-icon-class="h-3 w-3"
          @update:model-value="changeCatalog"
          @update:open="
            (open: boolean) => {
              if (open && activeConnection) loadCatalogOptions(activeConnection.id).catch(() => {});
            }
          "
        >
          <template #trigger-label="{ label, loading }">
            <Layers class="h-3.5 w-3.5 shrink-0" />
            <span class="truncate" :class="{ 'query-context-identifier': !loading }">{{ loading ? t("common.loading") : label }}</span>
          </template>
        </SearchableSelect>
      </div>
      <div
        v-if="
          activeConnection?.db_type !== 'elasticsearch' &&
          activeConnection?.db_type !== 'easysearch' &&
          activeConnection?.db_type !== 'meilisearch' &&
          activeConnection?.db_type !== 'solr' &&
          activeConnection?.db_type !== 'qdrant' &&
          activeConnection?.db_type !== 'milvus' &&
          activeConnection?.db_type !== 'weaviate' &&
          activeConnection?.db_type !== 'chromadb' &&
          activeConnection?.db_type !== 'zookeeper' &&
          activeConnection?.db_type !== 'consul' &&
          !isSingleDb
        "
        class="workbench-query-selector flex shrink-0 items-center gap-1"
        :class="{ 'database-required-prompt': databaseRequiredVisible }"
      >
        <ChevronRight class="query-context-separator" aria-hidden="true" />
        <SearchableSelect
          :model-value="activeDatabaseValue"
          :options="activeDatabaseOptions.length ? activeDatabaseOptions : activeDatabaseValue ? [activeDatabaseValue] : []"
          :placeholder="t('editor.selectDatabase')"
          :search-placeholder="t('editor.searchDatabase')"
          :empty-text="t('grid.noSearchResults')"
          :loading-text="t('common.loading')"
          :loading="loadingActiveDatabaseOptions"
          :display-name="databaseDisplayName"
          trigger-variant="ghost"
          trigger-class="workbench-context-trigger gap-1.5"
          trigger-icon-class="h-3 w-3"
          @update:model-value="(database) => emit('changeDatabase', database)"
          @update:open="
            (open: boolean) => {
              if (!open || !activeConnection) return;
              if (activeTab.catalog) loadCatalogDatabaseOptions(activeConnection.id, activeTab.catalog).catch(() => {});
              else loadDatabaseOptions(activeConnection.id).catch(() => {});
            }
          "
        >
          <template #trigger-label="{ label, loading }">
            <Database class="h-3.5 w-3.5 shrink-0" />
            <span class="truncate" :class="{ 'query-context-identifier': !loading }">{{ loading ? t("common.loading") : label }}</span>
            <ProductionContextBadge v-if="showDatabaseProductionBadge" compact />
          </template>
          <template #option-label="{ option, label }">
            <div class="flex min-w-0 flex-1 items-center gap-1.5">
              <TruncatedTextTooltip :text="label" class="query-context-identifier min-w-0 flex-1" side="left" :side-offset="8" />
              <ProductionContextBadge v-if="databaseOptionIsProduction(option)" compact />
            </div>
          </template>
        </SearchableSelect>
        <Tooltip v-if="activeDatabaseValue && !isSingleDb">
          <TooltipTrigger as-child>
            <Button variant="ghost" size="icon" class="h-6 w-6 text-muted-foreground hover:text-foreground" :aria-label="t('editor.clearDatabase')" @click="emit('changeDatabase', '')">
              <X class="h-3.5 w-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ t("editor.clearDatabase") }}</TooltipContent>
        </Tooltip>
        <Button
          v-if="activeDatabaseValue && !activeTab.catalog"
          variant="ghost"
          size="sm"
          class="h-6 w-6 shrink-0 p-0 text-[11px]"
          :aria-label="isActiveDatabaseDefault ? t('editor.defaultDatabase') : t('editor.setDefaultDatabase')"
          :title="isActiveDatabaseDefault ? t('editor.defaultDatabase') : t('editor.setDefaultDatabase')"
          @click="isActiveDatabaseDefault ? emit('clearDefaultDatabase') : emit('setDefaultDatabase')"
        >
          <Check v-if="isActiveDatabaseDefault" class="query-context-default h-3 w-3" />
          <Star v-else class="h-3 w-3" />
        </Button>
      </div>
      <div v-if="showSchemaSelector" class="workbench-query-selector flex shrink-0 items-center gap-1">
        <ChevronRight class="query-context-separator" aria-hidden="true" />
        <SearchableSelect
          :model-value="activeSchemaValue"
          :options="activeSchemaOptions.length ? activeSchemaOptions : activeSchemaValue ? [activeSchemaValue] : []"
          :placeholder="t('editor.selectSchema')"
          :search-placeholder="t('editor.searchSchema')"
          :empty-text="t('grid.noSearchResults')"
          :loading-text="t('common.loading')"
          :loading="!!activeConnection && isLoadingSchemas(activeConnection.id, schemaDatabaseKey)"
          :clear-selected-option="supportsClearableQuerySchema(activeConnection?.db_type)"
          trigger-variant="ghost"
          trigger-class="workbench-context-trigger gap-1.5"
          trigger-icon-class="h-3 w-3"
          @update:model-value="(schema) => emit('changeSchema', schema || undefined)"
          @update:open="
            (open: boolean) => {
              if (open && activeConnection) loadSchemaOptions(activeConnection.id, schemaDatabaseKey).catch(() => {});
            }
          "
        >
          <template #trigger-label="{ label, loading }">
            <Layers class="h-3.5 w-3.5 shrink-0" />
            <span class="truncate" :class="{ 'query-context-identifier': !loading }">{{ loading ? t("common.loading") : label }}</span>
          </template>
          <template #option-label="{ label }">
            <TruncatedTextTooltip :text="label" class="query-context-identifier min-w-0 flex-1" side="left" :side-offset="8" />
          </template>
        </SearchableSelect>
      </div>
    </div>
    <div v-if="activeTab.mode === 'data' && activeTab.tableMeta" class="query-context-meta ml-2 inline-flex shrink-0 items-center gap-1 rounded border border-border bg-muted/30 px-2 py-0.5 tabular-nums">
      <Table2 class="h-3.5 w-3.5 shrink-0" />
      <span class="truncate">{{ activeTab.tableMeta.columns.length }} {{ t("tree.columns") }}</span>
    </div>
    <div v-if="salesforceIdentity" data-testid="salesforce-identity-badge" class="query-context-identity ml-2 inline-flex max-w-[20rem] shrink-0 items-center gap-1 rounded border border-border bg-muted/30 px-2 py-0.5" :title="salesforceIdentityTooltip">
      <Shield v-if="salesforceIdentity.isAdmin === true" class="query-context-warning h-3.5 w-3.5 shrink-0" />
      <UserRound v-else class="h-3.5 w-3.5 shrink-0" />
      <span class="truncate">{{ salesforceIdentityLabel }}</span>
    </div>
  </nav>
</template>

<style scoped>
.query-workspace-context {
  display: flex;
  flex: 0 0 auto;
  min-width: 0;
  min-height: 32px;
  align-items: center;
  flex-wrap: wrap;
  gap: 2px 8px;
  padding: 2px 8px;
  border-bottom: 1px solid var(--workbench-border, var(--border));
  background: var(--workbench-surface, var(--background));
  color: var(--workbench-secondary, var(--muted-foreground));
  font-family: var(--workbench-font-ui, inherit);
  font-size: var(--workbench-font-size-ui, 12px);
  font-weight: 400;
}
.query-context-path {
  display: flex;
  min-width: 0;
  flex: 1 1 auto;
  flex-wrap: wrap;
  align-items: center;
  gap: 2px;
}
.workbench-query-selector {
  min-width: 0;
  max-width: 100%;
}
.query-context-separator {
  width: 12px;
  height: 12px;
  flex-shrink: 0;
  color: var(--workbench-muted, var(--muted-foreground));
}
:deep(.workbench-context-trigger) {
  min-width: 0;
  max-width: 200px;
  height: 26px;
  padding: 0 5px;
  border: 0;
  border-radius: 3px;
  box-shadow: none;
  background: transparent;
  color: var(--workbench-secondary, var(--muted-foreground));
  font-family: var(--workbench-font-ui, inherit);
  font-size: var(--workbench-font-size-ui, 12px);
  font-weight: 400;
}
:deep(.workbench-context-trigger:hover) {
  background: var(--workbench-hover, var(--accent));
  color: var(--workbench-text, var(--foreground));
}
.query-context-connection {
  color: var(--workbench-text, var(--foreground));
  font-weight: var(--workbench-font-weight-heading, 600);
}
.query-context-identifier {
  font-family: var(--workbench-font-mono, monospace);
  font-weight: 400;
}
.query-context-meta {
  color: var(--workbench-muted, var(--muted-foreground));
  font-size: var(--workbench-font-size-caption, 11px);
  font-weight: 400;
}
.query-context-identity {
  color: var(--workbench-secondary, var(--muted-foreground));
  font-weight: 500;
}
.query-context-default {
  color: var(--workbench-accent, var(--primary));
}
.query-context-warning {
  color: var(--workbench-warning, #a15c10);
}
.database-required-prompt {
  color: var(--workbench-danger, var(--destructive));
  animation: database-required-shake 420ms ease;
}
.database-required-prompt :deep(button) {
  color: var(--workbench-danger, var(--destructive));
  background: color-mix(in oklch, var(--workbench-danger, var(--destructive)) 10%, transparent);
}
@keyframes database-required-shake {
  0%,
  100% {
    transform: translateX(0);
  }
  12%,
  36%,
  60% {
    transform: translateX(-3px);
  }
  24%,
  48%,
  72% {
    transform: translateX(3px);
  }
}
</style>
