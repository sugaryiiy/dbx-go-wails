package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sugaryiiy/magpiedb/internal/mcpbridge"
)

var objectMCPTools = []string{
	"dbx_list_objects", "dbx_list_routines", "dbx_list_events", "dbx_list_triggers",
	"dbx_get_object_source", "dbx_get_routine_source", "dbx_get_event_info",
}

func mcpObjectFixture(t *testing.T) *Server {
	t.Helper()
	s, _ := mcpFixture(t)
	for _, statement := range []string{
		"CREATE VIEW people_view AS SELECT id, name FROM people",
		"CREATE TRIGGER people_changed AFTER INSERT ON people BEGIN /*过程源码*/ UPDATE people SET name='trigger fired' WHERE id=NEW.id; END",
	} {
		if _, err := s.database.Handle(t.Context(), "/api/query/execute", map[string]any{"connectionId": "allowed", "sql": statement}); err != nil {
			t.Fatal(err)
		}
	}
	mcpAllow(t, s)
	return s
}

func objectMCPArgs(name string) map[string]any {
	args := map[string]any{"connection_id": "allowed", "database": "main", "schema": "main"}
	if name == "dbx_get_object_source" {
		args["name"], args["object_type"] = "people_changed", "TRIGGER"
	} else if name == "dbx_get_routine_source" {
		args["name"], args["routine_type"] = "not_supported", "PROCEDURE"
	} else if name == "dbx_get_event_info" {
		args["name"] = "disabled_event"
	}
	return args
}

func objectMCPPage(t *testing.T, result any) (map[string]any, []map[string]any) {
	t.Helper()
	page, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("Expected bounded object page, got %#v", result)
	}
	objects, ok := page["objects"].([]map[string]any)
	if !ok {
		t.Fatalf("Missing object rows: %#v", page)
	}
	return page, objects
}

func TestMCPObjectMetadataReadsNeverExecuteSource(t *testing.T) {
	s := mcpObjectFixture(t)
	page, objects := objectMCPPage(t, mcpCall(t, s, "dbx_list_objects", objectMCPArgs("dbx_list_objects")))
	if len(objects) != 3 || integer(page["total"], -1) != 3 {
		t.Fatalf("Table, view and trigger were not all listed: %#v", page)
	}
	args := objectMCPArgs("dbx_list_objects")
	args["object_types"] = []any{"TRIGGER"}
	page, objects = objectMCPPage(t, mcpCall(t, s, "dbx_list_objects", args))
	if len(objects) != 1 || objects[0]["name"] != "people_changed" || objects[0]["parent_name"] != "people" {
		t.Fatalf("Trigger identity missing: %#v", page)
	}
	args = objectMCPArgs("dbx_list_triggers")
	args["relation_name"] = "people"
	_, objects = objectMCPPage(t, mcpCall(t, s, "dbx_list_triggers", args))
	if len(objects) != 1 || objects[0]["object_type"] != "TRIGGER" {
		t.Fatal(objects)
	}
	args["relation_name"] = "people_view"
	_, objects = objectMCPPage(t, mcpCall(t, s, "dbx_list_triggers", args))
	if len(objects) != 0 {
		t.Fatal("Trigger relation filter ignored", objects)
	}
	args = objectMCPArgs("dbx_list_objects")
	args["limit"], args["offset"] = 1, 1
	page, objects = objectMCPPage(t, mcpCall(t, s, "dbx_list_objects", args))
	if len(objects) != 1 || integer(page["total"], -1) != 3 || integer(page["offset"], -1) != 1 || integer(page["limit"], -1) != 1 || page["truncated"] != true || integer(page["next_offset"], -1) != 2 {
		t.Fatalf("Object pagination lost bounds or total: %#v", page)
	}
	args["filter"] = "' OR 1=1 --"
	page, objects = objectMCPPage(t, mcpCall(t, s, "dbx_list_objects", args))
	if len(objects) != 0 || integer(page["total"], -1) != 0 {
		t.Fatalf("Object filter was interpreted as SQL: %#v", page)
	}
	source := mcpCall(t, s, "dbx_get_object_source", objectMCPArgs("dbx_get_object_source")).(map[string]any)
	if source["editable"] != false || source["source_truncated"] != false || !strings.Contains(str(source, "source"), "CREATE TRIGGER people_changed") {
		t.Fatalf("Trigger source unavailable: %#v", source)
	}
	args = objectMCPArgs("dbx_get_object_source")
	args["source_char_limit"] = 16
	first := mcpCall(t, s, "dbx_get_object_source", args).(map[string]any)
	if first["source_truncated"] != true || integer(first["next_source_char_offset"], -1) != 16 {
		t.Fatalf("Source window is not bounded: %#v", first)
	}
	args["source_char_offset"] = 16
	second := mcpCall(t, s, "dbx_get_object_source", args).(map[string]any)
	if str(first, "source")+str(second, "source") != str(source, "source")[:32] {
		t.Fatal("Source windows did not preserve offsets")
	}
	unicodeIndex := strings.Index(str(source, "source"), "过程源码")
	if unicodeIndex < 0 {
		t.Fatal("Chinese source comment disappeared")
	}
	charOffset := len([]rune(str(source, "source")[:unicodeIndex]))
	args["source_char_offset"], args["source_char_limit"] = charOffset, 2
	window := mcpCall(t, s, "dbx_get_object_source", args).(map[string]any)
	if window["source"] != "过程" || integer(window["next_source_char_offset"], -1) != charOffset+2 {
		t.Fatalf("Source cursor split multibyte Chinese characters: %#v", window)
	}
	for _, tool := range []string{"dbx_list_routines", "dbx_list_events", "dbx_get_routine_source", "dbx_get_event_info"} {
		if _, err := s.MCPBackend().Call(t.Context(), tool, objectMCPArgs(tool)); err == nil {
			t.Fatalf("SQLite falsely advertised support for %s", tool)
		}
	}
	rows := mcpCall(t, s, "dbx_execute_query", mcpArgs("SELECT id,name FROM people ORDER BY id")).(map[string]any)["rows"]
	if fmt.Sprint(rows) != "[[1 小古] [2 testing]]" {
		t.Fatalf("Reading source executed or changed the database: %#v", rows)
	}
}

func TestMCPObjectToolsRespectPolicyScopes(t *testing.T) {
	s := mcpObjectFixture(t)
	p := mcpAllow(t, s)
	p.ConnectionPolicies = []MCPConnectionPolicy{{ConnectionID: "allowed", DatabaseScope: "selected", AllowedDatabases: []string{"main"}}}
	if err := s.SaveMCPPolicy(p); err != nil {
		t.Fatal(err)
	}
	for _, name := range objectMCPTools {
		for _, selector := range []struct{ key, value string }{{"connection_id", "hidden"}, {"database", "other"}, {"schema", "temp"}} {
			args := objectMCPArgs(name)
			args[selector.key] = selector.value
			_, err := s.MCPBackend().Call(t.Context(), name, args)
			if err == nil || !strings.Contains(err.Error(), "not allowed") && !strings.Contains(err.Error(), "main database") {
				t.Fatalf("%s failed to enforce %s before object access: %v", name, selector.key, err)
			}
		}
	}
	p.AllowedToolNames = []string{"dbx_list_connections"}
	if err := s.SaveMCPPolicy(p); err != nil {
		t.Fatal(err)
	}
	for _, name := range objectMCPTools {
		_, err := s.MCPBackend().Call(t.Context(), name, objectMCPArgs(name))
		if err == nil || !strings.Contains(err.Error(), "Tool is not allowed") {
			t.Fatalf("%s bypassed tool allowlist: %v", name, err)
		}
	}
}

func TestMCPObjectSourceCursorAboveOneMillion(t *testing.T) {
	s := mcpObjectFixture(t)
	statement := "CREATE TRIGGER huge_source AFTER INSERT ON people BEGIN /*" + strings.Repeat("x", 1000050) + "*/ SELECT 1; END"
	if _, err := s.database.Handle(t.Context(), "/api/query/execute", map[string]any{"connectionId": "allowed", "sql": statement}); err != nil {
		t.Fatal(err)
	}
	args := objectMCPArgs("dbx_get_object_source")
	args["name"], args["source_char_offset"], args["source_char_limit"] = "huge_source", 999995, 16
	first := mcpCall(t, s, "dbx_get_object_source", args).(map[string]any)
	next := integer(first["next_source_char_offset"], -1)
	if first["source_truncated"] != true || next <= 1000000 {
		t.Fatalf("Large source did not return a resumable cursor: %#v", first)
	}
	args["source_char_offset"], args["source_char_limit"] = next, 65536
	if err := mcpCheckArgs("dbx_get_object_source", args); err != nil {
		t.Fatalf("Returned source cursor fails the tool's own input schema: %v", err)
	}
	last := mcpCall(t, s, "dbx_get_object_source", args).(map[string]any)
	if last["source_truncated"] != false || str(first, "source")+str(last, "source") != statement[999995:] {
		t.Fatal("Source paging above one million characters lost the remaining definition")
	}
	args["source_char_offset"] = 2147483647
	empty := mcpCall(t, s, "dbx_get_object_source", args).(map[string]any)
	if str(empty, "source") != "" || empty["source_truncated"] != false {
		t.Fatal("Maximum supported source cursor did not produce a safe empty window")
	}
	pageArgs := objectMCPArgs("dbx_list_objects")
	pageArgs["offset"], pageArgs["limit"] = 2147483647, 1000
	page, objects := objectMCPPage(t, mcpCall(t, s, "dbx_list_objects", pageArgs))
	if len(objects) != 0 || page["truncated"] != false || integer(page["total"], -1) != 4 {
		t.Fatalf("Maximum supported object cursor did not produce a safe empty page: %#v", page)
	}
}

func decodedMCPObjectResult(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || result.IsError || len(result.Content) != 1 {
		t.Fatalf("MCP %s: %#v %v", name, result, err)
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("MCP object result is not JSON text: %#v", result)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(content.Text), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestMCPObjectHTTPDiscoveryCallsAndStrictInput(t *testing.T) {
	s := mcpObjectFixture(t)
	host := httptest.NewServer(s)
	defer host.Close()
	endpoint, _ := url.Parse(host.URL)
	status, err := s.saveMCPWeb(MCPWebSettings{Enabled: true, AllowedHosts: []string{endpoint.Host}, AllowedOrigins: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	session, err := mcpbridge.ConnectHTTP(t.Context(), host.URL+"/mcp", str(status, "accessToken"))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, tool := range list.Tools {
		for _, required := range objectMCPTools {
			if tool.Name == required {
				if found[required] || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || tool.Description == "" {
					t.Fatalf("Object tool discovery lost read-only contract: %#v", tool)
				}
				found[required] = true
			}
		}
	}
	for _, name := range objectMCPTools {
		if !found[name] {
			t.Fatalf("%s missing from HTTP discovery", name)
		}
	}
	page := decodedMCPObjectResult(t, session, "dbx_list_triggers", objectMCPArgs("dbx_list_triggers"))
	objects := page["objects"].([]any)
	if len(objects) != 1 || objects[0].(map[string]any)["name"] != "people_changed" {
		t.Fatalf("HTTP lost trigger catalog: %#v", page)
	}
	source := decodedMCPObjectResult(t, session, "dbx_get_object_source", objectMCPArgs("dbx_get_object_source"))
	if !strings.Contains(str(source, "source"), "CREATE TRIGGER") || source["editable"] != false {
		t.Fatalf("HTTP lost source metadata: %#v", source)
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"dbx_get_object_source", map[string]any{"connection_id": "allowed", "object_type": "TRIGGER"}},
		{"dbx_get_event_info", map[string]any{"connection_id": "allowed"}},
		{"dbx_get_routine_source", map[string]any{"connection_id": "allowed", "name": "some_procedure", "routine_type": "EVENT"}},
		{"dbx_get_object_source", map[string]any{"connection_id": "allowed", "name": "people_changed", "object_type": "TRIGGER", "source_char_limit": 65537}},
		{"dbx_get_object_source", map[string]any{"connection_id": "allowed", "name": "people_changed", "object_type": "TRIGGER", "source_char_offset": -1}},
		{"dbx_list_objects", map[string]any{"connection_id": "allowed", "object_types": []any{"TRIGGER", "DROP TABLE"}}},
		{"dbx_list_objects", map[string]any{"connection_id": "allowed", "object_types": "TRIGGER"}},
		{"dbx_list_objects", map[string]any{"connection_id": "allowed", "object_types": []any{1}}},
		{"dbx_list_objects", map[string]any{"connection_id": "allowed", "limit": 1001}},
		{"dbx_list_objects", map[string]any{"connection_id": "allowed", "limit": "1"}},
		{"dbx_list_objects", map[string]any{"connection_id": "allowed", "offset": "0"}},
		{"dbx_get_object_source", map[string]any{"connection_id": "allowed", "name": "people_changed", "object_type": "TRIGGER", "source_char_offset": "0"}},
		{"dbx_list_objects", map[string]any{"connection_id": "allowed", "offset": -1}},
		{"dbx_list_routines", map[string]any{"connection_id": "allowed", "routine_type": "TABLE"}},
		{"dbx_list_events", map[string]any{"connection_id": "allowed", "sql": "DROP TABLE people"}},
		{"dbx_list_objects", map[string]any{"connection_id": "allowed", "schema": "main", "all_schemas": true}},
	} {
		// Both SDK schema rejection and a backend IsError are valid MCP failures.
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		if err == nil && (result == nil || !result.IsError) {
			t.Fatalf("Invalid object arguments accepted over HTTP: %s %#v -> %#v", tc.name, tc.args, result)
		}
		if _, err := s.MCPBackend().Call(t.Context(), tc.name, tc.args); err == nil {
			t.Fatalf("Invalid arguments bypassed direct-call validation: %s %#v", tc.name, tc.args)
		}
	}
	denied, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "dbx_get_object_source", Arguments: map[string]any{"connection_id": "hidden", "name": "people_changed", "object_type": "TRIGGER"}})
	if err != nil || !denied.IsError {
		t.Fatalf("HTTP revealed hidden object source: %#v %v", denied, err)
	}
}
