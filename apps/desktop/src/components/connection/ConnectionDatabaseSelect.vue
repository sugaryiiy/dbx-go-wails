<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Check, ChevronDown, Loader2, RefreshCw } from "@lucide/vue";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";

const props = defineProps<{
  placeholder: string;
  scopeKey: string;
  disabled?: boolean;
  loadOptions: () => Promise<string[]>;
}>();
const model = defineModel<string>();
const { t } = useI18n();
const open = ref(false);
const search = ref("");
const names = ref<string[]>([]);
const loading = ref(false);
const loaded = ref(false);
const error = ref("");
let requestId = 0;
const filteredNames = computed(() => names.value.filter((name) => name.toLocaleLowerCase().includes(search.value.trim().toLocaleLowerCase())));

async function load() {
  if (loading.value || props.disabled) return;
  const id = ++requestId;
  loading.value = true;
  error.value = "";
  names.value = [];
  try {
    const result = await props.loadOptions();
    if (id !== requestId) return;
    names.value = [...new Set(result)].sort((a, b) => a.localeCompare(b));
    loaded.value = true;
  } catch (cause) {
    if (id !== requestId) return;
    error.value = cause instanceof Error ? cause.message : String(cause);
    loaded.value = false;
  } finally {
    if (id === requestId) loading.value = false;
  }
}

watch(open, (value) => {
  if (!value) return;
  search.value = "";
  if (!loaded.value) void load();
});
watch(
  () => props.scopeKey,
  () => {
    requestId++;
    open.value = false;
    names.value = [];
    search.value = "";
    error.value = "";
    loading.value = false;
    loaded.value = false;
  },
  { flush: "sync" },
);
onUnmounted(() => requestId++);

function select(name: string) {
  model.value = name;
  open.value = false;
}
</script>

<template>
  <div data-connection-database-select class="min-w-0">
    <Popover v-model:open="open">
      <div class="relative">
        <Input v-model="model" data-connection-database-input class="pr-9" :placeholder="placeholder" :aria-label="t('connection.database')" @keydown.alt.down.prevent="open = !disabled" />
        <PopoverTrigger as-child>
          <Button type="button" variant="ghost" size="icon" class="absolute inset-y-0 right-0 h-8 w-8" :disabled="disabled" :aria-label="t('visibleDatabases.searchPlaceholder')" data-connection-database-trigger>
            <ChevronDown class="h-4 w-4" />
          </Button>
        </PopoverTrigger>
      </div>
      <PopoverContent align="end" class="w-80 max-w-[calc(100vw-2rem)] gap-2 p-2">
        <div class="flex items-center gap-1">
          <Input v-model="search" data-connection-database-search :placeholder="t('visibleDatabases.searchPlaceholder')" :aria-label="t('visibleDatabases.searchPlaceholder')" />
          <Button type="button" variant="ghost" size="icon" class="h-8 w-8 shrink-0" :disabled="loading || disabled" :aria-label="t('common.refresh')" @click="load">
            <RefreshCw class="h-4 w-4" />
          </Button>
        </div>
        <div v-if="loading" role="status" class="flex items-center justify-center gap-2 p-4 text-sm text-muted-foreground"><Loader2 class="h-4 w-4 animate-spin" />{{ t("common.loading") }}</div>
        <div v-else-if="error" role="alert" class="space-y-2 p-2">
          <p class="break-words text-xs text-destructive">{{ t("visibleDatabases.loadFailed", { message: error }) }}</p>
          <Button type="button" size="sm" variant="outline" @click="load">{{ t("common.retry") }}</Button>
        </div>
        <div v-else class="max-h-60 overflow-y-auto">
          <button v-for="name in filteredNames" :key="name" type="button" data-connection-database-option class="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-accent focus-visible:bg-accent focus-visible:outline-none" @click="select(name)">
            <Check class="h-4 w-4 shrink-0" :class="model === name ? 'opacity-100' : 'opacity-0'" />
            <span class="truncate">{{ name }}</span>
          </button>
          <p v-if="!filteredNames.length" class="p-4 text-center text-sm text-muted-foreground">{{ t("ai.noDatabasesFound") }}</p>
        </div>
      </PopoverContent>
    </Popover>
  </div>
</template>
