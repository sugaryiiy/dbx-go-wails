package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sugaryiiy/dbx-go-wails/internal/database"
	"net/http"
	"strings"
	"time"
)

type aiToolSpec struct {
	name, desc string
	properties map[string]any
	required   []string
}

func aiAgentTools() []aiToolSpec {
	stringType := map[string]any{"type": "string"}
	scope := func() map[string]any { return map[string]any{"database": stringType, "schema": stringType} }
	table := scope()
	table["table"] = stringType
	sample := scope()
	sample["table"] = stringType
	sample["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 20}
	return []aiToolSpec{{"get_current_time", "Get the current UTC date and time.", map[string]any{}, []string{}}, {"list_tables", "List tables in the selected database and schema.", scope(), []string{}}, {"get_columns", "Get authoritative columns, types, defaults and comments for a table.", table, []string{"table"}}, {"get_table_ddl", "Get the CREATE statement for a table.", table, []string{"table"}}, {"get_sample_data", "Read up to 20 rows from a table.", sample, []string{"table"}}, {"execute_query", "Execute one read query, up to 1000 rows. Writes require an exact SQL confirmation bound to this connection/database/schema. Production writes are blocked.", map[string]any{"sql": stringType, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000}}, []string{"sql"}}, {"explain_query", "Get an execution plan for one read query.", map[string]any{"sql": stringType}, []string{"sql"}}}
}
func aiAgentToolJSON(c map[string]any) []any {
	out := []any{}
	for _, spec := range aiAgentTools() {
		schema := map[string]any{"type": "object", "properties": spec.properties, "required": spec.required, "additionalProperties": false}
		switch {
		case str(c, "provider") == "gemini":
			out = append(out, map[string]any{"name": spec.name, "description": spec.desc, "parameters": schema})
		case aiAnthropic(c):
			out = append(out, map[string]any{"name": spec.name, "description": spec.desc, "input_schema": schema})
		case str(c, "apiStyle") == "responses":
			out = append(out, map[string]any{"type": "function", "name": spec.name, "description": spec.desc, "parameters": schema})
		default:
			out = append(out, map[string]any{"type": "function", "function": map[string]any{"name": spec.name, "description": spec.desc, "parameters": schema}})
		}
	}
	return out
}
func aiAgentCalls(data, c map[string]any) ([]map[string]any, error) {
	out := []map[string]any{}
	add := func(id, name string, args any) error {
		if text, ok := args.(string); ok {
			if json.Unmarshal([]byte(text), &args) != nil {
				return errors.New("Invalid AI tool arguments")
			}
		}
		m, ok := args.(map[string]any)
		if !ok {
			return errors.New("AI tool arguments must be an object")
		}
		if id == "" {
			id = fmt.Sprintf("call_%d", len(out)+1)
		}
		out = append(out, map[string]any{"id": id, "name": name, "arguments": m})
		return nil
	}
	switch {
	case str(c, "provider") == "gemini":
		for _, candidate := range appItems(data["candidates"]) {
			for _, part := range appItems(appObject(candidate["content"])["parts"]) {
				call := appObject(part["functionCall"])
				if len(call) > 0 {
					if err := add(str(call, "id"), str(call, "name"), call["args"]); err != nil {
						return nil, err
					}
					out[len(out)-1]["providerPayload"] = part
				}
			}
		}
	case aiAnthropic(c):
		for _, part := range appItems(data["content"]) {
			if str(part, "type") == "tool_use" {
				if err := add(str(part, "id"), str(part, "name"), part["input"]); err != nil {
					return nil, err
				}
			}
		}
	case str(c, "apiStyle") == "responses":
		for _, part := range appItems(data["output"]) {
			if str(part, "type") == "function_call" {
				if err := add(str(part, "call_id"), str(part, "name"), part["arguments"]); err != nil {
					return nil, err
				}
			}
		}
	default:
		if choices := appItems(data["choices"]); len(choices) > 0 {
			for _, call := range appItems(appObject(choices[0]["message"])["tool_calls"]) {
				fn := appObject(call["function"])
				if err := add(str(call, "id"), str(fn, "name"), fn["arguments"]); err != nil {
					return nil, err
				}
			}
		}
	}
	if len(out) > 16 {
		return nil, errors.New("AI requested too many tools in one turn")
	}
	return out, nil
}
func aiBoundResult(value any) any {
	if res, ok := value.(database.QueryResult); ok {
		for i, row := range res.Rows {
			for j, cell := range row {
				if text, ok := cell.(string); ok && len(text) > 4000 {
					res.Rows[i][j] = text[:4000] + "…"
				}
			}
		}
		return res
	}
	return value
}
func (s *Server) aiAgentTool(ctx context.Context, p, call map[string]any, emit func(any) error) (any, error) {
	name, args := str(call, "name"), appObject(call["arguments"])
	var spec *aiToolSpec
	for _, tool := range aiAgentTools() {
		if tool.name == name {
			copy := tool
			spec = &copy
			break
		}
	}
	if spec == nil {
		return nil, errors.New("Unknown AI tool")
	}
	for key := range args {
		if _, ok := spec.properties[key]; !ok {
			return nil, errors.New("Unsupported tool argument: " + key)
		}
	}
	for _, required := range spec.required {
		if str(args, required) == "" {
			return nil, errors.New("Missing tool argument: " + required)
		}
	}
	if name == "get_current_time" {
		return map[string]any{"utc": time.Now().UTC().Format(time.RFC3339), "timezone": "UTC"}, nil
	}
	id, db, schema := str(p, "connectionId"), str(p, "database"), str(p, "schema")
	if requested := str(args, "database"); requested != "" && requested != db {
		allowed := false
		if values, ok := p["selectedDatabases"].([]any); ok {
			for _, value := range values {
				if value == requested {
					allowed = true
				}
			}
		}
		if !allowed {
			return nil, errors.New("Tool database is outside the selected scope")
		}
		db = requested
	}
	if requested := str(args, "schema"); requested != "" {
		schema = requested
	}
	config := map[string]any{}
	for _, saved := range s.snapshot().Connections {
		if str(saved, "id") == id {
			config = saved
			break
		}
	}
	if len(config) == 0 {
		return nil, errors.New("AI connection does not exist")
	}
	kind := str(config, "db_type")
	req := map[string]any{"connectionId": id, "connection_id": id, "database": db, "schema": schema, "table": str(args, "table")}
	route := ""
	approvedWrite := false
	switch name {
	case "list_tables":
		route = "/api/schema/tables"
	case "get_columns":
		route = "/api/schema/columns"
	case "get_table_ddl":
		route = "/api/schema/ddl"
	case "get_sample_data":
		table := str(args, "table")
		if strings.ContainsRune(table, 0) {
			return nil, errors.New("Invalid table name")
		}
		target := quoteSQLName(table, kind)
		if schema != "" {
			target = quoteSQLName(schema, kind) + "." + target
		}
		limit := integer(args["limit"], 20)
		if limit < 1 || limit > 20 {
			return nil, errors.New("Sample limit must be 1–20")
		}
		sql := "SELECT * FROM " + target
		switch kind {
		case "sqlserver":
			sql = fmt.Sprintf("SELECT TOP (%d) * FROM %s", limit, target)
		case "oracle":
			sql += fmt.Sprintf(" FETCH FIRST %d ROWS ONLY", limit)
		default:
			sql += fmt.Sprintf(" LIMIT %d", limit)
		}
		req["sql"] = sql
		req["readOnly"] = true
		req["maxRows"] = limit
		route = "/api/query/execute"
	case "execute_query", "explain_query":
		sql := str(args, "sql")
		if len(sql) > 64<<10 {
			return nil, errors.New("AI SQL exceeds 64 KiB")
		}
		parts, readErr := database.ValidateMCPSQL(sql, kind, true, false)
		readOnly := readErr == nil
		if !readOnly {
			if _, err := database.ValidateMCPSQL(sql, kind, false, true); err != nil {
				return nil, err
			}
			if name == "explain_query" || str(p, "mode") != "agent" {
				return nil, errors.New("This AI operation only permits read queries")
			}
			production := boolValue(config, "is_production") || boolValue(config, "production")
			if databases, ok := config["production_databases"].([]any); ok {
				for _, value := range databases {
					if value == db {
						production = true
					}
				}
			}
			if production {
				_ = emit(map[string]any{"type": "production_write_blocked", "sql": sql})
				return nil, errors.New("AI writes to production are blocked")
			}
			if boolValue(config, "read_only") {
				return nil, errors.New("Connection is read-only")
			}
			confirmed := p["allowWriteSql"] == true && str(p, "confirmedWriteSql") == sql && str(p, "confirmedConnectionId") == id && str(p, "confirmedDatabase") == db && str(p, "confirmedSchema") == schema
			if !confirmed {
				_ = emit(map[string]any{"type": "write_sql_confirmation_required", "sql": sql})
				return nil, errors.New("Write SQL requires exact user confirmation")
			}
			approvedWrite = true
			parts, _ = database.SplitStatementsForDialect(sql, kind)
		}
		if len(parts) != 1 {
			return nil, errors.New("AI tools require one SQL statement")
		}
		limit := integer(args["limit"], 50)
		if limit < 1 || limit > 1000 {
			return nil, errors.New("Query limit must be 1–1000")
		}
		req["sql"] = sql
		req["readOnly"] = readOnly
		req["maxRows"] = limit
		route = "/api/query/execute"
		if name == "explain_query" {
			plan, handled, err := buildSQL("/api/query/build-explain-sql", map[string]any{"options": map[string]any{"databaseType": kind, "sql": sql, "format": "standard"}})
			if err != nil || !handled {
				return nil, errors.New("Explain is unavailable for this engine")
			}
			req["sql"] = str(appObject(plan), "sql")
		}
	}
	if approvedWrite {
		// The agent loop shares this request across tools and turns. Consume
		// confirmation before dispatch because a failure or cancellation can
		// leave the database outcome uncertain; a retry needs fresh approval.
		p["allowWriteSql"] = false
		for _, key := range []string{"confirmedWriteSql", "confirmedConnectionId", "confirmedDatabase", "confirmedSchema"} {
			delete(p, key)
		}
	}
	result, err := s.database.Handle(ctx, route, req)
	if err != nil {
		return nil, errors.New(s.redactError(err.Error(), config))
	}
	return aiBoundResult(result), nil
}
func (s *Server) handleAIAgent(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	id := aiSessionID(p)
	ctx, finish, err := s.aiRuntime.register(r.Context(), id)
	if err != nil {
		writeError(w, 400, err.Error())
		return true
	}
	defer finish()
	request := cloneMap(appObject(p["request"]))
	c := appObject(request["config"])
	if _, err = aiBody(request, false); err != nil {
		writeError(w, 400, err.Error())
		return true
	}
	if _, err = aiEndpoint(c, false, false); err != nil {
		writeError(w, 400, err.Error())
		return true
	}
	if str(p, "connectionId") == "" || str(p, "database") == "" {
		writeError(w, 400, "Agent requires a connection and database")
		return true
	}
	if mode := str(p, "mode"); mode != "" && mode != "ask" && mode != "agent" {
		writeError(w, 400, "Invalid AI mode")
		return true
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	emit := func(v any) error { return aiSSE(w, v) }
	err = s.aiAgentLoop(ctx, request, p, emit)
	if err != nil {
		_ = emit(map[string]any{"type": "error", "message": aiSafeError(c, err.Error())})
	}
	return true
}
func (s *Server) aiAgentLoop(ctx context.Context, request, p map[string]any, emit func(any) error) error {
	c := appObject(request["config"])
	messages := appItems(request["messages"])
	maxTurns := integer(s.snapshot().Settings["max-agent-turns"], 20)
	if maxTurns < 1 || maxTurns > 100 {
		maxTurns = 20
	}
	for turn := 1; turn <= maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := emit(map[string]any{"type": "turn_start", "turn": turn}); err != nil {
			return err
		}
		request["messages"] = messages
		body, err := aiBody(request, false)
		if err != nil {
			return err
		}
		tools := aiAgentToolJSON(c)
		if str(c, "provider") == "gemini" {
			body["tools"] = []any{map[string]any{"functionDeclarations": tools}}
		} else {
			body["tools"] = tools
		}
		endpoint, _ := aiEndpoint(c, false, false)
		res, err := s.aiSend(ctx, c, "POST", endpoint, body)
		if err != nil {
			return err
		}
		data, err := aiReadJSON(res)
		if err != nil {
			return err
		}
		text, reason := aiText(data, c)
		if reason != "" {
			if err = emit(map[string]any{"type": "reasoning_delta", "delta": reason}); err != nil {
				return err
			}
		}
		if text != "" {
			if err = emit(map[string]any{"type": "text_delta", "delta": text}); err != nil {
				return err
			}
		}
		calls, err := aiAgentCalls(data, c)
		if err != nil {
			return err
		}
		if len(calls) == 0 {
			if text == "" {
				return errors.New("AI agent returned neither text nor tool calls")
			}
			_ = emit(map[string]any{"type": "turn_end", "turn": turn})
			return emit(map[string]any{"type": "agent_end", "input_tokens": nil, "output_tokens": nil})
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": text, "toolCalls": calls})
		for _, call := range calls {
			if err = emit(map[string]any{"type": "tool_call_start", "tool_call_id": str(call, "id"), "tool_name": str(call, "name"), "args": call["arguments"]}); err != nil {
				return err
			}
			value, e := s.aiAgentTool(ctx, p, call, emit)
			isError := e != nil
			if e != nil {
				value = map[string]any{"error": e.Error()}
			}
			raw, _ := json.Marshal(value)
			if len(raw) > 128<<10 {
				raw = []byte(`{"error":"Tool result exceeds 128 KiB; narrow the query"}`)
				isError = true
			}
			if err = emit(map[string]any{"type": "tool_call_end", "tool_call_id": str(call, "id"), "tool_name": str(call, "name"), "result": value, "is_error": isError}); err != nil {
				return err
			}
			messages = append(messages, map[string]any{"role": "tool", "toolCallId": str(call, "id"), "toolName": str(call, "name"), "content": string(raw)})
		}
		if err = emit(map[string]any{"type": "turn_end", "turn": turn}); err != nil {
			return err
		}
	}
	return errors.New("AI agent reached the maximum number of turns")
}
