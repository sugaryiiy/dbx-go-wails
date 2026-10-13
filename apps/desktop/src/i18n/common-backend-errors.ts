import type { BackendErrorTranslate } from "./backend-errors";

// Match complete application messages. Do not replace English fragments inside
// arbitrary driver errors: they can be SQL, identifiers or diagnostic values.
const messages: Record<string, string> = {
  "database connection is not open": "connectionNotOpen",
  "database connection is closed": "connectionClosed",
  "database connection has been closed": "connectionClosed",
  "database manager is closed": "serviceClosed",
  "connection was closed while connecting": "connectionChanged",
  "sqlite restore is in progress": "restoreInProgress",
  "sqlite restore is already in progress": "restoreInProgress",
  "writes are locked for this connection": "writesLocked",
  "writes are locked for this read-only or production connection": "writesLocked",
  "writes are locked for this read-only or production connection; unlock writes explicitly before executing this statement": "writesLocked",
  "execution id is already running": "queryAlreadyRunning",
  "an explicit transaction is already open in this tab; commit or roll it back first": "transactionOpen",
  "explicit transaction statements cannot be combined with automatic transactions": "transactionMix",
  "clientsessionid is required": "clientSessionRequired",
  "preserveexplicittransaction requires clientsessionid": "clientSessionRequired",
  "timeoutsecs must be between 0 and 86400": "invalidTimeout",
  "pagesize must be between 0 and 100000": "invalidPageSize",
  "rowoffset must be between 0 and 10000000": "invalidRowOffset",
  "execute requires exactly one sql statement; use execute-multi for scripts": "singleStatement",
  "rowoffset requires a select, values or show query": "rowOffsetRequiresQuery",
  "backup snapshots only allow read-only sql": "backupReadOnly",
  "session was rolled back and closed because writes are locked; execute the read again": "sessionRolledBack",
  "query result session expired or was closed; execute the query again": "resultSessionExpired",
  "query result session belongs to another connection or tab": "resultScopeMismatch",
  "too many retained results; close unused result tabs": "resultLimit",
  "retained result limit reached; close unused result tabs": "resultLimit",
  "retained results exceed 64 mib; close unused result tabs": "resultLimit",
  "invalid result pagination": "invalidPagination",
  "limit and offset cannot be negative": "invalidPagination",
  "unterminated sql string, identifier, or comment": "sqlUnclosed",
  "unbalanced sql parentheses": "sqlParentheses",
  "unterminated sqlite trigger body": "triggerUnclosed",
  "connection id is required": "connectionIdRequired",
  "config is required": "configRequired",
  "table is required": "tableRequired",
  "sqlite database file path is required": "sqlitePathRequired",
  "query is empty": "queryEmpty",
  "mcp access is not configured; configure permissions in database workbench settings": "mcpNotConfigured",
  "save an mcp access policy before enabling the service": "mcpNotConfigured",
  "tool is not allowed by mcp policy": "mcpToolDenied",
  "connection does not exist or is not allowed by mcp policy": "mcpConnectionDenied",
  "database is not allowed by mcp policy": "mcpDatabaseDenied",
  "mcp policy is read-only": "mcpReadOnly",
  "object does not exist or is not visible in the selected database/schema with that identity": "objectNotFound",
  "clickhouse object does not exist": "objectNotFound",
  "object name is ambiguous; provide its routine signature or trigger relation_name": "objectAmbiguous",
  "context deadline exceeded": "operationTimedOut",
  "context canceled": "operationCanceled",
  "context cancelled": "operationCanceled",
  "driver: bad connection": "connectionInterrupted",
  "sql: database is closed": "connectionClosed",
  "unauthorized": "loginRequired",
  "authentication required": "loginRequired",
  "permission denied": "accessDenied",
  "access denied": "accessDenied",
  "forbidden": "accessDenied",
};

function withDetail(t: BackendErrorTranslate, key: string, detail: string): string {
  return `${t(`backendErrors.common.${key}`)}\n\n${t("backendErrors.common.originalDetail", { detail })}`;
}

export function translateCommonBackendError(t: BackendErrorTranslate, message: string): string | null {
  const text = message.trim();
  const lookup = text.toLowerCase();
  const key = Object.prototype.hasOwnProperty.call(messages, lookup) ? messages[lookup] : undefined;
  if (key) return t(`backendErrors.common.${key}`);

  const unsupported = text.match(/^not implemented by the Go database engine(?:: (.+))?$/i);
  if (unsupported) return unsupported[1] ? t("backendErrors.common.unsupportedFeature", { feature: unsupported[1] }) : t("backendErrors.common.unsupported");

  const commit = text.match(/^transaction commit outcome is unknown; check the database before retrying(?:: ([\s\S]+))?$/);
  if (commit) return commit[1] ? withDetail(t, "commitUnknown", commit[1]) : t("backendErrors.common.commitUnknown");

  const refused = text.match(/^dial (?:tcp[46]?|unix) (.+): connect: connection refused$/i);
  if (refused) return t("backendErrors.common.connectionRefused", { address: refused[1] });
  const timeout = text.match(/^(?:dial|read|write) (?:tcp[46]?|unix) (.+): (?:i\/o timeout|context deadline exceeded)$/i);
  if (timeout) return t("backendErrors.common.networkTimedOut", { address: timeout[1] });
  const closed = text.match(/^(?:read|write) (?:tcp[46]?|unix) (.+): (?:use of closed network connection|connection reset by peer|broken pipe)$/i);
  if (closed) return t("backendErrors.common.networkInterrupted", { address: closed[1] });
  const host = text.match(/^dial tcp[46]?: lookup (\S+?)(?: on [^\n]+)?: no such host$/i);
  if (host) return t("backendErrors.common.hostNotFound", { host: host[1] });

  // Driver diagnostics remain available after the localized explanation. In
  // particular, SQLSTATE/error codes and literal database names are preserved.
  if (/^Error 1045 \(28000\): Access denied for user /i.test(text) || /^ERROR: password authentication failed for user .+ \(SQLSTATE 28P01\)$/i.test(text) || /^failed to connect to [\s\S]+\(SQLSTATE 28P01\)$/i.test(text)) return withDetail(t, "authenticationFailed", text);
  if (/^Error (?:1044|1142|1227) \([A-Z0-9]+\): /i.test(text) || /^ERROR: permission denied [\s\S]+\(SQLSTATE 42501\)$/i.test(text)) return withDetail(t, "accessDenied", text);
  if (/^Error 1064 \(42000\): /i.test(text) || /^ERROR: syntax error [\s\S]+\(SQLSTATE 42601\)$/i.test(text)) return withDetail(t, "sqlSyntax", text);
  if (/^(?:tls:|x509:)/.test(text)) return withDetail(t, "tlsFailed", text);
  return null;
}
