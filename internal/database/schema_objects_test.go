package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func catalogCall(t *testing.T, m *Manager, database, schema, route string, extra map[string]any) any {
	t.Helper()
	p := map[string]any{"connection_id": "fixture", "database": database, "schema": schema}
	for key, value := range extra {
		p[key] = value
	}
	result, err := m.Handle(context.Background(), "/api/schema/"+route, p)
	if err != nil {
		t.Fatalf("%s: %v", route, err)
	}
	return result
}

func checkObjectCounts(t *testing.T, objects []record, expected map[string]int) {
	t.Helper()
	counts := map[string]int{}
	for _, object := range objects {
		counts[valueString(object["object_type"])]++
	}
	if !reflect.DeepEqual(counts, expected) {
		t.Fatalf("object browser counts: got %v, want %v", counts, expected)
	}
}

func TestSQLiteObjectCatalogIncludesTriggers(t *testing.T) {
	m, _ := testManager(t)
	m.mu.Lock()
	m.connections["fixture"] = m.connections["test"]
	m.mu.Unlock()
	run(t, m, "CREATE TABLE items(id INTEGER)")
	run(t, m, "CREATE VIEW item_view AS SELECT * FROM items")
	run(t, m, "CREATE TRIGGER item_changed AFTER INSERT ON items BEGIN SELECT 1; END")
	objects := catalogCall(t, m, "main", "main", "objects", nil).([]record)
	checkObjectCounts(t, objects, map[string]int{"TABLE": 1, "VIEW": 1, "TRIGGER": 1})
	checkObjectCounts(t, catalogCall(t, m, "main", "main", "objects", map[string]any{"object_types": " trigger "}).([]record), map[string]int{"TRIGGER": 1})
	if len(catalogCall(t, m, "main", "main", "tables", nil).([]record)) != 2 {
		t.Fatal("table route included a trigger")
	}
}

func TestSQLiteObjectSourceDistinguishesSameNamedTableAndTrigger(t *testing.T) {
	m, _ := testManager(t)
	m.mu.Lock()
	m.connections["fixture"] = m.connections["test"]
	m.mu.Unlock()
	run(t, m, "CREATE TABLE shared(id INTEGER)")
	run(t, m, "CREATE TRIGGER shared AFTER INSERT ON shared BEGIN SELECT 1; END")
	for _, kind := range []string{"TABLE", "TRIGGER"} {
		value := catalogCall(t, m, "main", "main", "object-source", map[string]any{"table": "shared", "object_type": kind}).(record)
		if !strings.HasPrefix(valueString(value["source"]), "CREATE "+kind+" ") {
			t.Fatalf("%s source returned a different object: %#v", kind, value)
		}
	}
	if source := catalogCall(t, m, "main", "main", "ddl", map[string]any{"table": "shared"}).(string); !strings.HasPrefix(source, "CREATE TABLE ") {
		t.Fatalf("table DDL returned a trigger: %s", source)
	}
}

func TestMySQLObjectCatalog(t *testing.T) {
	dsn := os.Getenv("DBX_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set DBX_TEST_MYSQL_DSN to a disposable MySQL server; this test creates a database")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager()
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	database := fmt.Sprintf("dbx_catalog_%d", time.Now().UnixNano())
	q := quoteIdentifier(database, "mysql")
	pgTestExec(t, ctx, db, "CREATE DATABASE "+q)
	defer db.ExecContext(context.Background(), "DROP DATABASE "+q)
	m.connections["fixture"] = &connection{config: Config{ID: "fixture", Type: "mysql", Database: database}, pools: map[string]*sql.DB{database: db}}
	for _, statement := range []string{
		"CREATE TABLE " + q + ".items(id integer)",
		"CREATE VIEW " + q + ".item_view AS SELECT * FROM " + q + ".items",
		"CREATE PROCEDURE " + q + ".demo_proc(IN n integer) BEGIN SELECT n; END",
		"CREATE FUNCTION " + q + ".demo_func(n integer) RETURNS integer DETERMINISTIC NO SQL RETURN n+1",
		"CREATE TRIGGER " + q + ".item_changed BEFORE INSERT ON " + q + ".items FOR EACH ROW SET NEW.id=NEW.id",
		"CREATE EVENT " + q + ".demo_event ON SCHEDULE EVERY 1 DAY STARTS CURRENT_TIMESTAMP + INTERVAL 1 DAY ON COMPLETION PRESERVE DISABLE COMMENT 'disabled test event' DO SELECT 1",
	} {
		pgTestExec(t, ctx, db, statement)
	}
	objects := catalogCall(t, m, database, database, "objects", nil).([]record)
	checkObjectCounts(t, objects, map[string]int{"TABLE": 1, "VIEW": 1, "PROCEDURE": 1, "FUNCTION": 1, "TRIGGER": 1, "EVENT": 1})
	for _, object := range objects {
		kind, name := valueString(object["object_type"]), valueString(object["name"])
		filtered := catalogCall(t, m, database, database, "objects", map[string]any{"object_types": strings.ToLower(kind), "filter": name}).([]record)
		if len(filtered) != 1 || filtered[0]["name"] != name || filtered[0]["schema"] != database {
			t.Fatalf("filtered %s: %#v", kind, filtered)
		}
		source := catalogCall(t, m, database, database, "object-source", map[string]any{"table": name, "object_type": kind}).(record)
		if !strings.Contains(strings.ToUpper(valueString(source["source"])), kind) {
			t.Fatalf("missing %s source: %#v", kind, source)
		}
	}
	for offset, object := range objects {
		page := catalogCall(t, m, database, database, "objects", map[string]any{"limit": 1, "offset": offset}).([]record)
		if len(page) != 1 || !reflect.DeepEqual(page[0], object) {
			t.Fatal("combined object pagination is unstable")
		}
	}
	event := catalogCall(t, m, database, database, "event-info", map[string]any{"table": "demo_event"}).(record)
	if event["status"] != "DISABLED" || event["event_definition"] != "SELECT 1" || event["comment"] != "disabled test event" {
		t.Fatalf("event details: %#v", event)
	}
	checkObjectCounts(t, catalogCall(t, m, database, database, "completion-objects", map[string]any{"object_types": "FUNCTION,PROCEDURE"}).([]record), map[string]int{"FUNCTION": 1, "PROCEDURE": 1})
	if objects := catalogCall(t, m, database, database, "objects", map[string]any{"filter": "' OR 1=1 --"}).([]record); len(objects) != 0 {
		t.Fatal("filter was interpreted as SQL")
	}
	search := catalogCall(t, m, "", "", "search-object-sources", map[string]any{"query": "SELECT", "databases": []string{database}}).(ObjectSourceSearchResult)
	if len(search.Matches) != 2 || search.Truncated || len(search.Warnings) != 0 {
		t.Fatalf("procedure/event definition search: %#v", search)
	}
	for _, match := range search.Matches {
		if match.Database != database || match.ObjectType != "PROCEDURE" && match.ObjectType != "EVENT" {
			t.Fatalf("search returned an unexpected object: %#v", match)
		}
	}
}

func TestPostgresObjectCatalog(t *testing.T) {
	for _, fixture := range []struct{ name, env string }{{"legacy", "DBX_TEST_POSTGRES_LEGACY_DSN"}, {"modern", "DBX_TEST_POSTGRES_DSN"}} {
		t.Run(fixture.name, func(t *testing.T) {
			dsn := os.Getenv(fixture.env)
			if dsn == "" {
				t.Skip("set " + fixture.env + " to a disposable PostgreSQL server")
			}
			db, err := sql.Open("pgx", dsn)
			if err != nil {
				t.Fatal(err)
			}
			m := NewManager()
			defer m.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			schema := fmt.Sprintf("dbx_objects_%d", time.Now().UnixNano())
			q := pgIdent(schema)
			pgTestExec(t, ctx, db, "CREATE SCHEMA "+q)
			defer db.ExecContext(context.Background(), "DROP SCHEMA "+q+" CASCADE")
			m.connections["fixture"] = &connection{config: Config{ID: "fixture", Type: "postgres", Database: "postgres"}, pools: map[string]*sql.DB{"postgres": db}}
			for _, statement := range []string{
				"CREATE TABLE " + q + ".items(id integer)",
				"CREATE TABLE " + q + ".other_items(id integer)",
				"CREATE VIEW " + q + ".item_view AS SELECT * FROM " + q + ".items",
				"CREATE SEQUENCE " + q + ".ids START 3 INCREMENT 2",
				"CREATE FUNCTION " + q + ".twice(integer) RETURNS integer LANGUAGE SQL AS $$SELECT $1*2$$",
				"CREATE FUNCTION " + q + ".twice(text) RETURNS text LANGUAGE SQL AS $$SELECT $1||$1$$",
				"CREATE TYPE " + q + ".state AS ENUM ('ready','done')",
				"CREATE FUNCTION " + q + ".typed_arg(" + q + ".state) RETURNS " + q + ".state LANGUAGE SQL AS $$SELECT $1$$",
				"CREATE FUNCTION " + q + ".touch_row() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NEW; END$$",
				"CREATE TRIGGER row_changed BEFORE INSERT ON " + q + ".items FOR EACH ROW EXECUTE PROCEDURE " + q + ".touch_row()",
				"CREATE TRIGGER row_changed BEFORE INSERT ON " + q + ".other_items FOR EACH ROW EXECUTE PROCEDURE " + q + ".touch_row()",
			} {
				pgTestExec(t, ctx, db, statement)
			}
			counts := map[string]int{"TABLE": 2, "VIEW": 1, "SEQUENCE": 1, "FUNCTION": 4, "TRIGGER": 2}
			version, err := postgresVersion(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			if version >= 110000 {
				pgTestExec(t, ctx, db, "CREATE PROCEDURE "+q+".demo_proc(IN n integer) LANGUAGE plpgsql AS $$BEGIN PERFORM n; END$$")
				counts["PROCEDURE"] = 1
			}
			objects := catalogCall(t, m, "postgres", schema, "objects", nil).([]record)
			checkObjectCounts(t, objects, counts)
			for _, object := range objects {
				kind := valueString(object["object_type"])
				if kind == "TABLE" || kind == "VIEW" {
					continue // Strict table export deliberately rejects tables with triggers.
				}
				p := map[string]any{"table": object["name"], "object_type": kind}
				if kind == "FUNCTION" || kind == "PROCEDURE" {
					p["signature"] = object["signature"]
				}
				if kind == "TRIGGER" {
					p["relation_name"] = object["parent_name"]
				}
				source := catalogCall(t, m, "postgres", schema, "object-source", p).(record)
				if !strings.Contains(valueString(source["source"]), kind) {
					t.Fatalf("missing %s source: %#v", kind, source)
				}
			}
			for kind, expected := range counts {
				filtered := catalogCall(t, m, "postgres", schema, "objects", map[string]any{"object_types": kind}).([]record)
				if len(filtered) != expected {
					t.Fatalf("%s group: got %d want %d", kind, len(filtered), expected)
				}
			}
			if _, err := m.Handle(ctx, "/api/schema/object-source", map[string]any{"connection_id": "fixture", "database": "postgres", "schema": schema, "table": "twice", "object_type": "FUNCTION"}); err == nil {
				t.Fatal("an overloaded name returned an arbitrary routine")
			}
			if _, err := m.Handle(ctx, "/api/schema/object-source", map[string]any{"connection_id": "fixture", "database": "postgres", "schema": schema, "table": "row_changed", "object_type": "TRIGGER"}); err == nil {
				t.Fatal("an ambiguous trigger name returned an arbitrary table's trigger")
			}
			search := catalogCall(t, m, "postgres", "", "search-object-sources", map[string]any{"query": "SELECT", "schemas": []string{schema}}).(ObjectSourceSearchResult)
			if len(search.Matches) != 3 || search.Truncated || len(search.Warnings) != 0 {
				t.Fatalf("overloaded function definition search: %#v", search)
			}
			for _, match := range search.Matches {
				if match.Schema != schema || match.ObjectType != "FUNCTION" || match.Signature == "" {
					t.Fatalf("search lost routine identity: %#v", match)
				}
			}
		})
	}
}
