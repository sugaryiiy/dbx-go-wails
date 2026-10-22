<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { loadMcpHttpServerSettings, saveMcpHttpServerSettings, mcpHttpServerStatus, rotateMcpHttpServerToken, loadWebMcpHttpStatus, saveWebMcpHttpSettings, rotateWebMcpToken } from "@/lib/backend/api";
import type { McpHttpServerSettings, McpHttpServerStatus, WebMcpHttpStatus } from "@/lib/backend/backendTypes";
import { isWailsRuntime } from "@/lib/backend/wailsRuntime";
import { apiUrl } from "@/lib/common/webPath";
import { copyToClipboard } from "@/lib/common/clipboard";
import { buildGoMcpConfig, mcpEndpointUrl, type GoMcpConfigFormat } from "@/lib/mcp/goMcpConfigTemplates";

const props = defineProps<{ policyConfigured: boolean }>();
const emit = defineEmits<{ openPermissions: [] }>();
const { locale } = useI18n();
const chinese = computed(() => locale.value.startsWith("zh"));
const tr = (zh: string, en: string) => (chinese.value ? zh : en);
const desktop = isWailsRuntime();
const currentHost = window.location.host;
const busy = ref(false);
const loaded = ref(false);
const error = ref("");
const tokenVisible = ref(false);
const copied = ref("");
const settings = ref<McpHttpServerSettings>({ enabled: false, host: "127.0.0.1", port: 5225, path: "/mcp", allowRemote: false, allowedHosts: [], allowedOrigins: [] });
const status = ref<McpHttpServerStatus | null>(null);
const webStatus = ref<WebMcpHttpStatus | null>(null);
const allowedHosts = ref("");
const allowedOrigins = ref("");
const enabled = computed(() => (desktop ? status.value?.running === true : webStatus.value?.enabled === true));
const endpoint = computed(() => (desktop ? status.value?.endpoint || "" : webStatus.value ? mcpEndpointUrl(window.location.origin, webStatus.value.endpointPath, apiUrl("/api")) : ""));
const token = computed(() => (desktop ? status.value?.accessToken : webStatus.value?.accessToken) || "");
const managed = computed(() => !desktop && (webStatus.value?.deploymentManaged || webStatus.value?.managementAvailable === false));
const format = ref<GoMcpConfigFormat>("http");
const clientConfig = computed(() => (endpoint.value ? buildGoMcpConfig(endpoint.value, format.value) : ""));
const configuredDraft = ref("");
const draftValue = computed(() => JSON.stringify(desktop ? settings.value : { enabled: settings.value.enabled, hosts: allowedHosts.value, origins: allowedOrigins.value }));
const unsaved = computed(() => loaded.value && draftValue.value !== configuredDraft.value);

function list(value: string): string[] {
  return [
    ...new Set(
      value
        .split(/[\n,]/)
        .map((item) => item.trim())
        .filter(Boolean),
    ),
  ];
}
function applyWebStatus(value: WebMcpHttpStatus) {
  webStatus.value = value;
  settings.value.enabled = value.enabled;
  allowedHosts.value = value.allowedHosts.join("\n");
  allowedOrigins.value = value.allowedOrigins.join("\n");
}
async function load() {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    if (desktop) {
      const [configuration, service] = await Promise.all([loadMcpHttpServerSettings(), mcpHttpServerStatus()]);
      settings.value = configuration;
      status.value = service;
    } else {
      applyWebStatus(await loadWebMcpHttpStatus());
    }
    configuredDraft.value = draftValue.value;
    loaded.value = true;
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause);
  } finally {
    busy.value = false;
  }
}
const validation = computed(() => {
  if (!settings.value.enabled) return "";
  if (!props.policyConfigured) return tr("请先在「权限授权」中选择访问范围并点击「应用」，再启用服务。", "Choose and apply the access policy in Permissions before enabling MCP.");
  if (desktop) {
    if (!["127.0.0.1", "::1"].includes(settings.value.host.trim()) || settings.value.allowRemote) return tr("桌面 MCP 目前仅支持本机回环地址。", "Desktop MCP currently supports loopback addresses only.");
    if (!Number.isInteger(Number(settings.value.port)) || Number(settings.value.port) < 1 || Number(settings.value.port) > 65535) return tr("端口须为 1–65535 的整数。", "Use an integer port between 1 and 65535.");
    if (!settings.value.path.startsWith("/") || /[?#]/.test(settings.value.path)) return tr("路径须以 / 开头且不含查询参数或片段。", "The path must start with / and contain no query or fragment.");
  } else if (!list(allowedHosts.value).length) return tr("请填写允许访问的 Host（例如 localhost:4334）。", "Set the allowed Host, for example localhost:4334.");
  return "";
});
async function save() {
  if (busy.value || !loaded.value || validation.value || managed.value) return;
  busy.value = true;
  error.value = "";
  try {
    if (desktop) {
      const next = { ...settings.value, host: settings.value.host.trim(), port: Number(settings.value.port), path: settings.value.path.trim(), allowRemote: false };
      status.value = await saveMcpHttpServerSettings(next);
      settings.value = next;
    } else {
      applyWebStatus(await saveWebMcpHttpSettings({ enabled: settings.value.enabled, allowedHosts: list(allowedHosts.value), allowedOrigins: list(allowedOrigins.value) }));
    }
    configuredDraft.value = draftValue.value;
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause);
  } finally {
    busy.value = false;
  }
}
async function rotate() {
  if (busy.value || managed.value || !window.confirm(tr("轮换后，现有客户端的旧 Token 立即失效。继续？", "Rotating the token immediately invalidates existing client tokens. Continue?"))) return;
  busy.value = true;
  error.value = "";
  try {
    if (desktop) status.value = await rotateMcpHttpServerToken();
    else webStatus.value = await rotateWebMcpToken();
    tokenVisible.value = false;
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause);
  } finally {
    busy.value = false;
  }
}
async function copy(value: string, kind: string) {
  try {
    await copyToClipboard(value);
    copied.value = kind;
  } catch {
    error.value = tr("复制失败，请手动复制。", "Copy failed. Please copy manually.");
  }
}
onMounted(load);
</script>

<template>
  <div class="space-y-4" data-testid="go-mcp-server-panel">
    <div class="rounded-md border bg-muted/20 p-4 space-y-3">
      <div class="flex items-center justify-between gap-3">
        <div class="space-y-1">
          <Label for="go-mcp-enabled" class="text-base">{{ tr("Go MCP 服务", "Go MCP service") }}</Label>
          <p class="text-xs text-muted-foreground" role="status">{{ enabled ? tr("运行中", "Running") : tr("已停止", "Stopped") }} · Streamable HTTP</p>
        </div>
        <Switch id="go-mcp-enabled" v-model="settings.enabled" :disabled="busy || !loaded || managed" />
      </div>
      <p class="text-xs text-muted-foreground">
        {{ tr("与喜鹊共用连接和权限，无需安装 npm 包。默认关闭；启用后客户端仍须提供 Bearer Token。", "Uses MagpieDB connections and permissions with no npm installation. Disabled by default; clients require a bearer token when enabled.") }}
      </p>
      <div v-if="!policyConfigured" class="rounded border border-amber-500/30 p-3 text-xs space-y-2">
        <p>{{ tr("先配置并应用访问权限，再开启服务。", "Configure and apply access permissions before enabling the service.") }}</p>
        <Button size="sm" variant="outline" @click="emit('openPermissions')">{{ tr("权限授权", "Permissions") }}</Button>
      </div>
      <template v-if="desktop">
        <div class="grid gap-3 sm:grid-cols-3">
          <div class="space-y-1"><Label for="go-mcp-host">Host</Label><Input id="go-mcp-host" v-model="settings.host" :disabled="busy || !loaded" placeholder="127.0.0.1" /></div>
          <div class="space-y-1">
            <Label for="go-mcp-port">{{ tr("端口", "Port") }}</Label
            ><Input id="go-mcp-port" v-model.number="settings.port" type="number" min="1" max="65535" :disabled="busy || !loaded" />
          </div>
          <div class="space-y-1">
            <Label for="go-mcp-path">{{ tr("路径", "Path") }}</Label
            ><Input id="go-mcp-path" v-model="settings.path" :disabled="busy || !loaded" />
          </div>
        </div>
        <p class="text-xs text-muted-foreground">
          {{ tr("桌面服务仅允许 127.0.0.1 或 ::1；关闭喜鹊后服务停止。远程接入请使用有 HTTPS 反向代理的 Web 部署。", "Desktop service only binds to 127.0.0.1 or ::1 and stops when MagpieDB closes. For remote access, use a Web deployment behind an HTTPS reverse proxy.") }}
        </p>
      </template>
      <template v-else>
        <div class="grid gap-3 sm:grid-cols-2">
          <div class="space-y-1">
            <Label for="go-mcp-hosts">{{ tr("允许的 Host", "Allowed hosts") }}</Label
            ><textarea id="go-mcp-hosts" v-model="allowedHosts" rows="2" :disabled="busy || !loaded || managed" class="w-full rounded-md border bg-background px-3 py-2 font-mono text-xs" :placeholder="currentHost" />
            <p class="text-xs text-muted-foreground">{{ tr("每行一个 host:port，与客户端请求完全匹配。", "One host:port per line, exactly matching the client request.") }}</p>
          </div>
          <div class="space-y-1">
            <Label for="go-mcp-origins">{{ tr("允许的 Origin（可选）", "Allowed origins (optional)") }}</Label
            ><textarea id="go-mcp-origins" v-model="allowedOrigins" rows="2" :disabled="busy || !loaded || managed" class="w-full rounded-md border bg-background px-3 py-2 font-mono text-xs" placeholder="https://mcp-client.example.com" />
            <p class="text-xs text-muted-foreground">{{ tr("浏览器客户端的完整 Origin；无 Origin 的桌面客户端不受此列表限制。", "Exact browser client origins. Desktop clients without an Origin header do not need an entry.") }}</p>
          </div>
        </div>
        <p v-if="managed" class="text-xs text-muted-foreground">{{ tr("此部署不允许在页面修改 MCP 设置。", "This deployment does not allow MCP settings to be changed here.") }}</p>
      </template>
      <p v-if="validation" class="text-xs text-amber-700 dark:text-amber-300">{{ validation }}</p>
      <p v-if="error || status?.lastError" role="alert" class="text-xs text-destructive">{{ error || status?.lastError }}</p>
      <div class="flex items-center gap-2">
        <Button variant="outline" size="sm" :disabled="busy" data-testid="mcp-reload" @click="load">{{ tr("刷新状态", "Refresh") }}</Button>
        <Button size="sm" :disabled="busy || !loaded || !!validation || managed || !unsaved" data-testid="mcp-save" @click="save">{{ busy ? tr("处理中…", "Working…") : tr("保存并应用服务设置", "Apply service settings") }}</Button>
      </div>
    </div>
    <section v-if="enabled && endpoint" class="space-y-3 rounded-md border p-4">
      <div class="space-y-1">
        <Label>Endpoint</Label>
        <div class="flex items-center gap-2">
          <code class="min-w-0 flex-1 overflow-x-auto rounded border bg-background px-2 py-1.5 text-xs">{{ endpoint }}</code
          ><Button variant="outline" size="sm" @click="copy(endpoint, 'endpoint')">{{ copied === "endpoint" ? tr("已复制", "Copied") : tr("复制", "Copy") }}</Button>
        </div>
      </div>
      <div v-if="token" class="space-y-2">
        <Label for="go-mcp-token">Bearer Token</Label>
        <div class="flex flex-wrap items-center gap-2">
          <input id="go-mcp-token" :value="token" :type="tokenVisible ? 'text' : 'password'" readonly autocomplete="off" class="min-w-0 flex-1 rounded border bg-background px-2 py-1.5 font-mono text-xs" /><Button variant="outline" size="sm" @click="tokenVisible = !tokenVisible">{{
            tokenVisible ? tr("隐藏", "Hide") : tr("显示", "Show")
          }}</Button
          ><Button variant="outline" size="sm" @click="copy(token, 'token')">{{ copied === "token" ? tr("已复制", "Copied") : tr("复制 Token", "Copy token") }}</Button
          ><Button variant="outline" size="sm" :disabled="busy || managed" data-testid="mcp-rotate" @click="rotate">{{ tr("轮换 Token", "Rotate token") }}</Button>
        </div>
      </div>
      <p v-if="unsaved" class="text-xs text-amber-700 dark:text-amber-300">{{ tr("以下配置使用当前已生效的地址；上方更改尚未应用。", "The configuration below uses the active endpoint. The changes above have not been applied.") }}</p>
      <div class="flex flex-wrap items-center gap-2" role="group" :aria-label="tr('客户端配置格式', 'Client configuration format')">
        <Button v-for="choice in ['http', 'vscode', 'codex', 'stdio'] as const" :key="choice" size="sm" :variant="format === choice ? 'default' : 'outline'" @click="format = choice">{{ { http: "HTTP JSON", vscode: "VS Code", codex: "Codex", stdio: "Go stdio" }[choice] }}</Button>
      </div>
      <pre class="max-h-72 overflow-auto rounded border bg-background p-3 text-xs whitespace-pre-wrap" data-testid="mcp-client-config">{{ clientConfig }}</pre>
      <Button variant="outline" size="sm" @click="copy(clientConfig, 'config')">{{ copied === "config" ? tr("已复制", "Copied") : tr("复制配置模板", "Copy configuration template") }}</Button>
      <p class="text-xs text-muted-foreground">
        {{
          format === "codex"
            ? tr("在客户端环境中设置 DBX_MCP_TOKEN 为上方 Token，再重启客户端。", "Set DBX_MCP_TOKEN in the client environment to the token above, then restart the client.")
            : tr("将模板中的 replace-with-your-mcp-token 替换为上方 Token，并保存在客户端的私有配置中。", "Replace replace-with-your-mcp-token with the token above and save it in the client's private configuration.")
        }}
      </p>
      <p v-if="format === 'stdio'" class="text-xs text-muted-foreground">
        {{
          tr(
            "使用 Go 仓库 build:mcp 生成的 dbx-mcp-go，将 command 替换为其绝对路径（Windows 为 .exe）。该程序通过 HTTP 连接当前喜鹊，喜鹊必须保持运行。",
            "Use dbx-mcp-go from the Go repository's build:mcp command and replace command with its absolute path (.exe on Windows). This bridge connects to the running MagpieDB over HTTP.",
          )
        }}
      </p>
    </section>
  </div>
</template>
