package server

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

func appObject(value any) map[string]any {
	if v, ok := value.(map[string]any); ok {
		return v
	}
	return map[string]any{}
}
func appItems(value any) []map[string]any {
	out := []map[string]any{}
	switch v := value.(type) {
	case []any:
		for _, raw := range v {
			if m, ok := raw.(map[string]any); ok {
				out = append(out, m)
			}
		}
	case []map[string]any:
		out = v
	}
	return out
}
func appList(st *diskState, key string) []map[string]any { return appItems(st.Settings[key]) }
func (s *Server) setAppValue(key string, value any) error {
	return s.update(func(st *diskState) error { st.Settings[key] = value; return nil })
}
func validAppID(id string) bool {
	return id != "" && len(id) <= 256 && !strings.ContainsAny(id, "/\\\x00")
}
func aiValidConfig(config map[string]any, item bool) error {
	provider := str(config, "provider")
	switch provider {
	case "claude", "openai", "gemini", "deepseek", "kimi", "qwen", "zhipu", "minimax", "ollama", "anthropic-compatible", "openai-compatible", "custom", "claude-code-cli", "pi-agent-cli", "codex-cli", "opencode-cli", "cursor-cli", "grok-cli", "codebuddy-cli", "qoder-cli":
	default:
		return errors.New("Invalid AI provider")
	}
	if item && (!validAppID(str(config, "id")) || strings.TrimSpace(str(config, "name")) == "") {
		return errors.New("AI configuration requires an id and name")
	}
	if n := integer(config["maxOutputTokens"], 0); n < 0 || n > 1000000 {
		return errors.New("Invalid output token limit")
	}
	return nil
}
func normalizeAIConfigs(items []map[string]any) error {
	seen := map[string]bool{}
	foundDefault := false
	for _, config := range items {
		if err := aiValidConfig(config, true); err != nil {
			return err
		}
		id := str(config, "id")
		if seen[id] {
			return errors.New("Duplicate AI configuration id")
		}
		seen[id] = true
		if b, _ := config["isDefault"].(bool); b {
			if foundDefault {
				return errors.New("Only one AI configuration can be the default")
			}
			foundDefault = true
		}
	}
	return nil
}
func (s *Server) handleAIStorage(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	route := strings.TrimSuffix(r.URL.Path, "/")
	method := r.Method
	var result any
	var err error
	switch {
	case route == "/api/ai/config" || route == "/api/ai/chat-selection":
		key, field := "ai.config", "config"
		if strings.HasSuffix(route, "chat-selection") {
			key, field = "ai.chatSelection", "selection"
		}
		if method == "GET" {
			result = s.snapshot().Settings[key]
		} else if method == "POST" {
			v, ok := p[field].(map[string]any)
			if !ok {
				err = errors.New(field + " must be an object")
			} else if field == "config" {
				if err = aiValidConfig(v, false); err == nil {
					err = s.setAppValue(key, v)
				}
			} else {
				err = s.setAppValue(key, v)
			}
		} else {
			writeError(w, 405, "Method not allowed")
			return true
		}
	case route == "/api/ai/provider-configs" && method == "GET":
		result = appObject(s.snapshot().Settings["ai.providerConfigs"])
	case route == "/api/ai/provider-config" && method == "POST":
		config, ok := p["config"].(map[string]any)
		if !ok {
			err = errors.New("config must be an object")
			break
		}
		config = cloneMap(config)
		config["provider"] = str(p, "provider")
		if err = aiValidConfig(config, false); err == nil {
			err = s.update(func(st *diskState) error {
				configs := appObject(st.Settings["ai.providerConfigs"])
				configs[str(p, "provider")] = config
				st.Settings["ai.providerConfigs"] = configs
				return nil
			})
		}
	case route == "/api/ai/configs" && method == "GET":
		result = appItems(s.snapshot().Settings["ai.configs"])
	case route == "/api/ai/configs" && method == "POST":
		raw, ok := p["configs"].([]any)
		if !ok {
			err = errors.New("configs must be an array")
			break
		}
		items := appItems(raw)
		if len(items) != len(raw) {
			err = errors.New("Each AI configuration must be an object")
			break
		}
		if err = normalizeAIConfigs(items); err == nil {
			err = s.setAppValue("ai.configs", items)
		}
	case route == "/api/ai/config-item" && method == "POST":
		config, ok := p["config"].(map[string]any)
		if !ok {
			err = errors.New("config must be an object")
			break
		}
		if err = aiValidConfig(config, true); err == nil {
			err = s.update(func(st *diskState) error {
				items := appList(st, "ai.configs")
				if b, _ := config["isDefault"].(bool); b {
					for _, item := range items {
						item["isDefault"] = false
					}
				}
				st.Settings["ai.configs"] = upsert(items, config)
				return nil
			})
		}
	case route == "/api/ai/default-config" && method == "POST":
		err = s.update(func(st *diskState) error {
			items := appList(st, "ai.configs")
			found := false
			for _, item := range items {
				active := str(item, "id") == str(p, "configId")
				item["isDefault"] = active
				found = found || active
			}
			if !found {
				return errors.New("AI configuration does not exist")
			}
			st.Settings["ai.configs"] = items
			return nil
		})
	case strings.HasPrefix(route, "/api/ai/config/") && method == "DELETE":
		id := strings.TrimPrefix(route, "/api/ai/config/")
		err = s.update(func(st *diskState) error {
			st.Settings["ai.configs"] = removeID(appList(st, "ai.configs"), id)
			return nil
		})
	case route == "/api/ai/conversations" && method == "GET":
		result = appItems(s.snapshot().Settings["ai.conversations"])
		items := result.([]map[string]any)
		sort.SliceStable(items, func(i, j int) bool { return str(items[i], "updatedAt") > str(items[j], "updatedAt") })
	case route == "/api/ai/conversation" && method == "POST":
		item, ok := p["conversation"].(map[string]any)
		if !ok || !validAppID(str(item, "id")) {
			err = errors.New("Conversation requires an id")
			break
		}
		err = s.update(func(st *diskState) error {
			st.Settings["ai.conversations"] = upsert(appList(st, "ai.conversations"), item)
			return nil
		})
	case strings.HasPrefix(route, "/api/ai/conversation/") && method == "DELETE":
		id := strings.TrimPrefix(route, "/api/ai/conversation/")
		err = s.update(func(st *diskState) error {
			st.Settings["ai.conversations"] = removeID(appList(st, "ai.conversations"), id)
			runs := appList(st, "ai.runs")
			kept := []map[string]any{}
			for _, run := range runs {
				if str(run, "conversationId") != id {
					kept = append(kept, run)
				}
			}
			st.Settings["ai.runs"] = kept
			return nil
		})
	case route == "/api/ai/runs" && method == "GET":
		result = appItems(s.snapshot().Settings["ai.runs"])
	case (route == "/api/ai/run" || route == "/api/ai/run-state") && method == "POST":
		run, ok := p["run"].(map[string]any)
		if !ok || !validAppID(str(run, "runId")) || !validAppID(str(run, "conversationId")) {
			err = errors.New("AI run requires runId and conversationId")
			break
		}
		err = s.update(func(st *diskState) error {
			if route == "/api/ai/run-state" {
				conversation, ok := p["conversation"].(map[string]any)
				if !ok || str(conversation, "id") != str(run, "conversationId") {
					return errors.New("Run and conversation ids must match")
				}
				st.Settings["ai.conversations"] = upsert(appList(st, "ai.conversations"), conversation)
			}
			items := appList(st, "ai.runs")
			found := false
			for i, item := range items {
				if str(item, "runId") == str(run, "runId") {
					items[i] = run
					found = true
				}
			}
			if !found {
				items = append(items, run)
			}
			st.Settings["ai.runs"] = items
			return nil
		})
	case route == "/api/ai/plugin-tools/plugins" && method == "GET":
		result = s.snapshot().Settings["ai.pluginTools"]
		if result == nil {
			result = []string{}
		}
	case route == "/api/ai/plugin-tools/plugins" && method == "POST":
		err = errors.New("Plugin AI tools require the Go plugin runtime, which has not been migrated")
	case route == "/api/ai/tool-approval" && method == "POST":
		result = false
	default:
		return false
	}
	if err != nil {
		writeError(w, 400, err.Error())
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
func (s *Server) recoverAIRuns() error {
	if len(appItems(s.snapshot().Settings["ai.runs"])) == 0 {
		return nil
	}
	return s.update(func(st *diskState) error {
		items := appList(st, "ai.runs")
		changed := false
		for _, run := range items {
			switch str(run, "status") {
			case "preparing", "queued":
				if str(run, "fifoCategory") == "write_confirmation_resume" {
					run["status"] = "awaiting_write_confirmation"
				} else {
					run["status"] = "pending_recoverable"
				}
				changed = true
			case "running":
				run["status"] = "interrupted"
				changed = true
			}
			if changed {
				run["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
			}
		}
		st.Settings["ai.runs"] = items
		return nil
	})
}
