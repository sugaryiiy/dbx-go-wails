package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func aiWriteFixture(t *testing.T) (*Server, *http.Cookie) {
	t.Helper()
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	config := map[string]any{"id": "ai-write-db", "name": "AI Write Test", "db_type": "sqlite", "host": filepath.Join(t.TempDir(), "approval.sqlite"), "database": "main"}
	value(t, call(s, "POST", "/api/connection/save", map[string]any{"configs": []any{config}}, cookie, ""))
	value(t, call(s, "POST", "/api/connection/connect", map[string]any{"config": config}, cookie, ""))
	for _, sql := range []string{"CREATE TABLE counter (value INTEGER NOT NULL)", "INSERT INTO counter VALUES (0)"} {
		value(t, call(s, "POST", "/api/query/execute", map[string]any{"connectionId": "ai-write-db", "database": "main", "sql": sql}, cookie, ""))
	}
	return s, cookie
}
func aiWriteApproval(sql string) map[string]any {
	return map[string]any{"connectionId": "ai-write-db", "database": "main", "schema": "", "mode": "agent", "allowWriteSql": true, "confirmedWriteSql": sql, "confirmedConnectionId": "ai-write-db", "confirmedDatabase": "main", "confirmedSchema": ""}
}
func TestAIWriteApprovalIsSingleUseAcrossToolsAndTurns(t *testing.T) {
	s, cookie := aiWriteFixture(t)
	sql := "UPDATE counter SET value = value + 1"
	var turns atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		turn := turns.Add(1)
		calls := []any{}
		makeCall := func(id string) any {
			args, _ := json.Marshal(map[string]any{"sql": sql})
			return map[string]any{"id": id, "type": "function", "function": map[string]any{"name": "execute_query", "arguments": string(args)}}
		}
		if turn == 1 {
			calls = []any{makeCall("first"), makeCall("duplicate-same-turn")}
		}
		if turn == 2 {
			calls = []any{makeCall("retry-next-turn")}
		}
		message := map[string]any{"content": "done"}
		if len(calls) > 0 {
			message = map[string]any{"content": "", "tool_calls": calls}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}})
	}))
	defer provider.Close()
	request := aiWriteApproval(sql)
	request["sessionId"] = "single-use-write"
	request["request"] = aiTestRequest(provider.URL, "openai", "completions")
	result := call(s, "POST", "/api/ai/agent-stream", request, cookie, "")
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"type":"agent_end"`) {
		t.Fatalf("agent loop: %d %s", result.Code, result.Body.String())
	}
	if count := strings.Count(result.Body.String(), `"type":"write_sql_confirmation_required"`); count != 2 {
		t.Fatalf("expected duplicate and retry to need approval, got %d: %s", count, result.Body.String())
	}
	query := value(t, call(s, "POST", "/api/query/execute", map[string]any{"connectionId": "ai-write-db", "database": "main", "sql": "SELECT value FROM counter"}, cookie, "")).(map[string]any)
	rows := query["rows"].([]any)
	if got := rows[0].([]any)[0]; got != float64(1) {
		t.Fatalf("one confirmation dispatched more than one write: %v", got)
	}
	if turns.Load() != 3 {
		t.Fatalf("agent did not finish expected retries: %d", turns.Load())
	}
}
func TestAIWriteApprovalConsumedBeforeFailedDispatch(t *testing.T) {
	s, _ := aiWriteFixture(t)
	sql := "UPDATE missing_table SET value = 1"
	request := aiWriteApproval(sql)
	tool := map[string]any{"name": "execute_query", "arguments": map[string]any{"sql": sql}}
	emit := func(any) error { return nil }
	if _, err := s.aiAgentTool(context.Background(), request, tool, emit); err == nil || strings.Contains(err.Error(), "requires exact user confirmation") {
		t.Fatalf("expected initial database failure, got %v", err)
	}
	if request["allowWriteSql"] != false {
		t.Fatal("dispatch did not consume the write approval")
	}
	for _, key := range []string{"confirmedWriteSql", "confirmedConnectionId", "confirmedDatabase", "confirmedSchema"} {
		if _, ok := request[key]; ok {
			t.Errorf("confirmation remained after dispatch: %s", key)
		}
	}
	if _, err := s.aiAgentTool(context.Background(), request, tool, emit); err == nil || !strings.Contains(err.Error(), "requires exact user confirmation") {
		t.Fatalf("failed write retry reused approval: %v", err)
	}
}
func TestAIReadDoesNotConsumeWriteApproval(t *testing.T) {
	s, _ := aiWriteFixture(t)
	request := aiWriteApproval("UPDATE counter SET value = value + 1")
	tool := map[string]any{"name": "execute_query", "arguments": map[string]any{"sql": "SELECT value FROM counter"}}
	if _, err := s.aiAgentTool(context.Background(), request, tool, func(any) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if request["allowWriteSql"] != true || str(request, "confirmedWriteSql") == "" {
		t.Fatal("read consumed unrelated write approval")
	}
}

func TestAIWriteApprovalConsumedOnCancelledDispatch(t *testing.T) {
	s, cookie := aiWriteFixture(t)
	sql := "UPDATE counter SET value = value + 1"
	request := aiWriteApproval(sql)
	tool := map[string]any{"name": "execute_query", "arguments": map[string]any{"sql": sql}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.aiAgentTool(ctx, request, tool, func(any) error { return nil }); err == nil {
		t.Fatal("cancelled database dispatch succeeded")
	}
	if request["allowWriteSql"] != false {
		t.Fatal("cancelled dispatch retained approval")
	}
	if _, err := s.aiAgentTool(context.Background(), request, tool, func(any) error { return nil }); err == nil || !strings.Contains(err.Error(), "requires exact user confirmation") {
		t.Fatalf("cancelled dispatch retry reused approval: %v", err)
	}
	query := value(t, call(s, "POST", "/api/query/execute", map[string]any{"connectionId": "ai-write-db", "database": "main", "sql": "SELECT value FROM counter"}, cookie, "")).(map[string]any)
	if got := query["rows"].([]any)[0].([]any)[0]; got != float64(0) {
		t.Fatalf("cancelled/retried dispatch modified data: %v", got)
	}
}
