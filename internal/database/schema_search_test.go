package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

type objectSearchConnector struct {
	query func(context.Context, string, []driver.NamedValue) (driver.Rows, error)
	calls []string
	tx    []driver.TxOptions
}

func (c *objectSearchConnector) Connect(context.Context) (driver.Conn, error) {
	return &objectSearchConn{fixture: c}, nil
}

func (*objectSearchConnector) Driver() driver.Driver { return objectSearchDriver{} }

type objectSearchDriver struct{}

func (objectSearchDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use the object search connector")
}

type objectSearchConn struct{ fixture *objectSearchConnector }

func (*objectSearchConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement")
}
func (*objectSearchConn) Close() error { return nil }
func (*objectSearchConn) Begin() (driver.Tx, error) {
	return nil, errors.New("use BeginTx with explicit read-only options")
}
func (c *objectSearchConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	c.fixture.tx = append(c.fixture.tx, options)
	return objectSearchTx{}, nil
}
func (c *objectSearchConn) QueryContext(ctx context.Context, statement string, args []driver.NamedValue) (driver.Rows, error) {
	c.fixture.calls = append(c.fixture.calls, statement)
	return c.fixture.query(ctx, statement, args)
}
func (c *objectSearchConn) ExecContext(_ context.Context, statement string, _ []driver.NamedValue) (driver.Result, error) {
	if statement != "SET LOCAL search_path = pg_catalog" {
		return nil, fmt.Errorf("object search attempted to execute %s", statement)
	}
	return driver.RowsAffected(0), nil
}

type objectSearchTx struct{}

func (objectSearchTx) Commit() error   { return nil }
func (objectSearchTx) Rollback() error { return nil }

type objectSearchRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *objectSearchRows) Columns() []string { return r.columns }
func (*objectSearchRows) Close() error        { return nil }
func (r *objectSearchRows) Next(dest []driver.Value) error {
	if r.index == len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

func objectSearchManager(t *testing.T, kind, database string, connector *objectSearchConnector) *Manager {
	t.Helper()
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	m := NewManager()
	m.connections["search"] = &connection{
		config: Config{ID: "search", Type: kind, Database: database, ReadOnly: true},
		pools:  map[string]*sql.DB{database: db},
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestSearchObjectSourceOffsetsAndModes(t *testing.T) {
	source := "😀 SELECT\r\n  select.selectOne\nSELECT"
	object := record{"object_name": "report", "object_type": "PROCEDURE", "database": "app", "schema": "app"}
	options, err := parseObjectSourceSearch(map[string]any{"query": "select", "whole_word": true})
	if err != nil {
		t.Fatal(err)
	}
	matches := searchObjectSource(source, object, options, 100)
	positions := [][4]int{}
	for _, match := range matches {
		positions = append(positions, [4]int{match.Line, match.Column, match.Start, match.End})
	}
	if !reflect.DeepEqual(positions, [][4]int{{1, 4, 3, 9}, {2, 3, 2, 8}, {3, 1, 0, 6}}) {
		t.Fatalf("UTF-16 positions: %#v", matches)
	}
	if matches[0].LineText != "😀 SELECT" || matches[1].MatchText != "select" {
		t.Fatalf("original preview text: %#v", matches)
	}
	options, _ = parseObjectSourceSearch(map[string]any{"query": "select", "case_sensitive": true})
	if matches := searchObjectSource(source, object, options, 100); len(matches) != 2 {
		t.Fatalf("case sensitive matches: %#v", matches)
	}
	options, _ = parseObjectSourceSearch(map[string]any{"query": "select.select"})
	if matches := searchObjectSource(source, object, options, 100); len(matches) != 1 || matches[0].MatchText != "select.select" {
		t.Fatalf("literal regex punctuation: %#v", matches)
	}
	options, _ = parseObjectSourceSearch(map[string]any{"query": "SELECT\r?\n", "is_regex": true})
	if matches := searchObjectSource(source, object, options, 100); len(matches) != 1 || matches[0].MatchText != "SELECT\r\n" || matches[0].End != 9 {
		t.Fatalf("multiline regex: %#v", matches)
	}
	options, _ = parseObjectSourceSearch(map[string]any{"query": "\\b|SELECT", "is_regex": true})
	if matches := searchObjectSource(source, object, options, 100); len(matches) != 0 {
		t.Fatalf("zero-width matches should be omitted: %#v", matches)
	}
	options, _ = parseObjectSourceSearch(map[string]any{"query": "k"})
	if matches := searchObjectSource("K", object, options, 100); len(matches) != 1 || matches[0].MatchText != "K" || matches[0].End != 1 {
		t.Fatalf("case folding changed original positions: %#v", matches)
	}
}

func TestMySQLSearchObjectSourcesAcrossConnection(t *testing.T) {
	connector := &objectSearchConnector{}
	connector.query = func(_ context.Context, statement string, args []driver.NamedValue) (driver.Rows, error) {
		switch {
		case strings.Contains(statement, "information_schema.routines"):
			if len(args) != 0 || !strings.Contains(statement, "information_schema.events") || !strings.Contains(statement, "'mysql','performance_schema','sys'") {
				t.Fatalf("whole connection metadata query: %s, %#v", statement, args)
			}
			return &objectSearchRows{columns: []string{"object_name", "object_type", "database", "schema"}, values: [][]driver.Value{
				{"report", "PROCEDURE", "other", "other"},
				{"hidden", "FUNCTION", "other", "other"},
				{"tick`event", "EVENT", "app", "app"},
			}}, nil
		case statement == "SHOW CREATE PROCEDURE `other`.`report`":
			return &objectSearchRows{columns: []string{"Create Procedure"}, values: [][]driver.Value{{"\n CREATE PROCEDURE report()\nBEGIN\n  SELECT 'needle';\nEND\n "}}}, nil
		case statement == "SHOW CREATE FUNCTION `other`.`hidden`":
			return &objectSearchRows{columns: []string{"Create Function"}, values: [][]driver.Value{{nil}}}, nil
		case statement == "SHOW CREATE EVENT `app`.`tick``event`":
			return &objectSearchRows{columns: []string{"Create Event"}, values: [][]driver.Value{{"CREATE EVENT `tick``event` ON SCHEDULE EVERY 1 DAY DO SELECT 'NEEDLE'"}}}, nil
		default:
			t.Fatalf("unexpected search SQL: %s", statement)
			return nil, errors.New("unexpected query")
		}
	}
	m := objectSearchManager(t, "mysql", "app", connector)
	m.connections["search"].config.Raw = map[string]any{"visible_schemas": map[string]any{"app": []any{"app"}}}
	raw, err := m.Handle(context.Background(), "/api/schema/search-object-sources", map[string]any{"connection_id": "search", "query": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	result := raw.(ObjectSourceSearchResult)
	if len(result.Matches) != 2 || result.Truncated || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "hidden") {
		t.Fatalf("search result: %#v", result)
	}
	if hit := result.Matches[0]; hit.Database != "other" || hit.ObjectType != "PROCEDURE" || hit.Line != 3 || hit.Column != 11 || hit.MatchText != "needle" {
		t.Fatalf("stored procedure source position: %#v", hit)
	}
	if hit := result.Matches[1]; hit.ObjectType != "EVENT" || hit.ObjectName != "tick`event" || hit.MatchText != "NEEDLE" {
		t.Fatalf("event identity: %#v", hit)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), `"source"`) {
		t.Fatal("search response repeats object source text")
	}
}

func TestMySQLSearchableObjectsParameterizesDatabase(t *testing.T) {
	name := "app' OR 1=1 --"
	connector := &objectSearchConnector{query: func(_ context.Context, statement string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(statement, name) || len(args) != 2 || args[0].Value != name || args[1].Value != name {
			t.Fatalf("database scope was not parameterized: %s %#v", statement, args)
		}
		return &objectSearchRows{columns: []string{"object_name", "object_type", "database", "schema"}}, nil
	}}
	db := sql.OpenDB(connector)
	defer db.Close()
	if _, err := mysqlSearchableObjects(context.Background(), db, name, objectSourceSearchScope{}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresSearchObjectSourcesKeepsOverloadIdentity(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(fmt.Sprint(modern), func(t *testing.T) {
			connector := &objectSearchConnector{query: func(_ context.Context, statement string, args []driver.NamedValue) (driver.Rows, error) {
				if strings.Contains(statement, "AS has_kind") {
					return &objectSearchRows{columns: []string{"has_kind"}, values: [][]driver.Value{{modern}}}, nil
				}
				if !strings.Contains(statement, "pg_get_functiondef(p.oid)") || !strings.Contains(statement, "n.nspname !~ '^pg_'") || len(args) != 1 || args[0].Value != "postgres" {
					t.Fatalf("postgres definition query: %s %#v", statement, args)
				}
				if modern && !strings.Contains(statement, "p.prokind IN ('f','p','w')") || !modern && !strings.Contains(statement, "NOT p.proisagg") {
					t.Fatalf("postgres routine compatibility: %s", statement)
				}
				return &objectSearchRows{columns: []string{"object_name", "object_type", "database", "schema", "signature", "source"}, values: [][]driver.Value{
					{"report", "FUNCTION", "postgres", "billing", "", "CREATE OR REPLACE FUNCTION billing.report() RETURNS text\nAS $$SELECT 'needle'$$ LANGUAGE sql"},
					{"report", "FUNCTION", "postgres", "billing", "n integer", "CREATE OR REPLACE FUNCTION billing.report(n integer) RETURNS text\nAS $$SELECT 'needle'$$ LANGUAGE sql"},
				}}, nil
			}}
			m := objectSearchManager(t, "postgres", "postgres", connector)
			m.connections["search"].config.Raw = map[string]any{"visible_schemas": map[string]any{"postgres": []any{"stale"}}}
			raw, err := m.Handle(context.Background(), "/api/schema/search-object-sources", map[string]any{"connectionId": "search", "query": "needle", "schemas": nil})
			if err != nil {
				t.Fatal(err)
			}
			result := raw.(ObjectSourceSearchResult)
			if len(result.Matches) != 2 || result.Matches[0].Schema != "billing" || result.Matches[0].Signature != "" || result.Matches[1].Signature != "n integer" || result.Truncated {
				t.Fatalf("overloaded results: %#v", result)
			}
			if len(connector.tx) != 1 || !connector.tx[0].ReadOnly || connector.tx[0].Isolation != driver.IsolationLevel(sql.LevelRepeatableRead) {
				t.Fatalf("search did not use a read-only catalog snapshot: %#v", connector.tx)
			}
		})
	}
}

func TestSearchObjectSourcesTruncation(t *testing.T) {
	for _, fixture := range []struct {
		source    string
		truncated bool
	}{{"needle needle", false}, {"needle needle needle", true}} {
		t.Run(fmt.Sprint(fixture.truncated), func(t *testing.T) {
			connector := &objectSearchConnector{query: func(_ context.Context, statement string, _ []driver.NamedValue) (driver.Rows, error) {
				if strings.Contains(statement, "information_schema.routines") {
					return &objectSearchRows{columns: []string{"object_name", "object_type", "database", "schema"}, values: [][]driver.Value{{"report", "PROCEDURE", "app", "app"}}}, nil
				}
				return &objectSearchRows{columns: []string{"Create Procedure"}, values: [][]driver.Value{{fixture.source}}}, nil
			}}
			m := objectSearchManager(t, "mysql", "app", connector)
			raw, err := m.Handle(context.Background(), "/api/schema/search-object-sources", map[string]any{"connection_id": "search", "query": "needle", "limit": 2})
			if err != nil {
				t.Fatal(err)
			}
			result := raw.(ObjectSourceSearchResult)
			if len(result.Matches) != 2 || result.Truncated != fixture.truncated {
				t.Fatalf("limit should require an extra match: %#v", result)
			}
		})
	}
}

func TestSearchObjectSourcesUnsupportedAndValidation(t *testing.T) {
	m, _ := testManager(t)
	if _, err := m.Handle(context.Background(), "/api/schema/search-object-sources", map[string]any{"connection_id": "test", "query": "needle"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("SQLite source search should explicitly be unsupported: %v", err)
	}
	for _, p := range []map[string]any{{"query": "[", "is_regex": true}, {"query": strings.Repeat("x", 4097)}, {"query": "needle", "limit": 2001}, {"query": "needle", "limit": 0}} {
		if _, err := parseObjectSourceSearch(p); err == nil {
			t.Fatalf("invalid search was accepted: %#v", p)
		}
	}
}

func TestSearchObjectSourcesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	connector := &objectSearchConnector{query: func(ctx context.Context, statement string, _ []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(statement, "information_schema.routines") {
			return &objectSearchRows{columns: []string{"object_name", "object_type", "database", "schema"}, values: [][]driver.Value{{"report", "PROCEDURE", "app", "app"}}}, nil
		}
		cancel()
		return nil, ctx.Err()
	}}
	m := objectSearchManager(t, "mysql", "app", connector)
	p := map[string]any{"connection_id": "search", "query": "needle"}
	if _, err := m.Handle(ctx, "/api/schema/search-object-sources", p); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was swallowed as a source warning: %v", err)
	}
	calls := len(connector.calls)
	if _, err := m.Handle(ctx, "/api/schema/search-object-sources", p); !errors.Is(err, context.Canceled) || len(connector.calls) != calls {
		t.Fatalf("cancelled search reached the database: %v, calls %#v", err, connector.calls)
	}
}

func TestObjectSourceSearchVisibility(t *testing.T) {
	config := Config{Raw: map[string]any{
		"visible_databases":         []any{"app", "reports"},
		"visible_database_patterns": []any{"team_%", " ", "sales^_%"},
		"visible_schemas":           map[string]any{"app": []any{"billing"}},
	}}
	scope, err := objectSourceSearchVisibility(nil, config, "app")
	if err != nil {
		t.Fatal(err)
	}
	if !scope.databases.enabled || !reflect.DeepEqual(scope.databases.exact, []string{"app", "reports"}) || !reflect.DeepEqual(scope.databases.patterns, []string{"team_%", "sales^_%"}) || !scope.schemas.enabled || !reflect.DeepEqual(scope.schemas.exact, []string{"billing"}) {
		t.Fatalf("connection visibility was not retained: %#v", scope)
	}
	args := []any{}
	predicate := objectSourceFilterSQL("routine_schema", scope.databases, &args, false)
	if !strings.Contains(predicate, "BINARY routine_schema IN (?,?) OR BINARY routine_schema LIKE ? ESCAPE '^'") || !reflect.DeepEqual(args, []any{"app", "reports", "team_%", "sales^^_%"}) {
		t.Fatalf("exact/pattern union was not parameterized: %s %#v", predicate, args)
	}
	scope, err = objectSourceSearchVisibility(map[string]any{"databases": []any{"new"}, "schemas": []string{}}, config, "app")
	if err != nil || !reflect.DeepEqual(scope.databases.exact, []string{"new"}) || len(scope.databases.patterns) != 0 || !scope.schemas.enabled {
		t.Fatalf("current UI visibility did not override connection snapshot: %#v %v", scope, err)
	}
	if predicate := objectSourceFilterSQL("n.nspname", scope.schemas, &args, true); predicate != " AND 1=0" {
		t.Fatalf("empty visible schemas should scan nothing: %s", predicate)
	}
	for _, p := range []map[string]any{{"databases": "app"}, {"schemas": []any{1}}} {
		if _, err := objectSourceSearchVisibility(p, config, "app"); err == nil {
			t.Fatalf("malformed scope accepted: %#v", p)
		}
	}
	config.Raw["visible_schemas"] = map[string][]string{"other": {"other"}}
	if scope, _ := objectSourceSearchVisibility(nil, config, "app"); scope.schemas.enabled {
		t.Fatal("missing visible_schemas entry was treated as an empty selection")
	}
}

func TestObjectSourceSearchCurrentSchemaScope(t *testing.T) {
	config := Config{Type: "mysql", Database: "app", Raw: map[string]any{
		"visible_schemas": map[string]any{"app": []any{"app"}},
	}}
	scope, err := objectSourceSearchVisibility(map[string]any{"databases": []string{"app", "other"}}, config, "app")
	if err != nil || scope.schemas.enabled {
		t.Fatalf("MySQL default schema selection constrained other databases: %#v %v", scope, err)
	}
	scope, err = objectSourceSearchVisibility(map[string]any{"schemas": []string{"other"}}, config, "app")
	if err != nil || !scope.schemas.enabled || !reflect.DeepEqual(scope.schemas.exact, []string{"other"}) {
		t.Fatalf("explicit MySQL schema scope was not retained: %#v %v", scope, err)
	}
	config.Type = "postgres"
	scope, err = objectSourceSearchVisibility(map[string]any{"schemas": nil}, config, "app")
	if err != nil || scope.schemas.enabled {
		t.Fatalf("null schema scope did not clear a stale PostgreSQL selection: %#v %v", scope, err)
	}
	scope, err = objectSourceSearchVisibility(map[string]any{"schemas": []string{}}, config, "app")
	if err != nil || !scope.schemas.enabled {
		t.Fatalf("empty PostgreSQL schema selection was treated as unrestricted: %#v %v", scope, err)
	}
}

func TestMySQLSearchObjectSourcesFiltersBeforeFetchingDefinitions(t *testing.T) {
	connector := &objectSearchConnector{query: func(_ context.Context, statement string, args []driver.NamedValue) (driver.Rows, error) {
		if !strings.Contains(statement, "BINARY routine_schema IN (?)") || !strings.Contains(statement, "BINARY event_schema IN (?)") || len(args) != 2 || args[0].Value != "visible" || args[1].Value != "visible" {
			t.Fatalf("hidden database definition could be fetched: %s %#v", statement, args)
		}
		return &objectSearchRows{columns: []string{"object_name", "object_type", "database", "schema"}}, nil
	}}
	m := objectSearchManager(t, "mysql", "app", connector)
	if _, err := m.Handle(context.Background(), "/api/schema/search-object-sources", map[string]any{"connection_id": "search", "query": "needle", "databases": []string{"visible"}}); err != nil {
		t.Fatal(err)
	}
	if len(connector.calls) != 1 {
		t.Fatalf("search fetched an invisible definition: %#v", connector.calls)
	}
}

func TestSearchObjectSourcesScanLimit(t *testing.T) {
	values := make([][]driver.Value, objectSourceSearchScanLimit+1)
	for index := range values {
		values[index] = []driver.Value{fmt.Sprintf("routine_%d", index), "PROCEDURE", "app", "app"}
	}
	connector := &objectSearchConnector{query: func(_ context.Context, statement string, _ []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(statement, "information_schema.routines") {
			return &objectSearchRows{columns: []string{"object_name", "object_type", "database", "schema"}, values: values}, nil
		}
		return &objectSearchRows{columns: []string{"Create Procedure"}, values: [][]driver.Value{{"SELECT 1"}}}, nil
	}}
	m := objectSearchManager(t, "mysql", "app", connector)
	raw, err := m.Handle(context.Background(), "/api/schema/search-object-sources", map[string]any{"connection_id": "search", "query": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	result := raw.(ObjectSourceSearchResult)
	if !result.Truncated || len(result.Warnings) != 1 || len(connector.calls) != objectSourceSearchScanLimit+1 {
		t.Fatalf("scan limit not disclosed or respected: %#v, %d queries", result, len(connector.calls))
	}
}
