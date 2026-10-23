package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAffectedRowGuardRollsBackRacedKeylessBatch(t *testing.T) {
	for _, mutation := range []string{"UPDATE entries SET value='changed' WHERE value='original'", "DELETE FROM entries WHERE value='original'"} {
		t.Run(mutation, func(t *testing.T) {
			m, _ := testManager(t)
			run(t, m, "CREATE TABLE entries(value TEXT)")
			run(t, m, "CREATE TABLE audit(value TEXT)")
			run(t, m, "INSERT INTO entries VALUES('original')")
			if got := run(t, m, "SELECT count(*) FROM entries WHERE value='original'"); got.Rows[0][0] != int64(1) {
				t.Fatal("preflight did not identify one row")
			}
			// Another writer duplicates the row after the UI's friendly count
			// check, before its actual save reaches the engine.
			run(t, m, "INSERT INTO entries VALUES('original')")
			_, err := m.Handle(context.Background(), "/api/query/execute-in-transaction", map[string]any{
				"connectionId": "test", "database": "main",
				"statements":                  []string{"INSERT INTO audit VALUES('must roll back')", mutation},
				"maxAffectedRowsPerStatement": []any{nil, 1},
			})
			if err == nil || !strings.Contains(err.Error(), "statement 2 affected 2 rows") {
				t.Fatalf("duplicate mutation did not fail its affected-row guard: %v", err)
			}
			if got := run(t, m, "SELECT count(*) FROM entries WHERE value='original'"); got.Rows[0][0] != int64(2) {
				t.Fatalf("failed guarded mutation changed rows: %+v", got)
			}
			if got := run(t, m, "SELECT * FROM audit"); len(got.Rows) != 0 {
				t.Fatalf("failed guarded batch left an earlier statement committed: %+v", got)
			}
		})
	}
}

func TestAffectedRowGuardForcesTransactionAndCommitsSuccess(t *testing.T) {
	m, _ := testManager(t)
	run(t, m, "CREATE TABLE entries(value TEXT)")
	run(t, m, "INSERT INTO entries VALUES('original')")
	result := handle(t, m, "/api/query/execute-multi", map[string]any{
		"connectionId": "test", "database": "main",
		"sql":                         "UPDATE entries SET value='changed' WHERE value='original'; INSERT INTO entries VALUES('new')",
		"maxAffectedRowsPerStatement": []any{1, nil},
	}).([]QueryResult)
	if len(result) != 2 || result[0].AffectedRows != 1 || result[1].AffectedRows != 1 {
		t.Fatalf("guarded execution results: %+v", result)
	}
	if got := run(t, m, "SELECT value FROM entries ORDER BY value"); !reflect.DeepEqual(got.Rows, [][]any{{"changed"}, {"new"}}) {
		t.Fatalf("successful guarded batch was not committed: %+v", got)
	}
}

func TestAffectedRowGuardPreservesEarlierManualTransactionWrites(t *testing.T) {
	for _, failure := range []string{
		"DELETE FROM entries WHERE value='duplicate'",
		"INSERT INTO missing_table VALUES(1)",
	} {
		t.Run(failure, func(t *testing.T) {
			m, _ := testManager(t)
			run(t, m, "CREATE TABLE entries(value TEXT)")
			run(t, m, "INSERT INTO entries VALUES('duplicate'),('duplicate')")
			id := handle(t, m, "/api/transaction/begin", map[string]any{"connectionId": "test", "database": "main"}).(string)
			handle(t, m, "/api/transaction/execute", map[string]any{"txnSessionId": id, "sql": "INSERT INTO entries VALUES('prior edit')"})
			_, err := m.Handle(context.Background(), "/api/transaction/execute", map[string]any{
				"txnSessionId": id, "statements": []string{"INSERT INTO entries VALUES('batch edit')", failure},
				"maxAffectedRowsPerStatement": []any{nil, 1},
			})
			if err == nil {
				t.Fatal("expected guarded batch failure")
			}
			inside := handle(t, m, "/api/transaction/execute", map[string]any{"txnSessionId": id, "sql": "SELECT value FROM entries ORDER BY value"}).([]QueryResult)
			want := [][]any{{"duplicate"}, {"duplicate"}, {"prior edit"}}
			if !reflect.DeepEqual(inside[0].Rows, want) {
				t.Fatalf("savepoint rollback lost earlier edits or retained this batch: %+v", inside)
			}
			handle(t, m, "/api/transaction/execute", map[string]any{
				"txnSessionId": id, "sql": "UPDATE entries SET value='saved edit' WHERE value='prior edit'", "maxAffectedRowsPerStatement": []any{1},
			})
			handle(t, m, "/api/transaction/commit", map[string]any{"txnSessionId": id})
			want[2][0] = "saved edit"
			if got := run(t, m, "SELECT value FROM entries ORDER BY value"); !reflect.DeepEqual(got.Rows, want) {
				t.Fatalf("manual transaction could not commit after guarded failure: %+v", got)
			}
		})
	}
}

func TestAffectedRowGuardRejectsUnsafeOptionsBeforeMutation(t *testing.T) {
	for _, patch := range []map[string]any{
		{"maxAffectedRowsPerStatement": []any{}},
		{"maxAffectedRowsPerStatement": []any{-1}},
		{"maxAffectedRowsPerStatement": []any{0.5}},
		{"continueOnError": true},
		{"pageSize": 1},
		{"sql": "DELETE FROM entries; COMMIT", "maxAffectedRowsPerStatement": []any{1, nil}},
		{"sql": "CREATE TABLE forbidden(id INTEGER)"},
	} {
		m, _ := testManager(t)
		run(t, m, "CREATE TABLE entries(value TEXT)")
		run(t, m, "INSERT INTO entries VALUES('kept')")
		p := map[string]any{"connectionId": "test", "database": "main", "sql": "DELETE FROM entries", "maxAffectedRowsPerStatement": []any{1}}
		for key, value := range patch {
			p[key] = value
		}
		if _, err := m.Handle(context.Background(), "/api/query/execute-multi", p); err == nil {
			t.Fatalf("accepted unsafe guarded request: %+v", patch)
		}
		if len(run(t, m, "SELECT * FROM entries").Rows) != 1 {
			t.Fatalf("invalid options mutated the database: %+v", patch)
		}
	}
	manager, _ := fixtureNativeManager(t)
	if _, err := manager.Handle(context.Background(), "/api/query/execute-multi", map[string]any{"connectionId": "native", "sql": "DELETE FROM entries", "maxAffectedRowsPerStatement": []any{1}}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("native driver silently ignored affected-row guard: %v", err)
	}
}

func TestMySQLAffectedRowGuardTargets(t *testing.T) {
	for _, fixture := range []struct{ sql, database, table string }{
		{"UPDATE `app db`.`ta``ble` SET value=1 WHERE value=0", "app db", "ta`ble"},
		{"DELETE FROM app.entries WHERE value=0", "app", "entries"},
		{"INSERT INTO `entries` (`value`) VALUES (1)", "", "entries"},
	} {
		database, table, err := mysqlAffectedRowTarget(fixture.sql)
		if err != nil || database != fixture.database || table != fixture.table {
			t.Fatalf("target for %s: %s.%s, %v", fixture.sql, database, table, err)
		}
	}
	for _, source := range []string{
		"UPDATE app.entries JOIN app.other USING(id) SET value=1",
		"UPDATE app.entries, app.other SET value=1",
		"DELETE a FROM app.entries a JOIN app.other b ON a.id=b.id",
		"WITH rows AS (SELECT 1) UPDATE app.entries SET value=1",
		"INSERT INTO app.entries SELECT * FROM app.other",
	} {
		if _, _, err := mysqlAffectedRowTarget(source); err == nil {
			t.Fatalf("accepted unsupported MySQL guard target: %s", source)
		}
	}
}

func TestMySQLAffectedRowGuardChecksAllEnginesBeforeWriting(t *testing.T) {
	connector := &objectSearchConnector{}
	connector.query = func(_ context.Context, statement string, args []driver.NamedValue) (driver.Rows, error) {
		switch {
		case strings.HasPrefix(statement, "SELECT 1 FROM "):
			return &objectSearchRows{columns: []string{"1"}}, nil
		case strings.Contains(statement, "information_schema.tables"):
			engine := "InnoDB"
			if args[1].Value == "unsafe" {
				engine = "MyISAM"
			}
			return &objectSearchRows{columns: []string{"ENGINE"}, values: [][]driver.Value{{engine}}}, nil
		default:
			t.Fatalf("unexpected engine-check query: %s", statement)
			return nil, errors.New("unexpected query")
		}
	}
	m := objectSearchManager(t, "mysql", "app", connector)
	m.connections["search"].config.ReadOnly = false
	_, err := m.Handle(context.Background(), "/api/query/execute-multi", map[string]any{
		"connectionId": "search", "database": "app",
		"sql": "INSERT INTO app.safe VALUES(1); DELETE FROM app.unsafe WHERE value=1", "maxAffectedRowsPerStatement": []any{nil, 1},
	})
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "InnoDB") {
		t.Fatalf("nontransactional engine was not rejected before mutation: %v", err)
	}
	if len(connector.calls) != 4 || connector.calls[0] != "SELECT 1 FROM `app`.`safe` WHERE 1=0" || connector.calls[2] != "SELECT 1 FROM `app`.`unsafe` WHERE 1=0" {
		t.Fatalf("target metadata locks were not acquired before engine checks: %+v", connector.calls)
	}
}

// PostgreSQL rejects ordinary commands after a statement error until rollback.
// Model that behavior to ensure search_path cleanup cannot discard the manual
// transaction before the guarded savepoint gets a chance to roll back.
type failedGuardConnector struct {
	statements []string
	aborted    bool
	localPath  bool
}

func (c *failedGuardConnector) Connect(context.Context) (driver.Conn, error) {
	return &failedGuardConn{fixture: c}, nil
}
func (*failedGuardConnector) Driver() driver.Driver { return objectSearchDriver{} }

type failedGuardConn struct{ fixture *failedGuardConnector }

func (*failedGuardConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*failedGuardConn) Close() error              { return nil }
func (*failedGuardConn) Begin() (driver.Tx, error) { return objectSearchTx{}, nil }
func (c *failedGuardConn) ExecContext(_ context.Context, statement string, args []driver.NamedValue) (driver.Result, error) {
	f := c.fixture
	f.statements = append(f.statements, statement)
	if strings.HasPrefix(statement, "ROLLBACK TO SAVEPOINT ") {
		f.aborted = false
	} else if f.aborted {
		return nil, errors.New("current transaction is aborted")
	} else if strings.HasPrefix(statement, "SELECT set_config(") {
		f.localPath, _ = args[1].Value.(bool)
	} else if strings.HasPrefix(statement, "UPDATE ") {
		f.aborted = true
		return nil, errors.New("fixture mutation failed")
	}
	return driver.RowsAffected(1), nil
}

func TestPostgresGuardedFailureRollsBackBeforeSearchPathCleanup(t *testing.T) {
	fixture := &failedGuardConnector{}
	db := sql.OpenDB(fixture)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager()
	t.Cleanup(func() { _ = m.Close() })
	m.connections["test"] = &connection{config: Config{ID: "test", Type: "postgres", Database: "app", DefaultSchema: "public"}, pools: map[string]*sql.DB{"app": db}}
	session := &querySession{conn: conn, connectionID: "test", database: "app", clientID: "manual-fixture", inTransaction: true}
	m.sessions[sessionKey("test", "app", session.clientID)] = session
	_, err = m.Handle(context.Background(), "/api/transaction/execute", map[string]any{
		"txnSessionId": session.clientID, "sql": "UPDATE entries SET id=1 WHERE id=0", "maxAffectedRowsPerStatement": []any{1},
	})
	if err == nil || !strings.Contains(err.Error(), "fixture mutation failed") {
		t.Fatalf("expected the original SQL error: %v", err)
	}
	if !fixture.localPath || fixture.aborted || session.closed || !session.inTransaction {
		t.Fatalf("failed save destroyed its surrounding transaction: localPath=%v aborted=%v sessionClosed=%v", fixture.localPath, fixture.aborted, session.closed)
	}
	for _, statement := range fixture.statements {
		if statement == "RESET search_path" {
			t.Fatal("session-wide search_path cleanup ran before savepoint rollback")
		}
	}
	if len(fixture.statements) != 5 || !strings.HasPrefix(fixture.statements[3], "ROLLBACK TO SAVEPOINT ") || !strings.HasPrefix(fixture.statements[4], "RELEASE SAVEPOINT ") {
		t.Fatalf("guarded SQL error did not roll back and release its savepoint: %+v", fixture.statements)
	}
}
