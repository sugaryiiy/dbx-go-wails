package database

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testManager(t *testing.T) (*Manager, Config) {
	t.Helper()
	// Cleanups run in reverse registration order. Close SQLite before TempDir
	// removes its files, which cannot be removed while open on Windows.
	c := Config{ID: "test", Type: "sqlite", Host: filepath.Join(t.TempDir(), "data with spaces.sqlite"), Database: "main"}
	m := NewManager()
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Errorf("close database manager: %v", err)
		}
	})
	if e := m.Connect(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	return m, c
}
func run(t *testing.T, m *Manager, statement string) QueryResult {
	t.Helper()
	r, e := m.ExecuteSQL(context.Background(), "test", "main", statement, 1000)
	if e != nil {
		t.Fatalf("%s: %v", statement, e)
	}
	return r
}
func handle(t *testing.T, m *Manager, path string, p map[string]any) any {
	t.Helper()
	v, e := m.Handle(context.Background(), path, p)
	if e != nil {
		t.Fatalf("%s: %v", path, e)
	}
	return v
}

func TestSQLiteCRUDMetadataPagingAndPrecision(t *testing.T) {
	m, c := testManager(t)
	run(t, m, "CREATE TABLE parent(id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE)")
	run(t, m, "CREATE TABLE child(id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parent(id) ON DELETE CASCADE, amount TEXT DEFAULT '0', payload BLOB)")
	run(t, m, "CREATE INDEX child_parent ON child(parent_id)")
	run(t, m, "INSERT INTO parent VALUES(1,'test')")
	run(t, m, "INSERT INTO child VALUES(9007199254740993,1,'12.500000000000000001',x'00ff'),(2,1,NULL,NULL),(3,1,'',NULL)")
	result := run(t, m, "SELECT id,amount,payload FROM child ORDER BY id DESC")
	if result.Rows[0][0] != "9007199254740993" || result.Rows[0][1] != "12.500000000000000001" || result.Rows[0][2] != "0x00ff" {
		t.Fatalf("precision/binary lost: %#v", result.Rows)
	}
	if result.Rows[1][1] != "" || result.Rows[2][1] != nil {
		t.Fatalf("null/empty distinction lost: %#v", result.Rows)
	}
	v := handle(t, m, "/api/query/execute", map[string]any{"connectionId": c.ID, "database": "main", "sql": "SELECT id FROM child ORDER BY id", "rowOffset": 1, "maxRows": 1}).(QueryResult)
	if !v.Truncated || len(v.Rows) != 1 || v.Rows[0][0] != int64(3) {
		t.Fatalf("bad pagination: %#v", v)
	}
	p := map[string]any{"connection_id": "test", "database": "main", "schema": "main", "table": "child"}
	cols := handle(t, m, "/api/schema/columns", p).([]record)
	if len(cols) != 4 || cols[0]["is_primary_key"] != true {
		t.Fatalf("bad columns: %#v", cols)
	}
	idx := handle(t, m, "/api/schema/indexes", p).([]record)
	if len(idx) != 2 {
		t.Fatalf("bad indexes: %#v", idx)
	}
	fk := handle(t, m, "/api/schema/foreign-keys", p).([]record)
	if len(fk) != 1 || fk[0]["ref_table"] != "parent" || fk[0]["on_delete"] != "CASCADE" {
		t.Fatalf("bad foreign keys: %#v", fk)
	}
	ddl := handle(t, m, "/api/schema/ddl", p).(string)
	if !strings.Contains(ddl, "CREATE TABLE child") {
		t.Fatal(ddl)
	}
	databases := handle(t, m, "/api/schema/databases", p).([]record)
	if len(databases) < 1 || databases[0]["name"] != "main" {
		t.Fatalf("bad database names: %#v", databases)
	}
	if _, e := m.ExecuteSQL(context.Background(), "test", "main", "INSERT INTO child(id,parent_id) VALUES(10,999)", 1); e == nil {
		t.Fatal("foreign key enforcement missing")
	}
	updated := run(t, m, "UPDATE child SET amount='changed' WHERE id=3")
	if updated.AffectedRows != 1 {
		t.Fatal(updated)
	}
	run(t, m, "DELETE FROM parent WHERE id=1")
	if len(run(t, m, "SELECT * FROM child").Rows) != 0 {
		t.Fatal("cascade failed")
	}
	if e := m.Connect(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	if len(run(t, m, "SELECT * FROM child").Rows) != 0 {
		t.Fatal("reconnection opened different database")
	}
}
func TestAtomicBatchAndImportRollback(t *testing.T) {
	m, _ := testManager(t)
	run(t, m, "CREATE TABLE entries(id INTEGER PRIMARY KEY, value TEXT)")
	_, e := m.Handle(context.Background(), "/api/query/execute-in-transaction", map[string]any{"connectionId": "test", "database": "main", "statements": []any{"INSERT INTO entries VALUES(1,'keep?')", "INSERT INTO entries VALUES(1,'duplicate')"}})
	if e == nil {
		t.Fatal("expected duplicate constraint error")
	}
	if len(run(t, m, "SELECT * FROM entries").Rows) != 0 {
		t.Fatal("failed transaction partially committed")
	}
	n, e := m.ImportRows(context.Background(), map[string]any{"connectionId": "test", "database": "main", "table": "entries"}, []string{"id", "value"}, [][]any{{1, "a'; DROP TABLE entries; --"}, {2, ""}, {3, nil}})
	if e != nil || n != 3 {
		t.Fatalf("import: %d %v", n, e)
	}
	_, e = m.ImportRows(context.Background(), map[string]any{"connectionId": "test", "database": "main", "table": "entries", "mode": "truncate"}, []string{"id", "value"}, [][]any{{1, "new"}, {1, "duplicate"}})
	if e == nil {
		t.Fatal("expected duplicate")
	}
	values := run(t, m, "SELECT value FROM entries ORDER BY id").Rows
	if len(values) != 3 || values[0][0] != "a'; DROP TABLE entries; --" || values[1][0] != "" || values[2][0] != nil {
		t.Fatalf("rollback/null/parameterization: %#v", values)
	}
}
func TestReadOnlyAndProductionGuards(t *testing.T) {
	for _, kind := range []string{"read_only", "production", "database_production"} {
		t.Run(kind, func(t *testing.T) {
			m, c := testManager(t)
			run(t, m, "CREATE TABLE protected(id INTEGER)")
			switch kind {
			case "read_only":
				c.ReadOnly = true
			case "production":
				c.Production = true
			case "database_production":
				c.ProductionDatabases = []string{"main"}
			}
			if e := m.Connect(context.Background(), c); e != nil {
				t.Fatal(e)
			}
			for _, statement := range []string{"INSERT INTO protected VALUES(1)", "--comment\nDELETE FROM protected", "PRAGMA query_only=OFF", "WITH c AS (SELECT 1) DELETE FROM protected", "EXPLAIN DELETE FROM protected"} {
				if _, e := m.ExecuteSQL(context.Background(), "test", "main", statement, 1); e == nil {
					t.Fatalf("guard allowed %s", statement)
				}
			}
			if _, e := m.ImportRows(context.Background(), map[string]any{"connectionId": "test", "database": "main", "table": "protected"}, []string{"id"}, [][]any{{1}}); e == nil {
				t.Fatal("import bypassed guard")
			}
			run(t, m, "SELECT * FROM protected")
			handle(t, m, "/api/connection/write-unlock", map[string]any{"connectionId": "test", "durationSecs": 30})
			run(t, m, "INSERT INTO protected VALUES(1)")
			handle(t, m, "/api/connection/write-unlock/lock", map[string]any{"connectionId": "test"})
			if _, e := m.ExecuteSQL(context.Background(), "test", "main", "DELETE FROM protected", 1); e == nil {
				t.Fatal("lock not restored")
			}
		})
	}
}
func TestExportReadOnlyCannotWriteAndResetsConnection(t *testing.T) {
	m, _ := testManager(t)
	run(t, m, "CREATE TABLE target(id INTEGER)")
	_, e := m.Handle(context.Background(), "/api/query/execute", map[string]any{"connectionId": "test", "database": "main", "sql": "DELETE FROM target RETURNING id", "readOnly": true})
	if e == nil {
		t.Fatal("export wrote data")
	}
	handle(t, m, "/api/query/execute", map[string]any{"connectionId": "test", "database": "main", "sql": "SELECT * FROM target", "readOnly": true})
	run(t, m, "INSERT INTO target VALUES(1)")
}
func TestQueryCancellationAndTimeout(t *testing.T) {
	m, _ := testManager(t)
	heavy := "WITH RECURSIVE counter(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM counter WHERE x<1000000000) SELECT sum(x) FROM counter"
	done := make(chan error, 1)
	go func() {
		_, e := m.Handle(context.Background(), "/api/query/execute", map[string]any{"connectionId": "test", "database": "main", "sql": heavy, "executionId": "cancel-me"})
		done <- e
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.RLock()
		_, running := m.running["cancel-me"]
		m.mu.RUnlock()
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("query never started")
		}
		time.Sleep(time.Millisecond)
	}
	if handle(t, m, "/api/query/cancel", map[string]any{"executionId": "cancel-me"}) != true {
		t.Fatal("cancel did not find execution")
	}
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("cancelled query succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("query ignored cancellation")
	}
	run(t, m, "SELECT 1")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	_, e := m.ExecuteSQL(ctx, "test", "main", heavy, 1)
	if e == nil {
		t.Fatal("query ignored timeout")
	}
	run(t, m, "SELECT 2")
}
func TestUnsupportedSettingsAndPathsFailClearly(t *testing.T) {
	m, c := testManager(t)
	for _, modify := range []func(*Config){func(c *Config) { c.SSL = true }, func(c *Config) { c.Transports = []map[string]any{{"type": "ssh"}} }, func(c *Config) { c.ConnectionString = "custom" }, func(c *Config) { c.Type = "oracle" }} {
		cfg := c
		modify(&cfg)
		if e := m.Connect(context.Background(), cfg); !errors.Is(e, ErrUnsupported) {
			t.Fatalf("unsupported settings silently accepted: %v", e)
		}
	}
	if _, e := m.Handle(context.Background(), "/api/query/nonexistent", map[string]any{}); !errors.Is(e, ErrUnsupported) {
		t.Fatal(e)
	}
	if _, e := m.Handle(context.Background(), "/api/query/execute", map[string]any{"connectionId": "test", "sql": "SELECT 1; SELECT 2"}); e == nil {
		t.Fatal("single execute silently dropped statement")
	}
}
func TestSQLSplitEscapesCommentsAndDollarQuotes(t *testing.T) {
	inputs := []struct {
		sql   string
		count int
	}{{"SELECT ';'; SELECT 'a'';b'; -- ;\nSELECT 3", 3}, {"/* outer /* ; nested */ */ SELECT 1; SELECT $$hello; world$$", 2}, {"-- only comment", 0}, {"SELECT \"a;b\", [x;y], `z;q`", 1}}
	for _, input := range inputs {
		parts, e := SplitStatements(input.sql)
		if e != nil || len(parts) != input.count {
			t.Fatalf("split %q: %#v %v", input.sql, parts, e)
		}
	}
	if _, e := SplitStatements("SELECT 'unterminated"); e == nil {
		t.Fatal("accepted unterminated string")
	}
}

func TestProductionDatabaseCannotBeBypassedWithAlternateSQLiteName(t *testing.T) {
	m, c := testManager(t)
	run(t, m, "CREATE TABLE guarded(id INTEGER)")
	c.ProductionDatabases = []string{"main"}
	if e := m.Connect(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	if _, e := m.ExecuteSQL(context.Background(), "test", "other", "DELETE FROM guarded", 1); e == nil {
		t.Fatal("alternate database bypassed production guard")
	}
}
func TestReturningWritesAndWithStatements(t *testing.T) {
	m, _ := testManager(t)
	run(t, m, "CREATE TABLE items(id INTEGER PRIMARY KEY, name TEXT)")
	r := run(t, m, "INSERT INTO items(name) VALUES('a'),('b') RETURNING id,name")
	if len(r.Rows) != 2 {
		t.Fatalf("RETURNING: %#v", r)
	}
	run(t, m, "WITH input(x) AS (VALUES('c')) INSERT INTO items(name) SELECT x FROM input")
	if len(run(t, m, "SELECT * FROM items").Rows) != 3 {
		t.Fatal("CTE insert did not execute")
	}
}

func TestFrontendConnectionProfilesAndReturnID(t *testing.T) {
	m := NewManager()
	defer m.Close()
	raw := map[string]any{"id": "actual-ui-id", "db_type": "sqlite", "driver_profile": "sqlite", "host": filepath.Join(t.TempDir(), "ui.sqlite"), "database": "main"}
	id := handle(t, m, "/api/connection/connect", map[string]any{"config": raw})
	if id != "actual-ui-id" {
		t.Fatalf("connect must return id, got %v", id)
	}
	for _, kind := range []string{"sqlite", "mysql", "postgres", "mariadb"} {
		c := Config{Type: kind, DriverProfile: kind, Host: "example", Database: "main"}.normalized()
		if e := c.validate(); e != nil {
			t.Fatalf("standard profile %s rejected: %v", kind, e)
		}
	}
	c := Config{Type: "mysql", DriverProfile: "mariadb"}
	if e := c.validate(); e != nil {
		t.Fatal(e)
	}
	c.DriverProfile = "custom-jdbc"
	if e := c.validate(); !errors.Is(e, ErrUnsupported) {
		t.Fatalf("unknown profile allowed: %v", e)
	}
}
func TestReadOnlyMultiStatementAndCommentBypasses(t *testing.T) {
	m, c := testManager(t)
	run(t, m, "CREATE TABLE guard(id INTEGER)")
	c.ReadOnly = true
	if e := m.Connect(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	probes := []string{"SELECT 1; INSERT INTO guard VALUES(1)", "SELECT 1; PRAGMA query_only=OFF; INSERT INTO guard VALUES(1)", "/*outer /*nested*/ */ INSERT INTO guard VALUES(1)", "WITH v AS (SELECT 1) INSERT INTO guard SELECT * FROM v", "SELECT id INTO changed FROM guard", "SELECT 1; ATTACH DATABASE ':memory:' AS other", "SELECT 1; SET autocommit=0"}
	for _, probe := range probes {
		if _, e := m.Handle(context.Background(), "/api/query/execute-multi", map[string]any{"connectionId": "test", "database": "main", "sql": probe}); e == nil {
			t.Errorf("readonly bypass accepted: %s", probe)
		}
	}
	if len(run(t, m, "SELECT * FROM guard").Rows) != 0 {
		t.Fatal("readonly probe mutated database")
	}
}

func TestDialectSplitterNeverExecutesLiteralContents(t *testing.T) {
	cases := []struct {
		dialect, sql string
		count        int
	}{
		{"postgres", "SELECT E'escaped\\'; still literal'; SELECT 2", 2},
		{"postgres", "SELECT $中文$first; DELETE FROM secret; last$中文$; SELECT 2", 2},
		{"postgres", "SELECT ARRAY['] ; DELETE FROM secret', 'ok']; SELECT 2", 2},
		{"mysql", "# SELECT '; DELETE FROM secret\nSELECT 1; -- comment ;\nSELECT 2", 2},
		{"mysql", "SELECT 1--\x01; DELETE FROM secret\n; SELECT 2", 2},
		{"mysql", "SELECT 5--2; SELECT 2", 2},
		{"sqlite", "SELECT [a;b], 'RETURNING'; SELECT 2", 2},
	}
	for _, c := range cases {
		parts, e := SplitStatementsForDialect(c.sql, c.dialect)
		if e != nil || len(parts) != c.count {
			t.Fatalf("%s %q: %#v %v", c.dialect, c.sql, parts, e)
		}
	}
	for _, source := range []string{
		"CREATE /* a */ /* b */ /* c */ TRIGGER t AFTER INSERT ON x BEGIN SELECT 1; DELETE FROM y; END",
		"CREATE DEFINER='root'@'localhost' PROCEDURE p() BEGIN SELECT 1; DELETE FROM y; END",
		"/*! SET autocommit=0 */; SELECT 1",
		`SELECT 'backslash\' ; DELETE FROM y'`,
	} {
		if _, e := SplitStatementsForDialect(source, "mysql"); !errors.Is(e, ErrUnsupported) {
			t.Fatalf("ambiguous MySQL SQL accepted: %q / %v", source, e)
		}
	}
}
func TestReturningAndCTEAffectedRows(t *testing.T) {
	m, _ := testManager(t)
	run(t, m, "CREATE TABLE counted(id INTEGER PRIMARY KEY, value TEXT)")
	r := run(t, m, "WITH v AS (VALUES(1),(2),(3)) INSERT INTO counted(id) SELECT * FROM v")
	if r.AffectedRows != 3 {
		t.Fatalf("CTE rows: %#v", r)
	}
	r = run(t, m, "UPDATE counted SET value='RETURNING is literal'")
	if r.AffectedRows != 3 {
		t.Fatalf("literal RETURNING misclassified: %#v", r)
	}
	r = handle(t, m, "/api/query/execute", map[string]any{"connectionId": "test", "database": "main", "sql": "DELETE FROM counted RETURNING id", "maxRows": 1}).(QueryResult)
	if r.AffectedRows != 3 || len(r.Rows) != 1 || !r.Truncated {
		t.Fatalf("RETURNING count/limit: %#v", r)
	}
	r = run(t, m, "CREATE TABLE another(id INTEGER)")
	if r.AffectedRows != 0 {
		t.Fatalf("DDL reported stale change count: %#v", r)
	}
}
func TestMySQLDriverValuesPreserveDecimalUnsignedAndBinary(t *testing.T) {
	cases := []struct {
		value any
		typ   string
		want  any
	}{
		{uint64(18446744073709551615), "UNSIGNED BIGINT", "18446744073709551615"},
		{[]byte("12345678901234567890.123456789"), "DECIMAL", "12345678901234567890.123456789"},
		{[]byte{0, 255}, "BIT", "0x00ff"},
		{[]byte{0, 255}, "GEOMETRY", "0x00ff"},
		{[]byte("0000-00-00"), "DATE", "0000-00-00"},
	}
	for _, c := range cases {
		if got := normalizeValue(c.value, c.typ); got != c.want {
			t.Fatalf("%s got %v want %v", c.typ, got, c.want)
		}
	}
}
func TestCloseCancelsRunningQueryWithoutManagerLockDeadlock(t *testing.T) {
	m, _ := testManager(t)
	done := make(chan error, 1)
	go func() {
		_, e := m.Handle(context.Background(), "/api/query/execute", map[string]any{"connectionId": "test", "database": "main", "executionId": "closing", "sql": "WITH RECURSIVE r(n) AS (VALUES(0) UNION ALL SELECT n+1 FROM r WHERE n<1000000000) SELECT sum(n) FROM r"})
		done <- e
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		m.mu.RLock()
		_, exists := m.running["closing"]
		m.mu.RUnlock()
		if exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("query did not start")
		}
		time.Sleep(time.Millisecond)
	}
	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	select {
	case e := <-closed:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manager.Close deadlocked")
	}
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("query was not cancelled")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("query failed to release its manager entry")
	}
}
