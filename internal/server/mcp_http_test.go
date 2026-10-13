package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const initializeMCPBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"dbx-mcp-test","version":"1"}}}`

func mcpManagement(s *Server, method, path string, payload any, cookie *http.Cookie, desktop bool) *httptest.ResponseRecorder {
	data, _ := json.Marshal(payload)
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.Host = "localhost:18080"
	r.Header.Set("X-DBX-MCP-Settings", "1")
	r.Header.Set("Content-Type", "application/json")
	if desktop {
		r.RemoteAddr = ""
		r.Header.Set("Origin", "wails://wails")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func mcpInitialize(s *Server, token, host, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/mcp", strings.NewReader(initializeMCPBody))
	r.Host = host
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func configureEmptyMCPPolicy(t *testing.T, s *Server) {
	t.Helper()
	if err := s.SaveMCPPolicy(defaultMCPPolicy()); err != nil {
		t.Fatal(err)
	}
}
func TestMCPWebAuthenticationRotationAndPersistence(t *testing.T) {
	dir := t.TempDir()
	s := testServer(t, dir)
	cookie := loginTest(t, s)
	if w := mcpInitialize(s, "", "localhost:18080", ""); w.Code != 404 {
		t.Fatalf("MCP must be disabled initially: %d", w.Code)
	}
	if w := mcpManagement(s, "GET", "/api/app-settings/mcp-http-status", nil, nil, false); w.Code != 401 {
		t.Fatal("anonymous management accepted")
	}
	settings := MCPWebSettings{Enabled: true, AllowedHosts: []string{"localhost:18080"}, AllowedOrigins: []string{}}
	if w := mcpManagement(s, "PUT", "/api/app-settings/mcp-http", settings, cookie, false); w.Code != 400 {
		t.Fatal("enabled before access policy was configured")
	}
	configureEmptyMCPPolicy(t, s)
	if w := call(s, "PUT", "/api/app-settings/mcp-http", settings, cookie, ""); w.Code != 403 {
		t.Fatal("management accepted without explicit header")
	}
	status := value(t, mcpManagement(s, "PUT", "/api/app-settings/mcp-http", settings, cookie, false)).(map[string]any)
	token := status["accessToken"].(string)
	if !validMCPToken(token) {
		t.Fatal("invalid generated token")
	}
	for _, tc := range []struct {
		token, host, origin string
		status              int
	}{
		{"", "localhost:18080", "", 401},
		{"invalid", "localhost:18080", "", 401},
		{token, "evil.example", "", 403},
		{token, "localhost:18080", "https://evil.example", 403},
		{token, "localhost:18080", "", 200},
		{token, "localhost:18080", "http://localhost:18080", 200},
	} {
		w := mcpInitialize(s, tc.token, tc.host, tc.origin)
		if w.Code != tc.status {
			t.Fatalf("MCP response %d, expected %d: %s", w.Code, tc.status, w.Body)
		}
		if w.Code == 200 && !strings.Contains(w.Body.String(), `"serverInfo"`) {
			t.Fatal("not a real MCP initialize response")
		}
	}
	rotated := value(t, mcpManagement(s, "POST", "/api/app-settings/mcp-http/rotate-token", nil, cookie, false)).(map[string]any)["accessToken"].(string)
	if rotated == token {
		t.Fatal("rotation kept the same token")
	}
	if w := mcpInitialize(s, token, "localhost:18080", ""); w.Code != 401 {
		t.Fatal("old token still accepted")
	}
	if w := mcpInitialize(s, rotated, "localhost:18080", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	encrypted, err := os.ReadFile(filepath.Join(dir, "state.enc"))
	if err != nil || bytes.Contains(encrypted, []byte(rotated)) {
		t.Fatal("token not encrypted at rest", err)
	}
	_ = s.Close()
	reopened := testServer(t, dir)
	if w := mcpInitialize(reopened, rotated, "localhost:18080", ""); w.Code != 200 {
		t.Fatal("persisted MCP settings failed", w.Code, w.Body)
	}
	if _, err = reopened.saveMCPWeb(MCPWebSettings{AllowedHosts: []string{}, AllowedOrigins: []string{}}); err != nil {
		t.Fatal(err)
	}
	if w := mcpInitialize(reopened, rotated, "localhost:18080", ""); w.Code != 404 {
		t.Fatal("disabled endpoint remained available")
	}
}
func TestMCPCORSAndExactAllowLists(t *testing.T) {
	s := testServer(t, t.TempDir())
	configureEmptyMCPPolicy(t, s)
	status, err := s.saveMCPWeb(MCPWebSettings{Enabled: true, AllowedHosts: []string{"dbx.example"}, AllowedOrigins: []string{"https://client.example"}})
	if err != nil {
		t.Fatal(err)
	}
	token := status["accessToken"].(string)
	if w := mcpInitialize(s, token, "dbx.example", "https://client.example"); w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != "https://client.example" {
		t.Fatal(w.Code, w.Body)
	}
	if w := mcpInitialize(s, token, "dbx.example:443", "https://client.example"); w.Code != 403 {
		t.Fatal("host authority match was not exact")
	}
	for _, values := range [][]string{{"*"}, {"https://domain.example/"}, {"null"}, {"https://user:password@domain.example"}} {
		if err := validateMCPLists(nil, values); err == nil {
			t.Fatal("accepted unsafe origin", values)
		}
	}
	for _, value := range []string{"*", "user:password@localhost", "example.com/path", "example.com?x=1"} {
		if err := validateMCPLists([]string{value}, nil); err == nil {
			t.Fatal("accepted unsafe host")
		}
	}
	r := httptest.NewRequest("OPTIONS", "/mcp", nil)
	r.Host = "dbx.example"
	r.Header.Set("Origin", "https://client.example")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 204 || strings.Contains(w.Header().Get("Access-Control-Allow-Origin"), "*") {
		t.Fatal("invalid CORS preflight")
	}
}
func TestMCPDesktopListenerLifecycleAndIsolation(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Options{DataDir: dir, Desktop: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	configureEmptyMCPPolicy(t, s)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, rawPort, _ := net.SplitHostPort(occupied.Addr().String())
	port, _ := strconv.Atoi(rawPort)
	settings := defaultMCPHTTPSettings()
	settings.Enabled = true
	settings.Port = port
	if _, err := s.saveMCPDesktop(settings); err == nil {
		t.Fatal("accepted occupied listener address")
	}
	s.mcpState.mu.Lock()
	oldEnabled := s.mcpState.settings.Enabled
	s.mcpState.mu.Unlock()
	if oldEnabled {
		t.Fatal("failed listener startup changed persisted enabled setting")
	}
	_ = occupied.Close()
	status, err := s.saveMCPDesktop(settings)
	if err != nil {
		t.Fatal(err)
	}
	if status["running"] != true {
		t.Fatal(status)
	}
	endpoint := status["endpoint"].(string)
	token := status["accessToken"].(string)
	client := &http.Client{Timeout: 3 * time.Second}
	request := func(path string) int {
		t.Helper()
		r, _ := http.NewRequest("POST", path, strings.NewReader(initializeMCPBody))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if status := request(endpoint); status != 200 {
		t.Fatal(status)
	}
	if status := request(strings.TrimSuffix(endpoint, "/mcp") + "/api/connection/list"); status != 404 {
		t.Fatal("MCP port exposed desktop management API")
	}
	invalid := settings
	invalid.Host = "0.0.0.0"
	if _, err := s.saveMCPDesktop(invalid); err == nil {
		t.Fatal("desktop allowed external listener")
	}
	if status := request(endpoint); status != 200 {
		t.Fatal("invalid update stopped prior listener")
	}
	_ = s.Close()
	reopened, err := New(Options{DataDir: dir, Desktop: true})
	if err != nil {
		t.Fatal(err)
	}
	if status := request(endpoint); status != 200 {
		t.Fatal("listener was not restored")
	}
	_ = reopened.Close()
	standalone, err := New(Options{DataDir: dir, Desktop: true, DisableMCPListener: true})
	if err != nil {
		t.Fatal(err)
	}
	defer standalone.Close()
	if standalone.mcpState.httpServer != nil {
		t.Fatal("stdio restored desktop HTTP listener")
	}
	connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatal("closed MCP port still listening")
	}
}
