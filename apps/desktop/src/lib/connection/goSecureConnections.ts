import type { ConnectionConfig, SshTunnelConfig, TransportLayerConfig } from "@/types/database";

export type GoTLSFields = Pick<ConnectionConfig, "ssl" | "ca_cert_path" | "client_cert_path" | "client_key_path" | "url_params"> & { db_type?: string };

export function isGoTLSDatabase(type: string): boolean {
  return ["mysql", "mariadb", "postgres", "redis", "mongodb", "clickhouse"].includes(type);
}

export function goTLSValidationMessage(config: GoTLSFields, chinese = false): string {
  if (config.url_params?.trim()) {
    if (config.db_type === "mongodb") {
      const params = new URLSearchParams(config.url_params);
      const seen = new Set<string>();
      for (const [key, value] of params) {
        if (seen.has(key) || !["authSource", "authMechanism", "directConnection"].includes(key) || (key === "authMechanism" && !["SCRAM-SHA-1", "SCRAM-SHA-256"].includes(value)) || (key === "directConnection" && value !== "true"))
          return chinese ? "Go MongoDB 当前仅支持 authSource、SCRAM authMechanism 和 directConnection=true；TLS 请在此设置。" : "Go MongoDB supports only authSource, SCRAM authMechanism and directConnection=true; configure TLS here.";
        seen.add(key);
      }
    } else return chinese ? "Go 版暂不支持自定义 URL 参数。请清空原有参数，并在此设置 TLS。" : "Custom URL parameters are not supported by the Go backend. Clear them and configure TLS here.";
  }
  const paths = [config.ca_cert_path, config.client_cert_path, config.client_key_path].map((path) => path?.trim() || "");
  if (!config.ssl && paths.some(Boolean)) return chinese ? "使用证书文件时必须启用 TLS；关闭 TLS 时请清空证书路径。" : "Enable TLS to use certificate files, or clear the certificate paths when TLS is disabled.";
  if (!!paths[1] !== !!paths[2]) return chinese ? "客户端证书和私钥必须同时提供。" : "Provide both the client certificate and its private key.";
  return "";
}

// Keep the Go driver's direct fields through the legacy editor's normalization.
// In particular, PostgreSQL CA paths and MySQL client identities are not URL parameters.
export function normalizeGoTLSFields(config: GoTLSFields): void {
  config.ca_cert_path = config.ca_cert_path?.trim() || "";
  config.client_cert_path = config.client_cert_path?.trim() || "";
  config.client_key_path = config.client_key_path?.trim() || "";
  const error = goTLSValidationMessage(config);
  if (error) throw new Error(error);
}

export function goSSHValidationMessage(config: Partial<SshTunnelConfig>, chinese = false): string {
  const fingerprint = config.host_key_fingerprint?.trim();
  if (fingerprint && !/^SHA256:[A-Za-z0-9+/]{43}$/.test(fingerprint)) return chinese ? "主机指纹格式应为 SHA256: 后接 43 个 Base64 字符。" : "Use a SHA256: host fingerprint followed by 43 Base64 characters.";
  if (config.proxy_command?.trim() || config.allow_exec_channel_proxy || config.expose_lan)
    return chinese ? "此 SSH 配置包含 Go 版不支持的 ProxyCommand、exec 回退或局域网监听选项，请移除后继续。" : "This SSH configuration uses unsupported ProxyCommand, exec fallback, or LAN listening options. Remove them before continuing.";
  return "";
}

export function validateGoTransportLayers(layers: TransportLayerConfig[]): void {
  const enabled = layers.filter((layer) => layer.enabled !== false);
  if (enabled.length > 4) throw new Error("The Go backend supports at most four SSH hops.");
  for (const layer of enabled) {
    if (layer.type !== "ssh") throw new Error("The Go backend currently supports SSH transports only; remove or disable proxy and HTTP tunnel layers.");
    if (layer.profile_id) continue;
    const error = goSSHValidationMessage(layer);
    if (error) throw new Error(error);
  }
}
