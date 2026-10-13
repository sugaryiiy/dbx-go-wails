package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type procedureCallConnector struct {
	statement string
	columns   []string
	values    []driver.Value
	newRows   func(context.Context) driver.Rows
	queries   atomic.Int64
	execs     atomic.Int64
}

func (c *procedureCallConnector) Connect(context.Context) (driver.Conn, error) {
	return &procedureCallConn{fixture: c}, nil
}
func (*procedureCallConnector) Driver() driver.Driver { return procedureCallDriver{} }

type procedureCallDriver struct{}

func (procedureCallDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use the procedure call connector")
}

type procedureCallConn struct{ fixture *procedureCallConnector }

func (*procedureCallConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement")
}
func (*procedureCallConn) Close() error { return nil }
func (*procedureCallConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (c *procedureCallConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	c.fixture.execs.Add(1)
	return nil, errors.New("procedure result was discarded through ExecContext")
}
func (c *procedureCallConn) QueryContext(ctx context.Context, statement string, _ []driver.NamedValue) (driver.Rows, error) {
	c.fixture.queries.Add(1)
	if statement == "SELECT 9" {
		return &procedureCallRows{columns: []string{"value"}, values: []driver.Value{int64(9)}}, nil
	}
	if statement != c.fixture.statement {
		return nil, errors.New("unexpected query")
	}
	if c.fixture.newRows != nil {
		return c.fixture.newRows(ctx), nil
	}
	return &procedureCallRows{columns: c.fixture.columns, values: c.fixture.values}, nil
}

// Mimic MySQL's watcher: Close stops cancellation before draining any unread
// result packets. NextResultSet and Next instead keep the watcher running.
type procedureCallFollowingRows struct {
	ctx          context.Context
	firstCount   int
	waitStage    string
	started      chan string
	release      <-chan struct{}
	interrupted  chan struct{}
	stopWatcher  chan struct{}
	watchStopped chan struct{}
	stopOnce     sync.Once
	signalOnce   sync.Once
	set          int
	read         int
	fullyRead    bool
	err          error
}

func newProcedureCallFollowingRows(ctx context.Context, firstCount int, waitStage string, release <-chan struct{}) *procedureCallFollowingRows {
	r := &procedureCallFollowingRows{
		ctx: ctx, firstCount: firstCount, waitStage: waitStage,
		started: make(chan string, 1), release: release,
		interrupted: make(chan struct{}), stopWatcher: make(chan struct{}), watchStopped: make(chan struct{}),
	}
	go func() {
		defer close(r.watchStopped)
		select {
		case <-ctx.Done():
			close(r.interrupted)
		case <-r.stopWatcher:
		}
	}()
	return r
}

func (*procedureCallFollowingRows) Columns() []string { return []string{"value"} }
func (r *procedureCallFollowingRows) signal(stage string) {
	r.signalOnce.Do(func() { r.started <- stage })
}
func (r *procedureCallFollowingRows) Close() error {
	r.stopOnce.Do(func() { close(r.stopWatcher) })
	<-r.watchStopped
	if !r.fullyRead && r.err == nil {
		r.signal("Close")
		// Once Close disables the watcher, cancellation no longer releases this
		// simulated network read. The test's cleanup must release it instead.
		<-r.release
	}
	return r.err
}
func (r *procedureCallFollowingRows) HasNextResultSet() bool { return r.set == 0 }
func (r *procedureCallFollowingRows) NextResultSet() error {
	if r.set > 0 {
		return io.EOF
	}
	if r.waitStage == "header" {
		if err := r.wait("NextResultSet"); err != nil {
			return err
		}
	}
	r.set, r.read = 1, 0
	return nil
}
func (r *procedureCallFollowingRows) wait(stage string) error {
	r.signal(stage)
	select {
	case <-r.interrupted:
		r.err = r.ctx.Err()
		return r.err
	case <-r.release:
		return nil
	}
}
func (r *procedureCallFollowingRows) Next(dest []driver.Value) error {
	if r.set == 1 && r.waitStage == "row" {
		if err := r.wait("Next"); err != nil {
			return err
		}
	}
	count := r.firstCount
	if r.set == 1 {
		count = 1
	}
	if r.read == count {
		r.fullyRead = r.set == 1
		return io.EOF
	}
	r.read++
	dest[0] = int64(r.read)
	if r.set == 1 {
		dest[0] = int64(99)
	}
	return nil
}

func TestMySQLProcedureCallDrainsFollowingResults(t *testing.T) {
	for _, limit := range []struct {
		name      string
		maxRows   int
		maxBytes  int
		wantRows  [][]any
		truncated bool
	}{
		{"first set only", 100, 1024, [][]any{{int64(1)}, {int64(2)}}, false},
		{"row limit", 1, 1024, [][]any{{int64(1)}}, true},
		{"byte limit", 100, 15, [][]any{}, true},
	} {
		t.Run(limit.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			release := make(chan struct{})
			close(release)
			following := newProcedureCallFollowingRows(ctx, 2, "", release)
			connector := &procedureCallConnector{statement: "CALL report(7)", newRows: func(context.Context) driver.Rows { return following }}
			db := sql.OpenDB(connector)
			defer db.Close()
			db.SetMaxOpenConns(1)
			result, err := runStatement(ctx, db, connector.statement, "mysql", limit.maxRows, 0, limit.maxBytes)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Rows, limit.wantRows) || result.Truncated != limit.truncated {
				t.Fatalf("first result set or limits changed: %#v", result)
			}
			if !following.fullyRead {
				t.Fatal("remaining procedure results were closed without draining through NextResultSet and Next")
			}
			next, err := runStatement(ctx, db, "SELECT 9", "mysql", 100, 0, 1024)
			if err != nil || !reflect.DeepEqual(next.Rows, [][]any{{int64(9)}}) {
				t.Fatalf("query after procedure: %#v, %v", next, err)
			}
		})
	}
}

func TestMySQLProcedureCallCancelsFollowingResults(t *testing.T) {
	for _, stage := range []string{"header", "row"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			following := newProcedureCallFollowingRows(ctx, 1, stage, release)
			connector := &procedureCallConnector{statement: "CALL report(7)", newRows: func(context.Context) driver.Rows { return following }}
			db := sql.OpenDB(connector)
			defer db.Close()
			db.SetMaxOpenConns(1)
			done := make(chan error, 1)
			go func() {
				_, err := runStatement(ctx, db, connector.statement, "mysql", 100, 0, 1024)
				done <- err
			}()
			select {
			case <-following.started:
				cancel()
			case err := <-done:
				t.Fatalf("procedure did not wait for its following result: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("procedure did not reach its following result")
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("following result cancellation: %v", err)
				}
			case <-time.After(2 * time.Second):
				unblock()
				<-done
				t.Fatal("following result ignored cancellation after Rows.Close disabled the watcher")
			}
			nextCtx, nextCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer nextCancel()
			next, err := runStatement(nextCtx, db, "SELECT 9", "mysql", 100, 0, 1024)
			if err != nil || !reflect.DeepEqual(next.Rows, [][]any{{int64(9)}}) {
				t.Fatalf("query after canceled procedure: %#v, %v", next, err)
			}
		})
	}
}

type procedureCallRows struct {
	columns []string
	values  []driver.Value
	read    bool
}

func (r *procedureCallRows) Columns() []string { return r.columns }
func (*procedureCallRows) Close() error        { return nil }
func (r *procedureCallRows) Next(dest []driver.Value) error {
	if r.read || len(r.values) == 0 {
		return io.EOF
	}
	r.read = true
	copy(dest, r.values)
	return nil
}

func TestMySQLProcedureCallResults(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		columns []string
		values  []driver.Value
		want    [][]any
	}{
		{name: "procedure SELECT", columns: []string{"value"}, values: []driver.Value{int64(7)}, want: [][]any{{int64(7)}}},
		{name: "procedure without a result set", columns: []string{}, want: [][]any{}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			const statement = "/* manual execution */ call `report`(7)"
			connector := &procedureCallConnector{statement: statement, columns: fixture.columns, values: fixture.values}
			db := sql.OpenDB(connector)
			defer db.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := runStatement(ctx, db, statement, "mysql", 100, 0, 1024)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Columns, fixture.columns) || !reflect.DeepEqual(result.Rows, fixture.want) {
				t.Fatalf("procedure result: got %#v, want columns %v and rows %v", result, fixture.columns, fixture.want)
			}
			if connector.queries.Load() != 1 || connector.execs.Load() != 0 {
				t.Fatalf("procedure dispatch: queries %d, execs %d", connector.queries.Load(), connector.execs.Load())
			}
			// A no-result CALL must also release its rows so another query can run.
			db.SetMaxOpenConns(1)
			next, err := runStatement(ctx, db, "SELECT 9", "mysql", 100, 0, 1024)
			if err != nil || !reflect.DeepEqual(next.Rows, [][]any{{int64(9)}}) {
				t.Fatalf("query after procedure: %#v, %v", next, err)
			}
		})
	}
}

func TestProcedureCallClassificationKeepsWriteRestrictions(t *testing.T) {
	for _, dialect := range []string{"mysql", "mariadb", "postgres", "sqlite", "clickhouse", ""} {
		t.Run(dialect, func(t *testing.T) {
			const statement = "CALL report(7)"
			wantRows := dialect == "mysql" || dialect == "mariadb"
			if got := returnsRows(statement, dialect); got != wantRows {
				t.Fatalf("returnsRows for %q: got %v, want %v", dialect, got, wantRows)
			}
			if ordinaryRead(statement, dialect) {
				t.Fatal("procedure calls must still require write permission")
			}
		})
	}
}
