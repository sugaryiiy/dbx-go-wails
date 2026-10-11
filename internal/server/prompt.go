package server

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

func (s *Server) handlePrompts(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	route := strings.TrimSuffix(r.URL.Path, "/")
	if !strings.HasPrefix(route, "/api/prompt-templates") {
		return false
	}
	var result any
	var err error
	switch {
	case route == "/api/prompt-templates/global-instructions" && r.Method == "GET":
		result = map[string]any{"content": s.snapshot().Settings["ai.globalInstructions"]}
		if result.(map[string]any)["content"] == nil {
			result.(map[string]any)["content"] = ""
		}
	case route == "/api/prompt-templates/global-instructions" && r.Method == "PUT":
		content, ok := p["content"].(string)
		if !ok || utf8.RuneCountInString(content) > 8000 {
			err = errors.New("Global instructions must be at most 8000 characters")
		} else {
			err = s.setAppValue("ai.globalInstructions", content)
		}
	case route == "/api/prompt-templates" && r.Method == "GET":
		result = appItems(s.snapshot().Settings["ai.promptTemplates"])
	case route == "/api/prompt-templates" && r.Method == "POST":
		id, name, content := str(p, "id"), strings.TrimSpace(str(p, "name")), str(p, "content")
		if !validAppID(id) || name == "" || utf8.RuneCountInString(name) > 50 || utf8.RuneCountInString(content) > 8000 {
			err = errors.New("Template requires an id, a name of 1–50 characters and content of at most 8000 characters")
			break
		}
		err = s.update(func(st *diskState) error {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			item := map[string]any{"id": id, "name": name, "content": content, "createdAt": now, "updatedAt": now}
			items := appList(st, "ai.promptTemplates")
			for _, existing := range items {
				if str(existing, "id") == id {
					item["createdAt"] = existing["createdAt"]
				}
			}
			st.Settings["ai.promptTemplates"] = upsert(items, item)
			result = item
			return nil
		})
	case strings.HasPrefix(route, "/api/prompt-templates/") && r.Method == "DELETE":
		id := strings.TrimPrefix(route, "/api/prompt-templates/")
		err = s.update(func(st *diskState) error {
			st.Settings["ai.promptTemplates"] = removeID(appList(st, "ai.promptTemplates"), id)
			return nil
		})
	default:
		writeError(w, 405, "Method not allowed")
		return true
	}
	if err != nil {
		writeError(w, 400, err.Error())
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
