package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sugaryiiy/dbx-go-wails/internal/database"
)

func TestDataComparisonAppliesExactDifference(t *testing.T) {
	s := testServer(t, t.TempDir())
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "compare.db")
	cfg := database.Config{ID: "compare", Type: "sqlite", Host: dbPath, Name: "Compare"}
	if err := s.database.Connect(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"CREATE TABLE item (id INTEGER PRIMARY KEY, name TEXT, data JSON)", "INSERT INTO item VALUES (1, 'old', '{\"a\":1}'), (3, 'remove', NULL)"} {
		if _, err := s.database.ExecuteSQL(ctx, cfg.ID, "main", sql, 100); err != nil {
			t.Fatal(err)
		}
	}
	prepared, err := prepareDataCompare(map[string]any{"databaseType": "sqlite", "tableName": "item", "schema": "main", "columns": []string{"id", "name", "data"}, "keyColumns": []string{"id"}, "columnInfo": []map[string]any{{"name": "id", "data_type": "INTEGER"}, {"name": "name", "data_type": "TEXT"}, {"name": "data", "data_type": "JSON"}}, "sourceRows": [][]any{{float64(1), "new 'quote'", map[string]any{"a": float64(2)}}, {float64(2), "added", nil}}, "targetRows": [][]any{{float64(1), "old", map[string]any{"a": float64(1)}}, {float64(3), "remove", nil}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range compareStrings(prepared["syncStatements"]) {
		if _, err = s.database.ExecuteSQL(ctx, cfg.ID, "main", statement, 100); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	result, err := s.database.ExecuteSQL(ctx, cfg.ID, "main", "SELECT id,name,data FROM item ORDER BY id", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 2 || result.Rows[0][1] != "new 'quote'" || result.Rows[1][1] != "added" {
		t.Fatal(result.Rows)
	}
}
func TestDataComparisonRejectsAmbiguousKeys(t *testing.T) {
	for _, p := range []map[string]any{{"columns": []string{"id"}, "keyColumns": []string{"missing"}}, {"columns": []string{"id"}, "keyColumns": []string{"id"}, "sourceRows": [][]any{{1}, {1}}}} {
		if _, err := prepareDataCompare(p); err == nil {
			t.Fatal("Accepted ambiguous key")
		}
	}
}
func TestDataComparisonBinaryAndGeneratedColumns(t *testing.T) {
	plan, err := buildDataSyncPlan([]map[string]any{{"databaseType": "postgres", "tableName": "x", "columns": []string{"id", "bytes", "computed"}, "keyColumns": []string{"id"}, "columnInfo": []map[string]any{{"name": "bytes", "data_type": "bytea"}, {"name": "computed", "extra": "GENERATED ALWAYS"}}, "diff": map[string]any{"added": []map[string]any{{"values": map[string]any{"id": 1, "bytes": "0x00ff", "computed": "skip"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	sql := str(plan, "syncSql")
	if !strings.Contains(sql, "decode('00ff','hex')") || strings.Contains(sql, "computed") {
		t.Fatal(sql)
	}
}
