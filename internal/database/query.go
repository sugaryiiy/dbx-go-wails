package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type QueryResult struct {
	Columns         []string `json:"columns"`
	ColumnTypes     []string `json:"column_types"`
	Rows            [][]any  `json:"rows"`
	AffectedRows    int64    `json:"affected_rows"`
	ExecutionTimeMS int64    `json:"execution_time_ms"`
	Truncated       bool     `json:"truncated,omitempty"`
	HasMore         bool     `json:"has_more"`
	StatementIndex  int      `json:"statement_index"`
	ExecutionError  bool     `json:"execution_error,omitempty"`
	Error           any      `json:"error,omitempty"`
}

func emptyResult() QueryResult {
	return QueryResult{Columns: []string{}, ColumnTypes: []string{}, Rows: [][]any{}}
}

type queryRunner interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (m *Manager) execute(ctx context.Context, path string, p map[string]any) (any, error) {
	id := str(p, "connectionId", "connection_id")
	database := str(p, "database")
	db, cfg, locked, e := m.pool(ctx, id, database)
	if e != nil {
		return nil, e
	}
	if boolean(p, "preserveExplicitTransaction") {
		return nil, unsupported("persistent explicit transactions")
	}
	if str(p, "resultSessionId") != "" {
		return nil, unsupported("retained result cursors")
	}
	mode := str(p, "executionMode")
	if mode != "" && mode != "simple" && mode != "postgres_read_only_transaction" {
		return nil, unsupported("query execution mode " + mode)
	}
	var statements []string
	if raw, ok := p["statements"]; ok {
		switch list := raw.(type) {
		case []any:
			for _, v := range list {
				s, ok := v.(string)
				if !ok {
					return nil, errors.New("statements must contain strings")
				}
				parts, err := splitStatements(s, cfg.Type)
				if err != nil {
					return nil, err
				}
				statements = append(statements, parts...)
			}
		case []string:
			for _, s := range list {
				parts, err := splitStatements(s, cfg.Type)
				if err != nil {
					return nil, err
				}
				statements = append(statements, parts...)
			}
		default:
			return nil, errors.New("statements must be an array")
		}
	} else {
		statements, e = splitStatements(str(p, "sql"), cfg.Type)
		if e != nil {
			return nil, e
		}
	}
	if path == "/api/query/execute" && len(statements) != 1 {
		return nil, errors.New("execute requires exactly one SQL statement; use execute-multi for scripts")
	}
	for _, s := range statements {
		switch firstWord(s, cfg.Type) {
		case "BEGIN", "START", "COMMIT", "ROLLBACK", "SAVEPOINT", "RELEASE", "END", "SET", "USE", "ATTACH", "DETACH":
			return nil, unsupported("persistent transaction/session statements; use execute-in-transaction for atomic batches")
		}
		if (locked || boolean(p, "readOnly") || mode == "postgres_read_only_transaction") && (!ordinaryRead(s, cfg.Type) || containsSQLWord(s, "INTO", cfg.Type)) {
			return nil, errors.New("writes are locked for this read-only or production connection; unlock writes explicitly before executing this statement")
		}
	}
	timeout := integer(p, cfg.QueryTimeout, "timeoutSecs")
	if timeout < 0 || timeout > 86400 {
		return nil, errors.New("timeoutSecs must be between 0 and 86400")
	}
	if timeout == 0 {
		timeout = 300
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	executionID := str(p, "executionId")
	if executionID != "" {
		m.mu.Lock()
		if _, exists := m.running[executionID]; exists {
			m.mu.Unlock()
			return nil, errors.New("execution id is already running")
		}
		m.running[executionID] = runningQuery{cancel: cancel, connectionID: id}
		m.mu.Unlock()
		defer func() { m.mu.Lock(); delete(m.running, executionID); m.mu.Unlock() }()
	}
	conn, e := db.Conn(ctx)
	if e != nil {
		return nil, redact(e, cfg)
	}
	defer conn.Close()
	var runner queryRunner = conn
	protected := locked || boolean(p, "readOnly") || mode == "postgres_read_only_transaction"
	if protected && cfg.Type == "sqlite" {
		if _, e = conn.ExecContext(ctx, "PRAGMA query_only = ON"); e != nil {
			return nil, e
		}
		defer func() {
			resetCtx, resetCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer resetCancel()
			if _, resetErr := conn.ExecContext(resetCtx, "PRAGMA query_only = OFF"); resetErr != nil {
				conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}()
	}
	transaction := path == "/api/query/execute-in-transaction" || boolean(p, "useTransaction") || protected && cfg.Type != "sqlite"
	if transaction && cfg.Type == "mysql" {
		for _, statement := range statements {
			switch mainVerb(statement, cfg.Type) {
			case "SELECT", "VALUES", "SHOW", "EXPLAIN", "DESCRIBE", "DESC", "INSERT", "UPDATE", "DELETE", "REPLACE":
			default:
				return nil, unsupported("MySQL statements that may implicitly commit an atomic transaction")
			}
		}
	}
	if transaction && boolean(p, "continueOnError") {
		return nil, unsupported("continueOnError inside a transaction")
	}
	var tx *sql.Tx
	if transaction {
		tx, e = conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: protected && cfg.Type != "sqlite"})
		if e != nil {
			return nil, redact(e, cfg)
		}
		defer tx.Rollback()
		runner = tx
	}
	schema := str(p, "schema")
	if schema == "" {
		schema = cfg.DefaultSchema
	}
	if cfg.Type == "postgres" && schema != "" {
		if _, e = runner.ExecContext(ctx, "SELECT set_config('search_path', $1, $2)", quoteIdentifier(schema, "postgres")+",pg_catalog", tx != nil); e != nil {
			return nil, redact(e, cfg)
		}
		// A transaction-local path restores itself on commit or rollback.
		// For autocommit, discard a connection if reset cannot be confirmed.
		if tx == nil {
			defer func() {
				resetCtx, resetCancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer resetCancel()
				if _, resetErr := conn.ExecContext(resetCtx, "RESET search_path"); resetErr != nil {
					conn.Raw(func(any) error { return driver.ErrBadConn })
				}
			}()
		}
	}
	maxRows := integer(p, 10000, "maxRows")
	if maxRows <= 0 {
		maxRows = 10000
	}
	if maxRows > 100000 {
		maxRows = 100000
	}
	pageSize := integer(p, 0, "pageSize")
	if pageSize > 0 && pageSize < maxRows {
		maxRows = pageSize
	}
	offset := integer(p, 0, "rowOffset")
	if offset < 0 || offset > 10000000 {
		return nil, errors.New("rowOffset must be between 0 and 10000000")
	}
	maxBytes := integer(p, 16*1024*1024, "maxResultBytes")
	if maxBytes <= 0 || maxBytes > 64*1024*1024 {
		maxBytes = 16 * 1024 * 1024
	}
	results := make([]QueryResult, 0, len(statements))
	aggregate := emptyResult()
	start := time.Now()
	for index, s := range statements {
		if offset > 0 && !ordinaryRead(s, cfg.Type) {
			return nil, errors.New("rowOffset requires a SELECT, VALUES or SHOW query")
		}
		result, err := runStatement(ctx, runner, s, cfg.Type, maxRows, offset, maxBytes)
		result.StatementIndex = index
		if err != nil {
			err = redact(err, cfg)
			if !boolean(p, "continueOnError") {
				return nil, err
			}
			result = emptyResult()
			result.StatementIndex = index
			result.ExecutionError = true
			result.Error = map[string]any{"message": err.Error(), "kind": "database"}
		}
		results = append(results, result)
		aggregate.AffectedRows += result.AffectedRows
	}
	if tx != nil {
		if e = tx.Commit(); e != nil {
			return nil, redact(e, cfg)
		}
	}
	aggregate.ExecutionTimeMS = time.Since(start).Milliseconds()
	switch path {
	case "/api/query/execute-multi":
		return results, nil
	case "/api/query/execute":
		return results[0], nil
	default:
		return aggregate, nil
	}
}
func runStatement(ctx context.Context, q queryRunner, statement, dialect string, maxRows, offset, maxBytes int) (QueryResult, error) {
	out := emptyResult()
	start := time.Now()
	mutation := false
	switch mainVerb(statement, dialect) {
	case "INSERT", "UPDATE", "DELETE", "REPLACE", "MERGE":
		mutation = true
	}
	if !returnsRows(statement, dialect) {
		result, e := q.ExecContext(ctx, statement)
		if e != nil {
			return out, e
		}
		if mutation {
			out.AffectedRows, _ = result.RowsAffected()
		}
		out.ExecutionTimeMS = time.Since(start).Milliseconds()
		return out, nil
	}
	rows, e := q.QueryContext(ctx, statement)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	out.Columns, e = rows.Columns()
	if e != nil {
		return out, e
	}
	types, _ := rows.ColumnTypes()
	for _, t := range types {
		out.ColumnTypes = append(out.ColumnTypes, t.DatabaseTypeName())
	}
	seen, totalBytes := 0, 0
	// MySQL Rows.Close drains packets after disabling its cancellation watcher.
	// Drain via Next instead: the request context then continues to interrupt it.
	// RETURNING mutations also need draining to report their full affected count.
	drain := dialect == "mysql" || mutation
	for rows.Next() {
		if mutation {
			out.AffectedRows++
		}
		if out.Truncated {
			if drain {
				continue
			}
			break
		}
		values := make([]any, len(out.Columns))
		dest := make([]any, len(values))
		for i := range values {
			dest[i] = &values[i]
		}
		if e = rows.Scan(dest...); e != nil {
			return out, e
		}
		if seen < offset {
			seen++
			continue
		}
		if len(out.Rows) >= maxRows {
			out.Truncated = true
			if drain {
				continue
			}
			break
		}
		for i, v := range values {
			typ := ""
			if i < len(out.ColumnTypes) {
				typ = out.ColumnTypes[i]
			}
			values[i] = normalizeValue(v, typ)
			if text, ok := values[i].(string); ok {
				totalBytes += len(text)
			} else {
				totalBytes += 16
			}
		}
		if totalBytes > maxBytes {
			out.Truncated = true
			if drain {
				continue
			}
			break
		}
		out.Rows = append(out.Rows, values)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	if e = rows.Close(); e != nil {
		return out, e
	}
	out.ExecutionTimeMS = time.Since(start).Milliseconds()
	return out, nil
}
func normalizeValue(v any, typ string) any {
	switch n := v.(type) {
	case int64:
		if n > 9007199254740991 || n < -9007199254740991 {
			return strconv.FormatInt(n, 10)
		}
		return n
	case uint64:
		if n > 9007199254740991 {
			return strconv.FormatUint(n, 10)
		}
		return n
	case float32:
		if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
			return strconv.FormatFloat(float64(n), 'g', -1, 32)
		}
		return n
	case []byte:
		upper := strings.ToUpper(typ)
		if strings.Contains(upper, "BLOB") || strings.Contains(upper, "BINARY") || upper == "BYTEA" || upper == "BIT" || upper == "GEOMETRY" {
			return "0x" + hex.EncodeToString(n)
		}
		return string(n)
	case time.Time:
		return n.Format(time.RFC3339Nano)
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return strconv.FormatFloat(n, 'g', -1, 64)
		}
		return n
	default:
		return v
	}
}
func quoteIdentifier(name, kind string) string {
	q := "\""
	if kind == "mysql" {
		q = "`"
	}
	return q + strings.ReplaceAll(name, q, q+q) + q
}

// ExecuteSQL is a bounded, permission-checked entry point for file import and
// export handlers. It uses precisely the same connection safety as HTTP queries.
func (m *Manager) ExecuteSQL(ctx context.Context, connectionID, database, statement string, maxRows int) (QueryResult, error) {
	value, e := m.execute(ctx, "/api/query/execute", map[string]any{"connectionId": connectionID, "database": database, "sql": statement, "maxRows": maxRows})
	if e != nil {
		return emptyResult(), e
	}
	result, ok := value.(QueryResult)
	if !ok {
		return emptyResult(), fmt.Errorf("unexpected query result")
	}
	return result, nil
}
