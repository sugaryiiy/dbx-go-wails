package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sugaryiiy/magpiedb/internal/updater"
)

func loadUpdateFixture(t *testing.T, name string, target any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
}

func fixtureLatest(t *testing.T) *updater.Release {
	t.Helper()
	var release updater.Release
	loadUpdateFixture(t, "update_release_latest.json", &release)
	return &release
}

func fixtureList(t *testing.T) []updater.Release {
	t.Helper()
	var releases []updater.Release
	loadUpdateFixture(t, "update_releases_list.json", &releases)
	return releases
}

func TestBuildUpdateInfo(t *testing.T) {
	list := fixtureList(t)
	newerPreview := &list[0]
	currentPreview := &list[1]
	// A writable fake install: a plain directory path on linux amd64.
	exe := filepath.Join(t.TempDir(), "magpiedb")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacyOnly := &updater.Release{TagName: "v0.6.34", Name: "Legacy assets", HTMLURL: "https://example.com/release", Assets: []updater.Asset{{Name: "magpiedb.exe", Digest: "sha256:4a3720a89b9756ee4643482ab71ae17a41e30eee967c2c13a6787cd689729659"}}}
	tests := []struct {
		name       string
		current    string
		candidate  *updater.Release
		desktop    bool
		executable string
		wantAvail  bool
		wantManual bool
		wantLatest string
	}{
		{name: "newer preview with matching asset", current: "0.6.33", candidate: newerPreview, desktop: true, executable: exe, wantAvail: true, wantManual: false, wantLatest: "0.6.36-preview.20261024"},
		{name: "same-version stable is not an update", current: "0.6.33", candidate: fixtureLatest(t), desktop: true, executable: exe, wantAvail: false, wantManual: false, wantLatest: "0.6.33"},
		{name: "prerelease of the running version is not an update", current: "0.6.33", candidate: currentPreview, desktop: true, executable: exe, wantAvail: false, wantManual: false, wantLatest: "0.6.33-preview.20261022"},
		{name: "missing platform asset downgrades to manual", current: "0.6.33", candidate: legacyOnly, desktop: true, executable: exe, wantAvail: true, wantManual: true, wantLatest: "0.6.34"},
		{name: "web mode is always manual", current: "0.6.33", candidate: newerPreview, desktop: false, executable: exe, wantAvail: true, wantManual: true, wantLatest: "0.6.36-preview.20261024"},
		{name: "no candidate", current: "0.6.33", candidate: nil, desktop: true, executable: exe, wantAvail: false, wantLatest: "0.6.33"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := buildUpdateInfo(test.current, test.candidate, test.desktop, test.executable, "linux", "amd64")
			if info["update_available"] != test.wantAvail {
				t.Fatalf("update_available = %v, want %v (info %v)", info["update_available"], test.wantAvail, info)
			}
			if info["manual_update_only"] != test.wantManual {
				t.Fatalf("manual_update_only = %v, want %v", info["manual_update_only"], test.wantManual)
			}
			if info["latest_version"] != test.wantLatest {
				t.Fatalf("latest_version = %v, want %q", info["latest_version"], test.wantLatest)
			}
			if info["current_version"] != test.current {
				t.Fatalf("current_version = %v, want %q", info["current_version"], test.current)
			}
		})
	}
}

func TestUpdateInstallWritable(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "magpiedb")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !updateInstallWritable(exe, "linux") {
		t.Fatal("writable temp install should be writable")
	}
	if updateInstallWritable(filepath.Join(t.TempDir(), "missing", "magpiedb"), "linux") {
		t.Fatal("missing install directory should not be writable")
	}
	if updateInstallWritable("/Volumes/MagpieDB/MagpieDB.app/Contents/MacOS/magpiedb", "darwin") {
		t.Fatal("DMG mount path must be treated as not writable")
	}
	if updateInstallWritable("/private/var/folders/xx/AppTranslocation/123/d/MagpieDB.app/Contents/MacOS/magpiedb", "darwin") {
		t.Fatal("translocated path must be treated as not writable")
	}
}

func newUpdateFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	latest, err := os.ReadFile(filepath.Join("testdata", "update_release_latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	list, err := os.ReadFile(filepath.Join("testdata", "update_releases_list.json"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/sugaryiiy/magpiedb/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "MagpieDB/") {
			t.Errorf("User-Agent = %q, want MagpieDB/ prefix", r.Header.Get("User-Agent"))
		}
		_, _ = w.Write(latest)
	})
	mux.HandleFunc("/repos/sugaryiiy/magpiedb/releases", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(list)
	})
	return httptest.NewServer(mux)
}

// The fixture's newest preview release must stay semver-ahead of
// internal/version.Version: a prerelease of the running version is not an
// update (IsNewer), so after a version bump the fixture tag needs a bump
// too — otherwise the preview check silently reports no update and this
// test fails.
func TestHandleUpdateCheckAgainstFixtureAPI(t *testing.T) {
	fixture := newUpdateFixtureServer(t)
	defer fixture.Close()
	s, err := New(Options{DataDir: t.TempDir(), Desktop: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.updateAPIBase = fixture.URL

	request := httptest.NewRequest(http.MethodGet, "/api/update/check?channel=preview", nil)
	info, err := s.handleUpdateCheck(request)
	if err != nil {
		t.Fatal(err)
	}
	if info["update_available"] != true || info["latest_version"] != "0.6.36-preview.20261024" {
		t.Fatalf("preview check = %v", info)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/update/check?channel=stable", nil)
	info, err = s.handleUpdateCheck(request)
	if err != nil {
		t.Fatal(err)
	}
	if info["update_available"] != false || info["latest_version"] != "0.6.33" {
		t.Fatalf("stable check = %v", info)
	}

	changelog, err := s.handleChangelog(httptest.NewRequest(http.MethodGet, "/api/changelog", nil))
	if err != nil {
		t.Fatal(err)
	}
	releases, ok := changelog["releases"].([]any)
	if !ok || len(releases) != 3 {
		t.Fatalf("changelog releases = %v", changelog["releases"])
	}
	first, ok := releases[0].(map[string]any)
	if !ok || first["tag"] != "v0.6.36-preview.20261024" || first["date"] != "2026-10-24T03:00:00Z" {
		t.Fatalf("first changelog release = %v", releases[0])
	}
}
