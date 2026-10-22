package database

import (
	"context"
	"errors"
	"strings"

	"github.com/sugaryiiy/magpiedb/internal/agent"
)

type agentCursor struct {
	session  *agent.Session
	readOnly bool
}

func (d *agentDriver) sessionLock(ctx context.Context) error {
	select {
	case d.sessionGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (d *agentDriver) sessionFor(ctx context.Context, path string, p map[string]any) (*agent.Session, error) {
	if err := d.sessionLock(ctx); err != nil {
		return nil, err
	}
	defer func() { <-d.sessionGate }()
	if d.closed {
		return nil, errors.New("agent connection is closed")
	}
	if cursor := str(p, "resultSessionId"); cursor != "" {
		entry, ok := d.cursors[cursor]
		if !ok {
			return nil, errors.New("query cursor does not belong to this connection")
		}
		return entry.session, nil
	}
	database := str(p, "database")
	if database == "" {
		database = d.config.Database
	}
	client := str(p, "clientSessionId")
	metadata := strings.HasPrefix(path, "/api/schema/")
	if database == d.config.Database && client == "" && !metadata {
		return d.session, nil
	}
	if metadata {
		client = "__metadata__"
	}
	key := database + "\x00" + client
	if session := d.sessions[key]; session != nil {
		return session, nil
	}
	if len(d.sessions) >= 64 {
		return nil, errors.New("agent connection session limit reached; close unused editors")
	}
	config := d.config
	config.Database = database
	params := agentConnectParams(config)
	if metadata {
		params["sessionRole"] = "metadata"
	}
	packageKey, ok := agentPackageKey(config)
	if !ok {
		return nil, unsupported("agent driver profile")
	}
	session, err := d.store.Open(ctx, packageKey, params)
	if err != nil {
		return nil, err
	}
	d.sessions[key] = session
	return session, nil
}
func (d *agentDriver) Close() error {
	if err := d.sessionLock(context.Background()); err != nil {
		return err
	}
	if d.closed {
		<-d.sessionGate
		return nil
	}
	d.closed = true
	sessions := d.sessions
	d.sessions = map[string]*agent.Session{}
	d.cursors = map[string]agentCursor{}
	<-d.sessionGate
	errs := []error{d.store.Release(d.session)}
	for _, session := range sessions {
		errs = append(errs, d.store.Release(session))
	}
	return errors.Join(errs...)
}
func (d *agentDriver) rememberCursor(session *agent.Session, result map[string]any, readOnly bool) {
	id, _ := result["session_id"].(string)
	if id == "" {
		return
	}
	_ = d.sessionLock(context.Background())
	defer func() { <-d.sessionGate }()
	if !d.closed {
		d.cursors[id] = agentCursor{session: session, readOnly: readOnly}
	}
}
func (d *agentDriver) cursorIsReadOnly(id string) bool {
	_ = d.sessionLock(context.Background())
	defer func() { <-d.sessionGate }()
	return d.cursors[id].readOnly
}
func (d *agentDriver) closeCursor(ctx context.Context, id string) (any, error) {
	if err := d.sessionLock(ctx); err != nil {
		return nil, err
	}
	entry, ok := d.cursors[id]
	delete(d.cursors, id)
	<-d.sessionGate
	if !ok {
		return false, nil
	}
	var result any
	err := entry.session.Call(ctx, "close_query_session", map[string]any{"sessionId": id}, &result)
	if err != nil {
		return nil, err
	}
	return result, nil
}
func (d *agentDriver) closeClientSession(database, client string) (any, error) {
	if client == "" {
		return nil, errors.New("clientSessionId is required")
	}
	_ = d.sessionLock(context.Background())
	key := database + "\x00" + client
	session := d.sessions[key]
	delete(d.sessions, key)
	if session != nil {
		for id, cursor := range d.cursors {
			if cursor.session == session {
				delete(d.cursors, id)
			}
		}
	}
	<-d.sessionGate
	if session == nil {
		return false, nil
	}
	err := d.store.Release(session)
	return err == nil, err
}
