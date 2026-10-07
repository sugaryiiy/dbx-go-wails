package server

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func TestStatementAtUTF16Cursor(t *testing.T) {
	first := "SELECT '😀;中文'"
	sql := first + "; SELECT 2; -- trailing comment"
	result, _, err := buildSQL("/api/query/find-statement-at-cursor", map[string]any{"sql": sql, "cursorPos": len(utf16.Encode([]rune(first))) + 3})
	if err != nil || result != "SELECT 2" {
		t.Fatal(result, err)
	}
}
func TestTableSQLQuotesIdentifiersAndRejectsExtraStatements(t *testing.T) {
	options := map[string]any{"databaseType": "sqlite", "tableName": "a\"b", "limit": 20, "offset": 40}
	result, _, err := buildSQL("/api/query/build-table-select-sql", map[string]any{"options": options})
	if err != nil || result != `SELECT * FROM "a""b" LIMIT 20 OFFSET 40` {
		t.Fatal(result, err)
	}
	options["whereInput"] = "1=1; DROP TABLE users"
	if _, _, err := buildSQL("/api/query/build-table-select-sql", map[string]any{"options": options}); err == nil {
		t.Fatal("allowed multiple statements in a filter")
	}
}
func TestPaginationPreservesExistingLimitAndNeverWrapsWrites(t *testing.T) {
	result, _, err := buildSQL("/api/query/prepare-pagination-plan", map[string]any{"options": map[string]any{"sql": "SELECT * FROM items LIMIT 12;", "databaseType": "sqlite", "pagination": map[string]any{"limit": 5, "offset": 10}}})
	if err != nil {
		t.Fatal(err)
	}
	sql := result.(map[string]any)["sqlToExecute"].(string)
	if !strings.Contains(sql, "LIMIT 12\n)") || !strings.HasSuffix(sql, "LIMIT 5 OFFSET 10") {
		t.Fatal(sql)
	}
	write := "UPDATE items SET value=1 RETURNING *"
	result, _, err = buildSQL("/api/query/prepare-pagination-plan", map[string]any{"options": map[string]any{"sql": write}})
	if err != nil || result.(map[string]any)["sqlToExecute"] != write {
		t.Fatal(result, err)
	}
	if _, exists := result.(map[string]any)["countSql"]; exists {
		t.Fatal("would execute a write a second time for count")
	}
}
