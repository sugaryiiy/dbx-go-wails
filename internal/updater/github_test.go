package updater

import "testing"

func TestSelectCandidate(t *testing.T) {
	stable := &Release{TagName: "v0.6.33"}
	newerStable := &Release{TagName: "v0.7.0"}
	list := []Release{
		{TagName: "v0.6.34-preview.20261024", Prerelease: true},
		{TagName: "v0.6.33-preview.20261022", Prerelease: true},
		{TagName: "go-v0.1.0-preview.20260101", Prerelease: true},
	}
	tests := []struct {
		name    string
		channel string
		latest  *Release
		list    []Release
		wantTag string
	}{
		{name: "stable uses latest endpoint result", channel: "stable", latest: stable, list: list, wantTag: "v0.6.33"},
		{name: "stable without any stable release", channel: "stable", latest: nil, list: list, wantTag: ""},
		{name: "preview takes newest prerelease", channel: "preview", latest: nil, list: list, wantTag: "v0.6.34-preview.20261024"},
		{name: "preview prefers a newer stable over an older prerelease", channel: "preview", latest: newerStable, list: list, wantTag: "v0.7.0"},
		{name: "preview falls back to stable when no prerelease exists", channel: "preview", latest: stable, list: nil, wantTag: "v0.6.33"},
		{name: "unknown channel behaves like stable", channel: "nightly", latest: stable, list: list, wantTag: "v0.6.33"},
		{name: "draft latest never qualifies", channel: "stable", latest: &Release{TagName: "v9.9.9", Draft: true}, list: list, wantTag: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := SelectCandidate(test.channel, test.latest, test.list)
			gotTag := ""
			if got != nil {
				gotTag = got.TagName
			}
			if gotTag != test.wantTag {
				t.Fatalf("candidate = %q, want %q", gotTag, test.wantTag)
			}
		})
	}
}

func TestSelectAsset(t *testing.T) {
	release := &Release{TagName: "v0.6.34-preview.20261024", Assets: []Asset{
		{Name: "MagpieDB-0.6.34-preview.20261024-macOS-arm64.dmg", Digest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},
		{Name: "MagpieDB-0.6.34-preview.20261024-Windows-x64.zip", Digest: "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},
		{Name: "MagpieDB-0.6.34-preview.20261024-Linux-x64.tar.gz", Digest: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"},
	}}
	legacy := &Release{TagName: "v0.6.33-preview.20261022", Assets: []Asset{
		{Name: "magpiedb.exe", Digest: "sha256:4a3720a89b9756ee4643482ab71ae17a41e30eee967c2c13a6787cd689729659"},
	}}
	tests := []struct {
		name       string
		release    *Release
		goos       string
		goarch     string
		wantName   string
		wantDigest string
	}{
		{name: "darwin arm64 asset with digest", release: release, goos: "darwin", goarch: "arm64", wantName: "MagpieDB-0.6.34-preview.20261024-macOS-arm64.dmg", wantDigest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},
		{name: "windows amd64 asset", release: release, goos: "windows", goarch: "amd64", wantName: "MagpieDB-0.6.34-preview.20261024-Windows-x64.zip", wantDigest: "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},
		{name: "linux amd64 asset", release: release, goos: "linux", goarch: "amd64", wantName: "MagpieDB-0.6.34-preview.20261024-Linux-x64.tar.gz", wantDigest: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"},
		{name: "legacy bare exe name never matches", release: legacy, goos: "windows", goarch: "amd64", wantName: ""},
		{name: "unsupported platform", release: release, goos: "darwin", goarch: "amd64", wantName: ""},
		{name: "nil release", release: nil, goos: "linux", goarch: "amd64", wantName: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			asset := SelectAsset(test.release, test.goos, test.goarch)
			if test.wantName == "" {
				if asset != nil {
					t.Fatalf("asset = %q, want nil", asset.Name)
				}
				return
			}
			if asset == nil {
				t.Fatalf("asset = nil, want %q", test.wantName)
			}
			if asset.Name != test.wantName || asset.Digest != test.wantDigest {
				t.Fatalf("asset = {%q %q}, want {%q %q}", asset.Name, asset.Digest, test.wantName, test.wantDigest)
			}
			if sum, ok := asset.SHA256(); !ok || "sha256:"+sum != test.wantDigest {
				t.Fatalf("parsed digest = %q/%v, want %q", sum, ok, test.wantDigest)
			}
		})
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		candidate string
		current   string
		want      bool
	}{
		{candidate: "v0.6.34-preview.20261024", current: "0.6.33", want: true},
		{candidate: "v0.6.33", current: "0.6.33", want: false},
		{candidate: "v0.6.33-preview.20261022", current: "0.6.33", want: false},
		{candidate: "v0.6.32", current: "0.6.33", want: false},
		{candidate: "not-a-version", current: "0.6.33", want: false},
		{candidate: "v1.0.0", current: "garbage", want: false},
	}
	for _, test := range tests {
		if got := IsNewer(test.candidate, test.current); got != test.want {
			t.Fatalf("IsNewer(%q, %q) = %v, want %v", test.candidate, test.current, got, test.want)
		}
	}
}
