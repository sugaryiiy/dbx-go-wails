package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"modernc.org/sqlite"
)

var sqliteCancellationFixtureID atomic.Uint64

// Register before opening the database: the anchor function signals from
// sqlite3_step, rather than merely from manager execution registration.
func sqliteCancellationQuery(t *testing.T) (string, <-chan struct{}) {
	t.Helper()
	started := make(chan struct{})
	var signal sync.Once
	name := fmt.Sprintf("dbx_cancel_started_%d", sqliteCancellationFixtureID.Add(1))
	if err := sqlite.RegisterScalarFunction(name, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		signal.Do(func() { close(started) })
		return int64(0), nil
	}); err != nil {
		t.Fatal(err)
	}
	query := fmt.Sprintf("WITH RECURSIVE counter(x) AS (VALUES(%s()) UNION ALL SELECT x+1 FROM counter WHERE x<1000000000) SELECT sum(x) FROM counter", name)
	return query, started
}

type cancellationQueryRunner struct {
	called bool
}

func (r *cancellationQueryRunner) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	r.called = true
	return nil, errors.New("unexpected query dispatch")
}

func (r *cancellationQueryRunner) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	r.called = true
	return nil, errors.New("unexpected statement dispatch")
}

func TestRunStatementCancellationBeforeDispatch(t *testing.T) {
	for _, statement := range []string{"SELECT 1", "INSERT INTO important VALUES(1)"} {
		t.Run(statement, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			runner := new(cancellationQueryRunner)
			if _, err := runStatement(ctx, runner, statement, "sqlite", 1, 0, 1024); !errors.Is(err, context.Canceled) {
				t.Fatalf("expected cancellation before dispatch, got %v", err)
			}
			if runner.called {
				t.Fatal("cancelled operation reached the database runner")
			}
		})
	}
}
