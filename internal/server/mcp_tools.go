package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sugaryiiy/dbx-go-wails/internal/database"
	"github.com/sugaryiiy/dbx-go-wails/internal/mcpbridge"
)

type mcpBackend struct{ s *Server }

func (s *Server) MCPBackend() mcpbridge.Backend { return &mcpBackend{s: s} }

// Return implemented definitions even before configuration so a running MCP
// client can discover them. Calls always evaluate the latest saved policy.
func (b *mcpBackend) Tools() []mcpbridge.Tool { return mcpToolDefinitions() }
func mcpToolDefinitions() []mcpbridge.Tool {
	strSchema := func() any { return map[string]any{"type": "string"} }
	integerSchema := func(min, max int) any { return map[string]any{"type": "integer", "minimum": min, "maximum": max} }
	selector := func() map[string]any {
		return map[string]any{"connection_id": strSchema(), "connection_name": strSchema(), "database": strSchema(), "schema": strSchema()}
	}
	tool := func(name, desc string, props map[string]any, required []string, ro bool) mcpbridge.Tool {
		if required == nil {
			required = []string{}
		}
		return mcpbridge.Tool{Name: name, Description: desc, InputSchema: map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}, ReadOnly: ro}
	}
	list := []mcpbridge.Tool{tool("dbx_list_connections", "List allowed saved connections without credentials, hostnames or local file paths.", map[string]any{}, nil, true)}
	p := selector()
	delete(p, "schema")
	delete(p, "database")
	list = append(list, tool("dbx_list_databases", "List databases visible under the saved MCP policy.", p, nil, true))
	p = selector()
	p["all_schemas"] = map[string]any{"type": "boolean"}
	list = append(list, tool("dbx_list_tables", "List tables and views in the selected database/schema.", p, nil, true))
	for _, spec := range []struct{ name, desc string }{{"dbx_describe_table", "Get table columns, indexes, foreign keys and DDL."}, {"dbx_get_table_ddl", "Get table or view DDL."}, {"dbx_list_indexes", "List indexes on a table."}} {
		p = selector()
		p["table"] = strSchema()
		list = append(list, tool(spec.name, spec.desc, p, []string{"table"}, true))
	}
	p = selector()
	p["tables"] = map[string]any{"type": "array", "items": strSchema(), "maxItems": 20}
	p["max_tables"] = integerSchema(1, 20)
	list = append(list, tool("dbx_get_schema_context", "Get bounded table and column metadata for SQL authoring (default 8, max 20 tables).", p, nil, true))
	for _, name := range []string{"dbx_execute_query", "dbx_execute_batch"} {
		p = selector()
		p["sql"] = strSchema()
		p["max_rows"] = integerSchema(1, 1000)
		p["cell_char_offset"] = integerSchema(0, 1000000)
		p["cell_char_limit"] = integerSchema(1, 4000)
		if name == "dbx_execute_batch" {
			p["use_transaction"] = map[string]any{"type": "boolean"}
			p["continue_on_error"] = map[string]any{"type": "boolean"}
		}
		list = append(list, tool(name, "Execute bounded SQL under saved MCP permissions. Batch execution is always atomic; continue_on_error and persistent sessions are unavailable. Read-only transactions protect reads. Unrecognized functions, external I/O and session/admin SQL are rejected.", p, []string{"sql"}, false))
	}
	p = selector()
	p["filter"] = strSchema()
	p["limit"] = integerSchema(1, 100)
	p["offset"] = integerSchema(0, 1000000)
	list = append(list, tool("dbx_list_saved_sql", "List saved SQL summaries for one permitted connection/database, without SQL contents.", p, nil, true))
	p = selector()
	p["id"] = strSchema()
	list = append(list, tool("dbx_get_saved_sql", "Read a saved SQL file strictly within the permitted connection/database scope (64 KiB maximum).", p, []string{"id"}, true))
	p = selector()
	p["name"] = strSchema()
	p["sql"] = strSchema()
	list = append(list, tool("dbx_save_sql", "Create a new saved SQL file; never execute or overwrite SQL. Allowed by read-only database policies.", p, []string{"name", "sql"}, false))
	p = selector()
	delete(p, "schema")
	p["search_text"] = strSchema()
	p["success"] = map[string]any{"type": "boolean"}
	p["limit"] = integerSchema(1, 100)
	p["cursor"] = map[string]any{"type": "object", "properties": map[string]any{"id": strSchema(), "executed_at": strSchema()}, "required": []string{"id", "executed_at"}, "additionalProperties": false}
	list = append(list, tool("dbx_search_history", "Search source=sql query history within one permitted connection/database; excludes MCP request/response and private details.", p, nil, true))
	return list
}
func mcpCheckArgs(name string, args map[string]any) error {
	for _, t := range mcpToolDefinitions() {
		if t.Name != name {
			continue
		}
		props := t.InputSchema["properties"].(map[string]any)
		for k := range args {
			if _, ok := props[k]; !ok {
				return fmt.Errorf("Unsupported MCP argument: %s", k)
			}
		}
		for _, k := range t.InputSchema["required"].([]string) {
			if strings.TrimSpace(str(args, k)) == "" {
				return fmt.Errorf("%s is required", k)
			}
		}
		for k, v := range args {
			spec := props[k].(map[string]any)
			switch spec["type"] {
			case "string":
				if _, ok := v.(string); !ok {
					return fmt.Errorf("%s must be a string", k)
				}
			case "boolean":
				if _, ok := v.(bool); !ok {
					return fmt.Errorf("%s must be boolean", k)
				}
			case "integer":
				n, parseErr := strconv.ParseFloat(fmt.Sprint(v), 64)
				if parseErr != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < float64(spec["minimum"].(int)) || n > float64(spec["maximum"].(int)) || n != math.Trunc(n) {
					return fmt.Errorf("%s is outside its supported integer range", k)
				}
			case "array":
				a, ok := v.([]any)
				if !ok {
					return fmt.Errorf("%s must be an array", k)
				}
				if len(a) > 20 {
					return errors.New("MCP table list exceeds 20 items")
				}
				for _, x := range a {
					if _, ok := x.(string); !ok {
						return errors.New("MCP table names must be strings")
					}
				}
			case "object":
				if _, ok := v.(map[string]any); !ok {
					return fmt.Errorf("%s must be an object", k)
				}
			}
		}
		return nil
	}
	return errors.New("MCP tool is not implemented")
}

type mcpScope struct {
	config           map[string]any
	id, db, schema   string
	groups           []string
	readOnly, danger bool
}

func (s *Server) mcpResolve(p MCPPolicy, args map[string]any, requireDB bool) (mcpScope, error) {
	state := s.snapshot()
	groups := map[string][]string{}
	if len(p.AllowedGroupIDs) > 0 || len(p.GroupPolicies) > 0 {
		var err error
		groups, err = mcpGroupPaths(state.Sidebar)
		if err != nil {
			return mcpScope{}, err
		}
	}
	id, name := str(args, "connection_id"), str(args, "connection_name")
	if id == "" && name == "" {
		return mcpScope{}, errors.New("connection_id or connection_name is required")
	}
	var selected map[string]any
	for _, c := range state.Connections {
		cid := str(c, "id")
		if !p.allowsConnection(cid, groups[cid]) {
			continue
		}
		if id != "" && cid != id || id == "" && str(c, "name") != name {
			continue
		}
		if selected != nil {
			return mcpScope{}, errors.New("Connection name is ambiguous; use connection_id")
		}
		selected = c
	}
	if selected == nil {
		return mcpScope{}, errors.New("Connection does not exist or is not allowed by MCP policy")
	}
	id = str(selected, "id")
	kind := str(selected, "db_type")
	if kind != "sqlite" && kind != "mysql" && kind != "mariadb" && kind != "postgres" {
		return mcpScope{}, errors.New("This database driver has not been migrated to Go MCP")
	}
	db := strings.TrimSpace(str(args, "database"))
	if db == "" {
		db = str(selected, "database")
	}
	if kind == "sqlite" && db == "" {
		db = "main"
	}
	if requireDB && !p.allowsDatabase(id, db) {
		return mcpScope{}, errors.New("Database is not allowed by MCP policy")
	}
	if r := p.connectionRule(id); r != nil && r.DatabaseScope == "selected" && kind != "sqlite" {
		return mcpScope{}, errors.New("Scoped SQL access for network databases is not implemented")
	}
	schema := str(args, "schema")
	if (kind == "mysql" || kind == "mariadb") && schema != "" && schema != db {
		return mcpScope{}, errors.New("MySQL schema must equal the permitted database")
	}
	if kind == "sqlite" && (schema != "" && schema != "main" || db != "main") {
		return mcpScope{}, errors.New("Go MCP SQLite access is restricted to the main database")
	}
	ro, danger := p.executionPolicy(id, db, groups[id])
	if boolValue(selected, "read_only") || boolValue(selected, "is_production") || mcpHasItems(selected["production_databases"]) {
		ro = true
		danger = false
	}
	return mcpScope{config: selected, id: id, db: db, schema: schema, groups: groups[id], readOnly: ro, danger: danger}, nil
}
func mcpHasItems(v any) bool {
	switch a := v.(type) {
	case []any:
		return len(a) > 0
	case []string:
		return len(a) > 0
	}
	return false
}
func (b *mcpBackend) Call(ctx context.Context, name string, args map[string]any) (result any, err error) {
	if err = mcpCheckArgs(name, args); err != nil {
		return nil, err
	}
	p, err := b.s.LoadMCPPolicy()
	if err != nil {
		return nil, err
	}
	if !p.Configured {
		return nil, errors.New("MCP access is not configured; configure permissions in DBX Settings")
	}
	if !p.allowsTool(name) {
		return nil, errors.New("Tool is not allowed by MCP policy")
	}
	if name == "dbx_list_connections" {
		start := time.Now()
		result, err = b.s.mcpConnections(ctx, p)
		if auditErr := b.s.mcpAudit(name, args, mcpScope{}, start, result, err); auditErr != nil && err == nil {
			return nil, errors.New("Saving MCP audit history failed")
		}
		return result, err
	}
	scope, err := b.s.mcpResolve(p, args, name != "dbx_list_databases")
	if err != nil {
		return nil, err
	}
	started := time.Now()
	defer func() {
		if err != nil {
			message, _ := mcpBoundText(b.s.mcpRedact(err.Error(), scope.config), 2048)
			err = errors.New(message)
		}
		{
			auditErr := b.s.mcpAudit(name, args, scope, started, result, err)
			if auditErr != nil && err == nil {
				err = errors.New("MCP operation completed, but saving its audit history failed")
			}
		}
	}()
	if name == "dbx_list_databases" {
		if rule := p.connectionRule(scope.id); rule != nil && (rule.DatabaseScope == "none" || rule.DatabaseScope == "selected" && len(rule.AllowedDatabases) == 0) {
			return []map[string]any{}, nil
		}
	}
	switch name {
	case "dbx_list_saved_sql", "dbx_get_saved_sql", "dbx_save_sql":
		return b.s.mcpSavedSQL(name, args, scope)
	case "dbx_search_history":
		return b.s.mcpSearchHistory(args, scope)
	}
	if name == "dbx_execute_query" || name == "dbx_execute_batch" {
		sql := str(args, "sql")
		if len(sql) > 64<<10 {
			return nil, errors.New("MCP SQL exceeds 64 KiB")
		}
		statements, e := database.ValidateMCPSQL(sql, str(scope.config, "db_type"), scope.readOnly, scope.danger)
		if e != nil {
			return nil, e
		}
		if name == "dbx_execute_query" && len(statements) != 1 {
			return nil, errors.New("Use dbx_execute_batch for multiple statements")
		}
		if boolValue(args, "continue_on_error") {
			return nil, errors.New("Go MCP batches are atomic; continue_on_error is unsupported")
		}
		if value, ok := args["use_transaction"].(bool); ok && !value {
			return nil, errors.New("Go MCP batches require use_transaction=true (atomic execution)")
		}
	}
	if str(scope.config, "db_type") != "sqlite" && !boolValue(scope.config, "save_password") {
		return nil, errors.New("MCP cannot reconnect without saved credentials; enable Save password for this connection in DBX")
	}
	timeout := 300
	if p.QueryTimeoutSecs != nil {
		timeout = *p.QueryTimeoutSecs
	} else if v := integer(scope.config["query_timeout_secs"], 0); v > 0 && v <= 86400 {
		timeout = v
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	manager := database.NewManager()
	defer manager.Close()
	if str(scope.config, "db_type") == "sqlite" {
		info, statErr := os.Stat(str(scope.config, "host"))
		if statErr != nil || !info.Mode().IsRegular() {
			return nil, errors.New("MCP requires an existing SQLite database file; open or create it in DBX first")
		}
	}
	cfg := cloneMap(scope.config)
	cfg["database"] = scope.db
	if _, err = manager.Handle(ctx, "/api/connection/connect", map[string]any{"config": cfg}); err != nil {
		return nil, err
	}
	req := map[string]any{"connectionId": scope.id, "database": scope.db, "schema": scope.schema}
	switch name {
	case "dbx_list_databases":
		value, e := manager.Handle(ctx, "/api/schema/databases", req)
		if e != nil {
			return nil, e
		}
		list := []map[string]any{}
		for _, v := range value.([]map[string]any) {
			if p.allowsDatabase(scope.id, str(v, "name")) {
				list = append(list, v)
			}
		}
		return list, nil
	case "dbx_list_tables":
		return mcpTables(ctx, manager, req, boolValue(args, "all_schemas"))
	case "dbx_describe_table", "dbx_list_indexes", "dbx_get_table_ddl":
		req["table"] = str(args, "table")
		if name == "dbx_get_table_ddl" {
			return manager.Handle(ctx, "/api/schema/ddl", req)
		}
		if name == "dbx_list_indexes" {
			return manager.Handle(ctx, "/api/schema/indexes", req)
		}
		out := map[string]any{}
		for _, part := range []string{"columns", "indexes", "foreign-keys", "ddl"} {
			v, e := manager.Handle(ctx, "/api/schema/"+part, req)
			if e != nil {
				if errors.Is(e, database.ErrUnsupported) && part != "columns" {
					out[strings.ReplaceAll(part, "-", "_")] = map[string]any{"available": false, "reason": "Not implemented by this Go database driver"}
					continue
				}
				return nil, e
			}
			out[strings.ReplaceAll(part, "-", "_")] = v
		}
		return out, nil
	case "dbx_get_schema_context":
		value, e := mcpTables(ctx, manager, req, false)
		if e != nil {
			return nil, e
		}
		tables := value.([]map[string]any)
		wanted := map[string]bool{}
		if a, ok := args["tables"].([]any); ok {
			for _, v := range a {
				wanted[strings.ToLower(v.(string))] = true
			}
		}
		max := mcpInteger(args["max_tables"], 8)
		out := []map[string]any{}
		total := 0
		for _, t := range tables {
			if len(wanted) > 0 && !wanted[strings.ToLower(str(t, "name"))] {
				continue
			}
			total++
			if len(out) >= max {
				continue
			}
			req["table"] = str(t, "name")
			cols, e := manager.Handle(ctx, "/api/schema/columns", req)
			if e != nil {
				return nil, e
			}
			out = append(out, map[string]any{"name": t["name"], "table_type": t["table_type"], "columns": cols})
		}
		return map[string]any{"connection_id": scope.id, "database": scope.db, "tables": out, "truncated": total > len(out)}, nil
	case "dbx_execute_query", "dbx_execute_batch":
		sql := str(args, "sql")
		req["sql"] = sql
		req["readOnly"] = scope.readOnly
		req["maxRows"] = mcpInteger(args["max_rows"], 100)
		req["maxResultBytes"] = 4 << 20
		req["timeoutSecs"] = timeout
		path := "/api/query/execute"
		if name == "dbx_execute_batch" {
			path = "/api/query/execute-multi"
			req["useTransaction"] = true
		}
		value, e := manager.Handle(ctx, path, req)
		if e != nil {
			return nil, e
		}
		return mcpWindowResult(value, mcpInteger(args["cell_char_offset"], 0), mcpInteger(args["cell_char_limit"], 200)), nil
	}
	return nil, errors.New("MCP tool is not implemented")
}
func (s *Server) mcpConnections(ctx context.Context, p MCPPolicy) (any, error) {
	st := s.snapshot()
	groups := map[string][]string{}
	if len(p.AllowedGroupIDs) > 0 || len(p.GroupPolicies) > 0 {
		var err error
		groups, err = mcpGroupPaths(st.Sidebar)
		if err != nil {
			return nil, err
		}
	}
	out := []map[string]any{}
	for _, c := range st.Connections {
		id := str(c, "id")
		if !p.allowsConnection(id, groups[id]) {
			continue
		}
		kind := str(c, "db_type")
		if kind != "sqlite" && kind != "mysql" && kind != "mariadb" && kind != "postgres" {
			continue
		}
		ro, danger := p.executionPolicy(id, str(c, "database"), groups[id])
		if boolValue(c, "read_only") || boolValue(c, "is_production") || mcpHasItems(c["production_databases"]) {
			ro = true
			danger = false
		}
		item := map[string]any{"id": id, "name": str(c, "name"), "db_type": kind, "read_only": ro, "allow_dangerous_sql": danger}
		if db := str(c, "database"); p.allowsDatabase(id, db) {
			item["database"] = db
		}
		out = append(out, item)
	}
	return out, nil
}
func mcpTables(ctx context.Context, m *database.Manager, req map[string]any, all bool) (any, error) {
	if !all {
		return m.Handle(ctx, "/api/schema/tables", req)
	}
	if str(req, "schema") != "" {
		return nil, errors.New("all_schemas cannot be combined with schema")
	}
	v, err := m.Handle(ctx, "/api/schema/schemas", req)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, schema := range v.([]map[string]any) {
		r := cloneMap(req)
		r["schema"] = schema["name"]
		v, err = m.Handle(ctx, "/api/schema/tables", r)
		if err != nil {
			return nil, err
		}
		for _, table := range v.([]map[string]any) {
			table["schema"] = schema["name"]
			out = append(out, table)
		}
	}
	return out, nil
}
func mcpWindowResult(value any, offset, limit int) any {
	b, _ := json.Marshal(value)
	var result any
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	_ = d.Decode(&result)
	apply := func(v map[string]any) {
		truncated := false
		if rows, ok := v["rows"].([]any); ok {
			for _, row := range rows {
				if cells, ok := row.([]any); ok {
					for i, cell := range cells {
						if s, ok := cell.(string); ok {
							r := []rune(s)
							start := offset
							if start > len(r) {
								start = len(r)
							}
							end := start + limit
							if end > len(r) {
								end = len(r)
							}
							if end < len(r) {
								truncated = true
							}
							cells[i] = string(r[start:end])
						}
					}
				}
			}
		}
		v["cell_char_offset"] = offset
		v["cell_char_limit"] = limit
		v["cells_truncated"] = truncated
		if truncated {
			v["next_cell_char_offset"] = offset + limit
		}
	}
	switch v := result.(type) {
	case map[string]any:
		apply(v)
	case []any:
		for _, x := range v {
			if r, ok := x.(map[string]any); ok {
				apply(r)
			}
		}
	}
	return result
}
func (s *Server) mcpRedact(message string, c map[string]any) string {
	walkSecrets(c, func(_ map[string]any, _ string, value string) {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[REDACTED]")
			message = strings.ReplaceAll(message, url.QueryEscape(value), "[REDACTED]")
			message = strings.ReplaceAll(message, url.PathEscape(value), "[REDACTED]")
		}
	})
	for _, key := range []string{"host", "username", "connection_string", "url_params"} {
		if v := str(c, key); v != "" {
			message = strings.ReplaceAll(message, v, "[REDACTED]")
		}
	}
	return message
}
func (s *Server) mcpAudit(name string, args map[string]any, scope mcpScope, started time.Time, result any, callErr error) error {
	entry := map[string]any{"id": transferID(), "connection_id": scope.id, "connection_name": str(scope.config, "name"), "database": scope.db, "source": "mcp", "activity_kind": "query", "mcp_tool_name": name, "sql": s.mcpRedact(str(args, "sql"), scope.config), "executed_at": started.UTC().Format(time.RFC3339Nano), "execution_time_ms": time.Since(started).Milliseconds(), "success": callErr == nil}
	if name != "dbx_execute_query" && name != "dbx_execute_batch" {
		entry["activity_kind"] = "tool"
	}
	var affected int64
	addAffected := func(v map[string]any) {
		n, _ := strconv.ParseInt(fmt.Sprint(v["affected_rows"]), 10, 64)
		affected += n
	}
	switch v := result.(type) {
	case map[string]any:
		addAffected(v)
	case []any:
		for _, raw := range v {
			if r, ok := raw.(map[string]any); ok {
				addAffected(r)
			}
		}
	}
	entry["affected_rows"] = affected
	if callErr != nil {
		entry["error"] = callErr.Error()
	}
	return s.update(func(st *diskState) error {
		st.History = append(st.History, entry)
		sortedHistory(st.History)
		limit := integer(st.Settings["mcp-history-retention-limit"], 1000)
		if limit > 0 {
			out := make([]map[string]any, 0, len(st.History))
			n := 0
			for _, x := range st.History {
				if source(x) == "mcp" {
					n++
					if n > limit {
						continue
					}
				}
				out = append(out, x)
			}
			st.History = out
		}
		return nil
	})
}
func mcpSavedScope(file map[string]any, scope mcpScope) bool {
	return str(file, "connectionId") == scope.id && str(file, "database") == scope.db && (scope.schema == "" || str(file, "schema") == scope.schema) && str(file, "catalog") == ""
}
func mcpSavedSummary(file map[string]any) map[string]any {
	return map[string]any{"id": file["id"], "connection_id": file["connectionId"], "database": file["database"], "schema": file["schema"], "name": file["name"], "created_at": file["createdAt"], "updated_at": file["updatedAt"]}
}
func (s *Server) mcpSavedSQL(name string, args map[string]any, scope mcpScope) (any, error) {
	if name == "dbx_save_sql" {
		title, sql := strings.TrimSpace(str(args, "name")), str(args, "sql")
		if title == "" || utf8.RuneCountInString(title) > 255 || strings.TrimSpace(sql) == "" || len(sql) > 64<<10 {
			return nil, errors.New("Saved SQL requires a name up to 255 characters and 1–65536 bytes of SQL")
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		file := map[string]any{"id": transferID(), "connectionId": scope.id, "database": scope.db, "schema": scope.schema, "name": title, "sql": sql, "sqlLoaded": true, "orderIndex": 0, "openCount": 0, "createdAt": now, "updatedAt": now}
		err := s.update(func(st *diskState) error { st.SQLFiles = append(st.SQLFiles, file); return nil })
		return mcpSavedSummary(file), err
	}
	files := []map[string]any{}
	for _, f := range s.snapshot().SQLFiles {
		if !mcpSavedScope(f, scope) {
			continue
		}
		if name == "dbx_get_saved_sql" && str(f, "id") == str(args, "id") {
			if len(str(f, "sql")) > 64<<10 {
				return nil, errors.New("Saved SQL exceeds 64 KiB")
			}
			out := mcpSavedSummary(f)
			out["sql"] = str(f, "sql")
			return out, nil
		}
		if name == "dbx_list_saved_sql" && (str(args, "filter") == "" || strings.Contains(strings.ToLower(str(f, "name")), strings.ToLower(str(args, "filter")))) {
			files = append(files, f)
		}
	}
	if name == "dbx_get_saved_sql" {
		return nil, errors.New("Saved SQL does not exist in the selected scope")
	}
	sort.Slice(files, func(i, j int) bool { return str(files[i], "updatedAt") > str(files[j], "updatedAt") })
	total := len(files)
	start := mcpInteger(args["offset"], 0)
	if start > total {
		start = total
	}
	end := start + mcpInteger(args["limit"], 20)
	if end > total {
		end = total
	}
	out := []map[string]any{}
	for _, f := range files[start:end] {
		out = append(out, mcpSavedSummary(f))
	}
	var next any
	if end < total {
		next = end
	}
	return map[string]any{"files": out, "total": total, "offset": start, "limit": mcpInteger(args["limit"], 20), "next_offset": next}, nil
}
func (s *Server) mcpSearchHistory(args map[string]any, scope mcpScope) (any, error) {
	entries := s.snapshot().History
	sortedHistory(entries)
	out := []map[string]any{}
	total := 0
	limit := mcpInteger(args["limit"], 20)
	cursor, _ := args["cursor"].(map[string]any)
	if cursor != nil {
		if _, err := time.Parse(time.RFC3339Nano, str(cursor, "executed_at")); err != nil || str(cursor, "id") == "" {
			return nil, errors.New("Invalid history cursor")
		}
	}
	for _, e := range entries {
		if str(e, "connection_id") != scope.id || str(e, "database") != scope.db || source(e) != "sql" || activity(e) != "query" || !historyMatches(e, args) {
			continue
		}
		total++
		if cursor != nil && (str(e, "executed_at") > str(cursor, "executed_at") || str(e, "executed_at") == str(cursor, "executed_at") && str(e, "id") >= str(cursor, "id")) {
			continue
		}
		if len(out) > limit {
			continue
		}
		copy := map[string]any{}
		for _, k := range []string{"id", "connection_id", "connection_name", "database", "executed_at", "execution_time_ms", "success", "affected_rows"} {
			copy[k] = e[k]
		}
		sql := s.mcpRedact(str(e, "sql"), scope.config)
		copy["sql_bytes"] = len(sql)
		copy["sql"], copy["sql_truncated"] = mcpBoundText(sql, 16<<10)
		copy["error"], copy["error_truncated"] = mcpBoundText(s.mcpRedact(str(e, "error"), scope.config), 2048)
		out = append(out, copy)
	}
	var next any
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = map[string]any{"id": last["id"], "executed_at": last["executed_at"]}
	}
	return map[string]any{"entries": out, "total": total, "next_cursor": next}, nil
}
func mcpBoundText(text string, max int) (string, bool) {
	if len(text) <= max {
		return text, false
	}
	for max > 0 && !utf8.RuneStart(text[max]) {
		max--
	}
	return text[:max], true
}

func mcpInteger(value any, fallback int) int {
	if n, ok := value.(json.Number); ok {
		v, e := n.Int64()
		if e == nil {
			return int(v)
		}
	}
	return integer(value, fallback)
}
