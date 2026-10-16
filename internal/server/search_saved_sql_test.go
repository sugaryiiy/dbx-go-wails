package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func savedSQLSearchRequest(t *testing.T, s *Server, cookie *http.Cookie, payload map[string]any) savedSQLSearchResult {
	t.Helper()
	response := call(s, "POST", "/api/saved-sql/search", payload, cookie, "")
	if response.Code != http.StatusOK {
		t.Fatalf("search failed: %d %s", response.Code, response.Body)
	}
	var result savedSQLSearchResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Matches == nil {
		t.Fatal("empty matches must be an array")
	}
	return result
}

func TestSavedSQLSearchScopeAndReadOnly(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	files := []map[string]any{
		{"id": "mine", "connectionId": "one", "name": "daily query", "database": "db", "catalog": "cat", "schema": "public", "sql": "select Needle, NEEDLE"},
		{"id": "other", "connectionId": "two", "name": "Needle", "sql": "Needle"},
		{"id": "unscoped", "name": "Needle", "sql": "Needle"},
	}
	if err := s.update(func(state *diskState) error { state.SQLFiles = files; return nil }); err != nil {
		t.Fatal(err)
	}
	before := s.snapshot()
	result := savedSQLSearchRequest(t, s, cookie, map[string]any{"connection_id": "one", "query": "needle"})
	if result.Truncated || len(result.Matches) != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	for _, match := range result.Matches {
		if match.ID != "mine" || match.ConnectionID != "one" || match.Database != "db" || match.Catalog != "cat" || match.Schema != "public" {
			t.Fatalf("wrong scope: %+v", match)
		}
	}
	if !reflect.DeepEqual(before, s.snapshot()) {
		t.Fatal("search mutated saved SQL state")
	}
	for _, connection := range []any{nil, "", "   ", 123} {
		response := call(s, "POST", "/api/saved-sql/search", map[string]any{"connection_id": connection, "query": "needle"}, cookie, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("missing connection accepted: %v, status %d", connection, response.Code)
		}
	}
	if got := savedSQLSearchRequest(t, s, cookie, map[string]any{"connection_id": "absent", "query": "needle"}); len(got.Matches) != 0 || got.Truncated {
		t.Fatalf("search leaked another connection: %+v", got)
	}
	if got := savedSQLSearchRequest(t, s, cookie, map[string]any{"connection_id": "one", "query": ""}); len(got.Matches) != 0 || got.Truncated {
		t.Fatalf("empty search returned results: %+v", got)
	}
}

func TestSavedSQLSearchUTF16CRLFLiteralAndName(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	if err := s.update(func(state *diskState) error {
		state.SQLFiles = []map[string]any{
			{"id": "body", "connectionId": "one", "name": "target target", "sql": "SELECT 1\r\n中😀 TaRgEt.target 中文\r\nTARGET"},
			{"id": "name", "connectionId": "one", "name": "中😀 Target report", "sql": "SELECT 1"},
			{"id": "fold", "connectionId": "one", "name": "fold", "sql": "中😀 Kk"},
			{"id": "literal", "connectionId": "one", "name": "literal", "sql": "a.b aXb a.b"},
			{"id": "overlap", "connectionId": "one", "name": "overlap", "sql": "aaaaa"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result := savedSQLSearchRequest(t, s, cookie, map[string]any{"connection_id": "one", "query": "target"})
	want := []savedSQLSearchMatch{
		{ID: "body", Name: "target target", ConnectionID: "one", Line: 2, Column: 5, LineText: "中😀 TaRgEt.target 中文", MatchText: "TaRgEt", Start: 4, End: 10},
		{ID: "body", Name: "target target", ConnectionID: "one", Line: 2, Column: 12, LineText: "中😀 TaRgEt.target 中文", MatchText: "target", Start: 11, End: 17},
		{ID: "body", Name: "target target", ConnectionID: "one", Line: 3, Column: 1, LineText: "TARGET", MatchText: "TARGET", Start: 0, End: 6},
		{ID: "name", Name: "中😀 Target report", ConnectionID: "one", Line: 0, Column: 5, LineText: "中😀 Target report", MatchText: "Target", Start: 4, End: 10},
	}
	if !reflect.DeepEqual(result.Matches, want) || result.Truncated {
		t.Fatalf("Unicode/CRLF/name search: got %+v; want %+v", result, want)
	}
	fold := savedSQLSearchRequest(t, s, cookie, map[string]any{"connection_id": "one", "query": "K"})
	if len(fold.Matches) != 2 || fold.Matches[0].MatchText != "K" || fold.Matches[0].Start != 4 || fold.Matches[0].End != 5 || fold.Matches[1].Start != 5 {
		t.Fatalf("Unicode case folding changed offsets: %+v", fold)
	}
	literal := savedSQLSearchRequest(t, s, cookie, map[string]any{"connection_id": "one", "query": "a.b"})
	if len(literal.Matches) != 2 || literal.Matches[0].Start != 0 || literal.Matches[1].Start != 8 {
		t.Fatalf("query was interpreted as a regular expression: %+v", literal)
	}
	overlap := savedSQLSearchRequest(t, s, cookie, map[string]any{"connection_id": "one", "query": "aa"})
	if len(overlap.Matches) != 2 || overlap.Matches[0].Start != 0 || overlap.Matches[1].Start != 2 {
		t.Fatalf("matches must not overlap: %+v", overlap)
	}
}

func TestSavedSQLSearchLimitsAndTruncation(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	setSQL := func(sql string) {
		t.Helper()
		if err := s.update(func(state *diskState) error {
			state.SQLFiles = []map[string]any{{"id": "one", "name": "saved", "connectionId": "one", "sql": sql}}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	setSQL(strings.Repeat("x ", 2001))
	for _, test := range []struct {
		name  string
		limit any
		want  int
	}{
		{"default", nil, 500},
		{"bounded", 3, 3},
		{"maximum", 99999, 2000},
		{"nonpositive", 0, 500},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := map[string]any{"connection_id": "one", "query": "x"}
			if test.limit != nil {
				payload["limit"] = test.limit
			}
			result := savedSQLSearchRequest(t, s, cookie, payload)
			if len(result.Matches) != test.want || !result.Truncated {
				t.Fatalf("limit/truncation: %+v", result)
			}
		})
	}
	setSQL("x x x")
	result := savedSQLSearchRequest(t, s, cookie, map[string]any{"connection_id": "one", "query": "x", "limit": 3})
	if len(result.Matches) != 3 || result.Truncated {
		t.Fatalf("exactly hitting limit must not mark truncation: %+v", result)
	}
	if err := s.update(func(state *diskState) error {
		state.SQLFiles = append(state.SQLFiles, map[string]any{"id": "later", "name": "x", "connectionId": "one", "sql": "SELECT 1"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result = savedSQLSearchRequest(t, s, cookie, map[string]any{"connection_id": "one", "query": "x", "limit": 3})
	if len(result.Matches) != 3 || !result.Truncated {
		t.Fatalf("name match after full result must mark truncation: %+v", result)
	}
}

func TestSavedSQLSearchFirstMatchPerQuery(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	if err := s.update(func(state *diskState) error {
		state.SQLFiles = []map[string]any{
			{"id": "busy", "connectionId": "one", "name": "Busy report", "sql": "SELECT 1\r\n😀 ORDERS orders\r\n" + strings.Repeat("SELECT orders;\n", 501)},
			{"id": "later", "connectionId": "one", "name": "Later report", "database": "biz", "schema": "public", "sql": "SELECT orders orders"},
			{"id": "name", "connectionId": "one", "name": "😀 orders orders", "sql": "SELECT 1"},
			{"id": "other", "connectionId": "two", "name": "orders", "sql": "SELECT orders"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"connection_id": "one", "query": "orders", "first_match_only": true, "limit": 500}
	result := savedSQLSearchRequest(t, s, cookie, payload)
	want := []savedSQLSearchMatch{
		{ID: "busy", Name: "Busy report", ConnectionID: "one", Line: 2, Column: 4, LineText: "😀 ORDERS orders", MatchText: "ORDERS", Start: 3, End: 9},
		{ID: "later", Name: "Later report", ConnectionID: "one", Database: "biz", Schema: "public", Line: 1, Column: 8, LineText: "SELECT orders orders", MatchText: "orders", Start: 7, End: 13},
		{ID: "name", Name: "😀 orders orders", ConnectionID: "one", Line: 0, Column: 4, LineText: "😀 orders orders", MatchText: "orders", Start: 3, End: 9},
	}
	if !reflect.DeepEqual(result.Matches, want) || result.Truncated {
		t.Fatalf("repeated body/name occurrences hid later queries or changed positions: got %+v; want %+v", result, want)
	}
	payload["limit"] = 3
	if exact := savedSQLSearchRequest(t, s, cookie, payload); len(exact.Matches) != 3 || exact.Truncated {
		t.Fatalf("exact query limit was marked truncated: %+v", exact)
	}
	payload["limit"] = 2
	if bounded := savedSQLSearchRequest(t, s, cookie, payload); len(bounded.Matches) != 2 || !bounded.Truncated || bounded.Matches[1].ID != "later" {
		t.Fatalf("later name fallback must mark truncation: %+v", bounded)
	}
	payload["limit"] = 500
	payload["exclude_ids"] = []string{"busy"}
	if excluded := savedSQLSearchRequest(t, s, cookie, payload); len(excluded.Matches) != 2 || excluded.Truncated || excluded.Matches[0].ID != "later" {
		t.Fatalf("excluded query consumed first-match slots: %+v", excluded)
	}
	delete(payload, "exclude_ids")
	payload["first_match_only"] = false
	if all := savedSQLSearchRequest(t, s, cookie, payload); len(all.Matches) != 500 || !all.Truncated || all.Matches[499].ID != "busy" {
		t.Fatalf("full-content search must retain occurrence limits: %+v", all)
	}
}

func TestSavedSQLSearchExclusionPreservesLimitAndScope(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	if err := s.update(func(state *diskState) error {
		state.SQLFiles = []map[string]any{
			{"id": "opened", "name": "x saved", "connectionId": "one", "sql": strings.Repeat("x ", 1000)},
			{"id": "opened-name", "name": "x saved", "connectionId": "one", "sql": "SELECT 1"},
			{"id": "kept", "name": "kept", "connectionId": "one", "sql": "x x x"},
			{"id": "other", "name": "x saved", "connectionId": "two", "sql": strings.Repeat("x ", 1000)},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"connection_id": "one", "query": "x", "limit": 3, "exclude_ids": []string{"opened", "opened-name", "other"}}
	result := savedSQLSearchRequest(t, s, cookie, payload)
	if len(result.Matches) != 3 || result.Truncated {
		t.Fatalf("excluded snapshots consumed limit or caused truncation: %+v", result)
	}
	for _, match := range result.Matches {
		if match.ID != "kept" || match.ConnectionID != "one" {
			t.Fatalf("excluded or wrong-connection match returned: %+v", match)
		}
	}
	payload["limit"] = 2
	result = savedSQLSearchRequest(t, s, cookie, payload)
	if len(result.Matches) != 2 || !result.Truncated {
		t.Fatalf("non-excluded matches must still cause truncation: %+v", result)
	}
	payload["exclude_ids"] = []string{"opened", "opened-name", "kept"}
	result = savedSQLSearchRequest(t, s, cookie, payload)
	if len(result.Matches) != 0 || result.Truncated {
		t.Fatalf("all snapshots excluded: %+v", result)
	}
	for _, invalid := range []any{"opened", []any{"opened", 1}, nil} {
		payload["exclude_ids"] = invalid
		response := call(s, "POST", "/api/saved-sql/search", payload, cookie, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid exclude_ids accepted: %v, status %d", invalid, response.Code)
		}
	}
}

func TestSavedSQLSearchCancellation(t *testing.T) {
	s := testServer(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := s.searchSavedSQL(ctx, map[string]any{"connection_id": "one", "query": "x"})
	if !errors.Is(err, context.Canceled) || len(result.Matches) != 0 {
		t.Fatalf("cancelled search continued: result %+v, error %v", result, err)
	}
}
