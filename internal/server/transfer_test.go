package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestCSVAndXLSXRoundTripPreservesValues(t *testing.T) {
	original := exportResult{Columns: []string{"id", "name", "amount", "note"}, Rows: [][]any{{json.Number("9007199254740993"), "中文,\"换行\n测试", json.Number("12.500000000000000001"), nil}, {json.Number("2"), "=1+1", json.Number("-2.75"), ""}}}
	for _, format := range []string{"csv", "xlsx"} {
		t.Run(format, func(t *testing.T) {
			data, _, err := encodeTransfer(original, format, map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			columns, rows, _, err := parseTransferFile(data, format, map[string]any{"hasHeader": true})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(columns, original.Columns) {
				t.Fatal(columns)
			}
			expected := [][]any{{"9007199254740993", "中文,\"换行\n测试", "12.500000000000000001", nil}, {"2", "=1+1", "-2.75", ""}}
			if !reflect.DeepEqual(rows, expected) {
				t.Fatalf("roundtrip mismatch %#v", rows)
			}
		})
	}
}
func TestPreviewRejectsAmbiguousOrInvalidFiles(t *testing.T) {
	for _, body := range []string{"a,a\n1,2", "a,b\n1,2,3", "a,b\n\"unclosed"} {
		if _, _, _, err := parseTransferFile([]byte(body), "csv", nil); err == nil {
			t.Fatalf("accepted malformed file %q", body)
		}
	}
	if _, _, _, err := parseTransferFile([]byte("x\n1"), "csv", map[string]any{"encoding": "gbk"}); err == nil {
		t.Fatal("silently accepted unsupported encoding")
	}
}
func TestHTTPExportReplayAndParameterizedImport(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	config := map[string]any{"id": "transfer", "name": "Test", "db_type": "sqlite", "host": filepath.Join(t.TempDir(), "test.sqlite"), "database": "main"}
	value(t, call(s, "POST", "/api/connection/connect", map[string]any{"config": config}, cookie, ""))
	query := func(sql string) any {
		return value(t, call(s, "POST", "/api/query/execute", map[string]any{"connectionId": "transfer", "database": "main", "sql": sql}, cookie, ""))
	}
	query(`CREATE TABLE target (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`)
	query(`INSERT INTO target VALUES (1, 'before')`)
	value(t, call(s, "POST", "/api/export/query-result", map[string]any{"request": map[string]any{"exportId": "export-one", "connectionId": "transfer", "database": "main", "sql": "SELECT * FROM target", "format": "csv", "filePath": "result.csv"}}, cookie, ""))
	waitJob := func(path string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			result := value(t, call(s, "GET", path+"?poll=1", nil, cookie, "")).(map[string]any)
			if result["status"] != "running" {
				if result["status"] != "done" {
					t.Fatalf("transfer failed: %#v", result)
				}
				return result
			}
			if time.Now().After(deadline) {
				t.Fatal("transfer timed out")
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitJob("/api/export/query-result/progress/export-one")
	// Polling a completed job replays its terminal state for the Wails transport.
	if got := waitJob("/api/export/query-result/progress/export-one"); got["rowsExported"] != float64(1) {
		t.Fatal(got)
	}
	download := call(s, "GET", "/api/export/query-result/download/export-one", nil, cookie, "")
	if download.Code != 200 || !bytes.Contains(download.Body.Bytes(), []byte("before")) {
		t.Fatal(download.Code, download.Body)
	}
	var upload bytes.Buffer
	form := multipart.NewWriter(&upload)
	file, _ := form.CreateFormFile("file", "import.csv")
	_, _ = file.Write([]byte("id,name\n2,\"x'); DROP TABLE target; --\"\n"))
	_ = form.WriteField("sourceFormat", "csv")
	_ = form.Close()
	req := httptest.NewRequest("POST", "/api/import/preview", &upload)
	req.Host = "localhost:18080"
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.AddCookie(cookie)
	response := httptest.NewRecorder()
	s.ServeHTTP(response, req)
	preview := value(t, response).(map[string]any)
	value(t, call(s, "POST", "/api/import/execute", map[string]any{"request": map[string]any{"importId": "import-one", "connectionId": "transfer", "database": "main", "table": "target", "sourceRef": preview["sourceRef"], "mode": "append", "mappings": []any{map[string]any{"sourceColumn": "id", "targetColumn": "id"}, map[string]any{"sourceColumn": "name", "targetColumn": "name"}}}}, cookie, ""))
	waitJob("/api/import/progress/import-one")
	result := query("SELECT name FROM target WHERE id=2").(map[string]any)
	if result["rows"].([]any)[0].([]any)[0] != "x'); DROP TABLE target; --" {
		t.Fatal(result)
	}
}

func TestExportCannotReplayWrites(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	config := map[string]any{"id": "export-readonly", "db_type": "sqlite", "host": filepath.Join(t.TempDir(), "test.sqlite")}
	value(t, call(s, "POST", "/api/connection/connect", map[string]any{"config": config}, cookie, ""))
	query := func(sql string) any {
		return value(t, call(s, "POST", "/api/query/execute", map[string]any{"connectionId": "export-readonly", "sql": sql}, cookie, ""))
	}
	query("CREATE TABLE target (id INTEGER PRIMARY KEY)")
	query("INSERT INTO target VALUES (1)")
	value(t, call(s, "POST", "/api/export/query-result", map[string]any{"request": map[string]any{"exportId": "write-export", "connectionId": "export-readonly", "sql": "DELETE FROM target RETURNING *", "format": "csv"}}, cookie, ""))
	deadline := time.Now().Add(5 * time.Second)
	for {
		status := value(t, call(s, "GET", "/api/export/query-result/progress/write-export?poll=1", nil, cookie, "")).(map[string]any)
		if status["status"] != "running" {
			if status["status"] != "error" {
				t.Fatalf("write export was accepted: %v", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("export did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	rows := query("SELECT id FROM target").(map[string]any)["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("export changed the database: %v", rows)
	}
}
