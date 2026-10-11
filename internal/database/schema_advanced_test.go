package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteConstraintMetadataIncludesChecksAndCompositeKeys(t *testing.T) {
	m := NewManager()
	defer m.Close()
	ctx := context.Background()
	cfg := Config{ID: "checks", Type: "sqlite", Host: filepath.Join(t.TempDir(), "schema.db")}
	if err := m.Connect(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`CREATE TABLE parent (a INTEGER,b INTEGER,PRIMARY KEY(a,b))`, `CREATE TABLE child (a INTEGER,b INTEGER,CONSTRAINT positive CHECK (a > 0 AND instr('x,y','x') = 1), UNIQUE(a,b),FOREIGN KEY(a,b) REFERENCES parent(a,b))`} {
		if _, err := m.ExecuteSQL(ctx, cfg.ID, "main", statement, 10); err != nil {
			t.Fatal(err)
		}
	}
	result, err := m.Handle(ctx, "/api/schema/constraints", map[string]any{"connectionId": cfg.ID, "database": "main", "schema": "main", "table": "child"})
	if err != nil {
		t.Fatal(err)
	}
	items := result.([]record)
	check, fk, unique := false, false, false
	for _, item := range items {
		switch item["constraint_type"] {
		case "CHECK":
			check = item["name"] == "positive" && strings.Contains(valueString(item["definition"]), "instr('x,y'")
		case "FOREIGN KEY":
			fk = len(item["columns"].([]string)) == 2 && len(item["ref_columns"].([]string)) == 2
		case "UNIQUE":
			unique = len(item["columns"].([]string)) == 2
		}
	}
	if !check || !fk || !unique {
		t.Fatal(items)
	}
}
func TestPostgresPartitionBoundPreservesSQLLiteralCommas(t *testing.T) {
	for _, p := range []struct {
		raw, kind string
		width     int
	}{{"FOR VALUES FROM ('a,b', (1 + 2)) TO ('z', MAXVALUE)", "range", 2}, {"FOR VALUES IN ('a,b', 'it''s', ROW(1,2))", "list", 3}, {"FOR VALUES WITH (modulus 8, remainder 3)", "hash", 0}, {"DEFAULT", "default", 0}} {
		bound := pgPartitionBound(p.raw)
		if bound == nil || bound["kind"] != p.kind {
			t.Fatal(bound)
		}
		if p.kind == "range" && len(bound["from"].([]string)) != p.width {
			t.Fatal(bound)
		}
		if p.kind == "list" && len(bound["values"].([]string)) != p.width {
			t.Fatal(bound)
		}
	}
}
func TestLivePostgresAdvancedMetadata(t *testing.T) {
	dsn := os.Getenv("DBX_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DBX_TEST_POSTGRES_DSN is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("Cannot open test PostgreSQL")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := fmt.Sprintf("dbx_go_advanced_%d", time.Now().UnixNano())
	q := pgIdent(schema)
	if _, err = db.ExecContext(ctx, "CREATE SCHEMA "+q); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(context.Background(), "DROP SCHEMA "+q+" CASCADE")
	statements := []string{"CREATE TABLE " + q + ".parent (id integer PRIMARY KEY)", "CREATE TABLE " + q + ".child (id integer NOT NULL, parent_id integer REFERENCES " + q + ".parent(id), CONSTRAINT positive CHECK(id>0),UNIQUE(id))", "CREATE SEQUENCE " + q + ".ids START WITH 3 INCREMENT BY 2", "CREATE FUNCTION " + q + ".twice(integer) RETURNS integer LANGUAGE SQL AS $$ SELECT $1*2 $$", "CREATE TABLE " + q + ".events (id integer) PARTITION BY RANGE(id)", "CREATE TABLE " + q + ".events_low PARTITION OF " + q + ".events FOR VALUES FROM (0) TO (100)"}
	for _, statement := range statements {
		if _, err = db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, part := range []string{"constraints", "functions", "sequences", "owners", "table-owner", "table-partition-status", "table-partitioning", "extensions", "available-extensions", "event-triggers", "invalid-indexes"} {
		table := "child"
		if strings.Contains(part, "partition") {
			table = "events"
		}
		result, err := postgresAdvanced(ctx, db, schema, table, part, map[string]any{})
		if err != nil {
			t.Fatalf("%s: %v", part, err)
		}
		switch part {
		case "constraints":
			if len(result.([]record)) != 3 {
				t.Fatal(result)
			}
		case "functions", "sequences":
			if len(result.([]record)) != 1 {
				t.Fatal(result)
			}
		case "table-partitioning":
			if len(result.(record)["partitions"].([]record)) != 1 {
				t.Fatal(result)
			}
		}
	}
}
