package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/sugaryiiy/dbx-go-wails/internal/database"
)

func advancedExplainDialect(kind string) string {
	switch kind {
	case "mysql", "mariadb", "doris", "starrocks", "oceanbase-mysql", "tidb":
		return "mysql"
	case "sqlite":
		return "sqlite"
	case "postgres", "questdb", "clickhouse":
		return "postgres"
	default:
		return ""
	}
}

func advancedExplainSQL(o map[string]any) map[string]any {
	failure := func(reason string) map[string]any { return map[string]any{"ok": false, "reason": reason} }
	kind := str(o, "databaseType")
	if kind == "" {
		return failure("unsupported")
	}
	source := strings.TrimSpace(str(o, "sql"))
	if source == "" {
		return failure("empty")
	}
	analyze := boolValue(o, "analyze")
	prefix := ""
	switch kind {
	case "postgres":
		prefix = "EXPLAIN (FORMAT JSON) "
		if analyze {
			prefix = "EXPLAIN (ANALYZE, FORMAT JSON) "
		}
	case "mysql", "mariadb":
		if analyze {
			return failure("unsupported")
		}
		prefix = "EXPLAIN FORMAT=JSON "
		if str(o, "format") == "standard" {
			prefix = "EXPLAIN FORMAT=TRADITIONAL "
		}
	case "sqlite":
		if analyze {
			return failure("unsupported")
		}
		prefix = "EXPLAIN QUERY PLAN "
	case "clickhouse", "doris", "questdb", "dameng":
		if analyze {
			return failure("unsupported")
		}
		prefix = "EXPLAIN "
	case "oracle":
		if analyze {
			return failure("unsupported")
		}
		prefix = "EXPLAIN PLAN FOR "
	case "oceanbase-oracle":
		if analyze {
			return failure("unsupported")
		}
		prefix = "EXPLAIN FORMAT=JSON "
	case "sqlserver":
	default:
		return failure("unsupported")
	}
	dialect := advancedExplainDialect(kind)
	sql, err := cleanSQL(source, dialect)
	if err != nil {
		return failure("unsafe")
	}
	tokens := schemaLex(sql)
	if len(tokens) == 0 {
		return failure("empty")
	}
	verb := strings.ToUpper(tokens[0].identifier)
	switch verb {
	case "SELECT", "WITH", "VALUES", "INSERT", "UPDATE", "DELETE":
	default:
		return failure("unsafe")
	}
	// ANALYZE executes its source. Use the existing conservative read-only gate
	// so writable CTEs, SELECT INTO and unknown functions cannot be run as plans.
	readOnly := analyze || verb == "SELECT" || verb == "WITH" || verb == "VALUES"
	if _, err = database.ValidateMCPSQL(sql, dialect, readOnly, !readOnly); err != nil {
		return failure("unsafe")
	}
	if kind == "sqlserver" {
		control := "SHOWPLAN_XML"
		if analyze {
			control = "STATISTICS XML"
		}
		return map[string]any{"ok": true, "sql": "SET " + control + " ON;\nGO\n" + sql + "\nGO\nSET " + control + " OFF;"}
	}
	return map[string]any{"ok": true, "sql": prefix + sql}
}

func (s *Server) handleAdvancedExplain(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	if r.URL.Path != "/api/query/build-explain-sql" && r.URL.Path != "/api/query/get-explain-info" {
		return false
	}
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return true
	}
	if strings.Contains(r.URL.Path, "build-explain") {
		writeJSON(w, 200, advancedExplainSQL(mapValue(p, "options")))
		return true
	}
	cfg, err := s.compareConnection(str(p, "connectionId"))
	if err != nil {
		writeError(w, 400, err.Error())
		return true
	}
	if cfg.Type != "sqlite" && cfg.Type != "mysql" && cfg.Type != "postgres" {
		result, err := s.database.Handle(r.Context(), r.URL.Path, p)
		if err != nil {
			writeError(w, 400, err.Error())
		} else {
			writeJSON(w, 200, result)
		}
		return true
	}
	mode := str(p, "mode")
	if mode != "" && mode != "explain" && mode != "analyze" {
		writeError(w, 400, "Unknown EXPLAIN mode")
		return true
	}
	built := advancedExplainSQL(map[string]any{"databaseType": cfg.Type, "sql": p["sql"], "analyze": mode == "analyze"})
	if !boolValue(built, "ok") {
		writeError(w, 400, "Cannot explain this statement: "+str(built, "reason"))
		return true
	}
	result, err := s.database.ExecuteSQL(r.Context(), cfg.ID, str(p, "database"), str(built, "sql"), 10000)
	if err == nil && result.Truncated {
		err = errors.New("Execution plan exceeds the bounded response size")
	}
	if err != nil {
		writeError(w, 400, err.Error())
		return true
	}
	if len(result.Rows) == 1 && len(result.Rows[0]) == 1 {
		if text, ok := result.Rows[0][0].(string); ok {
			writeJSON(w, 200, text)
			return true
		}
		data, err := json.Marshal(result.Rows[0][0])
		if err == nil {
			writeJSON(w, 200, string(data))
			return true
		}
	}
	rows := make([]map[string]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		item := map[string]any{}
		for i, name := range result.Columns {
			if i < len(row) {
				item[name] = row[i]
			}
		}
		rows = append(rows, item)
	}
	data, err := json.Marshal(rows)
	if err != nil {
		writeError(w, 400, fmt.Sprintf("Cannot encode execution plan: %v", err))
	} else {
		writeJSON(w, 200, string(data))
	}
	return true
}
