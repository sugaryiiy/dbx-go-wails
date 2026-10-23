package server

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	semver "github.com/Masterminds/semver/v3"
	"github.com/sugaryiiy/magpiedb/internal/version"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// pluginAppVersion is the application version reported to plugins and used
// in marketplace User-Agent strings. The single source of truth lives in
// internal/version; do not hardcode another copy here.
var pluginAppVersion = version.Version

var pluginIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func pluginValidID(id string) bool { return pluginIDPattern.MatchString(id) }
func pluginTarget() string {
	osname, arch := runtime.GOOS, runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	return osname + "-" + arch
}
func safePluginRelative(value string) bool {
	if value == "" || strings.ContainsAny(value, "\\\x00:") || path.IsAbs(value) {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	return true
}
func safePluginFile(root, relative string) (string, error) {
	if !safePluginRelative(relative) {
		return "", errors.New("Plugin asset path must stay inside its package")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.New("Plugin package root is unavailable")
	}
	target, err := filepath.EvalSymlinks(filepath.Join(canonical, filepath.FromSlash(relative)))
	if err != nil || !insideSkillRoot(canonical, target) {
		return "", errors.New("Plugin asset does not exist or escapes its package")
	}
	if err = regularFile(target); err != nil {
		return "", errors.New("Plugin asset must be a regular file")
	}
	return target, nil
}
func validatePluginManifest(m map[string]any, root string) error {
	if integer(m["manifest_version"], 0) != 1 || !pluginValidID(str(m, "id")) || strings.TrimSpace(str(m, "name")) == "" {
		return errors.New("Plugin manifest requires manifest_version 1, id and name")
	}
	if _, err := semver.StrictNewVersion(str(m, "version")); err != nil {
		return errors.New("Plugin version must be SemVer")
	}
	engines := appObject(m["engines"])
	for key, version := range map[string]string{"dbx": pluginAppVersion, "host_api": "1.4.0"} {
		if requirement := str(engines, key); requirement != "" {
			constraint, err := semver.NewConstraint(requirement)
			if err != nil || !constraint.Check(semver.MustParse(version)) {
				return errors.New("Plugin engine requirement is incompatible: " + key)
			}
		}
	}
	permissions := map[string]bool{}
	networks := 0
	if raw, ok := m["permissions"].([]any); ok {
		for _, value := range raw {
			permission, ok := value.(string)
			if !ok || permissions[permission] {
				return errors.New("Invalid or duplicate plugin permission")
			}
			permissions[permission] = true
			switch permission {
			case "host.events", "host.binary", "host.workbench", "host.filesystem", "host.plans:read", "host.schema:read", "host.storage", "host.ai", "host.clipboard:read", "host.data:read":
			default:
				if !strings.HasPrefix(permission, "host.network:") {
					return errors.New("Unsupported plugin permission")
				}
				origin := strings.TrimPrefix(permission, "host.network:")
				u, err := url.Parse(origin)
				if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
					return errors.New("Plugin network permission must specify one HTTPS origin")
				}
				networks++
				if networks > 8 {
					return errors.New("Plugin declares too many network origins")
				}
			}
		}
	} else if m["permissions"] != nil {
		return errors.New("Plugin permissions must be an array")
	}
	entries := appObject(m["entrypoints"])
	backend := appObject(entries["backend"])
	ui := appObject(entries["ui"])
	if len(backend) == 0 && len(ui) == 0 {
		return errors.New("Plugin requires a backend or UI entrypoint")
	}
	if len(backend) > 0 {
		transport := str(backend, "transport")
		if transport != "" && transport != "stdio-jsonl" && transport != "stdio-framed" {
			return errors.New("Unsupported plugin backend transport")
		}
		if values, ok := backend["protocol_versions"].([]any); ok {
			supported := false
			for _, v := range values {
				if integer(v, 0) == 1 {
					supported = true
				}
			}
			if !supported {
				return errors.New("Plugin backend must support protocol version 1")
			}
		}
		if _, err := safePluginFile(root, str(backend, "executable")); err != nil {
			return err
		}
	}
	if len(ui) > 0 {
		entry, err := safePluginFile(root, str(ui, "entry"))
		if err != nil {
			return err
		}
		uiRoot := str(ui, "root")
		if uiRoot == "" {
			uiRoot = path.Dir(str(ui, "entry"))
		}
		base := root
		if uiRoot != "." {
			if !safePluginRelative(uiRoot) {
				return errors.New("Invalid plugin UI root")
			}
			base = filepath.Join(root, filepath.FromSlash(uiRoot))
		}
		canonical, err := filepath.EvalSymlinks(base)
		if err != nil || !insideSkillRoot(root, canonical) || !insideSkillRoot(canonical, entry) {
			return errors.New("Plugin UI entry must stay inside its UI root")
		}
	}
	ids := map[string]bool{}
	for _, contribution := range appItems(m["contributions"]) {
		id := str(contribution, "id")
		if id == "" || ids[id] {
			return errors.New("Plugin contribution id is missing or duplicated")
		}
		ids[id] = true
		switch str(contribution, "type") {
		case "connection-provider", "workbench", "filesystem-provider", "context-menu", "result-view", "command", "menus", "mcp":
		default:
			return errors.New("Unsupported plugin contribution type")
		}
	}
	return nil
}
func (s *Server) pluginRecord(id string) (map[string]any, error) {
	if !pluginValidID(id) {
		return nil, errors.New("Invalid plugin id")
	}
	record, ok := appObject(s.snapshot().Settings["plugins.installed"])[id].(map[string]any)
	if !ok {
		return nil, errors.New("Plugin is not installed")
	}
	return record, nil
}
func (s *Server) pluginDir(record map[string]any) string {
	return filepath.Join(s.dataDir, "plugins", str(record, "id"), "versions", str(record, "revision"))
}
func pluginInfo(record map[string]any) map[string]any {
	return map[string]any{"manifest": record["manifest"], "compatibility": map[string]any{"compatible": true, "target": pluginTarget()}, "provenance": record["provenance"]}
}
func (s *Server) pluginList() []map[string]any {
	out := []map[string]any{}
	for _, record := range appObject(s.snapshot().Settings["plugins.installed"]) {
		out = append(out, pluginInfo(appObject(record)))
	}
	sort.Slice(out, func(i, j int) bool {
		return str(appObject(out[i]["manifest"]), "id") < str(appObject(out[j]["manifest"]), "id")
	})
	return out
}
func (s *Server) pluginTrustKeys() map[string]any {
	keys := map[string]any{"dbx-store-preview-2026": "VRb0VscZfWwuFa7LYfeD/wEOJeyNP8wPGND9br8Icmk=", "dbx-store-release-2026": "6WbMG2UDx+EZ/oauMtHjdinvSD5MuFWSgXbI0n7eL+k="}
	for key, value := range appObject(s.snapshot().Settings["plugins.trustedKeys"]) {
		if key != "dbx-store-preview-2026" && key != "dbx-store-release-2026" {
			keys[key] = value
		}
	}
	return keys
}
func (s *Server) installPluginPackage(data []byte, allowUnsigned bool, source string, expected map[string]any) (any, error) {
	if len(data) > 32<<20 {
		return nil, errors.New("Plugin archive exceeds 32 MiB upload limit")
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("Plugin package must be a ZIP archive")
	}
	if len(reader.File) > 10000 {
		return nil, errors.New("Plugin archive has too many entries")
	}
	root := filepath.Join(s.dataDir, "plugins")
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(root, ".install-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	files := map[string]bool{}
	total := int64(0)
	for _, file := range reader.File {
		name := strings.TrimSuffix(file.Name, "/")
		if !safePluginRelative(name) || file.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("Plugin archive contains an unsafe path or symbolic link")
		}
		if file.FileInfo().IsDir() {
			continue
		}
		if !file.Mode().IsRegular() || files[name] {
			return nil, errors.New("Plugin archive contains an unsupported or duplicate file")
		}
		if file.UncompressedSize64 > 64<<20 {
			return nil, errors.New("Plugin file exceeds 64 MiB")
		}
		target := filepath.Join(staging, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return nil, err
		}
		input, err := file.Open()
		if err != nil {
			return nil, err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			input.Close()
			return nil, err
		}
		size, copyErr := io.Copy(output, io.LimitReader(input, 64<<20+1))
		input.Close()
		closeErr := output.Close()
		if copyErr != nil || closeErr != nil || size > 64<<20 {
			return nil, errors.New("Cannot extract plugin archive")
		}
		total += size
		if total > 256<<20 {
			return nil, errors.New("Expanded plugin archive exceeds 256 MiB")
		}
		if file.Mode()&0111 != 0 {
			os.Chmod(target, 0700)
		}
		files[name] = true
	}
	raw, err := os.ReadFile(filepath.Join(staging, "checksums.json"))
	if err != nil {
		return nil, errors.New("Plugin package is missing checksums.json")
	}
	var sums struct {
		Algorithm string            `json:"algorithm"`
		Files     map[string]string `json:"files"`
	}
	if json.Unmarshal(raw, &sums) != nil || sums.Algorithm != "sha256" {
		return nil, errors.New("Invalid plugin checksum format")
	}
	count := len(files)
	if files["checksums.json"] {
		count--
	}
	if files["signature.json"] {
		count--
	}
	if len(sums.Files) != count {
		return nil, errors.New("Plugin checksums must cover every package file")
	}
	for name, expected := range sums.Files {
		if !safePluginRelative(name) || !files[name] || name == "checksums.json" || name == "signature.json" {
			return nil, errors.New("Invalid plugin checksum path")
		}
		content, err := os.ReadFile(filepath.Join(staging, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(content)
		if !strings.EqualFold(expected, hex.EncodeToString(digest[:])) {
			return nil, errors.New("Plugin checksum mismatch: " + name)
		}
	}
	signature := map[string]any{"status": "unsigned"}
	signingKey := ""
	if signed, err := os.ReadFile(filepath.Join(staging, "signature.json")); err == nil {
		var sig struct{ Algorithm, KeyID, Signature string }
		var generic map[string]any
		if json.Unmarshal(signed, &generic) != nil {
			return nil, errors.New("Invalid plugin signature")
		}
		sig.Algorithm = str(generic, "algorithm")
		sig.KeyID = str(generic, "key_id")
		sig.Signature = str(generic, "signature")
		keyText, _ := s.pluginTrustKeys()[sig.KeyID].(string)
		key, e := base64.StdEncoding.DecodeString(keyText)
		value, e2 := base64.StdEncoding.DecodeString(sig.Signature)
		if sig.Algorithm != "ed25519" || e != nil || e2 != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, raw, value) {
			return nil, errors.New("Plugin signature is invalid or uses an untrusted key")
		}
		signingKey = sig.KeyID
		signature = map[string]any{"status": "trusted", "key_id": signingKey}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else if !allowUnsigned {
		return nil, errors.New("Plugin package requires a trusted Ed25519 signature")
	}
	manifestRaw, err := os.ReadFile(filepath.Join(staging, "manifest.json"))
	if err != nil {
		return nil, errors.New("Plugin manifest.json is missing")
	}
	var manifest map[string]any
	if json.Unmarshal(manifestRaw, &manifest) != nil {
		return nil, errors.New("Invalid plugin manifest")
	}
	if err = validatePluginManifest(manifest, staging); err != nil {
		return nil, err
	}
	if expected != nil {
		if str(expected, "pluginId") != str(manifest, "id") || str(expected, "version") != str(manifest, "version") || str(expected, "publisher") != str(manifest, "publisher") || str(expected, "signingKeyId") != signingKey {
			return nil, errors.New("Marketplace plugin identity differs from the catalog")
		}
		permissionSet := func(value any) string {
			values := []string{}
			if list, ok := value.([]any); ok {
				for _, entry := range list {
					if v, ok := entry.(string); ok {
						values = append(values, v)
					}
				}
			}
			sort.Strings(values)
			return strings.Join(values, "\x00")
		}
		if permissionSet(expected["permissions"]) != permissionSet(manifest["permissions"]) {
			return nil, errors.New("Marketplace package permissions differ from the catalog")
		}
	}
	id := str(manifest, "id")
	revision := fmt.Sprintf("%s-%d", str(manifest, "version"), time.Now().UnixNano())
	record := map[string]any{"id": id, "revision": revision, "manifest": manifest, "provenance": map[string]any{"publisher": str(manifest, "publisher"), "signingKeyId": signingKey, "source": source}, "signature": signature}
	if expected != nil {
		appObject(record["provenance"])["repositoryId"] = expected["repositoryId"]
	}
	var previous any
	history := []any{}
	runtime := s.pluginRuntime
	runtime.opMu.Lock()
	defer runtime.opMu.Unlock()
	if old, err := s.pluginRecord(id); err == nil {
		allowSourceChange := expected != nil && expected["allowSourceChange"] == true
		if !allowSourceChange && (str(appObject(old["provenance"]), "publisher") != str(manifest, "publisher") || str(appObject(old["provenance"]), "signingKeyId") != signingKey || str(appObject(old["provenance"]), "repositoryId") != str(appObject(record["provenance"]), "repositoryId")) {
			return nil, errors.New("Plugin update source changed; remove the old plugin before installing this source")
		}
		previous = appObject(old["manifest"])["version"]
		oldVersion, _ := semver.NewVersion(str(appObject(old["manifest"]), "version"))
		newVersion, _ := semver.NewVersion(str(manifest, "version"))
		if !allowSourceChange && oldVersion != nil && newVersion.LessThan(oldVersion) {
			return nil, errors.New("Plugin downgrade requires explicit rollback")
		}
		if values, ok := old["history"].([]any); ok {
			history = values
		}
		copy := cloneMap(old)
		delete(copy, "history")
		history = append(history, copy)
	}
	record["history"] = history
	destination := s.pluginDir(record)
	if err = os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return nil, err
	}
	if err = os.Rename(staging, destination); err != nil {
		return nil, errors.New("Cannot publish plugin package")
	}
	runtime.mu.Lock()
	session := runtime.sessions[id]
	delete(runtime.sessions, id)
	runtime.mu.Unlock()
	if session != nil {
		session.close()
	}
	err = s.update(func(st *diskState) error {
		installed := appObject(st.Settings["plugins.installed"])
		installed[id] = record
		st.Settings["plugins.installed"] = installed
		return nil
	})
	if err != nil {
		os.RemoveAll(destination)
		return nil, err
	}
	digest := sha256.Sum256(data)
	runtime.emit(map[string]any{"kind": "event", "pluginId": id, "method": "dbx/runtime/replaced", "params": map[string]any{"version": str(manifest, "version")}})
	return map[string]any{"plugin": pluginInfo(record), "previousVersion": previous, "packageSha256": hex.EncodeToString(digest[:]), "signature": signature}, nil
}
