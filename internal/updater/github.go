// Package updater implements MagpieDB's self-update flow against this
// distribution's own GitHub releases: release discovery, platform asset
// selection, verified downloads into a local cache, and (in install.go)
// the detached-helper package swap. The server package reuses the GitHub
// client and selection helpers for its /api/update/check endpoint so the
// two can never disagree about which asset an update refers to.
package updater

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	semver "github.com/Masterminds/semver/v3"
)

const RepoOwner = "sugaryiiy"
const RepoName = "magpiedb"

const DefaultAPIBase = "https://api.github.com"

const maxAPIBytes = 8 << 20
const maxSidecarBytes = 64 << 10

// Release mirrors the subset of the GitHub release object the update flow
// consumes. Digest is the asset checksum GitHub computes at upload time
// ("sha256:<hex>"); it is the primary integrity source for downloads.
type Release struct {
	TagName    string  `json:"tag_name"`
	Name       string  `json:"name"`
	HTMLURL    string  `json:"html_url"`
	Body       string  `json:"body"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Published  string  `json:"published_at"`
	Assets     []Asset `json:"assets"`
}

// Version returns the release version without the leading "v".
func (r *Release) Version() string {
	if r == nil {
		return ""
	}
	return strings.TrimPrefix(r.TagName, "v")
}

type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Digest      string `json:"digest"`
	Size        int64  `json:"size"`
}

// SHA256 parses the asset digest ("sha256:<hex>"). The second return value
// reports whether a usable checksum was present.
func (a *Asset) SHA256() (string, bool) {
	if a == nil {
		return "", false
	}
	digest := strings.TrimSpace(a.Digest)
	if !strings.HasPrefix(digest, "sha256:") {
		return "", false
	}
	value := strings.ToLower(strings.TrimPrefix(digest, "sha256:"))
	if _, err := hex.DecodeString(value); err != nil || len(value) != 64 {
		return "", false
	}
	return value, true
}

// GitHubClient reads release metadata. It is stateless; callers that need
// caching (the server's check endpoint) layer it on top.
type GitHubClient struct {
	APIBase string
	Version string // application version, used for the User-Agent
	HTTP    *http.Client
}

func NewGitHubClient(apiBase, appVersion string) *GitHubClient {
	base := strings.TrimSuffix(strings.TrimSpace(apiBase), "/")
	if base == "" {
		base = DefaultAPIBase
	}
	return &GitHubClient{APIBase: base, Version: appVersion}
}

func (c *GitHubClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(request *http.Request, previous []*http.Request) error {
		if len(previous) >= 5 {
			return errors.New("update request redirect limit exceeded")
		}
		if len(previous) > 0 && previous[0].URL.Scheme == "https" && request.URL.Scheme != "https" {
			return errors.New("update request redirects cannot downgrade HTTPS")
		}
		return nil
	}}
}

func (c *GitHubClient) get(ctx context.Context, path string, limit int64) ([]byte, bool, error) {
	endpoint := c.APIBase + path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	if c.Version != "" {
		request.Header.Set("User-Agent", "MagpieDB/"+c.Version)
	}
	response, err := c.httpClient().Do(request)
	if err != nil {
		return nil, false, fmt.Errorf("update request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, false, fmt.Errorf("update request returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return nil, false, errors.New("update response exceeds its size limit")
	}
	return data, true, nil
}

// Latest returns the newest non-prerelease, non-draft release, or (nil, nil)
// when the repository has no stable release yet.
func (c *GitHubClient) Latest(ctx context.Context) (*Release, error) {
	data, found, err := c.get(ctx, "/repos/"+RepoOwner+"/"+RepoName+"/releases/latest", maxAPIBytes)
	if err != nil || !found {
		return nil, err
	}
	var release Release
	if err := json.Unmarshal(data, &release); err != nil {
		return nil, fmt.Errorf("update response is not valid JSON: %w", err)
	}
	return &release, nil
}

// List returns the most recent releases (newest first), including
// prereleases and drafts; callers filter.
func (c *GitHubClient) List(ctx context.Context, perPage int) ([]Release, error) {
	if perPage <= 0 || perPage > 100 {
		perPage = 20
	}
	data, found, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/releases?per_page=%d", RepoOwner, RepoName, perPage), maxAPIBytes)
	if err != nil || !found {
		return nil, err
	}
	var releases []Release
	if err := json.Unmarshal(data, &releases); err != nil {
		return nil, fmt.Errorf("update response is not valid JSON: %w", err)
	}
	return releases, nil
}

// FetchText downloads a small text resource (a checksum sidecar) from an
// absolute URL with the same redirect hardening as API requests.
func (c *GitHubClient) FetchText(ctx context.Context, rawURL string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	if c.Version != "" {
		request.Header.Set("User-Agent", "MagpieDB/"+c.Version)
	}
	response, err := c.httpClient().Do(request)
	if err != nil {
		return "", fmt.Errorf("update sidecar request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("update sidecar request returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxSidecarBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxSidecarBytes {
		return "", errors.New("update sidecar exceeds its size limit")
	}
	return string(data), nil
}

// FindRelease locates the release for an exact version (with or without the
// leading "v") across the stable endpoint and the recent-release list.
func (c *GitHubClient) FindRelease(ctx context.Context, version string) (*Release, error) {
	want := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if want == "" {
		return nil, errors.New("update version is required")
	}
	if latest, err := c.Latest(ctx); err != nil {
		return nil, err
	} else if latest != nil && latest.Version() == want {
		return latest, nil
	}
	releases, err := c.List(ctx, 20)
	if err != nil {
		return nil, err
	}
	for index := range releases {
		if releases[index].Version() == want && !releases[index].Draft {
			return &releases[index], nil
		}
	}
	return nil, fmt.Errorf("release %s was not found", version)
}

// NormalizeChannel keeps the contract small: anything that is not
// explicitly "preview" resolves to the stable channel.
func NormalizeChannel(value string) string {
	if value == "preview" {
		return "preview"
	}
	return "stable"
}

// SelectCandidate picks the release a channel points at. Preview takes the
// newest prerelease, unless the newest stable release is even newer (a
// preview channel must never offer a downgrade). Drafts never qualify.
func SelectCandidate(channel string, latest *Release, list []Release) *Release {
	if NormalizeChannel(channel) != "preview" {
		if latest != nil && !latest.Draft {
			return latest
		}
		return nil
	}
	var preview *Release
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

// ExpectedAssetName is the release-asset naming convention adopted with the
// first self-updatable release. Assets published under older names never
// match, so they can be offered for manual download but never auto-installed.
func ExpectedAssetName(version, goos, goarch string) string {
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

// SelectAsset finds the platform asset for a release under the naming
// convention, or nil when the release carries no matching asset.
func SelectAsset(release *Release, goos, goarch string) *Asset {
	if release == nil {
		return nil
	}
	name := ExpectedAssetName(release.TagName, goos, goarch)
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

// SidecarAsset finds the "<asset>.sha256" companion asset of a release.
func SidecarAsset(release *Release, assetName string) *Asset {
	if release == nil {
		return nil
	}
	for index := range release.Assets {
		if release.Assets[index].Name == assetName+".sha256" {
			return &release.Assets[index]
		}
	}
	return nil
}

// IsNewer reports whether candidate is a strictly newer version than
// current, using SemVer rules (a prerelease of the running version is not
// an update). Unparseable versions never count as updates.
func IsNewer(candidate, current string) bool {
	latest, err := semver.NewVersion(candidate)
	if err != nil {
		return false
	}
	running, err := semver.NewVersion(current)
	if err != nil {
		return false
	}
	return latest.GreaterThan(running)
}
