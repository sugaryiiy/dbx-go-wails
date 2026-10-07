package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func gridFixture() gridOptions {
	def := "'default'"
	return gridOptions{Kind: "sqlite", Table: gridTable{Name: "people", Schema: "main", PrimaryKeys: []string{"id"}, Columns: []gridColumn{
		{Name: "id", Type: "INTEGER", Primary: true}, {Name: "code", Type: "TEXT"}, {Name: "note", Type: "TEXT", Nullable: true, Default: &def}, {Name: "n", Type: "NUMERIC", Nullable: true}, {Name: "raw", Type: "BLOB", Nullable: true},
	}}, Columns: []string{"id", "code", "note", "n", "raw"}, Rows: [][]any{{float64(1), "old", "orig", float64(42), "0x00ff"}, {float64(2), "two", nil, float64(5), nil}}}
}
func jsonMap(t *testing.T, object any) map[string]any {
	t.Helper()
	data, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func TestGridSaveSQLiteCRUDAndCompensatingSQL(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	config := map[string]any{"id": "grid", "db_type": "sqlite", "host": filepath.Join(t.TempDir(), "grid.sqlite"), "database": "main"}
	if id := value(t, call(s, "POST", "/api/connection/connect", map[string]any{"config": config}, cookie, "")); id != "grid" {
		t.Fatalf("returned connection id %v", id)
	}
	query := func(sql string) map[string]any {
		t.Helper()
		return value(t, call(s, "POST", "/api/query/execute", map[string]any{"connectionId": "grid", "database": "main", "sql": sql}, cookie, "")).(map[string]any)
	}
	query("CREATE TABLE people(id INTEGER PRIMARY KEY,code TEXT UNIQUE NOT NULL,note TEXT DEFAULT 'default',n NUMERIC,raw BLOB)")
	query("INSERT INTO people VALUES(1,'old','orig',42,X'00ff'),(2,'two',NULL,5,NULL)")
	opts := gridFixture()
	payload := jsonMap(t, opts)
	payload["dirtyRows"] = []any{[]any{0, []any{[]any{0, 3}, []any{1, "quote'; DELETE FROM people; --\\中文"}, []any{2, nil}, []any{3, "9007199254740993"}, []any{4, "0x271700"}}}}
	payload["deletedRows"] = []int{1}
	payload["newRows"] = [][]any{{4, "new", nil, 1, nil}}
	prepared := value(t, call(s, "POST", "/api/query/prepare-data-grid-save", map[string]any{"options": payload}, cookie, "")).(map[string]any)
	if prepared["validationError"] != nil {
		t.Fatal(prepared)
	}
	if len(prepared["statements"].([]any)) != 3 {
		t.Fatal(prepared)
	}
	value(t, call(s, "POST", "/api/query/execute-in-transaction", map[string]any{"connectionId": "grid", "database": "main", "statements": prepared["statements"]}, cookie, ""))
	rows := query("SELECT id,code,note,n,raw FROM people ORDER BY id")["rows"].([]any)
	if len(rows) != 2 || rows[0].([]any)[0] != float64(3) || rows[0].([]any)[1] != "quote'; DELETE FROM people; --\\中文" || rows[0].([]any)[2] != nil || rows[0].([]any)[3] != "9007199254740993" || rows[0].([]any)[4] != "0x271700" || rows[1].([]any)[2] != "default" {
		t.Fatalf("save values: %#v", rows)
	}
	if len(prepared["rollbackStatements"].([]any)) != 3 {
		t.Fatal("missing compensating SQL", prepared)
	}
	value(t, call(s, "POST", "/api/query/execute-in-transaction", map[string]any{"connectionId": "grid", "database": "main", "statements": prepared["rollbackStatements"]}, cookie, ""))
	rows = query("SELECT id,code,note,n,raw FROM people ORDER BY id")["rows"].([]any)
	if len(rows) != 2 || rows[0].([]any)[0] != float64(1) || rows[0].([]any)[1] != "old" || rows[0].([]any)[2] != "orig" || rows[0].([]any)[4] != "0x00ff" || rows[1].([]any)[2] != nil {
		t.Fatalf("rollback values: %#v", rows)
	}
}
func TestGridSaveRejectsUnsafeTargets(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*gridOptions)
	}{
		{"no primary key", func(o *gridOptions) { o.Table.PrimaryKeys = nil }},
		{"missing primary key", func(o *gridOptions) { o.Table.PrimaryKeys = []string{"missing"} }},
		{"primary key absent from projection", func(o *gridOptions) {
			o.Columns = []string{"code"}
			o.Rows = [][]any{{"old"}}
			o.DirtyRows[0].Changes[0].Index = 0
		}},
		{"NULL primary key", func(o *gridOptions) { o.Rows[0][0] = nil }},
		{"NULL changed primary key", func(o *gridOptions) { o.DirtyRows[0].Changes = []gridChange{{Index: 0, Value: nil}} }},
		{"NULL not-null field", func(o *gridOptions) { o.DirtyRows[0].Changes = []gridChange{{Index: 1, Value: nil}} }},
		{"out of range row", func(o *gridOptions) { o.DirtyRows[0].Index = 999 }},
		{"out of range column", func(o *gridOptions) { o.DirtyRows[0].Changes[0].Index = 999 }},
		{"duplicate changes", func(o *gridOptions) {
			o.DirtyRows[0].Changes = append(o.DirtyRows[0].Changes, o.DirtyRows[0].Changes[0])
		}},
		{"unknown column", func(o *gridOptions) { o.Columns[1] = "not-a-column" }},
		{"computed column", func(o *gridOptions) { extra := "GENERATED"; o.Table.Columns[1].Extra = &extra }},
		{"missing metadata", func(o *gridOptions) { o.Table.Columns = nil }},
		{"unsafe float integer", func(o *gridOptions) {
			o.DirtyRows[0].Changes = []gridChange{{Index: 3, Value: float64(9007199254740992)}}
		}},
		{"unknown dialect", func(o *gridOptions) { o.Kind = "oracle" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := gridFixture()
			o.DirtyRows = []gridDirtyRow{{Index: 0, Changes: []gridChange{{Index: 1, Value: "changed"}}}}
			tc.mutate(&o)
			b, err := newGridBuilder(o)
			if err == nil {
				_, err = b.prepare()
			}
			if err == nil {
				t.Fatal("unsafe save accepted")
			}
		})
	}
}
func TestGridCompositeKeyAndSourceMapping(t *testing.T) {
	a, b := "tenant", "id"
	n := "name"
	opts := gridOptions{Kind: "postgres", Table: gridTable{Name: "a\"b", Schema: "public", PrimaryKeys: []string{"tenant", "id"}, Columns: []gridColumn{{Name: "tenant", Type: "TEXT"}, {Name: "id", Type: "INTEGER"}, {Name: "name", Type: "TEXT", Nullable: true}}}, Columns: []string{"display_tenant", "display_id", "display_name"}, SourceColumns: &[]*string{&a, &b, &n}, Rows: [][]any{{"acme", float64(4), "old"}}, DirtyRows: []gridDirtyRow{{Index: 0, Changes: []gridChange{{Index: 1, Value: float64(7)}, {Index: 2, Value: "a\\'b"}}}}}
	builder, err := newGridBuilder(opts)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := builder.prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prepared.Statements[0], `"public"."a""b"`) || !strings.Contains(prepared.Statements[0], `WHERE "tenant" = E'acme' AND "id" = 4`) || !strings.Contains(prepared.Rollback[0], `WHERE "tenant" = E'acme' AND "id" = 7`) {
		t.Fatal(prepared)
	}
	if !strings.Contains(prepared.Statements[0], `E'a\\''b'`) {
		t.Fatalf("postgres escape: %s", prepared.Statements[0])
	}
}
func TestGridMySQLStringsAreIndependentOfSQLMode(t *testing.T) {
	for _, kind := range []string{"mysql", "mariadb"} {
		t.Run(kind, func(t *testing.T) {
			o := gridFixture()
			o.Kind = kind
			o.Table.Schema = ""
			o.Table.Database = "db`name"
			o.IncludeDatabase = true
			o.DirtyRows = []gridDirtyRow{{Index: 0, Changes: []gridChange{{Index: 1, Value: "';\\ DROP TABLE people;--"}}}}
			b, err := newGridBuilder(o)
			if err != nil {
				t.Fatal(err)
			}
			p, err := b.prepare()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(p.Statements[0], "`db``name`.`people`") || !strings.Contains(p.Statements[0], "CONVERT(X'") || strings.Contains(p.Statements[0], "DROP TABLE") {
				t.Fatal(p)
			}
		})
	}
}
func TestGridGeneratedInsertAndMissingRollbackIdentity(t *testing.T) {
	o := gridFixture()
	o.NewRows = [][]any{{nil, "new", nil, nil, nil}}
	b, err := newGridBuilder(o)
	if err != nil {
		t.Fatal(err)
	}
	p, err := b.prepare()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Statements) != 1 || strings.Contains(p.Statements[0], `"id"`) || len(p.Rollback) != 0 {
		t.Fatal(p)
	}
	o.NewRows[0][0] = ""
	b, _ = newGridBuilder(o)
	p, err = b.prepare()
	if err != nil || len(p.Rollback) != 0 {
		t.Fatal("unknown generated id received compensating DELETE", p, err)
	}
}
func TestGridMalformedRequestAndValidationEnvelope(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	opts := jsonMap(t, gridFixture())
	opts["dirtyRows"] = []any{[]any{0, []any{[]any{1, "new"}}}}
	opts["tableMeta"].(map[string]any)["primaryKeys"] = []any{}
	result := value(t, call(s, "POST", "/api/query/prepare-data-grid-save", map[string]any{"options": opts}, cookie, "")).(map[string]any)
	if result["validationError"] == nil || len(result["statements"].([]any)) != 0 {
		t.Fatal(result)
	}
	opts["dirtyRows"] = []any{[]any{0, "invalid"}}
	response := call(s, "POST", "/api/query/prepare-data-grid-save", map[string]any{"options": opts}, cookie, "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid shape status %d", response.Code)
	}
}
func TestGridCopyInsertMergedAndExactNULL(t *testing.T) {
	o := gridFixture()
	b, err := newGridBuilder(o)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := b.copyInserts()
	if err != nil {
		t.Fatal(err)
	}
	sql := copy.(string)
	if strings.Count(sql, "INSERT INTO") != 1 || !strings.Contains(sql, "NULL") || !strings.Contains(sql, "), (") {
		t.Fatal(sql)
	}
	o.InsertMode = "row-by-row"
	b, _ = newGridBuilder(o)
	copy, err = b.copyInserts()
	if err != nil || strings.Count(copy.(string), "INSERT INTO") != 2 {
		t.Fatal(copy, err)
	}
}
