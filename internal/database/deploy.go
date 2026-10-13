package database

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"
)

func (m *Manager) executeSchemaDeployment(ctx context.Context, p map[string]any) (any, error) {
	c, err := m.ConnectionConfig(str(p, "connectionId"))
	if err != nil {
		return nil, err
	}
	statements := []string{}
	switch raw := p["statements"].(type) {
	case []any:
		for _, v := range raw {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("statements must contain strings")
			}
			parts, e := splitStatements(s, c.Type)
			if e != nil {
				return nil, e
			}
			statements = append(statements, parts...)
		}
	case []string:
		for _, s := range raw {
			parts, e := splitStatements(s, c.Type)
			if e != nil {
				return nil, e
			}
			statements = append(statements, parts...)
		}
	default:
		return nil, fmt.Errorf("statements are required")
	}
	if len(statements) > 1000 {
		return nil, fmt.Errorf("schema deployment exceeds 1000 statements")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	atomic := c.Type == "sqlite" || c.Type == "postgres"
	metadata := record{"source": "schema_diff_deploy", "mode": "single_connection_tx", "ddl_atomic": atomic, "atomicity": "guaranteed_rollback"}
	if !atomic {
		metadata["mode"] = "sequential_autocommit"
		metadata["atomicity"] = "partial_effects_possible"
	}
	participant := record{"id": c.ID, "name": c.Name, "role": "database", "connectionId": c.ID, "database": str(p, "database")}
	out := record{"transactionId": rand.Text(), "status": "committed", "participants": []record{participant}, "createdAt": now, "updatedAt": now, "executedCount": 0, "statementCount": len(statements), "error": nil, "metadata": metadata}
	destructive := 0
	for _, s := range statements {
		verb := firstWord(s, c.Type)
		if verb == "DROP" || verb == "TRUNCATE" || verb == "ALTER" && containsSQLWord(s, "DROP", c.Type) {
			destructive++
		}
		switch verb {
		case "BEGIN", "START", "COMMIT", "ROLLBACK", "END", "SET", "USE", "ATTACH", "DETACH":
			return nil, fmt.Errorf("deployment cannot contain session or transaction controls")
		}
	}
	if destructive > 0 && !boolean(p, "destructiveConfirmed") {
		out["status"] = "rolled_back"
		out["error"] = "Destructive schema diff SQL requires explicit confirmation"
		metadata["blocked"] = "destructive_confirmation_required"
		metadata["destructive_statement_count"] = destructive
		return out, nil
	}
	if len(statements) == 0 {
		return out, nil
	}
	request := map[string]any{"connectionId": c.ID, "database": p["database"], "schema": p["schema"], "statements": statements, "executionId": p["executionId"]}
	start := time.Now()
	if atomic {
		result, e := m.execute(ctx, "/api/query/execute-in-transaction", request)
		if e != nil {
			out["status"] = "rolled_back"
			if errors.Is(e, ErrCommitOutcomeUnknown) {
				out["status"] = "mixed"
				out["executedCount"] = len(statements)
				metadata["atomicity"] = "commit_outcome_unknown"
			}
			out["error"] = redact(e, c).Error()
		} else {
			out["executedCount"] = len(statements)
			metadata["affected_rows"] = result.(QueryResult).AffectedRows
		}
	} else {
		metadata["affected_rows"] = int64(0)
		for index, s := range statements {
			request["sql"] = s
			delete(request, "statements")
			result, e := m.Handle(ctx, "/api/query/execute", request)
			if e != nil {
				out["status"] = "mixed"
				out["error"] = fmt.Sprintf("Statement %d failed: %s", index+1, redact(e, c).Error())
				break
			}
			out["executedCount"] = index + 1
			if q, ok := result.(QueryResult); ok {
				metadata["affected_rows"] = metadata["affected_rows"].(int64) + q.AffectedRows
			}
		}
	}
	metadata["execution_time_ms"] = time.Since(start).Milliseconds()
	out["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	return out, nil
}
