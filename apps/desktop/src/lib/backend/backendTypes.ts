// Shared backend contract types.
// Extracted from the former Tauri transport module: these types describe the
// backend API surface shared by every transport and by the api facade.

import type { DatabaseType, ExternalSqlFileVersion, ColumnInfo } from "@/types/database";
import type { AiConfig, AiEffortLevel, AiEffortCapability } from "@/types/ai";
import type { AiToolApprovalOutcome } from "@/types/pluginAiTools";
import type { BackendError } from "@/lib/backend/errorUtils";
import type { SqlInsertDialect, SqlInsertMode, SqlExportColumnSelection } from "@/lib/export/sqlInsertMode";
import type { CsvQuoteMode } from "@/lib/export/csvQuoteMode";

export interface SshPromptResolution {
  id: string;
  action: "accept" | "reject" | "secret";
  remember?: boolean;
  secret?: string;
}

export interface AgentDriverInfo {
  db_type: string;
  label: string;
  version: string;
  size: number;
  installed: boolean;
  installed_version: string | null;
  update_available: boolean;
  requires_java_runtime?: boolean;
  jre: string;
  jre_installed: boolean;
}

export interface AgentDriverUpdateIssue {
  db_type: string;
  error: string;
}

export interface UpgradeAllAgentDriversResult {
  upgraded: number;
  /** Drivers whose install was aborted by a user cancel (single-driver or batch). */
  cancelled: number;
  failed: AgentDriverUpdateIssue[];
}

export interface AgentUpdateBlocker {
  db_type: string;
  label: string;
  connections: string[];
}

export type AgentOfflineArtifactKind = "jar" | "native";

export type AgentOfflineExportUnavailableReason = "unmanagedInstall" | "localInstall" | "launchConfig" | "missingArtifact" | "invalidArtifact" | "unsafeSource" | "externalDriverRequired" | "missingManagedJre" | "invalidManagedJre";

export interface AgentOfflineExportCandidate {
  dbType: string;
  label: string;
  version: string;
  size: number;
  artifactKind: AgentOfflineArtifactKind | null;
  requiredJre: string | null;
  eligible: boolean;
  unavailableReason: AgentOfflineExportUnavailableReason | null;
}

export interface AgentOfflineExportPreview {
  platform: string;
  candidates: AgentOfflineExportCandidate[];
}

export interface AgentOfflineExportResult {
  platform: string;
  driverCount: number;
  jreCount: number;
  bytes: number;
}

export interface AgentOfflineImportResult {
  count: number;
  jreCount: number;
  /** Items the package could not install; the rest of the import still ran. */
  failures: AgentOfflineImportFailure[];
}

export interface AgentOfflineImportFailure {
  /** Managed JRE key (e.g. "21") or driver key (e.g. "oracle"). */
  key: string;
  /** True when the failed item is a managed JRE runtime rather than a driver. */
  is_jre: boolean;
  /** Failure text, including the underlying OS error when there is one. */
  error: string;
}

export type JavaRuntimeMode = "managed" | "system" | "custom";

export interface JavaRuntimeConfig {
  mode: JavaRuntimeMode;
  custom_java_path: string | null;
}

export interface DriverStoreUsageItem {
  id: string;
  bytes: number;
}

export interface DriverStoreUsage {
  total_bytes: number;
  jre_bytes: number;
  agent_driver_bytes: number;
  download_cache_bytes?: number;
  jdbc_plugin_bytes: number;
  jdbc_driver_bytes: number;
  jres: DriverStoreUsageItem[];
  agent_drivers: DriverStoreUsageItem[];
}

export type DriverRuntimeHealth = "healthy" | "warning" | "error";

export type DriverRuntimeStatus = "running" | "stopped" | "error" | "unknown";

export interface DriverRuntimeInfo {
  id: string;
  driver_key: string;
  label: string;
  kind: string;
  source: string;
  status: DriverRuntimeStatus;
  pid: number | null;
  memory_bytes: number | null;
  cpu_percent: number | null;
  uptime_seconds: number | null;
  version: string | null;
  last_error: string | null;
  can_stop: boolean;
  can_restart: boolean;
  control_unavailable_reason: string | null;
  protocol_mode: "multi_session" | "legacy" | null;
  active_sessions: number | null;
}

export interface DriverRuntimeSummary {
  running_count: number;
  total_memory_bytes: number;
  last_error: string | null;
  health: DriverRuntimeHealth;
  runtimes: DriverRuntimeInfo[];
}

export interface DesktopSettings {
  show_tray_icon: boolean;
  icon_theme: "default" | "black";
  quit_on_close: boolean;
  close_action_prompted: boolean;
  debug_logging_enabled: boolean;
  metadata_cache_max_memory_mb: number;
  duckdb_worker_process_isolation: boolean;
  duckdb_worker_max_processes: number;
  saved_sql_sync_dir?: string | null;
  driver_store_dir?: string | null;
  plugin_store_dir?: string | null;
  agent_store_dir?: string | null;
  custom_ai_skill_root_enabled?: boolean | null;
  custom_ai_skill_root?: string | null;
  sidebar_table_page_size?: number | null;
}

export interface McpGlobalPolicy {
  readOnly: boolean;
  allowDangerousSql: boolean;
  allowedConnectionIds: string[] | null;
  allowedGroupIds: string[];
  allowedToolNames: string[] | null;
  connectionPolicies: McpConnectionPolicy[];
  groupPolicies: McpGroupPolicy[];
  configured: boolean;
  queryTimeoutSecs: number | null;
}

export interface McpGroupPolicy {
  groupId: string;
  readOnly: boolean;
  allowDangerousSql: boolean;
}

export interface McpConnectionPolicy {
  connectionId: string;
  readOnly: boolean;
  allowDangerousSql: boolean;
  executionModeConfigured: boolean;
  executionModePolicyVersion: number | null;
  databaseScope: "all" | "selected" | "none";
  allowedDatabases: string[];
  databasePolicies: McpDatabasePolicy[];
  /** Opt-in for AI-agent DML against a Salesforce org; forced off by `readOnly`. */
  allowSalesforceDml: boolean;
}

export interface McpDatabasePolicy {
  databaseName: string;
  readOnly: boolean;
  allowDangerousSql: boolean;
}

export interface SavedSqlSyncEntry {
  folderName?: string;
  fileName: string;
  sql: string;
}

export interface SavedSqlSyncRequest {
  targetDir: string;
  entries: SavedSqlSyncEntry[];
}

export interface WebDavConfig {
  endpoint: string;
  username?: string;
  password?: string;
  remotePath?: string;
}

export interface WebDavSyncSummary {
  remotePath: string;
  bytes: number;
  exportedAt?: string;
  appVersion?: string;
}

export interface SyncCatalogItem {
  id: string;
  label: string;
}

export interface PluginUiStorageItemRef {
  pluginId: string;
  key: string;
  pluginName?: string;
}

export interface SyncSelection {
  connections?: string[];
  connectionSecrets?: string[];
  tunnelProfiles?: string[];
  tunnelSecrets?: string[];
  savedSqlFolders?: string[];
  savedSqlFiles?: string[];
  desktopSettings?: string[];
  editorSettings?: string[];
  aiConfigs?: string[];
  pluginUiStorage?: PluginUiStorageItemRef[];
  sidebarLayout?: boolean;
  pinnedTreeNodeIds?: boolean;
  includeSecrets: boolean;
  syncCredentials: boolean;
}

export interface SyncSnapshotCatalog {
  exportedAt: string;
  appVersion: string;
  hasEncryptedSecrets: boolean;
  connections: SyncCatalogItem[];
  connectionSecrets: string[];
  tunnelProfiles: SyncCatalogItem[];
  tunnelSecrets: string[];
  savedSqlFolders: SyncCatalogItem[];
  savedSqlFiles: SyncCatalogItem[];
  desktopSettings: SyncCatalogItem[];
  editorSettings: SyncCatalogItem[];
  aiConfigs: SyncCatalogItem[];
  aiConfigsLocked: boolean;
  pluginUiStorage: PluginUiStorageItemRef[];
  pluginUiStorageLocked: boolean;
  hasSidebarLayout: boolean;
  hasPinnedTreeNodeIds: boolean;
  selection?: SyncSelection;
}

export interface WebDavDownloadResult {
  summary: WebDavSyncSummary;
  editorSettings?: unknown;
  desktopSettings: DesktopSettings;
  applySummary: {
    encryptedSecretsPresent: boolean;
    secretsApplied: boolean;
  };
}

export interface LocalBackupImportResult {
  editorSettings?: unknown;
  desktopSettings: DesktopSettings;
  applySummary: WebDavDownloadResult["applySummary"];
}

export interface LocalBackupExportSummary {
  bytes: number;
}

export interface WebDavPasswordStatus {
  hasSavedPassword: boolean;
}

export interface WebDavSyncSecretsStatus {
  enabled: boolean;
  hasSavedPassphrase: boolean;
}

export type SnippetProvider = "github" | "gitee" | "gitlab";

export interface SnippetSyncConfig {
  provider: SnippetProvider;
  instanceUrl?: string;
  token?: string;
  snippetId?: string;
  replaceLegacySnippet?: boolean;
}

export interface SnippetSyncSettings {
  snippetId?: string;
  legacyCleanupRequiredId?: string;
}

export interface SnippetSyncSummary {
  provider: SnippetProvider;
  snippetId: string;
  bytes: number;
  exportedAt?: string;
  appVersion?: string;
  legacyCleanupRequiredId?: string;
}

export interface SnippetDownloadResult {
  summary: SnippetSyncSummary;
  editorSettings?: unknown;
  desktopSettings: DesktopSettings;
  applySummary: WebDavDownloadResult["applySummary"];
}

export interface SnippetTokenStatus {
  hasSavedToken: boolean;
}

export interface AppSupportInfo {
  appVersion: string;
  runtime: "desktop" | "web";
  osName: string;
  osVersion?: string | null;
  arch: string;
  userAgent?: string;
  databaseTypes?: string[];
  localDriverVersions?: Array<{ dbType: string; version: string }>;
  aiProviders?: string[];
}

export interface QueryPagination {
  limit: number;
  offset: number;
  sessionId?: string;
}

export interface QueryPaginationExecutionPlanOptions {
  sql: string;
  queryBaseSql: string;
  databaseType?: DatabaseType;
  pagination: QueryPagination;
  useAgentCursor: boolean;
  firstPageUsesActualSql?: boolean;
}

export interface QueryPaginationExecutionPlan {
  sqlToExecute: string;
  pageSql?: string;
  pageLimit?: number;
  pageOffset?: number;
  countSql?: string;
  exactQueryRowBound?: number;
  useAgentResultSession: boolean;
  paginationRowNumberColumn?: string;
  paginationError?: string;
}

export type QuerySortDirection = "asc" | "desc";

export interface SortedQuerySqlOptions {
  originalSql: string;
  databaseType?: DatabaseType;
  resultColumns: string[];
  columnIndex: number;
  column: string;
  direction: QuerySortDirection;
}

export interface QuerySqlBuildResult {
  ok: boolean;
  sql?: string;
  reason?: "empty" | "multi" | "not_select" | "unsupported" | "with";
}

export interface BuildExplainSqlOptions {
  databaseType?: DatabaseType;
  sql: string;
  /** MySQL can return either the existing JSON plan or its native tabular plan. */
  format?: "json" | "standard";
  /** PostgreSQL only: run the statement so the plan carries measured rows and timings. */
  analyze?: boolean;
}

export interface ExplainSqlBuildResult {
  ok: boolean;
  sql?: string;
  reason?: "unsupported" | "empty" | "unsafe";
}

export interface DroppedFilePreviewSqlOptions {
  path: string;
  limit?: number;
}

export type XlsxCellValue = string | number | boolean | null;

export interface DriverInstallProgress {
  operation_id?: string;
  step: string;
  downloaded?: number;
  total?: number;
  db_type?: string;
  current?: number;
  total_drivers?: number;
}

export interface AiMessage {
  role: "user" | "assistant" | "system";
  content: string;
  /** Transient images for this message. Persisted conversation history intentionally omits them. */
  images?: Array<{
    mediaType: string;
    data: string;
  }>;
}

export interface AiTaskContract {
  action?: string;
  mode?: string;
  userRequest?: string;
}

export interface AiCompletionRequest {
  config: AiConfig;
  systemPrompt: string;
  messages: AiMessage[];
  taskContract?: AiTaskContract;
  maxTokens?: number;
  /** Stable per-conversation key used by the Responses API prompt cache. */
  promptCacheKey?: string;
}

export interface AiModelInfo {
  id: string;
  displayName?: string;
  supportedEffortLevels?: AiEffortLevel[];
  effortCapability?: AiEffortCapability;
}

export interface AiStreamChunk {
  session_id: string;
  delta: string;
  reasoning_delta?: string;
  done: boolean;
  /** Web-only explicit terminal error; Tauri reports invoke failures directly. */
  error?: string;
}

export type AgentEvent =
  | { type: "turn_start"; turn: number }
  | { type: "text_delta"; delta: string }
  | { type: "write_sql_confirmation_required"; sql: string }
  | { type: "production_write_blocked"; sql: string }
  | { type: "reasoning_delta"; delta: string }
  | {
      type: "tool_call_start";
      tool_call_id: string;
      tool_name: string;
      args: Record<string, unknown>;
    }
  | {
      /** A plugin tool call that may change state waits for the user's approval; the run is paused. */
      type: "tool_approval_required";
      approval_id: string;
      tool_call_id: string;
      tool_name: string;
      plugin_id: string;
      plugin_name: string;
      plugin_tool: string;
      connection_id: string;
      connection_name: string;
      /** Exactly the arguments DBX forwards if the user approves. */
      args: Record<string, unknown>;
      timeout_secs: number;
    }
  | { type: "tool_approval_resolved"; approval_id: string; tool_call_id: string; outcome: AiToolApprovalOutcome }
  | {
      type: "tool_call_end";
      tool_call_id: string;
      tool_name: string;
      result: unknown;
      is_error: boolean;
    }
  | { type: "turn_end"; turn: number }
  | {
      /**
       * The reply stream is fully consumed but the run is NOT yet confirmed
       * successful — the CLI process may still exit non-zero or hang after
       * closing stdout. Non-terminal: the UI may stop the reply animation on
       * it, but must keep listening for the real `agent_end` (success) /
       * `error` (failure).
       */
      type: "response_complete";
    }
  | { type: "agent_end"; input_tokens?: number; output_tokens?: number }
  | {
      type: "context_compacted";
      summary: string;
      summary_tokens: number;
      compacted_messages: number;
      estimated_before: number;
      estimated_after: number;
    }
  | { type: "error"; message: string };

export interface McpHttpServerSettings {
  enabled: boolean;
  host: string;
  port: number;
  path: string;
  allowRemote: boolean;
  allowedHosts: string[];
  allowedOrigins: string[];
}

export interface McpHttpServerStatus {
  enabled: boolean;
  running: boolean;
  endpoint: string | null;
  accessToken: string | null;
  lastError: string | null;
  recentLogs: string[];
}

export interface WebMcpHttpStatus {
  enabled: boolean;
  endpointPath: string;
  tokenSource: "environment" | "file" | "managed" | null;
  allowedHosts: string[];
  allowedOrigins: string[];
  deploymentManaged: boolean;
  managementAvailable: boolean;
  accessToken: string | null;
}

export interface WebMcpHttpSettings {
  enabled: boolean;
  allowedHosts: string[];
  allowedOrigins: string[];
}

export interface GlobalSearchSettings {
  roots: string[];
  extensions: string[];
}

export interface BackgroundImageInfo {
  storedPath: string;
  fileName: string;
}

export interface DriverStoreMigrationResult {
  driver_store_dir: string | null;
  plugin_store_dir: string | null;
  agent_store_dir: string | null;
  plugins_dir: string;
  agents_dir: string;
  migrated_plugins: boolean;
  migrated_agents: boolean;
}

export interface DriverStorePathInfo {
  driver_store_dir: string | null;
  plugin_store_dir: string | null;
  agent_store_dir: string | null;
  plugins_dir: string;
  agents_dir: string;
}

export interface ExternalSqlFileSnapshot {
  content: string;
  version: ExternalSqlFileVersion;
}

export type ExternalSqlFileStatus = { kind: "present"; sizeBytes: number; modifiedNs: string } | { kind: "missing" };

export type ExternalSqlFileWriteResult = { kind: "written"; version: ExternalSqlFileVersion } | { kind: "conflict"; currentVersion: ExternalSqlFileVersion } | { kind: "missing" };

export interface SqlFileEntry {
  name: string;
  path: string;
  is_dir: boolean;
  children: SqlFileEntry[];
}

export interface GlobalSearchRequest {
  roots: string[];
  query: string;
  extensions?: string[];
  caseSensitive?: boolean;
  useRegex?: boolean;
  wholeWord?: boolean;
  limit?: number;
}

export interface GlobalSearchMatch {
  path: string;
  fileName: string;
  /** 1-based line number. */
  line: number;
  /** 1-based char column within the line (for CodeMirror). */
  column: number;
  matchText: string;
  lineText: string;
}

export interface AiChatMessage {
  role: string;
  content: string;
  mentions?: unknown[];
  reasoning?: string;
  kind?: "contextSummary" | "writeSqlConfirmation" | "productionWriteBlocked";
  /** Set on the assistant message whose generation failed; persisted (mirrors dbx-core `AiChatMessage.failed`). */
  failed?: boolean;
  /** Target frozen when this assistant turn started, retained for confirmation. */
  sourceBinding?: import("@/lib/ai/aiConversationBinding").AiConversationBinding;
  /**
   * Footprint of a turn that carried a context selection (#10058). The selection
   * text is never persisted (it can be 12 000 chars and records are
   * cloud-synced), so this boolean is all a reloaded transcript has left to say
   * the turn was not empty. Absent on records written before the field existed.
   */
  selectionsOmitted?: boolean;
}

export interface AiConversation {
  pluginContext?: import("@/lib/ai/aiPluginConversation").AiPluginContext;
  id: string;
  title: string;
  connectionName: string;
  /** Connection this conversation is bound to (#9902). The binding belongs to
   *  the conversation, not to whichever editor tab is active.
   *  Empty means "unbound": either persisted before session-scoped binding
   *  existed and its `connectionName` matched zero or several saved connections
   *  (names are not unique), or the bound connection was deleted. Consumers must
   *  ask the user rather than fall back to the active tab. */
  connectionId: string;
  database: string;
  /** Schema for schema-scoped engines (Postgres, Dameng); absent otherwise. */
  schema?: string;
  messages: AiChatMessage[];
  /** One editable "send later" input saved while an active run occupies the
   *  conversation (parent PRD §5). Persisted with the conversation. */
  queuedInput?: string;
  createdAt: string;
  updatedAt: string;
}

export type AiRunStatus = "preparing" | "queued" | "running" | "awaiting_write_confirmation" | "completed" | "failed" | "cancelled" | "interrupted" | "pending_recoverable";

export type AiRunFifoCategory = "normal_send" | "write_confirmation_resume";

export interface AiRun {
  runId: string;
  conversationId: string;
  sessionIds: string[];
  status: AiRunStatus;
  connectionId: string;
  database: string;
  schema?: string;
  pendingConfirmation?: unknown;
  fifoCategory?: AiRunFifoCategory;
  pendingInput?: string;
  /** Highest event seq assigned to this run across all its sessions (parent
   *  PRD §8). Drives the unread baseline and the "updates while you were away"
   *  separator anchor. */
  maxSeq?: number;
  createdAt: string;
  updatedAt: string;
}

export interface PromptTemplate {
  id: string;
  name: string;
  content: string;
  createdAt: string;
  updatedAt: string;
}

export interface WriteUnlockState {
  remainingMs: number;
}

export interface TableColumnsResult {
  table_name: string;
  columns: ColumnInfo[];
  error?: string;
}

export interface ExecuteMultiProgress {
  executionId: string;
  statementIndex: number;
  completed: number;
  total: number;
  success: boolean;
  executionTimeMs: number;
  affectedRows: number;
  error?: BackendError;
}

export interface ConditionalUpdateCancellationResult {
  requested: boolean;
  terminal: boolean;
}

export interface DataGridSaveGuard {
  sql: string;
  maxMatchedRows: number;
  message: string;
}

export interface DataGridSavePreparation {
  validationError?: string;
  statements: string[];
  rollbackStatements: string[];
  executionSchema?: string;
  keylessGuards?: DataGridSaveGuard[];
}

export interface TablePartitionStatus {
  isPartitionedParent: boolean;
  isPartition: boolean;
}

export interface DocsCollectProgress {
  completed: number;
  total: number;
  current: string;
}

export interface PluginLocalFileHandle {
  /** Opaque uuid string from the Rust registry — never a number (JS doubles lose precision above 2^53). */
  handleId: string;
  name: string;
  size: number;
  contentType: string;
  write: boolean;
  /** Only for files expanded out of a dropped folder: '/'-separated path relative to the dropped folder root. */
  relativePath?: string;
}

export interface PluginLocalFileChunk {
  dataBase64: string;
  length: number;
  eof: boolean;
}

export interface PluginLocalFileWriteResult {
  written: number;
  nextOffset: number;
}

export interface PluginDroppedFilesResult {
  dropId: string;
  files: PluginLocalFileHandle[];
  truncated: boolean;
}

export interface UpdateInfo {
  current_version: string;
  latest_version: string;
  update_available: boolean;
  portable_mode: boolean;
  manual_update_only: boolean;
  release_name: string;
  release_url: string;
  release_notes: string;
}

export type UpdateDownloadSource = "official" | "cnb";

export interface DownloadedUpdate {
  cache_id: string;
  version: string;
  portable_mode: boolean;
  release_url: string;
  release_notes: string;
  downloaded_at: number;
}

export interface UpdateDownloadProgress {
  attempt_id: string;
  version: string;
  downloaded: number;
  total: number | null;
}

export interface McpServerStatus {
  installed: boolean;
  installation_source: "native" | "homebrew" | "npm" | null;
  npm_available: boolean;
  npm_installed: boolean;
  node_path: string | null;
  node_version: string | null;
  current_version: string | null;
  latest_version: string | null;
  update_available: boolean;
  bin_path: string | null;
  native_bin_path: string | null;
  script_path: string | null;
  data_dir: string | null;
  install_command: string;
  update_command: string;
  uninstall_command: string;
  error: string | null;
}

export interface RedisKeyInfo {
  key_display: string;
  key_raw: string;
  key_type?: string;
  ttl?: number;
  size?: number;
  value_preview?: string;
}

export interface RedisDatabaseInfo {
  db: number;
  /** 该库的键数量；服务端无法给出可信数量时（如 kvrocks 未执行过 DBSIZE SCAN）缺省。 */
  keys?: number;
}

export type RedisBlobEncoding = "utf8" | "binary";

export interface RedisBlob {
  raw_base64: string;
  encoding: RedisBlobEncoding;
}

export interface RedisListItem {
  index: number;
  value: RedisBlob;
}

export interface RedisSetItem {
  member: RedisBlob;
}

export interface RedisHashItem {
  field: RedisBlob;
  value: RedisBlob;
  field_ttl?: number;
}

export interface RedisZsetItem {
  score: string;
  member: RedisBlob;
}

export interface RedisKeysExpiryResult {
  applied: number;
  missing_key_raws: string[];
}

export interface RedisStreamField {
  field: string;
  value: string;
}

export interface RedisStreamEntry {
  id: string;
  fields: RedisStreamField[];
}

export interface RedisStreamPage {
  entries: RedisStreamEntry[];
  next_cursor?: string;
}

export type RedisStreamMetric = number | string;

export interface RedisStreamGroup {
  name: RedisBlob;
  consumers: RedisStreamMetric;
  pending: RedisStreamMetric;
  last_delivered_id: string;
  entries_read?: RedisStreamMetric;
  lag?: RedisStreamMetric;
}

export interface RedisStreamConsumer {
  name: RedisBlob;
  pending: RedisStreamMetric;
  idle_ms: RedisStreamMetric;
  inactive_ms?: RedisStreamMetric;
}

export interface RedisStreamPendingEntry {
  id: string;
  consumer: RedisBlob;
  idle_ms: RedisStreamMetric;
  deliveries: RedisStreamMetric;
}

export interface RedisStreamPendingPage {
  entries: RedisStreamPendingEntry[];
  next_cursor?: string;
}

export type RedisValueData =
  | { kind: "string"; content: RedisBlob; total_bytes?: number; truncated?: boolean }
  | { kind: "json"; value: string }
  | {
      kind: "list";
      items: RedisListItem[];
      total: number;
      scan_cursor?: number;
    }
  | { kind: "set"; items: RedisSetItem[]; total: number; scan_cursor?: number }
  | {
      kind: "hash";
      items: RedisHashItem[];
      total: number;
      scan_cursor?: number;
    }
  | {
      kind: "zset";
      items: RedisZsetItem[];
      total: number;
      scan_cursor?: number;
    }
  | { kind: "stream"; entries: RedisStreamEntry[]; total?: number; next_cursor?: string }
  // kvrocks 把位图/HLL 实现为独立类型（TYPE 返回 bitmap / hyperloglog），
  // 这里单独建模，避免落到 unknown 后界面显示不出值。
  | { kind: "bitmap"; content: RedisBlob; total_bytes?: number; truncated?: boolean; set_bits?: number }
  | { kind: "hyperloglog"; count?: number }
  | { kind: "unknown"; redis_type: string };

export interface RedisValue {
  key_display: string;
  key_raw: string;
  ttl: number;
  redis_type: string;
  data: RedisValueData;
}

export type RedisCollectionPage = { kind: "list"; items: RedisListItem[]; scan_cursor?: number } | { kind: "set"; items: RedisSetItem[]; scan_cursor?: number } | { kind: "hash"; items: RedisHashItem[]; scan_cursor?: number } | { kind: "zset"; items: RedisZsetItem[]; scan_cursor?: number };

export interface RedisScanResult {
  cursor: number;
  keys: RedisKeyInfo[];
  total_keys: number;
}

export type RedisCommandSafety = "allowed" | "write" | "confirm" | "blocked";

export interface RedisCommandResult {
  command: string;
  safety: RedisCommandSafety;
  value: any;
}

export interface RedisSlowlogEntry {
  id: number;
  timestamp: number;
  duration_micros: number;
  command: string;
  client_addr: string | null;
  client_name: string | null;
}

export interface RedisNodeEndpoint {
  host: string;
  port: number;
}

export type KvValueEncoding = "utf8" | "base64";

export type KvInt64 = string;

export interface KvValue {
  encoding: KvValueEncoding;
  data: string;
}

export interface KvKeyMetadata {
  createRevision?: KvInt64 | number | null;
  modRevision?: KvInt64 | number | null;
  version?: KvInt64 | number | null;
  lease?: KvInt64 | number | null;
  ttl?: number | null;
  valueSize?: number | null;
  czxid?: number | null;
  mzxid?: number | null;
  pzxid?: number | null;
  ctime?: number | null;
  mtime?: number | null;
  cversion?: number | null;
  aversion?: number | null;
  ephemeralOwner?: number | null;
  dataLength?: number | null;
  numChildren?: number | null;
  flags?: KvInt64 | number | null;
  lockIndex?: KvInt64 | number | null;
  session?: string | null;
}

export interface KvKeySummary extends KvKeyMetadata {
  key: string;
  keyIdentity?: string | null;
  keyBytes?: KvValue | null;
  value?: KvValue | null;
}

export interface KvListPrefixResponse {
  keys: KvKeySummary[];
  continuation?: string | null;
  revision?: KvInt64 | number | null;
  filteredByAcls?: boolean | null;
}

export interface KvListPrefixOptions {
  recursive?: boolean | null;
  revision?: KvInt64 | null;
  includeValues?: boolean | null;
}

export interface KvGetResponse {
  found: boolean;
  key?: string | null;
  keyIdentity?: string | null;
  keyBytes?: KvValue | null;
  value?: KvValue | null;
  metadata?: KvKeyMetadata | null;
}

export interface KvGetOptions {
  metadataOnly?: boolean | null;
  keyBytes?: KvValue | null;
  revision?: KvInt64 | null;
}

export interface KvPutResponse {
  revision?: number | null;
  version?: number | null;
  mtime?: number | null;
  key?: string | null;
  createdKey?: string | null;
}

export type KvWriteMode = "upsert" | "create" | "update";

export type KvCreateMode = "persistent" | "ephemeral" | "persistent_sequential" | "ephemeral_sequential";

export interface KvPutOptions {
  lease?: KvInt64 | number | null;
  ttl?: number | null;
  preserveLease?: boolean | null;
  writeMode?: KvWriteMode | null;
  createMode?: KvCreateMode | null;
  keyBytes?: KvValue | null;
  expectedModRevision?: KvInt64 | null;
  expectedCreateRevision?: KvInt64 | null;
  flags?: KvInt64 | null;
}

export interface KvDeleteOptions {
  keyBytes?: KvValue | null;
  expectedModRevision?: KvInt64 | null;
}

export interface KvDeleteResponse {
  deleted: number;
  revision?: KvInt64 | number | null;
}

export type KvHistoryEventType = "put" | "delete";

export interface KvHistoryEvent {
  eventType: KvHistoryEventType;
  revision: KvInt64;
  value?: KvValue | null;
  previousValue?: KvValue | null;
  metadata?: KvKeyMetadata | null;
}

export interface KvHistoryResponse {
  events: KvHistoryEvent[];
  observedRevision: KvInt64;
  truncated: boolean;
}

export interface KvStatusMember {
  endpoint: string;
  memberId?: KvInt64 | null;
  name?: string | null;
  version?: string | null;
  leaderId?: KvInt64 | null;
  revision?: KvInt64 | null;
  raftTerm?: KvInt64 | null;
  raftIndex?: KvInt64 | null;
  raftAppliedIndex?: KvInt64 | null;
  dbSize?: KvInt64 | null;
  dbSizeInUse?: KvInt64 | null;
  learner: boolean;
  reachable: boolean;
  latencyMs?: number | null;
  errors: string[];
}

export interface KvPrometheusMetrics {
  available: boolean;
  sourceUrl?: string | null;
  error?: string | null;
  collectedAtMs?: number | null;
  sampleCount?: number | null;
  serverVersion?: string | null;
  clusterVersion?: string | null;
  goVersion?: string | null;
  authRevision?: number | null;
  hasLeader?: number | null;
  isLeader?: number | null;
  leaderChangesTotal?: number | null;
  proposalsCommittedTotal?: number | null;
  proposalsAppliedTotal?: number | null;
  proposalsPending?: number | null;
  proposalsFailedTotal?: number | null;
  grpcRequestsTotal?: number | null;
  grpcFailuresTotal?: number | null;
  grpcMethodRequestsTotal: Record<string, number>;
  grpcMethodFailuresTotal: Record<string, number>;
  requestDurationSecondsSumByType: Record<string, number>;
  requestDurationSecondsCountByType: Record<string, number>;
  mvccPutTotal?: number | null;
  mvccDeleteTotal?: number | null;
  mvccRangeTotal?: number | null;
  mvccTxnTotal?: number | null;
  mvccCurrentRevision?: number | null;
  mvccCompactRevision?: number | null;
  mvccKeysTotal?: number | null;
  mvccEventsTotal?: number | null;
  mvccPendingEventsTotal?: number | null;
  mvccSlowWatcherTotal?: number | null;
  mvccWatchStreamTotal?: number | null;
  mvccWatcherTotal?: number | null;
  mvccTotalPutSizeBytes?: number | null;
  openReadTransactions?: number | null;
  leaseGrantedTotal?: number | null;
  leaseRenewedTotal?: number | null;
  leaseRevokedTotal?: number | null;
  leaseExpiredTotal?: number | null;
  leaseTtlSecondsSum?: number | null;
  leaseTtlSecondsCount?: number | null;
  clientReceivedBytesTotal?: number | null;
  clientSentBytesTotal?: number | null;
  peerReceivedBytesTotal?: number | null;
  peerSentBytesTotal?: number | null;
  peerReceivedFailuresTotal?: number | null;
  peerSentFailuresTotal?: number | null;
  walFsyncDurationSecondsSum?: number | null;
  walFsyncDurationSecondsCount?: number | null;
  walWriteBytesTotal?: number | null;
  walWriteDurationSecondsSum?: number | null;
  walWriteDurationSecondsCount?: number | null;
  backendCommitDurationSecondsSum?: number | null;
  backendCommitDurationSecondsCount?: number | null;
  backendSnapshotDurationSecondsSum?: number | null;
  backendSnapshotDurationSecondsCount?: number | null;
  backendDefragDurationSecondsSum?: number | null;
  backendDefragDurationSecondsCount?: number | null;
  diskDefragInflight?: number | null;
  snapshotApplyInProgress?: number | null;
  quotaBackendBytes?: number | null;
  knownPeers?: number | null;
  heartbeatSendFailuresTotal?: number | null;
  readIndexesFailedTotal?: number | null;
  slowApplyTotal?: number | null;
  slowReadIndexesTotal?: number | null;
  healthSuccessTotal?: number | null;
  healthFailuresTotal?: number | null;
  residentMemoryBytes?: number | null;
  virtualMemoryBytes?: number | null;
  cpuSecondsTotal?: number | null;
  processStartTimeSeconds?: number | null;
  processReceivedBytesTotal?: number | null;
  processTransmittedBytesTotal?: number | null;
  openFds?: number | null;
  maxFds?: number | null;
  goroutines?: number | null;
  goThreads?: number | null;
  goMaxProcs?: number | null;
  goHeapAllocBytes?: number | null;
  goHeapInuseBytes?: number | null;
  goHeapSysBytes?: number | null;
  goHeapObjects?: number | null;
  goNextGcBytes?: number | null;
  goGcDurationSecondsSum?: number | null;
  goGcDurationSecondsCount?: number | null;
  dbSizeMetricBytes?: number | null;
  dbSizeInUseMetricBytes?: number | null;
}

export interface KvStatusResponse {
  clusterId?: KvInt64 | null;
  revision?: KvInt64 | null;
  leaderId?: KvInt64 | null;
  keyCount?: KvInt64 | null;
  alarms: string[];
  members: KvStatusMember[];
  metrics?: KvPrometheusMetrics | null;
}

export interface EtcdDefragMemberResult {
  endpoint: string;
  status: "succeeded" | "failed" | "not_executed";
  durationMs?: number | null;
  error?: string | null;
}

export interface EtcdDefragResponse {
  members: EtcdDefragMemberResult[];
}

export interface EtcdWatchStartRequest {
  key: string;
  keyBytes?: KvValue | null;
  scope: "key" | "prefix";
  startRevision?: KvInt64 | null;
  includePrevKv: boolean;
}

export interface EtcdWatchStartResponse {
  watchId: string;
  startedRevision: KvInt64;
}

export interface EtcdWatchPollResponse {
  watchId: string;
  batches: Array<{ revision: KvInt64; events: Array<{ eventType: "put" | "delete"; revision: KvInt64; key: string; keyBytes?: KvValue | null; value?: KvValue | null; previousValue?: KvValue | null; metadata?: KvKeyMetadata | null }> }>;
  terminal?: { reason: string; message?: string; compactedRevision?: KvInt64 | null } | null;
}

export interface EtcdLeaseListResponse {
  leases: Array<{ id: KvInt64; ttl: number; grantedTtl?: number }>;
  partial: boolean;
  nextContinuation?: string | null;
}

export interface EtcdLeaseDetail {
  id: KvInt64;
  ttl: number;
  grantedTtl?: number;
  keys: KvValue[];
  truncated: boolean;
}

export interface EtcdAuthUserListResponse {
  users: string[];
}

export interface EtcdAuthUserDetail {
  user: string;
  roles: string[];
  authEnabled?: boolean;
}

export interface EtcdAuthPermission {
  access: "read" | "write" | "readwrite";
  key: KvValue;
  rangeEnd: KvValue;
  resource: "all" | "key" | "prefix";
}

export interface EtcdAuthRoleListResponse {
  roles: string[];
}

export interface EtcdAuthRoleDetail {
  role: string;
  permissions: EtcdAuthPermission[];
}

export interface EtcdPreflightResponse {
  token: string;
  action: string;
  confirmationText: string;
  expiresAtMs: number;
  clusterId?: KvInt64 | null;
}

export interface EtcdDangerousApproval {
  preflightToken: string;
  confirmationText: string;
}

export interface DocumentQueryResult {
  documents: any[];
  raw_documents?: string[];
  extended_documents?: any[];
  total: number;
  total_is_exact?: boolean;
  next_cursor?: string;
}

export interface DynamoDbKeyInfo {
  name: string;
  attributeType: "S" | "N" | "B" | string;
}

export interface DynamoDbIndexInfo {
  name: string;
  kind: "global" | "local" | string;
  partitionKey: DynamoDbKeyInfo;
  sortKey?: DynamoDbKeyInfo;
  projectionType: "ALL" | "KEYS_ONLY" | "INCLUDE" | string;
  nonKeyAttributes: string[];
}

export interface DynamoDbTableDescription {
  name: string;
  status: string;
  itemCount: number;
  sizeBytes: number;
  partitionKey: DynamoDbKeyInfo;
  sortKey?: DynamoDbKeyInfo;
  indexes: DynamoDbIndexInfo[];
}

export type MongoDocumentResult = DocumentQueryResult;

export interface MongoCollectionStatsResult {
  count: unknown;
  size: unknown;
  avgObjSize: unknown;
  storageSize: unknown;
  totalIndexSize: unknown;
  nindexes: unknown;
}

export interface MongoDropIndexFailure {
  name: string;
  message: string;
}

export interface MongoDropIndexesResult {
  dropped_names: string[];
  affected_rows: number;
  failures?: MongoDropIndexFailure[];
}

export interface MongoIndexKey {
  field: string;
  /** `1`, `-1`, or a MongoDB key type such as `text` / `2dsphere` / `hashed`. */
  direction: string;
}

export interface MongoIndexSpec {
  name: string;
  keys: MongoIndexKey[];
  is_unique: boolean;
  is_primary: boolean;
  is_sparse: boolean;
  /** TTL in seconds; null when the index does not expire. */
  expire_after_seconds: number | null;
  partial_filter_expression: string | null;
  /** Ignored by MongoDB 4.2+, still reported by older servers. */
  background: boolean;
  /** Only meaningful for geoHaystack indexes, removed in MongoDB 4.4+. */
  bucket_size: number | null;
  hidden: boolean;
  /** False when the driver could not report the properties above (Legacy Agent). */
  properties_complete: boolean;
  extra_options: string | null;
}

export interface MongoCloneCollectionResult {
  documents_copied: number;
  indexes_copied: number;
}

export interface MongoGridFsFileInfo {
  id: string;
  filename?: string;
  length: number;
  chunkSize: number;
  uploadDate?: string;
  metadata?: any;
  md5?: string;
  contentType?: string;
  aliases?: string[];
}

export interface MongoGridFsBucketInfo {
  name: string;
  fileCount: number;
  totalBytes: number;
}

export type ElasticsearchIndexMetadataKind = "mapping" | "settings" | "stats";

export interface ElasticsearchDeleteByQueryResult {
  total: number;
  deleted: number;
  versionConflicts: number;
  timedOut: boolean;
  failures: string[];
}

export interface MeilisearchIndexSettings {
  [key: string]: unknown;
  pagination?: {
    maxTotalHits?: number;
  };
}

export interface MeilisearchIndexOverview {
  uid: string;
  primaryKey: string | null;
  createdAt: string | null;
  updatedAt: string | null;
  numberOfDocuments: number;
  isIndexing: boolean;
  /** Raw document store size of this index (Meilisearch >= 1.14); null on older servers. */
  documentSize: number | null;
  /** Average document size of this index (Meilisearch >= 1.14); null on older servers. */
  avgDocumentSize: number | null;
  /** Instance-wide database size; every index shares it, so it is only a fallback. */
  databaseSize: number | null;
}

export interface HistoryEntry {
  id: string;
  connection_id?: string;
  connection_name: string;
  database: string;
  sql: string;
  executed_at: string;
  execution_time_ms: number;
  success: boolean;
  error?: string;
  activity_kind?: "query" | "data_change" | "schema_change" | "import" | "transfer" | "redis_command";
  operation?: string;
  target?: string;
  affected_rows?: number | null;
  rollback_sql?: string | null;
  details_json?: string | null;
  source?: "sql" | "mcp" | "other";
  mcp_tool_name?: string | null;
  mcp_request_json?: string | null;
  mcp_response_json?: string | null;
  mcp_session_id?: string | null;
}

export interface HistoryConnectionFilter {
  connection_id: string;
  connection_name: string;
}

export interface HistoryDatabaseFilter extends HistoryConnectionFilter {
  database: string;
}

export interface HistoryCursor {
  executed_at: string;
  id: string;
}

export interface HistorySearchRequest {
  search_text: string;
  connections: HistoryConnectionFilter[];
  databases: HistoryDatabaseFilter[];
  activity_kind?: string;
  success?: boolean;
  started_at?: string;
  ended_at?: string;
  cursor?: HistoryCursor;
  limit: number;
  source?: "sql" | "mcp" | "other";
  mcp_tool_name?: string;
}

export interface HistorySearchResult {
  entries: HistoryEntry[];
  next_cursor?: HistoryCursor | null;
  total: number;
}

export interface HistoryConnectionOption extends HistoryConnectionFilter {
  databases: string[];
}

export type SqlFileStatus = "started" | "running" | "statementDone" | "statementFailed" | "done" | "error" | "cancelled";

export interface SqlFileRequest {
  executionId: string;
  connectionId: string;
  database: string;
  schema?: string;
  filePath: string;
  continueOnError: boolean;
  txnSessionId?: string;
  selectedTables?: SqlFileTable[];
  partCooldownMs?: number;
  skipRelationalConstraints?: boolean;
}

export interface SqlFileTable {
  database: string | null;
  name: string;
}

export interface SqlFilePreview {
  fileName: string;
  filePath: string;
  sizeBytes: number;
  preview: string;
  canExecuteWithoutSelectedDatabase: boolean;
  establishesDatabaseContext?: boolean;
  packageFilePaths?: string[];
  packagePartCount?: number;
  cleanupToken?: string;
}

export interface SqlFileProgress {
  executionId: string;
  status: SqlFileStatus;
  statementIndex: number;
  successCount: number;
  failureCount: number;
  affectedRows: number;
  elapsedMs: number;
  statementSummary: string;
  error?: string | null;
  bytesRead?: number;
  totalBytes?: number;
  phase?: "preparing" | "reading" | "executing";
  fileIndex?: number;
  fileName?: string;
}

export type TransferMode = "append" | "overwrite" | "upsert";

export type TransferTableNameCase = "preserve" | "lower" | "upper";

export type TransferOwnershipPolicy = "preserve" | "skip" | "reassignMissing";

export type TransferContent = "structureAndData" | "structureOnly" | "dataOnly";

export type TransferObjectKind = "TABLE" | "VIEW" | "MATERIALIZED_VIEW" | "PROCEDURE" | "FUNCTION" | "TRIGGER" | "SEQUENCE" | "EVENT";

export interface TransferObjectSelection {
  objectType: TransferObjectKind;
  names: string[];
}

export interface TransferRequest {
  transferId: string;
  sourceConnectionId: string;
  sourceDatabase: string;
  sourceSchema: string;
  sourceCatalog?: string;
  targetConnectionId: string;
  targetDatabase: string;
  targetSchema: string;
  targetCatalog?: string;
  tables: string[];
  createTable: boolean;
  content: TransferContent;
  objects: TransferObjectSelection[];
  mode: TransferMode;
  targetTableNameCase: TransferTableNameCase;
  quoteTargetColumnNames: boolean;
  ownershipPolicy?: TransferOwnershipPolicy;
  batchSize: number;
  dropTargetBeforeCreate: boolean;
  dropTargetConfirmed: boolean;
}

export interface TransferOwnershipPreview {
  missingOwners: string[];
  targetOwner: string;
  rebuild?: {
    sql: string;
    tables: Array<{ sourceTable: string; targetTable: string; backupTable?: string }>;
  };
}

export interface TransferProgress {
  transferId: string;
  table: string;
  tableIndex: number;
  totalTables: number;
  rowsTransferred: number;
  totalRows: number | null;
  status: "running" | "tableDone" | "done" | "error" | "cancelled";
  error: string | null;
  terminal: boolean;
  transferFailuresOmitted?: number;
}

export interface SortTablesByFkOptions {
  connectionId: string;
  database: string;
  schema: string;
  tables: string[];
  parentsFirst: boolean;
}

export type TableImportMode = "append" | "truncate";

export type TableImportConflictPolicy = "error" | "skip" | "updateExisting";

export type TableImportStatus = "running" | "done" | "error" | "cancelled";

export type TableImportPhase = "preparing" | "detectingEncoding" | "reading" | "writing" | "finalizing" | "done";

export type TableImportSourceFormat = "csv" | "tsv" | "delimited" | "json" | "excel" | "sql" | "parquet";

export type TableImportJsonShape = "auto" | "objects" | "arrays";

export type TableImportTextEncoding = "auto" | "utf8" | "gbk" | "utf16Le" | "utf16Be";

export interface TableImportColumnMapping {
  sourceColumn: string;
  targetColumn: string;
  targetDataType?: string | null;
}

export interface TableImportParseOptions {
  delimiter?: string | null;
  decimalSeparator?: string | null;
  encoding?: TableImportTextEncoding | null;
  hasHeader?: boolean | null;
  titleRow?: number | null;
  dataStartRow?: number | null;
  lastDataRow?: number | null;
  trimValues?: boolean | null;
  emptyStringAsNull?: boolean | null;
  /** 分隔文本里代表 NULL 的字面量。缺省表示用后端默认值 `\N`；空串表示关闭字面量，退回「空字段即 NULL」。 */
  nullLiteral?: string | null;
  sheetName?: string | null;
  sheetIndex?: number | null;
  jsonShape?: TableImportJsonShape | null;
  sqlDialect?: DatabaseType | null;
}

export interface TableImportPreviewRequest {
  filePath: string;
  connectionId?: string | null;
  database?: string | null;
  sourceRef?: string | null;
  sourceFormat?: TableImportSourceFormat | null;
  parseOptions?: TableImportParseOptions | null;
  previewLimit?: number | null;
}

export interface TableImportPreview {
  fileName: string;
  filePath: string;
  sourceRef?: string | null;
  fileType: string;
  sizeBytes: number;
  columns: string[];
  rows: unknown[][];
  totalRows: number;
  totalRowsExact?: boolean;
  sourceFingerprint: string;
  effectiveEncoding?: TableImportTextEncoding | null;
  sheets?: string[];
}

export interface TableImportPreparedSource {
  fingerprint: string;
  columns: string[];
  rows: unknown[][];
  totalRows: number;
  totalRowsExact?: boolean;
  effectiveEncoding?: TableImportTextEncoding | null;
}

export interface TableImportRequest {
  importId: string;
  connectionId: string;
  database: string;
  schema: string;
  table: string;
  filePath: string;
  sourceRef?: string | null;
  sourceFormat?: TableImportSourceFormat | null;
  parseOptions?: TableImportParseOptions | null;
  mappings: TableImportColumnMapping[];
  mode: TableImportMode;
  createTable?: boolean;
  batchSize: number;
  dateTimeFormat?: string;
  preparedSource?: TableImportPreparedSource | null;
  retainSource?: boolean;
  conflictPolicy?: TableImportConflictPolicy;
  skipDuplicateRows?: boolean;
}

export interface TableImportSummary {
  importId: string;
  rowsImported: number;
  totalRows: number;
  elapsedMs: number;
}

export interface TableImportProgress {
  importId: string;
  status: TableImportStatus;
  phase?: TableImportPhase;
  rowsImported: number;
  totalRows: number;
  totalRowsExact?: boolean;
  bytesRead?: number;
  totalBytes?: number;
  elapsedMs: number;
  error?: string | null;
}

export type MongoImportFormat = "csv" | "json" | "ndjson" | "bson";

export type MongoImportTypeMode = "string" | "auto" | "extendedJson";

export type MongoImportInferredType = "boolean" | "integer" | "decimal" | "date" | "objectId" | "object" | "array" | "mixed" | "string";

export type MongoImportStatus = "running" | "done" | "error" | "cancelled";

export type MongoImportPhase = "preparing" | "parsing" | "writing" | "done";

export type MongoExportFormat = "csv" | "ndjson" | "bson";

export type MongoExportStatus = "running" | "done" | "error" | "cancelled";

export interface MongoImportIssue {
  code: string;
  message: string;
  row?: number | null;
  column?: string | null;
  value?: string | null;
  batch?: number | null;
  retryable?: boolean;
}

export interface MongoImportParseOptions {
  encoding?: TableImportTextEncoding | null;
  delimiter?: string | null;
  hasHeader?: boolean | null;
  trim?: boolean | null;
  emptyAsNull?: boolean | null;
  typeMode?: MongoImportTypeMode | null;
  recognizeObjectIdHex?: boolean | null;
  skipErrorRows?: boolean | null;
  columnTypes?: Partial<Record<string, MongoImportInferredType>> | null;
}

export interface MongoImportPreviewRequest {
  filePath: string;
  sourceRef?: string | null;
  format: MongoImportFormat;
  parseOptions?: MongoImportParseOptions;
  previewLimit?: number | null;
}

export interface MongoImportColumn {
  name: string;
  inferredType: MongoImportInferredType;
  sampleValues?: unknown[];
}

export interface MongoImportPreview {
  sourceRef?: string | null;
  format: MongoImportFormat;
  detectedEncoding?: TableImportTextEncoding | null;
  fileName: string;
  filePath: string;
  sizeBytes: number;
  columns: MongoImportColumn[];
  rows: Record<string, unknown>[];
  rowNumbers?: number[];
  warnings: MongoImportIssue[];
  errors: MongoImportIssue[];
  estimatedRows?: number | null;
  estimatedRowsExact: boolean;
}

export interface MongoImportRequest {
  importId: string;
  connectionId: string;
  database: string;
  collection: string;
  filePath: string;
  sourceRef?: string | null;
  format: MongoImportFormat;
  parseOptions?: MongoImportParseOptions;
  batchSize: number;
  executionId?: string | null;
}

export interface MongoImportProgress {
  importId: string;
  phase: MongoImportPhase;
  status: MongoImportStatus;
  rowsRead: number;
  rowsInserted: number;
  rowsFailed: number;
  batchesCommitted: number;
  totalRows?: number | null;
  errorRows?: MongoImportIssue[];
  errorMessage?: string | null;
  elapsedMs: number;
}

export interface MongoImportSummary {
  importId: string;
  rowsInserted: number;
  rowsFailed: number;
  batchesCommitted: number;
  elapsedMs: number;
}

export interface MongoExportRequest {
  exportId: string;
  connectionId: string;
  database: string;
  collection: string;
  filter?: string | null;
  sort?: string | null;
  projection?: string | null;
  collation?: string | null;
  format: MongoExportFormat;
  includeHeader?: boolean;
  gzip?: boolean;
  filePath: string;
  executionId?: string | null;
}

export interface MongoExportProgress {
  exportId: string;
  status: MongoExportStatus;
  documentsRead: number;
  bytesWritten: number;
  totalDocuments?: number | null;
  errorMessage?: string | null;
  elapsedMs: number;
}

export interface MongoExportSummary {
  exportId: string;
  documentsExported: number;
  filePath: string;
  elapsedMs: number;
}

export interface DatabaseExportRequest {
  exportId: string;
  connectionId: string;
  database: string;
  schema: string;
  filePath: string;
  selectedTables?: string[];
  excludedTables?: string[];
  includeStructure: boolean;
  includeData: boolean;
  includeObjects: boolean;
  includeCreateDatabase?: boolean;
  dropTableIfExists?: boolean;
  omitAutoIncrement?: boolean;
  failOnError?: boolean;
  preventOverwrite?: boolean;
  outputCompression?: "none" | "gzip";
  insertDialect?: SqlInsertDialect;
  insertMode?: "batch" | "single";
  snapshotSessionId?: string;
  batchSize: number;
  splitMaxMb?: number;
}

export interface DatabaseBackupSnapshot {
  sessionId: string;
  schemas: string[];
}

export interface ExportProgress {
  exportId: string;
  currentObject: string;
  objectIndex: number;
  totalObjects: number;
  rowsExported: number;
  totalRows: number | null;
  status: "Running" | "Done" | "Error" | "Cancelled";
  error: string | null;
  /** True while listing schema / prefetching metadata before objects are written. */
  preparing?: boolean;
  /** Per-object failures written into the file as `-- ERROR` comments (lenient mode). */
  errorCount?: number;
  /** First lenient failure, for completion warnings without opening the file. */
  errorSummary?: string | null;
}

export type TableExportStatus = "Running" | "Writing" | "Done" | "Error" | "Cancelled";

export interface TableExportRequest {
  exportId: string;
  connectionId: string;
  database: string;
  schema?: string;
  identifierQuote?: string;
  tableName: string;
  filePath: string;
  format: "csv" | "xlsx" | "json" | "markdown" | "sql" | "txt";
  insertMode?: SqlInsertMode;
  insertDialect?: SqlInsertDialect;
  csvQuoteMode?: CsvQuoteMode;
  /** CSV 里 NULL 写成什么。缺省表示用后端默认值 `\N`；空串表示关闭字面量。 */
  nullLiteral?: string;
  columns?: string[];
  selectedColumns?: SqlExportColumnSelection[];
  columnTypes?: Array<string | null | undefined>;
  /** 与 `columns` 对齐的列 EXTRA 元数据（identity 等），用于 SQL INSERT 导出的 `SET IDENTITY_INSERT`。 */
  columnExtras?: Array<string | null | undefined>;
  columnComments?: Array<string | null> | null;
  primaryKeys?: string[];
  /** 导出 SQL 时是否排除主键列（对应数据提取设置里的“排除主键”）。 */
  excludePrimaryKeys?: boolean;
  whereInput?: string;
  orderBy?: string;
  skipCount?: boolean;
  batchSize?: number;
  rowLimit?: number | null;
  dateTimeFormat?: string;
  numericColumnRightAlign?: boolean;
  autoFilter?: boolean;
  splitMaxMb?: number;
  /**
   * SQL 导出时省略 INSERT 目标的库/模式限定（前端按「生成 SQL 时包含数据库名」设置 +
   * `dropsSchemaQualifier` 引擎规则解析；仅影响 INSERT 目标，读取 SQL 不变）。
   */
  omitDatabaseQualifier?: boolean;
}

export interface TableCsvExportOptions {
  filePath: string;
  connectionId: string;
  database: string;
  schema?: string;
  tableName: string;
  columns?: string[];
  pageSize?: number;
  timeoutSecs?: number;
  csvQuoteMode?: CsvQuoteMode;
  /** CSV 里 NULL 写成什么。缺省表示用后端默认值 `\N`；空串表示关闭字面量。 */
  nullLiteral?: string;
}

export interface TableExportProgress {
  exportId: string;
  tableName: string;
  rowsExported: number;
  totalRows: number | null;
  status: TableExportStatus;
  errorMessage?: string;
}

export interface QueryResultExportRequest {
  exportId: string;
  connectionId: string;
  database: string;
  schema?: string;
  catalog?: string;
  sql: string;
  queryBaseSql: string;
  setupSql?: string[];
  databaseType: DatabaseType;
  useAgentCursor: boolean;
  filePath: string;
  format: "csv" | "xlsx" | "json" | "txt" | "sql";
  insertMode?: SqlInsertMode;
  csvQuoteMode?: CsvQuoteMode;
  /** CSV 里 NULL 写成什么。缺省表示用后端默认值 `\N`；空串表示关闭字面量。 */
  nullLiteral?: string;
  includeSqlSheet?: boolean;
  pageSize: number;
  rowLimit?: number | null;
  totalRows?: number | null;
  timeoutSecs?: number;
  keysetOptimizationEnabled: boolean;
  clientSessionId?: string;
  executionId?: string;
  dateTimeFormat?: string;
  exportTableName?: string;
  exportColumnTypes?: Array<string | null | undefined>;
  selectedColumns?: SqlExportColumnSelection[];
  /**
   * 结果列对应的原表 EXTRA 元数据（identity 等）。后端据此为 SQL INSERT 导出
   * 补上 `SET IDENTITY_INSERT` 包裹，缺省表示未知。
   */
  exportColumnExtras?: Array<string | null | undefined>;
  numericColumnRightAlign?: boolean;
  columnComments?: Array<string | null> | null;
  autoFilter?: boolean;
  identifierQuote?: string;
  /** 导出 SQL 时是否排除主键列（对应数据提取设置里的“排除主键”）。 */
  excludePrimaryKeys?: boolean;
  /** 结果集对应的原表主键列名，由前端从表元数据带入。 */
  primaryKeys?: string[];
}
export type { MigrationPreflight, MigrationReport } from "./migration";
export type { OpenTabsStatePayload, PersistedEditorGroup } from "@/lib/app/openTabsPersistence";
export type { PluginPlanCapabilities, PluginPlanRequest, PluginPlanResult } from "@/types/pluginPlan";
export type { PluginColumnMetadata, PluginMetadataFieldAvailability, PluginMetadataFieldCapabilities, PluginTableMetadata, PluginTableMetadataRequest } from "@/types/pluginSchemaMetadata";
