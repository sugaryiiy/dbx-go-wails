interface ContentMatchPosition {
  line?: number;
  column?: number;
  lineText?: string;
  matchText?: string;
}

/** Locate a database snapshot match in an editor that may contain unsaved edits. */
export function resolveContentSearchReveal(text: string, match: ContentMatchPosition): { line: number; column: number } | undefined {
  const needle = match.matchText;
  if (!needle) return undefined;
  const lines = text.split("\n");
  const starts = [0];
  for (let index = 1; index < lines.length; index++) starts.push(starts[index - 1] + lines[index - 1].length + 1);
  const expectedLine = Math.min(Math.max((match.line ?? 1) - 1, 0), lines.length - 1);
  const expectedColumn = Math.max((match.column ?? 1) - 1, 0);
  const expectedOffset = starts[expectedLine] + Math.min(expectedColumn, lines[expectedLine].length);
  const pattern = new RegExp(needle.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "giu");
  let best: { line: number; column: number; sameContext: boolean; distance: number } | undefined;
  let lineIndex = 0;
  for (const candidate of text.matchAll(pattern)) {
    while (lineIndex + 1 < starts.length && starts[lineIndex + 1] <= candidate.index) lineIndex++;
    const column = candidate.index - starts[lineIndex] + 1;
    const sameContext = lines[lineIndex].replace(/\r$/, "") === match.lineText && column === match.column;
    const distance = Math.abs(candidate.index - expectedOffset);
    if (!best || (sameContext && !best.sameContext) || (sameContext === best.sameContext && distance < best.distance)) {
      best = { line: lineIndex + 1, column, sameContext, distance };
    }
  }
  return best ? { line: best.line, column: best.column } : undefined;
}
