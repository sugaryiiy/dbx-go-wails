<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { isWailsRuntime, openWailsFile } from "@/lib/backend/wailsRuntime";
import { goSSHValidationMessage } from "@/lib/connection/goSecureConnections";
import type { SshTunnelConfig } from "@/types/database";

const props = defineProps<{ config: Partial<SshTunnelConfig>; disabled?: boolean }>();
const { locale } = useI18n();
const chinese = computed(() => locale.value.startsWith("zh"));
const nativeFiles = isWailsRuntime();
const error = ref("");
const validation = computed(() => goSSHValidationMessage(props.config, chinese.value));
const unsupported = computed(() => props.config.proxy_command?.trim() || props.config.allow_exec_channel_proxy || props.config.expose_lan);
function clearUnsupported() {
  props.config.proxy_command = "";
  props.config.allow_exec_channel_proxy = false;
  props.config.expose_lan = false;
}
async function browse() {
  error.value = "";
  try {
    const path = await openWailsFile({ title: "SSH known_hosts", multiple: false });
    if (typeof path === "string") props.config.known_hosts_path = path;
  } catch (cause) {
    error.value = String(cause);
  }
}
</script>

<template>
  <section class="grid gap-3 rounded-md border p-3" data-testid="go-ssh-host-verification">
    <p class="text-xs font-medium">{{ chinese ? "SSH 主机身份验证" : "SSH host verification" }}</p>
    <label class="grid grid-cols-4 items-center gap-4 text-xs">
      <span>known_hosts</span>
      <span class="col-span-3 flex gap-2">
        <Input v-model="config.known_hosts_path" placeholder="~/.ssh/known_hosts" :disabled="disabled || !!config.host_key_fingerprint?.trim()" data-testid="go-ssh-known-hosts" class="min-w-0 flex-1" />
        <Button v-if="nativeFiles" type="button" variant="outline" size="sm" :disabled="disabled || !!config.host_key_fingerprint?.trim()" @click="browse">{{ chinese ? "选择" : "Browse" }}</Button>
      </span>
    </label>
    <label class="grid grid-cols-4 items-center gap-4 text-xs">
      <span>{{ chinese ? "固定主机指纹" : "Pinned host fingerprint" }}</span>
      <Input v-model="config.host_key_fingerprint" placeholder="SHA256:…" :disabled="disabled" data-testid="go-ssh-fingerprint" class="col-span-3" />
    </label>
    <p class="text-xs leading-5 text-muted-foreground">
      {{ chinese ? "默认使用 Go 服务所在设备的 ~/.ssh/known_hosts。也可从管理员或可信渠道核对 SHA256 指纹并填写，填写后以指纹为准。未知或变化的主机密钥会拒绝连接，不会自动信任。" : "Uses ~/.ssh/known_hosts on the Go service device by default. Alternatively, verify a SHA256 fingerprint with your administrator or a trusted source and pin it here; a pin takes precedence. Unknown or changed keys are rejected and never trusted automatically." }}
    </p>
    <p v-if="validation || error" role="alert" class="text-xs text-red-500">{{ validation || error }}</p>
    <Button v-if="unsupported" type="button" variant="outline" size="sm" :disabled="disabled" @click="clearUnsupported">{{ chinese ? "移除旧版 SSH 选项" : "Remove unsupported SSH options" }}</Button>
  </section>
</template>
