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
    <span v-if="chinese">当前支持 SQLite、MySQL / MariaDB、PostgreSQL 的基础操作、经过主机身份验证的 SSH / TLS 连接及 MCP 数据库工具；其他数据库、AI、插件、备份和数据库间传输尚未迁移。</span>
    <span v-else>Basic SQLite, MySQL / MariaDB and PostgreSQL operations, verified SSH / TLS connections, and MCP database tools are available. Other databases, AI, plugins, backups and database transfers are pending.</span>
  </div>
</template>
