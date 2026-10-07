// @vitest-environment happy-dom

import { createApp, defineComponent, h, nextTick, reactive, type App } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { ConnectionConfig } from "@/types/database";

const { store, settings, backend } = vi.hoisted(() => ({
  store: {
    connectionGroupOptions: [],
    newConnectionGroupId: null,
    selectedConnectionGroupId: null,
    addConnection: vi.fn(),
    updateConnection: vi.fn(),
    connect: vi.fn(),
    ensureConnected: vi.fn(),
    addEphemeralConnection: vi.fn(),
    removeConnection: vi.fn(),
    startEditing: vi.fn(),
    stopEditing: vi.fn(),
    clearConnectionError: vi.fn(),
    updateConnectionDatabaseInfo: vi.fn(),
    applyGlobalTimeouts: vi.fn((config) => config),
    getConfig: vi.fn(),
  },
  settings: {
    editorSettings: { sidebarShowConnectionNotes: false, globalConnectTimeoutSecs: 10, globalQueryTimeoutSecs: 0 },
    rememberedDatabaseForConnection: vi.fn(() => ""),
    persistEditorSettings: vi.fn(),
    updateEditorSettings: vi.fn(),
    updateEditorSettingsAndPersist: vi.fn(),
  },
  backend: {
    connectDb: vi.fn(),
    disconnectDb: vi.fn(),
    testConnectionWithInfo: vi.fn(),
    listDatabases: vi.fn(),
    listPlugins: vi.fn(),
    listJdbcDrivers: vi.fn(),
    listJdbcMavenBundles: vi.fn(),
    listJdbcLocalBundles: vi.fn(),
    listSshConfigHosts: vi.fn(),
    listInstalledAgentsLocal: vi.fn(),
    listInstalledAgents: vi.fn(),
    installAgent: vi.fn(),
    installJdbcPlugin: vi.fn(),
    listenAgentInstallProgress: vi.fn(),
  },
}));

vi.mock("@/stores/connectionStore", () => ({ useConnectionStore: () => store, CONNECTION_ATTEMPT_CANCELLED_MESSAGE: "cancelled" }));
vi.mock("@/stores/settingsStore", async (original) => ({ ...(await original<typeof import("@/stores/settingsStore")>()), useSettingsStore: () => settings }));
vi.mock("@/stores/tunnelProfileStore", () => ({ useTunnelProfileStore: () => ({ profiles: [], profileById: () => undefined, init: vi.fn() }) }));
vi.mock("@/lib/backend/api", async (original) => ({ ...(await original<typeof import("@/lib/backend/api")>()), ...backend }));
vi.mock("@/lib/backend/tauriRuntime", () => ({ isTauriRuntime: () => false }));
vi.mock("@/lib/backend/goCapabilities", () => ({ isGoBackend: () => true, GO_CONNECTION_PROFILES: new Set(["sqlite", "mysql", "mariadb", "postgres", "redis", "mongodb", "clickhouse"]) }));
const native = vi.hoisted(() => ({ open: vi.fn() }));
vi.mock("@/lib/backend/wailsRuntime", () => ({ isWailsRuntime: () => true, openWailsFile: native.open, saveWailsFile: vi.fn() }));
vi.mock("@/composables/useToast", () => ({ useToast: () => ({ toast: vi.fn() }) }));

vi.mock("@/components/ui/dialog", async () => {
  const { defineComponent, h } = await import("vue");
  const passthrough = defineComponent({
    setup:
      (_props, { slots }) =>
      () =>
        h("div", slots.default?.()),
  });
  const Dialog = defineComponent({
    props: { open: Boolean },
    emits: ["update:open"],
    setup:
      (props, { slots, emit }) =>
      () =>
        props.open ? h("section", [h("button", { "data-testid": "dismiss-dialog", onClick: () => emit("update:open", false) }, "Dismiss"), slots.default?.()]) : null,
  });
  return { Dialog, DialogContent: passthrough, DialogHeader: passthrough, DialogTitle: passthrough, DialogFooter: passthrough };
});
vi.mock("@/components/ui/button", async () => {
  const { defineComponent, h } = await import("vue");
  return {
    Button: defineComponent({
      inheritAttrs: false,
      setup:
        (_props, { attrs, slots }) =>
        () =>
          h("button", attrs, slots.default?.()),
    }),
  };
});
vi.mock("@/components/ui/input", async () => {
  const { defineComponent, h } = await import("vue");
  return {
    Input: defineComponent({
      props: ["modelValue"],
      emits: ["update:modelValue"],
      setup:
        (props, { attrs, emit }) =>
        () =>
          h("input", { ...attrs, value: props.modelValue, onInput: (event: Event) => emit("update:modelValue", (event.target as HTMLInputElement).value) }),
    }),
  };
});
vi.mock("@/components/ui/PasswordInput.vue", async () => {
  const { defineComponent, h } = await import("vue");
  return {
    default: defineComponent({
      props: ["modelValue"],
      setup:
        (props, { attrs }) =>
        () =>
          h("input", { ...attrs, type: "password", value: props.modelValue }),
    }),
  };
});
vi.mock("@/components/ui/tabs", async () => {
  const { defineComponent, h } = await import("vue");
  const pass = defineComponent({
    setup:
      (_props, { slots }) =>
      () =>
        h("div", slots.default?.()),
  });
  const TabsContent = defineComponent({
    props: ["value"],
    setup:
      (props, { slots }) =>
      () =>
        ["connection", "tls", "transport"].includes(props.value) ? h("div", slots.default?.()) : null,
  });
  return { Tabs: pass, TabsContent, TabsList: pass, TabsTrigger: pass };
});
vi.mock("@/components/ui/tooltip", async () => {
  const { defineComponent, h } = await import("vue");
  const pass = defineComponent({
    setup:
      (_props, { slots }) =>
      () =>
        h("span", slots.default?.()),
  });
  return { HelpTooltip: pass, Tooltip: pass, TooltipContent: pass, TooltipTrigger: pass };
});

import ConnectionDialog from "@/components/connection/ConnectionDialog.vue";

const mountedApps: App[] = [];

function savedConnection(overrides: Partial<ConnectionConfig> = {}): ConnectionConfig {
  return {
    id: "saved-production",
    name: "Saved production",
    db_type: "mysql",
    driver_profile: "mysql",
    driver_label: "MySQL",
    host: "old.example.test",
    port: 3307,
    username: "old-user",
    password: "old-password",
    database: "orders",
    ssl: true,
    read_only: true,
    is_production: true,
    production_databases: ["orders"],
    save_password: true,
    visible_databases: ["orders"],
    url_params: "",
    ca_cert_path: "~/certs/ca.pem",
    client_cert_path: "~/certs/client.pem",
    client_key_path: "~/certs/client-key.pem",
    ...overrides,
  };
}

async function settle() {
  for (let i = 0; i < 8; i++) await nextTick();
}

async function mountDialog(config: ConnectionConfig) {
  const props = reactive({ open: true, editConfig: config });
  store.getConfig.mockReturnValue(config);
  const container = document.createElement("div");
  document.body.append(container);
  const app = createApp(
    defineComponent({
      setup: () => () =>
        h(ConnectionDialog, {
          ...props,
          "onUpdate:open": (value: boolean) => {
            props.open = value;
          },
        }),
    }),
  );
  app.use(i18n);
  mountedApps.push(app);
  app.mount(container);
  await settle();
  return props;
}

beforeEach(() => {
  vi.clearAllMocks();
  i18n.global.locale.value = "en";
  for (const fn of Object.values(backend)) fn.mockResolvedValue([]);
  backend.listenAgentInstallProgress.mockResolvedValue(() => {});
  backend.testConnectionWithInfo.mockResolvedValue({ message: "Connection successful" });
  store.updateConnection.mockResolvedValue(undefined);
  store.addConnection.mockResolvedValue(undefined);
});

afterEach(() => {
  for (const app of mountedApps.splice(0)) app.unmount();
  document.body.innerHTML = "";
});

async function saveDraft() {
  const save = [...document.querySelectorAll("button")].find((button) => button.textContent?.trim() === "Save");
  expect(save).toBeTruthy();
  save!.click();
  await settle();
}

describe("Go secure connection editor", () => {
  for (const profile of ["sqlite", "mysql", "mariadb", "postgres", "redis", "mongodb", "clickhouse"] as const) {
    it(`tests native ${profile} without legacy agent installation or progress subscriptions`, async () => {
      backend.listInstalledAgentsLocal.mockRejectedValue(new Error("unmigrated agents endpoint"));
      await mountDialog(savedConnection({ db_type: profile === "mariadb" ? "mysql" : profile, driver_profile: profile, host: profile === "sqlite" ? "/tmp/test.sqlite" : "db.internal", ssl: profile !== "sqlite", ca_cert_path: "", client_cert_path: "", client_key_path: "" }));
      const test = [...document.querySelectorAll("button")].find((button) => button.textContent?.trim() === i18n.global.t("connection.test"));
      expect(test).toBeTruthy();
      test!.click();
      await settle();
      expect(backend.testConnectionWithInfo).toHaveBeenCalledOnce();
      expect(backend.testConnectionWithInfo).toHaveBeenCalledWith(expect.objectContaining({ driver_profile: profile }));
      for (const call of [backend.listInstalledAgentsLocal, backend.listInstalledAgents, backend.installAgent, backend.installJdbcPlugin, backend.listenAgentInstallProgress, backend.listJdbcDrivers, backend.listJdbcMavenBundles, backend.listJdbcLocalBundles, backend.listPlugins]) {
        expect(call).not.toHaveBeenCalled();
      }
    });
  }

  it("rejects an unmigrated driver explicitly instead of installing a legacy agent", async () => {
    await mountDialog(savedConnection({ db_type: "mysql", driver_profile: "doris" }));
    const test = [...document.querySelectorAll("button")].find((button) => button.textContent?.trim() === i18n.global.t("connection.test"));
    test!.click();
    await settle();
    expect(backend.testConnectionWithInfo).not.toHaveBeenCalled();
    expect(backend.installAgent).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("not yet been migrated to Go");
  });

  for (const profile of ["mysql", "mariadb", "postgres", "redis", "mongodb", "clickhouse"] as const) {
    it(`preserves direct TLS fields when saving ${profile}`, async () => {
      await mountDialog(savedConnection({ db_type: profile === "mariadb" ? "mysql" : profile, driver_profile: profile }));
      expect(document.querySelector('[data-testid="go-tls-fields"]')).not.toBeNull();
      expect(document.body.textContent).toContain("verify server identity");
      await saveDraft();
      expect(store.updateConnection).toHaveBeenCalledWith(
        expect.objectContaining({
          ssl: true,
          ca_cert_path: "~/certs/ca.pem",
          client_cert_path: "~/certs/client.pem",
          client_key_path: "~/certs/client-key.pem",
          url_params: "",
        }),
      );
    });
  }

  it("uses the Wails native certificate picker without a Rust dialog", async () => {
    native.open.mockResolvedValueOnce("/chosen/client.pem");
    await mountDialog(savedConnection());
    document.querySelector<HTMLButtonElement>('[aria-label="Client certificate"]')!.click();
    await settle();
    expect(native.open).toHaveBeenCalledWith({ title: "Client certificate", multiple: false });
    await saveDraft();
    expect(store.updateConnection).toHaveBeenCalledWith(expect.objectContaining({ client_cert_path: "/chosen/client.pem" }));
  });

  it("retains unsupported legacy URL parameters and blocks saving instead of dropping them", async () => {
    const saved = savedConnection({ url_params: "sslmode=disable" });
    await mountDialog(saved);
    expect(document.querySelector('[data-testid="go-tls-fields"]')!.textContent).toContain("Custom URL parameters");
    await saveDraft();
    expect(store.updateConnection).not.toHaveBeenCalled();
    expect(saved.url_params).toBe("sslmode=disable");
  });

  it("requires both client certificate and key and never disables TLS to continue", async () => {
    await mountDialog(savedConnection({ client_key_path: "" }));
    await saveDraft();
    expect(store.updateConnection).not.toHaveBeenCalled();
    expect(document.querySelector('[data-testid="go-tls-fields"]')!.textContent).toContain("both the client certificate");
    expect(document.querySelector<HTMLInputElement>('[data-testid="go-tls-enabled"]')!.checked).toBe(true);
  });

  it("preserves verified SSH host settings through normalization and hides unsupported creation", async () => {
    const fingerprint = "SHA256:" + "A".repeat(43);
    await mountDialog(savedConnection({ transport_layers: [{ type: "ssh", id: "hop", host: "bastion.internal", port: 22, user: "alice", enabled: true, auth_method: "key", key_path: "~/id_ed25519", host_key_fingerprint: fingerprint, known_hosts_path: "~/trusted_hosts" }] }));
    expect(document.querySelector('[data-testid="go-ssh-host-verification"]')).not.toBeNull();
    expect(document.body.textContent).toContain("trusted source");
    expect([...document.querySelectorAll("button")].some((button) => button.textContent?.trim() === "Proxy")).toBe(false);
    await saveDraft();
    expect(store.updateConnection).toHaveBeenCalledWith(expect.objectContaining({ transport_layers: [expect.objectContaining({ host_key_fingerprint: fingerprint, known_hosts_path: "~/trusted_hosts", key_path: "~/id_ed25519" })] }));
  });

  it("keeps inherited unsafe SSH options visible until explicitly removed", async () => {
    await mountDialog(savedConnection({ transport_layers: [{ type: "ssh", id: "hop", host: "bastion.internal", port: 22, user: "alice", auth_method: "password", password: "test-only", proxy_command: "legacy proxy", expose_lan: true }] }));
    await saveDraft();
    expect(store.updateConnection).not.toHaveBeenCalled();
    const remove = [...document.querySelectorAll("button")].find((button) => button.textContent?.trim() === "Remove unsupported SSH options");
    expect(remove).toBeTruthy();
    remove!.click();
    await settle();
    await saveDraft();
    expect(store.updateConnection).toHaveBeenCalledWith(expect.objectContaining({ transport_layers: [expect.objectContaining({ proxy_command: "", expose_lan: false, allow_exec_channel_proxy: false })] }));
  });
});
