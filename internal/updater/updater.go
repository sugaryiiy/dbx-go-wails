package updater

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// DownloadedUpdate is the frontend contract for a cached update package
// (lib/backend/backendTypes.ts). DownloadedAt is a Unix-millisecond stamp.
type DownloadedUpdate struct {
	CacheID      string `json:"cache_id"`
	Version      string `json:"version"`
	PortableMode bool   `json:"portable_mode"`
	ReleaseURL   string `json:"release_url"`
	ReleaseNotes string `json:"release_notes"`
	DownloadedAt int64  `json:"downloaded_at"`
}

// manifest is the on-disk record of one downloaded package
// (<root>/<cacheID>/update.json). It lets a later process restore the
// downloaded state and gives the installer everything it needs offline:
// asset file, expected checksum and version.
type manifest struct {
	CacheID      string `json:"cache_id"`
	Version      string `json:"version"`
	AssetName    string `json:"asset_name"`
	SHA256       string `json:"sha256"`
	ReleaseURL   string `json:"release_url"`
	ReleaseNotes string `json:"release_notes"`
	DownloadedAt int64  `json:"downloaded_at"`
	PortableMode bool   `json:"portable_mode"`
}

func (m *manifest) downloaded() *DownloadedUpdate {
	return &DownloadedUpdate{
		CacheID:      m.CacheID,
		Version:      m.Version,
		PortableMode: m.PortableMode,
		ReleaseURL:   m.ReleaseURL,
		ReleaseNotes: m.ReleaseNotes,
		DownloadedAt: m.DownloadedAt,
	}
}

var cacheIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

func validCacheID(id string) bool { return cacheIDPattern.MatchString(id) }

// CacheRoot is the update cache directory for an application data dir.
func CacheRoot(dataDir string) string {
	return filepath.Join(dataDir, "updates")
}

// Service owns the update cache under <dataDir>/updates. It is safe for
// concurrent use; a new Download supersedes (cancels) any in-flight one.
type Service struct {
	root   string
	client *GitHubClient
	goos   string
	goarch string
	// PortableMode is computed by the caller (it depends on the install
	// location of the running build) and stamped onto new manifests.
	PortableMode bool

	httpClient *http.Client

	mu       sync.Mutex
	cancel   context.CancelFunc
	attempt  string
	manifest *manifest
}

func NewService(root string, client *GitHubClient) *Service {
	if client == nil {
		client = NewGitHubClient("", "")
	}
	return &Service{root: root, client: client, goos: runtime.GOOS, goarch: runtime.GOARCH}
}

// Root returns the update cache directory.
func (s *Service) Root() string { return s.root }

func (s *Service) cacheDir(cacheID string) string {
	return filepath.Join(s.root, cacheID)
}

func (s *Service) manifestPath(cacheID string) string {
	return filepath.Join(s.cacheDir(cacheID), "update.json")
}

// Download resolves the release for version, selects this platform's asset,
// obtains its expected SHA-256 (the GitHub asset digest, or the .sha256
// sidecar as a fallback; neither means the download is refused), streams
// the package into the cache and records the manifest.
func (s *Service) Download(ctx context.Context, version, attemptID string, releaseNotes string, progress ProgressFunc) (*DownloadedUpdate, error) {
	if !validCacheID(attemptID) {
		return nil, errors.New("update download attempt id is invalid")
	}
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.cancel = cancel
	s.attempt = attemptID
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		if s.attempt == attemptID {
			s.cancel = nil
			s.attempt = ""
		}
		s.mu.Unlock()
	}()

	release, err := s.client.FindRelease(ctx, version)
	if err != nil {
		return nil, err
	}
	asset := SelectAsset(release, s.goos, s.goarch)
	if asset == nil {
		return nil, fmt.Errorf("release %s has no downloadable asset for %s/%s; use the release page instead", release.TagName, s.goos, s.goarch)
	}
	expectedSHA, err := s.expectedChecksum(ctx, release, asset)
	if err != nil {
		return nil, err
	}
	dir := s.cacheDir(attemptID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	dest := filepath.Join(dir, asset.Name)
	report := func(downloaded int64) {
		if progress != nil {
			progress(DownloadProgress{AttemptID: attemptID, Version: release.Version(), Downloaded: downloaded, Total: asset.Size})
		}
	}
	if _, err := streamDownload(ctx, s.downloadClient(), asset.DownloadURL, dest, expectedSHA, report); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	notes := releaseNotes
	if notes == "" {
		notes = release.Body
	}
	entry := &manifest{
		CacheID:      attemptID,
		Version:      release.Version(),
		AssetName:    asset.Name,
		SHA256:       expectedSHA,
		ReleaseURL:   release.HTMLURL,
		ReleaseNotes: notes,
		DownloadedAt: time.Now().UnixMilli(),
		PortableMode: s.PortableMode,
	}
	if err := s.writeManifest(entry); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	s.mu.Lock()
	s.manifest = entry
	s.mu.Unlock()
	return entry.downloaded(), nil
}

func (s *Service) downloadClient() *http.Client {
	if s.httpClient != nil {
		return s.httpClient
	}
	return s.client.httpClient()
}

// expectedChecksum resolves the checksum a download must match: the asset
// digest when GitHub provides one, else the "<asset>.sha256" sidecar.
// Neither available is a hard failure — updates are never installed
// unverified.
func (s *Service) expectedChecksum(ctx context.Context, release *Release, asset *Asset) (string, error) {
	if sum, ok := asset.SHA256(); ok {
		return sum, nil
	}
	sidecar := SidecarAsset(release, asset.Name)
	if sidecar == nil {
		return "", fmt.Errorf("release asset %s has no checksum (no digest and no .sha256 sidecar); refusing to download it automatically", asset.Name)
	}
	text, err := s.client.FetchText(ctx, sidecar.DownloadURL)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", errors.New("update checksum sidecar is empty")
	}
	sum := strings.ToLower(fields[0])
	if _, err := hex.DecodeString(sum); err != nil || len(sum) != 64 {
		return "", errors.New("update checksum sidecar does not contain a SHA-256 value")
	}
	return sum, nil
}

// Cancel aborts the in-flight download, if any. The partial cache entry is
// removed by the download itself when it observes the cancellation.
func (s *Service) Cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

// Downloaded returns the newest complete cached package, or (nil, nil).
// Entries whose package file disappeared or whose manifest is unreadable
// are skipped (and can be re-downloaded).
func (s *Service) Downloaded() (*DownloadedUpdate, error) {
	entry, err := s.loadNewestManifest()
	if err != nil || entry == nil {
		return nil, err
	}
	return entry.downloaded(), nil
}

func (s *Service) loadNewestManifest() (*manifest, error) {
	s.mu.Lock()
	cached := s.manifest
	s.mu.Unlock()
	if cached != nil && s.manifestIntact(cached) {
		return cached, nil
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var newest *manifest
	for _, entry := range entries {
		if !entry.IsDir() || !validCacheID(entry.Name()) {
			continue
		}
		loaded, err := s.readManifest(entry.Name())
		if err != nil || loaded == nil {
			continue
		}
		if !s.manifestIntact(loaded) {
			continue
		}
		if newest == nil || loaded.DownloadedAt > newest.DownloadedAt {
			newest = loaded
		}
	}
	s.mu.Lock()
	s.manifest = newest
	s.mu.Unlock()
	return newest, nil
}

func (s *Service) manifestIntact(entry *manifest) bool {
	info, err := os.Stat(filepath.Join(s.cacheDir(entry.CacheID), entry.AssetName))
	return err == nil && !info.IsDir() && info.Size() > 0
}

func (s *Service) readManifest(cacheID string) (*manifest, error) {
	data, err := os.ReadFile(s.manifestPath(cacheID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entry manifest
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, nil
	}
	if entry.CacheID != cacheID || entry.Version == "" || entry.AssetName == "" {
		return nil, nil
	}
	return &entry, nil
}

func (s *Service) writeManifest(entry *manifest) error {
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.manifestPath(entry.CacheID), data, 0o600)
}

// Discard removes one cached package (superseded or ignored updates).
func (s *Service) Discard(cacheID string) error {
	if !validCacheID(cacheID) {
		return errors.New("update cache id is invalid")
	}
	s.mu.Lock()
	if s.manifest != nil && s.manifest.CacheID == cacheID {
		s.manifest = nil
	}
	s.mu.Unlock()
	if err := os.RemoveAll(s.cacheDir(cacheID)); err != nil {
		return err
	}
	return nil
}

// loadManifest is the installer's entry point: it returns the manifest for
// a cache id after verifying the package file still matches its checksum.
func (s *Service) loadManifest(cacheID string) (*manifest, error) {
	entry, err := s.readManifest(cacheID)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, fmt.Errorf("update cache %s no longer exists; download the update again", cacheID)
	}
	return entry, nil
}
