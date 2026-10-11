package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const influxCSVFixture = `#datatype,string,long,dateTime:RFC3339,double,string
#group,false,false,false,false,true
#default,_result,,,,
,result,table,_time,_value,host
,,0,2026-10-09T01:00:00Z,12.5,"host,a"
,,0,2026-10-09T01:00:01Z,,host-b

#datatype,string,long,string
#group,false,false,true
#default,_result,,
,result,table,phase
,,1,second
`

func TestInfluxV2ManagerConnectsAndQueriesWithoutAgentStore(t *testing.T) {
	var queries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token test-token" {
			t.Error("missing InfluxDB Token authentication")
		}
		switch r.URL.Path {
		case "/api/v2/buckets":
			if r.URL.Query().Get("org") != "gb & org" || r.URL.Query().Get("limit") != "100" {
				t.Error("lost organization or bucket pagination")
			}
			fmt.Fprint(w, `{"buckets":[{"name":"db"}]}`)
		case "/health":
			fmt.Fprint(w, `{"status":"pass","version":"v2.7.12"}`)
		case "/api/v2/query":
			queries.Add(1)
			if r.Method != http.MethodPost || r.URL.Query().Get("org") != "gb & org" {
				t.Error("invalid Flux request")
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["type"] != "flux" {
				t.Error("invalid Flux envelope")
			}
			if strings.Contains(str(body, "query"), "schema.measurements") {
				fmt.Fprint(w, "#datatype,string,long,string\n#group,false,false,false\n#default,_result,,\n,result,table,_value\n,,0,cpu\n")
			} else {
				fmt.Fprint(w, influxCSVFixture)
			}
		default:
			t.Errorf("unexpected route (including driver download): %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	config := map[string]any{"id": "influx", "db_type": "influxdb", "driver_profile": "influxdb", "host": server.URL, "password": "test-token", "database": "db", "external_config": map[string]any{"version": "2", "org": "gb & org"}}
	manager := NewManager()
	defer manager.Close()
	info, err := manager.Handle(context.Background(), "/api/connection/test-info", map[string]any{"config": config})
	if err != nil || str(mapValue(mapValue(info)["databaseInfo"]), "productVersion") != "v2.7.12" {
		t.Fatalf("test-info: %v, %v", info, err)
	}
	if _, err = manager.Handle(context.Background(), "/api/connection/connect", map[string]any{"config": config}); err != nil {
		t.Fatal(err)
	}
	params := map[string]any{"connectionId": "influx"}
	databases, err := manager.Handle(context.Background(), "/api/schema/databases", params)
	if err != nil || !reflect.DeepEqual(databases, []map[string]any{{"name": "db"}}) {
		t.Fatalf("databases: %v, %v", databases, err)
	}
	tables, err := manager.Handle(context.Background(), "/api/schema/tables", params)
	if err != nil || !reflect.DeepEqual(tables, []map[string]any{{"name": "cpu", "table_type": "TABLE"}}) {
		t.Fatalf("tables: %v, %v", tables, err)
	}
	params["sql"] = `from(bucket: "db") |> range(start: -1h)`
	params["maxRows"] = 2
	value, err := manager.Handle(context.Background(), "/api/query/execute-multi", params)
	if err != nil {
		t.Fatal(err)
	}
	result := value.([]QueryResult)[0]
	if len(result.Rows) != 2 || !result.Truncated || result.Rows[0][0] != "_result" || result.Rows[0][4] != "host,a" || result.Rows[1][3] != nil {
		t.Fatalf("Flux values/defaults/limit: %+v", result)
	}
	if queries.Load() != 2 {
		t.Fatalf("unexpected queries: %d", queries.Load())
	}
}

func TestInfluxV2BucketPagination(t *testing.T) {
	var offsets []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		offsets = append(offsets, offset)
		buckets := []map[string]any{{"name": "last"}}
		if offset == "0" {
			buckets = nil
			for i := 0; i < 100; i++ {
				buckets = append(buckets, map[string]any{"name": fmt.Sprintf("bucket-%d", i)})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"buckets": buckets})
	}))
	defer server.Close()
	value, err := openService(context.Background(), Config{Type: "influxdb", Host: server.URL, Password: "test-token", Raw: map[string]any{"external_config": map[string]any{"version": "2", "org": "gb"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer value.Close()
	offsets = nil
	databases, err := value.(*serviceDriver).influxDatabases(context.Background())
	if err != nil || len(databases) != 101 || !reflect.DeepEqual(offsets, []string{"0", "100"}) {
		t.Fatalf("bucket pages: %d, %v, %v", len(databases), offsets, err)
	}
}

func TestInfluxCSVUnionsTablesDefaultsErrorsAndMalformedRows(t *testing.T) {
	result, err := parseInfluxCSV(influxCSVFixture, 100)
	if err != nil || len(result.Rows) != 3 || len(result.Columns) != 6 || result.Rows[2][5] != "second" || result.Rows[0][5] != nil {
		t.Fatalf("multi-table CSV: %+v, %v", result, err)
	}
	for _, source := range []string{"error,reference\nbad query,0\n", ",result,table,_value\n,,0\n", "not CSV or an Influx response"} {
		if _, err := parseInfluxCSV(source, 10); err == nil {
			t.Errorf("invalid/error response accepted: %q", source)
		}
	}
}

func TestInfluxV1AuthenticationFallbackAndHTTP200Errors(t *testing.T) {
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "reader" || password != "secret" {
			t.Error("missing Basic authentication")
		}
		switch r.URL.Query().Get("q") {
		case "SHOW DATABASES":
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"SHOW DATABASES unavailable"}`)
		case "SHOW MEASUREMENTS":
			if r.URL.Query().Get("db") != "db & name" {
				t.Error("database was not encoded")
			}
			if fail.Load() {
				fmt.Fprint(w, `{"results":[{"error":"secret invalid query"}]}`)
				return
			}
			fmt.Fprint(w, `{"results":[{"series":[{"columns":["name"],"values":[["cpu"]]}]}]}`)
		default:
			fmt.Fprint(w, `{"results":[{"series":[{"columns":["time","value"],"tags":{"host":"a"},"values":[["2026-10-09T00:00:00Z",42]]}]}]}`)
		}
	}))
	defer server.Close()
	value, err := openService(context.Background(), Config{Type: "influxdb", Host: server.URL, Username: "reader", Password: "secret", Database: "db & name"})
	if err != nil {
		t.Fatal(err)
	}
	defer value.Close()
	driver := value.(*serviceDriver)
	result, err := driver.influxQuery(context.Background(), "db & name", "SELECT * FROM cpu", 10)
	if err != nil || !reflect.DeepEqual(result.Columns, []string{"time", "value", "host"}) || result.Rows[0][2] != "a" {
		t.Fatalf("series tags: %+v, %v", result, err)
	}
	fail.Store(true)
	if err = driver.Ping(context.Background()); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("query error ignored or leaked token: %v", err)
	}
}

func TestInfluxValidationLocksAndCancellation(t *testing.T) {
	for _, external := range []map[string]any{{"version": "2"}, {"version": "2", "org": "gb"}, {"version": "3"}} {
		if _, err := openService(context.Background(), Config{Type: "influxdb", Host: "invalid.test", Raw: map[string]any{"external_config": external}}); err == nil {
			t.Fatalf("invalid config accepted: %v", external)
		}
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/buckets" {
			fmt.Fprint(w, `{"buckets":[]}`)
			return
		}
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	value, err := openService(context.Background(), Config{Type: "influxdb", Host: server.URL, Password: "test-token", Raw: map[string]any{"external_config": map[string]any{"version": "2", "org": "gb"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer value.Close()
	for _, source := range []string{`from(bucket: "db") |> range(start: -1h)`, `import "http"; http.post(url: "https://example.test")`} {
		if _, err := value.Handle(context.Background(), "/api/query/execute", map[string]any{"sql": source}, true); err == nil {
			t.Error("unverified locked Flux reached server")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("locked query sent HTTP request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := value.Handle(ctx, "/api/query/execute", map[string]any{"sql": `from(bucket: "db") |> range(start: -1h)`}, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("query cancellation: %v", err)
	}
	for _, source := range []string{"SELECT * INTO other FROM cpu", "SHOW DATABASES; DROP DATABASE db", `SELECT * FROM "a\"; DROP DATABASE db"`} {
		if influxQLReadOnly(source) {
			t.Errorf("unsafe InfluxQL allowed: %s", source)
		}
	}
	if !influxQLReadOnly("SHOW DATABASES; SELECT * FROM cpu") {
		t.Fatal("read-only InfluxQL rejected")
	}
}

func TestInfluxV2RejectsAuthenticationAndMalformedBucketResponses(t *testing.T) {
	for _, mode := range []string{"unauthorized", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Token secret-token" {
					t.Error("token not normalized")
				}
				if mode == "unauthorized" {
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprint(w, `{"message":"invalid secret-token"}`)
				} else {
					fmt.Fprint(w, `{"status":"pass"}`)
				}
			}))
			defer server.Close()
			value, err := openService(context.Background(), Config{Type: "influxdb", Host: server.URL, Password: " secret-token ", Raw: map[string]any{"external_config": map[string]any{"version": "2", "org": "gb"}}})
			if err == nil {
				value.Close()
				t.Fatal("unauthenticated/malformed probe reported success")
			}
			if strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("error leaked normalized token: %v", err)
			}
		})
	}
}
