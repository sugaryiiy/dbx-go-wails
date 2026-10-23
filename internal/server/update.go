package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	semver "github.com/Masterminds/semver/v3"
	"github.com/sugaryiiy/magpiedb/internal/version"
)

// Application update checks read this distribution's own GitHub releases.
// The frontend contract (UpdateInfo in lib/backend/backendTypes.ts) is served
// by /api/update/check; /api/changelog maps the same release data onto the
// frontend ChangelogData shape, and /api/version reports version.Version.

const updateRepoOwner = "sugaryiiy"
const updateRepoName = "magpiedb"

const defaultUpdateAPIBase = "https://api.github.com"

// updateCacheTTL bounds how often one process may hit the unauthenticated
// GitHub API (60 requests/hour/IP). The frontend checks on startup and then
// hourly, so a one-hour server cache keeps a single app instance well inside
// the budget even with several manual re-checks.
const updateCacheTTL = time.Hour

const maxUpdateAPIBytes = 8 << 20

// updateAPIBase resolves the GitHub API root. DBX_UPDATE_API_BASE exists so
// local end-to-end drills can point the checker at a fake release server.
func updateAPIBase() string {
	if base := strings.TrimSpace(os.Getenv("DBX_UPDATE_API_BASE")); base != "" {
		return strings.TrimSuffix(base, "/")
	}
	return defaultUpdateAPIBase
}

// githubRelease mirrors the subset of the GitHub release object the update
// flow consumes. Digest is the asset checksum GitHub computes at upload time
// ("sha256:<hex>"); it is the primary integrity source for downloads.
type githubRelease struct {
	TagName    string             `json:"tag_name"`
	Name       string             `json:"name"`
	HTMLURL    string             `json:"html_url"`
	Body       string             `json:"body"`
	Draft      bool               `json:"draft"`
	Prerelease bool               `json:"prerelease"`
	Published  string             `json:"published_at"`
	Assets     []githubAssetEntry `json:"assets"`
}

type githubAssetEntry struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Digest      string `json:"digest"`
	Size        int64  `json:"size"`
}

// updateReleaseCache memoizes the two GitHub endpoints the checker needs:
// /releases/latest (stable channel) and /releases?per_page=20 (preview
// channel and changelog).
type updateReleaseCache struct {
	latestFetchedAt time.Time
	latest          *githubRelease
	latestMissing   bool
	listFetchedAt   time.Time
	list            []githubRelease
}

func (s *Server) updateHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(request *http.Request, previous []*http.Request) error {
		if len(previous) >= 5 {
			return errors.New("update check redirect limit exceeded")
		}
		if len(previous) > 0 && previous[0].URL.Scheme == "https" && request.URL.Scheme != "https" {
			return errors.New("update check redirects cannot downgrade HTTPS")
		}
		return nil
	}}
}

func (s *Server) fetchUpdateJSON(ctx context.Context, path string, target any) (bool, error) {
	base := s.updateAPIBase
	if base == "" {
		base = defaultUpdateAPIBase
	}
	endpoint := base + "/repos/" + updateRepoOwner + "/" + updateRepoName + path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "MagpieDB/"+version.Version)
	response, err := s.updateHTTPClient().Do(request)
	if err != nil {
		return false, fmt.Errorf("update check request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, fmt.Errorf("update check returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxUpdateAPIBytes+1))
	if err != nil {
		return false, err
	}
	if int64(len(data)) > maxUpdateAPIBytes {
		return false, errors.New("update check response exceeds its size limit")
	}
	if err := json.Unmarshal(data, target); err != nil {
		return false, fmt.Errorf("update check response is not valid JSON: %w", err)
	}
	return true, nil
}

func (s *Server) latestRelease(ctx context.Context) (*githubRelease, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if s.updateCache == nil {
		s.updateCache = &updateReleaseCache{}
	}
	cache := s.updateCache
	if time.Since(cache.latestFetchedAt) < updateCacheTTL && (cache.latest != nil || cache.latestMissing) {
		return cache.latest, nil
	}
	var release githubRelease
	found, err := s.fetchUpdateJSON(ctx, "/releases/latest", &release)
	if err != nil {
		return nil, err
	}
	cache.latestFetchedAt = time.Now()
	cache.latestMissing = !found
	if found {
		releaseCopy := release
		cache.latest = &releaseCopy
	} else {
		cache.latest = nil
	}
	return cache.latest, nil
}

func (s *Server) releaseList(ctx context.Context) ([]githubRelease, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if s.updateCache == nil {
		s.updateCache = &updateReleaseCache{}
	}
	cache := s.updateCache
	if cache.list != nil && time.Since(cache.listFetchedAt) < updateCacheTTL {
		return cache.list, nil
	}
	var releases []githubRelease
	found, err := s.fetchUpdateJSON(ctx, "/releases?per_page=20", &releases)
	if err != nil {
		return nil, err
	}
	if !found {
		releases = nil
	}
	cache.listFetchedAt = time.Now()
	cache.list = releases
	return releases, nil
}

// normalizeUpdateChannel keeps the contract small: anything that is not
// explicitly "preview" resolves to the stable channel.
func normalizeUpdateChannel(value string) string {
	if value == "preview" {
		return "preview"
	}
	return "stable"
}

// selectChannelCandidate picks the release a channel points at. Preview takes
// the newest prerelease, unless the newest stable release is even newer (a
// preview channel must never offer a downgrade). Drafts never qualify.
func selectChannelCandidate(channel string, latest *githubRelease, list []githubRelease) *githubRelease {
	if channel != "preview" {
		if latest != nil && !latest.Draft {
			return latest
		}
		return nil
	}
	var preview *githubRelease
	for index := range list {
		if list[index].Prerelease && !list[index].Draft {
			preview = &list[index]
			break
		}
	}
	if latest != nil && !latest.Draft {
		if preview == nil {
			return latest
		}
		previewVersion, previewErr := semver.NewVersion(preview.TagName)
		latestVersion, latestErr := semver.NewVersion(latest.TagName)
		if previewErr == nil && latestErr == nil && latestVersion.GreaterThan(previewVersion) {
			return latest
		}
	}
	return preview
}

// expectedUpdateAssetName is the release-asset naming convention adopted with
// the first self-updatable release. Assets published under older names never
// match, so they can be offered for manual download but never auto-installed.
func expectedUpdateAssetName(version, goos, goarch string) string {
	trimmed := strings.TrimPrefix(version, "v")
	switch {
	case goos == "darwin" && goarch == "arm64":
		return "MagpieDB-" + trimmed + "-macOS-arm64.dmg"
	case goos == "windows" && goarch == "amd64":
		return "MagpieDB-" + trimmed + "-Windows-x64.zip"
	case goos == "linux" && goarch == "amd64":
		return "MagpieDB-" + trimmed + "-Linux-x64.tar.gz"
	}
	return ""
}

func selectUpdateAsset(release *githubRelease, goos, goarch string) *githubAssetEntry {
	if release == nil {
		return nil
	}
	name := expectedUpdateAssetName(release.TagName, goos, goarch)
	if name == "" {
		return nil
	}
	for index := range release.Assets {
		if release.Assets[index].Name == name {
			return &release.Assets[index]
		}
	}
	return nil
}

// updateInstallMode decides whether this process may download and install an
// update itself. Web mode is always manual-only. A desktop build whose
// install location is not writable (system-owned /Applications entry, a DMG
// mount, an App Translocation path) reports portable_mode with manual-only,
// matching the legacy frontend semantics.
func updateInstallMode(desktop bool, executablePath string, goos string) (portableMode, manualOnly bool) {
	if !desktop {
		return false, true
	}
	if !updateInstallWritable(executablePath, goos) {
		return true, true
	}
	return false, false
}

func updateInstallWritable(executablePath, goos string) bool {
	if executablePath == "" {
		return false
	}
	target := executablePath
	if goos == "darwin" {
		if strings.Contains(executablePath, "AppTranslocation") || strings.HasPrefix(executablePath, "/Volumes/") {
			return false
		}
		// The writable unit on macOS is the .app bundle's parent directory.
		dir := filepath.Dir(executablePath)
		for {
			if strings.HasSuffix(dir, ".app") {
				target = filepath.Dir(dir)
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				target = filepath.Dir(executablePath)
				break
			}
			dir = parent
		}
	} else {
		target = filepath.Dir(executablePath)
	}
	return directoryWritable(target)
}

func directoryWritable(dir string) bool {
	if dir == "" {
		return false
	}
	probe, err := os.CreateTemp(dir, ".magpiedb-update-probe-*")
	if err != nil {
		return false
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return true
}

// buildUpdateInfo assembles the frontend UpdateInfo contract for a candidate
// release. A candidate whose version is not strictly newer than the running
// build reports update_available=false. When the platform asset is missing
// from the release, the update stays visible but is downgraded to
// manual-only: the updater never guesses a substitute asset.
func buildUpdateInfo(current string, candidate *githubRelease, desktop bool, executablePath, goos, goarch string) map[string]any {
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
	info["latest_version"] = strings.TrimPrefix(candidate.TagName, "v")
	info["release_name"] = candidate.Name
	if info["release_name"] == "" {
		info["release_name"] = candidate.TagName
	}
	info["release_url"] = candidate.HTMLURL
	info["release_notes"] = candidate.Body
	latest, err := semver.NewVersion(candidate.TagName)
	if err != nil {
		return info
	}
	running, err := semver.NewVersion(current)
	if err != nil || !latest.GreaterThan(running) {
		return info
	}
	info["update_available"] = true
	if selectUpdateAsset(candidate, goos, goarch) == nil {
		info["manual_update_only"] = true
	}
	return info
}

func (s *Server) handleUpdateCheck(r *http.Request) (map[string]any, error) {
	channel := normalizeUpdateChannel(r.URL.Query().Get("channel"))
	latest, err := s.latestRelease(r.Context())
	if err != nil {
		return nil, err
	}
	var candidate *githubRelease
	if channel == "preview" {
		list, err := s.releaseList(r.Context())
		if err != nil {
			return nil, err
		}
		candidate = selectChannelCandidate(channel, latest, list)
	} else {
		candidate = selectChannelCandidate(channel, latest, nil)
	}
	executable, _ := os.Executable()
	return buildUpdateInfo(version.Version, candidate, s.desktop, executable, runtime.GOOS, runtime.GOARCH), nil
}

// handleChangelog maps the release list onto the frontend ChangelogData
// shape ({updatedAt, releases: [{tag, name, date, sections}]}). Only the
// history tab consumes it, and it reads tag/date; the release body is carried
// as a single notes section so nothing is silently dropped.
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
