<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Search } from "@lucide/vue";
import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog";
import { filterActionSearchItems, nextEnabledActionIndex, type ActionSearchItem } from "@/lib/app/actionSearch";
import { formatShortcutDisplay } from "@/lib/editor/shortcutDisplay";

const props = defineProps<{ open: boolean; actions: readonly ActionSearchItem[] }>();
const emit = defineEmits<{ "update:open": [value: boolean]; select: [id: string] }>();
const { t } = useI18n();
const id = useId();
const listId = `${id}-actions`;
const hintId = `${id}-hint`;
const inputRef = ref<HTMLInputElement>();
const listRef = ref<HTMLElement>();
const query = ref("");
const selectedIndex = ref(-1);
const filteredActions = computed(() => filterActionSearchItems(props.actions, query.value));
const selectedAction = computed(() => filteredActions.value[selectedIndex.value]);
const optionId = (index: number) => `${id}-action-${index}`;
let returnFocus: HTMLElement | null = null;
let pendingSelection: string | undefined;
let closing = false;

watch(
  () => props.open,
  (open) => {
    if (!open) return;
    returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    query.value = "";
    selectedIndex.value = nextEnabledActionIndex(filteredActions.value, -1, 1);
    pendingSelection = undefined;
    closing = false;
  },
  { immediate: true, flush: "sync" },
);

watch([query, () => filteredActions.value.map(({ id, disabled }) => ({ id, disabled }))], ([nextQuery, items], [previousQuery, previousItems]) => {
  if (nextQuery !== previousQuery) {
    selectedIndex.value = nextEnabledActionIndex(filteredActions.value, -1, 1);
    return;
  }
  // Availability can change while the user navigates (for example, a query
  // finishes). Keep the action identity rather than selecting a different
  // command at the same index, or resetting to the first result.
  const previousIndex = selectedIndex.value;
  const previousId = previousItems[previousIndex]?.id;
  const retainedIndex = items.findIndex((item) => item.id === previousId);
  if (retainedIndex >= 0 && !items[retainedIndex].disabled) {
    selectedIndex.value = retainedIndex;
    return;
  }
  const anchor = retainedIndex >= 0 ? retainedIndex : Math.min(Math.max(previousIndex, 0), items.length - 1);
  for (let distance = 0; distance < items.length; distance += 1) {
    for (const index of distance === 0 ? [anchor] : [anchor + distance, anchor - distance]) {
      if (index >= 0 && index < items.length && !items[index].disabled) {
        selectedIndex.value = index;
        return;
      }
    }
  }
  selectedIndex.value = -1;
});

watch(selectedIndex, async () => {
  await nextTick();
  listRef.value?.querySelector<HTMLElement>('[aria-selected="true"]')?.scrollIntoView?.({ block: "nearest" });
});

function close() {
  if (closing || !props.open) return;
  closing = true;
  emit("update:open", false);
}

function select(action: ActionSearchItem | undefined) {
  if (!action || action.disabled || closing || !props.open) return;
  pendingSelection = action.id;
  close();
}

function onOpenAutoFocus(event: Event) {
  event.preventDefault();
  inputRef.value?.focus();
}

function onCloseAutoFocus(event: Event) {
  // A keyboard-opened dialog has no DialogTrigger. Finish restoring its focus
  // before dispatching an action that may immediately open another dialog.
  event.preventDefault();
  if (props.open) return;
  if (returnFocus?.isConnected) returnFocus.focus({ preventScroll: true });
  const selected = pendingSelection;
  pendingSelection = undefined;
  if (selected !== undefined) emit("select", selected);
}

function onKeyDown(event: KeyboardEvent) {
  if (event.isComposing || event.keyCode === 229 || closing) return;
  if (event.key === "Escape") {
    event.preventDefault();
    close();
    return;
  }
  if (event.target !== inputRef.value) return;
  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    event.preventDefault();
    selectedIndex.value = nextEnabledActionIndex(filteredActions.value, selectedIndex.value, event.key === "ArrowDown" ? 1 : -1);
  } else if (event.key === "Enter") {
    event.preventDefault();
    if (!event.repeat) select(selectedAction.value);
  }
}
</script>

<template>
  <Dialog :open="open" @update:open="(value) => (value ? emit('update:open', true) : close())">
    <DialogContent
      class="max-w-xl gap-0 p-0"
      @open-auto-focus="onOpenAutoFocus"
      @close-auto-focus="onCloseAutoFocus"
      @escape-key-down="
        (event) => {
          if (event.isComposing || event.keyCode === 229) event.preventDefault();
        }
      "
      @keydown.stop="onKeyDown"
    >
      <DialogTitle class="px-4 pt-4 pr-12 text-sm">{{ t("actionSearch.title") }}</DialogTitle>
      <div class="flex items-center gap-2 border-b px-4 py-3">
        <Search class="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <input
          ref="inputRef"
          v-model="query"
          type="text"
          role="combobox"
          autocomplete="off"
          spellcheck="false"
          aria-autocomplete="list"
          :aria-label="t('actionSearch.title')"
          :aria-expanded="open"
          :aria-controls="listId"
          :aria-describedby="hintId"
          :aria-activedescendant="selectedAction ? optionId(selectedIndex) : undefined"
          :placeholder="t('actionSearch.placeholder')"
          class="min-w-0 flex-1 bg-transparent py-1 text-sm outline-none placeholder:text-muted-foreground"
        />
      </div>
      <div :id="listId" ref="listRef" role="listbox" :aria-label="t('actionSearch.title')" class="max-h-[min(50vh,24rem)] overflow-y-auto p-1">
        <div
          v-for="(action, index) in filteredActions"
          :id="optionId(index)"
          :key="action.id"
          :data-action-id="action.id"
          role="option"
          :aria-selected="index === selectedIndex"
          :aria-disabled="!!action.disabled"
          :aria-describedby="action.disabled && action.disabledReason ? `${optionId(index)}-reason` : undefined"
          :class="['flex items-center gap-3 rounded-md px-3 py-2', action.disabled ? 'cursor-not-allowed text-muted-foreground' : 'cursor-pointer', index === selectedIndex ? 'bg-accent text-accent-foreground' : !action.disabled && 'hover:bg-muted/60']"
          @mouseenter="!action.disabled && (selectedIndex = index)"
          @mousedown.prevent
          @click="select(action)"
        >
          <div class="min-w-0 flex-1">
            <div class="text-sm break-words">{{ action.label }}</div>
            <div class="mt-0.5 text-xs text-muted-foreground">{{ action.group }}</div>
            <div v-if="action.disabled && action.disabledReason" :id="`${optionId(index)}-reason`" class="mt-1 text-xs text-muted-foreground">{{ action.disabledReason }}</div>
          </div>
          <kbd v-if="action.shortcut?.trim()" class="shrink-0 rounded border bg-muted/60 px-1.5 py-0.5 text-xs text-muted-foreground">{{ formatShortcutDisplay(action.shortcut) }}</kbd>
        </div>
      </div>
      <p v-if="!filteredActions.length" role="status" class="px-4 py-8 text-center text-sm text-muted-foreground">{{ t("actionSearch.empty") }}</p>
      <p :id="hintId" class="border-t px-4 py-2 text-xs text-muted-foreground">{{ t("actionSearch.hint") }}</p>
    </DialogContent>
  </Dialog>
</template>
