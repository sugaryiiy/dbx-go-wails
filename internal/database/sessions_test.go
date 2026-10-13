package database

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func sessionSQL(t *testing.T, m *Manager, client, statement string) QueryResult {
	t.Helper()
	return handle(t, m, "/api/query/execute", map[string]any{"connectionId": "test", "database": "main", "clientSessionId": client, "sql": statement}).(QueryResult)
}
func TestExplicitTransactionPersistsAndCloseRollsBack(t *testing.T) {
	m, _ := testManager(t)
	run(t, m, "CREATE TABLE entries(id INTEGER PRIMARY KEY,value TEXT)")
	sessionSQL(t, m, "tab-a", "BEGIN")
	sessionSQL(t, m, "tab-a", "INSERT INTO entries VALUES(1,'pending')")
	if len(run(t, m, "SELECT * FROM entries").Rows) != 0 {
		t.Fatal("uncommitted data leaked")
	}
	if len(sessionSQL(t, m, "tab-a", "SELECT * FROM entries").Rows) != 1 {
		t.Fatal("session lost transaction")
	}
	sessionSQL(t, m, "tab-a", "COMMIT")
	if len(run(t, m, "SELECT * FROM entries").Rows) != 1 {
		t.Fatal("commit not persisted")
	}
	sessionSQL(t, m, "tab-a", "BEGIN")
	sessionSQL(t, m, "tab-a", "INSERT INTO entries VALUES(2,'rollback')")
	if !handle(t, m, "/api/query/close-client-session", map[string]any{"connectionId": "test", "database": "main", "clientSessionId": "tab-a"}).(bool) {
		t.Fatal("session was not closed")
	}
	if len(run(t, m, "SELECT * FROM entries").Rows) != 1 {
		t.Fatal("close committed pending changes")
	}
}
func TestSessionTemporaryObjectsAndRelock(t *testing.T) {
	m, c := testManager(t)
	run(t, m, "CREATE TABLE entries(id INTEGER)")
	sessionSQL(t, m, "one", "CREATE TEMP TABLE only_one(id INTEGER)")
	sessionSQL(t, m, "one", "INSERT INTO only_one VALUES(42)")
	if sessionSQL(t, m, "one", "SELECT * FROM only_one").Rows[0][0] != int64(42) {
		t.Fatal("lost temp table")
	}
	if _, err := m.Handle(context.Background(), "/api/query/execute", map[string]any{"connectionId": "test", "database": "main", "clientSessionId": "two", "sql": "SELECT * FROM only_one"}); err == nil {
		t.Fatal("temp table leaked between tabs")
	}
	handle(t, m, "/api/query/close-client-session", map[string]any{"connectionId": "test", "database": "main", "clientSessionId": "one"})
	c.Production = true
	if err := m.Connect(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	handle(t, m, "/api/connection/write-unlock", map[string]any{"connectionId": "test", "durationSecs": 30})
	sessionSQL(t, m, "protected", "BEGIN")
	sessionSQL(t, m, "protected", "INSERT INTO entries VALUES(1)")
	handle(t, m, "/api/connection/write-unlock/lock", map[string]any{"connectionId": "test"})
	if len(run(t, m, "SELECT * FROM entries").Rows) != 0 {
		t.Fatal("relock failed to rollback transaction")
	}
}
func TestResultSessionDoesNotReplayAndEnforcesOwner(t *testing.T) {
	m, _ := testManager(t)
	run(t, m, "CREATE TABLE entries(id INTEGER)")
	run(t, m, "INSERT INTO entries VALUES(1),(2),(3),(4)")
	p := map[string]any{"connectionId": "test", "database": "main", "clientSessionId": "a", "sql": "SELECT * FROM entries ORDER BY id", "pageSize": 2}
	first := handle(t, m, "/api/query/execute", p).(QueryResult)
	if len(first.Rows) != 2 || !first.HasMore || first.SessionID == "" {
		t.Fatalf("missing retained page: %#v", first)
	}
	run(t, m, "DELETE FROM entries")
	p["resultSessionId"] = first.SessionID
	p["rowOffset"] = 2
	second := handle(t, m, "/api/query/execute", p).(QueryResult)
	if len(second.Rows) != 2 || second.HasMore || second.Rows[0][0] != int64(3) {
		t.Fatal("replayed SQL or lost snapshot", second)
	}
	p["clientSessionId"] = "b"
	if _, err := m.Handle(context.Background(), "/api/query/execute", p); err == nil {
		t.Fatal("foreign tab read retained result")
	}
	if !handle(t, m, "/api/query/close-session", map[string]any{"connectionId": "test", "sessionId": first.SessionID}).(bool) {
		t.Fatal("failed to close result")
	}
	p["clientSessionId"] = "a"
	if _, err := m.Handle(context.Background(), "/api/query/execute", p); err == nil {
		t.Fatal("closed result still readable")
	}
}
func TestSQLiteTriggerCompoundStatementAndDollarRoutine(t *testing.T) {
	m, _ := testManager(t)
	run(t, m, "CREATE TABLE entries(id INTEGER, value TEXT)")
	run(t, m, "CREATE TABLE audit(value TEXT)")
	sql := `CREATE TRIGGER changed AFTER INSERT ON entries BEGIN INSERT INTO audit VALUES(CASE WHEN new.id=1 THEN 'one; case' ELSE 'other' END); UPDATE entries SET value='changed; value' WHERE id=new.id; END`
	parts, err := SplitStatementsForDialect(sql+"; SELECT 1;", "sqlite")
	if err != nil || len(parts) != 2 {
		t.Fatal(parts, err)
	}
	run(t, m, sql)
	run(t, m, "INSERT INTO entries VALUES(1,'initial')")
	if run(t, m, "SELECT value FROM audit").Rows[0][0] != "one; case" {
		t.Fatal("trigger was not executed")
	}
	parts, err = SplitStatementsForDialect(`CREATE FUNCTION f() RETURNS int LANGUAGE sql AS $$SELECT 1;$$; SELECT 2;`, "postgres")
	if err != nil || len(parts) != 2 {
		t.Fatal(parts, err)
	}
}
func TestSQLiteRebuildRevisionRollbackAndForeignKeys(t *testing.T) {
	m, _ := testManager(t)
	run(t, m, "CREATE TABLE parent(id INTEGER PRIMARY KEY,value TEXT CHECK(value<>''))")
	run(t, m, "CREATE TABLE child(id INTEGER REFERENCES parent(id))")
	run(t, m, "INSERT INTO parent VALUES(1,'kept')")
	run(t, m, "INSERT INTO child VALUES(1)")
	revision, err := m.SQLiteSchemaRevision(context.Background(), "test", "main", "main", "parent")
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{`CREATE TABLE replacement(id INTEGER PRIMARY KEY,value TEXT CHECK(value<>''),extra TEXT)`, `INSERT INTO replacement(id,value) SELECT id,value FROM parent`, `DROP TABLE parent`, `ALTER TABLE replacement RENAME TO parent`}
	if _, err = m.SQLiteRebuild(context.Background(), "test", "main", "main", "parent", "stale", statements); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatal("accepted stale revision", err)
	}
	if _, err = m.SQLiteRebuild(context.Background(), "test", "main", "main", "parent", revision, statements); err != nil {
		t.Fatal(err)
	}
	if len(run(t, m, "SELECT * FROM child").Rows) != 1 || run(t, m, "SELECT value FROM parent").Rows[0][0] != "kept" {
		t.Fatal("rebuild lost data")
	}
	if _, err = m.ExecuteSQL(context.Background(), "test", "main", "INSERT INTO child VALUES(999)", 1); err == nil {
		t.Fatal("foreign key protection was not restored")
	}
	revision, _ = m.SQLiteSchemaRevision(context.Background(), "test", "main", "main", "parent")
	bad := []string{`CREATE TABLE replacement(id INTEGER PRIMARY KEY,value TEXT)`, `INSERT INTO replacement SELECT id+10,value FROM parent`, `DROP TABLE parent`, `ALTER TABLE replacement RENAME TO parent`}
	if _, err = m.SQLiteRebuild(context.Background(), "test", "main", "main", "parent", revision, bad); err == nil {
		t.Fatal("rebuild committed broken foreign keys")
	}
	if got := fmt.Sprint(run(t, m, "SELECT id FROM parent").Rows[0][0]); got != "1" {
		t.Fatal("rebuild failure was not rolled back", got)
	}
}
