package server

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/sugaryiiy/magpiedb/internal/updater"
	"github.com/sugaryiiy/magpiedb/internal/version"
)

// Application update checks read this distribution's own GitHub releases.
// The frontend contract (UpdateInfo in lib/backend/backendTypes.ts) is served
// by /api/update/check; /api/changelog maps the same release data onto the
// frontend ChangelogData shape, and /api/version reports version.Version.
// Release discovery and asset selection live in internal/updater, shared
// with the desktop download/install bindings, so a check and the download
// it green-lights can never disagree about the asset or its checksum.

// updateCacheTTL bounds how often one process may hit the unauthenticated
// GitHub API (60 requests/hour/IP). The frontend checks on startup and then
// hourly, so a one-hour server cache keeps a single app instance well inside
// the budget even with several manual re-checks.
const updateCacheTTL = time.Hour

// updateAPIBase resolves the GitHub API root. DBX_UPDATE_API_BASE exists so
// local end-to-end drills can point the checker at a fake release server.
func updateAPIBase() string {
	if base := strings.TrimSpace(os.Getenv("DBX_UPDATE_API_BASE")); base != "" {
		return strings.TrimSuffix(base, "/")
	}
	return updater.DefaultAPIBase
}

func (s *Server) updateClient() *updater.GitHubClient {
	base := s.updateAPIBase
	if base == "" {
		base = updater.DefaultAPIBase
	}
	return updater.NewGitHubClient(base, version.Version)
}

// updateReleaseCache memoizes the two GitHub endpoints the checker needs:
// /releases/latest (stable channel) and /releases?per_page=20 (preview
// channel and changelog).
type updateReleaseCache struct {
	latestFetchedAt time.Time
	latest          *updater.Release
	latestMissing   bool
	listFetchedAt   time.Time
	list            []updater.Release
}

func (s *Server) latestRelease(ctx context.Context) (*updater.Release, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if s.updateCache == nil {
		s.updateCache = &updateReleaseCache{}
	}
	cache := s.updateCache
	if time.Since(cache.latestFetchedAt) < updateCacheTTL && (cache.latest != nil || cache.latestMissing) {
		return cache.latest, nil
	}
	release, err := s.updateClient().Latest(ctx)
	if err != nil {
		return nil, err
	}
	cache.latestFetchedAt = time.Now()
	cache.latestMissing = release == nil
	cache.latest = release
	return cache.latest, nil
}

func (s *Server) releaseList(ctx context.Context) ([]updater.Release, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if s.updateCache == nil {
		s.updateCache = &updateReleaseCache{}
	}
	cache := s.updateCache
	if cache.list != nil && time.Since(cache.listFetchedAt) < updateCacheTTL {
		return cache.list, nil
	}
	releases, err := s.updateClient().List(ctx, 20)
	if err != nil {
		return nil, err
	}
	cache.listFetchedAt = time.Now()
	cache.list = releases
	return releases, nil
}

// updateInstallMode decides whether this process may download and install
// an update itself. Web mode is always manual-only. A desktop build whose
// install location is not writable (system-owned /Applications entry, a
// DMG mount, an App Translocation path) reports portable_mode with
// manual-only, matching the legacy frontend semantics.
func updateInstallMode(desktop bool, executablePath string, goos string) (portableMode, manualOnly bool) {
	if !desktop {
		return false, true
	}
	if !updater.InstallLocationWritable(executablePath, goos) {
		return true, true
	}
	return false, false
}

func updateInstallWritable(executablePath, goos string) bool {
	return updater.InstallLocationWritable(executablePath, goos)
}

// buildUpdateInfo assembles the frontend UpdateInfo contract for a
// candidate release. A candidate whose version is not strictly newer than
// the running build reports update_available=false. When the platform
// asset is missing from the release, the update stays visible but is
// downgraded to manual-only: the updater never guesses a substitute asset.
func buildUpdateInfo(current string, candidate *updater.Release, desktop bool, executablePath, goos, goarch string) map[string]any {
	portableMode, manualOnly := updateInstallMode(desktop, executablePath, goos)
	info := map[string]any{
		"current_version":    current,
		"latest_version":     current,
		"update_available":   false,
		"portable_mode":      portableMode,
		"manual_update_only": manualOnly,
		"release_name":       "",
		"release_url":        "",
		"release_notes":      "",
	}
	if candidate == nil {
		return info
	}
	info["latest_version"] = candidate.Version()
	info["release_name"] = candidate.Name
	if info["release_name"] == "" {
		info["release_name"] = candidate.TagName
	}
	info["release_url"] = candidate.HTMLURL
	info["release_notes"] = candidate.Body
	if !updater.IsNewer(candidate.TagName, current) {
		return info
	}
	info["update_available"] = true
	if updater.SelectAsset(candidate, goos, goarch) == nil {
		info["manual_update_only"] = true
	}
	return info
}

func (s *Server) handleUpdateCheck(r *http.Request) (map[string]any, error) {
	channel := updater.NormalizeChannel(r.URL.Query().Get("channel"))
	latest, err := s.latestRelease(r.Context())
	if err != nil {
		return nil, err
	}
	var candidate *updater.Release
	if channel == "preview" {
		list, err := s.releaseList(r.Context())
		if err != nil {
			return nil, err
		}
		candidate = updater.SelectCandidate(channel, latest, list)
	} else {
		candidate = updater.SelectCandidate(channel, latest, nil)
	}
	executable, _ := os.Executable()
	return buildUpdateInfo(version.Version, candidate, s.desktop, executable, runtime.GOOS, runtime.GOARCH), nil
}

// handleChangelog maps the release list onto the frontend ChangelogData
// shape ({updatedAt, releases: [{tag, name, date, sections}]}). Only the
// history tab consumes it, and it reads tag/date; the release body is
// carried as a single notes section so nothing is silently dropped.
func (s *Server) handleChangelog(r *http.Request) (map[string]any, error) {
	releases, err := s.releaseList(r.Context())
	if err != nil {
		return nil, err
	}
	mapped := make([]any, 0, len(releases))
	updatedAt := ""
	for _, release := range releases {
		if release.Draft {
			continue
		}
		if updatedAt == "" {
			updatedAt = release.Published
		}
		name := release.Name
		if name == "" {
			name = release.TagName
		}
		sections := []any{}
		if strings.TrimSpace(release.Body) != "" {
			sections = append(sections, map[string]any{
				"type":  "notes",
				"title": name,
				"items": []any{map[string]any{"title": release.TagName, "desc": release.Body}},
			})
		}
		mapped = append(mapped, map[string]any{
			"tag":      release.TagName,
			"name":     name,
			"date":     release.Published,
			"sections": sections,
		})
	}
	return map[string]any{"updatedAt": updatedAt, "releases": mapped}, nil
}

// UpdateService exposes the download/install service rooted at this
// server's data directory, for the desktop shell's Wails bindings.
func (s *Server) UpdateService() *updater.Service {
	service := updater.NewService(updater.CacheRoot(s.dataDir), s.updateClient())
	executable, _ := os.Executable()
	portable, _ := updateInstallMode(s.desktop, executable, runtime.GOOS)
	service.PortableMode = portable
	return service
}

// ConfirmUpdatePending resolves the pending record of a previous install
// at startup: a swap that landed on this build is confirmed (its backup
// removed), anything else is preserved for the UI to report or dismiss.
func (s *Server) ConfirmUpdatePending() {
	_ = s.UpdateService().ConfirmPending(version.Version)
}
