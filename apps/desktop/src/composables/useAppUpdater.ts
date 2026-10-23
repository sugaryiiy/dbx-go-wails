import { computed, ref, watch, onScopeDispose } from "vue";
import { useI18n } from "vue-i18n";
import { isTauriRuntime } from "@/lib/backend/tauriRuntime";
import { isWailsRuntime, onWailsEvent, openWailsUrl, wailsApp } from "@/lib/backend/wailsRuntime";
import * as api from "@/lib/backend/api";
import { useSettingsStore } from "@/stores/settingsStore";
import type { UpdateChannel as SettingsUpdateChannel, UpdateDownloadSource as SettingsUpdateDownloadSource } from "@/stores/settingsStore";
import type { UpdateDownloadProgress } from "@/lib/backend/backendTypes";
import { currentLocale } from "@/i18n";
import { shouldBlockAppUpdate } from "@/lib/app/appUpdateTaskGuard";
import { uuid } from "@/lib/common/utils";
import { isUpdatePreviewMockEnabled, previewAppUpdateInfo } from "@/lib/updates/updatePreviewMock";
import { APP_RELEASES_URL, APP_UPDATES_ENABLED, appReleaseTagUrl } from "@/lib/app/distribution";

interface UseAppUpdaterOptions {
  getActiveTaskCount?: () => number;
  prepareForUpdate?: () => Promise<() => void>;
}

export function shouldOpenUpdateDialog(options: { silent?: boolean }) {
  return APP_UPDATES_ENABLED && options.silent !== true;
}

export function canDownloadAndInstallUpdate(info: api.UpdateInfo | null, isDesktop: boolean) {
  return APP_UPDATES_ENABLED && isDesktop && info?.update_available === true && info.manual_update_only !== true;
}

interface ParsedUpdateVersion {
  core: [string, string, string];
  prerelease: string[];
}

function normalizeUpdateVersion(version: string): string {
  return version.trim().replace(/^[vV]/, "");
}

function parseUpdateVersion(version: string): ParsedUpdateVersion | null {
  const match = normalizeUpdateVersion(version).match(/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*))*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/);
  if (!match) return null;
  return {
    core: [match[1], match[2], match[3]],
    prerelease: match[4]?.split(".") ?? [],
  };
}

function compareNumericVersionIdentifier(left: string, right: string): number {
  if (left.length !== right.length) return left.length < right.length ? -1 : 1;
  return left === right ? 0 : left < right ? -1 : 1;
}

function compareParsedUpdateVersions(left: ParsedUpdateVersion, right: ParsedUpdateVersion): number {
  for (let index = 0; index < left.core.length; index += 1) {
    const comparison = compareNumericVersionIdentifier(left.core[index], right.core[index]);
    if (comparison !== 0) return comparison;
  }
  if (!left.prerelease.length || !right.prerelease.length) {
    if (left.prerelease.length === right.prerelease.length) return 0;
    return left.prerelease.length ? -1 : 1;
  }
  const length = Math.max(left.prerelease.length, right.prerelease.length);
  for (let index = 0; index < length; index += 1) {
    const leftIdentifier = left.prerelease[index];
    const rightIdentifier = right.prerelease[index];
    if (leftIdentifier === undefined || rightIdentifier === undefined) return leftIdentifier === undefined ? -1 : 1;
    if (leftIdentifier === rightIdentifier) continue;
    const leftNumeric = /^\d+$/.test(leftIdentifier);
    const rightNumeric = /^\d+$/.test(rightIdentifier);
    if (leftNumeric && rightNumeric) return compareNumericVersionIdentifier(leftIdentifier, rightIdentifier);
    if (leftNumeric !== rightNumeric) return leftNumeric ? -1 : 1;
    return leftIdentifier < rightIdentifier ? -1 : 1;
  }
  return 0;
}

export function isUpdateIgnored(info: api.UpdateInfo | null, ignoredVersion: string | undefined): boolean {
  const latest = info?.latest_version;
  if (!latest || !ignoredVersion) return false;
  const parsedLatest = parseUpdateVersion(latest);
  const parsedIgnored = parseUpdateVersion(ignoredVersion);
  if (!parsedLatest || !parsedIgnored) return normalizeUpdateVersion(latest) === normalizeUpdateVersion(ignoredVersion);
  return compareParsedUpdateVersions(parsedLatest, parsedIgnored) <= 0;
}

export function isNewerRemoteVersion(latest: string, cached: string): boolean {
  const parsedLatest = parseUpdateVersion(latest);
  const parsedCached = parseUpdateVersion(cached);
  if (!parsedLatest || !parsedCached) return normalizeUpdateVersion(latest) !== normalizeUpdateVersion(cached);
  return compareParsedUpdateVersions(parsedLatest, parsedCached) > 0;
}

export function normalizeUpdateDownloadSource(value: unknown): SettingsUpdateDownloadSource {
  // Old persisted AtomGit preferences should retain their mainland mirror behavior.
  if (value === "atomgit") return "cnb";
  return value === "cnb" ? "cnb" : "official";
}

export function tagVersion(version: string): string {
  const trimmed = version.trim();
  return trimmed.startsWith("v") ? trimmed : `v${trimmed}`;
}

export function resolveUpdateReleaseUrl(info: api.UpdateInfo | null, _source: unknown, fallbackUrl: string): string {
  return info?.release_url || fallbackUrl || APP_RELEASES_URL;
}

/** Release pages live on this distribution's own GitHub repository. */
export function resolveReleaseTagUrl(tag: string, _source: unknown): string {
  return appReleaseTagUrl(tag);
}

export async function resolveUpdaterProxy(): Promise<string | undefined> {
  // The Go backend performs update downloads itself and resolves any system
  // proxy on its side; the frontend never supplies one.
  return undefined;
}

export function useAppUpdater(options: UseAppUpdaterOptions = {}) {
  const { t } = useI18n();
  const settingsStore = useSettingsStore();
  type Phase = "idle" | "checking" | "downloading" | "ready" | "preparing" | "installing" | "restart";
  const phase = ref<Phase>("idle");
  const downloaded = ref<api.DownloadedUpdate | null>(null);
  const updateInfo = ref<api.UpdateInfo | null>(null);
  const updateCheckMessage = ref("");
  const updateCheckFailed = ref(false);
  const showUpdateDialog = ref(false);
  const downloadProgress = ref<number | null>(null);
  const isIgnoringUpdate = ref(false);
  const checkingUpdates = computed(() => phase.value === "checking");
  const isDownloadingUpdate = computed(() => phase.value === "downloading");
  const updateDownloaded = computed(() => downloaded.value !== null);
  const isPreparingUpdate = computed(() => phase.value === "preparing");
  const isInstallingUpdate = computed(() => phase.value === "installing" || isPreparingUpdate.value);
  const updateReady = computed(() => phase.value === "restart");
  const activeTaskCount = computed(() => Math.max(0, Math.trunc(options.getActiveTaskCount?.() ?? 0)));
  const autoUpdateEnabled = computed(() => APP_UPDATES_ENABLED && settingsStore.editorSettings.autoUpdateApp !== false);
  const hasUpdateAvailable = computed(() => APP_UPDATES_ENABLED && (updateDownloaded.value || updateReady.value || updateInfo.value?.update_available === true) && !isUpdateIgnored(updateInfo.value, settingsStore.editorSettings.ignoredUpdateVersion));
  const latestReleaseUrl = "";
  let generation = 0;
  let activeDownload: Promise<void> | undefined;
  let automaticDownload = false;
  let cancellation: Promise<void> | undefined;
  let cancelOperation: Promise<void> | undefined;
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  let hourlyTimer: ReturnType<typeof setInterval> | undefined;
  let retries = 0;
  let initialized = false;
  let disposed = false;
  let versionRecheckPending = false;
  let updatePreparationRelease: (() => void) | undefined;

  function clearRetry() {
    clearTimeout(retryTimer);
    retryTimer = undefined;
  }
  function fail(error: unknown) {
    updateCheckFailed.value = true;
    updateCheckMessage.value = formatUpdateError(error instanceof Error ? error.message : String(error));
  }
  function clearError() {
    updateCheckFailed.value = false;
    updateCheckMessage.value = "";
  }
  function scheduleRetry() {
    clearRetry();
    if (!initialized || disposed || retries >= 3) return;
    const delay = [60_000, 300_000, 900_000][retries++];
    retryTimer = setTimeout(() => {
      retryTimer = undefined;
      void checkUpdates({ silent: true });
    }, delay);
  }
  function setDownloaded(cache: api.DownloadedUpdate) {
    downloaded.value = cache;
    updateInfo.value = {
      current_version: updateInfo.value?.current_version ?? "",
      latest_version: cache.version,
      update_available: true,
      portable_mode: cache.portable_mode,
      manual_update_only: false,
      release_name: tagVersion(cache.version),
      release_url: cache.release_url,
      release_notes: cache.release_notes,
    };
    phase.value = "ready";
    downloadProgress.value = 100;
    retries = 0;
    clearRetry();
    clearError();
  }
  function openUrl(url: string) {
    if (!APP_UPDATES_ENABLED || !url) return;
    if (isWailsRuntime()) {
      openWailsUrl(url);
      return;
    }
    if (isTauriRuntime()) void import("@tauri-apps/plugin-shell").then(({ open }) => open(url));
    else window.open(url, "_blank", "noopener,noreferrer");
  }
  function openLatestRelease() {
    openUrl(resolveUpdateReleaseUrl(updateInfo.value, settingsStore.editorSettings.updateDownloadSource, latestReleaseUrl));
  }
  function formatUpdateError(message: string): string {
    const lower = message.toLowerCase();
    if (lower.includes("cancel")) return t("updates.downloadCanceled");
    if (lower.includes("403") || lower.includes("rate limit")) return t("updates.rateLimited");
    // A blocked update cache is a local write problem, not a download problem.
    if (lower.includes("update cache")) return t("updates.cacheWriteFailed", { error: message });
    if (["stalled", "timeout", "all available mirrors", "error sending request", "failed to download"].some((part) => lower.includes(part))) return t("updates.networkOrMirrorFailed");
    return t("updates.downloadFailed", { error: message });
  }
  function formatInstallError(message: string): string {
    if (message.toLowerCase().includes("powershell")) return t("updates.portableHelperFailed", { error: message });
    return t("updates.installFailed", { error: message });
  }
  async function checkUpdates(checkOptions: { silent?: boolean } = {}) {
    if (!APP_UPDATES_ENABLED) return;
    if (disposed || isIgnoringUpdate.value) return;
    if (!checkOptions.silent) showUpdateDialog.value = true;
    // A downloaded-but-uninstalled update keeps the app in the ready phase; checks
    // continue so a newer release can replace the cached package.
    if (phase.value !== "idle" && phase.value !== "ready") return;
    clearRetry();
    const token = ++generation;
    phase.value = "checking";
    clearError();
    try {
      if (isUpdatePreviewMockEnabled()) {
        if (token !== generation || disposed) return;
        updateInfo.value = previewAppUpdateInfo(updateInfo.value?.current_version || "");
        phase.value = "idle";
        return;
      }
      const info = await api.checkForUpdates(currentLocale(), normalizeUpdateDownloadSource(settingsStore.editorSettings.updateDownloadSource), settingsStore.editorSettings.updateChannel);
      if (token !== generation || disposed) return;
      updateInfo.value = info;
      // Installation may have started while the check was in flight; only restore
      // the phase this check itself owns.
      if (phase.value === "checking") phase.value = downloaded.value ? "ready" : "idle";
      if (!info.update_available) updateCheckMessage.value = t("updates.upToDate", { version: info.current_version });
      if (canDownloadAndInstallUpdate(info, isWailsRuntime()) && !isUpdateIgnored(info, settingsStore.editorSettings.ignoredUpdateVersion)) {
        const cached = downloaded.value;
        if (cached && !isNewerRemoteVersion(info.latest_version, cached.version)) {
          // Keep a prepared package installable without replacing it automatically.
          if (!autoUpdateEnabled.value) setDownloaded(cached);
          return;
        }
        // A cached package older than the remote release must not mask the newer version.
        if (cached && !(await discardSupersededUpdate(cached))) return;
        if (!autoUpdateEnabled.value) return;
        await downloadUpdateInBackground(true);
      }
    } catch (error) {
      if (token !== generation || disposed) return;
      if (phase.value === "checking") phase.value = downloaded.value ? "ready" : "idle";
      fail(error);
      scheduleRetry();
    }
  }
  async function discardSupersededUpdate(cache: api.DownloadedUpdate): Promise<boolean> {
    if (isInstallingUpdate.value || isIgnoringUpdate.value) return false;
    try {
      await api.discardDownloadedUpdate(cache.cache_id);
      if (downloaded.value?.cache_id === cache.cache_id) {
        downloaded.value = null;
        downloadProgress.value = null;
        if (phase.value === "ready" || phase.value === "checking") phase.value = "idle";
      }
      return true;
    } catch (error) {
      fail(error);
      return false;
    }
  }
  async function downloadUpdateInBackground(automatic = false) {
    if (!APP_UPDATES_ENABLED) return;
    if (disposed || isIgnoringUpdate.value || phase.value !== "idle" || downloaded.value || !canDownloadAndInstallUpdate(updateInfo.value, isWailsRuntime())) return;
    if (automatic && !autoUpdateEnabled.value) return;
    automaticDownload = automatic;
    const version = updateInfo.value!.latest_version;
    const token = ++generation;
    const attemptId = uuid();
    phase.value = "downloading";
    downloadProgress.value = null;
    clearError();
    clearRetry();
    const run = async () => {
      let unlisten: (() => void) | undefined;
      try {
        await cancellation;
        if (token !== generation || disposed) return;
        unlisten = onWailsEvent("update-download-progress", (...data: unknown[]) => {
          const payload = data[0] as UpdateDownloadProgress | undefined;
          if (!payload || token !== generation || payload.attempt_id !== attemptId || payload.version !== version) return;
          downloadProgress.value = payload.total && payload.total > 0 ? Math.min(100, Math.round((payload.downloaded / payload.total) * 100)) : null;
        });
        if (token !== generation || disposed) return;
        const cache = await api.downloadUpdate(normalizeUpdateDownloadSource(settingsStore.editorSettings.updateDownloadSource), version, attemptId, updateInfo.value?.release_notes);
        if (token !== generation || disposed) return;
        if (cache.version !== version) throw new Error("Downloaded update version changed; check for updates again.");
        setDownloaded(cache);
      } catch (error) {
        if (token !== generation || disposed) return;
        phase.value = "idle";
        fail(error);
        if ((String(error).includes("VERSION_CHANGED") || String(error).toLowerCase().includes("version changed")) && !versionRecheckPending) {
          versionRecheckPending = true;
          setTimeout(() => {
            void checkUpdates({ silent: true }).finally(() => {
              versionRecheckPending = false;
            });
          });
        } else scheduleRetry();
      } finally {
        unlisten?.();
      }
    };
    activeDownload = run();
    await activeDownload;
    activeDownload = undefined;
    automaticDownload = false;
  }
  function cancelDownload(): Promise<void> {
    if (cancelOperation) return cancelOperation;
    cancelOperation = cancelDownloadImpl().finally(() => {
      cancelOperation = undefined;
    });
    return cancelOperation;
  }
  async function cancelDownloadImpl() {
    clearRetry();
    generation++;
    const running = activeDownload;
    if (phase.value === "checking") phase.value = "idle";
    if (!running) return;
    const pending = api.cancelUpdateDownload();
    cancellation = pending;
    try {
      await pending;
      await running;
      if (!disposed) {
        const cache = await api.getDownloadedUpdate();
        if (cache && !isUpdateIgnored({ latest_version: cache.version } as api.UpdateInfo, settingsStore.editorSettings.ignoredUpdateVersion)) setDownloaded(cache);
      }
    } finally {
      if (cancellation === pending) cancellation = undefined;
      if (phase.value === "downloading") phase.value = "idle";
    }
  }
  async function changeUpdateChannel(channel: SettingsUpdateChannel) {
    if (!APP_UPDATES_ENABLED) return;
    if (isIgnoringUpdate.value || isInstallingUpdate.value) return;
    try {
      await settingsStore.updateEditorSettingsAndPersist({ updateChannel: channel });
      if (downloaded.value || updateReady.value || isInstallingUpdate.value) return;
      await cancelDownload();
      retries = 0;
      await checkUpdates();
    } catch (error) {
      fail(error);
    }
  }
  async function changeUpdateDownloadSource(source: SettingsUpdateDownloadSource) {
    if (!APP_UPDATES_ENABLED) return;
    if (isIgnoringUpdate.value || isInstallingUpdate.value) return;
    try {
      await settingsStore.updateEditorSettingsAndPersist({ updateDownloadSource: source });
      if (downloaded.value || updateReady.value || isInstallingUpdate.value) return;
      await cancelDownload();
      retries = 0;
      await checkUpdates();
    } catch (error) {
      fail(error);
    }
  }
  async function ignoreCurrentVersion() {
    const version = updateInfo.value?.latest_version;
    if (!version || isIgnoringUpdate.value || isInstallingUpdate.value || updateReady.value) return;
    isIgnoringUpdate.value = true;
    try {
      await cancelDownload();
      await settingsStore.updateEditorSettingsAndPersist({ ignoredUpdateVersion: version });
      const cache = downloaded.value;
      downloaded.value = null;
      phase.value = "idle";
      if (cache) await api.discardDownloadedUpdate(cache.cache_id);
      showUpdateDialog.value = false;
    } catch (error) {
      fail(error);
    } finally {
      isIgnoringUpdate.value = false;
    }
  }
  async function performInstall(restartOnly: boolean) {
    if (!APP_UPDATES_ENABLED && !restartOnly) return;
    if (!isWailsRuntime() || isInstallingUpdate.value || isIgnoringUpdate.value || (!restartOnly && !downloaded.value)) return;
    const cache = downloaded.value;
    let release: (() => void) | undefined;
    phase.value = "preparing";
    clearError();
    let installed = restartOnly;
    let failed = false;
    try {
      release = await options.prepareForUpdate?.();
      updatePreparationRelease = release;
      if (shouldBlockAppUpdate(activeTaskCount.value)) throw new Error(t("updates.activeTasksBlockUpdate", { count: activeTaskCount.value }));
      phase.value = "installing";
      if (!restartOnly && cache) {
        await api.installDownloadedUpdate(cache.cache_id, cache.version);
        installed = true;
        downloaded.value = null;
      }
      // The Go install binding stages the package, starts the detached
      // update helper and drives the normal quit flow; the helper swaps the
      // package and relaunches once this process has exited. A bare restart
      // request means an earlier install was staged but its quit was
      // cancelled, so this process quits directly and the helper takes over.
      if (restartOnly) {
        phase.value = "restart";
        await wailsApp().Close();
      } else if (installed) {
        phase.value = "restart";
      }
    } catch (error) {
      failed = true;
      phase.value = installed ? "restart" : "ready";
      updateCheckFailed.value = true;
      const message = error instanceof Error ? error.message : String(error);
      updateCheckMessage.value = installed ? t("updates.restartFailed", { error: message }) : formatInstallError(message);
    } finally {
      // Successful handoff must keep every window frozen until the process exits.
      if (failed) {
        release?.();
        updatePreparationRelease = undefined;
      }
      if (!installed && cache) {
        try {
          const restored = await api.getDownloadedUpdate();
          if (!restored || restored.cache_id !== cache.cache_id) {
            downloaded.value = null;
            phase.value = "idle";
          }
        } catch {
          /* Keep the original installation error available for retry. */
        }
      }
    }
  }
  async function installDownloadedUpdate() {
    await performInstall(false);
  }
  async function restartApp() {
    if (updateReady.value) await performInstall(true);
  }
  async function initialize() {
    if (!APP_UPDATES_ENABLED) return;
    if (initialized || disposed) return;
    initialized = true;
    if (isWailsRuntime()) {
      phase.value = "checking";
      try {
        const cache = await api.getDownloadedUpdate();
        if (disposed) return;
        if (cache) {
          const ignored = isUpdateIgnored({ latest_version: cache.version } as api.UpdateInfo, settingsStore.editorSettings.ignoredUpdateVersion);
          if (ignored) await api.discardDownloadedUpdate(cache.cache_id);
          else {
            setDownloaded(cache);
            updateInfo.value!.current_version = await api.getAppVersion();
          }
        }
      } catch (error) {
        fail(error);
      } finally {
        if (phase.value === "checking") phase.value = "idle";
      }
    }
    if (disposed) return;
    hourlyTimer = setInterval(() => {
      if (!retryTimer) {
        retries = 0;
        void checkUpdates({ silent: true });
      }
    }, 3_600_000);
    void checkUpdates({ silent: true });
  }
  const stopSettingsWatch = watch(autoUpdateEnabled, (enabled) => {
    if (disposed) return;
    if (!enabled) {
      clearRetry();
      if (automaticDownload) void cancelDownload().catch(fail);
      return;
    }
    if (!initialized) return;
    void (cancelOperation ?? Promise.resolve())
      .then(() => {
        if (autoUpdateEnabled.value && !disposed) void checkUpdates({ silent: true });
      })
      .catch(fail);
  });
  function dispose() {
    disposed = true;
    clearRetry();
    clearInterval(hourlyTimer);
    stopSettingsWatch();
    updatePreparationRelease?.();
    updatePreparationRelease = undefined;
    void cancelDownload().catch(() => {});
  }
  onScopeDispose(dispose);
  return {
    phase,
    checkingUpdates,
    updateInfo,
    updateCheckMessage,
    updateCheckFailed,
    showUpdateDialog,
    isDownloadingUpdate,
    downloadProgress,
    updateDownloaded,
    isInstallingUpdate,
    isPreparingUpdate,
    updateReady,
    isIgnoringUpdate,
    activeTaskCount,
    hasUpdateAvailable,
    latestReleaseUrl,
    openUrl,
    checkUpdates,
    formatUpdateError,
    openLatestRelease,
    changeUpdateDownloadSource,
    changeUpdateChannel,
    ignoreCurrentVersion,
    downloadUpdateInBackground,
    cancelDownload,
    installDownloadedUpdate,
    restartApp,
    initialize,
    dispose,
  };
}
