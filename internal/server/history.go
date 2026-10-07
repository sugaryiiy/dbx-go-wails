package server

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

func integer(value any, fallback int) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return fallback
}
func bound(value any, fallback, maximum int) int {
	n := integer(value, fallback)
	if n < 0 {
		return 0
	}
	if n > maximum {
		return maximum
	}
	return n
}
func source(entry map[string]any) string {
	v := str(entry, "source")
	if v == "" {
		return "sql"
	}
	return v
}
func activity(entry map[string]any) string {
	v := str(entry, "activity_kind")
	if v == "" {
		return "query"
	}
	return v
}
func connectionMatches(entry, filter map[string]any) bool {
	if str(entry, "connection_id") != "" && str(filter, "connection_id") != "" {
		return str(entry, "connection_id") == str(filter, "connection_id")
	}
	return str(entry, "connection_name") == str(filter, "connection_name")
}
func historyMatches(entry, filter map[string]any) bool {
	if search := strings.ToLower(str(filter, "search_text")); search != "" {
		found := false
		for _, key := range []string{"sql", "connection_name", "database", "error", "operation", "target", "mcp_tool_name"} {
			if strings.Contains(strings.ToLower(str(entry, key)), search) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if v := str(filter, "activity_kind"); v != "" && activity(entry) != v {
		return false
	}
	if v := str(filter, "source"); v != "" && source(entry) != v {
		return false
	}
	if v := str(filter, "mcp_tool_name"); v != "" && str(entry, "mcp_tool_name") != v {
		return false
	}
	if v, ok := filter["success"].(bool); ok && entry["success"] != v {
		return false
	}
	executed := str(entry, "executed_at")
	if v := str(filter, "started_at"); v != "" && executed < v {
		return false
	}
	if v := str(filter, "ended_at"); v != "" && executed > v {
		return false
	}
	for _, key := range []string{"connections", "databases"} {
		if filters, ok := filter[key].([]any); ok && len(filters) > 0 {
			matched := false
			for _, raw := range filters {
				f, ok := raw.(map[string]any)
				if ok && connectionMatches(entry, f) && (key != "databases" || str(entry, "database") == str(f, "database")) {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
	}
	return true
}
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	route := strings.TrimSuffix(r.URL.Path, "/")
	method := r.Method
	var result any
	var err error
	switch {
	case route == "/api/history/save" && method == "POST":
		entry, ok := p["entry"].(map[string]any)
		if !ok || str(entry, "id") == "" || str(entry, "executed_at") == "" {
			writeError(w, 400, "History entry requires id and executed_at")
			return true
		}
		err = s.update(func(state *diskState) error {
			copy := cloneMap(entry)
			copy["source"] = source(copy)
			copy["activity_kind"] = activity(copy)
			state.History = upsert(state.History, copy)
			sortedHistory(state.History)
			limitKey := "history-retention-limit"
			if source(copy) == "mcp" {
				limitKey = "mcp-history-retention-limit"
			}
			limit := integer(state.Settings[limitKey], 1000)
			if limit > 0 {
				kept := make([]map[string]any, 0, len(state.History))
				count := 0
				for _, item := range state.History {
					if source(item) == source(copy) {
						count++
						if count > limit {
							continue
						}
					}
					kept = append(kept, item)
				}
				state.History = kept
			}
			return nil
		})
	case route == "/api/history" && method == "GET":
		entries := s.snapshot().History
		sortedHistory(entries)
		matching := []map[string]any{}
		for _, entry := range entries {
			if str(p, "activity_kind") == "" || activity(entry) == str(p, "activity_kind") {
				matching = append(matching, entry)
			}
		}
		offset := bound(p["offset"], 0, len(matching))
		limit := bound(p["limit"], 100, 10000)
		end := offset + limit
		if end > len(matching) {
			end = len(matching)
		}
		result = matching[offset:end]
	case route == "/api/history/search" && method == "POST":
		entries := s.snapshot().History
		sortedHistory(entries)
		matching := []map[string]any{}
		total := 0
		cursor, _ := p["cursor"].(map[string]any)
		for _, entry := range entries {
			if !historyMatches(entry, p) {
				continue
			}
			total++
			if cursor != nil {
				at, cutoff := str(entry, "executed_at"), str(cursor, "executed_at")
				if at > cutoff || (at == cutoff && str(entry, "id") >= str(cursor, "id")) {
					continue
				}
			}
			matching = append(matching, entry)
		}
		limit := bound(p["limit"], 100, 1000)
		if limit == 0 {
			limit = 100
		}
		var next any
		if len(matching) > limit {
			matching = matching[:limit]
			last := matching[len(matching)-1]
			next = map[string]any{"executed_at": last["executed_at"], "id": last["id"]}
		}
		result = map[string]any{"entries": matching, "next_cursor": next, "total": total}
	case route == "/api/history/options" && method == "GET":
		options := map[string]map[string]any{}
		databases := map[string]map[string]bool{}
		for _, entry := range s.snapshot().History {
			key := str(entry, "connection_id") + "\x00" + str(entry, "connection_name")
			if options[key] == nil {
				options[key] = map[string]any{"connection_id": str(entry, "connection_id"), "connection_name": str(entry, "connection_name")}
				databases[key] = map[string]bool{}
			}
			databases[key][str(entry, "database")] = true
		}
		list := []map[string]any{}
		for key, option := range options {
			names := []string{}
			for name := range databases[key] {
				names = append(names, name)
			}
			sort.Strings(names)
			option["databases"] = names
			list = append(list, option)
		}
		sort.Slice(list, func(i, j int) bool { return str(list[i], "connection_name") < str(list[j], "connection_name") })
		result = list
	case route == "/api/history" && method == "DELETE":
		err = s.update(func(state *diskState) error {
			kept := []map[string]any{}
			for _, entry := range state.History {
				if str(p, "source") != "" && source(entry) != str(p, "source") {
					kept = append(kept, entry)
				}
			}
			state.History = kept
			return nil
		})
	case strings.HasPrefix(route, "/api/history/") && method == "DELETE":
		id := strings.TrimPrefix(route, "/api/history/")
		if id == "" {
			err = errors.New("History id is required")
			break
		}
		err = s.update(func(state *diskState) error { state.History = removeID(state.History, id); return nil })
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
