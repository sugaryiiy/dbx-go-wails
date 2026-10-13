package database

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func sqliteSchemaRevision(ctx context.Context, q queryRunner, schema, table string) (string, error) {
	items, err := records(ctx, q, "SELECT type,name,tbl_name,sql FROM "+quoteIdentifier(schema, "sqlite")+".sqlite_schema WHERE name=? OR tbl_name=? ORDER BY type,name", table, table)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "", errors.New("SQLite table does not exist")
	}
	b, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func (m *Manager) SQLiteSchemaRevision(ctx context.Context, id, database, schema, table string) (string, error) {
	db, c, _, err := m.pool(ctx, id, database)
	if err != nil {
		return "", err
	}
	if c.Type != "sqlite" {
		return "", errors.New("table rebuild requires SQLite")
	}
	if schema == "" {
		schema = "main"
	}
	return sqliteSchemaRevision(ctx, db, schema, table)
}

// SQLiteRebuild holds a write reservation while checking the preview revision,
// copying data and validating all foreign keys. No partial schema is committed.
func (m *Manager) SQLiteRebuild(ctx context.Context, id, database, schema, table, expectedRevision string, statements []string) (QueryResult, error) {
	out := emptyResult()
	start := time.Now()
	db, c, locked, err := m.pool(ctx, id, database)
	if err != nil {
		return out, err
	}
	if c.Type != "sqlite" {
		return out, errors.New("table rebuild requires SQLite")
	}
	if locked {
		return out, errors.New("writes are locked for this connection")
	}
	if schema == "" {
		schema = "main"
	}
	if schema != "main" && schema != "temp" {
		return out, unsupported("SQLite attached schema rebuild")
	}
	if table == "" || expectedRevision == "" || len(statements) == 0 || len(statements) > 512 {
		return out, errors.New("table, preview revision and bounded rebuild statements are required")
	}
	for _, s := range statements {
		parts, e := splitStatements(s, "sqlite")
		if e != nil {
			return out, e
		}
		if len(parts) != 1 {
			return out, errors.New("each rebuild entry must be one statement")
		}
		switch firstWord(s, "sqlite") {
		case "CREATE", "DROP", "ALTER", "INSERT", "UPDATE", "DELETE":
		default:
			return out, errors.New("rebuild statements must be schema or data mutations")
		}
		if strings.ContainsRune(s, 0) {
			return out, errors.New("rebuild statement contains NUL")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return out, err
	}
	defer conn.Close()
	var foreignKeys int
	if err = conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return out, err
	}
	if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return out, err
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_, _ = conn.ExecContext(cleanup, "ROLLBACK")
		if _, e := conn.ExecContext(cleanup, fmt.Sprintf("PRAGMA foreign_keys=%d", foreignKeys)); e != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return out, err
	}
	revision, err := sqliteSchemaRevision(ctx, conn, schema, table)
	if err != nil {
		return out, err
	}
	if revision != expectedRevision {
		return out, errors.New("table structure changed after preview; refresh and preview the changes again")
	}
	for _, s := range statements {
		result, e := conn.ExecContext(ctx, s)
		if e != nil {
			return out, e
		}
		if firstWord(s, "sqlite") == "INSERT" || firstWord(s, "sqlite") == "UPDATE" || firstWord(s, "sqlite") == "DELETE" {
			n, _ := result.RowsAffected()
			out.AffectedRows += n
		}
	}
	violations, err := records(ctx, conn, "PRAGMA "+quoteIdentifier(schema, "sqlite")+".foreign_key_check")
	if err != nil {
		return out, err
	}
	if len(violations) > 0 {
		return out, fmt.Errorf("rebuild would violate %d foreign keys; changes rolled back", len(violations))
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return out, err
	}
	out.ExecutionTimeMS = time.Since(start).Milliseconds()
	return out, nil
}
