package database

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sugaryiiy/magpiedb/internal/agent"
)

func init() {
	if os.Getenv("DBX_DATABASE_AGENT_FIXTURE") == "1" {
		runDatabaseAgentFixture()
		os.Exit(0)
	}
}
func runDatabaseAgentFixture() {
	var writeMu sync.Mutex
	var mu sync.Mutex
	database := ""
	sessionID := ""
	cursorCount := 0
	respond := func(id uint64, result any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	fmt.Println(`{"ready":true}`)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     uint64         `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		if source := os.Getenv("DBX_DATABASE_AGENT_REQUESTS"); source != "" {
			file, _ := os.OpenFile(source, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if file != nil {
				_, _ = file.Write(append(append([]byte{}, scanner.Bytes()...), '\n'))
				file.Close()
			}
		}
		go func() {
			switch request.Method {
			case "handshake":
				if os.Getenv("DBX_DATABASE_AGENT_MQ") == "1" {
					respond(request.ID, map[string]any{"protocolVersion": 1, "capabilities": []string{"mq_connect", "mq_topics", "mq_messages"}})
					return
				}
				respond(request.ID, map[string]any{"protocolVersion": 2, "capabilities": []string{"multi_session", "metadata", "query", "paged_query", "transaction"}})
			case "open_session":
				if os.Getenv("DBX_DATABASE_AGENT_BLOCK_OPEN_SESSION") == "1" {
					select {}
				}
				mu.Lock()
				database, _ = request.Params["database"].(string)
				sessionID, _ = request.Params["agentSessionId"].(string)
				mu.Unlock()
				respond(request.ID, map[string]any{"ok": true})
			case "connection_info":
				respond(request.ID, map[string]any{"identifierQuote": "\"", "databaseInfo": map[string]any{"productName": "Fixture"}})
			case "list_databases":
				mu.Lock()
				name := database
				mu.Unlock()
				respond(request.ID, []map[string]any{{"name": name}})
			case "execute_query", "execute_query_page", "fetch_query_page":
				if request.Params["sql"] == "SELECT block" {
					select {}
				}
				mu.Lock()
				row := []any{database, os.Getpid(), sessionID}
				cursorCount++
				cursor := fmt.Sprintf("%s-%d", sessionID, cursorCount)
				mu.Unlock()
				result := map[string]any{"columns": []string{"db", "pid", "session"}, "rows": [][]any{row}, "affected_rows": 0, "execution_time_ms": 0, "has_more": false}
				if request.Method == "execute_query_page" {
					result["session_id"] = cursor
					result["has_more"] = true
				}
				respond(request.ID, result)
			case "test_connection":
				respond(request.ID, map[string]any{"serverVersion": "fixture", "aclEnabled": false})
			case "mq_list_topics":
				respond(request.ID, map[string]any{"topics": []map[string]any{{"name": "orders", "partitions": 3}}})
			case "mq_peek_messages":
				respond(request.ID, map[string]any{"messages": []map[string]any{{"offset": 0, "partition": 0, "payloadBase64": "eA==", "payloadText": "x"}}, "incomplete": true})
			case "mq_list_consumer_groups":
				respond(request.ID, map[string]any{"groups": []map[string]any{{"groupId": "worker", "committedOffsets": []map[string]any{{"topic": "orders", "partition": 0, "offset": 4}}, "endOffsets": []map[string]any{{"topic": "orders", "partition": 0, "offset": 10}}}}})
			case "close_query_session":
				respond(request.ID, true)
			default:
				respond(request.ID, map[string]any{"ok": true})
			}
		}()
	}
}
func installFixtureAgent(t *testing.T) *agent.Store {
	t.Helper()
	t.Setenv("DBX_DATABASE_AGENT_FIXTURE", "1")
	store, err := agent.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(store.Root, "drivers", "h2")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	name := "agent"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(file, source)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
func TestAgentManagerRoutesSessionsCursorsAndWriteLocks(t *testing.T) {
	store := installFixtureAgent(t)
	requests := filepath.Join(t.TempDir(), "requests.jsonl")
	t.Setenv("DBX_DATABASE_AGENT_REQUESTS", requests)
	manager := NewManager()
	manager.SetAgentStore(store)
	defer manager.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := manager.Connect(ctx, Config{ID: "fixture", Type: "h2", Database: "db-a", ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	p := map[string]any{"connectionId": "fixture", "database": "db-b", "clientSessionId": "editor-b", "sql": "SELECT 1", "pageSize": 2}
	raw, err := manager.Handle(ctx, "/api/query/execute", p)
	if err != nil {
		t.Fatal(err)
	}
	first := raw.(map[string]any)
	row := first["rows"].([]any)[0].([]any)
	if row[0] != "db-b" {
		t.Fatalf("query routed to wrong database: %v", row)
	}
	cursor := first["session_id"].(string)
	p2 := map[string]any{"connectionId": "fixture", "database": "db-a", "clientSessionId": "editor-a", "sql": "SELECT 2"}
	raw, err = manager.Handle(ctx, "/api/query/execute", p2)
	if err != nil {
		t.Fatal(err)
	}
	row2 := raw.(map[string]any)["rows"].([]any)[0].([]any)
	if row2[0] != "db-a" || row2[1] == row[1] {
		t.Fatalf("editor sessions leaked: %v %v", row, row2)
	}
	for _, sql := range []string{"UPDATE t SET x=1", "SELECT dangerous_function()", "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", "SELECT 1 INTO new_table"} {
		p2["sql"] = sql
		if _, err = manager.Handle(ctx, "/api/query/execute", p2); err == nil {
			t.Fatalf("write-lock bypass accepted: %s", sql)
		}
	}
	raw, err = manager.Handle(ctx, "/api/query/execute", map[string]any{"connectionId": "fixture", "resultSessionId": cursor, "pageSize": 2})
	if err != nil {
		t.Fatal(err)
	}
	if raw.(map[string]any)["rows"].([]any)[0].([]any)[0] != "db-b" {
		t.Fatal("cursor fetched from wrong database")
	}
	if raw, err = manager.Handle(ctx, "/api/query/close-session", map[string]any{"connectionId": "fixture", "sessionId": cursor}); err != nil || raw != true {
		t.Fatalf("cursor close: %v %v", raw, err)
	}
	if _, err = manager.Handle(ctx, "/api/query/execute", map[string]any{"connectionId": "fixture", "resultSessionId": cursor}); err == nil {
		t.Fatal("closed cursor remained routable")
	}
	if raw, err = manager.Handle(ctx, "/api/connection/identifier-quote", map[string]any{"connectionId": "fixture"}); err != nil || raw != "\"" {
		t.Fatalf("identifier quote: %v %v", raw, err)
	}
	if _, err = manager.Handle(ctx, "/api/query/close-client-session", map[string]any{"connectionId": "fixture", "database": "db-b", "clientSessionId": "editor-b"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(requests)
	scanner := bufio.NewScanner(bytesReader(data))
	for scanner.Scan() {
		var request map[string]any
		_ = json.Unmarshal(scanner.Bytes(), &request)
		if request["method"] == "execute_query" {
			params := request["params"].(map[string]any)
			if params["sql"] != "SELECT 2" {
				t.Fatalf("locked operation reached driver: %v", params)
			}
		}
	}
}
func bytesReader(data []byte) io.Reader { return strings.NewReader(string(data)) }

func waitForAgentFixtureRequest(t *testing.T, ctx context.Context, requests, method, sql string) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(requests)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(bytesReader(data))
		for scanner.Scan() {
			var request struct {
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			// A writer may still be completing the last line while we read it.
			if json.Unmarshal(scanner.Bytes(), &request) == nil && request.Method == method && (sql == "" || request.Params["sql"] == sql) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("agent fixture never received %s (%s): %v", method, sql, ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestAgentManagerCancelRequiresReconnectAndPreservesOtherEditor(t *testing.T) {
	store := installFixtureAgent(t)
	requests := filepath.Join(t.TempDir(), "requests.jsonl")
	t.Setenv("DBX_DATABASE_AGENT_REQUESTS", requests)
	manager := NewManager()
	manager.SetAgentStore(store)
	defer manager.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := manager.Connect(ctx, Config{ID: "fixture", Type: "h2", Database: "db-a"}); err != nil {
		t.Fatal(err)
	}
	active := make(chan error, 1)
	go func() {
		_, err := manager.Handle(ctx, "/api/query/execute", map[string]any{"connectionId": "fixture", "sql": "SELECT block", "clientSessionId": "blocked", "executionId": "cancel-test"})
		active <- err
	}()
	// Registration precedes editor initialization, so wait for the SQL itself
	// to reach the driver before asserting that cancellation quarantines it.
	waitForAgentFixtureRequest(t, ctx, requests, "execute_query", "SELECT block")
	if found, err := manager.Handle(ctx, "/api/query/cancel", map[string]any{"executionId": "cancel-test"}); err != nil || found != true {
		t.Fatalf("cancel did not find the running query: %v %v", found, err)
	}
	select {
	case err := <-active:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected query cancellation, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel leaked")
	}
	if _, err := manager.Handle(ctx, "/api/query/execute", map[string]any{"connectionId": "fixture", "sql": "SELECT 1", "clientSessionId": "blocked"}); err == nil {
		t.Fatal("cancelled session reused")
	}
	if _, err := manager.Handle(ctx, "/api/query/execute", map[string]any{"connectionId": "fixture", "sql": "SELECT 1", "clientSessionId": "other"}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentManagerCancelDuringSessionInitializationDoesNotExecuteQuery(t *testing.T) {
	store := installFixtureAgent(t)
	manager := NewManager()
	manager.SetAgentStore(store)
	defer manager.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := manager.Connect(ctx, Config{ID: "fixture", Type: "h2", Database: "db-a"}); err != nil {
		t.Fatal(err)
	}
	requests := filepath.Join(t.TempDir(), "requests.jsonl")
	t.Setenv("DBX_DATABASE_AGENT_REQUESTS", requests)
	t.Setenv("DBX_DATABASE_AGENT_BLOCK_OPEN_SESSION", "1")
	active := make(chan error, 1)
	go func() {
		_, err := manager.Handle(ctx, "/api/query/execute", map[string]any{"connectionId": "fixture", "sql": "SELECT block", "clientSessionId": "opening", "executionId": "cancel-opening"})
		active <- err
	}()
	waitForAgentFixtureRequest(t, ctx, requests, "open_session", "")
	if found, err := manager.Handle(ctx, "/api/query/cancel", map[string]any{"executionId": "cancel-opening"}); err != nil || found != true {
		t.Fatalf("cancel did not find the initializing query: %v %v", found, err)
	}
	select {
	case err := <-active:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected initialization cancellation, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled initialization leaked")
	}
	t.Setenv("DBX_DATABASE_AGENT_BLOCK_OPEN_SESSION", "")
	// No SQL reached the initializing session; a later explicit query may
	// safely create a new editor session, and other editors remain available.
	for _, client := range []string{"opening", "other"} {
		if _, err := manager.Handle(ctx, "/api/query/execute", map[string]any{"connectionId": "fixture", "sql": "SELECT 1", "clientSessionId": client}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(requests)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"sql":"SELECT block"`) {
		t.Fatal("cancelled initialization executed or replayed its query")
	}
}

func TestAgentMQProtocolRoutingResultsAndWriteProtection(t *testing.T) {
	store := installFixtureAgent(t)
	t.Setenv("DBX_DATABASE_AGENT_MQ", "1")
	requests := filepath.Join(t.TempDir(), "mq-requests.jsonl")
	t.Setenv("DBX_DATABASE_AGENT_REQUESTS", requests)
	if err := os.Rename(filepath.Join(store.Root, "drivers", "h2"), filepath.Join(store.Root, "drivers", "kafka")); err != nil {
		t.Fatal(err)
	}
	config := Config{Type: "mq", DriverProfile: "kafka", Host: "broker:9092", Raw: map[string]any{"external_config": map[string]any{"systemKind": "kafka", "auth": map[string]any{"kind": "basic", "username": "mq-user", "password": "mq-secret"}, "extra": map[string]any{"bootstrapServers": "broker:9092"}}}}
	manager := NewManager()
	manager.SetAgentStore(store)
	defer manager.Close()
	driver, err := manager.openNative(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	topics, err := driver.Handle(context.Background(), "/api/mq/topics/list", map[string]any{}, true)
	if err != nil {
		t.Fatal(err)
	}
	entries := topics.([]any)
	if len(entries) != 1 || mapValue(entries[0])["shortName"] != "orders" || mapValue(entries[0])["partitioned"] != true {
		t.Fatalf("bad topic result: %v", topics)
	}
	peek, err := driver.Handle(context.Background(), "/api/mq/subscriptions/peek-messages", map[string]any{"topic": map[string]any{"topic": "orders"}, "count": 2, "options": map[string]any{"partition": 0, "offset": 0}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if mapValue(peek)["incomplete"] != true || mapValue(anySlice(mapValue(peek)["messages"])[0])["messageId"] != "0" {
		t.Fatalf("lost offset 0 or partial response: %v", peek)
	}
	subs, err := driver.Handle(context.Background(), "/api/mq/subscriptions/list", map[string]any{"topic": map[string]any{"topic": "orders"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if integer(mapValue(subs.([]any)[0]), 0, "msgBacklog") != 6 {
		t.Fatalf("lag mismatch: %v", subs)
	}
	if _, err := driver.Handle(context.Background(), "/api/mq/topics/create", map[string]any{"topic": map[string]any{"topic": "blocked"}}, true); err == nil {
		t.Fatal("locked topic creation accepted")
	}
	data, err := os.ReadFile(requests)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "mq_create_topic") {
		t.Fatal("blocked write reached agent")
	}
	if !strings.Contains(string(data), `"bootstrap_servers":"broker:9092"`) || !strings.Contains(string(data), `"sasl_password":"mq-secret"`) || !strings.Contains(string(data), `"method":"connect"`) {
		t.Fatalf("MQ connection protocol mismatch: %s", data)
	}
}
