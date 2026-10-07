package server

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func mcpFixture(t *testing.T) (*Server, map[string]any) {
	t.Helper()
	s := testServer(t, t.TempDir())
	config := map[string]any{"id": "allowed", "name": "Allowed database", "db_type": "sqlite", "host": filepath.Join(t.TempDir(), "mcp.sqlite"), "database": "main", "save_password": false, "password": "private-db-password"}
	if err := s.update(func(st *diskState) error {
		st.Connections = []map[string]any{config, {"id": "hidden", "name": "Hidden database", "db_type": "sqlite", "host": "private-host", "database": "main", "password": "hidden-password"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.database.Handle(context.Background(), "/api/connection/connect", map[string]any{"config": config}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.database.Handle(context.Background(), "/api/query/execute-in-transaction", map[string]any{"connectionId": "allowed", "statements": []string{"CREATE TABLE people (id INTEGER PRIMARY KEY, name TEXT NOT NULL)", "INSERT INTO people VALUES (1, '小古'), (2, 'testing')", "CREATE INDEX people_name ON people(name)"}}); err != nil {
		t.Fatal(err)
	}
	return s, config
}
func mcpAllow(t *testing.T, s *Server) MCPPolicy {
	t.Helper()
	p := defaultMCPPolicy()
	p.AllowedConnectionIDs = []string{"allowed"}
	if err := s.SaveMCPPolicy(p); err != nil {
		t.Fatal(err)
	}
	return p
}
func mcpCall(t *testing.T, s *Server, name string, args map[string]any) any {
	t.Helper()
	v, err := s.MCPBackend().Call(context.Background(), name, args)
	if err != nil {
		t.Fatalf("%s failed: %v", name, err)
	}
	return v
}
func mcpArgs(sql string) map[string]any {
	return map[string]any{"connection_id": "allowed", "sql": sql}
}
func TestMCPDefaultPolicyAndFiltering(t *testing.T) {
	s, _ := mcpFixture(t)
	backend := s.MCPBackend()
	p, err := s.LoadMCPPolicy()
	if err != nil || p.Configured || !p.ReadOnly || p.AllowedConnectionIDs == nil {
		t.Fatalf("unsafe default: %+v %v", p, err)
	}
	if _, err = backend.Call(context.Background(), "dbx_list_connections", map[string]any{}); err == nil {
		t.Fatal("unconfigured access allowed")
	}
	p = mcpAllow(t, s)
	listed := mcpCall(t, s, "dbx_list_connections", map[string]any{})
	text := fmt.Sprint(listed)
	if strings.Contains(text, "Hidden") || strings.Contains(text, "password") || strings.Contains(text, "host") || strings.Contains(text, "sqlite/") {
		t.Fatal(text)
	}
	if len(listed.([]map[string]any)) != 1 {
		t.Fatal(listed)
	}
	for _, name := range []string{"dbx_list_tables", "dbx_list_databases", "dbx_search_history", "dbx_list_saved_sql"} {
		if _, err := backend.Call(context.Background(), name, map[string]any{"connection_id": "hidden"}); err == nil {
			t.Fatalf("%s revealed hidden connection", name)
		}
	}
	p.AllowedConnectionIDs = []string{}
	if err = s.SaveMCPPolicy(p); err != nil {
		t.Fatal(err)
	}
	if len(mcpCall(t, s, "dbx_list_connections", map[string]any{}).([]map[string]any)) != 0 {
		t.Fatal("empty allowlist allowed connections")
	}
	p.AllowedConnectionIDs = nil
	if err = s.SaveMCPPolicy(p); err != nil {
		t.Fatal(err)
	}
	if len(mcpCall(t, s, "dbx_list_connections", map[string]any{}).([]map[string]any)) != 2 {
		t.Fatal("null allowlist did not mean all")
	}
	p.AllowedToolNames = []string{"dbx_list_connections"}
	if err = s.SaveMCPPolicy(p); err != nil {
		t.Fatal(err)
	}
	if _, err = backend.Call(context.Background(), "dbx_execute_query", mcpArgs("SELECT 1")); err == nil {
		t.Fatal("tool allowlist bypassed")
	}
}
func TestMCPReadOnlyMetadataAndAudit(t *testing.T) {
	s, config := mcpFixture(t)
	mcpAllow(t, s)
	res := mcpCall(t, s, "dbx_execute_query", mcpArgs("SELECT id,name FROM people ORDER BY id")).(map[string]any)
	rows := res["rows"].([]any)
	if len(rows) != 2 || rows[0].([]any)[1] != "小古" {
		t.Fatal(res)
	}
	for _, sql := range []string{"INSERT INTO people VALUES(3,'blocked')", "DELETE FROM people", "CREATE TABLE bad (id INT)", "ATTACH DATABASE '/tmp/other.db' AS other", "PRAGMA query_only = OFF", "SELECT load_extension('/tmp/x')", "SELECT \"load_extension\"('/tmp/x')", "SELECT unknown_function()", "WITH d AS (DELETE FROM people RETURNING *) SELECT * FROM d"} {
		if _, err := s.MCPBackend().Call(context.Background(), "dbx_execute_query", mcpArgs(sql)); err == nil {
			t.Fatalf("readonly guard allowed %s", sql)
		}
	}
	if _, err := s.MCPBackend().Call(context.Background(), "dbx_execute_query", map[string]any{"connection_id": "allowed", "sql": "DELETE FROM people", "readOnly": false}); err == nil {
		t.Fatal("caller overrode policy")
	}
	desc := mcpCall(t, s, "dbx_describe_table", map[string]any{"connection_id": "allowed", "table": "people"}).(map[string]any)
	if len(desc["columns"].([]map[string]any)) != 2 || !strings.Contains(desc["ddl"].(string), "CREATE TABLE") {
		t.Fatal(desc)
	}
	if len(mcpCall(t, s, "dbx_list_indexes", map[string]any{"connection_id": "allowed", "table": "people"}).([]map[string]any)) == 0 {
		t.Fatal("indexes missing")
	}
	ctx := mcpCall(t, s, "dbx_get_schema_context", map[string]any{"connection_id": "allowed"}).(map[string]any)
	if len(ctx["tables"].([]map[string]any)) != 1 {
		t.Fatal(ctx)
	}
	history := s.snapshot().History
	if len(history) < 2 {
		t.Fatal("MCP audit absent")
	}
	for _, entry := range history {
		if entry["source"] != "mcp" || str(entry, "mcp_tool_name") == "" {
			t.Fatal(entry)
		}
		if strings.Contains(fmt.Sprint(entry), str(config, "password")) {
			t.Fatal("credential leak in history")
		}
	}
}
func TestMCPAtomicBatchAndProductionProtection(t *testing.T) {
	s, c := mcpFixture(t)
	p := mcpAllow(t, s)
	p.ReadOnly = false
	if err := s.SaveMCPPolicy(p); err != nil {
		t.Fatal(err)
	}
	mcpCall(t, s, "dbx_execute_batch", map[string]any{"connection_id": "allowed", "sql": "INSERT INTO people VALUES (3,'third'); UPDATE people SET name='updated' WHERE id=3", "use_transaction": true})
	if _, err := s.MCPBackend().Call(context.Background(), "dbx_execute_batch", mcpArgs("INSERT INTO people VALUES(4,'rollback'); INSERT INTO missing VALUES(1)")); err == nil {
		t.Fatal("batch error absent")
	}
	res := mcpCall(t, s, "dbx_execute_query", mcpArgs("SELECT count(*) FROM people")).(map[string]any)
	if fmt.Sprint(res["rows"]) != "[[3]]" {
		t.Fatalf("rollback failed: %v", res)
	}
	if _, err := s.MCPBackend().Call(context.Background(), "dbx_execute_query", mcpArgs("DROP TABLE people")); err == nil {
		t.Fatal("dangerous SQL allowed without opt-in")
	}
	for _, sql := range []string{"DELETE FROM people", "UPDATE people SET name='all'", "DELETE FROM people WHERE 1=1", "DELETE FROM people WHERE TRUE=TRUE", "DELETE FROM people WHERE id=id", `DELETE FROM people WHERE "literal"='literal'`} {
		if _, err := s.MCPBackend().Call(context.Background(), "dbx_execute_query", mcpArgs(sql)); err == nil {
			t.Fatalf("unguarded dangerous DML allowed: %s", sql)
		}
	}
	p.AllowDangerousSQL = true
	if err := s.SaveMCPPolicy(p); err != nil {
		t.Fatal(err)
	}
	mcpCall(t, s, "dbx_execute_query", mcpArgs("CREATE TABLE extra(id INTEGER)"))
	mcpCall(t, s, "dbx_execute_query", mcpArgs("DROP TABLE extra"))
	for _, flag := range []string{"read_only", "is_production", "production_databases"} {
		if err := s.update(func(st *diskState) error {
			copy := cloneMap(c)
			if flag == "production_databases" {
				copy[flag] = []string{"another_database"}
			} else {
				copy[flag] = true
			}
			st.Connections[0] = copy
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// GUI unlocks must not grant MCP write permission.
		_, _ = s.database.Handle(context.Background(), "/api/connection/write-unlock", map[string]any{"connectionId": "allowed", "durationSecs": 3600})
		if _, err := s.MCPBackend().Call(context.Background(), "dbx_execute_query", mcpArgs("DELETE FROM people")); err == nil {
			t.Fatalf("MCP bypassed %s", flag)
		}
	}
}
func TestMCPSavedSQLHistoryScope(t *testing.T) {
	s, _ := mcpFixture(t)
	mcpAllow(t, s)
	file := mcpCall(t, s, "dbx_save_sql", map[string]any{"connection_id": "allowed", "name": "report", "sql": "SELECT * FROM people"}).(map[string]any)
	got := mcpCall(t, s, "dbx_get_saved_sql", map[string]any{"connection_id": "allowed", "id": file["id"]}).(map[string]any)
	if got["sql"] != "SELECT * FROM people" {
		t.Fatal(got)
	}
	if err := s.update(func(st *diskState) error {
		st.SQLFiles = append(st.SQLFiles, map[string]any{"id": "secret", "connectionId": "hidden", "database": "main", "name": "private", "sql": "private SQL"})
		st.History = append(st.History, map[string]any{"id": "h1", "connection_id": "allowed", "database": "main", "sql": "SELECT 1", "source": "sql", "activity_kind": "query", "executed_at": "2026-10-07T00:00:00Z", "success": true}, map[string]any{"id": "h2", "connection_id": "hidden", "database": "main", "sql": "private SQL", "source": "sql", "executed_at": "2026-10-07T01:00:00Z"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MCPBackend().Call(context.Background(), "dbx_get_saved_sql", map[string]any{"connection_id": "allowed", "id": "secret"}); err == nil {
		t.Fatal("file ID escaped connection scope")
	}
	files := mcpCall(t, s, "dbx_list_saved_sql", map[string]any{"connection_id": "allowed"})
	if strings.Contains(fmt.Sprint(files), "SELECT") || strings.Contains(fmt.Sprint(files), "private") {
		t.Fatal(files)
	}
	entries := mcpCall(t, s, "dbx_search_history", map[string]any{"connection_id": "allowed"}).(map[string]any)["entries"].([]map[string]any)
	if len(entries) != 1 || entries[0]["id"] != "h1" {
		t.Fatal(entries)
	}
}
func TestMCPPolicyScopeGroupsAndPersistence(t *testing.T) {
	s, _ := mcpFixture(t)
	p := mcpAllow(t, s)
	if err := s.update(func(st *diskState) error {
		st.Sidebar = map[string]any{"groups": []any{map[string]any{"id": "outer"}, map[string]any{"id": "inner"}}, "order": []any{map[string]any{"type": "group", "id": "outer", "children": []any{map[string]any{"type": "group", "id": "inner", "connectionIds": []string{"allowed"}}}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p.AllowedConnectionIDs = []string{}
	p.AllowedGroupIDs = []string{"outer"}
	p.GroupPolicies = []MCPGroupPolicy{{GroupID: "outer", ReadOnly: false}, {GroupID: "inner", ReadOnly: true}}
	if err := s.SaveMCPPolicy(p); err != nil {
		t.Fatal(err)
	}
	if len(mcpCall(t, s, "dbx_list_connections", map[string]any{}).([]map[string]any)) != 1 {
		t.Fatal("ancestor group access missing")
	}
	if _, err := s.MCPBackend().Call(context.Background(), "dbx_execute_query", mcpArgs("DELETE FROM people")); err == nil {
		t.Fatal("inner group mode ignored")
	}
	version := 1
	p.ConnectionPolicies = []MCPConnectionPolicy{{ConnectionID: "allowed", ReadOnly: false, ExecutionModeConfigured: true, ExecutionModePolicyVersion: &version, DatabaseScope: "selected", AllowedDatabases: []string{"main"}, DatabasePolicies: []MCPDatabasePolicy{{DatabaseName: "main", ReadOnly: true}}}}
	if err := s.SaveMCPPolicy(p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MCPBackend().Call(context.Background(), "dbx_execute_query", map[string]any{"connection_id": "allowed", "database": "other", "sql": "SELECT 1"}); err == nil {
		t.Fatal("database scope bypassed")
	}
	if _, err := s.MCPBackend().Call(context.Background(), "dbx_execute_query", mcpArgs("SELECT * FROM other.people")); err == nil {
		t.Fatal("qualified cross-database SQL succeeded")
	}
	if _, err := s.MCPBackend().Call(context.Background(), "dbx_list_tables", map[string]any{"connection_id": "allowed", "schema": "temp"}); err == nil {
		t.Fatal("schema escaped allowed database")
	}
	loaded, err := s.LoadMCPPolicy()
	if err != nil || !loaded.Configured || loaded.ConnectionPolicies[0].DatabaseScope != "selected" {
		t.Fatalf("policy persistence failed: %+v %v", loaded, err)
	}
	loaded.ConnectionPolicies[0].DatabaseScope = "none"
	loaded.ConnectionPolicies[0].AllowedDatabases = nil
	loaded.ConnectionPolicies[0].DatabasePolicies = nil
	if err = s.SaveMCPPolicy(loaded); err != nil {
		t.Fatal(err)
	}
	if len(mcpCall(t, s, "dbx_list_databases", map[string]any{"connection_id": "allowed"}).([]map[string]any)) != 0 {
		t.Fatal("scope=none leaked database")
	}
}
func TestMCPCredentialsAndPolicyValidation(t *testing.T) {
	s, _ := mcpFixture(t)
	p := mcpAllow(t, s)
	if err := s.update(func(st *diskState) error {
		st.Connections = append(st.Connections, map[string]any{"id": "network", "name": "network", "db_type": "mysql", "host": "sensitive-internal-host", "save_password": false, "database": "prod"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p.AllowedConnectionIDs = append(p.AllowedConnectionIDs, "network")
	if err := s.SaveMCPPolicy(p); err != nil {
		t.Fatal(err)
	}
	_, err := s.MCPBackend().Call(context.Background(), "dbx_execute_query", map[string]any{"connection_id": "network", "sql": "SELECT 1"})
	if err == nil || !strings.Contains(err.Error(), "saved credentials") || strings.Contains(err.Error(), "sensitive-internal-host") {
		t.Fatal(err)
	}
	p.ConnectionPolicies = []MCPConnectionPolicy{{ConnectionID: "network", DatabaseScope: "selected", AllowedDatabases: []string{"prod"}}}
	if err := s.SaveMCPPolicy(p); err == nil {
		t.Fatal("unsupported network database scoping silently accepted")
	}
	var partial MCPPolicy
	if err = json.Unmarshal([]byte(`{"allowedConnectionIds":null}`), &partial); err != nil || !partial.ReadOnly || partial.Configured {
		t.Fatal("partial policy failed closed", partial, err)
	}
	if err = json.Unmarshal([]byte(`{"read_only":false}`), &partial); err == nil {
		t.Fatal("unknown security field silently ignored")
	}
}
func TestMCPConcurrentCallsAndPolicyChanges(t *testing.T) {
	s, _ := mcpFixture(t)
	p := mcpAllow(t, s)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				if _, err := s.MCPBackend().Call(context.Background(), "dbx_execute_query", mcpArgs("SELECT count(*) FROM people")); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	for i := 0; i < 8; i++ {
		if err := s.SaveMCPPolicy(p); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
