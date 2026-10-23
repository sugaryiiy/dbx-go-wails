package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Hold the first physical connection attempt so a later request can publish
// its session first. This deterministically exercises the candidate collision.
type retirementConnector struct {
	calls   atomic.Int32
	started chan struct{}
	resume  chan struct{}
	arm     func()
}

func (c *retirementConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if c.calls.Add(1) == 1 {
		close(c.started)
		select {
		case <-c.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return retirementConn{onClose: c.arm}, nil
	}
	return retirementConn{}, nil
}
func (*retirementConnector) Driver() driver.Driver { return retirementDriver{} }

type retirementDriver struct{}

func (retirementDriver) Open(string) (driver.Conn, error) { return retirementConn{}, nil }

type retirementConn struct{ onClose func() }

func (retirementConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (retirementConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (c retirementConn) Close() error {
	if c.onClose != nil {
		c.onClose()
	}
	return nil
}

// Done is evaluated when LockContext enters its select, after the contender
// has found the existing locked session. No sleep or scheduler timing is needed.
type retirementWaitContext struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
	armed   atomic.Bool
}

func (c *retirementWaitContext) Done() <-chan struct{} {
	if c.armed.Load() {
		c.once.Do(func() { close(c.waiting) })
	}
	return c.Context.Done()
}

func TestSessionCandidateCollisionDuringRetirement(t *testing.T) {
	for _, action := range []string{"clean release", "explicit close", "connection replaced"} {
		t.Run(action, func(t *testing.T) {
			connector := &retirementConnector{started: make(chan struct{}), resume: make(chan struct{})}
			db := sql.OpenDB(connector)
			db.SetMaxIdleConns(0)
			m := NewManager()
			m.connections["test"] = &connection{pools: map[string]*sql.DB{"main": db}}
			t.Cleanup(func() { _ = m.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			waiting := &retirementWaitContext{Context: ctx, waiting: make(chan struct{})}
			connector.arm = func() { waiting.armed.Store(true) }
			type acquired struct {
				session *querySession
				err     error
			}
			result := make(chan acquired, 1)
			go func() {
				session, err := m.acquireQuerySession(waiting, db, "test", "main", "tab")
				result <- acquired{session, err}
			}()
			select {
			case <-connector.started:
			case <-ctx.Done():
				t.Fatal("first connection attempt did not start")
			}
			existing, err := m.acquireQuerySession(ctx, db, "test", "main", "tab")
			if err != nil {
				t.Fatal(err)
			}
			locked := true
			defer func() {
				if locked {
					existing.mu.Unlock()
				}
			}()
			close(connector.resume)
			select {
			case <-waiting.waiting:
			case <-ctx.Done():
				t.Fatal("contender did not reach the existing session")
			}
			switch action {
			case "clean release":
				m.releaseCleanQuerySession(existing)
			case "explicit close":
				existing.closeLocked()
			case "connection replaced":
				m.releaseCleanQuerySession(existing)
				m.mu.Lock()
				m.connections["test"] = &connection{pools: map[string]*sql.DB{"main": db}}
				m.mu.Unlock()
			}
			existing.mu.Unlock()
			locked = false
			select {
			case got := <-result:
				if got.session != nil {
					defer got.session.mu.Unlock()
					defer m.releaseCleanQuerySession(got.session)
				}
				if action == "clean release" {
					if got.err != nil || got.session == nil || got.session == existing || got.session.closed {
						t.Fatalf("clean retirement did not reacquire a usable session: %+v", got)
					}
				} else if got.err == nil || got.session != nil {
					t.Fatalf("closed connection was resurrected: %+v", got)
				}
			case <-ctx.Done():
				t.Fatal("queued acquisition did not complete")
			}
		})
	}
}
