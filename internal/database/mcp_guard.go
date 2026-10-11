package database

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
)

// ValidateMCPSQL accepts the SQL subset for which MCP can enforce read/write
// classification. DB-native read-only transactions remain the final write guard.
// Session controls, external I/O, user-defined functions and executable routines
// are deliberately unavailable even with the high-risk SQL switch enabled.
func ValidateMCPSQL(source, dialect string, readOnly, allowDDL bool) ([]string, error) {
	if dialect == "mariadb" {
		dialect = "mysql"
	}
	statements, err := splitStatements(source, dialect)
	if err != nil {
		return nil, err
	}
	if len(statements) == 0 || len(statements) > 100 {
		return nil, errors.New("MCP SQL requires 1–100 statements")
	}
	safeFuncs := strings.Fields("ABS AVG COUNT SUM TOTAL MIN MAX ROUND LOWER UPPER LENGTH CHAR_LENGTH CHARACTER_LENGTH SUBSTR SUBSTRING TRIM LTRIM RTRIM REPLACE INSTR POSITION COALESCE IFNULL NULLIF IIF IF CONCAT CONCAT_WS LEFT RIGHT REVERSE REPEAT LPAD RPAD HEX UNHEX QUOTE TYPEOF CAST CONVERT STRFTIME DATE TIME DATETIME JULIANDAY UNIXEPOCH TIMEDIFF NOW CURRENT_DATE CURRENT_TIME CURRENT_TIMESTAMP EXTRACT DATE_TRUNC DATE_PART TO_CHAR TO_DATE TO_TIMESTAMP STR_TO_DATE DATE_FORMAT YEAR MONTH DAY HOUR MINUTE SECOND FLOOR CEIL CEILING MOD POWER SQRT EXP LN LOG SIGN RANDOM RANDOMBLOB ZEROBLOB UNICODE CHAR JSON JSON_ARRAY JSON_OBJECT JSON_EXTRACT JSON_TYPE JSON_VALID JSON_QUOTE JSON_ARRAY_LENGTH JSON_GROUP_ARRAY JSON_GROUP_OBJECT JSON_BUILD_OBJECT JSON_BUILD_ARRAY JSON_AGG JSONB_AGG JSONB_BUILD_OBJECT JSONB_BUILD_ARRAY JSONB_ARRAY_LENGTH GROUP_CONCAT STRING_AGG ARRAY_AGG ROW_NUMBER RANK DENSE_RANK LAG LEAD FIRST_VALUE LAST_VALUE NTH_VALUE NTILE PERCENT_RANK CUME_DIST SQLITE_VERSION VERSION DATABASE CURRENT_DATABASE CURRENT_SCHEMA")
	syntax := strings.Fields("IN NOT EXISTS AS VALUES SELECT DISTINCT OVER PARTITION FILTER CHECK KEY UNIQUE PRIMARY REFERENCES ON USING INTEGER INT BIGINT SMALLINT TINYINT REAL DOUBLE NUMERIC DECIMAL VARCHAR CHAR CHARACTER TEXT BLOB BOOLEAN BIT DATE DATETIME TIME TIMESTAMP TIMESTAMPTZ BYTEA JSON JSONB")
	allowed := map[string]bool{}
	for _, s := range append(safeFuncs, syntax...) {
		allowed[s] = true
	}
	for _, statement := range statements {
		verb := mainVerb(statement, dialect)
		switch verb {
		case "SELECT", "VALUES":
		case "INSERT", "UPDATE", "DELETE", "REPLACE":
			if readOnly {
				return nil, errors.New("MCP policy is read-only")
			}
		case "CREATE", "ALTER", "DROP", "TRUNCATE":
			if readOnly || !allowDDL {
				return nil, errors.New("MCP DDL requires explicit high-risk SQL permission")
			}
		default:
			return nil, errors.New("SQL statement is outside the supported MCP subset")
		}
		tokens, err := mcpSQLTokens(statement, dialect)
		if err != nil {
			return nil, err
		}
		if (verb == "CREATE" || verb == "ALTER" || verb == "DROP") && mcpEventDDL(tokens) {
			return nil, errors.New("Scheduled event DDL is unavailable through Go MCP")
		}
		if !allowDDL && (verb == "UPDATE" || verb == "DELETE") && !mcpGuardedPredicate(tokens) {
			return nil, errors.New("UPDATE/DELETE requires a simple unquoted column = literal WHERE condition, or explicit high-risk SQL permission")
		}
		if !allowDDL {
			depth := 0
			for _, token := range tokens {
				if token == "(" {
					depth++
				}
				if token == ")" {
					depth--
				}
				u := strings.ToUpper(token)
				if depth > 0 && (u == "UPDATE" || u == "DELETE" || u == "INSERT" || u == "MERGE") {
					return nil, errors.New("Nested data-changing statements require high-risk SQL permission")
				}
			}
		}
		for i, t := range tokens {
			upper := strings.ToUpper(t)
			if dialect == "sqlite" && strings.Trim(upper, "\"") == "TEMP" && i+1 < len(tokens) && tokens[i+1] == "." {
				return nil, errors.New("MCP SQLite queries are restricted to the main database")
			}
			if verb == "CREATE" || verb == "ALTER" || verb == "DROP" || verb == "TRUNCATE" {
				switch upper {
				case "TEMP", "TEMPORARY", "TRIGGER", "PROCEDURE", "FUNCTION", "EXTENSION", "SERVER", "FOREIGN", "VIRTUAL", "TABLESPACE", "DIRECTORY", "DATABASE", "SCHEMA", "ROLE", "USER":
					return nil, errors.New("Administrative, external-I/O and routine DDL is unavailable through Go MCP")
				}
			}
			if upper == "OUTFILE" || upper == "DUMPFILE" {
				return nil, errors.New("External file access is unavailable through MCP")
			}

			if upper == "INTO" && (verb == "SELECT" || verb == "VALUES") {
				return nil, errors.New("SELECT INTO is unavailable through MCP")
			}
			if readOnly && (upper == "INSERT" || upper == "UPDATE" || upper == "DELETE" || upper == "REPLACE" && !(i+1 < len(tokens) && tokens[i+1] == "(") || upper == "MERGE") {
				return nil, errors.New("MCP policy is read-only")
			}
			if i+1 < len(tokens) && tokens[i+1] == "(" && mcpIdentifier(t) {
				// Table names preceding a column list are not function calls.
				prev := ""
				if i > 0 {
					prev = strings.ToUpper(tokens[i-1])
				}
				tableList := (verb == "INSERT" || verb == "REPLACE") && prev == "INTO" || (verb == "CREATE" || verb == "ALTER") && (prev == "TABLE" || prev == "ON" || prev == "REFERENCES" || prev == "EXISTS")
				if i >= 3 && prev == "." {
					qualifierPrev := strings.ToUpper(tokens[i-3])
					tableList = (verb == "INSERT" || verb == "REPLACE") && qualifierPrev == "INTO" || (verb == "CREATE" || verb == "ALTER") && (qualifierPrev == "TABLE" || qualifierPrev == "ON" || qualifierPrev == "REFERENCES" || qualifierPrev == "EXISTS")
				}
				if !allowed[upper] && !tableList {
					return nil, errors.New("MCP cannot establish that this function is safe; user-defined and unrecognized functions are not supported")
				}
				if i > 0 && tokens[i-1] == "." && !tableList {
					return nil, errors.New("Schema-qualified functions are not supported through MCP")
				}
			}
		}
	}
	return statements, nil
}

// Scheduled events can execute SQL after the MCP request and its permissions
// expire. They are not ordinary DDL, even when high-risk SQL is allowed.
func mcpEventDDL(tokens []string) bool {
	if len(tokens) < 2 {
		return false
	}
	start := 1
	if len(tokens) > 3 && strings.EqualFold(tokens[1], "OR") && strings.EqualFold(tokens[2], "REPLACE") {
		start = 3
	}
	if start < len(tokens) {
		switch strings.ToUpper(tokens[start]) {
		case "UNIQUE", "UNLOGGED", "MATERIALIZED":
			start++
		}
	}
	if start < len(tokens) {
		switch strings.ToUpper(tokens[start]) {
		case "TABLE", "VIEW", "INDEX", "SEQUENCE":
			return false // Ordinary object names/columns may be named event.
		}
	}
	for _, token := range tokens[start:] {
		if strings.EqualFold(token, "EVENT") {
			return true
		}
	}
	return false
}

func mcpIdentifier(s string) bool {
	if s == "" {
		return false
	}
	c := rune(s[0])
	return unicode.IsLetter(c) || c == '_' || c == '"' || s[0] >= 128
}
func mcpSQLTokens(s, dialect string) ([]string, error) {
	// Reject syntax that would require connection-specific string-escape modes.
	if strings.ContainsAny(s, "\\$") {
		return nil, errors.New("MCP SQL does not support backslash or dollar-quoted syntax")
	}
	out := []string{}
	for i := 0; i < len(s); {
		c := s[i]
		if sqlSpace(c) {
			i++
			continue
		}
		if i+1 < len(s) && s[i:i+2] == "--" && (dialect != "mysql" || i+2 == len(s) || s[i+2] <= 32) || c == '#' && dialect == "mysql" {
			for i < len(s) && s[i] != '\n' && s[i] != '\r' {
				i++
			}
			continue
		}
		if i+1 < len(s) && s[i:i+2] == "/*" {
			depth := 1
			i += 2
			for i < len(s) && depth > 0 {
				if i+1 < len(s) && s[i:i+2] == "/*" {
					depth++
					i += 2
				} else if i+1 < len(s) && s[i:i+2] == "*/" {
					depth--
					i += 2
				} else {
					i++
				}
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' || c == '[' && dialect == "sqlite" {
			end := c
			if c == '[' {
				end = ']'
			}
			i++
			var b strings.Builder
			for i < len(s) {
				if s[i] == end {
					if i+1 < len(s) && s[i+1] == end {
						b.WriteByte(end)
						i += 2
						continue
					}
					i++
					break
				}
				b.WriteByte(s[i])
				i++
			}
			if c == '\'' {
				out = append(out, "?")
			} else {
				out = append(out, "\""+b.String()+"\"")
			}
			continue
		}
		if sqlIdentifierByte(c) || c >= 128 {
			j := i + 1
			for j < len(s) && (sqlIdentifierByte(s[j]) || s[j] >= 128) {
				j++
			}
			out = append(out, s[i:j])
			i = j
			continue
		}
		out = append(out, string(c))
		i++
	}
	return out, nil
}

// Permit a deliberately small, provably non-tautological predicate subset when
// high-risk SQL is disabled. Complex expressions must opt in explicitly instead
// of being guessed safe by the presence of a WHERE keyword.
func mcpGuardedPredicate(tokens []string) bool {
	depth, where := 0, -1
	for i, t := range tokens {
		if t == "(" {
			depth++
		}
		if t == ")" {
			depth--
		}
		if depth == 0 && strings.EqualFold(t, "WHERE") {
			where = i + 1
			break
		}
	}
	if where < 0 || where >= len(tokens) {
		return false
	}
	i := where
	for {
		if i >= len(tokens) || !mcpIdentifier(tokens[i]) || strings.HasPrefix(tokens[i], "\"") {
			return false
		}
		switch strings.ToUpper(tokens[i]) {
		case "TRUE", "FALSE", "NULL", "CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP", "CURRENT_USER", "SESSION_USER", "SYSTEM_USER", "USER":
			return false
		}
		i++
		if i < len(tokens) && tokens[i] == "." {
			i++
			if i >= len(tokens) || !mcpIdentifier(tokens[i]) || strings.HasPrefix(tokens[i], "\"") {
				return false
			}
			i++
		}
		if i >= len(tokens) || tokens[i] != "=" {
			return false
		}
		i++
		if i < len(tokens) && (tokens[i] == "-" || tokens[i] == "+") {
			i++
		}
		if i >= len(tokens) {
			return false
		}
		v := tokens[i]
		i++
		if v != "?" && !strings.EqualFold(v, "NULL") && !strings.EqualFold(v, "TRUE") && !strings.EqualFold(v, "FALSE") {
			if _, err := strconv.ParseFloat(v, 64); err != nil {
				return false
			}
		}
		if i == len(tokens) {
			return true
		}
		if !strings.EqualFold(tokens[i], "AND") {
			return false
		}
		i++
	}
}
