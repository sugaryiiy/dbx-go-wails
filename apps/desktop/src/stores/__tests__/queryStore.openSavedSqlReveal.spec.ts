import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useQueryStore } from "@/stores/queryStore";
import type { SavedSqlFile } from "@/types/database";

const savedFile: SavedSqlFile = {
  id: "saved-sql-reveal-1",
  connectionId: "conn-1",
  database: "main",
  name: "query.sql",
  sql: "SELECT 1;\nSELECT 2;\nSELECT 3;",
  sqlLoaded: true,
  createdAt: "2026-10-01T00:00:00.000Z",
  updatedAt: "2026-10-01T00:00:00.000Z",
};

function installLocalStorage() {
  const data = new Map<string, string>();
  vi.stubGlobal("localStorage", {
    getItem: (key: string) => data.get(key) ?? null,
    setItem: (key: string, value: string) => data.set(key, value),
    removeItem: (key: string) => data.delete(key),
  });
}

describe("queryStore openSavedSql with reveal", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.unstubAllGlobals();
    installLocalStorage();
    setActivePinia(createPinia());
  });

  it("sets editorRevealRequest when opening a new tab with reveal", () => {
    const store = useQueryStore();
    const tabId = store.openSavedSql(savedFile, {
      reveal: { line: 2, column: 5 },
    });

    const tab = store.tabs.find((t) => t.id === tabId);
    expect(tab).toBeDefined();
    expect(tab?.editorRevealRequest).toEqual(
      expect.objectContaining({
        line: 2,
        column: 5,
      }),
    );
    expect(tab?.editorRevealRequest?.id).toBeGreaterThan(0);
  });

  it("updates editorRevealRequest when focusing an existing tab with reveal", () => {
    const store = useQueryStore();
    const tabId = store.openSavedSql(savedFile);
    const tab = store.tabs.find((t) => t.id === tabId)!;
    expect(tab.editorRevealRequest).toBeUndefined();

    // Reopen same file with reveal
    store.openSavedSql(savedFile, {
      reveal: { line: 3, column: 1 },
    });

    expect(tab.editorRevealRequest).toEqual(
      expect.objectContaining({
        line: 3,
        column: 1,
      }),
    );
  });

  it("reveals an open query without replacing unsaved SQL or its execution target", () => {
    const store = useQueryStore();
    const tabId = store.openSavedSql(savedFile);
    const tab = store.tabs.find((candidate) => candidate.id === tabId)!;
    store.updateSql(tabId, "SELECT 1;\nSELECT 'unsaved change';");

    expect(store.requestEditorReveal(tabId, { line: 2, column: 8 })).toBe(true);
    const firstRequest = tab.editorRevealRequest!;
    expect(store.requestEditorReveal(tabId, { line: 2, column: 8 })).toBe(true);

    expect(tab.editorRevealRequest).toMatchObject({ line: 2, column: 8 });
    expect(tab.editorRevealRequest!.id).toBeGreaterThan(firstRequest.id);
    expect(tab.sql).toBe("SELECT 1;\nSELECT 'unsaved change';");
    expect(tab.connectionId).toBe(savedFile.connectionId);
    expect(tab.database).toBe(savedFile.database);
  });

  it("ignores a result whose query tab was closed", () => {
    const store = useQueryStore();
    expect(store.requestEditorReveal("closed-tab", { line: 7 })).toBe(false);
    expect(store.tabs).toHaveLength(0);
  });
});
