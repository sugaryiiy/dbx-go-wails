package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func aiTestRequest(endpoint, provider, style string) map[string]any {
	return map[string]any{"config": map[string]any{"provider": provider, "apiKey": "mock-only-token", "endpoint": endpoint, "model": "test-model", "apiStyle": style}, "systemPrompt": "Be brief", "messages": []any{map[string]any{"role": "user", "content": "Hello"}}, "maxTokens": 32}
}
func TestAIProviderProtocolsAndModels(t *testing.T) {
	for _, tc := range []struct{ provider, style, path, response string }{{"openai", "completions", "/v1/chat/completions", `{"choices":[{"message":{"content":"chat answer"}}]}`}, {"openai", "responses", "/v1/responses", `{"output":[{"content":[{"type":"output_text","text":"responses answer"}]}]}`}, {"claude", "anthropic-messages", "/v1/messages", `{"content":[{"type":"text","text":"anthropic answer"}]}`}, {"gemini", "completions", "/v1beta/models/test-model:generateContent", `{"candidates":[{"content":{"parts":[{"text":"gemini answer"}]}}]}`}} {
		t.Run(tc.provider+tc.style, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("path %s", r.URL.Path)
				}
				if tc.provider == "claude" && r.Header.Get("x-api-key") != "mock-only-token" {
					t.Error("missing Anthropic auth")
				}
				if tc.provider == "gemini" && r.Header.Get("x-goog-api-key") != "mock-only-token" {
					t.Error("missing Gemini auth")
				}
				var b map[string]any
				if json.NewDecoder(r.Body).Decode(&b) != nil {
					t.Error("invalid body")
				}
				if tc.provider == "openai" && tc.style == "responses" && b["max_output_tokens"] != float64(32) {
					t.Error("response budget missing")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.response)
			}))
			defer provider.Close()
			s := testServer(t, t.TempDir())
			cookie := loginTest(t, s)
			result := value(t, call(s, "POST", "/api/ai/complete", map[string]any{"request": aiTestRequest(provider.URL, tc.provider, tc.style)}, cookie, "")).(string)
			if !strings.Contains(result, "answer") {
				t.Fatal(result)
			}
		})
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Error(r.URL.Path)
		}
		fmt.Fprint(w, `{"data":[{"id":"model-a","display_name":"Model A"}]}`)
	}))
	defer provider.Close()
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	models := value(t, call(s, "POST", "/api/ai/models", map[string]any{"config": aiTestRequest(provider.URL, "openai", "completions")["config"]}, cookie, "")).([]any)
	if len(models) != 1 || models[0].(map[string]any)["id"] != "model-a" {
		t.Fatal(models)
	}
}
func TestAIStreamProtocolCancellationAndCredentialRedaction(t *testing.T) {
	started := make(chan struct{}, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(r.Header.Get("X-Test-Mode"), "cancel") {
			started <- struct{}{}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\",\"reasoning_content\":\"think\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	req := aiTestRequest(provider.URL, "openai", "completions")
	w := call(s, "POST", "/api/ai/stream", map[string]any{"session_id": "stream-1", "request": req}, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"delta":"hello"`) || !strings.Contains(w.Body.String(), `"reasoning_delta":"think"`) || !strings.Contains(w.Body.String(), `"done":true`) {
		t.Fatal(w.Code, w.Body)
	}
	appObject(req["config"])["customHeaders"] = map[string]any{"X-Test-Mode": "mock-cancel-mode"}
	done := make(chan *httptest.ResponseRecorder)
	go func() {
		done <- call(s, "POST", "/api/ai/stream", map[string]any{"session_id": "cancel-1", "request": req}, cookie, "")
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("stream not started")
	}
	if value(t, call(s, "POST", "/api/ai/cancel-stream", map[string]any{"sessionId": "cancel-1"}, cookie, "")) != true {
		t.Fatal("cancel not found")
	}
	select {
	case result := <-done:
		if !strings.Contains(result.Body.String(), "canceled") {
			t.Fatal(result.Body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not close upstream")
	}
	secret := "mock-private-error-token"
	errServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprintf(w, `{"error":{"message":"Rejected %s"}}`, secret)
	}))
	defer errServer.Close()
	req = aiTestRequest(errServer.URL, "openai", "completions")
	appObject(req["config"])["apiKey"] = secret
	w = call(s, "POST", "/api/ai/complete", map[string]any{"request": req}, cookie, "")
	if w.Code == 200 || strings.Contains(w.Body.String(), secret) || !strings.Contains(w.Body.String(), "[REDACTED]") {
		t.Fatal(w.Code, w.Body)
	}
}
func TestAIStateEncryptedAndPromptPersistence(t *testing.T) {
	dir := t.TempDir()
	s := testServer(t, dir)
	cookie := loginTest(t, s)
	config := appObject(aiTestRequest("https://example.invalid", "openai", "completions")["config"])
	config["id"] = "a"
	config["name"] = "A"
	config["isDefault"] = true
	value(t, call(s, "POST", "/api/ai/config-item", map[string]any{"config": config}, cookie, ""))
	conversation := map[string]any{"id": "conversation", "title": "Saved", "messages": []any{}, "updatedAt": "2026-10-08T00:00:00Z"}
	value(t, call(s, "POST", "/api/ai/conversation", map[string]any{"conversation": conversation}, cookie, ""))
	template := value(t, call(s, "POST", "/api/prompt-templates", map[string]any{"id": "template", "name": "SQL", "content": "review SQL"}, cookie, "")).(map[string]any)
	if template["createdAt"] == nil {
		t.Fatal(template)
	}
	if call(s, "POST", "/api/prompt-templates", map[string]any{"id": "long", "name": "Long", "content": strings.Repeat("界", 8001)}, cookie, "").Code == 200 {
		t.Fatal("unicode character limit bypass")
	}
	b, err := os.ReadFile(filepath.Join(dir, "state.enc"))
	if err != nil || bytes.Contains(b, []byte("mock-only-token")) {
		t.Fatal("AI token plaintext", err)
	}
	s.Close()
	again := testServer(t, dir)
	cookie = loginTest(t, again)
	configs := value(t, call(again, "GET", "/api/ai/configs", nil, cookie, "")).([]any)
	if len(configs) != 1 || configs[0].(map[string]any)["apiKey"] != "mock-only-token" {
		t.Fatal(configs)
	}
	if len(value(t, call(again, "GET", "/api/ai/conversations", nil, cookie, "")).([]any)) != 1 {
		t.Fatal("conversation missing")
	}
	if call(again, "POST", "/api/ai/default-config", map[string]any{"configId": "missing"}, cookie, "").Code == 200 {
		t.Fatal("missing default accepted")
	}
}
func TestAIAgentToolLoopAndExactWriteGuard(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	config := map[string]any{"id": "agent-db", "name": "Agent", "db_type": "sqlite", "host": filepath.Join(t.TempDir(), "agent.sqlite"), "database": "main"}
	value(t, call(s, "POST", "/api/connection/save", map[string]any{"configs": []any{config}}, cookie, ""))
	value(t, call(s, "POST", "/api/connection/connect", map[string]any{"config": config}, cookie, ""))
	value(t, call(s, "POST", "/api/query/execute", map[string]any{"connectionId": "agent-db", "database": "main", "sql": "CREATE TABLE test (id INTEGER)"}, cookie, ""))
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"q","function":{"name":"execute_query","arguments":"{\"sql\":\"SELECT 42\"}"}}]}}]}`)
		} else {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			messages := appItems(body["messages"])
			if len(messages) != 4 || str(messages[len(messages)-1], "role") != "tool" {
				t.Errorf("tool replay missing: %#v", messages)
			}
			fmt.Fprint(w, `{"choices":[{"message":{"content":"42"}}]}`)
		}
	}))
	defer provider.Close()
	p := map[string]any{"sessionId": "agent", "connectionId": "agent-db", "database": "main", "mode": "ask", "request": aiTestRequest(provider.URL, "openai", "completions")}
	result := call(s, "POST", "/api/ai/agent-stream", p, cookie, "")
	if result.Code != 200 || !strings.Contains(result.Body.String(), `"type":"tool_call_end"`) || !strings.Contains(result.Body.String(), `"type":"agent_end"`) {
		t.Fatal(result.Code, result.Body)
	}
	p["mode"] = "agent"
	sql := "INSERT INTO test VALUES (1)"
	emit := func(any) error { return nil }
	tool := map[string]any{"name": "execute_query", "id": "w", "arguments": map[string]any{"sql": sql}}
	if _, err := s.aiAgentTool(context.Background(), p, tool, emit); err == nil {
		t.Fatal("unconfirmed write ran")
	}
	p["allowWriteSql"] = true
	p["confirmedWriteSql"] = sql
	p["confirmedConnectionId"] = "agent-db"
	p["confirmedDatabase"] = "main"
	p["confirmedSchema"] = ""
	if _, err := s.aiAgentTool(context.Background(), p, tool, emit); err != nil {
		t.Fatal(err)
	}
	p["confirmedDatabase"] = "different"
	if _, err := s.aiAgentTool(context.Background(), p, tool, emit); err == nil {
		t.Fatal("cross-target confirmation ran")
	}
}
func aiDesktopCall(s *Server, method, path string, p any) *httptest.ResponseRecorder {
	var b bytes.Buffer
	if p != nil {
		json.NewEncoder(&b).Encode(p)
	}
	r := httptest.NewRequest(method, path, &b)
	r.RemoteAddr = ""
	if p != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Origin", "wails://wails")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestWailsAIBackgroundEventsAndShutdown(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Wails\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	s, err := New(Options{DataDir: t.TempDir(), Desktop: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	result := aiDesktopCall(s, "POST", "/api/ai/stream", map[string]any{"sessionId": "wails-stream", "background": true, "request": aiTestRequest(provider.URL, "openai", "completions")})
	if result.Code != 200 {
		t.Fatal(result.Body)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		progress := value(t, aiDesktopCall(s, "GET", "/api/ai/stream-events?session_id=wails-stream", nil)).(map[string]any)
		if progress["done"] == true {
			events := progress["events"].([]any)
			if len(events) != 2 || events[0].(map[string]any)["delta"] != "Wails" {
				t.Fatal(events)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background job never completed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
