<script setup lang="ts">
import { Database, Search, FilePlus2, BookMarked, FolderTree, History, Bot, Settings, Waypoints } from "@lucide/vue";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { formatShortcutTooltip } from "@/lib/editor/shortcutDisplay";

export type WorkbenchToolIcon = "database" | "search" | "newQuery" | "sqlLibrary" | "sqlFile" | "history" | "ai" | "settings" | "mcp";
export interface WorkbenchToolItem {
  id: string;
  label: string;
  icon: WorkbenchToolIcon;
  shortcut?: string;
  active?: boolean;
  disabled?: boolean;
  badge?: number;
  attention?: boolean;
  bottom?: boolean;
}
defineProps<{ items: readonly WorkbenchToolItem[]; label: string; side: "left" | "right" }>();
const emit = defineEmits<{ select: [id: string] }>();
const icons = { database: Database, search: Search, newQuery: FilePlus2, sqlLibrary: BookMarked, sqlFile: FolderTree, history: History, ai: Bot, settings: Settings, mcp: Waypoints };

function navigate(event: KeyboardEvent) {
  if (!['ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key) || event.isComposing || event.altKey || event.metaKey || event.ctrlKey || event.shiftKey) return;
  const rail = event.currentTarget as HTMLElement;
  const buttons = Array.from(rail.querySelectorAll<HTMLButtonElement>('button[data-workbench-tool]:not(:disabled)'));
  const index = buttons.indexOf(document.activeElement as HTMLButtonElement);
  if (index < 0 || !buttons.length) return;
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length;
  event.preventDefault();
  buttons[next]?.focus();
}
</script>

<template>
  <nav class="workbench-tool-rail" :class="`workbench-tool-rail--${side}`" :aria-label="label" @keydown="navigate">
    <template v-for="section in [false, true]" :key="String(section)">
      <div class="workbench-tool-rail-group" :class="{ 'workbench-tool-rail-group--bottom': section }">
        <Tooltip v-for="item in items.filter((entry) => !!entry.bottom === section)" :key="item.id">
          <TooltipTrigger as-child>
            <button
              type="button"
              :data-workbench-tool="item.id"
              :data-sql-library-trigger="item.id === 'sqlLibrary' ? '' : undefined"
              class="workbench-tool-button"
              :class="{ 'is-active': item.active, 'has-attention': item.attention }"
              :aria-label="item.label"
              :aria-pressed="item.active"
              :disabled="item.disabled"
              :title="formatShortcutTooltip(item.label, item.shortcut ?? '')"
              @click="emit('select', item.id)"
            >
              <component :is="icons[item.icon]" class="size-[18px]" aria-hidden="true" />
              <span v-if="item.badge && item.badge > 0" class="workbench-tool-badge">{{ item.badge > 9 ? '9+' : item.badge }}</span>
              <span v-else-if="item.attention" class="workbench-tool-dot" aria-hidden="true" />
            </button>
          </TooltipTrigger>
          <TooltipContent :side="side === 'left' ? 'right' : 'left'">{{ formatShortcutTooltip(item.label, item.shortcut ?? '') }}</TooltipContent>
        </Tooltip>
      </div>
    </template>
  </nav>
</template>

<style scoped>
.workbench-tool-rail { display: flex; flex-direction: column; flex: 0 0 40px; width: 40px; padding: 6px 3px; background: var(--workbench-rail); border-color: var(--workbench-border); color: var(--workbench-muted); }
.workbench-tool-rail--left { border-right: 1px solid var(--workbench-border); }
.workbench-tool-rail--right { border-left: 1px solid var(--workbench-border); }
.workbench-tool-rail-group { display: flex; flex-direction: column; gap: 4px; }
.workbench-tool-rail-group--bottom { margin-top: auto; padding-top: 16px; }
.workbench-tool-button { display: flex; align-items: center; justify-content: center; position: relative; width: 32px; height: 34px; border-radius: 5px; flex-shrink: 0; transition: background-color .12s, color .12s; }
.workbench-tool-button:not(:disabled):hover { background: var(--workbench-hover); color: var(--workbench-text); }
.workbench-tool-button.is-active { background: var(--workbench-accent-soft); color: var(--workbench-accent); }
.workbench-tool-button.is-active::before { content: ''; position: absolute; top: 8px; bottom: 8px; width: 2px; background: var(--workbench-accent); border-radius: 2px; left: -3px; }
.workbench-tool-rail--right .workbench-tool-button.is-active::before { left: auto; right: -3px; }
.workbench-tool-button:focus-visible { outline: 2px solid var(--workbench-accent); outline-offset: -2px; }
.workbench-tool-button:disabled { opacity: .35; cursor: not-allowed; }
.workbench-tool-badge { position: absolute; right: -1px; top: 0; padding: 0 3px; min-width: 13px; height: 13px; border-radius: 4px; font-size: 9px; line-height: 13px; color: var(--workbench-surface); background: var(--workbench-accent); }
.has-attention .workbench-tool-badge, .workbench-tool-dot { background: #c48432; }
.workbench-tool-dot { position: absolute; width: 5px; height: 5px; border-radius: 50%; right: 3px; top: 4px; }
</style>
