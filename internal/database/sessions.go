package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"time"
)

// A tab owns its physical connection. Session state and explicit transactions
// never leak into the shared pool, including after cancellation or disconnect.
type querySession struct {
	mu                               sync.Mutex
	conn                             *sql.Conn
	connectionID, database, clientID string
	inTransaction, dirty             bool
	readOnly                         bool
	lastUsed                         time.Time
	closed                           bool
}

func sessionKey(id, database, client string) string { return id + "\x00" + database + "\x00" + client }

func (s *querySession) closeLocked() {
	if s.closed {
		return
	}
	s.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if s.inTransaction || s.dirty {
		_, _ = s.conn.ExecContext(ctx, "ROLLBACK")
	}
	// Resetting all dialect-specific SET, temp objects and session variables is
	// not reliably portable. Discard the physical connection instead.
	_ = s.conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = s.conn.Close()
}

func (m *Manager) acquireQuerySession(ctx context.Context, db *sql.DB, id, database, client string) (*querySession, error) {
	key := sessionKey(id, database, client)
	m.mu.RLock()
	s := m.sessions[key]
	m.mu.RUnlock()
	if s != nil {
		s.mu.Lock()
		if !s.closed {
			s.lastUsed = time.Now()
			return s, nil
		}
		s.mu.Unlock()
		m.mu.Lock()
		if m.sessions[key] == s {
			delete(m.sessions, key)
		}
		m.mu.Unlock()
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	candidate := &querySession{conn: conn, connectionID: id, database: database, clientID: client, lastUsed: time.Now()}
	m.mu.Lock()
	if m.closed || m.connections[id] == nil {
		m.mu.Unlock()
		conn.Close()
		return nil, errors.New("connection was closed while acquiring a query session")
	}
	if len(m.sessions) >= 64 {
		m.mu.Unlock()
		conn.Close()
		return nil, errors.New("too many open query sessions; close unused query tabs")
	}
	if existing := m.sessions[key]; existing != nil {
		m.mu.Unlock()
		conn.Close()
		existing.mu.Lock()
		if existing.closed {
			existing.mu.Unlock()
			return nil, errors.New("query session has been closed")
		}
		existing.lastUsed = time.Now()
		return existing, nil
	}
	candidate.mu.Lock()
	m.sessions[key] = candidate
	m.mu.Unlock()
	return candidate, nil
}

func (m *Manager) closeQuerySessions(id, database, client string) bool {
	m.mu.Lock()
	closing := []*querySession{}
	for key, s := range m.sessions {
		if (id == "" || s.connectionID == id) && (database == "" || s.database == database) && (client == "" || s.clientID == client) {
			delete(m.sessions, key)
			closing = append(closing, s)
		}
	}
	for key, s := range m.resultSessions {
		if (id == "" || s.connectionID == id) && (database == "" || s.database == database) && (client == "" || s.clientID == client) {
			delete(m.resultSessions, key)
		}
	}
	for _, q := range m.running {
		if (id == "" || q.connectionID == id) && (client == "" || q.clientSessionID == client) {
			q.cancel()
		}
	}
	m.mu.Unlock()
	for _, s := range closing {
		s.mu.Lock()
		s.closeLocked()
		s.mu.Unlock()
	}
	return len(closing) > 0
}

func (m *Manager) ConnectionConfig(id string) (Config, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c := m.connections[id]
	if c == nil {
		return Config{}, errors.New("database connection is not open")
	}
	return c.config, nil
}

func (m *Manager) HasOpenTransaction(id, client string) bool {
	m.mu.RLock()
	sessions := []*querySession{}
	for _, s := range m.sessions {
		if s.connectionID == id && s.clientID == client {
			sessions = append(sessions, s)
		}
	}
	m.mu.RUnlock()
	for _, s := range sessions {
		s.mu.Lock()
		open := !s.closed && s.inTransaction
		s.mu.Unlock()
		if open {
			return true
		}
	}
	return false
}
