import { history, undo } from "@codemirror/commands";
import { EditorSelection, EditorState, StateEffect, Transaction, type Extension, type TransactionSpec } from "@codemirror/state";
import type { EditorView } from "@codemirror/view";
import { describe, expect, it } from "vitest";
import { extendQueryEditorSelection, queryEditorSelectionExpansionHistory, shrinkQueryEditorSelection } from "@/lib/editor/queryEditorExtendSelection";

function createEditor(doc: string, selection: EditorSelection, extensions: Extension[] = []) {
  let state = EditorState.create({
    doc,
    selection,
    extensions: [EditorState.allowMultipleSelections.of(true), queryEditorSelectionExpansionHistory, ...extensions],
  });
  let dispatches = 0;
  const view = {
    get state() {
      return state;
    },
    dispatch(transaction: Transaction | TransactionSpec) {
      dispatches += 1;
      state = transaction instanceof Transaction ? transaction.state : state.update(transaction).state;
    },
  } as EditorView;
  return {
    view,
    get dispatches() {
      return dispatches;
    },
  };
}

function selectedText(view: EditorView) {
  const selection = view.state.selection.main;
  return view.state.sliceDoc(selection.from, selection.to);
}

describe("query editor shrink selection", () => {
  it("walks SQL semantic expansion history back to the exact initial cursor", () => {
    const doc = "select * from records where code = '001' and enabled = 1";
    const initial = EditorSelection.create([EditorSelection.cursor(doc.indexOf("001") + 1, -1)]);
    const { view } = createEditor(doc, initial);
    const selections = [initial];
    const texts: string[] = [];
    for (let step = 0; step < 4; step += 1) {
      expect(extendQueryEditorSelection(view, { databaseType: "mysql" })).toBe(true);
      selections.push(view.state.selection);
      texts.push(selectedText(view));
    }
    expect(texts.slice(0, 3)).toEqual(["001", "'001'", "code = '001'"]);
    for (const expected of selections.slice(0, -1).reverse()) {
      expect(shrinkQueryEditorSelection(view)).toBe(true);
      expect(view.state.selection.eq(expected, true)).toBe(true);
    }
    const before = view.state;
    expect(shrinkQueryEditorSelection(view)).toBe(false);
    expect(view.state).toBe(before);
    expect(view.state.doc.toString()).toBe(doc);
  });

  it("supports plain text and repeated extend/shrink cycles without duplicate history", () => {
    const doc = "prefix (hello world) suffix\n\nnext paragraph";
    const initial = EditorSelection.single(doc.indexOf("hello") + 2);
    const { view } = createEditor(doc, initial);
    expect(extendQueryEditorSelection(view, { language: "text" })).toBe(true);
    const word = view.state.selection;
    expect(selectedText(view)).toBe("hello");
    expect(extendQueryEditorSelection(view, { language: "text" })).toBe(true);
    const content = view.state.selection;
    expect(selectedText(view)).toBe("hello world");
    expect(shrinkQueryEditorSelection(view)).toBe(true);
    expect(view.state.selection.eq(word, true)).toBe(true);
    expect(extendQueryEditorSelection(view, { language: "text" })).toBe(true);
    expect(view.state.selection.eq(content, true)).toBe(true);
    expect(shrinkQueryEditorSelection(view)).toBe(true);
    expect(shrinkQueryEditorSelection(view)).toBe(true);
    expect(view.state.selection.eq(initial, true)).toBe(true);
    expect(shrinkQueryEditorSelection(view)).toBe(false);
  });

  it("restores merged ranges with their directions and original main range", () => {
    const initial = EditorSelection.create([EditorSelection.range(3, 1), EditorSelection.range(7, 8)], 1);
    const { view } = createEditor("alpha beta gamma", initial);
    expect(extendQueryEditorSelection(view, { language: "text" })).toBe(true);
    const words = view.state.selection;
    expect(words.ranges).toHaveLength(2);
    expect(words.mainIndex).toBe(1);
    expect(words.ranges[0].anchor).toBeGreaterThan(words.ranges[0].head);

    expect(extendQueryEditorSelection(view, { language: "text" })).toBe(true);
    expect(view.state.selection.ranges).toHaveLength(1);
    expect(shrinkQueryEditorSelection(view)).toBe(true);
    expect(view.state.selection.eq(words, true)).toBe(true);
    expect(shrinkQueryEditorSelection(view)).toBe(true);
    expect(view.state.selection.eq(initial, true)).toBe(true);
  });

  it.each(["select.pointer", "select", undefined])("invalidates history after an external %s selection even if it later returns", (userEvent) => {
    const { view } = createEditor("alpha beta", EditorSelection.single(2));
    extendQueryEditorSelection(view, { language: "text" });
    const expanded = view.state.selection;
    view.dispatch({ selection: EditorSelection.single(8), userEvent });
    view.dispatch({ selection: expanded, userEvent });
    const before = view.state;
    expect(shrinkQueryEditorSelection(view)).toBe(false);
    expect(view.state).toBe(before);

    // A later expansion starts a fresh session rooted at the manual selection.
    expect(extendQueryEditorSelection(view, { language: "text" })).toBe(true);
    expect(shrinkQueryEditorSelection(view)).toBe(true);
    expect(view.state.selection.eq(expanded, true)).toBe(true);
    expect(shrinkQueryEditorSelection(view)).toBe(false);
  });

  it("invalidates history when only the primary selection changes", () => {
    const { view } = createEditor("alpha beta", EditorSelection.create([EditorSelection.cursor(2), EditorSelection.cursor(8)], 0));
    extendQueryEditorSelection(view, { language: "text" });
    view.dispatch({ selection: EditorSelection.create(view.state.selection.ranges, 1) });
    const before = view.state.selection;
    expect(shrinkQueryEditorSelection(view)).toBe(false);
    expect(view.state.selection).toBe(before);
    expect(view.state.selection.mainIndex).toBe(1);
  });

  it("invalidates history on document edits and does not resurrect it after undo", () => {
    const { view } = createEditor("alpha beta", EditorSelection.single(2), [history()]);
    extendQueryEditorSelection(view, { language: "text" });
    view.dispatch({ changes: { from: 0, to: 5, insert: "x" }, userEvent: "input" });
    const afterEdit = view.state;
    expect(shrinkQueryEditorSelection(view)).toBe(false);
    expect(view.state).toBe(afterEdit);
    expect(undo(view)).toBe(true);
    expect(view.state.doc.toString()).toBe("alpha beta");
    const afterUndo = view.state;
    expect(shrinkQueryEditorSelection(view)).toBe(false);
    expect(view.state).toBe(afterUndo);
  });

  it("preserves history across non-selection effects and a failed expansion at the document boundary", () => {
    const { view } = createEditor("alpha", EditorSelection.single(2));
    extendQueryEditorSelection(view, { language: "text" });
    const before = view.state.selection;
    const effect = StateEffect.define<boolean>();
    view.dispatch({ effects: effect.of(true) });
    expect(extendQueryEditorSelection(view, { language: "text" })).toBe(false);
    expect(view.state.selection).toBe(before);
    expect(shrinkQueryEditorSelection(view)).toBe(true);
    expect(view.state.selection.eq(EditorSelection.single(2))).toBe(true);
  });

  it("does not dispatch when history is absent, empty, or an expansion was filtered out", () => {
    const empty = createEditor("alpha", EditorSelection.single(2));
    expect(shrinkQueryEditorSelection(empty.view)).toBe(false);
    expect(empty.dispatches).toBe(0);

    const withoutExtension = {
      state: EditorState.create({ doc: "alpha" }),
      dispatch: () => {
        throw new Error("unexpected dispatch");
      },
    } as unknown as EditorView;
    expect(shrinkQueryEditorSelection(withoutExtension)).toBe(false);

    const filtered = createEditor("alpha", EditorSelection.single(2), [EditorState.transactionFilter.of((transaction) => (transaction.selection ? [] : transaction))]);
    extendQueryEditorSelection(filtered.view, { language: "text" });
    expect(filtered.view.state.selection.eq(EditorSelection.single(2))).toBe(true);
    expect(shrinkQueryEditorSelection(filtered.view)).toBe(false);
    expect(filtered.dispatches).toBe(1);
  });

  it("keeps expansion history isolated between editors", () => {
    const first = createEditor("alpha beta", EditorSelection.single(2));
    const second = createEditor("alpha beta", EditorSelection.single(8));
    extendQueryEditorSelection(first.view, { language: "text" });
    expect(shrinkQueryEditorSelection(second.view)).toBe(false);
    expect(shrinkQueryEditorSelection(first.view)).toBe(true);
    expect(first.view.state.selection.eq(EditorSelection.single(2))).toBe(true);
    expect(second.view.state.selection.eq(EditorSelection.single(8))).toBe(true);
  });
});
