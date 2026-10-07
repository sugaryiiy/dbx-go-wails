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

func TestClickHouseTableSQLAndPagination(t *testing.T) {
	options := map[string]any{"databaseType": "clickhouse", "database": "analytics", "includeDatabaseName": true, "tableName": "events", "columns": []any{"id", "a`b"}, "whereInput": "id > 10", "orderBy": "id DESC", "limit": 10, "offset": 20}
	result, handled, err := buildSQL("/api/query/build-table-select-sql", map[string]any{"options": options})
	want := "SELECT `id`, `a``b` FROM `analytics`.`events` WHERE id > 10 ORDER BY id DESC LIMIT 10 OFFSET 20"
	if err != nil || !handled || result != want {
		t.Fatalf("table select: %v %v", result, err)
	}
	count, _, err := buildSQL("/api/query/build-data-grid-count-sql", map[string]any{"options": options})
	if err != nil || count != "SELECT COUNT(*) FROM `analytics`.`events` WHERE id > 10" {
		t.Fatalf("count: %v %v", count, err)
	}
	result, _, err = buildSQL("/api/query/prepare-pagination-plan", map[string]any{"options": map[string]any{"databaseType": "clickhouse", "sql": "SELECT id FROM `analytics`.`events` LIMIT 100;", "pagination": map[string]any{"limit": 10, "offset": 20}}})
	if err != nil {
		t.Fatal(err)
	}
	sql := result.(map[string]any)["sqlToExecute"].(string)
	if !strings.Contains(sql, "LIMIT 100\n)") || !strings.HasSuffix(sql, "LIMIT 10 OFFSET 20") {
		t.Fatalf("pagination: %s", sql)
	}
	options["whereInput"] = "1 = 1; DROP TABLE `analytics`.`events`"
	if _, _, err := buildSQL("/api/query/build-table-select-sql", map[string]any{"options": options}); err == nil {
		t.Fatal("ClickHouse filter accepted a second statement")
	}
}

func TestClickHouseExplainSelect(t *testing.T) {
	for _, options := range []map[string]any{
		{"databaseType": "clickhouse", "sql": "SELECT count() FROM events"},
		{"databaseType": "clickhouse", "sql": "SELECT count() FROM events", "format": "text"},
		{"databaseType": "clickhouse", "sql": "SELECT count() FROM events", "format": "standard"},
	} {
		result, _, err := buildSQL("/api/query/build-explain-sql", map[string]any{"options": options})
		if err != nil || result.(map[string]any)["sql"] != "EXPLAIN SELECT count() FROM events" {
			t.Fatalf("explain: %v %v", result, err)
		}
	}
	for _, options := range []map[string]any{
		{"databaseType": "clickhouse", "sql": "SELECT 1", "analyze": true},
		{"databaseType": "clickhouse", "sql": "SELECT 1", "format": "json"},
		{"databaseType": "clickhouse", "sql": "DELETE FROM events"},
		{"databaseType": "clickhouse", "sql": "SELECT 1; DROP TABLE events"},
	} {
		result, _, err := buildSQL("/api/query/build-explain-sql", map[string]any{"options": options})
		if err != nil || result.(map[string]any)["ok"] != false {
			t.Fatalf("unsafe or unsupported explain accepted: %v %v", result, err)
		}
	}
}
