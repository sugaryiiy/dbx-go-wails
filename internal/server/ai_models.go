package server

import (
	"context"
	"errors"
	"strings"
)

func (s *Server) aiModels(ctx context.Context, c map[string]any) (any, error) {
	if err := aiValidConfig(c, false); err != nil {
		return nil, err
	}
	endpoint, err := aiEndpoint(c, true, false)
	if err != nil {
		return nil, err
	}
	res, err := s.aiSend(ctx, c, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	data, err := aiReadJSON(res)
	if err != nil {
		return nil, err
	}
	key := "data"
	if str(c, "provider") == "gemini" {
		key = "models"
	}
	raw, ok := data[key].([]any)
	if !ok {
		return nil, errors.New("AI provider returned an invalid model list")
	}
	items := []map[string]any{}
	for _, m := range appItems(raw) {
		id := str(m, "id")
		name := str(m, "display_name")
		if str(c, "provider") == "gemini" {
			id = strings.TrimPrefix(str(m, "name"), "models/")
			name = str(m, "displayName")
			supported := false
			if methods, ok := m["supportedGenerationMethods"].([]any); ok {
				for _, method := range methods {
					if method == "generateContent" {
						supported = true
					}
				}
			} else {
				supported = true
			}
			if !supported {
				continue
			}
		}
		if id == "" {
			continue
		}
		out := map[string]any{"id": id}
		if name != "" {
			out["displayName"] = name
		}
		cap := aiEffortCapability(c, id)
		if str(cap, "kind") != "unsupported" {
			out["effortCapability"] = cap
		}
		items = append(items, out)
	}
	return items, nil
}
func aiEnumCapability(values []string, source string) map[string]any {
	options := []any{}
	for _, value := range values {
		options = append(options, map[string]any{"id": value, "label": strings.ToUpper(value[:1]) + value[1:], "selection": map[string]any{"kind": "enum", "value": value}})
	}
	def := any(map[string]any{"kind": "providerDefault"})
	if len(options) > 0 {
		def = options[0].(map[string]any)["selection"]
	}
	return map[string]any{"kind": "enum", "options": options, "default": def, "source": source}
}
func aiEffortCapability(c map[string]any, model string) map[string]any {
	for _, configured := range appItems(c["models"]) {
		if str(configured, "name") == model {
			if raw, ok := configured["supportedEffortLevels"].([]any); ok && len(raw) > 0 {
				levels := []string{}
				for _, v := range raw {
					if text, ok := v.(string); ok && text != "" {
						levels = append(levels, text)
					}
				}
				return aiEnumCapability(levels, "custom")
			}
		}
	}
	m := strings.ToLower(strings.TrimPrefix(model, "models/"))
	source := "officialRegistry"
	switch str(c, "provider") {
	case "custom", "openai-compatible", "anthropic-compatible":
		return map[string]any{"kind": "freeText", "source": "custom"}
	case "openai":
		if strings.HasPrefix(m, "gpt-5-pro") {
			return aiEnumCapability([]string{"high"}, source)
		}
		if strings.HasPrefix(m, "gpt-5.6") {
			return aiEnumCapability([]string{"none", "low", "medium", "high", "xhigh", "max"}, source)
		}
		if strings.HasPrefix(m, "gpt-5.1") {
			return aiEnumCapability([]string{"none", "low", "medium", "high"}, source)
		}
		if strings.HasPrefix(m, "gpt-5") {
			return aiEnumCapability([]string{"minimal", "low", "medium", "high", "xhigh"}, source)
		}
		if strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4-mini") {
			return aiEnumCapability([]string{"low", "medium", "high"}, source)
		}
	case "gemini":
		if strings.HasPrefix(m, "gemini-2.5") {
			min, max := 0, 24576
			if strings.Contains(m, "-pro") {
				min, max = 128, 32768
			} else if strings.Contains(m, "flash-lite") {
				min = 512
			}
			return map[string]any{"kind": "integer", "min": min, "max": max, "step": 1, "default": map[string]any{"kind": "integer", "value": -1}, "source": source}
		}
		if strings.HasPrefix(m, "gemini-3") {
			return aiEnumCapability([]string{"minimal", "low", "medium", "high"}, source)
		}
	case "deepseek":
		if strings.HasPrefix(m, "deepseek-v4") {
			return aiEnumCapability([]string{"high", "max"}, source)
		}
	case "kimi":
		if m == "k3" || strings.HasPrefix(m, "kimi-k3") {
			return aiEnumCapability([]string{"low", "high", "max"}, source)
		}
		if strings.HasPrefix(m, "kimi-k2.6") {
			return map[string]any{"kind": "boolean", "default": map[string]any{"kind": "providerDefault"}, "source": source}
		}
	case "qwen", "zhipu", "ollama":
		if strings.HasPrefix(m, "qwen3") || strings.HasPrefix(m, "glm-") || strings.HasPrefix(m, "deepseek") {
			return map[string]any{"kind": "boolean", "default": map[string]any{"kind": "providerDefault"}, "source": source}
		}
	case "minimax":
		if strings.HasPrefix(m, "minimax-m3") {
			return map[string]any{"kind": "boolean", "default": map[string]any{"kind": "providerDefault"}, "source": source}
		}
	}
	return map[string]any{"kind": "unsupported"}
}
func aiDecorateEffort(body, c map[string]any) {
	selection := appObject(c["runtimeEffort"])
	kind, value := str(selection, "kind"), selection["value"]
	if kind == "" || kind == "providerDefault" {
		if level := str(c, "reasoningLevel"); level != "" && level != "default" {
			kind, value = "enum", level
		} else if c["enableThinking"] == true {
			kind, value = "boolean", true
		} else {
			return
		}
	}
	if str(c, "provider") == "gemini" {
		generation := appObject(body["generationConfig"])
		think := map[string]any{}
		if kind == "integer" {
			think["thinkingBudget"] = value
		} else if kind == "disabled" || kind == "boolean" && value == false {
			think["thinkingBudget"] = 0
		} else if kind == "enum" || kind == "text" {
			think["thinkingLevel"] = value
		} else {
			return
		}
		generation["thinkingConfig"] = think
		body["generationConfig"] = generation
		return
	}
	if aiAnthropic(c) {
		if kind == "disabled" || kind == "boolean" && value == false {
			body["thinking"] = map[string]any{"type": "disabled"}
		} else if kind == "integer" {
			body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": value}
		} else {
			body["thinking"] = map[string]any{"type": "adaptive"}
			if kind == "enum" || kind == "text" {
				body["output_config"] = map[string]any{"effort": value}
			}
		}
		return
	}
	switch str(c, "provider") {
	case "deepseek", "kimi":
		enabled := kind != "disabled" && !(kind == "boolean" && value == false)
		mode := "disabled"
		if enabled {
			mode = "enabled"
		}
		body["thinking"] = map[string]any{"type": mode}
		if kind == "enum" || kind == "text" {
			body["reasoning_effort"] = value
		}
	case "qwen":
		body["enable_thinking"] = kind != "disabled" && !(kind == "boolean" && value == false)
	case "zhipu":
		mode := "enabled"
		if kind == "disabled" || kind == "boolean" && value == false {
			mode = "disabled"
		}
		body["thinking"] = map[string]any{"type": mode}
	case "minimax":
		body["reasoning_split"] = true
	default:
		effort := value
		if kind == "disabled" {
			effort = "none"
		}
		if kind == "boolean" {
			if value == true {
				effort = "medium"
			} else {
				effort = "none"
			}
		}
		if str(c, "apiStyle") == "responses" {
			body["reasoning"] = map[string]any{"effort": effort}
		} else {
			body["reasoning_effort"] = effort
		}
	}
}
