import { computed, getCurrentScope, nextTick, onScopeDispose, ref, shallowRef, toValue, watch, watchEffect, type MaybeRefOrGetter } from "vue";
import { DataGridSearchMatchIndex } from "@/lib/dataGrid/dataGridSearchMatchIndex";
import { loadDataGridSearchState, saveDataGridSearchState } from "@/lib/dataGrid/dataGridSearchStatePersistence";
import { dataGridReplacementPattern } from "@/lib/dataGrid/dataGridReplace";

export type DataGridSearchMatch = {
  kind: "cell" | "column";
  displayRow: number;
  col: number;
};

const SEARCH_MATCH_KEY_BASE = 65536;

export type UseDataGridSearchOptions<Row> = {
  columns: MaybeRefOrGetter<readonly string[]>;
  suggestionColumns?: MaybeRefOrGetter<readonly string[]>;
  rows: MaybeRefOrGetter<readonly Row[]>;
  /** 必须返回小写文本（查询词已小写）。调用方可据此缓存小写副本，
   * 避免每次按键对全部单元格重新分配 toLowerCase 字符串。 */
  getCellSearchText: (row: Row, columnIndex: number) => string;
  getCellRawSearchText?: (row: Row, columnIndex: number) => string;
  caseSensitive?: MaybeRefOrGetter<boolean>;
  literalQuery?: MaybeRefOrGetter<boolean>;
  includeColumnMatches?: MaybeRefOrGetter<boolean>;
  isCellSearchable?: (row: Row, columnIndex: number) => boolean;
  debounceMs?: number;
  /** Reactive predicate/formatting inputs not represented by the row snapshot.
   * Large scans yield between chunks, so read these dependencies before scanning. */
  invalidationKey?: MaybeRefOrGetter<unknown>;
  onNavigate?: (match: DataGridSearchMatch) => void;
  /** 按标签页键控的持久化键；不传则该宿主不参与搜索状态保留。 */
  persistenceKey?: MaybeRefOrGetter<string | undefined>;
  /** 列签名，用于避免把陈旧搜索恢复到结构不同的结果上。 */
  persistenceScopeKey?: MaybeRefOrGetter<string>;
};

const SEARCH_TOKEN_SEPARATOR = /[\s,()><=!&|]+/;
const SEARCH_TOKEN_SUFFIX = /([^\s,()><=!&|]+)$/;
const SEARCH_PAIRS: Record<string, string> = { "'": "'", '"': '"', "(": ")" };

export function useDataGridSearch<Row>(options: UseDataGridSearchOptions<Row>) {
  const searchText = ref("");
  const deferredSearchText = ref("");
  const overlayVisible = ref(false);
  const currentMatchIndex = ref(-1);
  const suggestions = ref<string[]>([]);
  const suggestionIndex = ref(-1);
  let searchTimer: ReturnType<typeof setTimeout> | undefined;

  // Case-sensitive matching needs the raw (non-lowercased) cell text. Without a
  // raw-text provider only the lowercase cache exists, so degrade to
  // case-insensitive instead of never matching uppercase input.
  const effectiveCaseSensitive = computed(() => !!toValue(options.caseSensitive) && (!!toValue(options.literalQuery) || !!options.getCellRawSearchText));

  const matchState = shallowRef(new DataGridSearchMatchIndex(0));
  const isSearching = ref(false);
  let scanGeneration = 0;

  function normalizedQuery(value: string) {
    const text = toValue(options.literalQuery) ? value : value.trim();
    return toValue(options.literalQuery) || effectiveCaseSensitive.value ? text : text.toLowerCase();
  }

  watchEffect((onCleanup) => {
    const generation = ++scanGeneration;
    let chunkTimer: ReturnType<typeof setTimeout> | undefined;
    onCleanup(() => {
      scanGeneration++;
      if (chunkTimer !== undefined) clearTimeout(chunkTimer);
    });
    const query = deferredSearchText.value;
    // Changing the input also cancels an in-progress scan during the debounce.
    if (!query || normalizedQuery(searchText.value) !== query) {
      isSearching.value = false;
      matchState.value = new DataGridSearchMatchIndex(0);
      return;
    }
    toValue(options.invalidationKey);
    const columns = toValue(options.columns);
    const rows = toValue(options.rows);
    const index = new DataGridSearchMatchIndex(columns.length);
    const sensitive = effectiveCaseSensitive.value;
    const pattern = toValue(options.literalQuery) ? dataGridReplacementPattern(query, sensitive) : undefined;
    const includeColumns = toValue(options.includeColumnMatches) !== false;
    const includes = (value: string) => {
      // getCellSearchText already supplies lowercase text; avoid duplicating
      // every cached string on every keypress. Headers are normalized below.
      if (!pattern) return value.includes(query);
      pattern.lastIndex = 0;
      return pattern.test(value);
    };
    const totalCells = (rows.length + 1) * columns.length;
    const asynchronous = totalCells > 20_000;
    let cellIndex = 0;
    isSearching.value = asynchronous;
    if (asynchronous) matchState.value = new DataGridSearchMatchIndex(0);

    const scanChunk = () => {
      if (generation !== scanGeneration) return;
      const end = asynchronous ? Math.min(totalCells, cellIndex + 32_768) : totalCells;
      const deadline = performance.now() + 4;
      while (cellIndex < end) {
        const column = cellIndex % columns.length;
        const rowIndex = Math.floor(cellIndex / columns.length) - 1;
        if (rowIndex < 0) {
          if (includeColumns && includes(sensitive || pattern ? columns[column]! : columns[column]!.toLowerCase())) index.appendCell(cellIndex);
        } else {
          const row = rows[rowIndex]!;
          if (!options.isCellSearchable || options.isCellSearchable(row, column)) {
            const text = (pattern || sensitive) && options.getCellRawSearchText ? options.getCellRawSearchText(row, column) : options.getCellSearchText(row, column);
            if (includes(text)) index.appendCell(cellIndex);
          }
        }
        cellIndex++;
        if (asynchronous && cellIndex % 128 === 0 && performance.now() >= deadline) break;
      }
      if (cellIndex < totalCells) {
        chunkTimer = setTimeout(scanChunk, 0);
        return;
      }
      isSearching.value = false;
      matchState.value = index;
    };
    // Small results retain their synchronous reactive scan. The first large
    // chunk also runs here so synchronous predicate dependencies are tracked.
    scanChunk();
  });

  // These compatibility views materialize only if explicitly requested. Grid
  // painting and navigation use the shared bitmap directly.
  const matchKeys = computed(() => Array.from(matchState.value));
  const matchSet = computed<ReadonlySet<number>>(() => matchState.value);
  const matches = computed<DataGridSearchMatch[]>(() => Array.from(matchState.value, dataGridSearchMatchFromKey));
  const matchCount = computed(() => matchState.value.size);

  function matchAt(index: number): DataGridSearchMatch | null {
    const key = matchState.value.keyAt(index);
    return key === undefined ? null : dataGridSearchMatchFromKey(key);
  }

  const currentMatch = computed(() => matchAt(currentMatchIndex.value));

  function clearTimer() {
    if (searchTimer !== undefined) clearTimeout(searchTimer);
    searchTimer = undefined;
  }

  // A single-use token rather than a boolean flag: it is immune to Vue's async
  // watcher flush and self-invalidates if the user types during the restore window.
  let restoreToken: { searchText: string; matchIndex: number } | null = null;

  watch([searchText, () => toValue(options.caseSensitive), () => toValue(options.literalQuery)], ([value]) => {
    clearTimer();
    const query = normalizedQuery(value);
    const restoring = restoreToken !== null && restoreToken.searchText === value;
    if (!query) deferredSearchText.value = "";
    // Restoring is not typing: resolve the query now so the row set (and, in
    // "filter" search mode, the content height) is settled before the grid's
    // scroll restore measures it.
    else if (restoring) deferredSearchText.value = query;
    else searchTimer = setTimeout(() => (deferredSearchText.value = query), options.debounceMs ?? 150);

    if (restoring) {
      // Returning to a tab must not reopen the typeahead popup.
      suggestions.value = [];
      suggestionIndex.value = -1;
      return;
    }
    // The user typed before the restore resolved: drop the token so the query they
    // are writing keeps the normal debounce, index reset and auto-navigate.
    restoreToken = null;

    if (toValue(options.literalQuery)) {
      suggestions.value = [];
      suggestionIndex.value = -1;
      return;
    }

    const lastToken = value.trim().split(SEARCH_TOKEN_SEPARATOR).pop()?.toLowerCase() ?? "";
    suggestions.value = lastToken
      ? toValue(options.suggestionColumns ?? options.columns)
          .filter((column) => column.toLowerCase().startsWith(lastToken) && column.toLowerCase() !== lastToken)
          .slice(0, 8)
      : [];
    suggestionIndex.value = suggestions.value.length ? 0 : -1;
  });

  watch(matchState, (value) => {
    if (isSearching.value) return;
    const restored = restoreToken;
    restoreToken = null;
    if (!value.size) {
      currentMatchIndex.value = -1;
      return;
    }
    if (restored) {
      // Re-entering the tab must not scroll to the first match: the grid's own
      // scroll restore owns the viewport (#8524). Clamp because the match list may
      // have shifted while the tab was away.
      currentMatchIndex.value = Math.min(Math.max(restored.matchIndex, 0), value.size - 1);
      return;
    }
    currentMatchIndex.value = 0;
    const firstMatch = matchAt(0);
    const generation = scanGeneration;
    if (firstMatch)
      nextTick(() => {
        if (generation === scanGeneration) options.onNavigate?.(firstMatch);
      });
  });

  function acceptSuggestion(index = suggestionIndex.value) {
    const suggestion = suggestions.value[index];
    if (!suggestion) return false;
    const token = searchText.value.match(SEARCH_TOKEN_SUFFIX)?.[1];
    if (token) searchText.value = searchText.value.slice(0, -token.length) + suggestion;
    suggestions.value = [];
    suggestionIndex.value = -1;
    return true;
  }

  function navigateSuggestion(delta: number) {
    if (!suggestions.value.length) return;
    suggestionIndex.value = Math.min(Math.max(suggestionIndex.value + delta, 0), suggestions.value.length - 1);
  }

  function navigateMatch(delta: number) {
    const count = matchState.value.size;
    if (!count || isSearching.value) return;
    // Results may change between input and navigation; recover from a stale index in the requested direction.
    const currentIndex = currentMatchIndex.value >= 0 && currentMatchIndex.value < count ? currentMatchIndex.value : delta < 0 ? 0 : -1;
    currentMatchIndex.value = (((currentIndex + delta) % count) + count) % count;
    const match = currentMatch.value;
    if (match) options.onNavigate?.(match);
  }

  function close() {
    clearTimer();
    overlayVisible.value = false;
    searchText.value = "";
    deferredSearchText.value = "";
    suggestions.value = [];
  }

  /**
   * Must be called from onMounted, never from the setup body: the search effect
   * eagerly evaluates its source, and seeding a non-empty query would push
   * matchState past its empty-query early return into `options.rows`, which the
   * host declares later in its own setup (#8524).
   */
  function restorePersistedState(): boolean {
    const key = toValue(options.persistenceKey);
    if (!key) return false;
    const state = loadDataGridSearchState(key, toValue(options.persistenceScopeKey) ?? "");
    if (!state) return false;
    overlayVisible.value = state.overlayVisible;
    // An empty query never re-triggers the searchText watcher, so the token would
    // never be consumed — only arm it when there is text to restore.
    if (!state.searchText) return state.overlayVisible;
    restoreToken = { searchText: state.searchText, matchIndex: state.currentMatchIndex };
    searchText.value = state.searchText;
    return true;
  }

  if (options.persistenceKey) {
    watch(
      [searchText, deferredSearchText, overlayVisible, currentMatchIndex],
      () =>
        saveDataGridSearchState(toValue(options.persistenceKey), {
          scopeKey: toValue(options.persistenceScopeKey) ?? "",
          searchText: searchText.value,
          deferredSearchText: deferredSearchText.value,
          overlayVisible: overlayVisible.value,
          currentMatchIndex: currentMatchIndex.value,
        }),
      { flush: "post" },
    );
  }

  function onKeydown(event: KeyboardEvent) {
    const pair = SEARCH_PAIRS[event.key];
    const input = event.target as HTMLInputElement;
    if (pair && !event.ctrlKey && !event.metaKey && input?.setSelectionRange) {
      const start = input.selectionStart ?? 0;
      const end = input.selectionEnd ?? start;
      event.preventDefault();
      const selected = searchText.value.slice(start, end);
      searchText.value = `${searchText.value.slice(0, start)}${event.key}${selected}${pair}${searchText.value.slice(end)}`;
      nextTick(() => input.setSelectionRange(start + 1 + selected.length, start + 1 + selected.length));
      return;
    }
    if (suggestions.value.length && event.key === "Tab") {
      event.preventDefault();
      acceptSuggestion();
    } else if (suggestions.value.length && (event.key === "ArrowDown" || event.key === "ArrowUp")) {
      event.preventDefault();
      navigateSuggestion(event.key === "ArrowDown" ? 1 : -1);
    } else if (event.key === "Enter") {
      event.preventDefault();
      navigateMatch(event.shiftKey ? -1 : 1);
    }
  }

  if (getCurrentScope()) onScopeDispose(clearTimer);

  return { searchText, deferredSearchText, overlayVisible, currentMatchIndex, suggestions, suggestionIndex, isSearching, matches, matchKeys, matchCount, matchAt, matchSet, currentMatch, acceptSuggestion, navigateSuggestion, navigateMatch, close, onKeydown, restorePersistedState };
}

function dataGridSearchMatchFromKey(key: number): DataGridSearchMatch {
  const displayRow = Math.floor(key / SEARCH_MATCH_KEY_BASE) - 1;
  return {
    kind: displayRow === -1 ? "column" : "cell",
    displayRow,
    col: key % SEARCH_MATCH_KEY_BASE,
  };
}
