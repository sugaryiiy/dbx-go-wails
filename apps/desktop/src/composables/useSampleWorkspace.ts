import { ref } from "vue";
import * as api from "@/lib/backend/api";
import { isTauriRuntime } from "@/lib/backend/tauriRuntime";
import { useConnectionStore } from "@/stores/connectionStore";
import { useQueryStore } from "@/stores/queryStore";

export function useSampleWorkspace(options: { queryTitle: () => string; onError: (error: unknown) => void }) {
  const connectionStore = useConnectionStore();
  const queryStore = useQueryStore();
  const openingSampleWorkspace = ref(false);

  async function openSampleWorkspace() {
    if (isTauriRuntime() || openingSampleWorkspace.value) return;
    openingSampleWorkspace.value = true;
    try {
      const { connection, sql } = await api.createSampleWorkspace();
      await connectionStore.reloadFromDisk();
      await connectionStore.ensureConnected(connection.id);
      connectionStore.activeConnectionId = connection.id;
      // Reopening uses the existing named tab, preserving any edits the user made.
      // SQL is supplied to the editor, never executed by the welcome action.
      queryStore.createTab(connection.id, connection.database || "main", options.queryTitle(), "query", undefined, sql);
    } catch (error) {
      options.onError(error);
    } finally {
      openingSampleWorkspace.value = false;
    }
  }

  return { openingSampleWorkspace, openSampleWorkspace };
}
