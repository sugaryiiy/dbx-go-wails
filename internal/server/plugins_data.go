package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sugaryiiy/magpiedb/internal/database"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

func (s *Server) pluginPermission(id, permission string) error {
	record, err := s.pluginRecord(id)
	if err != nil {
		return err
	}
	if !pluginHasPermission(appObject(record["manifest"]), permission) {
		return errors.New("Plugin does not declare " + permission)
	}
	return nil
}
func (s *Server) pluginGrantIDs(id string) []any {
	raw := appObject(s.snapshot().Settings["plugins.dataGrants"])[id]
	if ids, ok := raw.([]any); ok {
		return ids
	}
	return []any{}
}
func (s *Server) pluginGranted(id, connection string) bool {
	for _, value := range s.pluginGrantIDs(id) {
		if value == connection {
			return true
		}
	}
	return false
}
func (s *Server) pluginGrantList(id string) []map[string]any {
	out := []map[string]any{}
	for _, value := range s.pluginGrantIDs(id) {
		connection, ok := value.(string)
		if !ok {
			continue
		}
		item := map[string]any{"connectionId": connection}
		for _, config := range s.snapshot().Connections {
			if str(config, "id") == connection {
				item["connectionName"] = config["name"]
				break
			}
		}
		out = append(out, item)
	}
	return out
}
func (s *Server) pluginConnection(id string) (map[string]any, error) {
	for _, config := range s.snapshot().Connections {
		if str(config, "id") == id {
			return config, nil
		}
	}
	return nil, errors.New("Connection does not exist")
}
func validatePluginContext(p map[string]any) error {
	for _, key := range []string{"connectionId", "database", "schema", "table"} {
		if value, exists := p[key]; exists && value != nil {
			v, ok := value.(string)
			if !ok || utf8.RuneCountInString(v) > 256 || strings.ContainsRune(v, 0) {
				return errors.New("Invalid plugin context: " + key)
			}
		}
	}
	if str(p, "connectionId") == "" {
		return errors.New("Connection id is required")
	}
	return nil
}
func (s *Server) pluginDataQuery(ctx context.Context, id string, request map[string]any) (any, error) {
	if err := validatePluginContext(request); err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(str(request, "sql")) > 100000 {
		return nil, errors.New("Plugin data SQL exceeds 100000 characters")
	}
	runtime := s.pluginRuntime
	runtime.dataMu.Lock()
	if err := s.pluginPermission(id, "host.data:read"); err != nil {
		runtime.dataMu.Unlock()
		return nil, err
	}
	connection := str(request, "connectionId")
	if !s.pluginGranted(id, connection) {
		runtime.dataMu.Unlock()
		return nil, errors.New("PLUGIN_DATA_ACCESS_NOT_GRANTED")
	}
	config, err := s.pluginConnection(connection)
	if err != nil {
		runtime.dataMu.Unlock()
		return nil, err
	}
	kind := str(config, "db_type")
	parts, err := database.ValidateMCPSQL(str(request, "sql"), kind, true, false)
	if err != nil || len(parts) != 1 {
		runtime.dataMu.Unlock()
		return nil, errors.New("Plugin data queries require one supported read-only SQL statement")
	}
	maxRows := integer(request["maxRows"], 500)
	if maxRows < 1 || maxRows > 5000 {
		runtime.dataMu.Unlock()
		return nil, errors.New("Plugin data maxRows must be 1–5000")
	}
	timeout := integer(request["timeoutMs"], 60000)
	if timeout < 1 || timeout > 60000 {
		runtime.dataMu.Unlock()
		return nil, errors.New("Plugin data timeout must be 1–60000 ms")
	}
	if seconds := integer(config["query_timeout_secs"], 0); seconds > 0 && seconds*1000 < timeout {
		timeout = seconds * 1000
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	scope := id + "\x00" + connection
	runtime.mu.Lock()
	if runtime.closed {
		runtime.mu.Unlock()
		runtime.dataMu.Unlock()
		cancel()
		return nil, errors.New("Plugin runtime closed")
	}
	runtime.dataSeq++
	operation := runtime.dataSeq
	if runtime.dataOps[scope] == nil {
		runtime.dataOps[scope] = map[uint64]context.CancelFunc{}
	}
	runtime.dataOps[scope][operation] = cancel
	runtime.mu.Unlock()
	runtime.dataMu.Unlock()
	defer func() {
		cancel()
		runtime.mu.Lock()
		delete(runtime.dataOps[scope], operation)
		if len(runtime.dataOps[scope]) == 0 {
			delete(runtime.dataOps, scope)
		}
		runtime.mu.Unlock()
	}()
	started := time.Now()
	payload := map[string]any{"connectionId": connection, "database": request["database"], "schema": request["schema"], "sql": str(request, "sql"), "readOnly": true, "maxRows": maxRows, "maxResultBytes": 4 << 20, "timeoutSecs": (timeout + 999) / 1000}
	result, err := s.database.Handle(ctx, "/api/query/execute", payload)
	if err != nil {
		return nil, errors.New(s.redactError(err.Error(), config))
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !s.pluginGranted(id, connection) {
		return nil, errors.New("PLUGIN_DATA_ACCESS_NOT_GRANTED")
	}
	query, ok := result.(database.QueryResult)
	if !ok {
		return nil, errors.New("Driver returned an unsupported plugin data result")
	}
	columns := []map[string]any{}
	for index, name := range query.Columns {
		column := map[string]any{"name": name}
		if index < len(query.ColumnTypes) {
			column["dataType"] = query.ColumnTypes[index]
		}
		columns = append(columns, column)
	}
	return map[string]any{"dbType": kind, "columns": columns, "rows": query.Rows, "truncated": query.Truncated || query.HasMore, "elapsedMs": time.Since(started).Milliseconds()}, nil
}
func (s *Server) pluginTableMetadata(ctx context.Context, id string, p map[string]any) (any, error) {
	if id != "" {
		if err := s.pluginPermission(id, "host.schema:read"); err != nil {
			return nil, err
		}
	}
	if err := validatePluginContext(p); err != nil {
		return nil, err
	}
	if str(p, "table") == "" {
		return nil, errors.New("Table is required")
	}
	if _, err := s.pluginConnection(str(p, "connectionId")); err != nil {
		return nil, err
	}
	payload := cloneMap(p)
	payload["connection_id"] = str(p, "connectionId")
	raw, err := s.database.Handle(ctx, "/api/schema/columns", payload)
	if err != nil {
		return nil, errors.New(s.redactError(err.Error(), p))
	}
	columns := []map[string]any{}
	capabilities := map[string]any{"length": "unknown", "precision": "unknown", "scale": "unknown", "default": "supported"}
	for _, column := range appItems(raw) {
		out := map[string]any{"name": str(column, "name"), "dataType": str(column, "data_type"), "nullable": column["is_nullable"] == true || integer(column["is_nullable"], 0) == 1}
		for target, source := range map[string]string{"length": "character_maximum_length", "precision": "numeric_precision", "scale": "numeric_scale", "default": "column_default"} {
			if value, exists := column[source]; exists {
				out[target] = value
				if target != "default" {
					capabilities[target] = "supported"
				}
			}
		}
		columns = append(columns, out)
	}
	return map[string]any{"columns": columns, "fieldCapabilities": capabilities}, nil
}
func pluginEstimatedSupported(kind string) bool {
	switch kind {
	case "sqlite", "mysql", "mariadb", "postgres", "clickhouse":
		return true
	}
	return false
}
func (s *Server) pluginPlanCapabilities(p map[string]any) (any, error) {
	if err := validatePluginContext(p); err != nil {
		return nil, err
	}
	config, err := s.pluginConnection(str(p, "connectionId"))
	if err != nil {
		return nil, err
	}
	timeout := 60000
	if seconds := integer(config["query_timeout_secs"], 0); seconds > 0 && seconds*1000 < timeout {
		timeout = seconds * 1000
	}
	return map[string]any{"dbType": str(config, "db_type"), "supports": map[string]any{"estimatedPlan": pluginEstimatedSupported(str(config, "db_type"))}, "limits": map[string]any{"maxTimeoutMs": timeout, "maxPlanBytes": 4 << 20}}, nil
}
func (s *Server) pluginEstimatedPlan(ctx context.Context, p map[string]any) (any, error) {
	if err := validatePluginContext(p); err != nil {
		return nil, err
	}
	if str(p, "mode") != "estimated" {
		return nil, errors.New("Only estimated plugin plans are supported")
	}
	sql := str(p, "sql")
	if utf8.RuneCountInString(sql) > 200000 {
		return nil, errors.New("Plan SQL exceeds 200000 characters")
	}
	config, err := s.pluginConnection(str(p, "connectionId"))
	if err != nil {
		return nil, err
	}
	kind := str(config, "db_type")
	if !pluginEstimatedSupported(kind) {
		return nil, errors.New("Estimated plans are not supported for this engine")
	}
	parts, err := database.ValidateMCPSQL(sql, kind, true, false)
	if err != nil || len(parts) != 1 {
		return nil, errors.New("Estimated plan requires one supported read query")
	}
	built, handled, err := buildSQL("/api/query/build-explain-sql", map[string]any{"options": map[string]any{"databaseType": kind, "sql": sql, "format": "json"}})
	if err != nil || !handled || appObject(built)["ok"] != true {
		return nil, errors.New("Cannot generate a safe estimated plan for this query")
	}
	timeout := integer(p["timeoutMs"], 60000)
	if timeout < 1 || timeout > 60000 {
		return nil, errors.New("Plan timeout must be 1–60000 ms")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()
	payload := cloneMap(p)
	payload["sql"] = str(appObject(built), "sql")
	payload["readOnly"] = true
	payload["maxResultBytes"] = 4 << 20
	payload["maxRows"] = 10000
	result, err := s.database.Handle(ctx, "/api/query/execute", payload)
	if err != nil {
		return nil, errors.New(s.redactError(err.Error(), config))
	}
	query, ok := result.(database.QueryResult)
	if !ok {
		return nil, errors.New("Driver did not return a plan")
	}
	format := "text"
	var raw any
	warnings := []string{}
	if len(query.Rows) > 0 && len(query.Rows[0]) > 0 && (kind == "postgres" || kind == "mysql" || kind == "mariadb") {
		cell := query.Rows[0][0]
		text, ok := cell.(string)
		if ok && json.Unmarshal([]byte(text), &raw) == nil {
			format = "json"
		} else {
			raw = cell
			warnings = append(warnings, "plan_not_json")
		}
	} else {
		lines := []string{}
		for _, row := range query.Rows {
			fields := []string{}
			for _, cell := range row {
				fields = append(fields, fmt.Sprint(cell))
			}
			lines = append(lines, strings.Join(fields, "\t"))
		}
		raw = strings.Join(lines, "\n")
	}
	if query.Truncated || query.HasMore {
		warnings = append(warnings, "plan_rows_truncated")
	}
	return map[string]any{"dbType": kind, "format": format, "rawPlan": raw, "truncated": query.Truncated || query.HasMore, "warnings": warnings}, nil
}
func (s *Server) handlePluginData(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	route := r.URL.Path
	switch route {
	case "/api/plugin/data/grants", "/api/plugin/data/grant", "/api/plugin/data/query", "/api/plugin/table-metadata", "/api/query/plugin-plan-capabilities", "/api/query/plugin-estimated-plan":
	default:
		return false
	}
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return true
	}
	var result any
	var err error
	id := str(p, "pluginId")
	switch route {
	case "/api/plugin/data/grants":
		err = s.pluginPermission(id, "host.data:read")
		if err == nil {
			result = s.pluginGrantList(id)
		}
	case "/api/plugin/data/grant":
		runtime := s.pluginRuntime
		runtime.dataMu.Lock()
		err = s.pluginPermission(id, "host.data:read")
		connection := str(p, "connectionId")
		if err == nil {
			_, err = s.pluginConnection(connection)
		}
		granted, ok := p["granted"].(bool)
		if !ok {
			err = errors.New("granted must be a boolean")
		}
		if err == nil {
			err = s.update(func(st *diskState) error {
				all := appObject(st.Settings["plugins.dataGrants"])
				ids := []any{}
				if values, ok := all[id].([]any); ok {
					for _, value := range values {
						if value != connection {
							ids = append(ids, value)
						}
					}
				}
				if granted {
					ids = append(ids, connection)
				}
				all[id] = ids
				st.Settings["plugins.dataGrants"] = all
				return nil
			})
			if err == nil && !granted {
				runtime.mu.Lock()
				for _, cancel := range runtime.dataOps[id+"\x00"+connection] {
					cancel()
				}
				runtime.mu.Unlock()
			}
		}
		runtime.dataMu.Unlock()
		if err == nil {
			result = s.pluginGrantList(id)
		}
	case "/api/plugin/data/query":
		result, err = s.pluginDataQuery(r.Context(), id, appObject(p["request"]))
	case "/api/plugin/table-metadata":
		result, err = s.pluginTableMetadata(r.Context(), id, p)
	case "/api/query/plugin-plan-capabilities":
		result, err = s.pluginPlanCapabilities(p)
	case "/api/query/plugin-estimated-plan":
		result, err = s.pluginEstimatedPlan(r.Context(), p)
	}
	if err != nil {
		writeError(w, 400, s.redactError(err.Error(), p))
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
