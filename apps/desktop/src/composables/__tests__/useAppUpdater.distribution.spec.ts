import { effectScope } from "vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useAppUpdater, canDownloadAndInstallUpdate, shouldOpenUpdateDialog } from "../useAppUpdater";
import { fetchChangelog, changelogReleaseUrl, changelogWebsiteUrl } from "@/lib/app/changelog";

const backend = vi.hoisted(() => ({ checkForUpdates: vi.fn(), downloadUpdate: vi.fn(), getDownloadedUpdate: vi.fn(), installDownloadedUpdate: vi.fn(), fetchChangelog: vi.fn() }));
vi.mock("@/lib/backend/api", () => backend);
vi.mock("@/lib/backend/tauriRuntime", () => ({ isTauriRuntime: () => true }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }), createI18n: () => ({ global: { locale: { value: "en" } } }) }));
vi.mock("@/stores/settingsStore", () => ({ useSettingsStore: () => ({ editorSettings: { autoUpdateApp: true, updateDownloadSource: "official" } }) }));

afterEach(() => {
  vi.clearAllMocks();
  vi.useRealTimers();
});

describe("separate application distribution", () => {
  it("blocks interactive and background update paths even when old automatic update preferences are enabled", async () => {
    vi.useFakeTimers();
    const scope = effectScope();
    const updater = scope.run(() => useAppUpdater())!;
    try {
      await updater.initialize();
      await updater.checkUpdates();
      await updater.downloadUpdateInBackground(true);
      await updater.installDownloadedUpdate();
      await vi.advanceTimersByTimeAsync(3_600_000);
      expect(backend.checkForUpdates).not.toHaveBeenCalled();
      expect(backend.getDownloadedUpdate).not.toHaveBeenCalled();
      expect(backend.downloadUpdate).not.toHaveBeenCalled();
      expect(backend.installDownloadedUpdate).not.toHaveBeenCalled();
      expect(updater.showUpdateDialog.value).toBe(false);
      expect(updater.hasUpdateAvailable.value).toBe(false);
      expect(updater.phase.value).toBe("idle");
      expect(shouldOpenUpdateDialog({})).toBe(false);
      expect(canDownloadAndInstallUpdate({ update_available: true } as never, true)).toBe(false);
    } finally {
      scope.stop();
    }
  });

  it("does not fetch or link to the upstream changelog", async () => {
    expect(await fetchChangelog("en", { force: true })).toEqual({ updatedAt: "", releases: [] });
    expect(backend.fetchChangelog).not.toHaveBeenCalled();
    expect(changelogWebsiteUrl("en")).toBe("");
    expect(changelogReleaseUrl("v1.0.0")).toBe("");
  });
});
