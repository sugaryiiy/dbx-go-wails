package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sugaryiiy/magpiedb/internal/mcpbridge"
)

// A canceled database operation may still need to close its connection and
// persist an audit. Keep that finalization gated to make shutdown ordering
// observable without timing a real query or disk write.
type shutdownMCPBackend struct {
	server                     *Server
	started, canceled, release chan struct{}
}

func (b *shutdownMCPBackend) Tools() []mcpbridge.Tool { return b.server.MCPBackend().Tools() }
func (b *shutdownMCPBackend) Call(ctx context.Context, name string, args map[string]any) (result any, err error) {
	start := time.Now()
	defer func() {
		if e := b.server.mcpAudit(name, args, mcpScope{}, start, result, err); e != nil {
			err = e
		}
	}()
	close(b.started)
	<-ctx.Done()
	close(b.canceled)
	<-b.release
	return nil, ctx.Err()
}

func TestMCPCloseWaitsForAuditAndRetainsStoreLock(t *testing.T) {
	dir := t.TempDir()
	app := testServer(t, dir)
	configureEmptyMCPPolicy(t, app)
	backend := &shutdownMCPBackend{server: app, started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(backend.release) })
	app.mcpState.mu.Lock()
	app.mcpState.handler = mcpbridge.HTTPHandler(backend)
	app.mcpState.mu.Unlock()
	status, err := app.saveMCPWeb(MCPWebSettings{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(app)
	defer func() { release.Do(func() { close(backend.release) }); host.Close() }()
	client, err := mcpbridge.ConnectHTTP(t.Context(), host.URL+"/mcp", status["accessToken"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release.Do(func() { close(backend.release) }); client.Close() }()
	called := make(chan struct{})
	go func() {
		_, _ = client.CallTool(t.Context(), &mcp.CallToolParams{Name: "dbx_list_connections", Arguments: map[string]any{}})
		close(called)
	}()
	select {
	case <-backend.started:
	case <-time.After(3 * time.Second):
		t.Fatal("MCP operation did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- app.Close() }()
	select {
	case <-backend.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not cancel MCP operation")
	}
	select {
	case err := <-closed:
		t.Fatalf("Close returned before MCP finalization: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	other, err := New(Options{DataDir: dir, Password: "test-password"})
	if err == nil {
		other.Close()
		t.Fatal("Shutdown released the data lock before the MCP audit completed")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("Unexpected store lock error: %v", err)
	}
	release.Do(func() { close(backend.release) })
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not finish after MCP finalization")
	}
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("MCP request did not complete")
	}
	reopened, err := New(Options{DataDir: dir, Password: "test-password"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	history := reopened.snapshot().History
	if len(history) != 1 || history[0]["mcp_tool_name"] != "dbx_list_connections" {
		t.Fatalf("Audit was not saved before store unlock: %#v", history)
	}
}

func TestMCPApprovedProxyHostOverTCP(t *testing.T) {
	app := testServer(t, t.TempDir())
	configureEmptyMCPPolicy(t, app)
	status, err := app.saveMCPWeb(MCPWebSettings{Enabled: true, AllowedHosts: []string{"dbx.example"}, AllowedOrigins: []string{"https://client.example"}})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(app)
	defer host.Close()
	for _, tc := range []struct {
		host, origin string
		want         int
	}{
		{"dbx.example", "", 200}, {"dbx.example", "https://client.example", 200},
		{"unapproved.example", "", 403}, {"127.0.0.1", "", 403}, {"dbx.example", "https://evil.example", 403},
	} {
		r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, host.URL+"/mcp", strings.NewReader(initializeMCPBody))
		if err != nil {
			t.Fatal(err)
		}
		r.Host = tc.host
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("Authorization", "Bearer "+status["accessToken"].(string))
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		response, err := host.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != tc.want {
			t.Fatalf("Host=%s Origin=%s got %d want %d: %s", tc.host, tc.origin, response.StatusCode, tc.want, body)
		}
	}
}
