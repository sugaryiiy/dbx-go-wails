package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const maxAIResponseBytes = 16 << 20

type aiRuntime struct {
	mu      sync.Mutex
	streams map[string]context.CancelFunc
	jobs    map[string]*aiJob
	closed  bool
	wg      sync.WaitGroup
}

func newAIRuntime() *aiRuntime {
	return &aiRuntime{streams: map[string]context.CancelFunc{}, jobs: map[string]*aiJob{}}
}
func (a *aiRuntime) register(ctx context.Context, id string) (context.Context, func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, nil, errors.New("AI runtime is closed")
	}
	if !validAppID(id) {
		return nil, nil, errors.New("Valid sessionId is required")
	}
	if _, ok := a.streams[id]; ok {
		return nil, nil, errors.New("AI session is already running")
	}
	ctx, cancel := context.WithCancel(ctx)
	a.streams[id] = cancel
	a.wg.Add(1)
	return ctx, func() { cancel(); a.mu.Lock(); delete(a.streams, id); a.mu.Unlock(); a.wg.Done() }, nil
}
func (a *aiRuntime) cancel(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	cancel, ok := a.streams[id]
	if ok {
		cancel()
	}
	return ok
}
func (s *Server) closeAI() {
	if s.aiRuntime == nil {
		return
	}
	a := s.aiRuntime
	a.mu.Lock()
	a.closed = true
	for _, cancel := range a.streams {
		cancel()
	}
	a.mu.Unlock()
	a.wg.Wait()
}

func aiSessionID(p map[string]any) string {
	if id := str(p, "sessionId"); id != "" {
		return id
	}
	return str(p, "session_id")
}
func aiAnthropic(c map[string]any) bool {
	return str(c, "provider") == "claude" || str(c, "provider") == "anthropic-compatible" || str(c, "apiStyle") == "anthropic-messages"
}
func aiEndpoint(c map[string]any, models, stream bool) (string, error) {
	if strings.HasSuffix(str(c, "provider"), "-cli") {
		return "", errors.New("This CLI AI provider has not yet been migrated to Go")
	}
	ep := strings.TrimRight(strings.TrimSpace(str(c, "endpoint")), "/")
	u, err := url.Parse(ep)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Fragment != "" || u.RawQuery != "" {
		return "", errors.New("AI endpoint must be an HTTP(S) URL without credentials, query or fragment")
	}
	if str(c, "provider") == "gemini" {
		base := strings.Split(ep, "/v1beta/models/")[0]
		base = strings.TrimSuffix(base, "/v1beta")
		if models {
			return base + "/v1beta/models", nil
		}
		if str(c, "model") == "" {
			return "", errors.New("Model is required")
		}
		action := ":generateContent"
		if stream {
			action = ":streamGenerateContent?alt=sse"
		}
		return base + "/v1beta/models/" + url.PathEscape(strings.TrimPrefix(str(c, "model"), "models/")) + action, nil
	}
	for _, suffix := range []string{"/chat/completions", "/responses", "/messages", "/models"} {
		ep = strings.TrimSuffix(ep, suffix)
	}
	u, _ = url.Parse(ep)
	if u.Path == "" || u.Path == "/" {
		ep += "/v1"
	} else if aiAnthropic(c) && !strings.HasSuffix(ep, "/v1") {
		ep += "/v1"
	}
	if models {
		return ep + "/models", nil
	}
	if aiAnthropic(c) {
		return ep + "/messages", nil
	}
	if str(c, "apiStyle") == "responses" {
		return ep + "/responses", nil
	}
	return ep + "/chat/completions", nil
}
func aiClient(c map[string]any) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.ResponseHeaderTimeout = 45 * time.Second
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: c["skipTlsVerify"] == true} // Explicit per-provider setting for private/self-signed endpoints.
	if c["proxyEnabled"] == true {
		u, err := url.Parse(str(c, "proxyUrl"))
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") {
			return nil, errors.New("Invalid AI proxy URL")
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Transport: tr, Timeout: 5 * time.Minute, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return errors.New("AI endpoint redirects are disabled to protect credentials")
	}}, nil
}
func aiRequest(ctx context.Context, c map[string]any, method, endpoint string, body any) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, errors.New("Invalid AI request")
	}
	req.Header.Set("Content-Type", "application/json")
	key := strings.TrimSpace(str(c, "apiKey"))
	if strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("Invalid AI credential")
	}
	if str(c, "provider") == "gemini" {
		req.Header.Set("x-goog-api-key", key)
	} else if aiAnthropic(c) {
		if str(c, "authMethod") == "bearer" {
			req.Header.Set("Authorization", "Bearer "+key)
		} else {
			req.Header.Set("x-api-key", key)
		}
		req.Header.Set("anthropic-version", "2023-06-01")
	} else if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for name, value := range appObject(c["customHeaders"]) {
		v, ok := value.(string)
		if !ok || name == "" || strings.ContainsAny(v, "\r\n") || strings.ContainsAny(name, " ()<>@,;:\\\"/[]?={}\t\r\n") {
			return nil, errors.New("Invalid custom AI header")
		}
		switch strings.ToLower(name) {
		case "host", "content-length", "transfer-encoding", "connection":
			return nil, errors.New("Unsupported transport header")
		}
		req.Header.Set(name, v)
	}
	return req, nil
}
func aiImageParts(message map[string]any, style string) []any {
	parts := []any{}
	if text := str(message, "content"); text != "" {
		if style == "anthropic" {
			parts = append(parts, map[string]any{"type": "text", "text": text})
		} else if style == "gemini" {
			parts = append(parts, map[string]any{"text": text})
		} else {
			kind := "text"
			if style == "responses" {
				kind = "input_text"
			}
			parts = append(parts, map[string]any{"type": kind, "text": text})
		}
	}
	for _, img := range appItems(message["images"]) {
		media, data := str(img, "mediaType"), str(img, "data")
		if media == "" || data == "" {
			continue
		}
		switch style {
		case "anthropic":
			parts = append(parts, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": media, "data": data}})
		case "gemini":
			parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": media, "data": data}})
		default:
			u := "data:" + media + ";base64," + data
			if style == "responses" {
				parts = append(parts, map[string]any{"type": "input_image", "image_url": u})
			} else {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": u}})
			}
		}
	}
	return parts
}
func aiBody(request map[string]any, stream bool) (map[string]any, error) {
	c := appObject(request["config"])
	if err := aiValidConfig(c, false); err != nil {
		return nil, err
	}
	if str(c, "model") == "" {
		return nil, errors.New("Model is required")
	}
	raw, ok := request["messages"].([]any)
	messages := appItems(request["messages"])
	if !ok {
		if _, typed := request["messages"].([]map[string]any); !typed {
			return nil, errors.New("messages must be an array")
		}
	} else if len(messages) != len(raw) {
		return nil, errors.New("Each message must be an object")
	}
	budget := integer(request["maxTokens"], integer(c["maxOutputTokens"], 2048))
	if budget < 1 || budget > 1000000 {
		return nil, errors.New("Invalid output token limit")
	}
	body := map[string]any{"model": str(c, "model"), "stream": stream}
	system := str(request, "systemPrompt")
	switch {
	case str(c, "provider") == "gemini":
		delete(body, "model")
		delete(body, "stream")
		items := []any{}
		for _, m := range messages {
			role := str(m, "role")
			if role == "assistant" {
				role = "model"
			} else {
				role = "user"
			}
			parts := aiImageParts(m, "gemini")
			if str(m, "role") == "tool" {
				var response any
				if json.Unmarshal([]byte(str(m, "content")), &response) != nil {
					response = map[string]any{"result": str(m, "content")}
				}
				parts = []any{map[string]any{"functionResponse": map[string]any{"name": str(m, "toolName"), "response": response}}}
			}
			for _, call := range appItems(m["toolCalls"]) {
				if payload, ok := call["providerPayload"].(map[string]any); ok {
					parts = append(parts, payload)
				} else {
					parts = append(parts, map[string]any{"functionCall": map[string]any{"name": call["name"], "args": call["arguments"]}})
				}
			}
			items = append(items, map[string]any{"role": role, "parts": parts})
		}
		body["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": system}}}
		body["contents"] = items
		body["generationConfig"] = map[string]any{"maxOutputTokens": budget}
	case aiAnthropic(c):
		body["system"] = system
		body["max_tokens"] = budget
		items := []any{}
		for _, m := range messages {
			role := str(m, "role")
			parts := aiImageParts(m, "anthropic")
			if role == "tool" {
				role = "user"
				parts = []any{map[string]any{"type": "tool_result", "tool_use_id": str(m, "toolCallId"), "content": str(m, "content")}}
			}
			for _, tc := range appItems(m["toolCalls"]) {
				parts = append(parts, map[string]any{"type": "tool_use", "id": tc["id"], "name": tc["name"], "input": tc["arguments"]})
			}
			items = append(items, map[string]any{"role": role, "content": parts})
		}
		body["messages"] = items
	case str(c, "apiStyle") == "responses":
		body["instructions"] = system
		body["max_output_tokens"] = budget
		items := []any{}
		for _, m := range messages {
			role := str(m, "role")
			if role == "tool" {
				items = append(items, map[string]any{"type": "function_call_output", "call_id": str(m, "toolCallId"), "output": str(m, "content")})
				continue
			}
			parts := aiImageParts(m, "responses")
			if role == "assistant" {
				parts = []any{map[string]any{"type": "output_text", "text": str(m, "content")}}
			}
			if str(m, "content") != "" || len(appItems(m["images"])) > 0 {
				items = append(items, map[string]any{"role": role, "content": parts})
			}
			for _, tc := range appItems(m["toolCalls"]) {
				args, _ := json.Marshal(tc["arguments"])
				items = append(items, map[string]any{"type": "function_call", "call_id": tc["id"], "name": tc["name"], "arguments": string(args)})
			}
		}
		body["input"] = items
		if key := str(request, "promptCacheKey"); key != "" && str(c, "provider") == "openai" {
			body["prompt_cache_key"] = key
		}
	default:
		body["max_tokens"] = budget
		items := []any{map[string]any{"role": "system", "content": system}}
		for _, m := range messages {
			item := map[string]any{"role": str(m, "role"), "content": str(m, "content")}
			if len(appItems(m["images"])) > 0 {
				item["content"] = aiImageParts(m, "chat")
			}
			if str(m, "role") == "tool" {
				item["tool_call_id"] = str(m, "toolCallId")
			}
			if calls := appItems(m["toolCalls"]); len(calls) > 0 {
				out := []any{}
				for _, tc := range calls {
					args, _ := json.Marshal(tc["arguments"])
					out = append(out, map[string]any{"id": tc["id"], "type": "function", "function": map[string]any{"name": tc["name"], "arguments": string(args)}})
				}
				item["tool_calls"] = out
			}
			items = append(items, item)
		}
		body["messages"] = items
	}
	aiDecorateEffort(body, c)
	return body, nil
}
func aiSafeError(c map[string]any, message string) string {
	values := []string{str(c, "apiKey"), str(c, "proxyUrl")}
	for _, v := range appObject(c["customHeaders"]) {
		if value, ok := v.(string); ok {
			values = append(values, value)
		}
	}
	walkSecrets(c, func(_ map[string]any, _ string, v string) { values = append(values, v) })
	for _, v := range values {
		if v != "" {
			for _, variant := range []string{v, url.QueryEscape(v), url.PathEscape(v)} {
				message = strings.ReplaceAll(message, variant, "[REDACTED]")
			}
		}
	}
	return message
}
func aiHTTPError(c map[string]any, res *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8193))
	detail := ""
	var data map[string]any
	if json.Unmarshal(b, &data) == nil {
		e := data["error"]
		if m, ok := e.(map[string]any); ok {
			detail = str(m, "message")
		} else if v, ok := e.(string); ok {
			detail = v
		}
	}
	if detail == "" {
		detail = http.StatusText(res.StatusCode)
	}
	return errors.New(aiSafeError(c, fmt.Sprintf("AI provider returned HTTP %d: %s", res.StatusCode, detail)))
}
func (s *Server) aiSend(ctx context.Context, c map[string]any, method, endpoint string, body any) (*http.Response, error) {
	client, err := aiClient(c)
	if err != nil {
		return nil, err
	}
	retries := integer(s.snapshot().Settings["max-retries"], 2)
	if retries < 0 {
		retries = 0
	}
	if retries > 5 {
		retries = 5
	}
	for attempt := 0; ; attempt++ {
		req, err := aiRequest(ctx, c, method, endpoint, body)
		if err != nil {
			return nil, err
		}
		res, err := client.Do(req)
		if err == nil && res.StatusCode >= 200 && res.StatusCode < 300 {
			return res, nil
		}
		retry := err == nil && (res.StatusCode == 429 || res.StatusCode >= 500)
		if err != nil {
			err = errors.New(aiSafeError(c, "AI request failed: "+err.Error()))
		} else {
			err = aiHTTPError(c, res)
			res.Body.Close()
		}
		if !retry || attempt >= retries {
			return nil, err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
func aiReadJSON(res *http.Response) (map[string]any, error) {
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxAIResponseBytes+1))
	if err != nil {
		return nil, errors.New("Cannot read AI response")
	}
	if len(b) > maxAIResponseBytes {
		return nil, errors.New("AI response exceeds size limit")
	}
	var data map[string]any
	if json.Unmarshal(b, &data) != nil {
		return nil, errors.New("Invalid AI provider JSON response")
	}
	return data, nil
}
func aiText(data map[string]any, c map[string]any) (text, reasoning string) {
	if str(c, "provider") == "gemini" {
		for _, candidate := range appItems(data["candidates"]) {
			for _, part := range appItems(appObject(candidate["content"])["parts"]) {
				if part["thought"] == true {
					reasoning += str(part, "text")
				} else {
					text += str(part, "text")
				}
			}
		}
		return
	}
	if aiAnthropic(c) {
		for _, part := range appItems(data["content"]) {
			switch str(part, "type") {
			case "text":
				text += str(part, "text")
			case "thinking":
				reasoning += str(part, "thinking")
			}
		}
		return
	}
	if str(c, "apiStyle") == "responses" {
		text = str(data, "output_text")
		if text == "" {
			for _, item := range appItems(data["output"]) {
				for _, part := range appItems(item["content"]) {
					if str(part, "type") == "output_text" {
						text += str(part, "text")
					}
				}
			}
		}
		return
	}
	if choices := appItems(data["choices"]); len(choices) > 0 {
		m := appObject(choices[0]["message"])
		text = str(m, "content")
		reasoning = str(m, "reasoning_content")
	}
	return
}
func (s *Server) aiComplete(ctx context.Context, request map[string]any) (string, error) {
	c := appObject(request["config"])
	body, err := aiBody(request, false)
	if err != nil {
		return "", err
	}
	endpoint, err := aiEndpoint(c, false, false)
	if err != nil {
		return "", err
	}
	res, err := s.aiSend(ctx, c, "POST", endpoint, body)
	if err != nil {
		return "", err
	}
	data, err := aiReadJSON(res)
	if err != nil {
		return "", err
	}
	text, _ := aiText(data, c)
	if text == "" {
		return "", errors.New("AI provider returned no text")
	}
	return text, nil
}
func aiStreamDelta(data map[string]any, c map[string]any) (text, reasoning string, done bool, err error) {
	if e := appObject(data["error"]); len(e) > 0 {
		return "", "", false, errors.New(aiSafeError(c, str(e, "message")))
	}
	if str(c, "provider") == "gemini" {
		text, reasoning = aiText(data, c)
		return
	}
	if aiAnthropic(c) {
		switch str(data, "type") {
		case "content_block_delta":
			delta := appObject(data["delta"])
			text = str(delta, "text")
			reasoning = str(delta, "thinking")
		case "message_stop":
			done = true
		case "error":
			err = errors.New(aiSafeError(c, str(appObject(data["error"]), "message")))
		}
		return
	}
	if str(c, "apiStyle") == "responses" {
		switch str(data, "type") {
		case "response.output_text.delta":
			text = str(data, "delta")
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			reasoning = str(data, "delta")
		case "response.completed":
			done = true
		case "response.failed", "response.incomplete":
			err = errors.New("AI response did not complete")
		}
		return
	}
	if choices := appItems(data["choices"]); len(choices) > 0 {
		delta := appObject(choices[0]["delta"])
		text = str(delta, "content")
		reasoning = str(delta, "reasoning_content")
		if reasoning == "" {
			reasoning = str(delta, "reasoning")
		}
		done = str(choices[0], "finish_reason") != ""
	}
	return
}
func (s *Server) aiStream(ctx context.Context, request map[string]any, emit func(string, string) error) error {
	c := appObject(request["config"])
	body, err := aiBody(request, true)
	if err != nil {
		return err
	}
	endpoint, err := aiEndpoint(c, false, true)
	if err != nil {
		return err
	}
	res, err := s.aiSend(ctx, c, "POST", endpoint, body)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if strings.Contains(res.Header.Get("Content-Type"), "application/json") {
		data, err := aiReadJSON(res)
		if err != nil {
			return err
		}
		text, reason := aiText(data, c)
		if text == "" && reason == "" {
			return errors.New("AI provider returned no text")
		}
		return emit(text, reason)
	}
	scanner := bufio.NewScanner(io.LimitReader(res.Body, maxAIResponseBytes+1))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var event []string
	got := false
	completed := false
	consume := func() error {
		if len(event) == 0 {
			return nil
		}
		raw := strings.Join(event, "\n")
		event = nil
		if raw == "[DONE]" {
			completed = true
			return nil
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			return errors.New("Invalid AI stream event")
		}
		text, reason, done, err := aiStreamDelta(data, c)
		if err != nil {
			return err
		}
		completed = completed || done
		if text != "" || reason != "" {
			got = true
			return emit(text, reason)
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err = consume(); err != nil {
				return err
			}
			if completed {
				break
			}
		} else if strings.HasPrefix(line, "data:") {
			event = append(event, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err = consume(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if scanner.Err() != nil {
		return errors.New("AI stream read failed")
	}
	if !got {
		return errors.New("AI stream returned no text")
	}
	if !completed && str(c, "provider") != "gemini" {
		return errors.New("AI stream ended before completion")
	}
	return nil
}
func aiSSE(w http.ResponseWriter, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}
func (s *Server) handleAI(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	if s.handleAIJobs(w, r, p) {
		return true
	}
	if s.handleAIStorage(w, r, p) {
		return true
	}
	route := strings.TrimSuffix(r.URL.Path, "/")
	if !strings.HasPrefix(route, "/api/ai/") {
		return false
	}
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return true
	}
	var result any
	var err error
	switch route {
	case "/api/ai/complete":
		result, err = s.aiComplete(r.Context(), appObject(p["request"]))
	case "/api/ai/cancel-stream":
		result = s.aiRuntime.cancel(aiSessionID(p))
	case "/api/ai/stream":
		id := aiSessionID(p)
		ctx, finish, e := s.aiRuntime.register(r.Context(), id)
		if e != nil {
			writeError(w, 400, e.Error())
			return true
		}
		defer finish()
		request := appObject(p["request"])
		if _, e := aiBody(request, true); e != nil {
			writeError(w, 400, e.Error())
			return true
		}
		if _, e := aiEndpoint(appObject(request["config"]), false, true); e != nil {
			writeError(w, 400, e.Error())
			return true
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(200)
		err = s.aiStream(ctx, request, func(delta, reason string) error {
			v := map[string]any{"session_id": id, "delta": delta, "done": false}
			if reason != "" {
				v["reasoning_delta"] = reason
			}
			return aiSSE(w, v)
		})
		terminal := map[string]any{"session_id": id, "delta": "", "done": true}
		if err != nil {
			terminal["error"] = aiSafeError(appObject(request["config"]), err.Error())
		}
		_ = aiSSE(w, terminal)
		return true
	case "/api/ai/models":
		result, err = s.aiModels(r.Context(), appObject(p["config"]))
	case "/api/ai/model-effort":
		result = aiEffortCapability(appObject(p["config"]), str(p, "modelId"))
	case "/api/ai/test-connection":
		c := appObject(p["config"])
		started := time.Now()
		text, e := s.aiComplete(r.Context(), map[string]any{"config": c, "messages": []any{map[string]any{"role": "user", "content": "Reply with OK."}}, "systemPrompt": "", "maxTokens": 16})
		result = map[string]any{"success": e == nil && text != "", "modelUsed": str(c, "model"), "latencyMs": time.Since(started).Milliseconds(), "message": "Connection succeeded"}
		if e != nil {
			result.(map[string]any)["message"] = aiSafeError(c, e.Error())
			result.(map[string]any)["errorCategory"] = "provider"
		}
	case "/api/ai/agent-stream":
		return s.handleAIAgent(w, r, p)
	default:
		return false
	}
	if err != nil {
		writeError(w, 400, aiSafeError(appObject(p["config"]), err.Error()))
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
