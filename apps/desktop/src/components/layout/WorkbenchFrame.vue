<script setup lang="ts">
import { useI18n } from "vue-i18n";
import GoMigrationNotice from "./GoMigrationNotice.vue";
const { t } = useI18n();
</script>

<template>
  <div class="dbx-workbench workbench-frame">
    <header v-if="$slots.header" class="workbench-frame-header"><slot name="header" /></header>
    <GoMigrationNotice />
    <div class="workbench-frame-body">
      <slot name="navigation" />
      <main class="workbench-frame-stage" :aria-label="t('workbenchLayout.workspace')"><slot /></main>
      <slot name="tools" />
    </div>
    <slot name="status" />
    <slot name="overlays" />
  </div>
</template>

<style scoped>
.workbench-frame {
  display: flex;
  flex-direction: column;
  width: 100%;
  height: 100%;
  min-width: 760px;
  min-height: 600px;
  overflow: hidden;
}
.workbench-frame-header {
  flex: 0 0 auto;
  min-width: 0;
}
.workbench-frame-body {
  display: flex;
  flex: 1 1 0;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
}
.workbench-frame-stage {
  display: flex;
  flex: 1 1 0;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
}
</style>
