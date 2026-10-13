<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { CircleHelp, Loader2 } from "@lucide/vue";
import { useI18n } from "vue-i18n";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import LightTooltip from "@/components/ui/LightTooltip.vue";
import { loadRoutineParameters } from "@/lib/table/routineParameters";
import { acceptsRoutineInput, buildProcedureExecutionSql, buildProcedureExecutionSqlFromValues, type RoutineParameterValue } from "@/lib/table/routineExecutionSql";
import type { DatabaseType } from "@/types/database";

const { t } = useI18n();

const open = defineModel<boolean>("open", { default: false });

const props = defineProps<{
  connectionId: string;
  database: string;
  databaseType?: DatabaseType;
  schema?: string;
  routineName: string;
}>();

const emit = defineEmits<{
  execute: [sql: string];
  openSql: [sql: string];
}>();

const loading = ref(false);
const loadError = ref("");
const parameters = ref<RoutineParameterValue[]>([]);
const sqlDraft = ref("");
const manualSqlDirty = ref(false);
const dialogActive = ref(false);
let loadToken = 0;

const generatedSql = computed(() => {
  if (parameters.value.length) {
    return buildProcedureExecutionSqlFromValues({
      databaseType: props.databaseType,
      schema: props.schema,
      routineName: props.routineName,
      parameters: parameters.value,
    });
  }
  return buildProcedureExecutionSql({
    databaseType: props.databaseType,
    schema: props.schema,
    routineName: props.routineName,
  });
});

const inputParameterCount = computed(() => parameters.value.filter(acceptsRoutineInput).length);
const outputParameterCount = computed(() => parameters.value.filter((parameter) => parameter.mode === "OUT" || parameter.mode === "INOUT").length);
const isXugu = computed(() => props.databaseType === "xugu");
const manualSqlActive = computed(() => manualSqlDirty.value && sqlDraft.value.trim() !== generatedSql.value.trim());
const missingInputParameters = computed(() => parameters.value.filter(isMissingInput));
const canSubmit = computed(() => dialogActive.value && open.value && !loading.value && !!sqlDraft.value.trim() && (manualSqlActive.value || (!loadError.value && missingInputParameters.value.length === 0)));

const executionContext = () => [open.value, props.connectionId, props.database, props.databaseType, props.schema, props.routineName] as const;

watch(
  executionContext,
  () => {
    // Invalidate on close as well as target changes: a response for a closed
    // dialog must never populate its next invocation.
    ++loadToken;
    dialogActive.value = false;
    loading.value = false;
    loadError.value = "";
    manualSqlDirty.value = false;
    parameters.value = [];
    sqlDraft.value = "";
  },
  { flush: "sync" },
);

watch(
  executionContext,
  ([isOpen]) => {
    if (!isOpen || !props.connectionId || !props.database || !props.routineName) return;
    dialogActive.value = true;
    void refreshParameters();
  },
  { immediate: true },
);

onBeforeUnmount(() => {
  dialogActive.value = false;
  ++loadToken;
});

watch(
  generatedSql,
  (sql) => {
    if (dialogActive.value && !manualSqlDirty.value) sqlDraft.value = sql;
  },
  { immediate: true, flush: "sync" },
);

async function refreshParameters() {
  const token = ++loadToken;
  loading.value = true;
  loadError.value = "";
  manualSqlDirty.value = false;
  parameters.value = [];
  sqlDraft.value = generatedSql.value;
  try {
    const loaded = await loadRoutineParameters({
      connectionId: props.connectionId,
      database: props.database,
      databaseType: props.databaseType,
      schema: props.schema,
      routineName: props.routineName,
    });
    if (token !== loadToken || !dialogActive.value) return;
    parameters.value = loaded.map((parameter) => ({
      ...parameter,
      value: "",
      useNull: false,
      useDefault: !!parameter.hasDefault,
    }));
    sqlDraft.value = generatedSql.value;
  } catch (e: any) {
    if (token !== loadToken || !dialogActive.value) return;
    loadError.value = e?.message || String(e);
    sqlDraft.value = generatedSql.value;
  } finally {
    if (token === loadToken) loading.value = false;
  }
}

function onSqlInput(event: Event) {
  if (!dialogActive.value || loading.value) return;
  sqlDraft.value = (event.target as HTMLTextAreaElement).value;
  manualSqlDirty.value = sqlDraft.value.trim() !== generatedSql.value.trim();
}

function resetSqlPreview() {
  manualSqlDirty.value = false;
  sqlDraft.value = generatedSql.value;
}

function close() {
  dialogActive.value = false;
  ++loadToken;
  open.value = false;
}

function openSql() {
  if (!canSubmit.value) return;
  const sql = sqlDraft.value.trim();
  close();
  emit("openSql", sql);
}

function execute() {
  if (!canSubmit.value) return;
  const sql = sqlDraft.value.trim();
  close();
  emit("execute", sql);
}

function canEditParameter(parameter: RoutineParameterValue): boolean {
  return acceptsRoutineInput(parameter);
}

function isMissingInput(parameter: RoutineParameterValue): boolean {
  return acceptsRoutineInput(parameter) && !parameter.useNull && !(parameter.useDefault && parameter.hasDefault) && parameter.value.trim() === "";
}

function displayParameterDefault(parameter: RoutineParameterValue): string {
  if (!parameter.hasDefault) return "-";
  return parameter.defaultValue?.trim() || "DEFAULT";
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[86vh] border border-border !bg-background-solid text-foreground shadow-2xl !backdrop-blur-none sm:max-w-[780px]">
      <DialogHeader>
        <DialogTitle>{{ t("contextMenu.confirmExecuteProcedureTitle") }}</DialogTitle>
      </DialogHeader>

      <div class="grid max-h-[calc(86vh-8rem)] gap-4 overflow-y-auto pr-1">
        <div class="flex min-w-0 items-start justify-between gap-3">
          <div class="min-w-0 space-y-1">
            <p class="truncate text-sm text-muted-foreground">
              {{ t("contextMenu.confirmExecuteProcedureMessage", { name: props.routineName }) }}
            </p>
            <p class="truncate font-mono text-xs text-muted-foreground">
              {{ props.schema ? `${props.schema}.${props.routineName}` : props.routineName }}
            </p>
          </div>
          <div class="flex shrink-0 items-center gap-2">
            <Badge v-if="inputParameterCount" variant="outline">
              {{ t("contextMenu.inputParameters", { count: inputParameterCount }) }}
            </Badge>
            <Badge v-if="outputParameterCount" variant="outline">
              {{ t("contextMenu.outputParameters", { count: outputParameterCount }) }}
            </Badge>
          </div>
        </div>

        <div v-if="loading" class="flex items-center gap-2 rounded-md border bg-muted/30 px-3 py-2 text-sm text-muted-foreground">
          <Loader2 class="h-4 w-4 animate-spin" />
          {{ t("contextMenu.loadingProcedureParameters") }}
        </div>

        <div v-else-if="parameters.length" class="overflow-x-auto rounded-md border bg-background">
          <div :class="isXugu ? 'min-w-[720px]' : 'min-w-[650px]'">
            <div
              class="grid border-b bg-muted px-3 py-2 text-xs font-medium text-muted-foreground"
              :class="isXugu ? 'grid-cols-[minmax(120px,1.2fr)_minmax(96px,1fr)_72px_minmax(150px,1.4fr)_56px_minmax(130px,1.1fr)]' : 'grid-cols-[minmax(120px,1.2fr)_minmax(96px,1fr)_72px_minmax(160px,1.5fr)_64px_86px]'"
            >
              <div>{{ t("contextMenu.parameterName") }}</div>
              <div>{{ t("contextMenu.parameterType") }}</div>
              <div>{{ t("contextMenu.parameterMode") }}</div>
              <div>{{ t("contextMenu.parameterValue") }}</div>
              <div>{{ t("contextMenu.parameterNull") }}</div>
              <div class="flex items-center gap-1">
                {{ t("contextMenu.parameterDefault") }}
                <LightTooltip :text="t('contextMenu.parameterDefaultHint')" side="top" :delay="150">
                  <button type="button" class="inline-flex h-4 w-4 items-center justify-center rounded-full text-muted-foreground hover:bg-background hover:text-foreground" :aria-label="t('contextMenu.parameterDefaultHint')">
                    <CircleHelp class="h-3.5 w-3.5" />
                  </button>
                </LightTooltip>
              </div>
            </div>
            <div
              v-for="parameter in parameters"
              :key="`${parameter.ordinal}:${parameter.name}`"
              :data-routine-parameter="parameter.name"
              class="grid items-center gap-2 border-b px-3 py-2 text-sm last:border-b-0"
              :class="isXugu ? 'grid-cols-[minmax(120px,1.2fr)_minmax(96px,1fr)_72px_minmax(150px,1.4fr)_56px_minmax(130px,1.1fr)]' : 'grid-cols-[minmax(120px,1.2fr)_minmax(96px,1fr)_72px_minmax(160px,1.5fr)_64px_86px]'"
            >
              <div class="min-w-0 truncate font-medium">{{ parameter.name }}</div>
              <div class="min-w-0 truncate text-muted-foreground">{{ parameter.dataType || "-" }}</div>
              <div class="text-xs text-muted-foreground">{{ parameter.mode }}</div>
              <Input
                v-model="parameter.value"
                class="h-8 bg-background font-mono text-xs"
                :aria-label="parameter.name"
                :aria-invalid="!manualSqlActive && isMissingInput(parameter)"
                :disabled="!canEditParameter(parameter) || parameter.useNull || parameter.useDefault"
                :placeholder="canEditParameter(parameter) ? t('contextMenu.parameterValuePlaceholder') : t('contextMenu.outputOnly')"
              />
              <input
                type="checkbox"
                class="h-4 w-4 accent-primary"
                :aria-label="`${parameter.name} ${t('contextMenu.parameterNull')}`"
                :checked="!!parameter.useNull"
                :disabled="!canEditParameter(parameter) || parameter.useDefault"
                @change="(event: Event) => (parameter.useNull = (event.target as HTMLInputElement).checked)"
              />
              <label v-if="isXugu" class="flex min-w-0 items-center gap-2" :title="displayParameterDefault(parameter)">
                <input
                  type="checkbox"
                  class="h-4 w-4 shrink-0 accent-primary"
                  :aria-label="`${parameter.name} ${t('contextMenu.parameterDefault')}`"
                  :checked="!!parameter.useDefault"
                  :disabled="!canEditParameter(parameter) || !parameter.hasDefault || parameter.useNull"
                  @change="(event: Event) => (parameter.useDefault = (event.target as HTMLInputElement).checked)"
                />
                <span class="truncate font-mono text-xs text-muted-foreground">{{ displayParameterDefault(parameter) }}</span>
              </label>
              <input
                v-else
                type="checkbox"
                class="h-4 w-4 accent-primary"
                :aria-label="`${parameter.name} ${t('contextMenu.parameterDefault')}`"
                :checked="!!parameter.useDefault"
                :disabled="!canEditParameter(parameter) || !parameter.hasDefault || parameter.useNull"
                @change="(event: Event) => (parameter.useDefault = (event.target as HTMLInputElement).checked)"
              />
            </div>
          </div>
        </div>

        <p v-else-if="loadError" class="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-800">
          {{ t("contextMenu.procedureParametersUnavailable") }}
        </p>

        <p v-else class="rounded-md border bg-muted/30 px-3 py-2 text-sm text-muted-foreground">
          {{ t("contextMenu.noProcedureParameters") }}
        </p>

        <p v-if="!loading && !manualSqlActive && missingInputParameters.length" role="alert" class="text-sm text-destructive">
          {{ t("contextMenu.procedureParameterValuesRequired", { names: missingInputParameters.map((parameter) => parameter.name).join(", ") }) }}
        </p>

        <div class="grid gap-2">
          <div class="flex items-center justify-between gap-2">
            <div class="text-xs font-medium text-muted-foreground">{{ t("contextMenu.sqlPreview") }}</div>
            <Button v-if="manualSqlDirty" variant="ghost" size="sm" class="h-7 px-2 text-xs" @click="resetSqlPreview">
              {{ t("contextMenu.resetSqlPreview") }}
            </Button>
          </div>
          <textarea
            :value="sqlDraft"
            :disabled="loading"
            :aria-label="t('contextMenu.sqlPreview')"
            class="min-h-28 w-full resize-y rounded-md border border-input bg-background px-3 py-2 font-mono text-xs outline-none transition-colors focus:border-ring focus:ring-1 focus:ring-ring/40 disabled:opacity-50"
            spellcheck="false"
            @input="onSqlInput"
          ></textarea>
          <p v-if="inputParameterCount" class="text-xs text-muted-foreground">{{ t("contextMenu.procedureEmptyStringHint") }}</p>
        </div>
      </div>

      <DialogFooter>
        <Button variant="outline" @click="close">{{ t("dangerDialog.cancel") }}</Button>
        <Button variant="outline" :disabled="!canSubmit" @click="openSql">
          {{ t("contextMenu.openInSqlEditor") }}
        </Button>
        <Button :disabled="!canSubmit" @click="execute">
          {{ t("contextMenu.executeProcedure") }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
