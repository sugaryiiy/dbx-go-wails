package database

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/sugaryiiy/magpiedb/internal/agent"
	"strings"
	"time"
)

// Native drivers preserve their database's protocol and operation semantics.
// All calls share the manager's timeout, cancellation and write-unlock boundary.
type nativeDriver interface {
	Close() error
	Ping(context.Context) error
	Info(context.Context) (map[string]any, error)
	Handle(context.Context, string, map[string]any, bool) (any, error)
}

// Populated once by the in-process native driver implementations during init.
var nativeOpeners = map[string]func(context.Context, Config) (nativeDriver, error){}

func usesNativeDriver(kind string) bool {
	return nativeOpeners[kind] != nil || agent.UsesDatabase(kind)
}

func (c *Config) UnmarshalJSON(data []byte) error {
	type plain Config
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &decoded.Raw); err != nil {
		return err
	}
	*c = Config(decoded)
	return nil
}

func openNative(ctx context.Context, c Config) (nativeDriver, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	open, ok := nativeOpeners[c.Type]
	if !ok {
		return nil, unsupported("database type " + c.Type)
	}
	seconds := c.ConnectTimeout
	if seconds <= 0 {
		seconds = 15
	}
	if seconds > 300 {
		seconds = 300
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	driver, err := open(ctx, c)
	if err != nil {
		return nil, redact(err, c)
	}
	return driver, nil
}

func testNative(ctx context.Context, c Config, withInfo bool) (any, error) {
	driver, err := openNative(ctx, c)
	if err != nil {
		return nil, err
	}
	defer driver.Close()
	if !withInfo {
		return "Connection successful", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	info, err := driver.Info(ctx)
	return map[string]any{"message": "Connection successful", "databaseInfo": info}, redact(err, c)
}

func (m *Manager) connectNative(ctx context.Context, c Config) error {
	driver, err := m.openNative(ctx, c)
	if err != nil {
		return err
	}
	life, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		driver.Close()
		return errors.New("database manager is closed")
	}
	old := m.connections[c.ID]
	if old != nil && old.maintenanceCancel != nil {
		m.mu.Unlock()
		cancel()
		driver.Close()
		return errors.New("SQLite restore is in progress")
	}
	for _, query := range m.running {
		if query.connectionID == c.ID {
			query.cancel()
		}
	}
	m.connections[c.ID] = &connection{config: c, native: driver, life: life, cancel: cancel}
	m.mu.Unlock()
	if old != nil {
		if old.cancel != nil {
			old.cancel()
		}
		if old.native != nil {
			old.native.Close()
		}
		for _, pool := range old.pools {
			pool.Close()
		}
	}
	return nil
}

func (m *Manager) handleNative(parent context.Context, path string, p map[string]any) (any, bool, error) {
	// These routes manage the connection itself, including replacement/cancel.
	switch path {
	case "/api/connection/connect", "/api/connection/test", "/api/connection/test-info", "/api/connection/is-open", "/api/connection/disconnect",
		"/api/connection/write-unlock", "/api/connection/write-unlock/lock", "/api/connection/write-unlock/state", "/api/query/cancel":
		return nil, false, nil
	}
	id := str(p, "connectionId", "connection_id")
	m.mu.RLock()
	session := m.connections[id]
	if session == nil || session.native == nil {
		m.mu.RUnlock()
		return nil, false, nil
	}
	c := session.config
	if p["maxAffectedRowsPerStatement"] != nil {
		m.mu.RUnlock()
		return nil, true, unsupported("atomic affected row guards for " + c.Type)
	}
	if path == "/api/query/close-session" || path == "/api/query/close-client-session" {
		if _, ok := session.native.(*agentDriver); !ok {
			m.mu.RUnlock()
			return nil, false, nil
		}
	}
	locked := (c.ReadOnly || c.Production || len(c.ProductionDatabases) > 0) && !time.Now().Before(session.unlockedUntil)
	m.mu.RUnlock()
	seconds := integer(p, c.QueryTimeout, "timeoutSecs")
	if seconds < 0 || seconds > 86400 {
		return nil, true, errors.New("timeoutSecs must be between 0 and 86400")
	}
	if seconds == 0 {
		seconds = 300
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(seconds)*time.Second)
	defer cancel()
	stop := context.AfterFunc(session.life, cancel)
	defer stop()
	executionID := str(p, "executionId")
	m.mu.Lock()
	if m.connections[id] != session || m.closed {
		m.mu.Unlock()
		return nil, true, errors.New("database connection is closed")
	}
	if executionID != "" {
		if _, exists := m.running[executionID]; exists {
			m.mu.Unlock()
			return nil, true, errors.New("execution id is already running")
		}
		m.running[executionID] = runningQuery{cancel: cancel, connectionID: id}
	}
	m.mu.Unlock()
	if executionID != "" {
		defer func() { m.mu.Lock(); delete(m.running, executionID); m.mu.Unlock() }()
	}
	var result any
	var err error
	switch path {
	case "/api/connection/database-info":
		result, err = session.native.Info(ctx)
	case "/api/connection/check-health", "/api/connection/prewarm":
		err = session.native.Ping(ctx)
	case "/api/connection/identifier-quote":
		if quoter, ok := session.native.(interface {
			IdentifierQuote(context.Context) (string, error)
		}); ok {
			result, err = quoter.IdentifierQuote(ctx)
		} else if c.Type == "clickhouse" {
			result = "`"
		} else {
			err = unsupported("SQL identifier quoting for " + c.Type)
		}
	default:
		if strings.HasPrefix(path, "/api/") {
			result, err = session.native.Handle(ctx, path, p, locked || boolean(p, "readOnly"))
		} else {
			err = unsupported(path)
		}
	}
	return result, true, redact(err, c)
}
