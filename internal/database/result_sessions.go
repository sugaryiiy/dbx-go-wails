package database

import (
	"crypto/rand"
	"errors"
	"time"
)

// Bounded snapshots allow subsequent pages without replaying SQL against data
// which may have changed. They never retain a database cursor or transaction.
type resultSession struct {
	result                           QueryResult
	connectionID, database, clientID string
	pageSize                         int
	lastUsed                         time.Time
	bytes                            int
}

func resultPage(result QueryResult, offset, size int, id string) QueryResult {
	end := offset + size
	if end > len(result.Rows) {
		end = len(result.Rows)
	}
	if offset > len(result.Rows) {
		offset = len(result.Rows)
	}
	out := result
	out.Rows = result.Rows[offset:end]
	out.HasMore = end < len(result.Rows)
	out.SessionID = id
	return out
}
func (m *Manager) retainResult(result QueryResult, p map[string]any, size int) (QueryResult, error) {
	if len(result.Rows) <= size {
		return result, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, s := range m.resultSessions {
		if time.Since(s.lastUsed) > 15*time.Minute {
			delete(m.resultSessions, key)
		}
	}
	if m.closed || m.connections[str(p, "connectionId", "connection_id")] == nil {
		return emptyResult(), errors.New("database connection has been closed")
	}
	if len(m.resultSessions) >= 32 {
		return emptyResult(), errors.New("too many retained results; close unused result tabs")
	}
	// Limit the complete retained store, not merely each query's response.
	rows := len(result.Rows)
	bytes := 0
	for _, row := range result.Rows {
		for _, cell := range row {
			if value, ok := cell.(string); ok {
				bytes += len(value)
			} else {
				bytes += 16
			}
		}
	}
	cost := bytes
	for _, s := range m.resultSessions {
		rows += len(s.result.Rows)
		bytes += s.bytes
	}
	if rows > 200000 {
		return emptyResult(), errors.New("retained result limit reached; close unused result tabs")
	}
	if bytes > 64<<20 {
		return emptyResult(), errors.New("retained results exceed 64 MiB; close unused result tabs")
	}
	id := rand.Text()
	m.resultSessions[id] = &resultSession{result: result, connectionID: str(p, "connectionId", "connection_id"), database: str(p, "database"), clientID: str(p, "clientSessionId"), pageSize: size, lastUsed: time.Now(), bytes: cost}
	return resultPage(result, 0, size, id), nil
}
func (m *Manager) fetchResult(p map[string]any) (any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := str(p, "resultSessionId")
	s := m.resultSessions[id]
	if s == nil || time.Since(s.lastUsed) > 15*time.Minute {
		delete(m.resultSessions, id)
		return nil, errors.New("query result session expired or was closed; execute the query again")
	}
	if s.connectionID != str(p, "connectionId", "connection_id") || s.database != str(p, "database") || s.clientID != str(p, "clientSessionId") {
		return nil, errors.New("query result session belongs to another connection or tab")
	}
	size := integer(p, s.pageSize, "pageSize")
	offset := integer(p, 0, "rowOffset")
	if size <= 0 || size > 100000 || offset < 0 || offset > 10000000 {
		return nil, errors.New("invalid result pagination")
	}
	s.lastUsed = time.Now()
	return resultPage(s.result, offset, size, id), nil
}
func (m *Manager) closeResultSession(connectionID, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.resultSessions[id]
	if s == nil || s.connectionID != connectionID {
		return false
	}
	delete(m.resultSessions, id)
	return true
}
