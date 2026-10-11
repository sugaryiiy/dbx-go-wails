package server

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sugaryiiy/dbx-go-wails/internal/database"
)

func sqliteToolsFixture(t *testing.T) (*Server, database.Config, *http.Cookie) {
	t.Helper()
	s := testServer(t, t.TempDir())
	cfg := database.Config{ID: "tools", Type: "sqlite", Name: "Tools", Host: filepath.Join(t.TempDir(), "tools.db")}
	if err := s.database.Connect(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	return s, cfg, loginTest(t, s)
}

func sqliteEditColumns(t *testing.T, s *Server, cfg database.Config, table string) []map[string]any {
	t.Helper()
	metadata, err := s.compareMetadata(context.Background(), cfg.ID, "main", "main", table, "columns")
	if err != nil {
		t.Fatal(err)
	}
	columns := []map[string]any{}
	for _, c := range compareList(metadata) {
		columns = append(columns, map[string]any{"name": c["name"], "dataType": c["data_type"], "isNullable": c["is_nullable"], "isPrimaryKey": c["is_primary_key"], "defaultValue": c["column_default"], "extra": c["extra"], "original": c})
	}
	return columns
}

func TestSQLiteStructureRebuildPreservesStoredBehavior(t *testing.T) {
	s, cfg, cookie := sqliteToolsFixture(t)
	ctx := context.Background()
	for _, sql := range []string{
		`CREATE TABLE audit (item INTEGER, label TEXT)`,
		`CREATE TABLE item (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL CHECK(length(name)>0), value INTEGER NOT NULL DEFAULT 1, computed INTEGER GENERATED ALWAYS AS(value*2) STORED, UNIQUE(name), CHECK(value>0)) STRICT`,
		`CREATE INDEX ix_expr ON item(lower(name)) WHERE value>0`,
		`CREATE TRIGGER item_audit AFTER UPDATE ON item BEGIN INSERT INTO audit VALUES(new.id,new.name); END`,
		`CREATE TABLE child (item_id INTEGER REFERENCES item(id))`,
		`INSERT INTO item(id,name,value) VALUES(1,'first',3),(100,'removed',1)`,
		`DELETE FROM item WHERE id=100`,
		`INSERT INTO child VALUES(1)`,
	} {
		if _, err := s.database.ExecuteSQL(ctx, cfg.ID, "main", sql, 100); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	columns := sqliteEditColumns(t, s, cfg, "item")
	columns = append(columns, map[string]any{"name": "tag", "dataType": "TEXT", "isNullable": false, "defaultValue": "'migrated'"})
	request := map[string]any{"connectionId": cfg.ID, "database": "main", "options": map[string]any{"databaseType": "sqlite", "schema": "main", "tableName": "item", "columns": columns}}
	preview := value(t, call(s, "POST", "/api/query/preview-sqlite-table-structure-change", request, cookie, "")).(map[string]any)
	request["schemaRevision"] = preview["schemaRevision"]
	value(t, call(s, "POST", "/api/query/apply-sqlite-table-structure-change", request, cookie, ""))
	result, err := s.database.ExecuteSQL(ctx, cfg.ID, "main", `SELECT id,name,value,computed,tag FROM item`, 100)
	if err != nil || len(result.Rows) != 1 || result.Rows[0][4] != "migrated" || result.Rows[0][3] != int64(6) {
		t.Fatal(result, err)
	}
	for _, sql := range []string{`UPDATE item SET value=4,name='changed' WHERE id=1`, `INSERT INTO item(name) VALUES('second')`} {
		if _, err := s.database.ExecuteSQL(ctx, cfg.ID, "main", sql, 100); err != nil {
			t.Fatal(err)
		}
	}
	result, err = s.database.ExecuteSQL(ctx, cfg.ID, "main", `SELECT id,computed FROM item WHERE name='second'`, 100)
	if err != nil || len(result.Rows) != 1 || result.Rows[0][0] != int64(101) {
		t.Fatal("AUTOINCREMENT counter was not preserved", result, err)
	}
	result, err = s.database.ExecuteSQL(ctx, cfg.ID, "main", `SELECT label FROM audit`, 100)
	if err != nil || len(result.Rows) != 1 || result.Rows[0][0] != "changed" {
		t.Fatal("Trigger was not preserved", result, err)
	}
	result, err = s.database.ExecuteSQL(ctx, cfg.ID, "main", `SELECT sql FROM sqlite_schema WHERE name='ix_expr'`, 100)
	if err != nil || len(result.Rows) != 1 || !strings.Contains(result.Rows[0][0].(string), "lower(name)") || !strings.Contains(result.Rows[0][0].(string), "WHERE value>0") {
		t.Fatal("Expression index was not preserved", result, err)
	}
	for _, sql := range []string{`INSERT INTO item(name) VALUES('changed')`, `UPDATE item SET name='' WHERE id=1`, `UPDATE item SET value=0 WHERE id=1`} {
		if _, err := s.database.ExecuteSQL(ctx, cfg.ID, "main", sql, 100); err == nil {
			t.Fatal("Constraint disappeared:", sql)
		}
	}
}

func TestSQLiteStructureRejectsStalePreviewAndRollsBackBadCopy(t *testing.T) {
	s, cfg, cookie := sqliteToolsFixture(t)
	ctx := context.Background()
	for _, sql := range []string{`CREATE TABLE item(id INTEGER PRIMARY KEY, name TEXT)`, `INSERT INTO item VALUES(1,NULL)`} {
		if _, err := s.database.ExecuteSQL(ctx, cfg.ID, "main", sql, 100); err != nil {
			t.Fatal(err)
		}
	}
	columns := sqliteEditColumns(t, s, cfg, "item")
	columns[1]["isNullable"] = false
	request := map[string]any{"connectionId": cfg.ID, "database": "main", "options": map[string]any{"databaseType": "sqlite", "schema": "main", "tableName": "item", "columns": columns}}
	preview := value(t, call(s, "POST", "/api/query/preview-sqlite-table-structure-change", request, cookie, "")).(map[string]any)
	request["schemaRevision"] = preview["schemaRevision"]
	failed := call(s, "POST", "/api/query/apply-sqlite-table-structure-change", request, cookie, "")
	if failed.Code != 400 {
		t.Fatal("Invalid copy succeeded", failed.Code, failed.Body)
	}
	result, err := s.database.ExecuteSQL(ctx, cfg.ID, "main", `SELECT name FROM item`, 100)
	if err != nil || len(result.Rows) != 1 || result.Rows[0][0] != nil {
		t.Fatal("Failed rebuild did not roll back", result, err)
	}
	if _, err := s.database.ExecuteSQL(ctx, cfg.ID, "main", `CREATE INDEX ix_item ON item(name)`, 100); err != nil {
		t.Fatal(err)
	}
	stale := call(s, "POST", "/api/query/apply-sqlite-table-structure-change", request, cookie, "")
	if stale.Code != 400 || !strings.Contains(stale.Body.String(), "changed since preview") {
		t.Fatal("Stale preview was accepted", stale.Code, stale.Body)
	}
}

func TestSQLExportPreservesNullableMetadataBinaryAndDefaults(t *testing.T) {
	s, cfg, _ := sqliteToolsFixture(t)
	ctx := context.Background()
	if _, err := s.database.ExecuteSQL(ctx, cfg.ID, "main", `CREATE TABLE exported(id INTEGER, bytes BLOB, data JSON, computed INTEGER GENERATED ALWAYS AS(id*2))`, 100); err != nil {
		t.Fatal(err)
	}
	statements, err := advancedExportInserts(map[string]any{"databaseType": "sqlite", "qualifiedTableName": `"main"."exported"`, "columns": []string{"id", "bytes", "data", "computed"}, "columnTypes": []any{nil, "BLOB", "JSON", "INTEGER"}, "columnExtras": []any{nil, nil, nil, "GENERATED"}, "rows": [][]any{{1, "0x00ff", map[string]any{"quoted": "it's safe"}, 2}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range statements {
		if strings.Contains(sql, "computed") {
			t.Fatal("Export included generated column", sql)
		}
		if _, err := s.database.ExecuteSQL(ctx, cfg.ID, "main", sql, 100); err != nil {
			t.Fatal(sql, err)
		}
	}
	result, err := s.database.ExecuteSQL(ctx, cfg.ID, "main", `SELECT hex(bytes),computed FROM exported`, 100)
	if err != nil || len(result.Rows) != 1 || result.Rows[0][0] != "00FF" || result.Rows[0][1] != int64(2) {
		t.Fatal(result, err)
	}
	for _, name := range []string{"x JOIN y", "x/*comment*/", "x;DELETE FROM y", "x = y", "x('value')", "x..y", "\"unfinished"} {
		if _, err := advancedExportInserts(map[string]any{"databaseType": "sqlite", "qualifiedTableName": name}); err == nil {
			t.Fatal("Accepted SQL in qualifiedTableName", name)
		}
	}
}

func TestPostgresSQLExportEmptyArraysIdentityAndSpatialSRID(t *testing.T) {
	statements, err := advancedExportInserts(map[string]any{"databaseType": "postgres", "tableName": "sample", "schema": "dst", "columns": []string{"id", "values", "point"}, "columnTypes": []any{"integer", "integer[]", "geometry"}, "columnExtras": []any{"IDENTITY", nil, nil}, "spatialColumns": []map[string]any{{"column_index": 2, "srid": 4326}}, "spatialValues": [][]any{{nil, nil, 3857}}, "rows": [][]any{{1, []any{}, "0x0101000000000000000000F03F0000000000000040"}}})
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.Join(statements, "\n")
	if !strings.Contains(sql, "ARRAY[]::integer[]") || !strings.Contains(sql, "OVERRIDING SYSTEM VALUE") || !strings.Contains(sql, "'hex'), 3857)") {
		t.Fatal(sql)
	}
}

func TestExplainPlanExecutesSQLitePlanWithoutRunningSourceMutation(t *testing.T) {
	s, cfg, cookie := sqliteToolsFixture(t)
	if _, err := s.database.ExecuteSQL(context.Background(), cfg.ID, "main", `CREATE TABLE item(id INTEGER PRIMARY KEY, name TEXT)`, 100); err != nil {
		t.Fatal(err)
	}
	plan := value(t, call(s, "POST", "/api/query/get-explain-info", map[string]any{"connectionId": cfg.ID, "database": "main", "sql": `SELECT name FROM item WHERE id=1`, "mode": "explain"}, cookie, ""))
	if !strings.Contains(plan.(string), "INTEGER PRIMARY KEY") {
		t.Fatal(plan)
	}
	for _, sql := range []string{`SELECT 1; DELETE FROM item`, `CREATE TABLE other(id INTEGER)`, `WITH x AS (DELETE FROM item RETURNING *) SELECT * FROM x`, `SELECT load_extension('unsafe')`} {
		built := advancedExplainSQL(map[string]any{"databaseType": "sqlite", "sql": sql})
		if boolValue(built, "ok") {
			t.Fatal("Unsafe EXPLAIN accepted", sql, built)
		}
	}
	if built := advancedExplainSQL(map[string]any{"databaseType": "postgres", "sql": `UPDATE item SET name='unsafe'`, "analyze": true}); boolValue(built, "ok") {
		t.Fatal("ANALYZE accepted a mutation", built)
	}
	if built := advancedExplainSQL(map[string]any{"databaseType": "mysql", "sql": `SELECT name FROM item`}); !strings.Contains(str(built, "sql"), "FORMAT=JSON") {
		t.Fatal(built)
	}
}
