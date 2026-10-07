package server

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClickHouseHTTPExportAndExplain(t *testing.T) {
	path := os.Getenv("DBX_TEST_CLICKHOUSE_CONFIG")
	if path == "" {
		t.Skip("set DBX_TEST_CLICKHOUSE_CONFIG to disposable ClickHouse configuration")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	config["id"] = "export-clickhouse"
	config["read_only"] = true
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	value(t, call(s, "POST", "/api/connection/connect", map[string]any{"config": config}, cookie, ""))
	sql := "SELECT toUInt64('18446744073709551615') AS id, toDecimal128('1234567890123456789.123456789', 9) AS amount, CAST(NULL AS Nullable(String)) AS missing"
	for _, format := range []string{"csv", "xlsx", "json", "txt"} {
		t.Run(format, func(t *testing.T) {
			id := "clickhouse-export-" + format
			value(t, call(s, "POST", "/api/export/query-result", map[string]any{"request": map[string]any{"exportId": id, "connectionId": "export-clickhouse", "database": config["database"], "sql": sql, "format": format, "filePath": "result." + format}}, cookie, ""))
			deadline := time.Now().Add(5 * time.Second)
			for {
				status := value(t, call(s, "GET", "/api/export/query-result/progress/"+id+"?poll=1", nil, cookie, "")).(map[string]any)
				if status["status"] != "running" {
					if status["status"] != "done" || status["rowsExported"] != float64(1) {
						t.Fatalf("export failed: %#v", status)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("export timed out")
				}
				time.Sleep(5 * time.Millisecond)
			}
			download := call(s, "GET", "/api/export/query-result/download/"+id, nil, cookie, "")
			if download.Code != 200 {
				t.Fatal(download.Code)
			}
			body := download.Body.String()
			if format == "xlsx" {
				_, rows, _, err := parseTransferFile(download.Body.Bytes(), format, map[string]any{"hasHeader": true})
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(rows)
				body = string(raw)
			}
			if !strings.Contains(body, "18446744073709551615") || !strings.Contains(body, "1234567890123456789.123456789") {
				t.Fatalf("export lost precision: %s", body)
			}
		})
	}
	plan := value(t, call(s, "POST", "/api/query/build-explain-sql", map[string]any{"options": map[string]any{"databaseType": "clickhouse", "sql": "SELECT number FROM numbers(10)", "format": "standard"}}, cookie, "")).(map[string]any)
	if plan["ok"] != true {
		t.Fatalf("explain builder: %#v", plan)
	}
	result := value(t, call(s, "POST", "/api/query/execute", map[string]any{"connectionId": "export-clickhouse", "database": config["database"], "sql": plan["sql"]}, cookie, "")).(map[string]any)
	if len(result["rows"].([]any)) == 0 {
		t.Fatal("empty ClickHouse execution plan")
	}
}
