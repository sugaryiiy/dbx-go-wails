package database

import (
	"context"
	"crypto/rand"
	"errors"
	"time"
)

// BeginBackupSnapshot holds one physical connection for a read-only,
// repeatable-read transaction. Call CloseBackupSnapshot after all exports.
func (m *Manager) BeginBackupSnapshot(ctx context.Context, id, database string) (string, error) {
	db, cfg, _, err := m.pool(ctx, id, database)
	if err != nil {
		return "", err
	}
	if cfg.Type != "sqlite" && cfg.Type != "mysql" && cfg.Type != "postgres" {
		return "", unsupported("consistent snapshots for " + cfg.Type)
	}
	if cfg.Type == "sqlite" && sqlitePath(cfg) == ":memory:" {
		return "", unsupported("snapshots for private in-memory SQLite")
	}
	client := "manual-backup-" + rand.Text()
	session, err := m.acquireQuerySession(ctx, db, id, database, client)
	if err != nil {
		return "", err
	}
	defer session.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	statements := []string{"PRAGMA query_only = ON", "BEGIN", "SELECT count(*) FROM sqlite_schema"}
	switch cfg.Type {
	case "mysql":
		statements = []string{"SET TRANSACTION ISOLATION LEVEL REPEATABLE READ", "START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY"}
	case "postgres":
		statements = []string{"BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY"}
	}
	for _, statement := range statements {
		if _, err = session.conn.ExecContext(ctx, statement); err != nil {
			session.dirty = true
			session.closeLocked()
			return "", redact(err, cfg)
		}
	}
	session.readOnly = true
	session.inTransaction = true
	session.dirty = true
	return client, nil
}

func (m *Manager) CloseBackupSnapshot(id, database, session string) bool {
	return m.closeQuerySessions(id, database, session)
}

func (m *Manager) ValidateBackupSnapshot(id, database, client string) error {
	m.mu.RLock()
	s := m.sessions[sessionKey(id, database, client)]
	m.mu.RUnlock()
	if s == nil {
		return errors.New("backup snapshot was closed or belongs to another database")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.readOnly || !s.inTransaction || time.Since(s.lastUsed) > 10*time.Minute {
		return errors.New("backup snapshot has expired")
	}
	return nil
}
