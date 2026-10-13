package server

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

func pluginPayloadID(p map[string]any) string {
	if id := str(p, "pluginId"); id != "" {
		return id
	}
	return str(p, "plugin_id")
}
func (s *Server) handlePlugins(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	if s.handlePluginData(w, r, p) {
		return true
	}
	route := strings.TrimSuffix(r.URL.Path, "/")
	if !strings.HasPrefix(route, "/api/plugins") {
		return false
	}
	var result any
	var err error
	id := pluginPayloadID(p)
	if route == "/api/plugins/events" {
		s.servePluginEvents(w, r, p)
		return true
	}
	if r.Method == "GET" {
		switch route {
		case "/api/plugins":
			result = s.pluginList()
		case "/api/plugins/active":
			result = s.pluginActive()
		case "/api/plugins/trusted-keys":
			result = s.pluginTrustedKeyList()
		case "/api/plugins/repositories":
			result = s.pluginRepositories()
		case "/api/plugins/marketplace/catalogs":
			result = s.pluginCatalogs(r.Context())
		default:
			if strings.HasPrefix(route, "/api/plugins/") {
				s.servePluginAsset(w, r)
				return true
			}
			return false
		}
		writeJSON(w, 200, result)
		return true
	}
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return true
	}
	switch route {
	case "/api/plugins/trusted-keys/save":
		keyID, text := str(p, "keyId"), str(p, "publicKey")
		if keyID == "dbx-store-preview-2026" || keyID == "dbx-store-release-2026" {
			err = errors.New("Built-in plugin trust keys cannot be replaced")
			break
		}
		key, e := base64.StdEncoding.DecodeString(text)
		if !pluginValidID(keyID) || e != nil || len(key) != ed25519.PublicKeySize {
			err = errors.New("Trusted plugin key requires an id and a base64 Ed25519 public key")
			break
		}
		err = s.update(func(st *diskState) error {
			keys := appObject(st.Settings["plugins.trustedKeys"])
			keys[keyID] = text
			st.Settings["plugins.trustedKeys"] = keys
			return nil
		})
		result = s.pluginTrustedKeyList()
	case "/api/plugins/trusted-keys/remove":
		keyID := str(p, "keyId")
		err = s.update(func(st *diskState) error {
			keys := appObject(st.Settings["plugins.trustedKeys"])
			delete(keys, keyID)
			st.Settings["plugins.trustedKeys"] = keys
			return nil
		})
		result = s.pluginTrustedKeyList()
	case "/api/plugins/repositories/save":
		err = s.savePluginRepository(p)
		result = s.pluginRepositories()
	case "/api/plugins/repositories/remove":
		err = s.update(func(st *diskState) error {
			st.Settings["plugins.repositories"] = removeID(appList(st, "plugins.repositories"), str(p, "repositoryId"))
			return nil
		})
		result = s.pluginRepositories()
	case "/api/plugins/marketplace/install":
		result, err = s.installMarketplacePlugin(r.Context(), p)
	case "/api/plugins/install":
		var data []byte
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			if e := r.ParseMultipartForm(1 << 20); e != nil {
				err = errors.New("Invalid plugin upload")
				break
			}
			defer r.MultipartForm.RemoveAll()
			file, _, e := r.FormFile("file")
			if e != nil {
				err = errors.New("Plugin package file is required")
				break
			}
			data, e = io.ReadAll(io.LimitReader(file, 32<<20+1))
			file.Close()
			if e != nil {
				err = e
				break
			}
		} else if s.desktop && str(p, "path") != "" {
			if e := regularFile(str(p, "path")); e != nil {
				err = errors.New("Plugin package must be a regular file")
				break
			}
			file, e := os.Open(str(p, "path"))
			if e != nil {
				err = errors.New("Cannot read plugin package")
				break
			}
			data, e = io.ReadAll(io.LimitReader(file, 32<<20+1))
			file.Close()
			if e != nil {
				err = e
				break
			}
		} else {
			err = errors.New("Plugin install requires a package upload")
			break
		}
		source := "file"
		if str(p, "from_url") == "true" {
			source = "url"
		}
		result, err = s.installPluginPackage(data, str(p, "allow_unsigned") == "true" || p["allowUnsigned"] == true, source, nil)
	case "/api/plugins/uninstall":
		err = s.uninstallPlugin(id)
		result = s.pluginList()
	case "/api/plugins/rollback":
		result, err = s.rollbackPlugin(id)
	case "/api/plugins/activate":
		_, err = s.pluginRuntime.activate(r.Context(), id)
		result = s.pluginActive()
	case "/api/plugins/stop":
		s.pluginRuntime.opMu.Lock()
		s.pluginRuntime.mu.Lock()
		session := s.pluginRuntime.sessions[id]
		delete(s.pluginRuntime.sessions, id)
		s.pluginRuntime.mu.Unlock()
		if session != nil {
			session.close()
		} else {
			_, err = s.pluginRecord(id)
		}
		s.pluginRuntime.opMu.Unlock()
	case "/api/plugins/invoke", "/api/plugins/notify", "/api/plugins/binary":
		var session *pluginProcess
		session, err = s.pluginRuntime.activate(r.Context(), id)
		if err != nil {
			break
		}
		timeout := integer(p["timeoutMs"], 30000)
		if timeout < 1 || timeout > 300000 {
			err = errors.New("Plugin timeout must be 1–300000 ms")
			break
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeout)*time.Millisecond)
		defer cancel()
		if route == "/api/plugins/invoke" {
			result, err = session.invoke(ctx, str(p, "method"), p["params"])
		} else if route == "/api/plugins/notify" {
			if !pluginMethod(str(p, "method")) || strings.HasPrefix(str(p, "method"), "host/") {
				err = errors.New("Invalid or reserved plugin method")
			} else {
				err = session.write(ctx, map[string]any{"jsonrpc": "2.0", "method": p["method"], "params": p["params"]})
			}
		} else {
			if !pluginHasPermission(session.manifest, "host.binary") {
				err = errors.New("Plugin does not declare host.binary")
				break
			}
			channel := str(p, "channel")
			data, e := base64.StdEncoding.DecodeString(str(p, "dataBase64"))
			if e != nil || !pluginMethod(channel) || len(channel) > 65535 || len(data) > maxPluginBinary {
				err = errors.New("Invalid binary plugin payload")
				break
			}
			payload := make([]byte, 2+len(channel)+len(data))
			binary.BigEndian.PutUint16(payload[:2], uint16(len(channel)))
			copy(payload[2:], channel)
			copy(payload[2+len(channel):], data)
			err = session.writeFrame(ctx, 1, payload)
		}
	case "/api/plugins/connection-action":
		config := appObject(p["config"])
		id = str(config, "plugin_id")
		record, e := s.pluginRecord(id)
		if e != nil {
			err = e
			break
		}
		provider := str(config, "plugin_connection_provider")
		valid := false
		for _, c := range appItems(appObject(record["manifest"])["contributions"]) {
			if str(c, "type") == "connection-provider" && str(c, "id") == provider {
				for _, action := range appItems(c["actions"]) {
					if str(action, "id") == str(p, "actionId") {
						valid = true
					}
				}
			}
		}
		if !valid {
			err = errors.New("Plugin connection action is not declared")
			break
		}
		session, e := s.pluginRuntime.activate(r.Context(), id)
		if e != nil {
			err = e
			break
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		result, err = session.invoke(ctx, "connection/action", map[string]any{"providerId": provider, "actionId": p["actionId"], "connection": config})
		cancel()
	default:
		if strings.HasPrefix(route, "/api/plugins/filesystem/") {
			result, err = s.pluginFilesystem(r.Context(), strings.TrimPrefix(route, "/api/plugins/filesystem/"), p)
		} else {
			return false
		}
	}
	if err != nil {
		writeError(w, 400, s.redactError(err.Error(), p))
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
func (s *Server) pluginTrustedKeyList() []map[string]any {
	result := []map[string]any{}
	for key, text := range s.pluginTrustKeys() {
		result = append(result, map[string]any{"keyId": key, "publicKey": text})
	}
	return result
}
func (s *Server) pluginActive() []map[string]any {
	result := []map[string]any{}
	s.pluginRuntime.mu.Lock()
	defer s.pluginRuntime.mu.Unlock()
	for id, p := range s.pluginRuntime.sessions {
		state := "running"
		if !p.running() {
			state = "exited"
		}
		result = append(result, map[string]any{"pluginId": id, "processId": p.cmd.Process.Pid, "state": state})
	}
	return result
}
func (s *Server) uninstallPlugin(id string) error {
	if !pluginValidID(id) {
		return errors.New("Invalid plugin id")
	}
	runtime := s.pluginRuntime
	runtime.opMu.Lock()
	defer runtime.opMu.Unlock()
	if _, err := s.pluginRecord(id); err != nil {
		return err
	}
	runtime.mu.Lock()
	session := runtime.sessions[id]
	delete(runtime.sessions, id)
	runtime.mu.Unlock()
	if session != nil {
		session.close()
	}
	err := s.update(func(st *diskState) error {
		items := appObject(st.Settings["plugins.installed"])
		delete(items, id)
		st.Settings["plugins.installed"] = items
		grants := appObject(st.Settings["plugins.dataGrants"])
		delete(grants, id)
		st.Settings["plugins.dataGrants"] = grants
		return nil
	})
	if err == nil {
		os.RemoveAll(filepath.Join(s.dataDir, "plugins", id))
	}
	return err
}
func (s *Server) rollbackPlugin(id string) (any, error) {
	runtime := s.pluginRuntime
	runtime.opMu.Lock()
	defer runtime.opMu.Unlock()
	record, err := s.pluginRecord(id)
	if err != nil {
		return nil, err
	}
	history, ok := record["history"].([]any)
	if !ok || len(history) == 0 {
		return nil, errors.New("Plugin has no previous installation to restore")
	}
	previous := appObject(history[len(history)-1])
	if err = validatePluginManifest(appObject(previous["manifest"]), s.pluginDir(previous)); err != nil {
		return nil, err
	}
	nextHistory := append([]any{}, history[:len(history)-1]...)
	copy := cloneMap(record)
	delete(copy, "history")
	nextHistory = append(nextHistory, copy)
	previous["history"] = nextHistory
	runtime.mu.Lock()
	session := runtime.sessions[id]
	delete(runtime.sessions, id)
	runtime.mu.Unlock()
	if session != nil {
		session.close()
	}
	err = s.update(func(st *diskState) error {
		items := appObject(st.Settings["plugins.installed"])
		items[id] = previous
		st.Settings["plugins.installed"] = items
		return nil
	})
	return map[string]any{"plugin": pluginInfo(previous), "previousVersion": appObject(record["manifest"])["version"]}, err
}
func (s *Server) servePluginAsset(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/plugins/"), "/")
	if len(parts) < 2 {
		writeError(w, 404, "Plugin route does not exist")
		return
	}
	record, err := s.pluginRecord(parts[0])
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	root := s.pluginDir(record)
	uiRoot := ""
	relative := strings.Join(parts[2:], "/")
	if parts[1] == "ui" {
		ui := appObject(appObject(appObject(record["manifest"])["entrypoints"])["ui"])
		base := str(ui, "root")
		if base == "" {
			base = path.Dir(str(ui, "entry"))
		}
		uiRoot = canonicalSkillRoot(filepath.Join(root, filepath.FromSlash(base)))
		if uiRoot == "" {
			writeError(w, 404, "Plugin UI root is unavailable")
			return
		}
		if len(parts) == 2 {
			relative = str(ui, "entry")
		} else {
			if base != "." {
				relative = base + "/" + relative
			}
		}
	} else if parts[1] != "assets" {
		writeError(w, 404, "Plugin asset route does not exist")
		return
	}
	file, err := safePluginFile(root, relative)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if uiRoot != "" && !insideSkillRoot(uiRoot, file) {
		writeError(w, 400, "Plugin UI asset must stay inside its UI root")
		return
	}
	input, err := os.Open(file)
	if err != nil {
		writeError(w, 404, "Plugin asset is unavailable")
		return
	}
	defer input.Close()
	data, err := io.ReadAll(io.LimitReader(input, 32<<20+1))
	if err != nil || len(data) > 32<<20 {
		writeError(w, 400, "Plugin asset exceeds 32 MiB")
		return
	}
	kind := mime.TypeByExtension(filepath.Ext(file))
	if kind == "" {
		kind = "application/octet-stream"
	}
	digest := sha256.Sum256(data)
	w.Header().Set("Content-Type", kind)
	w.Header().Set("ETag", `"`+hex.EncodeToString(digest[:])+`"`)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(200)
	w.Write(data)
}
func (s *Server) servePluginEvents(w http.ResponseWriter, r *http.Request, p map[string]any) {
	if r.Method != "GET" {
		writeError(w, 405, "Method not allowed")
		return
	}
	after := uint64(integer(p["after_seq"], 0))
	poll := str(p, "poll") == "1"
	for {
		runtime := s.pluginRuntime
		runtime.mu.Lock()
		events := []map[string]any{}
		for _, event := range runtime.events {
			seq := uint64(integer(event["seq"], 0))
			if seq > after {
				events = append(events, cloneMap(event))
				after = seq
			}
		}
		wake := runtime.wake
		closed := runtime.closed
		runtime.mu.Unlock()
		if poll {
			writeJSON(w, 200, map[string]any{"events": events, "lastSeq": after})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			if aiSSE(w, event) != nil {
				return
			}
		}
		if closed {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-wake:
		case <-time.After(15 * time.Second):
			fmt.Fprint(w, ": keepalive\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}
func pluginURI(provider map[string]any, raw string) (string, error) {
	if raw == "" {
		raw = str(provider, "root_uri")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || strings.ContainsAny(raw, "\x00\r\n") {
		return "", errors.New("Invalid plugin filesystem URI")
	}
	allowed := false
	if schemes, ok := provider["schemes"].([]any); ok {
		for _, scheme := range schemes {
			if scheme == u.Scheme {
				allowed = true
			}
		}
	}
	if !allowed {
		return "", errors.New("Filesystem URI scheme is not declared by this provider")
	}
	return raw, nil
}
func (s *Server) pluginFilesystem(ctx context.Context, operation string, p map[string]any) (any, error) {
	id := pluginPayloadID(p)
	record, err := s.pluginRecord(id)
	if err != nil {
		return nil, err
	}
	manifest := appObject(record["manifest"])
	if !pluginHasPermission(manifest, "host.filesystem") {
		return nil, errors.New("Plugin does not declare host.filesystem")
	}
	var provider map[string]any
	for _, c := range appItems(manifest["contributions"]) {
		if str(c, "type") == "filesystem-provider" && str(c, "id") == str(p, "providerId") {
			provider = c
			break
		}
	}
	if provider == nil {
		return nil, errors.New("Filesystem provider is not declared")
	}
	capability := map[string]string{"read": "read", "write": "write", "delete": "delete", "rename": "rename", "create-directory": "mkdir"}[operation]
	if capability != "" {
		allowed := false
		if values, ok := provider["capabilities"].([]any); ok {
			for _, v := range values {
				if v == capability {
					allowed = true
				}
			}
		}
		if !allowed {
			return nil, errors.New("Filesystem operation is not declared by this provider")
		}
	}
	req := cloneMap(p)
	delete(req, "pluginId")
	delete(req, "plugin_id")
	if operation == "rename" {
		for _, key := range []string{"sourceUri", "targetUri"} {
			uri, err := pluginURI(provider, str(req, key))
			if err != nil {
				return nil, err
			}
			req[key] = uri
		}
	} else {
		uri, err := pluginURI(provider, str(req, "uri"))
		if err != nil {
			return nil, err
		}
		req["uri"] = uri
	}
	if operation == "list" {
		req["limit"] = bound(p["limit"], 200, 1000)
	}
	if operation == "read" {
		req["maxBytes"] = bound(p["maxBytes"], 256<<10, 4<<20)
	}
	if operation == "write" {
		data, err := base64.StdEncoding.DecodeString(str(p, "dataBase64"))
		if err != nil || len(data) > 4<<20 {
			return nil, errors.New("Filesystem inline write exceeds 4 MiB or is not base64")
		}
	}
	method := map[string]string{"list": "filesystem/list", "read": "filesystem/read", "write": "filesystem/write", "delete": "filesystem/delete", "rename": "filesystem/rename", "create-directory": "filesystem/createDirectory"}[operation]
	if method == "" {
		return nil, errors.New("Unknown filesystem operation")
	}
	session, err := s.pluginRuntime.activate(ctx, id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	value, err := session.invoke(ctx, method, req)
	if err != nil {
		return nil, err
	}
	result := appObject(value)
	if operation == "list" {
		entries := appItems(result["entries"])
		if len(entries) > integer(req["limit"], 200) {
			return nil, errors.New("Plugin filesystem response exceeds row limit")
		}
		for _, entry := range entries {
			if _, err := pluginURI(provider, str(entry, "uri")); err != nil {
				return nil, err
			}
		}
	} else if operation == "read" {
		data, err := base64.StdEncoding.DecodeString(str(result, "dataBase64"))
		if err != nil || len(data) > integer(req["maxBytes"], 4<<20) {
			return nil, errors.New("Plugin filesystem response exceeds byte limit")
		}
	} else if result["success"] == false {
		return nil, errors.New("Plugin filesystem mutation failed: " + str(result, "message"))
	}
	return value, nil
}
