package server

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/sugaryiiy/magpiedb/internal/version"
)

func (s *Server) handleApplication(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	route := strings.TrimSuffix(r.URL.Path, "/")
	method := r.Method
	var result any
	var err error
	switch {
	case route == "/api/health" && method == "GET", route == "/api/ready" && method == "GET":
		result = map[string]any{"status": "ok", "backend": "go", "desktop": s.desktop}
	case route == "/api/version" && method == "GET":
		result = map[string]any{"version": version.Version}
	case route == "/api/update/check" && method == "GET":
		result, err = s.handleUpdateCheck(r)
	case route == "/api/changelog" && method == "GET":
		result, err = s.handleChangelog(r)
	case route == "/api/migration/status" && method == "GET":
		result = map[string]any{"migrationId": "go-isolated-storage-v1", "state": "not_required", "needsMigration": false, "keyProviderAvailable": true, "keyStatus": "ready", "keyCreationAllowed": false, "databasePlaintextCount": 0, "connectionCount": len(s.snapshot().Connections), "pluginSecretCount": 0, "aiSecretCount": 0, "tunnelSecretCount": 0, "syncCredentialCount": 0, "legacyJsonFiles": []any{}, "backupRequired": false, "dataDir": s.dataDir, "keyFileConfigured": true, "keyFileReadable": true, "persistentKeyConfigured": true, "keySource": "managed_data_dir"}
	case route == "/api/connection/list" && method == "GET":
		result = s.snapshot().Connections
	case route == "/api/connection/save" && method == "POST":
		configs, ok := p["configs"].([]any)
		if !ok {
			err = errors.New("configs must be an array")
			break
		}
		err = s.update(func(state *diskState) error {
			if removed, ok := p["removedIds"].([]any); ok {
				for _, id := range removed {
					if text, ok := id.(string); ok {
						state.Connections = removeID(state.Connections, text)
					}
				}
			}
			for _, raw := range configs {
				config, ok := raw.(map[string]any)
				if !ok || str(config, "id") == "" || str(config, "db_type") == "" {
					return errors.New("Each connection requires id and db_type")
				}
				state.Connections = upsert(state.Connections, cleanConnection(config))
			}
			return nil
		})
		if err == nil {
			if removed, ok := p["removedIds"].([]any); ok {
				for _, id := range removed {
					_, _ = s.database.Handle(r.Context(), "/api/connection/disconnect", map[string]any{"connectionId": id})
				}
			}
		}
	case route == "/api/connection/session-credential-status" && method == "POST":
		// Transient passwords live only in an open connection. There is no
		// credential cache for reconnecting: save_password=false prompts again.
		result = false
	case route == "/api/connection/forget-session-credential" && method == "POST":
		err = errors.New("No reusable transient credential is cached; disconnect the connection to release its active credentials")
	case route == "/api/connection/database-info/save" && method == "POST":
		err = s.update(func(state *diskState) error {
			for _, c := range state.Connections {
				if str(c, "id") == str(p, "connectionId") {
					c["database_info"] = p["databaseInfo"]
					return nil
				}
			}
			return errors.New("Connection does not exist")
		})
	case route == "/api/layout/sidebar" && method == "GET":
		result = s.snapshot().Sidebar
	case route == "/api/layout/sidebar" && method == "POST":
		err = s.update(func(state *diskState) error { state.Sidebar = p["layout"]; return nil })
	case route == "/api/layout/table-vgroups" && method == "GET":
		result = s.snapshot().TableGroups
	case route == "/api/layout/table-vgroups" && method == "POST":
		if str(p, "scopeKey") == "" {
			err = errors.New("scopeKey is required")
			break
		}
		err = s.update(func(state *diskState) error { state.TableGroups[str(p, "scopeKey")] = p["layout"]; return nil })
	case strings.HasPrefix(route, "/api/layout/table-vgroups/connection/") && method == "DELETE":
		id := strings.TrimPrefix(route, "/api/layout/table-vgroups/connection/")
		err = s.update(func(state *diskState) error {
			for key := range state.TableGroups {
				if key == id || strings.HasPrefix(key, id+"\x00") || strings.HasPrefix(key, id+"::") || strings.HasPrefix(key, id+"/") {
					delete(state.TableGroups, key)
				}
			}
			return nil
		})
	case route == "/api/saved-sql/search" && method == "POST":
		result, err = s.searchSavedSQL(r.Context(), p)
	case route == "/api/saved-sql" && method == "GET":
		state := s.snapshot()
		for _, file := range state.SQLFiles {
			file["sql"] = ""
			file["sqlLoaded"] = false
		}
		result = map[string]any{"folders": state.SQLFolders, "files": state.SQLFiles}
	case (route == "/api/saved-sql" || route == "/api/saved-sql/folders") && method == "POST":
		if str(p, "id") == "" || str(p, "name") == "" || str(p, "connectionId") == "" {
			err = errors.New("Saved SQL requires id, name and connectionId")
			break
		}
		err = s.update(func(state *diskState) error {
			item := cloneMap(p)
			if route == "/api/saved-sql/folders" {
				parent := str(item, "parentFolderId")
				if parent == str(item, "id") {
					return errors.New("A folder cannot contain itself")
				}
				visited := map[string]bool{str(item, "id"): true}
				for parent != "" {
					if visited[parent] {
						return errors.New("SQL folder hierarchy cannot contain a cycle")
					}
					visited[parent] = true
					found := false
					for _, folder := range state.SQLFolders {
						if str(folder, "id") == parent {
							if str(folder, "connectionId") != str(item, "connectionId") {
								return errors.New("Parent folder belongs to another connection")
							}
							parent = str(folder, "parentFolderId")
							found = true
							break
						}
					}
					if !found {
						return errors.New("Parent SQL folder does not exist")
					}
				}
				state.SQLFolders = upsert(state.SQLFolders, item)
			} else {
				if folderID := str(item, "folderId"); folderID != "" {
					found := false
					for _, folder := range state.SQLFolders {
						if str(folder, "id") == folderID && str(folder, "connectionId") == str(item, "connectionId") {
							found = true
						}
					}
					if !found {
						return errors.New("SQL folder does not exist for this connection")
					}
				}
				if loaded, ok := item["sqlLoaded"].(bool); ok && !loaded {
					for _, existing := range state.SQLFiles {
						if str(existing, "id") == str(item, "id") {
							item["sql"] = existing["sql"]
							break
						}
					}
				}
				item["sqlLoaded"] = true
				state.SQLFiles = upsert(state.SQLFiles, item)
			}
			result = item
			return nil
		})
	case strings.HasPrefix(route, "/api/saved-sql/folders/") && method == "DELETE":
		id := strings.TrimPrefix(route, "/api/saved-sql/folders/")
		err = s.update(func(state *diskState) error {
			removed := map[string]bool{id: true}
			for changed := true; changed; {
				changed = false
				for _, folder := range state.SQLFolders {
					fid := str(folder, "id")
					if removed[str(folder, "parentFolderId")] && !removed[fid] {
						removed[fid] = true
						changed = true
					}
				}
			}
			for id := range removed {
				state.SQLFolders = removeID(state.SQLFolders, id)
			}
			kept := make([]map[string]any, 0, len(state.SQLFiles))
			for _, file := range state.SQLFiles {
				if !removed[str(file, "folderId")] {
					kept = append(kept, file)
				}
			}
			state.SQLFiles = kept
			return nil
		})
	case strings.HasPrefix(route, "/api/saved-sql/") && method == "GET":
		id := strings.TrimPrefix(route, "/api/saved-sql/")
		for _, file := range s.snapshot().SQLFiles {
			if str(file, "id") == id {
				result = file
				break
			}
		}
	case strings.HasPrefix(route, "/api/saved-sql/") && method == "DELETE":
		id := strings.TrimPrefix(route, "/api/saved-sql/")
		err = s.update(func(state *diskState) error { state.SQLFiles = removeID(state.SQLFiles, id); return nil })
	case route == "/api/workspace/sample" && method == "POST":
		result, err = s.sampleWorkspace()
	case strings.HasPrefix(route, "/api/history"):
		return s.handleHistory(w, r, p)
	case strings.HasPrefix(route, "/api/app-settings/"):
		return s.handleSetting(w, r, p)
	default:
		return false
	}
	if err != nil {
		writeError(w, 400, err.Error())
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
func (s *Server) handleSetting(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	key := strings.TrimPrefix(r.URL.Path, "/api/app-settings/")
	defaults := map[string]any{"locale": "zh-CN", "editor-settings": nil, "pinned-tree-node-ids": []any{}, "history-retention-limit": 1000, "mcp-history-retention-limit": 1000, "max-agent-turns": 20, "max-retries": 2, "sql-file-upload-max-bytes": maxTransferBytes}
	value, known := defaults[key]
	if !known {
		return false
	}
	if r.Method == "GET" {
		if saved, ok := s.snapshot().Settings[key]; ok {
			value = saved
		}
		writeJSON(w, 200, value)
		return true
	}
	if r.Method != "POST" && r.Method != "PUT" {
		writeError(w, 405, "Method not allowed")
		return true
	}
	field := map[string]string{"locale": "locale", "editor-settings": "settings", "pinned-tree-node-ids": "ids", "history-retention-limit": "limit", "mcp-history-retention-limit": "limit", "max-agent-turns": "maxAgentTurns", "max-retries": "maxRetries", "sql-file-upload-max-bytes": "sqlFileUploadMaxMb"}[key]
	value, exists := p[field]
	if !exists {
		writeError(w, 400, "Missing setting value")
		return true
	}
	if key == "sql-file-upload-max-bytes" {
		mb := integer(value, -1)
		if mb < 1 || mb > maxTransferBytes/(1<<20) {
			writeError(w, 400, "Upload limit must be between 1 and 32 MiB")
			return true
		}
		value = mb * (1 << 20)
	}
	if strings.Contains(key, "retention-limit") {
		number := integer(value, -1)
		if number != 0 && number != 200 && number != 1000 && number != 5000 && number != 10000 {
			writeError(w, 400, "Invalid history retention limit")
			return true
		}
	}
	err := s.update(func(state *diskState) error { state.Settings[key] = value; return nil })
	if err != nil {
		writeError(w, 500, err.Error())
	} else {
		writeJSON(w, 200, nil)
	}
	return true
}
func sortedHistory(entries []map[string]any) {
	sort.Slice(entries, func(i, j int) bool {
		a, b := str(entries[i], "executed_at"), str(entries[j], "executed_at")
		if a == b {
			return str(entries[i], "id") > str(entries[j], "id")
		}
		return a > b
	})
}
