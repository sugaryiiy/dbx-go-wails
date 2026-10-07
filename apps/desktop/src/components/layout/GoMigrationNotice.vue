<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { isGoBackend } from "@/lib/backend/goCapabilities";

const { locale } = useI18n();
const chinese = computed(() => locale.value.startsWith("zh"));
</script>

<template>
  <div v-if="isGoBackend()" role="note" class="border-b border-amber-500/20 bg-amber-500/5 px-4 py-1.5 text-xs text-muted-foreground">
    <strong class="mr-2 font-medium text-foreground">Go / Wails {{ chinese ? "迁移预览" : "migration preview" }}</strong>
    <span v-if="chinese">已接入 SQLite、MySQL / MariaDB、PostgreSQL、Redis、MongoDB 和 ClickHouse 的基础操作与安全连接。MCP 当前支持前三类关系数据库；其他引擎、AI、插件及高级管理功能仍在迁移。</span>
    <span v-else>Basic operations and secure connections are available for SQLite, MySQL / MariaDB, PostgreSQL, Redis, MongoDB and ClickHouse. MCP supports the first three relational families; other engines, AI, plugins and advanced management remain in progress.</span>
  </div>
</template>
