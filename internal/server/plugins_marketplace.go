package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	semver "github.com/Masterminds/semver/v3"
)

const officialPluginRepository = "dbx-official"

var marketplaceIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
var marketplaceTargetPattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
var marketplaceHashPattern = regexp.MustCompile(`(?i)^[0-9a-f]{64}$`)

func (s *Server) pluginRepositories() []map[string]any {
	// This distribution exposes only repositories configured by the user.
	result := []map[string]any{}
	custom := []map[string]any{}
	state := s.snapshot()
	for _, repository := range appList(&state, "plugins.repositories") {
		if str(repository, "id") != officialPluginRepository {
			custom = append(custom, repository)
		}
	}
	sort.Slice(custom, func(i, j int) bool { return str(custom[i], "id") < str(custom[j], "id") })
	return append(result, custom...)
}

func marketplaceURL(raw string, base *url.URL) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("Plugin marketplace URL is required")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || strings.ContainsAny(raw, "\x00\r\n") {
		return nil, errors.New("Invalid plugin marketplace URL")
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("Plugin marketplace URLs must use HTTP or HTTPS without credentials or fragments")
	}
	return u, nil
}

func (s *Server) savePluginRepository(p map[string]any) error {
	id, name, kind := str(p, "id"), strings.TrimSpace(str(p, "name")), str(p, "kind")
	if !marketplaceIDPattern.MatchString(id) || len(name) == 0 || len(name) > 128 {
		return errors.New("Plugin repository requires a valid id and a name of 1–128 bytes")
	}
	if id == officialPluginRepository || kind == "official" || boolValue(p, "managed") {
		return errors.New("Managed plugin repositories cannot be modified")
	}
	if kind != "custom" && kind != "enterprise" {
		return errors.New("Plugin repository kind must be custom or enterprise")
	}
	u, err := marketplaceURL(str(p, "catalogUrl"), nil)
	if err != nil {
		return err
	}
	enabled := true
	if _, present := p["enabled"]; present {
		enabled = boolValue(p, "enabled")
	}
	repository := map[string]any{"id": id, "name": name, "kind": kind, "catalogUrl": u.String(), "enabled": enabled, "managed": false}
	return s.update(func(st *diskState) error {
		items := removeID(appList(st, "plugins.repositories"), id)
		if len(items) >= 100 {
			return errors.New("At most 100 plugin repositories can be configured")
		}
		st.Settings["plugins.repositories"] = append(items, repository)
		return nil
	})
}

func marketplaceDownload(ctx context.Context, u *url.URL, limit int64) ([]byte, *url.URL, error) {
	client := &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(request *http.Request, previous []*http.Request) error {
		if len(previous) >= 5 {
			return errors.New("Plugin marketplace redirect limit exceeded")
		}
		if _, err := marketplaceURL(request.URL.String(), nil); err != nil {
			return err
		}
		if len(previous) > 0 && previous[0].URL.Scheme == "https" && request.URL.Scheme != "https" {
			return errors.New("Plugin marketplace redirects cannot downgrade HTTPS")
		}
		return nil
	}}
	request, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("User-Agent", "DBX-Go/"+pluginAppVersion)
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("Cannot download plugin marketplace content: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("Plugin marketplace download returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		return nil, nil, errors.New("Plugin marketplace download exceeds its size limit")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, nil, errors.New("Plugin marketplace download is incomplete or exceeds its size limit")
	}
	return data, response.Request.URL, nil
}

func marketplacePermissions(value any) ([]string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	permissions := []string{}
	if value != nil && json.Unmarshal(raw, &permissions) != nil {
		return nil, errors.New("Marketplace plugin permissions must be strings")
	}
	seen := map[string]bool{}
	networks := 0
	for _, permission := range permissions {
		if seen[permission] {
			return nil, errors.New("Marketplace plugin has duplicate permissions")
		}
		seen[permission] = true
		switch permission {
		case "host.events", "host.binary", "host.workbench", "host.filesystem", "host.plans:read", "host.schema:read", "host.storage", "host.ai", "host.clipboard:read", "host.data:read":
		default:
			if !strings.HasPrefix(permission, "host.network:") {
				return nil, errors.New("Unsupported marketplace plugin permission")
			}
			u, err := marketplaceURL(strings.TrimPrefix(permission, "host.network:"), nil)
			if err != nil || u.Scheme != "https" || u.Path != "" || u.RawQuery != "" {
				return nil, errors.New("Plugin network permission requires one HTTPS origin")
			}
			networks++
			if networks > 8 {
				return nil, errors.New("Marketplace plugin declares too many network origins")
			}
		}
	}
	return permissions, nil
}

func validateMarketplaceCatalog(catalog, repository map[string]any, base *url.URL) error {
	versionNumber, _ := marketplaceInt(catalog["catalogVersion"])
	if versionNumber != 1 {
		return errors.New("Unsupported plugin catalog version; this build supports version 1")
	}
	meta := appObject(catalog["repository"])
	if str(meta, "id") != str(repository, "id") || strings.TrimSpace(str(meta, "name")) == "" {
		return errors.New("Plugin catalog repository identity does not match its configuration")
	}
	seen := map[string]bool{}
	if err := marketplaceArray(catalog["plugins"]); err != nil {
		return err
	}
	plugins := appItems(catalog["plugins"])
	if len(plugins) > 10000 {
		return errors.New("Plugin catalog contains too many plugins")
	}
	for _, plugin := range plugins {
		id := str(plugin, "id")
		if !marketplaceIDPattern.MatchString(id) || seen[id] || strings.TrimSpace(str(plugin, "name")) == "" || strings.TrimSpace(str(plugin, "publisher")) == "" {
			return errors.New("Plugin catalog contains an invalid or duplicate plugin identity")
		}
		seen[id] = true
		permissions, err := marketplacePermissions(plugin["permissions"])
		if err != nil {
			return fmt.Errorf("Plugin %s: %w", id, err)
		}
		permissionValues := make([]any, len(permissions))
		for i, permission := range permissions {
			permissionValues[i] = permission
		}
		plugin["permissions"] = permissionValues
		_, err = semver.StrictNewVersion(str(plugin, "latestVersion"))
		if err != nil {
			return errors.New("Marketplace latestVersion must be SemVer")
		}
		versions := map[string]bool{}
		containsLatest := false
		if err := marketplaceArray(plugin["versions"]); err != nil {
			return err
		}
		for _, version := range appItems(plugin["versions"]) {
			_, err := semver.StrictNewVersion(str(version, "version"))
			if err != nil || versions[str(version, "version")] {
				return errors.New("Marketplace plugin has invalid or duplicate versions")
			}
			versions[str(version, "version")] = true
			containsLatest = containsLatest || str(version, "version") == str(plugin, "latestVersion")
			targets := map[string]bool{}
			if err := marketplaceArray(version["artifacts"]); err != nil {
				return err
			}
			for _, artifact := range appItems(version["artifacts"]) {
				target := str(artifact, "target")
				if !marketplaceTargetPattern.MatchString(target) || targets[target] || !marketplaceHashPattern.MatchString(str(artifact, "sha256")) || !pluginValidID(str(artifact, "signingKeyId")) {
					return errors.New("Marketplace artifact has an invalid target, hash or signing key")
				}
				targets[target] = true
				if size := artifact["size"]; size != nil {
					n, err := marketplaceInt(size)
					if err != nil || n < 0 || n > 32<<20 {
						return errors.New("Marketplace artifact size is outside the package limit")
					}
				}
				u, err := marketplaceURL(str(artifact, "url"), base)
				if err != nil {
					return err
				}
				artifact["url"] = u.String()
			}
		}
		if !containsLatest {
			return errors.New("Marketplace latestVersion is absent from versions")
		}
		if icon := str(plugin, "icon"); icon != "" {
			u, err := marketplaceURL(icon, base)
			if err != nil {
				return err
			}
			plugin["icon"] = u.String()
		}
	}
	return nil
}

func marketplaceArray(value any) error {
	if value == nil {
		return nil
	}
	if _, ok := value.([]map[string]any); ok {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		return errors.New("Marketplace plugins, versions and artifacts must be arrays")
	}
	for _, item := range items {
		if _, ok := item.(map[string]any); !ok {
			return errors.New("Marketplace arrays must contain objects")
		}
	}
	return nil
}

func marketplaceInt(value any) (int, error) {
	switch number := value.(type) {
	case json.Number:
		n, err := strconv.ParseInt(string(number), 10, 32)
		return int(n), err
	case int:
		return number, nil
	case float64:
		if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number < 0 || number > math.MaxInt32 {
			return 0, errors.New("Expected a bounded integer")
		}
		return int(number), nil
	}
	return 0, errors.New("Expected an integer")
}

func (s *Server) fetchPluginCatalog(ctx context.Context, repository map[string]any) (map[string]any, error) {
	u, err := marketplaceURL(str(repository, "catalogUrl"), nil)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	data, base, err := marketplaceDownload(ctx, u, 4<<20)
	if err != nil && str(repository, "id") == officialPluginRepository && ctx.Err() == nil {
		fallback, _ := marketplaceURL("https://raw.githubusercontent.com/t8y2/dbx-store/main/catalog/index.json", nil)
		data, base, err = marketplaceDownload(ctx, fallback, 4<<20)
	}
	if err != nil {
		return nil, err
	}
	var catalog map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err = decoder.Decode(&catalog); err != nil {
		return nil, errors.New("Cannot parse plugin marketplace catalog JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("Plugin catalog must contain one JSON document")
	}
	if err = validateMarketplaceCatalog(catalog, repository, base); err != nil {
		return nil, err
	}
	return catalog, nil
}

func (s *Server) pluginCatalogs(ctx context.Context) []map[string]any {
	repositories := s.pluginRepositories()
	result := make([]map[string]any, len(repositories))
	var group sync.WaitGroup
	concurrency := make(chan struct{}, 4)
	for i, repository := range repositories {
		if !boolValue(repository, "enabled") {
			continue
		}
		group.Add(1)
		go func(index int, repository map[string]any) {
			defer group.Done()
			select {
			case concurrency <- struct{}{}:
				defer func() { <-concurrency }()
			case <-ctx.Done():
				result[index] = map[string]any{"repository": repository, "target": pluginTarget(), "error": "Catalog request cancelled"}
				return
			}
			item := map[string]any{"repository": repository, "target": pluginTarget()}
			catalog, err := s.fetchPluginCatalog(ctx, repository)
			if err != nil {
				item["error"] = err.Error()
			} else {
				item["catalog"] = catalog
			}
			result[index] = item
		}(i, repository)
	}
	group.Wait()
	out := []map[string]any{}
	for _, item := range result {
		if item != nil {
			out = append(out, item)
		}
	}
	return out
}

func (s *Server) installMarketplacePlugin(ctx context.Context, p map[string]any) (any, error) {
	var repository map[string]any
	for _, item := range s.pluginRepositories() {
		if str(item, "id") == str(p, "repositoryId") {
			repository = item
			break
		}
	}
	if repository == nil || !boolValue(repository, "enabled") {
		return nil, errors.New("Plugin repository does not exist or is disabled")
	}
	catalog, err := s.fetchPluginCatalog(ctx, repository)
	if err != nil {
		return nil, err
	}
	var plugin, version, artifact map[string]any
	for _, item := range appItems(catalog["plugins"]) {
		if str(item, "id") == str(p, "pluginId") {
			plugin = item
			break
		}
	}
	if plugin == nil {
		return nil, errors.New("Plugin is not listed in this marketplace")
	}
	wanted := str(p, "version")
	if wanted == "" {
		wanted = str(plugin, "latestVersion")
	}
	for _, item := range appItems(plugin["versions"]) {
		if str(item, "version") == wanted {
			version = item
			break
		}
	}
	if version == nil {
		return nil, errors.New("Plugin version is not listed in this marketplace")
	}
	for _, item := range appItems(version["artifacts"]) {
		if str(item, "target") == pluginTarget() {
			artifact = item
			break
		}
		if str(item, "target") == "universal" {
			artifact = item
		}
	}
	if artifact == nil {
		return nil, errors.New("Plugin version has no artifact for this platform")
	}
	keyID := str(artifact, "signingKeyId")
	if str(repository, "kind") == "official" {
		officialKeys := map[string]string{"dbx-store-preview-2026": "VRb0VscZfWwuFa7LYfeD/wEOJeyNP8wPGND9br8Icmk=", "dbx-store-release-2026": "6WbMG2UDx+EZ/oauMtHjdinvSD5MuFWSgXbI0n7eL+k="}
		if officialKeys[keyID] == "" || s.pluginTrustKeys()[keyID] != officialKeys[keyID] {
			return nil, errors.New("Official marketplace artifacts require an unchanged official signing key")
		}
	}
	if _, exists := s.pluginTrustKeys()[keyID]; !exists {
		return nil, errors.New("Marketplace signing key is not trusted; add its Ed25519 public key first")
	}
	if old, err := s.pluginRecord(str(plugin, "id")); err == nil && !boolValue(p, "allowSourceChange") {
		provenance := appObject(old["provenance"])
		oldVersion, _ := semver.StrictNewVersion(str(appObject(old["manifest"]), "version"))
		newVersion, _ := semver.StrictNewVersion(wanted)
		if str(provenance, "publisher") != str(plugin, "publisher") || str(provenance, "signingKeyId") != keyID || str(provenance, "repositoryId") != str(repository, "id") || oldVersion != nil && newVersion.LessThan(oldVersion) {
			return nil, errors.New("Plugin update source changed or version is older; explicit allowSourceChange is required")
		}
	}
	u, _ := marketplaceURL(str(artifact, "url"), nil)
	data, _, err := marketplaceDownload(ctx, u, 32<<20)
	if err != nil {
		return nil, err
	}
	if size := artifact["size"]; size != nil {
		expectedSize, err := marketplaceInt(size)
		if err != nil || expectedSize != len(data) {
			return nil, errors.New("Marketplace package size differs from its catalog")
		}
	}
	digest := sha256.Sum256(data)
	if !strings.EqualFold(str(artifact, "sha256"), hex.EncodeToString(digest[:])) {
		return nil, errors.New("Marketplace package SHA-256 differs from its catalog")
	}
	expected := map[string]any{"pluginId": plugin["id"], "version": wanted, "publisher": plugin["publisher"], "signingKeyId": keyID, "permissions": plugin["permissions"], "repositoryId": repository["id"], "allowSourceChange": boolValue(p, "allowSourceChange")}
	return s.installPluginPackage(data, false, "marketplace", expected)
}
