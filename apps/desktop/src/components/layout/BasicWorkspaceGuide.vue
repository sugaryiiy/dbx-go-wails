<script setup lang="ts">
import { useI18n } from "vue-i18n";
import { ArrowRight, Code2, DatabaseZap, FileSpreadsheet, FolderOpen, History, LoaderCircle, PencilLine, Table2 } from "@lucide/vue";

defineProps<{ sampleWorkspaceAvailable?: boolean; openingSampleWorkspace?: boolean }>();
const emit = defineEmits<{ "open-sample-workspace": []; "show-history": []; "show-sql-library": [] }>();
const { t } = useI18n();
const steps = [
  { key: "connections", icon: DatabaseZap },
  { key: "browse", icon: Table2 },
  { key: "query", icon: Code2 },
  { key: "edit", icon: PencilLine },
  { key: "transfer", icon: FileSpreadsheet },
  { key: "reuse", icon: FolderOpen },
] as const;
</script>

<template>
  <section class="min-w-0" :aria-label="t('welcome.basics.title')">
    <div v-if="sampleWorkspaceAvailable" class="mb-4 flex flex-wrap items-center justify-between gap-4 rounded-xl border border-primary/20 bg-primary/5 p-4">
      <div class="min-w-0 flex-1 basis-64">
        <h2 class="text-sm font-semibold">{{ t("welcome.basics.sampleTitle") }}</h2>
        <p class="mt-1 text-xs leading-5 text-muted-foreground">{{ t("welcome.basics.sampleDescription") }}</p>
      </div>
      <button
        type="button"
        :disabled="openingSampleWorkspace"
        :aria-busy="openingSampleWorkspace"
        class="inline-flex h-9 shrink-0 items-center gap-2 rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90 disabled:cursor-wait disabled:opacity-60"
        @click="emit('open-sample-workspace')"
      >
        <LoaderCircle v-if="openingSampleWorkspace" class="h-4 w-4 animate-spin" />
        <DatabaseZap v-else class="h-4 w-4" />
        {{ openingSampleWorkspace ? t("welcome.basics.sampleOpening") : t("welcome.basics.sampleOpen") }}
        <ArrowRight v-if="!openingSampleWorkspace" class="h-3.5 w-3.5" />
      </button>
    </div>
    <h2 class="mb-3 text-sm font-semibold">{{ t("welcome.basics.title") }}</h2>
    <ol class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
      <li v-for="(step, index) in steps" :key="step.key" class="min-w-0 rounded-xl border bg-muted/15 p-4">
        <div class="flex items-center justify-between">
          <component :is="step.icon" class="h-4 w-4 text-primary" />
          <span class="text-xs tabular-nums text-muted-foreground">{{ String(index + 1).padStart(2, "0") }}</span>
        </div>
        <h3 class="mt-3 text-sm font-medium">{{ t(`welcome.basics.${step.key}`) }}</h3>
        <p class="mt-1 text-xs leading-5 text-muted-foreground">{{ t(`welcome.basics.${step.key}Description`) }}</p>
        <div v-if="step.key === 'reuse'" class="mt-3 flex flex-wrap gap-3 text-xs">
          <button type="button" class="inline-flex items-center gap-1.5 text-primary hover:underline" @click="emit('show-sql-library')"><FolderOpen class="h-3.5 w-3.5" />{{ t("welcome.basics.openLibrary") }}</button>
          <button type="button" class="inline-flex items-center gap-1.5 text-primary hover:underline" @click="emit('show-history')"><History class="h-3.5 w-3.5" />{{ t("history.title") }}</button>
        </div>
      </li>
    </ol>
  </section>
</template>
