package database

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClickHouseSQLGuard(t *testing.T) {
	for _, source := range []string{"SELECT 1", "SELECT count() FROM numbers(10)", "WITH q AS (SELECT 1) SELECT * FROM q", "SELECT 'SETTINGS readonly=0; FORMAT CSV'", "SHOW TABLES", "DESCRIBE t"} {
		if _, err := clickHouseStatements(source, true); err != nil {
			t.Errorf("rejected %q: %v", source, err)
		}
	}
	for _, source := range []string{"SELECT 1 SETTINGS readonly=0", "SELECT * FROM url('https://bad')", "SELECT file('/etc/passwd')", "SELECT * FROM remote('host','t')", "SELECT executable('x','TSV','x String')", "SELECT custom_udf(1)", "SELECT `url`('x')", "SELECT * FROM s3('x')", "SELECT 1 INTO OUTFILE '/tmp/a'", "SELECT 1 FORMAT CSV", "SET readonly=0", "BEGIN", "INSERT INTO t VALUES(1)", "EXPLAIN INSERT INTO t VALUES(1)", "SELECT 'x\\'; DROP TABLE t --'", "SELECT 1; DELETE FROM t"} {
		if _, err := clickHouseStatements(source, true); err == nil {
			t.Errorf("accepted %q", source)
		}
	}
	for _, source := range []string{"CREATE TABLE t (id UInt64) ENGINE=Memory", "INSERT INTO t VALUES (1, 'double''quote')", "ALTER TABLE t DELETE WHERE id=1"} {
		if _, err := clickHouseStatements(source, false); err != nil {
			t.Errorf("rejected write %q: %v", source, err)
		}
	}
	for _, source := range []string{"SET max_threads=2", "USE x", "SELECT 1 FORMAT CSV", "CREATE TABLE t(id UInt64) ENGINE=MergeTree ORDER BY id SETTINGS a=1"} {
		if _, err := clickHouseStatements(source, false); err == nil {
			t.Errorf("accepted session/format control %q", source)
		}
	}
}
func TestClickHouseJSONPrecisionBoundsAndErrors(t *testing.T) {
	source := `{"meta":[{"name":"big","type":"UInt64"},{"name":"decimal","type":"Decimal(38, 9)"},{"name":"nullable","type":"Nullable(String)"},{"name":"array","type":"Array(UInt64)"}],"data":[[18446744073709551615,1234567890123456789.123456789,null,[9007199254740992,1]]],"rows":1}`
	r, err := decodeClickHouse(strings.NewReader(source), 10, 0, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if r.Rows[0][0] != "18446744073709551615" || r.Rows[0][1] != "1234567890123456789.123456789" || r.Rows[0][2] != nil || r.Rows[0][3].([]any)[0] != "9007199254740992" {
		t.Fatalf("precision lost: %#v", r.Rows)
	}
	source = `{"meta":[{"name":"n","type":"UInt32"}],"data":[[1],[2],[3],[4]],"rows":4}`
	r, err = decodeClickHouse(strings.NewReader(source), 2, 1, 1024)
	if err != nil || !r.Truncated || len(r.Rows) != 2 || r.Rows[0][0] != int64(2) {
		t.Fatalf("bad bound: %#v %v", r, err)
	}
	r, err = decodeClickHouse(strings.NewReader(source), 10, 0, 4)
	if err != nil || !r.Truncated || len(r.Rows) != 1 {
		t.Fatalf("bad bytes: %#v %v", r, err)
	}
	for _, source := range []string{`{"meta":[],"data":[[1]]}`, `{"meta":[],"data":[],"exception":"failure"}`, `{"meta":[],"data":[]} Code: 123 exception`, `garbage`} {
		if _, err := decodeClickHouse(strings.NewReader(source), 1, 0, 1024); err == nil {
			t.Errorf("accepted error response: %s", source)
		}
	}
	oversized := `{"meta":[{"name":"x","type":"String"}],"data":[["` + strings.Repeat("a", 2<<20) + `"]]}`
	if _, err := decodeClickHouse(strings.NewReader(oversized), 1, 0, 16); err == nil {
		t.Fatal("unbounded value accepted")
	}
}
func clickHouseTestConfig(t *testing.T, address string) Config {
	t.Helper()
	u, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	number, _ := strconv.Atoi(port)
	return Config{ID: "clickhouse-http-test", Type: "clickhouse", Host: host, Port: number, Username: "dbx", Password: "private-test-secret", Database: "default"}
}
func TestClickHouseHTTPBoundary(t *testing.T) {
	var mu sync.Mutex
	queries := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		mu.Lock()
		queries = append(queries, s)
		mu.Unlock()
		user, pass, _ := r.BasicAuth()
		if user != "dbx" || pass != "private-test-secret" {
			t.Error("credentials missing")
		}
		if r.URL.Query().Has("password") {
			t.Error("password exposed in URL")
		}
		if strings.HasPrefix(s, "KILL QUERY") {
			w.Write([]byte(`{"meta":[],"data":[]}`))
			return
		}
		if r.URL.Query().Get("default_format") != "JSONCompact" || r.URL.Query().Get("output_format_json_quote_decimals") != "1" {
			t.Error("precision/format controls missing")
		}
		if s == "SELECT failure" {
			w.WriteHeader(400)
			w.Write([]byte("wrong password private-test-secret"))
			return
		}
		if r.URL.Query().Get("readonly") != "1" {
			t.Error("server readonly setting missing")
		}
		w.Write([]byte(`{"meta":[{"name":"n","type":"UInt8"}],"data":[[1]],"rows":1}`))
	}))
	defer server.Close()
	d, err := openClickHouse(context.Background(), clickHouseTestConfig(t, server.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Handle(context.Background(), "/api/query/execute", map[string]any{"sql": "SELECT 1"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Handle(context.Background(), "/api/query/execute", map[string]any{"sql": "SELECT failure"}, true); err == nil || strings.Contains(err.Error(), "private-test-secret") {
		t.Fatalf("error secret leak: %v", err)
	}
	if _, err := d.Handle(context.Background(), "/api/query/execute-in-transaction", map[string]any{}, false); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("atomic semantics claimed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 3 {
		t.Fatalf("unexpected requests (successful calls must not issue KILL): %#v", queries)
	}
}
func TestClickHouseCancellationKillsServerQuery(t *testing.T) {
	started := make(chan struct{})
	killed := make(chan struct{})
	var once sync.Once
	var queryID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		if s == "SELECT 1" {
			w.Write([]byte(`{"meta":[{"name":"n","type":"UInt8"}],"data":[[1]]}`))
			return
		}
		if strings.HasPrefix(s, "KILL QUERY") {
			if r.URL.Query().Get("param_id") != queryID {
				t.Error("wrong query killed")
			}
			once.Do(func() { close(killed) })
			w.Write([]byte(`{"meta":[],"data":[]}`))
			return
		}
		queryID = r.URL.Query().Get("query_id")
		close(started)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()
	d, err := openClickHouse(context.Background(), clickHouseTestConfig(t, server.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		_, err := d.Handle(ctx, "/api/query/execute", map[string]any{"sql": "SELECT sleep(3)"}, true)
		finished <- err
	}()
	<-started
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancel returned success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not return")
	}
	select {
	case <-killed:
	case <-time.After(time.Second):
		t.Fatal("server query not killed")
	}
}
func TestClickHouseRedirectAndInvalidOptions(t *testing.T) {
	reached := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 302) }))
	defer server.Close()
	c := clickHouseTestConfig(t, server.URL)
	if _, err := openClickHouse(context.Background(), c); err == nil || reached {
		t.Fatal("credential redirect allowed")
	}
	c.Raw = map[string]any{"protocol": "native"}
	if _, err := openClickHouse(context.Background(), c); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("native protocol silently accepted: %v", err)
	}
	c.Raw = nil
	c.URLParams = "readonly=0"
	if _, err := openClickHouse(context.Background(), c); err == nil {
		t.Fatal("arbitrary settings accepted")
	}
}
func TestClickHouseNullNestedJSON(t *testing.T) {
	r, err := decodeClickHouse(strings.NewReader(`{"meta":[{"name":"v","type":"Map(String, Nullable(Int64))"}],"data":[[{"a":null,"b":9007199254740993}]]}`), 1, 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(r)
	if err != nil || !strings.Contains(string(b), `"a":null`) || !strings.Contains(string(b), `"b":"9007199254740993"`) {
		t.Fatalf("nested data lost: %s %v", b, err)
	}
}
