export function filterDatabaseOptions(options: string[], query: string, displayName: (option: string) => string = (option) => option, includeRawValue = true): string[] {
  const normalizedQuery = query.trim().toLowerCase();
  if (!normalizedQuery) return options;

  return options.filter((option) => {
    const raw = option.toLowerCase();
    const label = displayName(option).toLowerCase();
    return (includeRawValue && raw.includes(normalizedQuery)) || label.includes(normalizedQuery);
  });
}
