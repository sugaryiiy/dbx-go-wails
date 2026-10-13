import { computed, type Ref } from "vue";
import { safeLocalStorageGet, safeLocalStorageSet } from "@/lib/backend/safeStorage";

export type WorkbenchNavigationSection = "objects" | "sqlLibrary" | "history" | "sqlFile";

interface NavigationState {
  sidebarOpen: Ref<boolean>;
  history: Ref<boolean>;
  sqlLibrary: Ref<boolean>;
  sqlFile: Ref<boolean>;
}

const SECTION_KEY = "magpiedb-navigation-section";
const sections: WorkbenchNavigationSection[] = ["objects", "sqlLibrary", "history", "sqlFile"];

/** One navigation surface; opening a resource never closes the independent AI inspector. */
export function useWorkbenchNavigation(state: NavigationState) {
  const activeSection = computed<WorkbenchNavigationSection>(() => (state.history.value ? "history" : state.sqlLibrary.value ? "sqlLibrary" : state.sqlFile.value ? "sqlFile" : "objects"));

  function applySection(section: WorkbenchNavigationSection) {
    state.history.value = section === "history";
    state.sqlLibrary.value = section === "sqlLibrary";
    state.sqlFile.value = section === "sqlFile";
  }

  function persistSection(section: WorkbenchNavigationSection) {
    safeLocalStorageSet(SECTION_KEY, section);
    // Retain legacy keys so existing shortcuts and saved-SQL feedback agree with the dock.
    safeLocalStorageSet("dbx-sql-library-open", String(state.sqlLibrary.value));
    safeLocalStorageSet("dbx-sql-file-panel-open", String(state.sqlFile.value));
  }

  const saved = safeLocalStorageGet(SECTION_KEY);
  applySection(sections.includes(saved as WorkbenchNavigationSection) ? (saved as WorkbenchNavigationSection) : activeSection.value);
  // Migrate old multi-panel settings without expanding a collapsed navigation dock.
  persistSection(activeSection.value);

  function selectSection(section: WorkbenchNavigationSection) {
    applySection(section);
    state.sidebarOpen.value = true;
    persistSection(section);
    safeLocalStorageSet("dbx-sidebar-open", "true");
  }

  function closeSection(section: WorkbenchNavigationSection) {
    if (activeSection.value === section) selectSection("objects");
  }

  return { activeSection, selectSection, closeSection };
}
