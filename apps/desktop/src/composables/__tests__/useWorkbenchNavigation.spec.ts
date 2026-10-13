// @vitest-environment happy-dom

import { ref } from "vue";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { useWorkbenchNavigation, type WorkbenchNavigationSection } from "../useWorkbenchNavigation";

function state(overrides: Partial<Record<"sidebarOpen" | "history" | "sqlLibrary" | "sqlFile", boolean>> = {}) {
  return {
    sidebarOpen: ref(overrides.sidebarOpen ?? true),
    history: ref(overrides.history ?? false),
    sqlLibrary: ref(overrides.sqlLibrary ?? false),
    sqlFile: ref(overrides.sqlFile ?? false),
  };
}

beforeEach(() => localStorage.clear());
afterEach(() => localStorage.clear());

describe("Unified workbench navigation", () => {
  it("restores one saved section over conflicting legacy panels while preserving a collapsed dock", () => {
    localStorage.setItem("magpiedb-navigation-section", "history");
    localStorage.setItem("dbx-sidebar-open", "false");
    localStorage.setItem("dbx-sql-library-open", "true");
    localStorage.setItem("dbx-sql-file-panel-open", "true");
    const refs = state({ sidebarOpen: false, sqlLibrary: true, sqlFile: true });
    const navigation = useWorkbenchNavigation(refs);

    expect(navigation.activeSection.value).toBe("history");
    expect(refs.history.value).toBe(true);
    expect(refs.sqlLibrary.value).toBe(false);
    expect(refs.sqlFile.value).toBe(false);
    expect(refs.sidebarOpen.value).toBe(false);
    expect(localStorage.getItem("dbx-sidebar-open")).toBe("false");
    expect(localStorage.getItem("dbx-sql-library-open")).toBe("false");
    expect(localStorage.getItem("dbx-sql-file-panel-open")).toBe("false");
  });

  it("migrates legacy multiple-panel state even when the saved section is invalid", () => {
    localStorage.setItem("magpiedb-navigation-section", "removed-section");
    const refs = state({ sqlLibrary: true, sqlFile: true });
    const navigation = useWorkbenchNavigation(refs);

    expect(navigation.activeSection.value).toBe("sqlLibrary");
    expect(refs.sqlFile.value).toBe(false);
    expect(localStorage.getItem("magpiedb-navigation-section")).toBe("sqlLibrary");
    expect(localStorage.getItem("dbx-sql-library-open")).toBe("true");
    expect(localStorage.getItem("dbx-sql-file-panel-open")).toBe("false");
  });

  it.each(["objects", "history", "sqlLibrary", "sqlFile"] as WorkbenchNavigationSection[])("opens %s exclusively and restores it in the next session", (section) => {
    const refs = state({ sidebarOpen: false, sqlLibrary: true });
    const navigation = useWorkbenchNavigation(refs);
    navigation.selectSection(section);

    expect(navigation.activeSection.value).toBe(section);
    expect(refs.sidebarOpen.value).toBe(true);
    expect([refs.history.value, refs.sqlLibrary.value, refs.sqlFile.value].filter(Boolean)).toHaveLength(section === "objects" ? 0 : 1);
    expect(localStorage.getItem("dbx-sidebar-open")).toBe("true");
    expect(localStorage.getItem("dbx-sql-library-open")).toBe(String(section === "sqlLibrary"));
    expect(localStorage.getItem("dbx-sql-file-panel-open")).toBe(String(section === "sqlFile"));

    const restored = useWorkbenchNavigation(state());
    expect(restored.activeSection.value).toBe(section);
  });

  it("returns a closed active panel to objects without dismissing an unrelated panel", () => {
    const navigation = useWorkbenchNavigation(state());
    navigation.selectSection("history");
    navigation.closeSection("sqlLibrary");
    expect(navigation.activeSection.value).toBe("history");
    navigation.closeSection("history");
    expect(navigation.activeSection.value).toBe("objects");
    expect(localStorage.getItem("magpiedb-navigation-section")).toBe("objects");
  });

  it("leaves the independent AI inspector and its persisted visibility intact", () => {
    const aiOpen = ref(true);
    localStorage.setItem("dbx-ai-panel-open", "true");
    const refs = { ...state(), aiOpen };
    const navigation = useWorkbenchNavigation(refs);
    navigation.selectSection("history");
    navigation.selectSection("sqlFile");
    navigation.closeSection("sqlFile");

    expect(aiOpen.value).toBe(true);
    expect(localStorage.getItem("dbx-ai-panel-open")).toBe("true");
  });
});
