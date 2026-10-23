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
	mu                               sessionMutex
	conn                             *sql.Conn
	connectionID, database, clientID string
	inTransaction, dirty             bool
	readOnly                         bool
	lastUsed                         time.Time
	closed                           bool
	released                         bool // Clean retirement may be retried; explicit close must stop queued work.
}

// Session operations may wait behind a long-running query. Unlike sync.Mutex,
// this gate lets a queued request leave as soon as its context is cancelled.
type sessionMutex struct {
	once sync.Once
	gate chan struct{}
}

func (m *sessionMutex) LockContext(ctx context.Context) error {
	m.once.Do(func() { m.gate = make(chan struct{}, 1) })
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case m.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-m.gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *sessionMutex) Lock()   { _ = m.LockContext(context.Background()) }
func (m *sessionMutex) Unlock() { <-m.gate }

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
	owner := m.connections[id]
	validPool := false
	if owner != nil {
		for _, pool := range owner.pools {
			validPool = validPool || pool == db
		}
	}
	m.mu.RUnlock()
	if !validPool {
		return nil, errors.New("connection was closed while acquiring a query session")
	}
	for {
		m.mu.RLock()
		if m.closed || m.connections[id] != owner || owner.maintenanceCancel != nil {
			m.mu.RUnlock()
			return nil, errors.New("connection was closed while acquiring a query session")
		}
		s := m.sessions[key]
		m.mu.RUnlock()
		if s == nil {
			conn, err := db.Conn(ctx)
			if err != nil {
				return nil, err
			}
			candidate := &querySession{conn: conn, connectionID: id, database: database, clientID: client, lastUsed: time.Now()}
			m.mu.Lock()
			if m.closed || m.connections[id] != owner || owner.maintenanceCancel != nil {
				m.mu.Unlock()
				conn.Close()
				return nil, errors.New("connection was closed while acquiring a query session")
			}
			if s = m.sessions[key]; s == nil {
				if len(m.sessions) >= 64 {
					m.mu.Unlock()
					conn.Close()
					return nil, errors.New("too many open query sessions; close unused query tabs")
				}
				candidate.mu.Lock()
				m.sessions[key] = candidate
				m.mu.Unlock()
				return candidate, nil
			}
			m.mu.Unlock()
			conn.Close()
		}
		if err := s.mu.LockContext(ctx); err != nil {
			return nil, err
		}
		if s.closed {
			retry := s.released
			s.mu.Unlock()
			if retry {
				continue
			}
			return nil, errors.New("query session has been closed")
		}
		m.mu.RLock()
		valid := !m.closed && m.connections[id] == owner && owner.maintenanceCancel == nil && m.sessions[key] == s
		m.mu.RUnlock()
		if !valid {
			s.mu.Unlock()
			return nil, errors.New("query session has been closed")
		}
		s.lastUsed = time.Now()
		return s, nil
	}
}

// A completed transaction without connection-local settings has nothing left
// to preserve. Return its connection instead of pinning every visited tab to
// the small shared pool until the tab is closed.
func (m *Manager) releaseCleanQuerySession(s *querySession) {
	if !s.closed && (s.inTransaction || s.dirty || s.readOnly) {
		return
	}
	m.mu.Lock()
	key := sessionKey(s.connectionID, s.database, s.clientID)
	if m.sessions[key] == s {
		delete(m.sessions, key)
	}
	m.mu.Unlock()
	if !s.closed {
		s.released = true
		s.closed = true
		_ = s.conn.Close()
	}
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
