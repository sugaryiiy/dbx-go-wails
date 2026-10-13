import { describe, expect, it } from "vitest";
import { goSSHValidationMessage, goTLSValidationMessage, normalizeGoTLSFields, validateGoTransportLayers } from "./goSecureConnections";
import { detachTunnelProfileLayer, tunnelProfileReferenceLayer } from "./tunnelProfiles";
import type { TransportLayerConfig, TunnelProfile } from "@/types/database";

const hop = (overrides: Record<string, unknown> = {}): TransportLayerConfig => ({ type: "ssh", id: "hop", host: "bastion", port: 22, user: "alice", auth_method: "agent", ...overrides });

describe("Go secure connection validation", () => {
  it("requires enabled TLS for certificate paths and a complete client pair", () => {
    expect(goTLSValidationMessage({ ssl: false, ca_cert_path: "ca.pem" })).toContain("Enable TLS");
    expect(goTLSValidationMessage({ ssl: true, client_cert_path: "client.pem" })).toContain("both");
    const fields = { ssl: true, ca_cert_path: " ca.pem ", client_cert_path: " client.pem ", client_key_path: " key.pem ", url_params: "" };
    normalizeGoTLSFields(fields);
    expect(fields).toEqual({ ssl: true, ca_cert_path: "ca.pem", client_cert_path: "client.pem", client_key_path: "key.pem", url_params: "" });
  });

  it("never silently discards an unsupported URL parameter", () => {
    const fields = { ssl: true, url_params: "sslmode=prefer" };
    expect(() => normalizeGoTLSFields(fields)).toThrow("URL parameters");
    expect(fields.url_params).toBe("sslmode=prefer");
  });

  it("keeps MongoDB authentication options separate from verified TLS", () => {
    expect(goTLSValidationMessage({ db_type: "mongodb", ssl: true, url_params: "authSource=admin&authMechanism=SCRAM-SHA-256&directConnection=true" })).toBe("");
    for (const params of ["tlsAllowInvalidCertificates=true", "replicaSet=rs0", "authMechanism=MONGODB-OIDC", "directConnection=false", "authSource=admin&authSource=other"]) {
      expect(goTLSValidationMessage({ db_type: "mongodb", ssl: true, url_params: params })).toContain("Go MongoDB");
    }
  });

  it("accepts a known_hosts default and validates explicit SHA256 pins", () => {
    expect(goSSHValidationMessage({})).toBe("");
    expect(goSSHValidationMessage({ host_key_fingerprint: "SHA256:" + "A".repeat(43) })).toBe("");
    for (const pin of ["MD5:00:01", "SHA256:short", "SHA256:" + "A".repeat(44)]) {
      expect(goSSHValidationMessage({ host_key_fingerprint: pin })).toContain("SHA256:");
    }
  });

  it("limits enabled transports to four SSH hops and rejects unsupported options", () => {
    expect(() => validateGoTransportLayers(Array.from({ length: 4 }, () => hop()))).not.toThrow();
    expect(() => validateGoTransportLayers(Array.from({ length: 5 }, () => hop()))).toThrow("four SSH hops");
    expect(() => validateGoTransportLayers([...Array.from({ length: 4 }, () => hop()), hop({ enabled: false })])).not.toThrow();
    for (const fields of [{ proxy_command: "legacy" }, { expose_lan: true }, { allow_exec_channel_proxy: true }]) {
      expect(() => validateGoTransportLayers([hop(fields)])).toThrow("unsupported");
    }
    expect(() => validateGoTransportLayers([{ type: "proxy", id: "proxy", host: "localhost", port: 8080, proxy_type: "http" }])).toThrow("SSH transports only");
  });

  it("retains trusted host settings when a shared profile is detached", () => {
    const profile = hop({ id: "shared", host_key_fingerprint: "SHA256:" + "A".repeat(43), known_hosts_path: "/trusted/known_hosts" }) as TunnelProfile;
    const reference = tunnelProfileReferenceLayer(profile, { id: "connection-layer", enabled: true });
    expect(reference.profile_id).toBe("shared");
    expect(reference).not.toHaveProperty("host_key_fingerprint");
    expect(detachTunnelProfileLayer(reference, profile)).toMatchObject({ id: "connection-layer", host_key_fingerprint: "SHA256:" + "A".repeat(43), known_hosts_path: "/trusted/known_hosts" });
  });
});
