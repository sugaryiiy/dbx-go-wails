package server

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGridKeylessSaveRollsBackConcurrentDuplicateAndEarlierStatements(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	config := map[string]any{"id": "atomic-grid", "db_type": "sqlite", "host": filepath.Join(t.TempDir(), "grid.sqlite"), "database": "main"}
	value(t, call(s, "POST", "/api/connection/connect", map[string]any{"config": config}, cookie, ""))
	query := func(sql string) map[string]any {
		t.Helper()
		return value(t, call(s, "POST", "/api/query/execute", map[string]any{"connectionId": "atomic-grid", "database": "main", "sql": sql}, cookie, "")).(map[string]any)
	}
	query("CREATE TABLE items(name TEXT, amount INTEGER)")
	query("CREATE TABLE audit(note TEXT)")
	query("INSERT INTO items VALUES('original',1)")
	opts := gridOptions{Kind: "sqlite", Table: gridTable{Name: "items", Columns: []gridColumn{{Name: "name", Type: "TEXT"}, {Name: "amount", Type: "INTEGER"}}}, Columns: []string{"name", "amount"}, Rows: [][]any{{"original", 1}}, DirtyRows: []gridDirtyRow{{Index: 0, Changes: []gridChange{{Index: 1, Value: 2}}}}}
	payload := jsonMap(t, opts)
	payload["dirtyRows"] = []any{[]any{0, []any{[]any{1, 2}}}}
	prepared := value(t, call(s, "POST", "/api/query/prepare-data-grid-save", map[string]any{"options": payload}, cookie, "")).(map[string]any)
	guards := prepared["keylessGuards"].([]any)
	count := query(guards[0].(map[string]any)["sql"].(string))["rows"].([]any)[0].([]any)[0]
	if count != float64(1) {
		t.Fatal(count)
	}
	// Another writer commits after the UI preflight has succeeded.
	query("INSERT INTO items VALUES('original',1)")
	statements := append([]any{"INSERT INTO audit VALUES('must roll back')"}, prepared["statements"].([]any)...)
	limits := append([]any{nil}, prepared["maxAffectedRowsPerStatement"].([]any)...)
	response := call(s, "POST", "/api/query/execute-in-transaction", map[string]any{"connectionId": "atomic-grid", "database": "main", "statements": statements, "maxAffectedRowsPerStatement": limits}, cookie, "")
	if response.Code != 400 || !strings.Contains(response.Body.String(), "affected 2 rows") {
		t.Fatalf("expected rejected duplicate: %d %s", response.Code, response.Body)
	}
	rows := query("SELECT amount FROM items")["rows"].([]any)
	for _, row := range rows {
		if row.([]any)[0] != float64(1) {
			t.Fatal("unsafe update was not rolled back", rows)
		}
	}
	if len(query("SELECT * FROM audit")["rows"].([]any)) != 0 {
		t.Fatal("earlier batch statement was not rolled back")
	}
}
