package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sugaryiiy/magpiedb/internal/mcpbridge"
	"github.com/sugaryiiy/magpiedb/internal/server"
)

// The helper executes the actual CLI entrypoint in a subprocess so stdout
// contamination, EOF cleanup, and process-level store locks are exercised.
func TestCLIProcess(t *testing.T) {
	if os.Getenv("DBX_MCP_TEST_PROCESS") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("DBX_MCP_TEST_ARGS")), &args); err != nil {
		os.Exit(2)
	}
	os.Args = append([]string{"dbx-mcp-go"}, args...)
	main()
	os.Exit(0)
}

func command(t *testing.T, args []string, token string, stderr *bytes.Buffer) *exec.Cmd {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCLIProcess$")
	// CI/release smoke can point at the packaged executable; ordinary tests
	// exercise the identical main entrypoint using the helper above.
	if binary := os.Getenv("DBX_MCP_TEST_BINARY"); binary != "" {
		cmd = exec.CommandContext(t.Context(), binary, args...)
	}
	// Do not inherit the developer's real MCP token into a child test process.
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "DBX_MCP_TOKEN=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "DBX_MCP_TEST_PROCESS=1", "DBX_MCP_TEST_ARGS="+string(encoded), "DBX_MCP_TOKEN="+token)
	cmd.Stderr = stderr
	return cmd
}

func childClient(t *testing.T, args []string, token string) (*mcp.ClientSession, *bytes.Buffer) {
	t.Helper()
	stderr := new(bytes.Buffer)
	client := mcp.NewClient(&mcp.Implementation{Name: "cli-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.CommandTransport{Command: command(t, args, token, stderr), TerminateDuration: 3 * time.Second}, nil)
	if err != nil {
		t.Fatalf("Connect CLI: %v; stderr: %s", err, stderr.String())
	}
	t.Cleanup(func() { session.Close() })
	return session, stderr
}

func TestLocalCLIStoreLockAndEOFCleanup(t *testing.T) {
	dir := t.TempDir()
	app, err := server.New(server.Options{DataDir: dir, Desktop: true, DisableMCPListener: true})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/workspace/sample", strings.NewReader("{}"))
	request.RemoteAddr = "" // The same in-process transport used by Wails.
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Seed sample: %s", response.Body)
	}
	var sample struct {
		Connection struct {
			ID string `json:"id"`
		} `json:"connection"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &sample); err != nil || sample.Connection.ID == "" {
		t.Fatalf("Sample connection: %s", response.Body)
	}
	var sampleConfig struct {
		Connection map[string]any `json:"connection"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &sampleConfig); err != nil {
		t.Fatal(err)
	}
	connectionRequest, _ := json.Marshal(map[string]any{"config": sampleConfig.Connection})
	request = httptest.NewRequest(http.MethodPost, "/api/connection/connect", bytes.NewReader(connectionRequest))
	request.RemoteAddr = ""
	response = httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Open sample for trigger fixture: %s", response.Body)
	}
	triggerSQL := "CREATE TRIGGER customers_mcp_changed AFTER INSERT ON customers BEGIN SELECT 1; END"
	triggerRequest, _ := json.Marshal(map[string]any{"connectionId": sample.Connection.ID, "database": "main", "sql": triggerSQL})
	request = httptest.NewRequest(http.MethodPost, "/api/query/execute", bytes.NewReader(triggerRequest))
	request.RemoteAddr = ""
	response = httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Seed trigger for source inspection: %s", response.Body)
	}
	if err := app.SaveMCPPolicy(server.MCPPolicy{Configured: true, ReadOnly: true, AllowedConnectionIDs: []string{sample.Connection.ID}, AllowedGroupIDs: []string{}}); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	session, stderr := childClient(t, []string{"--data-dir", dir}, "")
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) == 0 {
		t.Fatalf("List tools: %v %v", tools, err)
	}
	metadataTools := map[string]bool{
		"dbx_list_objects": false, "dbx_list_routines": false, "dbx_list_events": false,
		"dbx_list_triggers": false, "dbx_get_object_source": false,
		"dbx_get_routine_source": false, "dbx_get_event_info": false,
	}
	for _, tool := range tools.Tools {
		if tool.Name == "dbx_execute_query" && (tool.Annotations == nil || tool.Annotations.ReadOnlyHint) {
			t.Fatal("SQL execution incorrectly advertised read-only")
		}
		if tool.Name == "dbx_list_tables" && (tool.Annotations == nil || !tool.Annotations.ReadOnlyHint) {
			t.Fatal("Metadata did not advertise read-only")
		}
		if _, wanted := metadataTools[tool.Name]; wanted {
			if metadataTools[tool.Name] || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Fatalf("CLI object metadata contract missing or duplicated: %#v", tool)
			}
			metadataTools[tool.Name] = true
		}
	}
	for name, found := range metadataTools {
		if !found {
			t.Fatalf("CLI discovery lacks %s", name)
		}
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "dbx_execute_query", Arguments: map[string]any{"connection_id": sample.Connection.ID, "database": "main", "sql": "SELECT name FROM customers ORDER BY id LIMIT 1"}})
	if err != nil || result.IsError {
		t.Fatalf("Real SQLite CLI query: %v %v", result, err)
	}
	if len(result.Content) != 1 || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "rows") {
		t.Fatalf("Missing query rows: %#v", result)
	}
	listed, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "dbx_list_triggers", Arguments: map[string]any{"connection_id": sample.Connection.ID, "database": "main", "relation_name": "customers"}})
	if err != nil || listed.IsError || len(listed.Content) != 1 || !strings.Contains(listed.Content[0].(*mcp.TextContent).Text, "customers_mcp_changed") {
		t.Fatalf("CLI trigger catalog lost: %#v %v", listed, err)
	}
	source, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "dbx_get_object_source", Arguments: map[string]any{"connection_id": sample.Connection.ID, "database": "main", "name": "customers_mcp_changed", "object_type": "TRIGGER"}})
	if err != nil || source.IsError || len(source.Content) != 1 || !strings.Contains(source.Content[0].(*mcp.TextContent).Text, triggerSQL) {
		t.Fatalf("CLI trigger source lost: %#v %v", source, err)
	}
	denied, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "dbx_execute_query", Arguments: map[string]any{"connection_id": sample.Connection.ID, "database": "main", "sql": "DELETE FROM customers"}})
	if err != nil || !denied.IsError {
		t.Fatalf("CLI bypassed read-only policy: %v %v", denied, err)
	}
	if app, err := server.New(server.Options{DataDir: dir, Desktop: true, DisableMCPListener: true}); err == nil {
		app.Close()
		t.Fatal("Another process opened the CLI's locked store")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("CLI EOF: %v (%s)", err, stderr.String())
	}
	app, err = server.New(server.Options{DataDir: dir, Desktop: true, DisableMCPListener: true})
	if err != nil {
		t.Fatalf("CLI did not release store on EOF: %v", err)
	}
	app.Close()
}

type remoteTestBackend struct{}

func (remoteTestBackend) Tools() []mcpbridge.Tool {
	return []mcpbridge.Tool{{Name: "echo", ReadOnly: true, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}}}}
}

func (remoteTestBackend) Call(ctx context.Context, name string, args map[string]any) (any, error) {
	if args["text"] == "deny" {
		return nil, errors.New("Access denied")
	}
	return args, nil
}

func TestHTTPToStdioCLI(t *testing.T) {
	handler := mcpbridge.HTTPHandler(remoteTestBackend{})
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer subprocess-token" {
			http.Error(w, "unauthorized", 401)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	session, _ := childClient(t, []string{"--url", endpoint.URL + "/mcp"}, "subprocess-token")
	for _, value := range []string{"Go MCP 中文", "deny"} {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": value}})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError != (value == "deny") {
			t.Fatalf("Incorrect result forwarding: %#v", result)
		}
		if value != "deny" && !strings.Contains(result.Content[0].(*mcp.TextContent).Text, value) {
			t.Fatal("Proxy result lost text")
		}
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCLIRejectsConflictingModesAndRedactsToken(t *testing.T) {
	var stderr bytes.Buffer
	if err := run(t.Context(), []string{"--url", "http://127.0.0.1/mcp", "--data-dir", t.TempDir()}, &stderr); err == nil {
		t.Fatal("Conflicting modes accepted")
	}
	token := "secret-not-in-diagnostics"
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rejected "+token, http.StatusUnauthorized)
	}))
	defer endpoint.Close()
	cmd := command(t, []string{"--url", endpoint.URL + "/mcp"}, token, &stderr)
	stdout := new(bytes.Buffer)
	cmd.Stdout = stdout
	if err := cmd.Run(); err == nil {
		t.Fatal("Unauthorized CLI unexpectedly succeeded")
	}
	if stdout.Len() != 0 {
		t.Fatalf("Diagnostics corrupted protocol stdout: %s", stdout)
	}
	if strings.Contains(stderr.String(), token) {
		t.Fatal("Token was leaked to diagnostics")
	}
	if !strings.Contains(stderr.String(), "dbx-mcp-go:") {
		t.Fatalf("Missing diagnostic: %s", stderr.String())
	}
}
