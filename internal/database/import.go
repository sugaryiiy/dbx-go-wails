package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ImportRows atomically inserts parameterized values, preserving arbitrary
// strings, binary values, SQL NULLs and integers without interpolating literals.
func (m *Manager) ImportRows(ctx context.Context, payload map[string]any, columns []string, rows [][]any) (int64, error) {
	db, c, locked, e := m.pool(ctx, str(payload, "connectionId", "connection_id"), str(payload, "database"))
	if e != nil {
		return 0, e
	}
	if locked {
		return 0, errors.New("writes are locked for this read-only or production connection")
	}
	if len(columns) == 0 {
		return 0, errors.New("at least one import column is required")
	}
	if len(columns) > 2000 {
		return 0, errors.New("too many import columns")
	}
	table := str(payload, "table")
	if table == "" {
		return 0, errors.New("import table is required")
	}
	mode := str(payload, "mode")
	if mode != "" && mode != "append" && mode != "truncate" {
		return 0, unsupported("import mode " + mode)
	}
	schema := str(payload, "schema")
	if schema == "" {
		if c.Type == "postgres" {
			schema = c.DefaultSchema
			if schema == "" {
				schema = "public"
			}
		} else if c.Type == "mysql" {
			schema = str(payload, "database")
			if schema == "" {
				schema = c.Database
			}
		} else {
			schema = "main"
		}
	}
	qualified := quoteIdentifier(schema, c.Type) + "." + quoteIdentifier(table, c.Type)
	quoted := make([]string, len(columns))
	params := make([]string, len(columns))
	seen := map[string]bool{}
	for i, column := range columns {
		if column == "" || seen[column] {
			return 0, errors.New("import column names must be nonempty and unique")
		}
		seen[column] = true
		quoted[i] = quoteIdentifier(column, c.Type)
		params[i] = "?"
		if c.Type == "postgres" {
			params[i] = fmt.Sprintf("$%d", i+1)
		}
	}
	timeout := c.QueryTimeout
	if timeout <= 0 {
		timeout = 300
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return 0, redact(e, c)
	}
	defer tx.Rollback()
	if c.Type == "mysql" {
		var engine string
		if e = tx.QueryRowContext(ctx, "SELECT ENGINE FROM information_schema.tables WHERE table_schema=? AND table_name=?", schema, table).Scan(&engine); e != nil {
			return 0, redact(e, c)
		}
		if !strings.EqualFold(engine, "InnoDB") {
			return 0, unsupported("atomic MySQL imports require an InnoDB table")
		}
	}
	if mode == "truncate" {
		if _, e = tx.ExecContext(ctx, "DELETE FROM "+qualified); e != nil {
			return 0, redact(e, c)
		}
	}
	stmt, e := tx.PrepareContext(ctx, "INSERT INTO "+qualified+" ("+strings.Join(quoted, ",")+") VALUES ("+strings.Join(params, ",")+")")
	if e != nil {
		return 0, redact(e, c)
	}
	defer stmt.Close()
	var count int64
	for index, row := range rows {
		if len(row) != len(columns) {
			return 0, fmt.Errorf("import row %d has %d cells, expected %d", index+1, len(row), len(columns))
		}
		result, err := stmt.ExecContext(ctx, row...)
		if err != nil {
			return 0, redact(fmt.Errorf("import row %d: %w", index+1, err), c)
		}
		n, _ := result.RowsAffected()
		count += n
	}
	if e = tx.Commit(); e != nil {
		return 0, redact(e, c)
	}
	return count, nil
}
