package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testServer(t *testing.T, dir string) *Server {
	t.Helper()
	s, err := New(Options{DataDir: dir, Password: "test-auth-password"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func call(s *Server, method, path string, payload any, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	var body bytes.Buffer
	if payload != nil {
		_ = json.NewEncoder(&body).Encode(payload)
	}
	r := httptest.NewRequest(method, path, &body)
	r.Host = "localhost:18080"
	if payload != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func loginTest(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	w := call(s, "POST", "/api/auth/login", map[string]any{"password": "test-auth-password"}, nil, "")
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	return w.Result().Cookies()[0]
}
func value(t *testing.T, w *httptest.ResponseRecorder) any {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("request failed: %d %s", w.Code, w.Body)
	}
	var result any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func TestAuthenticationAndOrigin(t *testing.T) {
	if _, err := New(Options{DataDir: t.TempDir()}); err == nil {
		t.Fatal("Web mode accepted an empty password")
	}
	s := testServer(t, t.TempDir())
	check := value(t, call(s, "GET", "/api/auth/check", nil, nil, "")).(map[string]any)
	if check["required"] != true || check["authenticated"] != false || check["setup_required"] != false {
		t.Fatal(check)
	}
	if got := call(s, "GET", "/api/connection/list", nil, nil, ""); got.Code != 401 {
		t.Fatalf("unauthenticated access: %d", got.Code)
	}
	cookie := loginTest(t, s)
	if got := call(s, "GET", "/api/query/execute?sql=DROP+TABLE+customers", nil, cookie, ""); got.Code != 405 {
		t.Fatal("query execution accepted GET")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe cookie: %+v", cookie)
	}
	if got := call(s, "POST", "/api/connection/save", map[string]any{"configs": []any{}}, cookie, "https://evil.example"); got.Code != 403 {
		t.Fatalf("cross-origin allowed: %d", got.Code)
	}
	check = value(t, call(s, "GET", "/api/auth/check", nil, cookie, "")).(map[string]any)
	if check["authenticated"] != true {
		t.Fatal(check)
	}
	value(t, call(s, "POST", "/api/auth/logout", nil, cookie, ""))
	if got := call(s, "GET", "/api/connection/list", nil, cookie, ""); got.Code != 401 {
		t.Fatal("logout retained session")
	}
}
func TestDesktopCannotBeServedOverNetwork(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Desktop: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := call(s, "GET", "/api/auth/check", nil, nil, ""); got.Code != 403 {
		t.Fatal("Desktop handler accepted a network request")
	}
	req := httptest.NewRequest("GET", "http://wails.localhost/api/auth/check", nil)
	req.RemoteAddr = ""
	req.Header.Set("Origin", "wails://wails.localhost")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("Wails request rejected %d %s", w.Code, w.Body)
	}
}
func TestEncryptedPersistenceMergeAndPasswordPolicy(t *testing.T) {
	dir := t.TempDir()
	s := testServer(t, dir)
	cookie := loginTest(t, s)
	save := func(config map[string]any) {
		t.Helper()
		value(t, call(s, "POST", "/api/connection/save", map[string]any{"configs": []any{config}}, cookie, ""))
	}
	save(map[string]any{"id": "one", "name": "One", "db_type": "mysql", "host": "localhost", "password": "secret-stored-123", "save_password": true})
	save(map[string]any{"id": "two", "name": "Two", "db_type": "postgresql", "password": "secret-not-stored-789", "ssh": map[string]any{"password": "nested-transient-secret"}, "save_password": false})
	connections := value(t, call(s, "GET", "/api/connection/list", nil, cookie, "")).([]any)
	if len(connections) != 2 {
		t.Fatalf("upsert dropped another client: %v", connections)
	}
	if connections[1].(map[string]any)["password"] != "" {
		t.Fatal("save_password false persisted password")
	}
	bytes, err := os.ReadFile(filepath.Join(dir, "state.enc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-stored-123", "secret-not-stored-789", "nested-transient-secret"} {
		if strings.Contains(string(bytes), secret) {
			t.Fatal("credential in plaintext")
		}
	}
	for _, name := range []string{"state.enc", "credentials.key"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe permissions: %s", name)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := testServer(t, dir)
	cookie2 := loginTest(t, s2)
	restored := value(t, call(s2, "GET", "/api/connection/list", nil, cookie2, "")).([]any)
	if len(restored) != 2 || restored[0].(map[string]any)["password"] != "secret-stored-123" {
		t.Fatal("encrypted restore failed")
	}
	value(t, call(s2, "POST", "/api/connection/save", map[string]any{"configs": []any{}, "removedIds": []any{"one"}}, cookie2, ""))
	restored = value(t, call(s2, "GET", "/api/connection/list", nil, cookie2, "")).([]any)
	if len(restored) != 1 || restored[0].(map[string]any)["id"] != "two" {
		t.Fatal("explicit removal failed")
	}
}
func TestStartupAndUnsupportedCapability(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	ready := value(t, call(s, "GET", "/api/ready", nil, cookie, "")).(map[string]any)
	if ready["backend"] != "go" {
		t.Fatal(ready)
	}
	migration := value(t, call(s, "GET", "/api/migration/status", nil, cookie, "")).(map[string]any)
	if migration["state"] != "not_required" || migration["needsMigration"] != false {
		t.Fatal(migration)
	}
	got := call(s, "POST", "/api/not-migrated", map[string]any{}, cookie, "")
	if got.Code != 501 {
		t.Fatalf("unknown capability: %d %s", got.Code, got.Body)
	}
	var envelope map[string]any
	_ = json.Unmarshal(got.Body.Bytes(), &envelope)
	if envelope["version"] != float64(1) || envelope["messageKey"] != "backendErrors.legacy" {
		t.Fatal(envelope)
	}
}
func TestSavedSQLHistoryAndSampleCRUD(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	sample := value(t, call(s, "POST", "/api/workspace/sample", map[string]any{}, cookie, "")).(map[string]any)
	config := sample["connection"].(map[string]any)
	id := config["id"]
	if connectedID := value(t, call(s, "POST", "/api/connection/connect", map[string]any{"config": config}, cookie, "")); connectedID != id {
		t.Fatalf("connection API returned %v, want id %v", connectedID, id)
	}
	query := func(sql string) map[string]any {
		t.Helper()
		return value(t, call(s, "POST", "/api/query/execute", map[string]any{"connectionId": id, "database": "main", "sql": sql}, cookie, "")).(map[string]any)
	}
	result := query("SELECT count(*) AS count FROM customers")
	if rows, ok := result["rows"].([]any); !ok || len(rows) != 1 {
		t.Fatal(result)
	}
	query("INSERT INTO customers(id,name,city) VALUES(99,'Go User','Shanghai')")
	query("UPDATE customers SET city='Hangzhou' WHERE id=99")
	result = query("SELECT city FROM customers WHERE id=99")
	if result["rows"].([]any)[0].([]any)[0] != "Hangzhou" {
		t.Fatal(result)
	}
	value(t, call(s, "POST", "/api/workspace/sample", map[string]any{}, cookie, ""))
	result = query("SELECT city FROM customers WHERE id=99")
	if len(result["rows"].([]any)) != 1 {
		t.Fatal("reopening sample overwrote edits")
	}
	query("DELETE FROM customers WHERE id=99")
	file := map[string]any{"id": "sql-1", "connectionId": id, "name": "customers.sql", "database": "main", "sql": "SELECT * FROM customers", "createdAt": "2026-10-05T00:00:00Z", "updatedAt": "2026-10-05T00:00:00Z"}
	value(t, call(s, "POST", "/api/saved-sql", file, cookie, ""))
	loaded := value(t, call(s, "GET", "/api/saved-sql/sql-1", nil, cookie, "")).(map[string]any)
	if loaded["sql"] != file["sql"] {
		t.Fatal(loaded)
	}
	value(t, call(s, "DELETE", "/api/saved-sql/sql-1", nil, cookie, ""))
	if value(t, call(s, "GET", "/api/saved-sql/sql-1", nil, cookie, "")) != nil {
		t.Fatal("SQL deletion failed")
	}
	for i := 0; i < 3; i++ {
		entry := map[string]any{"id": fmt.Sprint(i), "connection_id": id, "connection_name": "Sample", "database": "main", "sql": "SELECT customers", "executed_at": "2026-10-05T00:00:00Z", "execution_time_ms": 1, "success": true}
		value(t, call(s, "POST", "/api/history/save", map[string]any{"entry": entry}, cookie, ""))
	}
	search := map[string]any{"search_text": "customers", "limit": 2, "connections": []any{map[string]any{"connection_id": id}}, "databases": []any{}}
	page := value(t, call(s, "POST", "/api/history/search", search, cookie, "")).(map[string]any)
	if page["total"] != float64(3) || len(page["entries"].([]any)) != 2 || page["next_cursor"] == nil {
		t.Fatal(page)
	}
	search["cursor"] = page["next_cursor"]
	page = value(t, call(s, "POST", "/api/history/search", search, cookie, "")).(map[string]any)
	if len(page["entries"].([]any)) != 1 {
		t.Fatal("cursor skipped same-time entry", page)
	}
}

func TestDataDirectoryLockAndMissingKey(t *testing.T) {
	dir := t.TempDir()
	s := testServer(t, dir)
	if other, err := New(Options{DataDir: dir, Password: "test"}); err == nil {
		other.Close()
		t.Fatal("concurrent process could overwrite saved state")
	}
	cookie := loginTest(t, s)
	value(t, call(s, "POST", "/api/connection/save", map[string]any{"configs": []any{}}, cookie, ""))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "credentials.key")); err != nil {
		t.Fatal(err)
	}
	if other, err := New(Options{DataDir: dir, Password: "test"}); err == nil {
		other.Close()
		t.Fatal("missing encryption key was silently replaced")
	}
}

func TestDesktopOriginMatchesWailsPlatformOrigins(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Desktop: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct {
		origin string
		status int
	}{
		{"wails://wails", http.StatusOK},
		{"wails://wails.localhost", http.StatusOK},
		{"http://wails.localhost", http.StatusOK},
		{"https://wails.localhost", http.StatusOK},
		{"http://wails", http.StatusForbidden},
		{"https://wails", http.StatusForbidden},
		{"wails://wails.evil.example", http.StatusForbidden},
		{"wails://wails:443", http.StatusForbidden},
		{"wails://wails/path", http.StatusForbidden},
		{"https://evil.example", http.StatusForbidden},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			r := httptest.NewRequest("GET", "wails://wails/api/auth/check", nil)
			r.RemoteAddr = ""
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("origin %q: got %d, want %d", tc.origin, w.Code, tc.status)
			}
		})
	}
	web := testServer(t, t.TempDir())
	if got := call(web, "GET", "/api/auth/check", nil, nil, "wails://wails"); got.Code != http.StatusForbidden {
		t.Fatal("Web server accepted a desktop origin")
	}
}
