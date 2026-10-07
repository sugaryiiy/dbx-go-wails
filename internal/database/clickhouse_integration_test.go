package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func liveClickHouseConfig(t *testing.T) Config {
	t.Helper()
	path := os.Getenv("DBX_TEST_CLICKHOUSE_CONFIG")
	if path == "" {
		t.Skip("set DBX_TEST_CLICKHOUSE_CONFIG to a disposable ClickHouse Config JSON file")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if c.Type != "clickhouse" {
		t.Fatal("ClickHouse test fixture has wrong db_type")
	}
	return c
}
func TestClickHouseLive(t *testing.T) {
	c := liveClickHouseConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	d, err := openClickHouse(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h := d.(*clickHouse)
	database := "dbx_go_live_" + fmt.Sprint(time.Now().UnixNano())
	run := func(sql string, locked bool) QueryResult {
		t.Helper()
		value, err := h.Handle(ctx, "/api/query/execute", map[string]any{"sql": sql, "database": database}, locked)
		if err != nil {
			t.Fatal(err)
		}
		return value.(QueryResult)
	}
	if _, err := h.Handle(ctx, "/api/query/execute", map[string]any{"sql": "CREATE DATABASE `" + database + "`"}, false); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_, err := h.Handle(cleanup, "/api/query/execute", map[string]any{"sql": "DROP DATABASE `" + database + "`"}, false)
		if err != nil {
			t.Error(err)
		}
	}()
	run("CREATE TABLE sample (id UInt64, amount Decimal(38, 9), label Nullable(String), INDEX ix_id id TYPE minmax GRANULARITY 1) ENGINE=MergeTree ORDER BY id", false)
	inserted := run("INSERT INTO sample VALUES (18446744073709551615, 1234567890123456789.123456789, NULL), (2, 2.500000000, 'hello')", false)
	if inserted.AffectedRows != 2 {
		t.Fatalf("insert count: %d", inserted.AffectedRows)
	}
	r := run("SELECT id, amount, label FROM sample ORDER BY id DESC", true)
	if len(r.Rows) != 2 || r.Rows[0][0] != "18446744073709551615" || r.Rows[0][1] != "1234567890123456789.123456789" || r.Rows[0][2] != nil {
		t.Fatalf("precision/null mismatch: %#v", r.Rows)
	}
	run("CREATE VIEW sample_view AS SELECT id FROM sample", false)
	run("CREATE MATERIALIZED VIEW sample_mv ENGINE=Memory AS SELECT id FROM sample", false)
	for _, name := range []string{"sample_view", "sample_mv"} {
		source, err := h.schema(ctx, "/api/schema/ddl", map[string]any{"database": database, "table": name})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(source.(string), "VIEW") {
			t.Fatalf("missing view DDL: %v", source)
		}
	}
	info, err := h.Info(ctx)
	if err != nil || info["productName"] != "ClickHouse" {
		t.Fatalf("info: %#v %v", info, err)
	}
	for _, route := range []string{"databases", "schemas", "tables", "objects", "completion-objects", "columns", "indexes", "foreign-keys", "reference-keys", "reference-key-columns", "ddl", "object-source", "all-columns"} {
		value, err := h.Handle(ctx, "/api/schema/"+route, map[string]any{"database": database, "table": "sample"}, true)
		if err != nil {
			t.Fatalf("%s: %v", route, err)
		}
		switch route {
		case "ddl":
			if !strings.Contains(value.(string), "MergeTree") || !strings.Contains(value.(string), "ix_id") {
				t.Fatalf("incomplete DDL: %v", value)
			}
		case "indexes":
			idx := value.([]record)
			if len(idx) != 2 {
				t.Fatalf("indexes: %#v", idx)
			}
			for _, item := range idx {
				if valueBool(item["is_unique"]) {
					t.Fatal("ClickHouse index incorrectly unique")
				}
			}
		case "columns":
			cols := value.([]record)
			if len(cols) != 3 || !valueBool(cols[2]["is_nullable"]) {
				t.Fatalf("columns: %#v", cols)
			}
		case "reference-key-columns":
			if len(value.([]string)) != 0 {
				t.Fatal("nonunique primary key used for grid writes")
			}
		}
	}
	for _, sql := range []string{"INSERT INTO sample VALUES (3,1,NULL)", "SELECT 1 SETTINGS readonly=0", "SELECT * FROM url('http://127.0.0.1','CSV','x String')"} {
		if _, err := h.Handle(ctx, "/api/query/execute", map[string]any{"sql": sql, "database": database}, true); err == nil {
			t.Fatalf("protected query allowed %q", sql)
		}
	}
	// Bypass the client classifier internally to prove the server readonly setting.
	if _, err := h.query(ctx, database, "INSERT INTO sample VALUES (3,1,NULL)", true, 10, 0, 1024, nil); err == nil {
		t.Fatal("server readonly failed")
	}
	value, err := h.Handle(ctx, "/api/query/execute", map[string]any{"sql": "SELECT number FROM numbers(1000000)", "database": database, "maxRows": 3}, true)
	if err != nil {
		t.Fatal(err)
	}
	r = value.(QueryResult)
	if len(r.Rows) != 3 || !r.Truncated {
		t.Fatalf("unbounded rows: %#v", r)
	}
	for _, route := range []string{"execute-in-transaction", "execute-batch"} {
		if _, err := h.Handle(ctx, "/api/query/"+route, map[string]any{"sql": "SELECT 1"}, false); err == nil {
			t.Fatalf("atomic operation accepted: %s", route)
		}
	}
	short, stop := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer stop()
	start := time.Now()
	if _, err := h.Handle(short, "/api/query/execute", map[string]any{"sql": "SELECT sum(sipHash64(number)) FROM numbers(10000000000)", "database": database}, true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow query must actually time out: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("slow cancellation")
	}
	count, err := h.query(ctx, database, "SELECT count() FROM system.processes WHERE startsWith(query_id, 'dbx-') AND query LIKE 'SELECT sum(sipHash64%'", true, 1, 0, 1024, nil)
	if err != nil {
		t.Fatal(err)
	}
	if valueString(count.Rows[0][0]) != "0" {
		t.Fatal("cancelled server query still running")
	}
	// Metadata identifiers are protocol parameters, not interpolated SQL.
	if _, err := h.schema(ctx, "/api/schema/columns", map[string]any{"database": database, "table": "x' OR 1=1 --"}); err != nil {
		t.Fatal(err)
	}
}

func TestClickHouseLiveSSH(t *testing.T) {
	c := liveClickHouseConfig(t)
	target := fmt.Sprintf("%s:%d", c.Host, c.Port)
	server := newSSHTestServer(t, func(s *sshTestServer) { s.allow = map[string]bool{target: true} })
	c.Transports = []map[string]any{server.layer()}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	d, err := openClickHouse(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Info(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	server.waitIdle(t)
}

func TestClickHouseLiveRejectsUntrustedTLS(t *testing.T) {
	c := liveClickHouseConfig(t)
	if !c.SSL || c.CACert == "" {
		t.Skip("fixture needs private TLS CA")
	}
	c.CACert = ""
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if d, err := openClickHouse(ctx, c); err == nil {
		d.Close()
		t.Fatal("untrusted TLS CA accepted")
	}
}
