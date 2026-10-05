<script setup lang="ts">
import { computed, useId } from "vue";
import { useI18n } from "vue-i18n";
import { RotateCcw } from "@lucide/vue";
import { Button } from "@/components/ui/button";
import { SearchableSelect } from "@/components/ui/searchable-select";
import { DEFAULT_DATA_GRID_FONT_FAMILY, DEFAULT_MONO_FONT_FAMILY, DEFAULT_UI_FONT_FAMILY, SYSTEM_UI_FONT_FAMILY, normalizeCustomFontFamilyInput, readableFontFamily } from "@/lib/app/appFonts";
import { buildFontFamilyOptions, displayFontFamily, isPresetFontFamily } from "@/lib/app/fontFamilyOptions";

const props = defineProps<{
  uiFontFamily: string;
  editorFontFamily: string;
  tableFontFamily: string;
  editorFontSize: number;
  systemFonts: string[];
  loading: boolean;
}>();

const emit = defineEmits<{
  "update:uiFontFamily": [value: string];
  "update:editorFontFamily": [value: string];
  "update:tableFontFamily": [value: string];
  "update:editorFontSize": [value: number];
  loadFonts: [];
  reset: [];
}>();

const { t } = useI18n();
const panelId = useId();
type FontTarget = "ui" | "editor" | "table";

const fontFields = computed(() => [
  {
    id: "ui" as const,
    title: t("settings.fontSettingsUiTitle"),
    description: t("settings.fontSettingsUiDescription"),
    value: props.uiFontFamily || DEFAULT_UI_FONT_FAMILY,
    options: buildFontFamilyOptions(props.systemFonts, [props.uiFontFamily], [DEFAULT_UI_FONT_FAMILY, SYSTEM_UI_FONT_FAMILY]),
  },
  {
    id: "editor" as const,
    title: t("settings.fontSettingsEditorTitle"),
    description: t("settings.fontSettingsEditorDescription"),
    value: props.editorFontFamily || DEFAULT_MONO_FONT_FAMILY,
    options: buildFontFamilyOptions(props.systemFonts, [props.editorFontFamily]),
  },
  {
    id: "table" as const,
    title: t("settings.fontSettingsTableTitle"),
    description: t("settings.fontSettingsTableDescription"),
    value: props.tableFontFamily || DEFAULT_DATA_GRID_FONT_FAMILY,
    options: buildFontFamilyOptions(props.systemFonts, [props.tableFontFamily], [DEFAULT_DATA_GRID_FONT_FAMILY]),
  },
]);

function fontLabel(value: string): string {
  if (value === DEFAULT_UI_FONT_FAMILY) return t("settings.uiFontAppDefault");
  if (value === SYSTEM_UI_FONT_FAMILY) return t("settings.uiFontSystemDefault");
  if (value === DEFAULT_DATA_GRID_FONT_FAMILY) return t("settings.fontSettingsTableDefault");
  return displayFontFamily(value);
}

function updateFont(target: FontTarget, value: string) {
  if (target === "ui") emit("update:uiFontFamily", value);
  else if (target === "editor") emit("update:editorFontFamily", value);
  else emit("update:tableFontFamily", value);
}

function loadFontsOnOpen(open: boolean) {
  if (open && !props.loading) emit("loadFonts");
}

function optionStyle(value: string, selectedValue: string) {
  // Loading hundreds of installed font faces just to render the menu is slow;
  // preview the selected face and known presets, then show any choice below.
  return isPresetFontFamily(value) || value === selectedValue || value === DEFAULT_UI_FONT_FAMILY || value === SYSTEM_UI_FONT_FAMILY || value === DEFAULT_DATA_GRID_FONT_FAMILY ? { fontFamily: value } : undefined;
}

function updateEditorFontSize(event: Event) {
  const value = Number((event.currentTarget as HTMLInputElement).value);
  if (Number.isFinite(value)) emit("update:editorFontSize", Math.min(24, Math.max(10, Math.round(value))));
}
</script>

<template>
  <section class="font-settings-panel" :aria-labelledby="`${panelId}-title`" :aria-describedby="`${panelId}-description`">
    <header class="font-settings-header">
      <div class="min-w-0 space-y-1">
        <h3 :id="`${panelId}-title`" class="font-settings-title">{{ t("settings.fontSettingsTitle") }}</h3>
        <p :id="`${panelId}-description`" class="font-settings-description">{{ t("settings.fontSettingsDescription") }}</p>
      </div>
      <Button type="button" variant="outline" size="sm" class="shrink-0" :title="t('settings.fontSettingsResetHint')" @click="emit('reset')">
        <RotateCcw class="size-3.5" aria-hidden="true" />
        {{ t("settings.fontSettingsReset") }}
      </Button>
    </header>

    <div class="font-settings-rows">
      <div v-for="field in fontFields" :key="field.id" class="font-settings-row">
        <div class="font-settings-controls">
          <div class="space-y-1">
            <h4 :id="`${panelId}-${field.id}-label`" class="font-settings-label">{{ field.title }}</h4>
            <p class="font-settings-description">{{ field.description }}</p>
          </div>
          <div role="group" :aria-labelledby="`${panelId}-${field.id}-label`">
            <SearchableSelect
              :model-value="field.value"
              :options="field.options"
              :placeholder="t('settings.selectFont')"
              :search-placeholder="t('settings.fontSettingsSearch')"
              :empty-text="t('settings.noFontsFound')"
              :loading-text="t('settings.loadingFonts')"
              :loading="loading"
              allow-custom
              search-by-label-only
              :display-name="fontLabel"
              :normalize-custom="normalizeCustomFontFamilyInput"
              trigger-class="w-full text-xs"
              trigger-icon-class="size-3.5 text-muted-foreground"
              content-class="w-[var(--reka-popover-trigger-width)] min-w-[260px]"
              list-class="w-full min-w-0"
              @update:model-value="updateFont(field.id, $event)"
              @update:open="loadFontsOnOpen"
            >
              <template #trigger-label="{ label, loading: fontsLoading }">
                <span class="truncate" :style="{ fontFamily: field.value }">{{ fontsLoading ? t("settings.loadingFonts") : label }}</span>
              </template>
              <template #option-label="{ option, label }">
                <span class="truncate" :style="optionStyle(option, field.value)">{{ label }}</span>
              </template>
              <template #custom-option-label="{ value }">
                <span class="truncate" :style="{ fontFamily: value }">{{ t("settings.useCustomFont", { font: readableFontFamily(value) }) }}</span>
              </template>
            </SearchableSelect>
          </div>

          <div v-if="field.id === 'editor'" class="font-settings-size">
            <label :for="`${panelId}-editor-size`">{{ t("settings.fontSettingsEditorSize") }}</label>
            <input :id="`${panelId}-editor-size`" type="range" min="10" max="24" step="1" :value="editorFontSize" :aria-valuetext="`${editorFontSize} px`" @input="updateEditorFontSize" />
            <output :for="`${panelId}-editor-size`" class="font-settings-size-value">{{ editorFontSize }} px</output>
          </div>
        </div>

        <div class="font-settings-preview" role="group" :aria-label="t('settings.fontSettingsPreviewFor', { target: field.title })">
          <span class="font-settings-preview-label">{{ t("settings.fontSettingsPreview") }}</span>
          <div v-if="field.id === 'ui'" class="font-settings-ui-sample" :style="{ fontFamily: field.value }">
            <p class="font-settings-ui-sample-title">{{ t("settings.fontSettingsUiSample") }}</p>
            <p>{{ t("settings.fontSettingsUiSampleDetail") }}</p>
            <p class="font-settings-sample-numbers">Aa Bb Cc · 0123456789</p>
          </div>
          <pre v-else-if="field.id === 'editor'" class="font-settings-sql-sample" :style="{ fontFamily: field.value, fontSize: `${editorFontSize}px` }"><span class="font-settings-sql-keyword">select</span> id, name, total
<span class="font-settings-sql-keyword">from</span> orders <span class="font-settings-sql-comment">-- 订单 Orders</span>
<span class="font-settings-sql-keyword">where</span> total &gt;= 1234.50;</pre>
          <div v-else class="font-settings-table-scroll">
            <table class="font-settings-table-sample" :style="{ fontFamily: field.value }">
              <thead>
                <tr>
                  <th>{{ t("settings.fontSettingsTableCustomer") }}</th>
                  <th>{{ t("settings.fontSettingsTableAmount") }}</th>
                  <th>{{ t("settings.fontSettingsTableNote") }}</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td>张三 / Alice</td>
                  <td class="font-settings-sample-numbers">1,234.50</td>
                  <td class="font-settings-null">NULL</td>
                </tr>
                <tr>
                  <td>李四 / Bob</td>
                  <td class="font-settings-sample-numbers">98.76</td>
                  <td>已完成 / Done</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </div>

    <footer class="font-settings-footer">
      <p>{{ t("settings.fontSettingsCustomHint") }}</p>
      <p>{{ t("settings.fontSettingsFallbackHint") }}</p>
      <p>{{ t("settings.fontSettingsResetHint") }}</p>
    </footer>
  </section>
</template>

<style scoped>
.font-settings-panel {
  min-width: 0;
  border: 1px solid var(--border);
  border-radius: 8px;
  color: var(--workbench-text, var(--foreground));
  font-family: var(--workbench-font-ui, inherit);
  font-size: var(--workbench-font-size-ui, 12px);
}
.font-settings-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--border);
}
.font-settings-title,
.font-settings-label {
  font-weight: var(--workbench-font-weight-heading, 600);
}
.font-settings-title {
  font-size: 14px;
}
.font-settings-description,
.font-settings-footer {
  color: var(--workbench-muted, var(--muted-foreground));
  font-size: var(--workbench-font-size-caption, 11px);
  line-height: 1.6;
}
.font-settings-row {
  display: grid;
  grid-template-columns: minmax(0, 0.9fr) minmax(0, 1.1fr);
  align-items: start;
  gap: 16px;
  padding: 14px 16px;
}
.font-settings-row + .font-settings-row {
  border-top: 1px solid var(--border);
}
.font-settings-controls {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 8px;
}
.font-settings-size {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--workbench-secondary, var(--muted-foreground));
  font-size: var(--workbench-font-size-caption, 11px);
}
.font-settings-size label,
.font-settings-size-value {
  flex-shrink: 0;
}
.font-settings-size input {
  min-width: 48px;
  width: 100%;
  accent-color: var(--workbench-accent, var(--primary));
}
.font-settings-size-value {
  min-width: 35px;
  text-align: right;
  font-variant-numeric: tabular-nums;
}
.font-settings-preview {
  min-width: 0;
  padding: 9px 10px;
  border: 1px solid var(--border);
  border-radius: 5px;
  background: color-mix(in oklch, var(--muted) 25%, transparent);
}
.font-settings-preview-label {
  display: block;
  margin-bottom: 7px;
  color: var(--workbench-muted, var(--muted-foreground));
  font-size: var(--workbench-font-size-caption, 11px);
}
.font-settings-ui-sample {
  font-size: 13px;
  line-height: 1.7;
  overflow-wrap: anywhere;
}
.font-settings-ui-sample-title {
  font-weight: 600;
}
.font-settings-sample-numbers {
  font-variant-numeric: tabular-nums;
}
.font-settings-sql-sample {
  margin: 0;
  overflow-x: auto;
  line-height: 1.65;
  tab-size: 2;
}
.font-settings-sql-keyword {
  color: var(--workbench-accent, var(--primary));
}
.font-settings-sql-comment,
.font-settings-null {
  color: var(--workbench-muted, var(--muted-foreground));
}
.font-settings-null {
  font-style: italic;
}
.font-settings-table-scroll {
  overflow-x: auto;
}
.font-settings-table-sample {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
  text-align: left;
  white-space: nowrap;
}
.font-settings-table-sample th,
.font-settings-table-sample td {
  padding: 5px 8px;
  border-bottom: 1px solid var(--border);
}
.font-settings-table-sample th {
  font-weight: 500;
}
.font-settings-table-sample th:first-child,
.font-settings-table-sample td:first-child {
  padding-left: 0;
}
.font-settings-table-sample tbody tr:last-child td {
  border-bottom: 0;
}
.font-settings-footer {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 10px 16px;
  border-top: 1px solid var(--border);
}
@media (max-width: 680px) {
  .font-settings-header {
    flex-wrap: wrap;
  }
  .font-settings-row {
    grid-template-columns: minmax(0, 1fr);
    gap: 10px;
  }
}
</style>
