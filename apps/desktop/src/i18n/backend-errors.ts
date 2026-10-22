import { GENERIC_TRANSPORT_FAILURE_MESSAGE, LEGACY_BACKEND_ERROR_CODE, normalizeBackendError, sanitizeBackendErrorMessage, type BackendError } from "@/lib/backend/errorUtils";
import { PHOENIX_DRIVER_NOT_INSTALLED_ERROR, PHOENIX_JDBC_PLUGIN_NOT_INSTALLED_ERROR } from "@/lib/database/phoenixConnection";
import { translateCommonBackendError } from "./common-backend-errors";

/**
 * Minimal shape of a translate function, satisfied by both `useI18n().t` inside
 * components and `i18n.global.t` in stores and composables. Using the full
 * `ComposerTranslation` type here would reject the latter, because the global
 * composer is typed against the concrete message schema.
 */
export type BackendErrorTranslate = {
  (key: string): string;
  (key: string, named: Record<string, unknown>): string;
};

const taggedAiCliErrorKeys: Record<string, string> = {
  claudeCodeNotInstalled: "ai.cliErrors.claudeCodeNotInstalled",
  claudeCodeCliPathInvalid: "ai.cliErrors.claudeCodeCliPathInvalid",
  claudeCodeEnvInvalid: "ai.cliErrors.claudeCodeEnvInvalid",
  claudeCodeEnvReserved: "ai.cliErrors.claudeCodeEnvReserved",
  claudeCodeNotAuthenticated: "ai.cliErrors.claudeCodeNotAuthenticated",
  claudeCodeMcpConfigInvalid: "ai.cliErrors.claudeCodeMcpConfigInvalid",
  dbxMcpMissing: "ai.cliErrors.dbxMcpMissing",
  claudeCodeMcpStartupFailed: "ai.cliErrors.claudeCodeMcpStartupFailed",
  claudeCodeCommandLineTooLong: "ai.cliErrors.claudeCodeCommandLineTooLong",
  claudeCodeRunFailed: "ai.cliErrors.claudeCodeRunFailed",
  piAgentNotInstalled: "ai.cliErrors.piAgentNotInstalled",
  piAgentCliPathInvalid: "ai.cliErrors.piAgentCliPathInvalid",
  piAgentEnvInvalid: "ai.cliErrors.piAgentEnvInvalid",
  piAgentEnvReserved: "ai.cliErrors.piAgentEnvReserved",
  piAgentNotAuthenticated: "ai.cliErrors.piAgentNotAuthenticated",
  piAgentMcpStartupFailed: "ai.cliErrors.piAgentMcpStartupFailed",
  piAgentTimeout: "ai.cliErrors.piAgentTimeout",
  piAgentProtocolError: "ai.cliErrors.piAgentProtocolError",
  piAgentModelInvalid: "ai.cliErrors.piAgentModelInvalid",
  piAgentRunFailed: "ai.cliErrors.piAgentRunFailed",
  openCodeNotInstalled: "ai.cliErrors.openCodeNotInstalled",
  openCodeCliPathInvalid: "ai.cliErrors.openCodeCliPathInvalid",
  openCodeEnvInvalid: "ai.cliErrors.openCodeEnvInvalid",
  openCodeEnvReserved: "ai.cliErrors.openCodeEnvReserved",
  openCodeNotAuthenticated: "ai.cliErrors.openCodeNotAuthenticated",
  openCodeMcpStartupFailed: "ai.cliErrors.openCodeMcpStartupFailed",
  openCodeTimeout: "ai.cliErrors.openCodeTimeout",
  openCodeProtocolError: "ai.cliErrors.openCodeProtocolError",
  openCodeRunFailed: "ai.cliErrors.openCodeRunFailed",
  cursorNotInstalled: "ai.cliErrors.cursorNotInstalled",
  cursorCliPathInvalid: "ai.cliErrors.cursorCliPathInvalid",
  grokCliNotInstalled: "ai.cliErrors.grokCliNotInstalled",
  grokCliPathInvalid: "ai.cliErrors.grokCliPathInvalid",
  grokCliEnvInvalid: "ai.cliErrors.grokCliEnvInvalid",
  grokCliEnvReserved: "ai.cliErrors.grokCliEnvReserved",
  grokCliNotAuthenticated: "ai.cliErrors.grokCliNotAuthenticated",
  grokCliMcpStartupFailed: "ai.cliErrors.grokCliMcpStartupFailed",
  grokCliCommandLineTooLong: "ai.cliErrors.grokCliCommandLineTooLong",
  grokCliRunFailed: "ai.cliErrors.grokCliRunFailed",
  codeBuddyNotInstalled: "ai.cliErrors.codeBuddyNotInstalled",
  codeBuddyCliPathInvalid: "ai.cliErrors.codeBuddyCliPathInvalid",
  codeBuddyEnvInvalid: "ai.cliErrors.codeBuddyEnvInvalid",
  codeBuddyEnvReserved: "ai.cliErrors.codeBuddyEnvReserved",
  codeBuddyNotAuthenticated: "ai.cliErrors.codeBuddyNotAuthenticated",
  codeBuddyTimeout: "ai.cliErrors.codeBuddyTimeout",
  codeBuddyMcpConfigInvalid: "ai.cliErrors.codeBuddyMcpConfigInvalid",
  codeBuddyMcpStartupFailed: "ai.cliErrors.codeBuddyMcpStartupFailed",
  codeBuddyProtocolError: "ai.cliErrors.codeBuddyProtocolError",
  codeBuddyRunFailed: "ai.cliErrors.codeBuddyRunFailed",
  qoderNotInstalled: "ai.cliErrors.qoderNotInstalled",
  qoderCliPathInvalid: "ai.cliErrors.qoderCliPathInvalid",
  qoderEnvInvalid: "ai.cliErrors.qoderEnvInvalid",
  qoderEnvReserved: "ai.cliErrors.qoderEnvReserved",
  qoderNotAuthenticated: "ai.cliErrors.qoderNotAuthenticated",
  qoderTimeout: "ai.cliErrors.qoderTimeout",
  qoderMcpConfigInvalid: "ai.cliErrors.qoderMcpConfigInvalid",
  qoderMcpStartupFailed: "ai.cliErrors.qoderMcpStartupFailed",
  qoderProtocolError: "ai.cliErrors.qoderProtocolError",
  qoderRunFailed: "ai.cliErrors.qoderRunFailed",
  cursorEnvInvalid: "ai.cliErrors.cursorEnvInvalid",
  cursorEnvReserved: "ai.cliErrors.cursorEnvReserved",
  cursorNotAuthenticated: "ai.cliErrors.cursorNotAuthenticated",
  cursorMcpStartupFailed: "ai.cliErrors.cursorMcpStartupFailed",
  cursorTimeout: "ai.cliErrors.cursorTimeout",
  cursorProtocolError: "ai.cliErrors.cursorProtocolError",
  cursorRunFailed: "ai.cliErrors.cursorRunFailed",
};

const exactMessageKeys: Record<string, string> = {
  [PHOENIX_DRIVER_NOT_INSTALLED_ERROR]: "connection.phoenixDriverNotInstalled",
  [PHOENIX_JDBC_PLUGIN_NOT_INSTALLED_ERROR]: "connection.phoenixDriverNotInstalled",
};

// This catalog is audited against the Go/Java backends (2026-10): every pattern
// below has a live producer in internal/, agents/, or the frontend itself.
// Rust-era wordings whose producers were deleted with the Rust workspace were
// removed; do not re-add a pattern without naming its current producer.
const patterns: [RegExp, string][] = [
  // Plugin updates (internal/server/plugins_marketplace.go, plugins_package.go)
  [/^Plugin update source changed/, "pluginPlatform.updateSourceChangeRequired"],
  [/^Plugin downgrade requires explicit rollback$/, "pluginPlatform.updateDowngradeRejected"],

  // Driver / JRE management (internal/agent/store.go)
  [/^agent driver (.+?) is not installed$/, "connection.driverNotInstalled"],
  [/^managed JRE (.+?) is not installed; install it or configure system Java$/, "connection.jreNotInstalled"],

  // The GBase JDBC driver reports server-name mismatches with these variables
  // in the raw error text, which reaches the UI through the agent RPC envelope.
  [/GBASEDBTSERVER[\s\S]*DBSERVERNAME[\s\S]*DBSERVERALIASES/, "connection.gbaseServerMismatch"],

  // Query result export limits: raised by the frontend itself
  // (apps/desktop/src/stores/queryStore.ts).
  [/^Streaming export is unsupported for this query\. Simplify it or use a supported driver\.$/, "exportProgress.streamingUnsupported"],

  // Agent-raised messages arrive wrapped in "Agent RPC error (<code>): "
  // (internal/agent/runtime.go). The MongoDB agent throws this when a find
  // cursor has expired (agents/drivers/mongodb MongoAgent.java).
  [/^(?:Agent RPC error \(-?\d+\): )?Find cursor not found$/, "mongo.import.legacyCursorInvalid"],

  // Filesystem open/reveal failures surface in this shape.
  [/^file does not exist: (.+)$/, "common.fileNotFound"],
];

// Named placeholders for each pattern's capture groups, in capture order.
// A bare string is shorthand for a single capture group.
const paramNames: Record<string, string | string[]> = {
  "connection.driverNotInstalled": "driver",
  "connection.jreNotInstalled": "jre",
  "common.fileNotFound": "path",
};

function backendErrorMessage(error: unknown): string {
  if (typeof error === "string") return sanitizeBackendErrorMessage(error);
  if (error && typeof error === "object" && "message" in error && typeof error.message === "string") return sanitizeBackendErrorMessage(error.message);
  return sanitizeBackendErrorMessage(String(error));
}

function usableFallbackDetail(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const detail = sanitizeBackendErrorMessage(value).trim();
  if (!detail || detail === GENERIC_TRANSPORT_FAILURE_MESSAGE) return undefined;
  return detail;
}

/**
 * Resolves a raw backend message through the known-message catalog, or `null` when the message is
 * not one the UI knows how to phrase.
 */
function translateKnownMessage(t: BackendErrorTranslate, message: string): string | null {
  const exactKey = Object.prototype.hasOwnProperty.call(exactMessageKeys, message) ? exactMessageKeys[message] : undefined;
  if (exactKey) return t(exactKey);

  const tagged = message.match(/^\[([A-Za-z][A-Za-z0-9]+)\]\s*([\s\S]*)$/);
  if (tagged) {
    const [, code, rawDetail] = tagged;
    const key = taggedAiCliErrorKeys[code];
    if (key) {
      const detail = rawDetail.trim();
      return [t(key), t("ai.cliErrors.code", { code }), t("ai.cliErrors.reportHint"), detail ? `${t("ai.cliErrors.details")}\n${detail}` : ""].filter(Boolean).join("\n\n");
    }
  }

  for (const [regex, key] of patterns) {
    const match = message.match(regex);
    if (match) {
      const names = paramNames[key];
      if (names) {
        const ordered = Array.isArray(names) ? names : [names];
        const params: Record<string, string> = {};
        ordered.forEach((name, index) => {
          const captured = match[index + 1];
          if (captured !== undefined) params[name] = captured;
        });
        if (Object.keys(params).length > 0) return t(key, params);
      }
      return t(key);
    }
  }
  return translateCommonBackendError(t, message);
}

function translateStructuredBackendError(t: BackendErrorTranslate, error: BackendError, fallbackDetail?: unknown): string {
  const translated = t(error.messageKey, error.messageParams);
  const summary = translated !== error.messageKey ? translated : t("backendErrors.unknown");
  let detail = error.detail ? sanitizeBackendErrorMessage(error.detail).trim() : usableFallbackDetail(fallbackDetail);
  // Callers may pass an already-composited "summary\n\noriginal" cell as the
  // fallback (query history re-translation of a failed result grid); strip the
  // repeated summary so the message stays singular.
  if (detail && detail.startsWith(`${summary}\n\n`)) {
    detail = detail.slice(summary.length + 2).trim() || undefined;
  }
  // An unclassified message still goes through the same catalog the raw transports use, so a
  // message DBX knows reads the same whether it arrived wrapped or bare.
  if (error.code === LEGACY_BACKEND_ERROR_CODE && detail) {
    const known = translateKnownMessage(t, detail);
    if (known) return known;
  }
  const rawAdapterCode = error.diagnostics?.adapterCode;
  const adapterCode = typeof rawAdapterCode === "string" && /^[A-Za-z][A-Za-z0-9_.-]{0,63}$/.test(rawAdapterCode) ? rawAdapterCode : undefined;
  const diagnosticDetail = detail && adapterCode ? `[${adapterCode}] ${detail}` : (detail ?? adapterCode);
  return diagnosticDetail && diagnosticDetail !== summary ? `${summary}\n\n${diagnosticDetail}` : summary;
}

export function translateBackendError(t: BackendErrorTranslate, error: unknown, fallbackDetail?: unknown): string {
  const structured = normalizeBackendError(error);
  if (structured) return translateStructuredBackendError(t, structured, fallbackDetail);

  const message = backendErrorMessage(error);
  return translateKnownMessage(t, message) ?? message;
}
