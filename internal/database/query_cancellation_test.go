package database

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

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
