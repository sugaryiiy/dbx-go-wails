package mcpbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type testBackend struct {
	calls    atomic.Int64
	started  chan struct{}
	finished chan struct{}
}

func (*testBackend) Tools() []Tool {
	return []Tool{
		{Name: "echo", Description: "Echo JSON values", ReadOnly: true, InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"},
		}},
		{Name: "fail", ReadOnly: true, InputSchema: map[string]any{"type": "object"}},
		{Name: "wait", ReadOnly: true, InputSchema: map[string]any{"type": "object"}},
	}
}

func (b *testBackend) Call(ctx context.Context, name string, args map[string]any) (any, error) {
	b.calls.Add(1)
	switch name {
	case "echo":
		return map[string]any{"text": args["text"], "integer": json.Number("9007199254740993")}, nil
	case "fail":
		return nil, errors.New("Access denied by connection policy")
	case "wait":
		close(b.started)
		<-ctx.Done()
		close(b.finished)
		return nil, ctx.Err()
	}
	return nil, errors.New("Unknown test tool")
}

func inMemoryClient(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serverSession.Close() })
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func requireEcho(t *testing.T, session *mcp.ClientSession) {
	t.Helper()
	list, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 3 {
		t.Fatalf("Tools count = %d", len(list.Tools))
	}
	for _, tool := range list.Tools {
		if tool.Name == "echo" && (tool.Annotations == nil || !tool.Annotations.ReadOnlyHint) {
			t.Fatal("Read-only annotation was lost")
		}
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "中文 \"quoted\"\nline"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("Unexpected result: %#v", result)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "9007199254740993") || !strings.Contains(text, "中文") {
		t.Fatalf("JSON precision or text lost: %s", text)
	}
	denied, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "fail", Arguments: map[string]any{}})
	if err != nil || !denied.IsError {
		t.Fatalf("Expected tool IsError, got %v %v", denied, err)
	}
}

func TestSDKInMemoryRoundTrip(t *testing.T) {
	requireEcho(t, inMemoryClient(t, NewServer(&testBackend{})))
}

func TestStrictToolInput(t *testing.T) {
	backend := &testBackend{}
	client := inMemoryClient(t, NewServer(backend))
	for _, args := range []any{
		map[string]any{"text": "okay", "connection_password": "unexpected"},
		map[string]any{}, map[string]any{"text": 123}, []any{}, "wrong", json.RawMessage("null"),
	} {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo", Arguments: args})
		if err == nil && (result == nil || !result.IsError) {
			t.Fatalf("Invalid input accepted: %#v -> %#v", args, result)
		}
	}
	if backend.calls.Load() != 0 {
		t.Fatal("Invalid arguments reached backend")
	}
	if result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "missing", Arguments: map[string]any{}}); err == nil && !result.IsError {
		t.Fatal("Unknown tool succeeded")
	}
}

func TestSDKCancellationReachesBackend(t *testing.T) {
	for _, transport := range []string{"memory", "http", "http-2025"} {
		t.Run(transport, func(t *testing.T) {
			backend := &testBackend{started: make(chan struct{}), finished: make(chan struct{})}
			var client *mcp.ClientSession
			if transport == "memory" {
				client = inMemoryClient(t, NewServer(backend))
			} else {
				host := httptest.NewServer(HTTPHandler(backend))
				defer host.Close()
				var err error
				client, err = ConnectHTTP(t.Context(), host.URL, "cancel-test")
				if transport == "http-2025" {
					if client != nil {
						client.Close()
					}
					client, err = mcp.NewClient(&mcp.Implementation{Name: "legacy-test"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: host.URL, DisableStandaloneSSE: true, MaxRetries: -1}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-03-26"})
				}
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "wait", Arguments: map[string]any{}})
				result <- err
			}()
			select {
			case <-backend.started:
			case <-time.After(3 * time.Second):
				t.Fatal("Backend did not start")
			}
			cancel()
			select {
			case <-backend.finished:
			case <-time.After(3 * time.Second):
				t.Fatal("Backend did not receive cancellation")
			}
			if err := <-result; err == nil {
				t.Fatal("Canceled call succeeded")
			}
		})
	}
}

func TestLegacyHTTPPermissionRevocationCancelsBackend(t *testing.T) {
	backend := &testBackend{started: make(chan struct{}), finished: make(chan struct{})}
	handler := HTTPHandler(backend)
	permission, revoke := context.WithCancel(t.Context())
	defer revoke()
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		stop := context.AfterFunc(permission, cancel)
		defer stop()
		defer cancel()
		handler.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer host.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "legacy-revocation"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: host.URL, DisableStandaloneSSE: true, MaxRetries: -1}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-03-26"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result := make(chan struct{}, 1)
	go func() {
		_, _ = client.CallTool(t.Context(), &mcp.CallToolParams{Name: "wait", Arguments: map[string]any{}})
		result <- struct{}{}
	}()
	select {
	case <-backend.started:
	case <-time.After(3 * time.Second):
		t.Fatal("Backend did not start")
	}
	revoke()
	select {
	case <-backend.finished:
	case <-time.After(3 * time.Second):
		t.Fatal("Old protocol ignored permission revocation")
	}
	select {
	case <-result:
	case <-time.After(3 * time.Second):
		t.Fatal("Revoked HTTP request did not finish")
	}
}

func TestSDKStreamableHTTPAndProxy(t *testing.T) {
	endpoint := HTTPHandler(&testBackend{})
	var unauthenticated atomic.Int64
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			unauthenticated.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		endpoint.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	remote, err := ConnectHTTP(t.Context(), httpServer.URL+"/mcp", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	requireEcho(t, remote)
	proxy, err := proxyServer(t.Context(), remote)
	if err != nil {
		t.Fatal(err)
	}
	requireEcho(t, inMemoryClient(t, proxy))
	if unauthenticated.Load() != 0 {
		t.Fatal("Authorization missing from SDK request")
	}
	response, err := http.Get(httpServer.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("Unauthenticated request was accepted")
	}
}

func TestProxyRefusesInsecureURLsAndRedirects(t *testing.T) {
	for _, endpoint := range []string{"http://example.com/mcp", "https://user:password@example.com/mcp", "https://example.com/mcp?token=secret", "file:///tmp/mcp", "https://example.com/mcp#fragment"} {
		if _, err := ConnectHTTP(t.Context(), endpoint, "secret"); err == nil {
			t.Fatalf("Accepted %q", endpoint)
		}
	}
	var forwarded atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1); w.WriteHeader(500) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	if _, err := ConnectHTTP(t.Context(), redirect.URL, "secret"); err == nil {
		t.Fatal("Redirect unexpectedly succeeded")
	}
	if forwarded.Load() != 0 {
		t.Fatal("Token-bearing request followed redirect")
	}
}
