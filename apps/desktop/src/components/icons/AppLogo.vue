<script setup lang="ts">
import { useI18n } from "vue-i18n";
import { webPath } from "@/lib/common/webPath";

const { t } = useI18n();
</script>

<template>
  <img :src="webPath('/workbench.svg')" :alt="t('app.name')" />
</template>
