package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sugaryiiy/magpiedb/internal/database"
)

func TestInfluxHTTPPreviewPaginationCountAndReadOnlyExecution(t *testing.T) {
	var queries, compatibility atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token preview-token" {
			t.Error("authentication missing")
		}
		switch r.URL.Path {
		case "/api/v2/buckets":
			fmt.Fprint(w, `{"buckets":[{"name":"db"}]}`)
		case "/health":
			fmt.Fprint(w, `{"version":"v2.7.12"}`)
		case "/api/v2/query":
			queries.Add(1)
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["type"] != "flux" || r.URL.Query().Get("org") != "org" {
				t.Error("wrong Flux request")
			}
			o, ok := database.ParseInfluxPreview(str(body, "query"))
			if !ok {
				t.Error("unexpected query")
				return
			}
			if o.Count {
				fmt.Fprint(w, "#datatype,string,long,long\n#default,_result,,\n,result,table,cnt\n,,0,42\n")
			} else {
				fmt.Fprintf(w, "#datatype,string,long,dateTime:RFC3339,string,double\n#default,_result,,,,\n,result,table,time,host,value\n,,0,2026-10-09T00:00:00Z,host-%d,12.5\n", o.Offset)
			}
		case "/query":
			compatibility.Add(1)
			if r.URL.Query().Get("db") != "db" {
				t.Error("compatibility database missing")
			}
			fmt.Fprint(w, `{"results":[{"series":[{"columns":["time","value"],"values":[["2026-10-09T00:00:00Z",1]]}]}]}`)
		default:
			t.Errorf("unexpected route or agent dependency: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer backend.Close()
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	config := map[string]any{"id": "influx-preview", "db_type": "influxdb", "host": backend.URL, "password": "preview-token", "database": "db", "read_only": true, "external_config": map[string]any{"version": "2", "org": "org"}}
	value(t, call(s, "POST", "/api/connection/connect", map[string]any{"config": config}, cookie, ""))
	options := map[string]any{"databaseType": "influxdb", "influxDbVersion": "2", "database": "db", "tableName": "cpu", "injectDefaultTimeSeriesWhere": true, "limit": 2}
	build := func(path string, o map[string]any) string {
		return value(t, call(s, "POST", path, map[string]any{"options": o}, cookie, "")).(string)
	}
	execute := func(source string) map[string]any {
		return value(t, call(s, "POST", "/api/query/execute", map[string]any{"connectionId": "influx-preview", "database": "db", "sql": source}, cookie, "")).(map[string]any)
	}
	source := build("/api/query/build-table-select-sql", options)
	result := execute(source)
	if cols := result["columns"].([]any); len(cols) != 3 || cols[0] != "time" {
		t.Fatal("transport columns leaked", result)
	}
	options["offset"] = 2
	page := execute(build("/api/query/build-table-select-sql", options))
	if page["rows"].([]any)[0].([]any)[1] != "host-2" {
		t.Fatal("pagination offset lost", page)
	}
	count := execute(build("/api/query/build-data-grid-count-sql", options))
	if count["rows"].([]any)[0].([]any)[0] != "42" {
		t.Fatal("not a scalar row count", count)
	}
	for _, sql := range []string{source + "\n |> to(bucket: \"other\")", `from(bucket: "db") |> range(start: -1h)`, `SELECT * INTO other FROM cpu`} {
		w := call(s, "POST", "/api/query/execute", map[string]any{"connectionId": "influx-preview", "sql": sql, "tableDataPreview": true}, cookie, "")
		if w.Code == 200 {
			t.Fatal("unverified query bypassed read-only lock")
		}
	}
	if queries.Load() != 3 {
		t.Fatal("locked program reached InfluxDB", queries.Load())
	}
	// An existing InfluxQL editor query on v2 uses its compatibility endpoint,
	// while native table preview above has no DBRP dependency.
	execute("SELECT * FROM cpu")
	if compatibility.Load() != 1 {
		t.Fatal("InfluxQL was sent as Flux")
	}
}

func TestInfluxPaginationNeverUsesRelationalWrapper(t *testing.T) {
	for _, source := range []string{`from(bucket: "db") |> range(start: -1h)`, `SELECT * FROM cpu LIMIT 100`} {
		result, _, err := buildSQL("/api/query/prepare-pagination-plan", map[string]any{"options": map[string]any{"databaseType": "influxdb", "sql": source, "pagination": map[string]any{"limit": 10, "offset": 20}}})
		if err != nil || result.(map[string]any)["sqlToExecute"] != source {
			t.Fatal(result, err)
		}
	}
	source, err := database.BuildInfluxPreview(database.InfluxPreviewOptions{Version: "2", Database: "db", Table: "cpu", Limit: 100, DefaultTime: true})
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := buildSQL("/api/query/prepare-pagination-plan", map[string]any{"options": map[string]any{"databaseType": "influxdb", "sql": source, "pagination": map[string]any{"limit": 10, "offset": 20}}})
	if err != nil || !strings.Contains(result.(map[string]any)["sqlToExecute"].(string), "limit(n: 10, offset: 20)") || result.(map[string]any)["exactQueryRowBound"] != 100 {
		t.Fatal(result, err)
	}
	result, _, err = buildSQL("/api/query/prepare-pagination-plan", map[string]any{"options": map[string]any{"databaseType": "influxdb", "sql": source, "pagination": map[string]any{"limit": 10, "offset": 100}}})
	if err != nil || !strings.Contains(result.(map[string]any)["sqlToExecute"].(string), "limit(n: 0)") {
		t.Fatal("exhausted page repeated data", result, err)
	}
	// Protocol selection works even when the reported server version is absent.
	_, _, err = buildSQL("/api/query/build-table-select-sql", map[string]any{"options": map[string]any{"databaseType": "influxdb", "influxDbVersion": "2", "database": "db", "tableName": "cpu"}})
	if err != nil {
		t.Fatal(err)
	}
}
