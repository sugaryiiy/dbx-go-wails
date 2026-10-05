export interface ActionSearchItem {
  id: string;
  label: string;
  group: string;
  shortcut?: string;
  keywords?: string[];
  disabled?: boolean;
  disabledReason?: string;
}

function normalize(value: string): string {
  return value.normalize("NFKC").toLowerCase().trim().replace(/\s+/gu, " ");
}

function matchRank(value: string, term: string): number {
  if (value === term) return 0;
  if (value.startsWith(term)) return 1;
  return value.includes(term) ? 2 : Infinity;
}

/** All query terms must match. Prefer labels, then aliases, then groups;
 * preserve the caller's order for equally relevant actions, including disabled ones. */
export function filterActionSearchItems(items: readonly ActionSearchItem[], query: string): ActionSearchItem[] {
  const normalizedQuery = normalize(query);
  if (!normalizedQuery) return [...items];
  const terms = normalizedQuery.split(" ");
  return items
    .map((item, index) => {
      const label = normalize(item.label);
      const group = normalize(item.group);
      const keywords = (item.keywords ?? []).map(normalize);
      const score = terms.reduce((total, term) => total + Math.min(matchRank(label, term), ...keywords.map((keyword) => 3 + matchRank(keyword, term)), 6 + matchRank(group, term)), 0);
      return { item, index, score: score - (label === normalizedQuery ? 1 : 0) };
    })
    .filter(({ score }) => Number.isFinite(score))
    .sort((a, b) => a.score - b.score || a.index - b.index)
    .map(({ item }) => item);
}

/** Wrap keyboard navigation, skipping unavailable actions. -1 means no enabled action. */
export function nextEnabledActionIndex(items: readonly ActionSearchItem[], currentIndex: number, direction: 1 | -1): number {
  if (!items.length) return -1;
  let index = currentIndex >= 0 && currentIndex < items.length ? currentIndex : direction === 1 ? -1 : 0;
  for (let visited = 0; visited < items.length; visited += 1) {
    index = (index + direction + items.length) % items.length;
    if (!items[index].disabled) return index;
  }
  return -1;
}
