package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestMarketplaceCatalogAndSignedInstall(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	value(t, call(s, "POST", "/api/plugins/trusted-keys/save", map[string]any{"keyId": "market-key", "publicKey": base64.StdEncoding.EncodeToString(public)}, cookie, ""))
	manifest := pluginTestManifest("market-plugin", "1.0.0", []any{"host.storage", "host.events"})
	archive := pluginTestArchive(t, manifest, map[string][]byte{"ui/index.html": []byte("<h1>Marketplace install</h1>")}, private, "market-key")
	hash := sha256.Sum256(archive)
	artifact := map[string]any{"target": "universal", "url": "../packages/plugin.dbxp", "sha256": hex.EncodeToString(hash[:]), "signingKeyId": "market-key", "size": len(archive)}
	catalog := map[string]any{"catalogVersion": 1, "repository": map[string]any{"id": "test-market", "name": "Test marketplace"}, "plugins": []map[string]any{{"id": "market-plugin", "name": "Market plugin", "publisher": "Test publisher", "permissions": []string{"host.events", "host.storage"}, "latestVersion": "1.0.0", "versions": []map[string]any{{"version": "1.0.0", "artifacts": []map[string]any{artifact}}}}}}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog/index.json":
			_ = json.NewEncoder(w).Encode(catalog)
		case "/packages/plugin.dbxp":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer remote.Close()
	repository := map[string]any{"id": "test-market", "name": "Test marketplace", "kind": "custom", "catalogUrl": remote.URL + "/catalog/index.json", "enabled": true}
	value(t, call(s, "POST", "/api/plugins/repositories/save", repository, cookie, ""))
	fetched, err := s.fetchPluginCatalog(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	url := str(appItems(appItems(appItems(fetched["plugins"])[0]["versions"])[0]["artifacts"])[0], "url")
	if url != remote.URL+"/packages/plugin.dbxp" {
		t.Fatal("Relative artifact URL was not resolved", url)
	}
	installed := value(t, call(s, "POST", "/api/plugins/marketplace/install", map[string]any{"repositoryId": "test-market", "pluginId": "market-plugin"}, cookie, "")).(map[string]any)
	if str(appObject(installed["signature"]), "status") != "trusted" || str(installed, "packageSha256") != hex.EncodeToString(hash[:]) {
		t.Fatal(installed)
	}
	record, err := s.pluginRecord("market-plugin")
	if err != nil || str(appObject(record["provenance"]), "repositoryId") != "test-market" {
		t.Fatal("Marketplace provenance was not preserved", record, err)
	}
	asset := call(s, "GET", "/api/plugins/market-plugin/ui", nil, cookie, "")
	if asset.Code != 200 || !strings.Contains(asset.Body.String(), "Marketplace install") {
		t.Fatal(asset.Code, asset.Body)
	}
	artifact["sha256"] = strings.Repeat("0", 64)
	failed := call(s, "POST", "/api/plugins/marketplace/install", map[string]any{"repositoryId": "test-market", "pluginId": "market-plugin"}, cookie, "")
	if failed.Code != 400 || !strings.Contains(failed.Body.String(), "SHA-256") {
		t.Fatal("Tampered artifact hash was accepted", failed.Code, failed.Body)
	}
	record, err = s.pluginRecord("market-plugin")
	if err != nil || str(appObject(record["manifest"]), "version") != "1.0.0" {
		t.Fatal("Failed download changed existing install", record, err)
	}
}

func TestMarketplaceRejectsManagedRepositoryAndUnsafeCatalog(t *testing.T) {
	s := testServer(t, t.TempDir())
	for _, repository := range []map[string]any{
		{"id": "dbx-official", "name": "Changed", "kind": "custom", "catalogUrl": "https://example.test/index.json"},
		{"id": "market", "name": "Market", "kind": "official", "catalogUrl": "https://example.test/index.json"},
		{"id": "market", "name": "Market", "kind": "custom", "catalogUrl": "file:///tmp/catalog.json"},
		{"id": "market", "name": "Market", "kind": "custom", "catalogUrl": "https://user:password@example.test/catalog.json"},
	} {
		if err := s.savePluginRepository(repository); err == nil {
			t.Fatal("Invalid repository accepted", repository)
		}
	}
	base, _ := url.Parse("https://example.test/catalog/index.json")
	repository := map[string]any{"id": "market"}
	if err := validateMarketplaceCatalog(map[string]any{"catalogVersion": 1, "repository": map[string]any{"id": "other", "name": "Other"}}, repository, base); err == nil {
		t.Fatal("Mismatched repository identity accepted")
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("123456789")) }))
	defer remote.Close()
	downloadURL, _ := url.Parse(remote.URL)
	if _, _, err := marketplaceDownload(context.Background(), downloadURL, 8); err == nil {
		t.Fatal("Unbounded marketplace content unexpectedly downloaded")
	}
}
