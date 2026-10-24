package server

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGridKeylessSQLiteSaveAndCompensatingSQL(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	config := map[string]any{"id": "keyless", "db_type": "sqlite", "host": filepath.Join(t.TempDir(), "keyless.sqlite"), "database": "main"}
	value(t, call(s, "POST", "/api/connection/connect", map[string]any{"config": config}, cookie, ""))
	query := func(sql string) map[string]any {
		t.Helper()
		return value(t, call(s, "POST", "/api/query/execute", map[string]any{"connectionId": "keyless", "database": "main", "sql": sql}, cookie, "")).(map[string]any)
	}
	query("CREATE TABLE people(id INTEGER,code TEXT NOT NULL,note TEXT,n NUMERIC,raw BLOB)")
	query("INSERT INTO people VALUES(1,'old','orig',42,X'00ff'),(2,'two',NULL,5,NULL)")
	opts := gridFixture()
	opts.Table.PrimaryKeys = nil
	payload := jsonMap(t, opts)
	payload["dirtyRows"] = []any{[]any{0, []any{[]any{1, "changed';--"}, []any{2, nil}}}}
	payload["deletedRows"] = []int{1}
	prepared := value(t, call(s, "POST", "/api/query/prepare-data-grid-save", map[string]any{"options": payload}, cookie, "")).(map[string]any)
	if prepared["validationError"] != nil {
		t.Fatal(prepared)
	}
	guards, ok := prepared["keylessGuards"].([]any)
	if !ok || len(guards) != 2 {
		t.Fatalf("expected a guard for each original row: %#v", prepared)
	}
	statements := prepared["statements"].([]any)
	if len(statements) != 2 {
		t.Fatal(prepared)
	}
	for i, raw := range guards {
		guard := raw.(map[string]any)
		if guard["maxMatchedRows"] != float64(1) || guard["message"] == "" {
			t.Fatalf("invalid guard: %#v", guard)
		}
		_, predicate, found := strings.Cut(statements[i].(string), " WHERE ")
		if !found || !strings.HasSuffix(guard["sql"].(string), " WHERE ("+strings.TrimSuffix(predicate, ";")+")") {
			t.Fatalf("guard must check the same predicate as the mutation: %#v", prepared)
		}
		rows := query(guard["sql"].(string))["rows"].([]any)
		if rows[0].([]any)[0] != float64(1) {
			t.Fatalf("guard must identify one stored row: %#v", rows)
		}
	}
	if len(statements) != 2 || !strings.Contains(statements[0].(string), `"code" = 'old'`) || !strings.Contains(statements[1].(string), `"note" IS NULL`) {
		t.Fatalf("mutations must match original row values: %#v", statements)
	}
	value(t, call(s, "POST", "/api/query/execute-in-transaction", map[string]any{"connectionId": "keyless", "database": "main", "statements": statements}, cookie, ""))
	rows := query("SELECT id,code,note,n,raw FROM people ORDER BY id")["rows"].([]any)
	if len(rows) != 1 || rows[0].([]any)[1] != "changed';--" || rows[0].([]any)[2] != nil {
		t.Fatalf("keyless save: %#v", rows)
	}
	rollback := prepared["rollbackStatements"].([]any)
	if len(rollback) != 2 {
		t.Fatalf("missing compensating statements: %#v", prepared)
	}
	value(t, call(s, "POST", "/api/query/execute-in-transaction", map[string]any{"connectionId": "keyless", "database": "main", "statements": rollback}, cookie, ""))
	rows = query("SELECT id,code,note,n,raw FROM people ORDER BY id")["rows"].([]any)
	if len(rows) != 2 || rows[0].([]any)[1] != "old" || rows[0].([]any)[2] != "orig" || rows[0].([]any)[4] != "0x00ff" || rows[1].([]any)[2] != nil {
		t.Fatalf("keyless rollback: %#v", rows)
	}
}

func TestGridKeylessGuardCountsDuplicatesOutsideLoadedPage(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	config := map[string]any{"id": "duplicates", "db_type": "sqlite", "host": filepath.Join(t.TempDir(), "duplicates.sqlite"), "database": "main"}
	value(t, call(s, "POST", "/api/connection/connect", map[string]any{"config": config}, cookie, ""))
	query := func(sql string) map[string]any {
		t.Helper()
		return value(t, call(s, "POST", "/api/query/execute", map[string]any{"connectionId": "duplicates", "database": "main", "sql": sql}, cookie, "")).(map[string]any)
	}
	query("CREATE TABLE stats(area TEXT,amount NUMERIC)")
	query("INSERT INTO stats VALUES('steam',NULL),('steam',NULL)")
	opts := gridOptions{Kind: "sqlite", Table: gridTable{Name: "stats", Columns: []gridColumn{{Name: "area", Type: "TEXT"}, {Name: "amount", Type: "NUMERIC", Nullable: true}}}, Columns: []string{"area", "amount"}, Rows: [][]any{{"steam", nil}}, DeletedRows: []int{0}}
	prepared := value(t, call(s, "POST", "/api/query/prepare-data-grid-save", map[string]any{"options": jsonMap(t, opts)}, cookie, "")).(map[string]any)
	if prepared["validationError"] != nil {
		t.Fatal(prepared)
	}
	guards, ok := prepared["keylessGuards"].([]any)
	if !ok || len(guards) != 1 {
		t.Fatalf("expected server guard despite a single loaded row: %#v", prepared)
	}
	guard := guards[0].(map[string]any)
	count := query(guard["sql"].(string))["rows"].([]any)[0].([]any)[0]
	if count != float64(2) || guard["maxMatchedRows"] != float64(1) {
		t.Fatalf("guard must reveal duplicates absent from the loaded page: count=%v guard=%#v", count, guard)
	}
}

func TestGridKeylessSourceMappingAndEmptyPredicate(t *testing.T) {
	name := "code"
	opts := gridFixture()
	opts.Table.PrimaryKeys = nil
	opts.Columns = []string{"display_code", "calculated"}
	opts.SourceColumns = &[]*string{&name, nil}
	opts.Rows = [][]any{{"old", float64(3)}}
	opts.DirtyRows = []gridDirtyRow{{Index: 0, Changes: []gridChange{{Index: 0, Value: "new"}}}}
	builder, err := newGridBuilder(opts)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := builder.prepare()
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Statements) != 1 || prepared.Statements[0] != `UPDATE "main"."people" SET "code" = 'new' WHERE "code" = 'old';` {
		t.Fatalf("must use mapped original values only: %#v", prepared)
	}
	copy, err := builder.copyUpdates()
	if err != nil || len(copy) != 1 || strings.Contains(copy[0], "calculated") || !strings.Contains(copy[0], `WHERE "code" = 'old'`) {
		t.Fatalf("keyless copy UPDATE: %#v %v", copy, err)
	}
	opts.SourceColumns = &[]*string{nil, nil}
	opts.DirtyRows = nil
	opts.DeletedRows = []int{0}
	builder, err = newGridBuilder(opts)
	if err == nil {
		_, err = builder.prepare()
	}
	if err == nil {
		t.Fatal("accepted keyless DELETE with no mapped column predicate")
	}
}

func TestGridKeylessMySQLMatchesOriginalValuesExactly(t *testing.T) {
	for _, kind := range []string{"mysql", "mariadb"} {
		t.Run(kind, func(t *testing.T) {
			opts := gridFixture()
			opts.Kind = kind
			opts.Table.PrimaryKeys = nil
			opts.Rows[0][2] = nil
			opts.DirtyRows = []gridDirtyRow{{Index: 0, Changes: []gridChange{{Index: 1, Value: "new"}}}}
			builder, err := newGridBuilder(opts)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := builder.prepare()
			if err != nil {
				t.Fatal(err)
			}
			statement := prepared.Statements[0]
			for _, part := range []string{"`id` = 1", "BINARY `code` = CONVERT(X'6f6c64' USING utf8mb4)", "`note` IS NULL", "`n` = 42", "`raw` = X'00ff'"} {
				if !strings.Contains(statement, part) {
					t.Fatalf("missing original-value predicate %q: %s", part, statement)
				}
			}
		})
	}
}

func TestGridKeylessPostgresJSONPredicates(t *testing.T) {
	for _, typ := range []string{"json", "jsonb"} {
		t.Run(typ, func(t *testing.T) {
			opts := gridOptions{Kind: "postgres", Table: gridTable{Name: "events", Columns: []gridColumn{{Name: "payload", Type: typ}, {Name: "note", Type: "TEXT", Nullable: true}}}, Columns: []string{"payload", "note"}, Rows: [][]any{{`{"a":1}`, nil}}, DirtyRows: []gridDirtyRow{{Index: 0, Changes: []gridChange{{Index: 1, Value: "new"}}}}}
			builder, err := newGridBuilder(opts)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := builder.prepare()
			if err != nil {
				t.Fatal(err)
			}
			predicate := `"payload" = E'{"a":1}'::jsonb`
			if typ == "json" {
				predicate = `"payload"::text = E'{"a":1}'::text`
			}
			if !strings.Contains(prepared.Statements[0], predicate) || !strings.Contains(prepared.Statements[0], `"note" IS NULL`) {
				t.Fatalf("invalid PostgreSQL JSON row predicate: %#v", prepared)
			}
		})
	}
}

func TestGridKeylessPostgresDuplicateRowsTargetSingleRow(t *testing.T) {
	// A report table without a primary key whose stored rows can be exact
	// duplicates of each other. PostgreSQL can still address one physical
	// row through ctid, so the save must not be refused: the UPDATE and the
	// DELETE each target a single matching tuple.
	row := []any{float64(31), "501202124", nil, float64(280), "0.9750"}
	opts := gridOptions{
		Kind: "postgres",
		Table: gridTable{
			Schema: "public",
			Name:   "rpt_dg_rate_pack",
			Columns: []gridColumn{
				{Name: "org_id", Type: "bigint"},
				{Name: "prod_code", Type: "character varying"},
				{Name: "xilie", Type: "character varying", Nullable: true},
				{Name: "specs", Type: "integer"},
				{Name: "rate", Type: "numeric"},
			},
		},
		Columns:     []string{"org_id", "prod_code", "xilie", "specs", "rate"},
		Rows:        [][]any{row, row},
		DirtyRows:   []gridDirtyRow{{Index: 0, Changes: []gridChange{{Index: 4, Value: "0.9800"}}}},
		DeletedRows: []int{1},
	}
	builder, err := newGridBuilder(opts)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := builder.prepare()
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.KeylessGuards) != 0 {
		t.Fatalf("ctid-targeted mutations need no count guard: %#v", prepared.KeylessGuards)
	}
	if len(prepared.Statements) != 2 {
		t.Fatalf("expected update + delete: %#v", prepared.Statements)
	}
	for _, statement := range prepared.Statements {
		if !strings.Contains(statement, `WHERE ctid IN (SELECT ctid FROM "public"."rpt_dg_rate_pack" WHERE (`) || !strings.Contains(statement, "LIMIT 1);") {
			t.Fatalf("mutation must target exactly one physical row: %s", statement)
		}
		if !strings.Contains(statement, `"prod_code" = E'501202124'`) || !strings.Contains(statement, `"xilie" IS NULL`) {
			t.Fatalf("mutation must keep the all-values predicate inside the locator subquery: %s", statement)
		}
	}
	for i, limit := range prepared.MaxAffectedRows {
		if limit == nil || *limit != 1 {
			t.Fatalf("statement %d must stay capped at one affected row: %#v", i, prepared.MaxAffectedRows)
		}
	}
	// The compensating UPDATE for the edited row is wrapped the same way;
	// the compensating statement for the deleted row is a plain re-INSERT.
	var sawWrappedUndo, sawInsertUndo bool
	for _, rollback := range prepared.Rollback {
		if strings.HasPrefix(rollback, "UPDATE") && strings.Contains(rollback, "ctid IN (SELECT ctid") {
			sawWrappedUndo = true
		}
		if strings.HasPrefix(rollback, "INSERT INTO") {
			sawInsertUndo = true
		}
	}
	if !sawWrappedUndo || !sawInsertUndo {
		t.Fatalf("rollback must mirror the single-row targeting: %#v", prepared.Rollback)
	}
}

func TestGridKeylessPostgresIncompleteRowKeepsGuard(t *testing.T) {
	// When the result does not carry every table column, two stored rows
	// that look identical on screen can still differ in the unloaded
	// columns; picking one physically would be a guess, so the count guard
	// (and its refusal) stays.
	opts := gridOptions{
		Kind: "postgres",
		Table: gridTable{
			Name: "events",
			Columns: []gridColumn{
				{Name: "payload", Type: "jsonb"},
				{Name: "note", Type: "text", Nullable: true},
				{Name: "internal_flag", Type: "boolean"},
			},
		},
		Columns:   []string{"payload", "note"},
		Rows:      [][]any{{`{"a":1}`, nil}},
		DirtyRows: []gridDirtyRow{{Index: 0, Changes: []gridChange{{Index: 1, Value: "new"}}}},
	}
	builder, err := newGridBuilder(opts)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := builder.prepare()
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.KeylessGuards) != 1 {
		t.Fatalf("incomplete keyless rows must keep the count guard: %#v", prepared)
	}
	if strings.Contains(prepared.Statements[0], "ctid") {
		t.Fatalf("incomplete keyless rows must not be locator-targeted: %s", prepared.Statements[0])
	}
}

func TestGridKeylessMySQLJSONEditCanBeCompensated(t *testing.T) {
	opts := gridOptions{Kind: "mysql", Table: gridTable{Name: "events", Columns: []gridColumn{{Name: "payload", Type: "JSON"}}}, Columns: []string{"payload"}, Rows: [][]any{{`{"a": 1}`}}, DirtyRows: []gridDirtyRow{{Index: 0, Changes: []gridChange{{Index: 0, Value: `{"a":2}`}}}}}
	builder, err := newGridBuilder(opts)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := builder.prepare()
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range append(prepared.Statements, prepared.Rollback...) {
		if !strings.Contains(statement, "JSON_EXTRACT(`payload`, '$') = JSON_EXTRACT(CONVERT(X'") || strings.Contains(statement, "BINARY `payload`") {
			t.Fatalf("JSON comparison must not depend on server whitespace formatting: %s", statement)
		}
	}
	if len(prepared.Rollback) != 1 || !strings.Contains(prepared.Rollback[0], "X'7b2261223a327d'") {
		t.Fatalf("rollback must match the newly assigned JSON: %#v", prepared)
	}
}

func TestGridKeylessMySQLBitValueUsesBinaryLiteral(t *testing.T) {
	opts := gridOptions{Kind: "mysql", Table: gridTable{Name: "flags", Columns: []gridColumn{{Name: "flag", Type: "bit(1)"}, {Name: "note", Type: "TEXT"}}}, Columns: []string{"flag", "note"}, Rows: [][]any{{"0x01", "old"}}, DirtyRows: []gridDirtyRow{{Index: 0, Changes: []gridChange{{Index: 1, Value: "new"}}}}}
	builder, err := newGridBuilder(opts)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := builder.prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prepared.Statements[0], "`flag` = X'01'") || !strings.Contains(prepared.KeylessGuards[0].SQL, "`flag` = X'01'") {
		t.Fatalf("MySQL BIT results use the same hex encoding as binary columns: %#v", prepared)
	}
}
