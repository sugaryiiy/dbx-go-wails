package server

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func mcpLiveObjectServer(t *testing.T, config map[string]any) *Server {
	t.Helper()
	s := testServer(t, t.TempDir())
	config["id"], config["save_password"] = "allowed", true
	config["connect_timeout_secs"], config["query_timeout_secs"] = 5, 15
	if err := s.update(func(st *diskState) error { st.Connections = []map[string]any{config}; return nil }); err != nil {
		t.Fatal(err)
	}
	mcpAllow(t, s)
	return s
}

func mcpLiveObjectExec(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, statement); err != nil {
		t.Fatalf("Disposable object fixture failed: %v", err)
	}
}

func requireMCPDisposableHost(t *testing.T, host string) {
	t.Helper()
	if host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
		t.Fatal("MCP object integration tests require a disposable loopback database")
	}
}

func TestMCPMySQLRoutinesEventsAreMetadataOnly(t *testing.T) {
	dsn := os.Getenv("DBX_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set DBX_TEST_MYSQL_DSN to a disposable loopback MySQL server")
	}
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(config.Addr)
	if err != nil {
		t.Fatal(err)
	}
	requireMCPDisposableHost(t, host)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	database := fmt.Sprintf("dbx_mcp_objects_%d", time.Now().UnixNano())
	outside := database + "_outside"
	quoted := "`" + database + "`"
	mcpLiveObjectExec(t, db, "CREATE DATABASE "+quoted)
	defer db.ExecContext(context.Background(), "DROP DATABASE "+quoted)
	mcpLiveObjectExec(t, db, "CREATE DATABASE `"+outside+"`")
	defer db.ExecContext(context.Background(), "DROP DATABASE `"+outside+"`")
	for _, statement := range []string{
		"CREATE TABLE " + quoted + ".sentinel(id INTEGER)",
		"CREATE TABLE " + quoted + ".items(id INTEGER)",
		"CREATE PROCEDURE " + quoted + ".demo_proc() BEGIN INSERT INTO " + quoted + ".sentinel VALUES (1); END",
		"CREATE PROCEDURE " + quoted + ".root_identity() BEGIN SELECT 1; END",
		"CREATE FUNCTION " + quoted + ".demo_func(n INTEGER) RETURNS INTEGER DETERMINISTIC NO SQL RETURN n+1",
		"CREATE TRIGGER " + quoted + ".item_changed BEFORE INSERT ON " + quoted + ".items FOR EACH ROW SET NEW.id=NEW.id",
		"CREATE EVENT " + quoted + ".demo_event ON SCHEDULE EVERY 1 DAY STARTS CURRENT_TIMESTAMP + INTERVAL 1 DAY ON COMPLETION PRESERVE DISABLE COMMENT 'metadata fixture' DO INSERT INTO " + quoted + ".sentinel VALUES (2)",
		"CREATE PROCEDURE `" + outside + "`.outside_proc() BEGIN SELECT 1; END",
	} {
		mcpLiveObjectExec(t, db, statement)
	}
	s := mcpLiveObjectServer(t, map[string]any{"db_type": "mysql", "host": host, "port": integer(port, 0), "username": config.User, "password": config.Passwd, "database": database})
	args := map[string]any{"connection_id": "allowed", "database": database, "schema": database}
	page, objects := objectMCPPage(t, mcpCall(t, s, "dbx_list_routines", args))
	if len(objects) != 3 || integer(page["total"], -1) != 3 {
		t.Fatalf("MySQL procedure/function missing: %#v", page)
	}
	foundIdentity := false
	for _, object := range objects {
		if object["name"] == "root_identity" && object["schema"] == database {
			foundIdentity = true
		}
	}
	if !foundIdentity {
		t.Fatalf("Connection username redaction corrupted a legal object identity: %#v", objects)
	}
	procedureArgs := cloneMap(args)
	procedureArgs["routine_type"] = "PROCEDURE"
	procedureArgs["filter"] = "demo_proc"
	_, procedures := objectMCPPage(t, mcpCall(t, s, "dbx_list_routines", procedureArgs))
	if len(procedures) != 1 || procedures[0]["name"] != "demo_proc" {
		t.Fatal(procedures)
	}
	allArgs := cloneMap(args)
	delete(allArgs, "schema")
	allArgs["all_schemas"] = true
	_, objects = objectMCPPage(t, mcpCall(t, s, "dbx_list_objects", allArgs))
	for _, object := range objects {
		if object["schema"] != database || object["name"] == "outside_proc" {
			t.Fatalf("MySQL all_schemas escaped selected database: %#v", object)
		}
	}
	eventPage, events := objectMCPPage(t, mcpCall(t, s, "dbx_list_events", args))
	if len(events) != 1 || events[0]["name"] != "demo_event" || events[0]["object_type"] != "EVENT" {
		t.Fatalf("MySQL scheduled event missing: %#v", eventPage)
	}
	for _, object := range []struct{ name, kind string }{{"demo_proc", "PROCEDURE"}, {"demo_func", "FUNCTION"}, {"demo_event", "EVENT"}, {"item_changed", "TRIGGER"}} {
		sourceArgs := cloneMap(args)
		sourceArgs["name"], sourceArgs["object_type"] = object.name, object.kind
		source := mcpCall(t, s, "dbx_get_object_source", sourceArgs).(map[string]any)
		if source["editable"] != false || source["source_truncated"] != false || !strings.Contains(strings.ToUpper(str(source, "source")), object.kind) {
			t.Fatalf("Missing %s source: %#v", object.kind, source)
		}
	}
	routineSourceArgs := cloneMap(args)
	routineSourceArgs["name"], routineSourceArgs["routine_type"] = "demo_proc", "PROCEDURE"
	aliasSource := mcpCall(t, s, "dbx_get_routine_source", routineSourceArgs).(map[string]any)
	if aliasSource["object_type"] != "PROCEDURE" || !strings.Contains(str(aliasSource, "source"), "INSERT INTO") {
		t.Fatalf("Routine source alias lost body: %#v", aliasSource)
	}
	eventArgs := cloneMap(args)
	eventArgs["name"] = "demo_event"
	info := mcpCall(t, s, "dbx_get_event_info", eventArgs).(map[string]any)
	if info["status"] != "DISABLED" || info["event_type"] != "RECURRING" || info["comment"] != "metadata fixture" || !strings.Contains(str(info, "event_definition"), "INSERT INTO") {
		t.Fatalf("Scheduled event metadata missing: %#v", info)
	}
	for _, name := range objectMCPTools {
		blocked := cloneMap(args)
		blocked["schema"] = outside
		if name == "dbx_get_object_source" {
			blocked["name"], blocked["object_type"] = "outside_proc", "PROCEDURE"
		} else if name == "dbx_get_routine_source" {
			blocked["name"], blocked["routine_type"] = "outside_proc", "PROCEDURE"
		} else if name == "dbx_get_event_info" {
			blocked["name"] = "demo_event"
		}
		if _, err := s.MCPBackend().Call(t.Context(), name, blocked); err == nil || !strings.Contains(err.Error(), "schema must equal") {
			t.Fatalf("%s let schema escape MySQL database: %v", name, err)
		}
	}
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+quoted+".sentinel").Scan(&count); err != nil || count != 0 {
		t.Fatalf("Reading procedure/event source executed its body: count=%d err=%v", count, err)
	}
	var eventStatus string
	if err := db.QueryRowContext(t.Context(), "SELECT status FROM information_schema.events WHERE event_schema=? AND event_name='demo_event'", database).Scan(&eventStatus); err != nil || eventStatus != "DISABLED" {
		t.Fatalf("Reading event metadata changed scheduling: status=%s err=%v", eventStatus, err)
	}
}

func TestMCPPostgresRoutineAndTriggerIdentity(t *testing.T) {
	for _, fixture := range []struct{ name, env string }{{"legacy", "DBX_TEST_POSTGRES_LEGACY_DSN"}, {"modern", "DBX_TEST_POSTGRES_DSN"}} {
		t.Run(fixture.name, func(t *testing.T) {
			dsn := os.Getenv(fixture.env)
			if dsn == "" {
				t.Skip("set " + fixture.env + " to a disposable loopback PostgreSQL server")
			}
			endpoint, err := url.Parse(dsn)
			if err != nil || endpoint.User == nil {
				t.Fatal("PostgreSQL object tests require a URL DSN")
			}
			requireMCPDisposableHost(t, endpoint.Hostname())
			password, _ := endpoint.User.Password()
			database := strings.TrimPrefix(endpoint.Path, "/")
			db, err := sql.Open("pgx", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			schema := fmt.Sprintf("dbx_mcp_objects_%d", time.Now().UnixNano())
			quoted := `"` + schema + `"`
			mcpLiveObjectExec(t, db, "CREATE SCHEMA "+quoted)
			defer db.ExecContext(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
			for _, statement := range []string{
				"CREATE TABLE " + quoted + ".sentinel(id INTEGER)",
				"CREATE TABLE " + quoted + ".items(id INTEGER)",
				"CREATE TABLE " + quoted + ".other_items(id INTEGER)",
				"CREATE SEQUENCE " + quoted + ".demo_sequence",
				"CREATE FUNCTION " + quoted + ".demo() RETURNS integer LANGUAGE SQL AS 'SELECT 1'",
				"CREATE FUNCTION " + quoted + ".demo(n integer) RETURNS integer LANGUAGE SQL AS 'SELECT $1'",
				"CREATE FUNCTION " + quoted + ".touch() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN INSERT INTO " + quoted + ".sentinel VALUES (1); RETURN NEW; END'",
				"CREATE TRIGGER item_changed BEFORE INSERT ON " + quoted + ".items FOR EACH ROW EXECUTE PROCEDURE " + quoted + ".touch()",
				"CREATE TRIGGER item_changed BEFORE INSERT ON " + quoted + ".other_items FOR EACH ROW EXECUTE PROCEDURE " + quoted + ".touch()",
			} {
				mcpLiveObjectExec(t, db, statement)
			}
			arguments := make([]string, 50)
			for i := range arguments {
				arguments[i] = fmt.Sprintf("argument_%03d integer", i)
			}
			longSignature := strings.Join(arguments, ", ")
			mcpLiveObjectExec(t, db, "CREATE FUNCTION "+quoted+".long_signature("+longSignature+") RETURNS integer LANGUAGE SQL AS 'SELECT $1'")
			if fixture.name == "modern" {
				mcpLiveObjectExec(t, db, "CREATE PROCEDURE "+quoted+".demo_proc() LANGUAGE SQL AS 'INSERT INTO "+quoted+".sentinel VALUES (2)'")
			}
			s := mcpLiveObjectServer(t, map[string]any{"db_type": "postgres", "host": endpoint.Hostname(), "port": integer(endpoint.Port(), 5432), "username": endpoint.User.Username(), "password": password, "database": database})
			args := map[string]any{"connection_id": "allowed", "database": database, "schema": schema}
			page, routines := objectMCPPage(t, mcpCall(t, s, "dbx_list_routines", args))
			expectedRoutines := 4
			if fixture.name == "modern" {
				expectedRoutines++
			}
			if len(routines) != expectedRoutines || integer(page["total"], -1) != expectedRoutines {
				t.Fatalf("PostgreSQL routine catalog missing: %#v", page)
			}
			for _, routine := range routines {
				if routine["schema"] != schema {
					t.Fatalf("Explicit routine schema ignored: %#v", routine)
				}
				if routine["name"] == "long_signature" && routine["signature"] != longSignature {
					t.Fatalf("Routine identity was truncated and cannot select its source: %#v", routine)
				}
			}
			sourceArgs := cloneMap(args)
			sourceArgs["name"], sourceArgs["object_type"] = "demo", "FUNCTION"
			if _, err := s.MCPBackend().Call(t.Context(), "dbx_get_object_source", sourceArgs); err == nil || !strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
				t.Fatalf("Overload source silently selected an arbitrary function: %v", err)
			}
			for _, routine := range routines {
				sourceArgs := cloneMap(args)
				sourceArgs["name"], sourceArgs["routine_type"], sourceArgs["signature"] = routine["name"], routine["object_type"], routine["signature"]
				source := mcpCall(t, s, "dbx_get_routine_source", sourceArgs).(map[string]any)
				if source["editable"] != false || !strings.Contains(strings.ToUpper(str(source, "source")), str(routine, "object_type")) {
					t.Fatalf("Routine identity did not select source: %#v", source)
				}
				if routine["name"] == "demo" && routine["signature"] == "" && !strings.Contains(str(source, "source"), "demo()") {
					t.Fatalf("Explicit empty signature was discarded: %#v", source)
				}
			}
			page, triggers := objectMCPPage(t, mcpCall(t, s, "dbx_list_triggers", args))
			if len(triggers) != 2 || triggers[0]["parent_name"] == triggers[1]["parent_name"] {
				t.Fatalf("Per-table trigger identities collapsed: %#v", page)
			}
			sourceArgs = cloneMap(args)
			sourceArgs["name"], sourceArgs["object_type"] = "item_changed", "TRIGGER"
			if _, err := s.MCPBackend().Call(t.Context(), "dbx_get_object_source", sourceArgs); err == nil || !strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
				t.Fatalf("Duplicate trigger source silently selected one table: %v", err)
			}
			for _, trigger := range triggers {
				sourceArgs["relation_name"] = trigger["parent_name"]
				source := mcpCall(t, s, "dbx_get_object_source", sourceArgs).(map[string]any)
				if !strings.Contains(str(source, "source"), "."+str(trigger, "parent_name")) {
					t.Fatalf("Trigger source came from the wrong table: %#v", source)
				}
			}
			triggerArgs := cloneMap(args)
			triggerArgs["relation_name"] = "items"
			_, filtered := objectMCPPage(t, mcpCall(t, s, "dbx_list_triggers", triggerArgs))
			if len(filtered) != 1 || filtered[0]["parent_name"] != "items" {
				t.Fatalf("Trigger parent filter ignored: %#v", filtered)
			}
			var count int
			if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+quoted+".sentinel").Scan(&count); err != nil || count != 0 {
				t.Fatalf("Reading PostgreSQL routine/trigger source executed a body: count=%d err=%v", count, err)
			}
		})
	}
}
