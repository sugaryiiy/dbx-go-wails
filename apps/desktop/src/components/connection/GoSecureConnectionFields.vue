<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { isWailsRuntime, openWailsFile } from "@/lib/backend/wailsRuntime";
import { goTLSValidationMessage, type GoTLSFields } from "@/lib/connection/goSecureConnections";

const props = defineProps<{ config: GoTLSFields }>();
const { locale } = useI18n();
const chinese = computed(() => locale.value.startsWith("zh"));
const nativeFiles = isWailsRuntime();
const error = ref("");
const validation = computed(() => goTLSValidationMessage(props.config, chinese.value));
const fields = computed(() => [
  { key: "ca_cert_path" as const, label: chinese.value ? "CA 证书" : "CA certificate", placeholder: "~/certs/ca.pem" },
  { key: "client_cert_path" as const, label: chinese.value ? "客户端证书" : "Client certificate", placeholder: "~/certs/client.pem" },
  { key: "client_key_path" as const, label: chinese.value ? "客户端私钥" : "Client private key", placeholder: "~/certs/client-key.pem" },
]);

async function browse(field: (typeof fields.value)[number]) {
  error.value = "";
  try {
    const path = await openWailsFile({ title: field.label, multiple: false });
    if (typeof path === "string") props.config[field.key] = path;
  } catch (cause) {
    error.value = String(cause);
  }
}
</script>

<template>
  <section class="grid gap-4" data-testid="go-tls-fields">
    <label class="flex items-center gap-2 text-sm">
      <input v-model="config.ssl" type="checkbox" data-testid="go-tls-enabled" />
      {{ chinese ? "启用 TLS，并验证服务器身份" : "Enable TLS and verify server identity" }}
    </label>
    <p class="text-xs leading-5 text-muted-foreground">
      {{ chinese ? "TLS 1.2 或更新版本，校验证书及数据库主机名。CA 路径留空使用系统信任库；填写后仅信任该 CA 文件。客户端证书与私钥为可选配对项。" : "TLS 1.2 or newer verifies the certificate and database hostname. An empty CA path uses system trust; a supplied CA file becomes the trust store. Client certificate and key are an optional pair." }}
    </p>
    <label v-for="field in fields" :key="field.key" class="grid grid-cols-4 items-center gap-4 text-xs">
      <span>{{ field.label }}</span>
      <span class="col-span-3 flex gap-2">
        <Input v-model="config[field.key]" :placeholder="field.placeholder" :data-testid="field.key" class="min-w-0 flex-1" />
        <Button v-if="nativeFiles" type="button" variant="outline" size="sm" :aria-label="field.label" @click="browse(field)">{{ chinese ? "选择" : "Browse" }}</Button>
      </span>
    </label>
    <p v-if="!nativeFiles" class="text-xs text-muted-foreground">{{ chinese ? "路径指向运行 Go 服务的设备上的 PEM 文件。" : "Paths refer to PEM files on the device running the Go service." }}</p>
    <p v-if="validation || error" role="alert" class="text-xs text-red-500">{{ validation || error }}</p>
  </section>
</template>
