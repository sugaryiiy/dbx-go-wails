package server

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/sugaryiiy/dbx-go-wails/internal/database"
)

func TestMySQLObjectSourceBuildKeepsCompoundDefinition(t *testing.T) {
	const source = "CREATE DEFINER='root'@'localhost' PROCEDURE report(IN n INT) BEGIN IF n > 0 THEN SELECT n; END IF; SELECT 2; END"
	for _, draft := range []string{source, "DELIMITER $$\n" + source + "$$\nDELIMITER ;"} {
		result, err := advancedAdminSQL("/api/query/build-executable-object-source-statements", map[string]any{
			"databaseType": "mysql", "objectType": "PROCEDURE", "schema": "app", "name": "report", "source": draft,
		})
		want := []string{"DROP PROCEDURE IF EXISTS `app`.`report`;", strings.Replace(source, "PROCEDURE report", "PROCEDURE `app`.`report`", 1) + ";"}
		if err != nil || !reflect.DeepEqual(result, want) {
			t.Fatalf("source save SQL: %#v, %v", result, err)
		}
	}
	result, err := advancedAdminSQL("/api/query/build-routine-rename-object-source-statements", map[string]any{
		"databaseType": "mysql", "objectType": "PROCEDURE", "schema": "app", "name": "report", "newName": "renamed", "source": source,
	})
	want := []string{strings.Replace(source, "PROCEDURE report", "PROCEDURE `app`.`renamed`", 1) + ";", "DROP PROCEDURE `app`.`report`;"}
	if err != nil || !reflect.DeepEqual(result, want) {
		t.Fatalf("routine rename SQL: %#v, %v", result, err)
	}
	for _, draft := range []string{source + "; DELETE FROM entries;", "CREATE PROCEDURE report() BEGIN SELECT 1;"} {
		if _, err := advancedAdminSQL("/api/query/build-executable-object-source-statements", map[string]any{
			"databaseType": "mysql", "objectType": "PROCEDURE", "schema": "app", "name": "report", "source": draft,
		}); err == nil {
			t.Fatalf("object editor accepted extra SQL or an incomplete definition: %s", draft)
		}
	}
}

func TestMySQLObjectSourceSaveRoundTrip(t *testing.T) {
	dsn := os.Getenv("DBX_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set DBX_TEST_MYSQL_DSN to a disposable loopback MySQL server")
	}
	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(parsed.Addr)
	if err != nil {
		t.Fatal(err)
	}
	requireMCPDisposableHost(t, host)
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	dbName := fmt.Sprintf("dbx_source_save_%d", time.Now().UnixNano())
	mcpLiveObjectExec(t, db, "CREATE DATABASE `"+dbName+"`")
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP DATABASE `"+dbName+"`") })
	s := testServer(t, t.TempDir())
	const connectionID = "source-save"
	if err := s.database.Connect(t.Context(), database.Config{
		ID: connectionID, Type: "mysql", Host: host, Port: port, Username: parsed.User, Password: parsed.Passwd, Database: dbName,
	}); err != nil {
		t.Fatal(err)
	}
	cookie := loginTest(t, s)
	execute := func(statement string) map[string]any {
		t.Helper()
		return value(t, call(s, "POST", "/api/query/execute", map[string]any{
			"connectionId": connectionID, "database": dbName, "sql": statement,
		}, cookie, "")).(map[string]any)
	}
	execute("CREATE TABLE entries(id INT)")
	for _, fixture := range []struct{ kind, name, definition, before, after string }{
		{"PROCEDURE", "report", "(IN n INT) BEGIN SELECT n AS value; END", "BEGIN SELECT n AS value; END", "BEGIN IF n > 0 THEN BEGIN SELECT CASE WHEN n > 1 THEN n * 2 ELSE 0 END AS value; END; ELSE SELECT 0 AS value; END IF; END"},
		{"FUNCTION", "twice", "(n INT) RETURNS INT DETERMINISTIC RETURN n", "RETURN n", "BEGIN RETURN IF(n > 0, n * 2, 0); END"},
		{"TRIGGER", "changed", "BEFORE INSERT ON entries FOR EACH ROW SET NEW.id = 1", "SET NEW.id = 1", "BEGIN SET NEW.id = 2; SET NEW.id = NEW.id * 2; END"},
		{"EVENT", "disabled_report", "ON SCHEDULE EVERY 1 DAY DISABLE DO SELECT 1", "DO SELECT 1", "DO BEGIN SELECT 2; SELECT 3; END"},
	} {
		t.Run(fixture.kind, func(t *testing.T) {
			prefix := "CREATE " + fixture.kind + " `" + fixture.name + "` "
			execute(prefix + fixture.definition)
			readSource := func() string {
				t.Helper()
				result := value(t, call(s, "POST", "/api/schema/object-source", map[string]any{
					"connection_id": connectionID, "database": dbName, "schema": dbName, "table": fixture.name, "object_type": fixture.kind,
				}, cookie, "")).(map[string]any)
				return result["source"].(string)
			}
			draft := strings.Replace(readSource(), fixture.before, fixture.after, 1)
			if !strings.Contains(draft, fixture.after) {
				t.Fatalf("fixture did not replace the stored definition: %s", draft)
			}
			statements := value(t, call(s, "POST", "/api/query/build-executable-object-source-statements", map[string]any{
				"input": map[string]any{"databaseType": "mysql", "objectType": fixture.kind, "schema": dbName, "name": fixture.name, "source": draft},
			}, cookie, "")).([]any)
			if len(statements) != 2 {
				t.Fatalf("expected one DROP and one whole CREATE: %#v", statements)
			}
			for _, statement := range statements {
				execute(statement.(string))
			}
			if source := readSource(); !strings.Contains(source, fixture.after) {
				t.Fatalf("saved definition did not round trip: %s", source)
			}
		})
	}
	if result := execute("CALL report(7)"); !reflect.DeepEqual(result["rows"], []any{[]any{float64(14)}}) {
		t.Fatalf("saved procedure behavior: %#v", result)
	}
	if result := execute("SELECT twice(7)"); !reflect.DeepEqual(result["rows"], []any{[]any{float64(14)}}) {
		t.Fatalf("saved function behavior: %#v", result)
	}
	execute("INSERT INTO entries VALUES(1)")
	if result := execute("SELECT id FROM entries"); !reflect.DeepEqual(result["rows"], []any{[]any{float64(4)}}) {
		t.Fatalf("saved trigger behavior: %#v", result)
	}
	for _, fixture := range []struct {
		extra  map[string]any
		status int
	}{{map[string]any{"readOnly": true}, 400}, {map[string]any{"useTransaction": true}, 501}} {
		request := map[string]any{"connectionId": connectionID, "database": dbName, "sql": "CREATE PROCEDURE blocked() BEGIN SELECT 1; END"}
		for key, val := range fixture.extra {
			request[key] = val
		}
		if response := call(s, "POST", "/api/query/execute", request, cookie, ""); response.Code != fixture.status {
			t.Fatalf("routine creation bypassed a write/transaction guard: %d %s", response.Code, response.Body)
		}
	}
}
