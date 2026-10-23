// This workspace is a separate distribution: it must never install an
// upstream application package (that would replace its local functionality
// and branding). App updates are served from this distribution's own
// GitHub releases (sugaryiiy/magpiedb) through the Go backend, so the
// update machinery is enabled with this distribution's own endpoints.
export const APP_UPDATES_ENABLED: boolean = true;

// Release pages for this distribution. The update checker returns the
// concrete release URL per check; these helpers cover the places that only
// have a tag (history/rollback entries) or need the generic releases page.
export const APP_RELEASES_URL = "https://github.com/sugaryiiy/magpiedb/releases";

export function appReleaseTagUrl(tag: string): string {
  const trimmed = tag.trim();
  const normalized = trimmed.startsWith("v") ? trimmed : `v${trimmed}`;
  return `${APP_RELEASES_URL}/tag/${normalized}`;
}
