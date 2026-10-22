package server

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/sugaryiiy/magpiedb/internal/database"
)

func adminObjectKind(value string) (string, error) {
	kind := strings.ReplaceAll(strings.ToUpper(value), "_", " ")
	switch kind {
	case "TABLE", "VIEW", "MATERIALIZED VIEW", "FUNCTION", "PROCEDURE", "SEQUENCE", "TRIGGER", "INDEX", "TYPE", "DOMAIN", "EXTENSION", "EVENT TRIGGER", "EVENT":
		return kind, nil
	}
	return "", errors.New("Unsupported database object kind")
}
func (s *Server) handleAdvancedAdmin(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	switch r.URL.Path {
	case "/api/query/build-rename-object-sql", "/api/query/build-rename-database-sql", "/api/query/build-rename-database-preflight-sql", "/api/query/build-create-database-sql", "/api/query/build-sqlite-attach-database-sql", "/api/query/build-duckdb-attach-database-sql", "/api/query/build-drop-object-sql", "/api/query/build-drop-table-sql", "/api/query/build-drop-table-child-object-sql", "/api/query/build-empty-table-sql", "/api/query/build-truncate-table-sql", "/api/query/build-vacuum-table-sql", "/api/query/build-mysql-auto-increment-sql", "/api/query/build-drop-database-sql", "/api/query/build-create-schema-sql", "/api/query/build-update-database-properties-sql", "/api/query/build-drop-schema-sql", "/api/query/build-duplicate-table-structure-sql", "/api/query/build-copy-table-data-sql", "/api/query/build-executable-object-source-statements", "/api/query/build-executable-object-source-sql", "/api/query/build-editable-object-source", "/api/query/build-routine-rename-object-source-statements", "/api/query/build-view-ddl-sql":
	default:
		return false
	}
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return true
	}
	o := mapValue(p, "options")
	if strings.Contains(r.URL.Path, "object-source") || strings.HasSuffix(r.URL.Path, "view-ddl-sql") {
		o = mapValue(p, "input")
	}
	if strings.Contains(r.URL.Path, "rename-database") {
		o = p
	}
	result, err := advancedAdminSQL(r.URL.Path, o)
	if err != nil {
		writeError(w, 400, err.Error())
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
func advancedAdminSQL(path string, o map[string]any) (any, error) {
	kind, err := compareKind(str(o, "databaseType"))
	if err != nil {
		return nil, err
	}
	if path == "/api/query/build-duckdb-attach-database-sql" {
		kind = "sqlite"
	}
	schema := str(o, "schema")
	table := str(o, "tableName")
	target := ""
	if table != "" {
		target, err = compareTable(kind, schema, table)
		if err != nil {
			return nil, err
		}
	}
	switch path {
	case "/api/query/build-rename-object-sql":
		object, err := adminObjectKind(str(o, "objectType"))
		if err != nil {
			return nil, err
		}
		old, err := compareTable(kind, schema, str(o, "oldName"))
		if err != nil {
			return nil, err
		}
		if err = compareIdentifier(str(o, "newName")); err != nil {
			return nil, err
		}
		if kind == "mysql" && (object == "TABLE" || object == "VIEW") {
			new, err := compareTable(kind, schema, str(o, "newName"))
			if err != nil {
				return nil, err
			}
			return "RENAME TABLE " + old + " TO " + new + ";", nil
		}
		if kind == "sqlite" && object != "TABLE" {
			return nil, errors.New("SQLite supports only table renaming")
		}
		if object != "TABLE" && object != "VIEW" && object != "MATERIALIZED VIEW" && object != "SEQUENCE" {
			return nil, errors.New("This object requires a source-backed rename")
		}
		return "ALTER " + object + " " + old + " RENAME TO " + quoteSQLName(str(o, "newName"), kind) + ";", nil
	case "/api/query/build-rename-database-preflight-sql":
		if kind != "postgres" {
			return nil, errors.New("Database rename requires PostgreSQL")
		}
		return "SELECT pid, usename, application_name, state FROM pg_stat_activity WHERE datname = " + schemaLiteral(str(o, "databaseName"), kind) + " AND pid <> pg_backend_pid();", nil
	case "/api/query/build-rename-database-sql":
		if kind != "postgres" {
			return nil, errors.New("Database rename requires PostgreSQL")
		}
		for _, name := range []string{str(o, "oldName"), str(o, "newName")} {
			if err = compareIdentifier(name); err != nil {
				return nil, err
			}
		}
		sql := ""
		if boolValue(o, "terminateConnections") {
			sql = "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = " + schemaLiteral(str(o, "oldName"), kind) + " AND pid <> pg_backend_pid();\n"
		}
		return sql + "ALTER DATABASE " + quoteSQLName(str(o, "oldName"), kind) + " RENAME TO " + quoteSQLName(str(o, "newName"), kind) + ";", nil
	case "/api/query/build-create-database-sql", "/api/query/build-create-schema-sql", "/api/query/build-drop-database-sql", "/api/query/build-drop-schema-sql":
		name := str(o, "name")
		if err = compareIdentifier(name); err != nil {
			return nil, err
		}
		if kind == "sqlite" {
			return nil, errors.New("SQLite databases are created by opening a file and schemas by attaching a database")
		}
		object := "DATABASE"
		if strings.Contains(path, "schema") {
			object = "SCHEMA"
		}
		if target := str(o, "target"); target == "schema" {
			object = "SCHEMA"
		}
		if str(o, "target") == "catalog" || str(o, "target") == "namespace" {
			return nil, errors.New("Catalog or namespace creation is unavailable for this database")
		}
		operation := "CREATE "
		if strings.Contains(path, "drop-") {
			operation = "DROP "
		}
		sql := operation + object + " " + quoteSQLName(name, kind)
		if operation == "CREATE " && kind == "mysql" {
			for _, pair := range [][2]string{{"charset", "CHARACTER SET"}, {"collation", "COLLATE"}} {
				value := str(o, pair[0])
				if value != "" {
					if !schemaWordPattern.MatchString(value) {
						return nil, errors.New("Invalid database charset or collation")
					}
					sql += " " + pair[1] + " " + value
				}
			}
		}
		return sql + ";", nil
	case "/api/query/build-sqlite-attach-database-sql", "/api/query/build-duckdb-attach-database-sql":
		if err = compareIdentifier(str(o, "name")); err != nil {
			return nil, err
		}
		if strings.ContainsRune(str(o, "path"), 0) || str(o, "path") == "" {
			return nil, errors.New("Database path is required")
		}
		return "ATTACH DATABASE " + schemaLiteral(str(o, "path"), "sqlite") + " AS " + quoteSQLName(str(o, "name"), "sqlite") + ";", nil
	case "/api/query/build-drop-object-sql":
		object, err := adminObjectKind(str(o, "objectType"))
		if err != nil {
			return nil, err
		}
		target, err := compareTable(kind, schema, str(o, "name"))
		if err != nil {
			return nil, err
		}
		if kind == "sqlite" && object != "TABLE" && object != "VIEW" && object != "TRIGGER" && object != "INDEX" {
			return nil, errors.New("This object does not exist in SQLite")
		}
		if object == "FUNCTION" || object == "PROCEDURE" {
			if kind == "postgres" {
				signature := str(o, "signature")
				if signature == "" {
					signature = "()"
				}
				if !strings.HasPrefix(signature, "(") {
					signature = "(" + signature + ")"
				}
				if err = schemaExpression("NULL"+signature, kind); err != nil {
					return nil, err
				}
				target += signature
			}
		}
		return "DROP " + object + " " + target + ";", nil
	case "/api/query/build-drop-table-sql":
		if target == "" {
			return nil, errors.New("Table name is required")
		}
		suffix := ""
		if kind == "postgres" && boolValue(o, "cascade") {
			suffix = " CASCADE"
		}
		return "DROP TABLE " + target + suffix + ";", nil
	case "/api/query/build-empty-table-sql":
		if target == "" {
			return nil, errors.New("Table name is required")
		}
		return "DELETE FROM " + target + ";", nil
	case "/api/query/build-truncate-table-sql":
		if target == "" {
			return nil, errors.New("Table name is required")
		}
		if kind == "sqlite" {
			return "DELETE FROM " + target + ";", nil
		}
		suffix := ""
		if kind == "postgres" && boolValue(o, "cascade") {
			suffix = " CASCADE"
		}
		return "TRUNCATE TABLE " + target + suffix + ";", nil
	case "/api/query/build-vacuum-table-sql":
		if kind == "mysql" {
			return "OPTIMIZE TABLE " + target + ";", nil
		}
		if kind == "sqlite" {
			return "VACUUM;", nil
		}
		options := []string{}
		if boolValue(o, "full") {
			options = append(options, "FULL")
		}
		if boolValue(o, "analyze") {
			options = append(options, "ANALYZE")
		}
		sql := "VACUUM "
		if len(options) > 0 {
			sql += "(" + strings.Join(options, ", ") + ") "
		}
		return sql + target + ";", nil
	case "/api/query/build-mysql-auto-increment-sql":
		if kind != "mysql" {
			return nil, errors.New("AUTO_INCREMENT counters require MySQL")
		}
		value := str(o, "value")
		if !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(value) {
			return nil, errors.New("AUTO_INCREMENT requires a positive integer")
		}
		if target == "" {
			return nil, errors.New("Table name is required")
		}
		return "ALTER TABLE " + target + " AUTO_INCREMENT = " + value + ";", nil
	case "/api/query/build-drop-table-child-object-sql":
		if target == "" {
			return nil, errors.New("Table name is required")
		}
		name := str(o, "name")
		if err = compareIdentifier(name); err != nil {
			return nil, err
		}
		switch str(o, "objectType") {
		case "COLUMN":
			return "ALTER TABLE " + target + " DROP COLUMN " + quoteSQLName(name, kind) + ";", nil
		case "INDEX":
			return schemaDropIndex(kind, schema, table, map[string]any{"name": name})
		case "FOREIGN_KEY":
			if kind == "sqlite" {
				return nil, errors.New("SQLite foreign key changes require table rebuild")
			}
			keyword := "DROP CONSTRAINT"
			if kind == "mysql" {
				keyword = "DROP FOREIGN KEY"
			}
			return "ALTER TABLE " + target + " " + keyword + " " + quoteSQLName(name, kind) + ";", nil
		case "TRIGGER":
			sql := "DROP TRIGGER " + quoteSQLName(name, kind)
			if schema != "" && kind != "postgres" {
				sql = "DROP TRIGGER " + quoteSQLName(schema, kind) + "." + quoteSQLName(name, kind)
			}
			if kind == "postgres" {
				sql += " ON " + target
			}
			return sql + ";", nil
		}
		return nil, errors.New("Unknown table child object")
	case "/api/query/build-update-database-properties-sql":
		name := str(o, "name")
		if err = compareIdentifier(name); err != nil {
			return nil, err
		}
		if kind == "mysql" {
			clauses := []string{}
			for _, pair := range [][2]string{{"charset", "CHARACTER SET"}, {"collation", "COLLATE"}} {
				value := str(o, pair[0])
				if value != "" {
					if !schemaWordPattern.MatchString(value) {
						return nil, errors.New("Invalid charset or collation")
					}
					clauses = append(clauses, pair[1]+" "+value)
				}
			}
			if len(clauses) == 0 {
				return nil, errors.New("MySQL database properties require charset or collation")
			}
			return "ALTER DATABASE " + quoteSQLName(name, kind) + " " + strings.Join(clauses, " ") + ";", nil
		}
		if kind != "postgres" {
			return nil, errors.New("Database comments require PostgreSQL")
		}
		object := "DATABASE"
		if str(o, "target") == "schema" {
			object = "SCHEMA"
		}
		return "COMMENT ON " + object + " " + quoteSQLName(name, kind) + " IS " + schemaLiteral(str(o, "comment"), kind) + ";", nil
	case "/api/query/build-duplicate-table-structure-sql":
		source, err := compareTable(kind, schema, str(o, "sourceName"))
		if err != nil {
			return nil, err
		}
		target, err := compareTable(kind, schema, str(o, "targetName"))
		if err != nil {
			return nil, err
		}
		if kind == "mysql" {
			return "CREATE TABLE " + target + " LIKE " + source + ";", nil
		}
		if kind == "postgres" {
			sql := "CREATE TABLE " + target + " (LIKE " + source + " INCLUDING ALL);"
			if comment := str(o, "tableComment"); comment != "" {
				sql += "\nCOMMENT ON TABLE " + target + " IS " + schemaLiteral(comment, kind) + ";"
			}
			return sql, nil
		}
		return nil, errors.New("SQLite structure duplication needs source DDL to preserve constraints; use the structure editor")
	case "/api/query/build-copy-table-data-sql":
		source, err := compareTable(kind, schema, str(o, "sourceName"))
		if err != nil {
			return nil, err
		}
		target, err := compareTable(kind, schema, str(o, "targetName"))
		if err != nil {
			return nil, err
		}
		selection := "*"
		columns := ""
		if cols := compareStrings(o["columns"]); len(cols) > 0 {
			quoted := []string{}
			for _, col := range cols {
				if err = compareIdentifier(col); err != nil {
					return nil, err
				}
				quoted = append(quoted, quoteSQLName(col, kind))
			}
			selection = strings.Join(quoted, ", ")
			columns = " (" + selection + ")"
		}
		overriding := ""
		if kind == "postgres" && boolValue(o, "postgresOverridingSystemValue") {
			overriding = " OVERRIDING SYSTEM VALUE"
		}
		return "INSERT INTO " + target + columns + overriding + " SELECT " + selection + " FROM " + source + ";", nil
	case "/api/query/build-view-ddl-sql":
		target, err := compareTable(kind, schema, str(o, "name"))
		if err != nil {
			return nil, err
		}
		source := str(o, "source")
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(source)), "CREATE ") {
			return schemaDDLTarget(source, kind, schema, str(o, "name"))
		}
		source, err = cleanSQL(source, kind)
		if err != nil {
			return nil, err
		}
		if !isSimpleSelect(source) && !strings.HasPrefix(strings.ToUpper(source), "WITH ") {
			return nil, errors.New("View source must be a SELECT query")
		}
		prefix := "CREATE OR REPLACE VIEW "
		if kind == "sqlite" {
			prefix = "CREATE VIEW "
		}
		return prefix + target + " AS\n" + source + ";", nil
	case "/api/query/build-editable-object-source":
		source := strings.TrimSpace(str(o, "source"))
		if source == "" {
			return nil, errors.New("Object source is required")
		}
		return source, nil
	case "/api/query/build-executable-object-source-statements", "/api/query/build-executable-object-source-sql", "/api/query/build-routine-rename-object-source-statements":
		statements, err := advancedObjectStatements(o, kind, strings.Contains(path, "routine-rename"))
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(path, "-sql") {
			return strings.Join(statements, "\n"), nil
		}
		return statements, nil
	}
	return nil, errors.New("Unknown SQL builder")
}
func advancedObjectStatements(o map[string]any, kind string, rename bool) ([]string, error) {
	source := strings.TrimSpace(str(o, "source"))
	object, err := adminObjectKind(str(o, "objectType"))
	if err != nil {
		return nil, err
	}
	name := str(o, "name")
	if rename {
		name = str(o, "newName")
	}
	target, err := compareTable(kind, str(o, "schema"), name)
	if err != nil {
		return nil, err
	}
	if source == "" {
		return nil, errors.New("Object source is required")
	}
	if object == "VIEW" {
		sql, err := advancedAdminSQL("/api/query/build-view-ddl-sql", map[string]any{"databaseType": kind, "schema": o["schema"], "name": name, "source": source})
		if err != nil {
			return nil, err
		}
		out := []string{}
		if kind == "sqlite" {
			out = append(out, "DROP VIEW IF EXISTS "+target+";")
		}
		return append(out, sql.(string)), nil
	}
	lines := []string{}
	for _, line := range strings.Split(source, "\n") {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "DELIMITER ") {
			continue
		}
		lines = append(lines, line)
	}
	source = strings.Join(lines, "\n")
	source = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(source), "$$"), "//")
	pattern := regexp.MustCompile(`(?is)^(\s*CREATE\s+(?:OR\s+REPLACE\s+)?(?:DEFINER\s*=\s*[^ ]+\s+)?` + regexp.QuoteMeta(object) + `\s+)(?:"(?:[^"]|"")+"|` + "`(?:[^`]|``)+`" + `|[\w$]+)(?:\s*\.\s*(?:"(?:[^"]|"")+"|` + "`(?:[^`]|``)+`" + `|[\w$]+))?`)
	loc := pattern.FindStringSubmatchIndex(source)
	if loc == nil {
		return nil, errors.New("Object source must contain a recognizable CREATE statement")
	}
	source = source[:loc[2]] + source[loc[2]:loc[3]] + target + source[loc[1]:]
	parts, err := database.SplitStatementsForDialect(source, kind)
	if err != nil {
		return nil, err
	}
	if len(parts) != 1 {
		return nil, errors.New("Object editor accepts one CREATE definition")
	}
	out := []string{}
	if kind == "mysql" && (object == "FUNCTION" || object == "PROCEDURE" || object == "TRIGGER" || object == "EVENT") && !rename {
		out = append(out, "DROP "+object+" IF EXISTS "+target+";")
	}
	out = append(out, parts[0]+";")
	if rename {
		old, err := compareTable(kind, str(o, "schema"), str(o, "name"))
		if err != nil {
			return nil, err
		}
		if kind == "postgres" {
			return nil, errors.New("PostgreSQL routine rename requires its exact identity signature; use ALTER FUNCTION/PROCEDURE with argument types")
		}
		out = append(out, "DROP "+object+" "+old+";")
	}
	return out, nil
}
