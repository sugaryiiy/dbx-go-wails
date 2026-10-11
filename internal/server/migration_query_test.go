package server

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

func TestWebSubpathAndScopedSessionCookie(t *testing.T) {
	s, e := New(Options{DataDir: t.TempDir(), Password: "test-auth-password", BasePath: "/tools/dbx", Assets: fstest.MapFS{"index.html": {Data: []byte("<html>dbx</html>")}}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if w := call(s, "GET", "/api/auth/check", nil, nil, ""); w.Code != 404 {
		t.Fatal("unmounted API exposed", w.Code)
	}
	w := call(s, "POST", "/tools/dbx/api/auth/login", map[string]any{"password": "test-auth-password"}, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Path != "/tools/dbx/" {
		t.Fatal("cookie escaped Web mount", cookie.Path)
	}
	if w = call(s, "GET", "/tools/dbx/api/health", nil, cookie, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w = call(s, "GET", "/tools/dbx", nil, cookie, ""); w.Code != 307 || w.Header().Get("Location") != "/tools/dbx/" {
		t.Fatal("wrong mount redirect", w.Code, w.Header())
	}
	if w = call(s, "GET", "/tools/dbx/", nil, cookie, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w = call(s, "POST", "/tools/dbx/api/connection/save", map[string]any{"configs": []any{}}, cookie, "https://evil.example"); w.Code != 403 {
		t.Fatal("subpath lost Origin protection")
	}
}
func uploadSQL(t *testing.T, s *Server, cookie *http.Cookie, source string) map[string]any {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	f, e := form.CreateFormFile("file", "restore.sql")
	if e != nil {
		t.Fatal(e)
	}
	f.Write([]byte(source))
	form.Close()
	r := httptest.NewRequest("POST", "/api/sql-file/preview", &body)
	r.Host = "localhost:18080"
	r.Header.Set("Content-Type", form.FormDataContentType())
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return value(t, w).(map[string]any)
}
func waitSQLJob(t *testing.T, s *Server, id string) map[string]any {
	t.Helper()
	s.extensions.mu.Lock()
	j := s.extensions.jobs["sql:"+id]
	s.extensions.mu.Unlock()
	if j == nil {
		t.Fatal("SQL job not created")
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		j.mu.Lock()
		done := j.done
		changed := j.changed
		progress := cloneMap(j.progress)
		j.mu.Unlock()
		if done {
			return progress
		}
		select {
		case <-changed:
		case <-deadline.C:
			t.Fatal("SQL execution did not complete")
		}
	}
}
func TestSQLFileUploadExecutionManualRollbackAndCancellationContract(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	config := map[string]any{"id": "file-db", "db_type": "sqlite", "host": filepath.Join(t.TempDir(), "test.sqlite"), "database": "main"}
	value(t, call(s, "POST", "/api/connection/connect", map[string]any{"config": config}, cookie, ""))
	preview := uploadSQL(t, s, cookie, "CREATE TABLE entries(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO entries VALUES(1,'kept');")
	value(t, call(s, "POST", "/api/sql-file/execute", map[string]any{"request": map[string]any{"executionId": "sql-a", "connectionId": "file-db", "database": "main", "filePath": preview["filePath"]}}, cookie, ""))
	if p := waitSQLJob(t, s, "sql-a"); p["status"] != "done" || p["successCount"] != float64(2) {
		t.Fatal(p)
	}
	txn := value(t, call(s, "POST", "/api/transaction/begin", map[string]any{"connectionId": "file-db", "database": "main"}, cookie, "")).(string)
	preview = uploadSQL(t, s, cookie, "INSERT INTO entries VALUES(2,'rollback');")
	value(t, call(s, "POST", "/api/sql-file/execute", map[string]any{"request": map[string]any{"executionId": "sql-b", "connectionId": "file-db", "database": "main", "filePath": preview["filePath"], "txnSessionId": txn}}, cookie, ""))
	if p := waitSQLJob(t, s, "sql-b"); p["status"] != "done" {
		t.Fatal(p)
	}
	value(t, call(s, "POST", "/api/transaction/rollback", map[string]any{"txnSessionId": txn}, cookie, ""))
	result, e := s.database.ExecuteSQL(context.Background(), "file-db", "main", "SELECT * FROM entries", 10)
	if e != nil || len(result.Rows) != 1 {
		t.Fatal(result, e)
	}
	value(t, call(s, "POST", "/api/sql-file/preview/release", map[string]any{"cleanupToken": preview["cleanupToken"]}, cookie, ""))
	if w := call(s, "POST", "/api/sql-file/tables", map[string]any{"filePath": preview["filePath"]}, cookie, ""); w.Code != 400 {
		t.Fatal("released source remained readable", w.Code)
	}
}

func TestSQLFileTargetIdentityIsNotSplitInsideQuotes(t *testing.T) {
	for _, tc := range []struct{ sql, namespace, table string }{
		{`-- a comment
CREATE TABLE "db.one"."table.two"(id INTEGER)`, "db.one", "table.two"},
		{`INSERT INTO "quote""name" VALUES(1)`, "", `quote"name`},
		{`UPDATE [main].[a.b] SET id=2`, "main", "a.b"},
	} {
		got := sqlFileTable(tc.sql)
		if got == nil || str(got, "database") != tc.namespace || str(got, "name") != tc.table {
			t.Fatal(tc.sql, got)
		}
	}
	for _, source := range []string{`DROP TABLE a,b`, `DROP TABLE a /* gap */,b`, `UPDATE a JOIN b ON a.id=b.id SET b.id=1`, `DELETE FROM a USING b WHERE a.id=b.id`, `SELECT * FROM a`} {
		if got := sqlFileTable(source); got != nil {
			t.Fatal("ambiguous filter target", source, got)
		}
	}
	if sqlFileTableKey(map[string]any{"database": "first", "name": "same"}) == sqlFileTableKey(map[string]any{"database": "second", "name": "same"}) {
		t.Fatal("namespaces were collapsed")
	}
}
