package database

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
)

func (m *Manager) handleManualTransaction(ctx context.Context, path string, p map[string]any) (any, bool, error) {
	if !strings.HasPrefix(path, "/api/transaction/") {
		return nil, false, nil
	}
	if path == "/api/transaction/begin" {
		c, err := m.ConnectionConfig(str(p, "connectionId"))
		if err != nil {
			return nil, true, err
		}
		if c.Type != "sqlite" && c.Type != "mysql" && c.Type != "postgres" {
			return nil, true, unsupported("manual transaction sessions for " + c.Type)
		}
		id := "manual-" + rand.Text()
		request := map[string]any{"connectionId": c.ID, "database": p["database"], "schema": p["schema"], "clientSessionId": id, "sql": "BEGIN"}
		_, err = m.execute(ctx, "/api/query/execute", request)
		if err != nil {
			return nil, true, err
		}
		return id, true, nil
	}
	id := str(p, "txnSessionId")
	if !strings.HasPrefix(id, "manual-") {
		return nil, true, errors.New("invalid manual transaction session")
	}
	m.mu.RLock()
	var session *querySession
	for _, s := range m.sessions {
		if s.clientID == id {
			session = s
			break
		}
	}
	m.mu.RUnlock()
	if session == nil {
		return nil, true, errors.New("manual transaction session was closed")
	}
	if database := str(p, "database"); database != "" && database != session.database {
		return nil, true, errors.New("manual transaction belongs to another database")
	}
	request := map[string]any{}
	for k, v := range p {
		request[k] = v
	}
	request["connectionId"] = session.connectionID
	request["database"] = session.database
	request["clientSessionId"] = id
	switch path {
	case "/api/transaction/execute":
		returnValue, err := m.execute(ctx, "/api/query/execute-multi", request)
		return returnValue, true, err
	case "/api/transaction/commit", "/api/transaction/rollback":
		if err := session.mu.LockContext(ctx); err != nil {
			return nil, true, err
		}
		readOnly := session.readOnly
		session.mu.Unlock()
		if readOnly {
			m.closeQuerySessions(session.connectionID, session.database, id)
			return emptyResult(), true, nil
		}
		request["sql"] = "COMMIT"
		if strings.HasSuffix(path, "rollback") {
			request["sql"] = "ROLLBACK"
		}
		result, err := m.execute(ctx, "/api/query/execute", request)
		m.closeQuerySessions(session.connectionID, session.database, id)
		return result, true, err
	default:
		return nil, true, unsupported(path)
	}
}
