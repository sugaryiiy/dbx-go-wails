package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Limits align with the statements after SQL splitting. A null entry leaves
// that statement unrestricted, while the whole batch remains atomic.
func affectedRowLimits(p map[string]any, statements []string, dialect string) ([]*int64, error) {
	raw := p["maxAffectedRowsPerStatement"]
	if raw == nil {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, errors.New("maxAffectedRowsPerStatement must be an array of non-negative integers or null")
	}
	var limits []*int64
	if err = json.Unmarshal(data, &limits); err != nil || len(limits) != len(statements) {
		return nil, errors.New("maxAffectedRowsPerStatement must match the SQL statements and contain non-negative integers or null")
	}
	for i, limit := range limits {
		if limit != nil && *limit < 0 {
			return nil, errors.New("maxAffectedRowsPerStatement limits must be non-negative")
		}
		// Guarded batches are for data-grid mutations. Excluding transaction
		// controls and DDL also prevents MySQL's implicit commits from escaping
		// the rollback boundary.
		switch firstWord(statements[i], dialect) {
		case "INSERT", "UPDATE", "DELETE":
		default:
			return nil, unsupported("affected row guards on statements other than INSERT, UPDATE or DELETE")
		}
	}
	if boolean(p, "continueOnError") {
		return nil, unsupported("continueOnError with affected row guards")
	}
	if integer(p, 0, "pageSize") != 0 || integer(p, 0, "rowOffset") != 0 {
		return nil, errors.New("affected row guards cannot be combined with result pagination")
	}
	return limits, nil
}

const mysqlGuardIdentifier = "(?:`(?:``|[^`\x00])+`|[\\pL_$][\\pL\\pN_$]*)"
const mysqlGuardTable = "(" + mysqlGuardIdentifier + "(?:\\s*\\.\\s*" + mysqlGuardIdentifier + ")?)"

var mysqlGuardTargets = map[string]*regexp.Regexp{
	"UPDATE": regexp.MustCompile("(?is)^\\s*UPDATE\\s+" + mysqlGuardTable + "\\s+SET\\b"),
	"DELETE": regexp.MustCompile("(?is)^\\s*DELETE\\s+FROM\\s+" + mysqlGuardTable + "\\s+WHERE\\b"),
	"INSERT": regexp.MustCompile("(?is)^\\s*INSERT\\s+INTO\\s+" + mysqlGuardTable + "(?:\\s*\\(|\\s+VALUES\\b)"),
}
var mysqlGuardIdentifierPattern = regexp.MustCompile(mysqlGuardIdentifier)

func mysqlAffectedRowTarget(statement string) (database, table string, err error) {
	pattern := mysqlGuardTargets[firstWord(statement, "mysql")]
	if pattern == nil {
		return "", "", unsupported("atomic MySQL guarded saves require direct table mutations")
	}
	match := pattern.FindStringSubmatch(statement)
	if len(match) != 2 {
		return "", "", unsupported("atomic MySQL guarded saves require direct table mutations")
	}
	parts := mysqlGuardIdentifierPattern.FindAllString(match[1], -1)
	for i, part := range parts {
		if strings.HasPrefix(part, "`") {
			parts[i] = strings.ReplaceAll(part[1:len(part)-1], "``", "`")
		}
	}
	if len(parts) == 2 {
		return parts[0], parts[1], nil
	}
	return "", parts[0], nil
}

func validateMySQLAffectedRowTables(ctx context.Context, q queryRunner, statements []string) error {
	type target struct{ database, table string }
	targets := make([]target, 0, len(statements))
	defaultDatabase := ""
	for _, statement := range statements {
		database, table, err := mysqlAffectedRowTarget(statement)
		if err != nil {
			return err
		}
		if database == "" {
			if defaultDatabase == "" {
				rows, err := records(ctx, q, "SELECT DATABASE() AS name")
				if err != nil {
					return err
				}
				if len(rows) != 1 || valueString(rows[0]["name"]) == "" {
					return errors.New("atomic MySQL guarded save requires a selected database")
				}
				defaultDatabase = valueString(rows[0]["name"])
			}
			database = defaultDatabase
		}
		targets = append(targets, target{database, table})
	}
	checked := map[target]bool{}
	for _, t := range targets {
		if checked[t] {
			continue
		}
		qualified := quoteIdentifier(t.database, "mysql") + "." + quoteIdentifier(t.table, "mysql")
		// Opening the table in this transaction holds its metadata lock until
		// commit, so ALTER TABLE cannot switch engines after this check.
		if _, err := records(ctx, q, "SELECT 1 FROM "+qualified+" WHERE 1=0"); err != nil {
			return err
		}
		rows, err := records(ctx, q, "SELECT ENGINE FROM information_schema.tables WHERE table_schema=? AND table_name=?", t.database, t.table)
		if err != nil {
			return err
		}
		if len(rows) != 1 || !strings.EqualFold(valueString(rows[0]["ENGINE"]), "InnoDB") {
			return unsupported("atomic MySQL guarded saves require InnoDB tables: " + qualified)
		}
		checked[t] = true
	}
	return nil
}

type affectedRowSavepoint struct {
	conn    *sql.Conn
	session *querySession
	name    string
	done    bool
}

func beginAffectedRowSavepoint(ctx context.Context, conn *sql.Conn, session *querySession) (*affectedRowSavepoint, error) {
	s := &affectedRowSavepoint{conn: conn, session: session, name: "dbx_guard_" + rand.Text()}
	if _, err := conn.ExecContext(ctx, "SAVEPOINT "+s.name); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *affectedRowSavepoint) release(ctx context.Context) error {
	if _, err := s.conn.ExecContext(ctx, "RELEASE SAVEPOINT "+s.name); err != nil {
		return err
	}
	s.done = true
	return nil
}

func (s *affectedRowSavepoint) rollback() error {
	if s.done {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := s.conn.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+s.name)
	if err == nil {
		_, err = s.conn.ExecContext(ctx, "RELEASE SAVEPOINT "+s.name)
	}
	if err != nil {
		// A failed rollback cannot leave a partially applied grid save in an
		// otherwise usable manual transaction.
		s.session.closeLocked()
		return fmt.Errorf("could not roll back the guarded batch; transaction session was closed: %w", err)
	}
	s.done = true
	return nil
}
