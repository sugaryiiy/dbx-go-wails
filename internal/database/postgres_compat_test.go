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

// Both fixtures must be disposable databases. This test creates only a unique
// schema and exercises the same routes used by the sidebar and query editor.
func TestPostgresMetadataCompatibility(t *testing.T) {
	for _, fixture := range []struct{ name, env string }{{"legacy", "DBX_TEST_POSTGRES_LEGACY_DSN"}, {"modern", "DBX_TEST_POSTGRES_DSN"}} {
		t.Run(fixture.name, func(t *testing.T) {
			dsn := os.Getenv(fixture.env)
			if dsn == "" {
				t.Skip("set " + fixture.env + " to a disposable PostgreSQL database")
			}
			db, err := sql.Open("pgx", dsn)
			if err != nil {
				t.Fatal(err)
			}
			m := NewManager()
			defer m.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			schema := fmt.Sprintf("dbx_compat_%d", time.Now().UnixNano())
			m.connections["fixture"] = &connection{config: Config{ID: "fixture", Type: "postgres", Database: "postgres", DefaultSchema: schema}, pools: map[string]*sql.DB{"postgres": db}}
			pgTestExec(t, ctx, db, "CREATE SCHEMA "+pgIdent(schema))
			defer func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if _, err := db.ExecContext(cleanup, "DROP SCHEMA "+pgIdent(schema)+" CASCADE"); err != nil {
					t.Error(err)
				}
			}()
			tableName := "Mixed Table_%"
			table := pgQualified(schema, tableName)
			parent := pgQualified(schema, "parent")
			pgTestExec(t, ctx, db, "CREATE TABLE "+parent+"(a integer,b integer,PRIMARY KEY(b,a))")
			pgTestExec(t, ctx, db, "CREATE TABLE "+table+`(id serial PRIMARY KEY,a integer,b integer,"Display Name" varchar(12) DEFAULT 'initial',amount numeric(10,2) CHECK(amount>0),CONSTRAINT composite_fk FOREIGN KEY(b,a) REFERENCES `+parent+`(b,a) ON DELETE CASCADE,UNIQUE(b,a))`)
			pgTestExec(t, ctx, db, "CREATE INDEX expression_idx ON "+table+`(lower("Display Name"),amount DESC); COMMENT ON TABLE `+table+` IS 'table comment'; COMMENT ON COLUMN `+table+`."Display Name" IS 'column comment'`)
			pgTestExec(t, ctx, db, "CREATE VIEW "+pgQualified(schema, "sample_view")+" AS SELECT id FROM "+table)
			pgTestExec(t, ctx, db, "CREATE FUNCTION "+pgQualified(schema, "twice")+"(integer) RETURNS integer LANGUAGE SQL AS $$ SELECT $1*2 $$")
			call := func(route string, extra map[string]any) any {
				t.Helper()
				p := map[string]any{"connection_id": "fixture", "database": "postgres", "schema": schema, "table": tableName}
				for key, value := range extra {
					p[key] = value
				}
				result, err := m.Handle(ctx, "/api/schema/"+route, p)
				if err != nil {
					t.Fatalf("%s: %v", route, err)
				}
				return result
			}
			cols := call("columns", nil).([]record)
			if len(cols) != 5 || !valueBool(cols[0]["is_primary_key"]) || valueBool(cols[1]["is_primary_key"]) || cols[3]["data_type"] != "character varying(12)" || cols[3]["comment"] != "column comment" {
				t.Fatalf("column metadata: %#v", cols)
			}
			idx := call("indexes", nil).([]record)
			if len(idx) != 3 {
				t.Fatalf("index metadata: %#v", idx)
			}
			for _, index := range idx {
				if index["name"] == "expression_idx" && (!reflect.DeepEqual(index["key_is_expression"], []bool{true, false}) || index["columns"].([]string)[1] != "amount") {
					t.Fatalf("expression index metadata: %#v", index)
				}
			}
			fk := call("foreign-keys", nil).([]record)
			if len(fk) != 2 || fk[0]["column"] != "b" || fk[0]["ref_column"] != "b" || fk[1]["column"] != "a" || fk[0]["on_delete"] != "CASCADE" {
				t.Fatalf("foreign key order: %#v", fk)
			}
			constraints := call("constraints", nil).([]record)
			if len(constraints) != 4 {
				t.Fatalf("constraints: %#v", constraints)
			}
			for _, constraint := range constraints {
				if constraint["name"] == "composite_fk" && (!reflect.DeepEqual(constraint["columns"], []string{"b", "a"}) || !reflect.DeepEqual(constraint["ref_columns"], []string{"b", "a"})) {
					t.Fatalf("constraint key order: %#v", constraint)
				}
			}
			if call("table-comment", nil) != "table comment" {
				t.Fatal("table comment missing")
			}
			for _, route := range []string{"all-columns", "reference-keys", "reference-key-columns", "triggers", "functions", "sequences", "table-owner", "table-partition-status", "table-partitioning", "partitions", "event-triggers", "invalid-indexes"} {
				call(route, nil)
			}
			completion := func(p map[string]any) record {
				t.Helper()
				return call("completion-assistant", p).(record)
			}
			for _, global := range []bool{false, true} {
				result := completion(map[string]any{"object_kinds": []string{"table", "view"}, "mask": "Mixed Table_%", "global_search": global})
				items := result["candidates"].([]record)
				if len(items) != 1 || items[0]["name"] != tableName || items[0]["schema"] != schema || items[0]["comment"] != "table comment" {
					t.Fatalf("cold table completion (global=%t): %#v", global, result)
				}
			}
			result := completion(map[string]any{"object_kinds": []string{"column"}, "parent_name": tableName, "mask": "Display"})
			items := result["candidates"].([]record)
			if len(items) != 1 || items[0]["name"] != "Display Name" || items[0]["parent_name"] != tableName || items[0]["data_type"] != "character varying(12)" {
				t.Fatalf("column completion: %#v", result)
			}
			result = completion(map[string]any{"object_kinds": []string{"table", "view"}, "max_results": 1})
			if !valueBool(result["incomplete"]) || len(result["candidates"].([]record)) != 1 {
				t.Fatalf("completion truncation: %#v", result)
			}
			for _, kinds := range [][]string{{"routine"}, {"sequence"}, {"schema"}, {"database"}, {"table", "column", "function"}} {
				completion(map[string]any{"object_kinds": kinds, "parent_name": tableName})
			}
			result = completion(map[string]any{"mask": "' OR 1=1 --"})
			if len(result["candidates"].([]record)) != 0 {
				t.Fatal("completion mask was interpreted as SQL")
			}
			result = completion(map[string]any{"mask": "Table_%", "match_mode": "contains", "case_sensitive": true})
			if len(result["candidates"].([]record)) != 1 {
				t.Fatal("literal substring completion failed")
			}
			ddl := call("ddl", nil).(string)
			if !strings.Contains(ddl, `CREATE TABLE `+table) || !strings.Contains(ddl, "CREATE SEQUENCE") || !strings.Contains(ddl, "column comment") {
				t.Fatalf("incomplete DDL: %s", ddl)
			}
			pgTestExec(t, ctx, db, "DROP VIEW "+pgQualified(schema, "sample_view")+"; DROP TABLE "+table)
			pgTestExec(t, ctx, db, ddl)
			if after := call("columns", nil).([]record); !reflect.DeepEqual(cols, after) {
				t.Fatalf("DDL round trip changed columns: before=%#v after=%#v", cols, after)
			}
			pgTestExec(t, ctx, db, "INSERT INTO "+parent+" VALUES(1,2); INSERT INTO "+table+"(a,b) VALUES(1,2)")
			if _, err := db.ExecContext(ctx, "INSERT INTO "+table+"(a,b) VALUES(9,9)"); err == nil {
				t.Fatal("DDL round trip lost foreign key enforcement")
			}
		})
	}
}
