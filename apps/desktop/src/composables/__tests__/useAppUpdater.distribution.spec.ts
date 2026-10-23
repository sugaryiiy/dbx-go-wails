import { effectScope } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useAppUpdater, canDownloadAndInstallUpdate, resolveReleaseTagUrl, resolveUpdateReleaseUrl, shouldOpenUpdateDialog } from "../useAppUpdater";
import { fetchChangelog, changelogReleaseUrl, changelogWebsiteUrl } from "@/lib/app/changelog";

const backend = vi.hoisted(() => ({ checkForUpdates: vi.fn(), downloadUpdate: vi.fn(), getDownloadedUpdate: vi.fn(), installDownloadedUpdate: vi.fn(), fetchChangelog: vi.fn(), cancelUpdateDownload: vi.fn(), discardDownloadedUpdate: vi.fn(), getAppVersion: vi.fn() }));
vi.mock("@/lib/backend/api", () => backend);
vi.mock("@/lib/backend/tauriRuntime", () => ({ isTauriRuntime: () => false }));
vi.mock("@/lib/backend/wailsRuntime", () => ({
  isWailsRuntime: () => true,
  onWailsEvent: () => () => {},
  wailsApp: () => ({ Close: vi.fn() }),
  openWailsUrl: () => true,
}));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }), createI18n: () => ({ global: { locale: { value: "en" } } }) }));
vi.mock("@/stores/settingsStore", () => ({
  useSettingsStore: () => ({
    editorSettings: { autoUpdateApp: true, updateDownloadSource: "official", updateChannel: "stable", ignoredUpdateVersion: "" },
    updateEditorSettingsAndPersist: vi.fn(async () => {}),
  }),
}));

const info = { current_version: "0.6.33", latest_version: "0.6.34", update_available: true, portable_mode: false, manual_update_only: false, release_name: "v0.6.34", release_url: "https://github.com/sugaryiiy/magpiedb/releases/tag/v0.6.34", release_notes: "Changes" };
const cache = { cache_id: "cached", version: "0.6.34", portable_mode: false, release_url: info.release_url, release_notes: "Changes", downloaded_at: 1 };

beforeEach(() => {
  backend.checkForUpdates.mockResolvedValue(info);
  backend.downloadUpdate.mockResolvedValue(cache);
  backend.getDownloadedUpdate.mockResolvedValue(null);
  backend.getAppVersion.mockResolvedValue("0.6.33");
  backend.fetchChangelog.mockResolvedValue({ updatedAt: "2026-10-24T03:00:00Z", releases: [{ tag: "v0.6.34", name: "MagpieDB 0.6.34", date: "2026-10-24T03:00:00Z", sections: [] }] });
});

afterEach(() => {
  vi.clearAllMocks();
  vi.useRealTimers();
});

describe("this distribution's own update channel", () => {
  it("runs checks and automatic downloads against its own backend", async () => {
    vi.useFakeTimers();
    const scope = effectScope();
    const updater = scope.run(() => useAppUpdater())!;
    try {
      await updater.initialize();
      await vi.advanceTimersByTimeAsync(0);
      await vi.advanceTimersByTimeAsync(0);
      expect(backend.getDownloadedUpdate).toHaveBeenCalled();
      expect(backend.checkForUpdates).toHaveBeenCalledWith(expect.any(String), "official", "stable");
      expect(backend.downloadUpdate).toHaveBeenCalledWith("official", "0.6.34", expect.any(String), "Changes");
      expect(updater.hasUpdateAvailable.value).toBe(true);
      expect(updater.updateDownloaded.value).toBe(true);
      expect(shouldOpenUpdateDialog({})).toBe(true);
      expect(shouldOpenUpdateDialog({ silent: true })).toBe(false);
      expect(canDownloadAndInstallUpdate(info as never, true)).toBe(true);
      expect(canDownloadAndInstallUpdate(info as never, false)).toBe(false);
    } finally {
      scope.stop();
    }
  });

  it("fetches the changelog from the backend and links to this repository's releases", async () => {
    const data = await fetchChangelog("en", { force: true });
    expect(backend.fetchChangelog).toHaveBeenCalled();
    expect(data.releases).toHaveLength(1);
    expect(data.releases[0]?.tag).toBe("v0.6.34");
    expect(changelogWebsiteUrl("en")).toBe("https://github.com/sugaryiiy/magpiedb/releases");
    expect(changelogReleaseUrl("v1.0.0")).toBe("https://github.com/sugaryiiy/magpiedb/releases/tag/v1.0.0");
    expect(resolveReleaseTagUrl("0.6.34", "official")).toBe("https://github.com/sugaryiiy/magpiedb/releases/tag/v0.6.34");
    expect(resolveUpdateReleaseUrl(info as never, "official", "")).toBe(info.release_url);
  });
});
