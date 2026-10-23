package database

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestSQLitePragmaSettingsStayInTheirTab(t *testing.T) {
	for _, pragma := range []string{"PRAGMA foreign_keys=OFF", "PRAGMA foreign_keys(OFF)"} {
		t.Run(pragma, func(t *testing.T) {
			m, _ := testManager(t)
			run(t, m, "CREATE TABLE parent(id INTEGER PRIMARY KEY)")
			run(t, m, "CREATE TABLE child(parent_id INTEGER REFERENCES parent(id))")
			sessionSQL(t, m, "settings", pragma)
			sessionSQL(t, m, "settings", "INSERT INTO child VALUES(99)")
			_, err := m.Handle(context.Background(), "/api/query/execute", map[string]any{
				"connectionId": "test", "database": "main", "clientSessionId": "other", "sql": "INSERT INTO child VALUES(100)",
			})
			if err == nil {
				t.Fatal("another tab inherited disabled foreign key checks")
			}
			handle(t, m, "/api/query/close-client-session", map[string]any{"connectionId": "test", "database": "main", "clientSessionId": "settings"})
			if _, err := m.ExecuteSQL(context.Background(), "test", "main", "INSERT INTO child VALUES(101)", 1); err == nil {
				t.Fatal("closed tab returned its modified connection to the pool")
			}
		})
	}
}

func TestSQLiteUnscopedPragmaDiscardsConnection(t *testing.T) {
	m, _ := testManager(t)
	run(t, m, "CREATE TABLE parent(id INTEGER PRIMARY KEY)")
	run(t, m, "CREATE TABLE child(parent_id INTEGER REFERENCES parent(id))")
	run(t, m, "PRAGMA foreign_keys=OFF")
	if _, err := m.ExecuteSQL(context.Background(), "test", "main", "INSERT INTO child VALUES(99)", 1); err == nil {
		t.Fatal("unscoped PRAGMA contaminated the pool")
	}
	sessionSQL(t, m, "readonly-tab", "PRAGMA query_only=ON")
	sessionSQL(t, m, "writable-tab", "INSERT INTO parent VALUES(1)")
	if _, err := m.Handle(context.Background(), "/api/query/execute", map[string]any{
		"connectionId": "test", "database": "main", "clientSessionId": "readonly-tab", "sql": "INSERT INTO parent VALUES(2)",
	}); err == nil {
		t.Fatal("query_only setting was lost within its owning tab")
	}
}

func TestSQLiteReadPragmasKeepPrivateMemoryDatabase(t *testing.T) {
	m := NewManager()
	t.Cleanup(func() { _ = m.Close() })
	if err := m.Connect(context.Background(), Config{ID: "test", Type: "sqlite", Host: ":memory:", Database: "main"}); err != nil {
		t.Fatal(err)
	}
	run(t, m, "CREATE TABLE entries(id INTEGER)")
	run(t, m, "INSERT INTO entries VALUES(42)")
	for _, pragma := range []string{"PRAGMA foreign_keys", "PRAGMA table_info(entries)", `PRAGMA main."table_xinfo"('entries')`} {
		if len(run(t, m, pragma).Rows) != 1 {
			t.Fatalf("read PRAGMA lost its result: %s", pragma)
		}
	}
	if _, err := m.ExecuteSQL(context.Background(), "test", "main", "PRAGMA foreign_keys=OFF", 1); err == nil {
		t.Fatal("private in-memory settings change must be rejected before discarding its sole connection")
	}
	if got := run(t, m, "SELECT * FROM entries"); len(got.Rows) != 1 || got.Rows[0][0] != int64(42) {
		t.Fatalf("PRAGMA discarded the private in-memory database: %+v", got)
	}
}

func TestSQLiteConnectionPragmaSyntax(t *testing.T) {
	for _, statement := range []string{
		"PRAGMA foreign_keys=OFF", "PRAGMA main.foreign_keys(OFF)", `PRAGMA "foreign_keys"=OFF`,
		"PRAGMA foreign_keys /* comment */ =OFF", "/* comment */ PRAGMA foreign_keys=OFF", "PRAGMA main.[query_only]=ON",
	} {
		if !sqliteConnectionPragma(statement) {
			t.Fatalf("settings change was classified as a read: %s", statement)
		}
	}
	for _, statement := range []string{"PRAGMA foreign_keys", "PRAGMA main.foreign_keys", "PRAGMA index_list('entries')", "PRAGMA table_xinfo(entries)"} {
		if sqliteConnectionPragma(statement) {
			t.Fatalf("read PRAGMA unnecessarily pinned a connection: %s", statement)
		}
	}
}

func TestCompletedCleanTransactionsReleasePoolConnections(t *testing.T) {
	for _, end := range []string{"COMMIT", "ROLLBACK"} {
		t.Run(end, func(t *testing.T) {
			m, _ := testManager(t)
			db := m.connections["test"].pools["main"]
			db.SetMaxOpenConns(2)
			for i := 0; i < 20; i++ {
				client := fmt.Sprintf("tab-%d", i)
				sessionSQL(t, m, client, "BEGIN")
				sessionSQL(t, m, client, end)
			}
			if len(m.sessions) != 0 || db.Stats().InUse != 0 {
				t.Fatalf("clean sessions still hold pool connections: sessions=%d stats=%+v", len(m.sessions), db.Stats())
			}
			run(t, m, "SELECT 1")
		})
	}
}

func TestDirtySessionRetainsStateAfterCommit(t *testing.T) {
	m, _ := testManager(t)
	sessionSQL(t, m, "temporary", "CREATE TEMP TABLE private_rows(id INTEGER)")
	sessionSQL(t, m, "temporary", "BEGIN")
	sessionSQL(t, m, "temporary", "INSERT INTO private_rows VALUES(42)")
	sessionSQL(t, m, "temporary", "COMMIT")
	if got := sessionSQL(t, m, "temporary", "SELECT * FROM private_rows"); len(got.Rows) != 1 || got.Rows[0][0] != int64(42) {
		t.Fatalf("commit discarded connection-local objects: %+v", got)
	}
}

func TestQueuedSessionExecutionCanBeCancelled(t *testing.T) {
	query, started := sqliteCancellationQuery(t)
	m, _ := testManager(t)
	sessionSQL(t, m, "busy", "BEGIN")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() {
		_, err := m.Handle(ctx, "/api/query/execute", map[string]any{
			"connectionId": "test", "database": "main", "clientSessionId": "busy", "sql": query,
		})
		first <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("blocking query did not start")
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer waitCancel()
	queued := make(chan error, 1)
	go func() {
		_, err := m.Handle(waitCtx, "/api/query/execute", map[string]any{
			"connectionId": "test", "database": "main", "clientSessionId": "busy", "sql": "SELECT 1",
		})
		queued <- err
	}()
	select {
	case err := <-queued:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("queued execution returned %v", err)
		}
	case <-time.After(time.Second):
		cancel()
		t.Fatal("queued execution waited for the first query after its own deadline")
	}
	select {
	case err := <-first:
		t.Fatalf("cancelling queued execution interrupted the active query: %v", err)
	default:
	}
	cancel()
	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("active query did not stop after cancellation")
	}
}
