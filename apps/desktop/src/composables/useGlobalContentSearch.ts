import { computed, onScopeDispose, ref, watch, type Ref } from "vue";
import type { ConnectionConfig, QueryTab } from "@/types/database";
import type { QuickOpenItem } from "@/composables/useQuickOpen";
import { useQueryStore } from "@/stores/queryStore";
import * as api from "@/lib/backend/api";
import { composeGlobalSearchRoots, getGlobalSearchExtensions, globalSearchSettingsVersion } from "@/lib/globalSearch/globalSearchSettings";
import { sqlFileFoldersVersion } from "@/lib/sqlFile/sqlFileFolders";
import i18n from "@/i18n";
import { filterDatabaseNamesForConnection } from "@/lib/database/visibleDatabases";

const SEARCH_DEBOUNCE_MS = 200;
const MAX_RESULTS = 500;
const POSTGRES_TYPES = new Set(["postgres"]);

export interface ContentGroup {
  key: string;
  header: string;
  description?: string;
  type: string;
  filePath?: string;
  matches: QuickOpenItem[];
}

interface ContentSearchOptions {
  searchQuery: Ref<string>;
  selectedIndex: Ref<number>;
  connectionStore: {
    activeConnectionId: string | null;
    connections: ConnectionConfig[];
    ensureConnected: (id: string, options?: { activate?: boolean; verifyHealth?: boolean }) => Promise<void>;
  };
  savedSqlVersion: () => number;
  getQueryTabs?: () => QueryTab[];
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/** Match literal text without changing original UTF-16 offsets when Unicode case folds. */
export function findContentMatches(text: string, query: string, limit: number): Array<{ line: number; column: number; lineText: string; matchText: string; highlightIndices: [number, number] }> {
  if (!query || limit <= 0) return [];
  const pattern = new RegExp(query.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "giu");
  const matches: ReturnType<typeof findContentMatches> = [];
  let line = 1;
  // Use a whole-text expression so a literal query can also span lines.
  let previousOffset = 0;
  let lineStart = 0;
  for (const match of text.matchAll(pattern)) {
    const offset = match.index;
    for (let index = previousOffset; index < offset; index++) {
      if (text[index] === "\n") {
        line++;
        lineStart = index + 1;
      }
    }
    previousOffset = offset;
    const lineEnd = text.indexOf("\n", offset);
    const lineText = text.slice(lineStart, lineEnd < 0 ? text.length : lineEnd).replace(/\r$/, "");
    const start = offset - lineStart;
    matches.push({ line, column: start + 1, lineText, matchText: match[0], highlightIndices: [start, Math.min(start + match[0].length, lineText.length)] });
    if (matches.length >= limit) break;
  }
  return matches;
}

export function useGlobalContentSearch(options: ContentSearchOptions) {
  const { searchQuery, selectedIndex, connectionStore } = options;
  const getQueryTabs = options.getQueryTabs ?? (() => useQueryStore().tabs);
  const contentMode = ref(false);
  const searchActive = ref(false);
  const contentSearching = ref(false);
  const contentItems = ref<QuickOpenItem[]>([]);
  const contentSearchError = ref<string[]>([]);
  const contentTruncated = ref(false);
  let generation = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let controller: AbortController | undefined;

  const connection = computed(() => connectionStore.connections.find((item) => item.id === connectionStore.activeConnectionId));
  const contentScopeLabel = computed(() => connection.value?.name || i18n.global.t("quickOpen.contentScopeUnconnected"));
  const queryTabs = computed(() => (searchActive.value ? getQueryTabs().filter((tab) => tab.connectionId === connection.value?.id && tab.mode === "query" && !tab.objectSource) : []));
  const contentGroups = computed<ContentGroup[]>(() => {
    const groups = new Map<string, ContentGroup>();
    for (const item of contentItems.value) {
      const key = item.contentGroupKey || item.filePath || item.id;
      let group = groups.get(key);
      if (!group) {
        group = { key, header: item.fileName || item.label, description: item.description, type: item.contentGroupType || "sql_file", filePath: item.filePath, matches: [] };
        groups.set(key, group);
      }
      group.matches.push(item);
    }
    for (const group of groups.values()) group.matches.sort((a, b) => (a.line ?? 0) - (b.line ?? 0) || (a.column ?? 0) - (b.column ?? 0));
    return [...groups.values()];
  });
  const selectableContentItems = computed(() => contentGroups.value.flatMap((group) => group.matches));
  const contentSelectedItem = computed(() => (contentMode.value ? (selectableContentItems.value[selectedIndex.value] ?? null) : null));

  function cancel(): void {
    generation++;
    if (timer) clearTimeout(timer);
    timer = undefined;
    controller?.abort();
    controller = undefined;
    contentSearching.value = false;
  }

  function clear(): void {
    contentItems.value = [];
    contentSearchError.value = [];
    contentTruncated.value = false;
  }

  async function runSearch(query: string, searchGeneration: number): Promise<void> {
    const abort = new AbortController();
    controller = abort;
    const current = () => generation === searchGeneration && !abort.signal.aborted;
    const config = connection.value;
    const connectionId = config?.id ?? "";
    const tabs = [...queryTabs.value];
    const firstMatchOnly = !contentMode.value;
    const openSavedIds = new Set(tabs.map((tab) => tab.savedSqlId).filter(Boolean));
    const results: QuickOpenItem[][] = [[], [], [], []];
    const publish = () => {
      if (!current()) return;
      const selectedId = contentSelectedItem.value?.id;
      const seen = new Set<string>();
      const combined = results.flat().filter((item) => {
        if (!firstMatchOnly) return true;
        const key = item.contentGroupKey || item.id;
        if (seen.has(key)) return false;
        seen.add(key);
        return true;
      });
      if (combined.length > MAX_RESULTS) contentTruncated.value = true;
      contentItems.value = combined.slice(0, MAX_RESULTS);
      if (contentMode.value) {
        const retainedIndex = selectedId ? selectableContentItems.value.findIndex((item) => item.id === selectedId) : -1;
        selectedIndex.value = retainedIndex >= 0 ? retainedIndex : Math.min(selectedIndex.value, Math.max(0, selectableContentItems.value.length - 1));
      }
    };
    const report = (error: unknown) => {
      if (current()) contentSearchError.value.push(errorMessage(error));
    };
    const description = (database?: string, schema?: string, catalog?: string) => [...new Set([config?.name, catalog, database, schema].filter(Boolean))].join(" / ");
    contentSearching.value = true;

    for (const tab of tabs) {
      const remaining = MAX_RESULTS + 1 - results[0].length;
      if (remaining <= 0) break;
      const matches = findContentMatches(tab.sql, query, firstMatchOnly ? 1 : remaining);
      for (const match of matches) {
        results[0].push({
          id: `query-content-${tab.id}-${match.line}-${match.column}`,
          type: "content_match",
          contentSource: "query_tab",
          contentGroupType: "query_tab",
          contentGroupKey: `query-${tab.id}`,
          label: tab.title,
          fileName: tab.title,
          description: description(tab.database, tab.schema, tab.catalog),
          connectionId,
          database: tab.database,
          schema: tab.schema,
          catalog: tab.catalog,
          queryTabId: tab.id,
          sqlFileId: tab.savedSqlId,
          searchText: match.lineText,
          ...match,
        });
      }
    }
    publish();

    async function searchSaved(): Promise<void> {
      if (!connectionId) return;
      try {
        const response = await api.searchSavedQueries({ connection_id: connectionId, query, limit: MAX_RESULTS + 1, exclude_ids: [...openSavedIds] as string[], ...(firstMatchOnly ? { first_match_only: true } : {}) }, abort.signal);
        if (!current()) return;
        contentTruncated.value ||= response.truncated;
        for (const match of response.matches) {
          if (openSavedIds.has(match.id)) continue;
          results[1].push({
            id: `saved-content-${match.id}-${match.line}-${match.column}`,
            type: "content_match",
            contentSource: "saved_query",
            contentGroupType: "saved_query",
            contentGroupKey: `saved-${match.id}`,
            label: match.name,
            fileName: match.name,
            description: description(match.database, match.schema, match.catalog),
            connectionId,
            database: match.database,
            schema: match.schema,
            catalog: match.catalog,
            sqlFileId: match.id,
            line: match.line,
            column: match.column,
            lineText: match.line_text,
            matchText: match.match_text,
            highlightIndices: [match.start, match.end],
            searchText: match.line_text,
          });
        }
        publish();
      } catch (error) {
        report(error);
      }
    }

    async function searchObjects(): Promise<void> {
      if (!config) return;
      // These connections have no stored SQL definitions; saved queries still apply.
      if (["sqlite", "duckdb", "redis", "mongodb", "elasticsearch", "easysearch", "meilisearch", "etcd", "zookeeper", "mq", "nacos", "consul"].includes(config.db_type)) return;
      try {
        await connectionStore.ensureConnected(connectionId, { activate: false, verifyHealth: false });
        if (!current()) return;
        let databases: Array<string | undefined> = [undefined];
        let visibleDatabases: string[] | undefined;
        if (POSTGRES_TYPES.has(config.db_type) || config.db_type === "mysql") {
          const listed = await api.listDatabases(connectionId);
          if (!current()) return;
          visibleDatabases = filterDatabaseNamesForConnection(
            listed.map((database) => database.name),
            config,
          );
          if (!visibleDatabases.length) return;
          if (POSTGRES_TYPES.has(config.db_type)) databases = visibleDatabases;
        }
        let cursor = 0;
        const objectResults: QuickOpenItem[][] = databases.map(() => []);
        const worker = async () => {
          while (current() && cursor < databases.length && results[2].length <= MAX_RESULTS) {
            const index = cursor++;
            const database = databases[index];
            try {
              const response = await api.searchObjectSources(
                { connection_id: connectionId, database, databases: visibleDatabases, schemas: database ? (config.visible_schemas?.[database] ?? null) : undefined, query, limit: MAX_RESULTS + 1, ...(firstMatchOnly ? { first_match_only: true } : {}) },
                abort.signal,
              );
              if (!current()) return;
              contentTruncated.value ||= response.truncated;
              for (const warning of response.warnings ?? []) report(warning);
              objectResults[index] = response.matches.map((match) => {
                const key = JSON.stringify([connectionId, match.database, match.schema, match.object_type, match.object_name, match.signature]);
                return {
                  id: `object-content-${key}-${match.line}-${match.column}`,
                  type: "content_match",
                  contentSource: "object",
                  contentGroupType: match.object_type.toLowerCase(),
                  contentGroupKey: key,
                  label: `${match.object_name}${match.signature ? `(${match.signature})` : ""}`,
                  fileName: `${match.object_name}${match.signature ? `(${match.signature})` : ""}`,
                  description: description(match.database, match.schema),
                  connectionId,
                  database: match.database,
                  schema: match.schema,
                  objectType: match.object_type,
                  objectName: match.object_name,
                  signature: match.signature,
                  line: match.line,
                  column: match.column,
                  lineText: match.line_text,
                  matchText: match.match_text,
                  highlightIndices: [match.start, match.end],
                  searchText: match.line_text,
                } satisfies QuickOpenItem;
              });
              results[2] = objectResults.flat();
              publish();
            } catch (error) {
              report(database ? `${database}: ${errorMessage(error)}` : error);
            }
          }
        };
        await Promise.all([worker(), worker()]);
        if (current() && cursor < databases.length) contentTruncated.value = true;
      } catch (error) {
        report(error);
      }
    }

    async function searchFiles(): Promise<void> {
      const roots = composeGlobalSearchRoots();
      if (!roots.length) return;
      try {
        const matches = await api.globalSearch({ roots, query, extensions: getGlobalSearchExtensions(), limit: MAX_RESULTS + 1 });
        if (!current()) return;
        results[3] = matches.map((match) => ({
          id: `content-${match.path}-${match.line}-${match.column}`,
          type: "content_match",
          contentSource: "file",
          contentGroupType: "sql_file",
          contentGroupKey: match.path,
          label: match.fileName,
          fileName: match.fileName,
          filePath: match.path,
          description: match.path,
          connectionId: "",
          line: match.line,
          column: match.column,
          matchText: match.matchText,
          lineText: match.line === 0 ? match.fileName : match.lineText,
          highlightIndices: [Math.max(0, match.column - 1), Math.max(0, match.column - 1) + match.matchText.length],
          searchText: match.lineText,
        }));
        publish();
      } catch (error) {
        report(error);
      }
    }

    await Promise.allSettled([searchSaved(), searchObjects(), searchFiles()]);
    if (current()) contentSearching.value = false;
  }

  function schedule(): void {
    cancel();
    clear();
    selectedIndex.value = 0;
    const query = searchQuery.value.trim();
    if (!searchActive.value || !query) return;
    const searchGeneration = generation;
    contentSearching.value = true;
    timer = setTimeout(() => {
      timer = undefined;
      void runSearch(query, searchGeneration);
    }, SEARCH_DEBOUNCE_MS);
  }

  function setContentMode(enabled: boolean): void {
    contentMode.value = enabled;
  }

  function setSearchActive(active: boolean): void {
    searchActive.value = active;
  }

  watch(
    [
      searchQuery,
      contentMode,
      searchActive,
      () => connectionStore.activeConnectionId,
      globalSearchSettingsVersion,
      sqlFileFoldersVersion,
      options.savedSqlVersion,
      () => [connection.value?.db_type, connection.value?.database, connection.value?.visible_databases, connection.value?.visible_database_patterns, connection.value?.visible_schemas],
      () => queryTabs.value.map((tab) => [tab.id, tab.title, tab.sql, tab.database, tab.schema, tab.catalog, tab.savedSqlId]),
    ],
    () => schedule(),
    { flush: "sync" },
  );
  onScopeDispose(cancel, true);

  return { contentMode, contentSearching, contentGroups, contentSelectedItem, contentScopeLabel, contentSearchError, contentTruncated, selectableContentItems, setContentMode, setSearchActive };
}
