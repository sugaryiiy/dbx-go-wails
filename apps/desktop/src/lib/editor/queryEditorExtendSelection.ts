import type { Command, EditorView } from "@codemirror/view";
import { Annotation, EditorSelection, StateField } from "@codemirror/state";
import { matchesShortcut, type ShortcutLikeEvent } from "@/lib/editor/keyboardShortcuts";
import { analyzePlainTextSelectionRanges, nextPlainTextSelectionRange } from "@/lib/editor/plainTextSelectionRanges";
import { chooseNextSemanticSelectionRange, type SemanticSelectionRange } from "@/lib/editor/semanticSelectionRanges";
import { analyzeSqlSemanticSelectionRanges, nextSqlSemanticSelectionRange, type SqlSemanticSelectionOptions } from "@/lib/editor/sqlSemanticSelectionRanges";

export interface QueryEditorExtendSelectionOptions extends SqlSemanticSelectionOptions {
  language?: "sql" | "text";
}

interface SelectionExpansionHistory {
  selection: EditorSelection;
  previous: SelectionExpansionHistory | null;
}

const selectionExpansionAction = Annotation.define<"extend" | "shrink">();

/** Keep complete selections: expanding can merge ranges, and shrinking must
 * restore their original direction, cursor association, and main range. */
export const queryEditorSelectionExpansionHistory = StateField.define<SelectionExpansionHistory | null>({
  create: () => null,
  update(history, transaction) {
    if (transaction.docChanged) return null;
    if (!transaction.selection) return history;
    const action = transaction.annotation(selectionExpansionAction);
    if (action === "extend") {
      if (transaction.newSelection.eq(transaction.startState.selection, true)) return history;
      return { selection: transaction.startState.selection, previous: history };
    }
    if (action === "shrink" && history?.selection.eq(transaction.newSelection, true)) return history.previous;
    // Any other explicit selection transaction starts a new selection session,
    // including clicks, cursor keys, search, and restored tab selections.
    return null;
  },
});

export function extendQueryEditorSelection(view: EditorView, options: QueryEditorExtendSelectionOptions = {}): boolean {
  const doc = view.state.doc.toString();
  const plainTextAnalysis = analyzePlainTextSelectionRanges(doc);
  const sqlAnalysis = options.language === "text" ? null : analyzeSqlSemanticSelectionRanges(doc, options);
  let changed = false;
  const ranges = view.state.selection.ranges.map((range) => {
    const context = {
      doc,
      cursor: range.head,
      current: { from: range.from, to: range.to },
      preferDelimitedContent: options.language !== "text",
    };
    const candidates: SemanticSelectionRange[] = [];
    const plainTextCandidate = nextPlainTextSelectionRange(context, plainTextAnalysis);
    if (plainTextCandidate) candidates.push(plainTextCandidate);
    const sqlCandidate = sqlAnalysis ? nextSqlSemanticSelectionRange(context, options, sqlAnalysis) : null;
    if (sqlCandidate) candidates.push(sqlCandidate);
    const next = chooseNextSemanticSelectionRange(context.current, context.cursor, candidates);
    if (!next) return range;
    changed = true;
    return range.anchor <= range.head ? EditorSelection.range(next.from, next.to) : EditorSelection.range(next.to, next.from);
  });
  if (!changed) return false;
  view.dispatch({
    selection: EditorSelection.create(ranges, view.state.selection.mainIndex),
    annotations: selectionExpansionAction.of("extend"),
    scrollIntoView: true,
  });
  return true;
}

export function shrinkQueryEditorSelection(view: EditorView): boolean {
  const history = view.state.field(queryEditorSelectionExpansionHistory, false);
  if (!history) return false;
  view.dispatch({
    selection: history.selection,
    annotations: selectionExpansionAction.of("shrink"),
    scrollIntoView: true,
  });
  return true;
}

export interface PreventableShortcutEvent extends ShortcutLikeEvent {
  preventDefault(): void;
}

export function runQueryEditorAltExtendSelection(event: PreventableShortcutEvent, shortcut: string, view: EditorView, command: Command | null, platform = globalThis.navigator?.platform || ""): boolean {
  if (!command || !event.altKey || event.ctrlKey || event.metaKey) return false;
  if (!matchesShortcut(event, shortcut, platform)) return false;
  if (!command(view)) return false;
  event.preventDefault();
  return true;
}
