/** Cache once per SQL value. Offsets use UTF-16 code units and only LF starts
 * a new line, matching the query tab's SQL offsets (including a trailing LF). */
export function buildLineStarts(sql: string): number[] {
  const starts = [0];
  for (let next = sql.indexOf("\n"); next >= 0; next = sql.indexOf("\n", next + 1)) {
    starts.push(next + 1);
  }
  return starts;
}

/** Resolve a one-based line/column without rescanning SQL on cursor movement.
 * `starts` must be the index built for the SQL whose length is supplied. */
export function cursorPositionFromLineStarts(starts: readonly number[], sqlLength: number, cursorPos: number): { line: number; column: number } {
  const length = Number.isFinite(sqlLength) ? Math.max(0, Math.trunc(sqlLength)) : 0;
  const position = Number.isNaN(cursorPos) ? 0 : Math.max(0, Math.min(length, Math.trunc(cursorPos)));
  // Upper bound: the final start <= the cursor owns its line. At a LF itself
  // the next line has not started; at the following offset it has.
  let low = 0;
  let high = starts.length;
  while (low < high) {
    const middle = low + Math.floor((high - low) / 2);
    if (starts[middle] <= position) low = middle + 1;
    else high = middle;
  }
  const lineIndex = Math.max(0, low - 1);
  return { line: lineIndex + 1, column: position - (starts[lineIndex] ?? 0) + 1 };
}
