package server

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/sugaryiiy/dbx-go-wails/internal/database"
)

func quoteSQLName(value, kind string) string {
	q := "\""
	if kind == "mysql" || kind == "mariadb" || kind == "clickhouse" {
		q = "`"
	}
	return q + strings.ReplaceAll(value, q, q+q) + q
}
func tableSQLName(p map[string]any) (string, error) {
	kind := str(p, "databaseType")
	if kind == "mariadb" {
		kind = "mysql"
	}
	if kind != "" && kind != "sqlite" && kind != "mysql" && kind != "postgres" && kind != "clickhouse" {
		return "", errors.New("SQL generation for this database has not yet been migrated")
	}
	table := str(p, "tableName")
	if table == "" || strings.ContainsRune(table, 0) {
		return "", errors.New("Table name is required")
	}
	namespace := str(p, "schema")
	if (kind == "mysql" || kind == "clickhouse") && boolValue(p, "includeDatabaseName") {
		namespace = str(p, "database")
	}
	if namespace != "" {
		return quoteSQLName(namespace, kind) + "." + quoteSQLName(table, kind), nil
	}
	return quoteSQLName(table, kind), nil
}
func cleanSQL(sql string, dialect ...string) (string, error) {
	kind := ""
	if len(dialect) > 0 {
		kind = dialect[0]
	}
	parts, err := database.SplitStatementsForDialect(sql, kind)
	if err != nil {
		return "", err
	}
	if len(parts) != 1 {
		return "", errors.New("Exactly one SQL statement is required")
	}
	return parts[0], nil
}
func isSimpleSelect(sql string) bool {
	// A conservative boundary: statements we cannot classify keep their original
	// execution path instead of being wrapped and potentially executed twice.
	upper := strings.ToUpper(strings.TrimSpace(sql))
	return strings.HasPrefix(upper, "SELECT ") || strings.HasPrefix(upper, "SELECT\n") || strings.HasPrefix(upper, "SELECT\t")
}
func buildSQL(path string, p map[string]any) (any, bool, error) {
	options := mapValue(p, "options")
	if str(options, "databaseType") == "influxdb" {
		if result, handled, err := buildInfluxSQL(path, options); handled {
			return result, true, err
		}
	}
	if str(options, "databaseType") == "mariadb" {
		options["databaseType"] = "mysql"
	}
	switch path {
	case "/api/query/find-statement-at-cursor":
		sql := str(p, "sql")
		parts, err := database.SplitStatementsForDialect(sql, str(p, "databaseType"))
		if err != nil {
			return nil, true, err
		}
		cursor := numberValue(p, "cursorPos", 0)
		search := 0
		for _, part := range parts {
			offset := strings.Index(sql[search:], part)
			if offset < 0 {
				continue
			}
			end := search + offset + len(part)
			if cursor <= len(utf16.Encode([]rune(sql[:end]))) {
				return part, true, nil
			}
			search = end
		}
		if len(parts) > 0 {
			return parts[len(parts)-1], true, nil
		}
		return "", true, nil
	case "/api/query/build-table-select-sql", "/api/query/build-data-grid-count-sql":
		table, err := tableSQLName(options)
		if err != nil {
			return nil, true, err
		}
		selection := "*"
		if columns, ok := options["columns"].([]any); ok && len(columns) > 0 {
			names := make([]string, len(columns))
			for i, col := range columns {
				name, ok := col.(string)
				if !ok {
					return nil, true, errors.New("Invalid column name")
				}
				names[i] = quoteSQLName(name, str(options, "databaseType"))
			}
			selection = strings.Join(names, ", ")
		}
		if path == "/api/query/build-data-grid-count-sql" {
			selection = "COUNT(*)"
		}
		sql := "SELECT " + selection + " FROM " + table
		if where := strings.TrimSpace(str(options, "whereInput")); where != "" {
			if strings.HasPrefix(strings.ToUpper(where), "WHERE ") {
				where = where[6:]
			}
			sql += " WHERE " + where
		}
		if path == "/api/query/build-table-select-sql" {
			if order := strings.TrimSpace(str(options, "orderBy")); order != "" {
				if strings.HasPrefix(strings.ToUpper(order), "ORDER BY ") {
					order = order[9:]
				}
				sql += " ORDER BY " + order
			}
			if _, ok := options["limit"]; ok {
				limit := numberValue(options, "limit", 100)
				offset := numberValue(options, "offset", 0)
				if limit < 0 || offset < 0 {
					return nil, true, errors.New("Invalid pagination")
				}
				sql += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
			}
		}
		if _, err = cleanSQL(sql, str(options, "databaseType")); err != nil {
			return nil, true, errors.New("Filter or order expression must belong to one SQL statement")
		}
		return sql, true, nil
	case "/api/query/prepare-pagination-plan":
		sql := str(options, "sql")
		pagination := mapValue(options, "pagination")
		limit := numberValue(pagination, "limit", 100)
		offset := numberValue(pagination, "offset", 0)
		plan := map[string]any{"sqlToExecute": sql, "useAgentResultSession": false}
		base, err := cleanSQL(sql, str(options, "databaseType"))
		if err != nil || !isSimpleSelect(base) {
			return plan, true, nil
		}
		if limit <= 0 || limit > maxTransferRows || offset < 0 {
			return nil, true, errors.New("Invalid pagination")
		}
		kind := str(options, "databaseType")
		if kind != "" && kind != "sqlite" && kind != "mysql" && kind != "postgres" && kind != "clickhouse" {
			return nil, true, errors.New("Pagination is not implemented for this database")
		}
		paged := fmt.Sprintf("SELECT * FROM (\n%s\n) AS dbx_page LIMIT %d OFFSET %d", base, limit, offset)
		if boolValue(options, "firstPageUsesActualSql") && offset == 0 {
			paged = sql
		}
		plan["sqlToExecute"] = paged
		plan["pageSql"] = paged
		plan["pageLimit"] = limit
		plan["pageOffset"] = offset
		plan["countSql"] = "SELECT COUNT(*) FROM (\n" + base + "\n) AS dbx_count"
		return plan, true, nil
	case "/api/query/build-sorted-sql":
		sql, err := cleanSQL(str(options, "originalSql"), str(options, "databaseType"))
		if err != nil {
			return map[string]any{"ok": false, "reason": "multi"}, true, nil
		}
		if !isSimpleSelect(sql) {
			return map[string]any{"ok": false, "reason": "not_select"}, true, nil
		}
		columns, _ := options["resultColumns"].([]any)
		index := numberValue(options, "columnIndex", -1)
		if index < 0 || index >= len(columns) {
			return nil, true, errors.New("Sort column is out of range")
		}
		direction := strings.ToUpper(str(options, "direction"))
		if direction != "ASC" && direction != "DESC" {
			return nil, true, errors.New("Invalid sort direction")
		}
		return map[string]any{"ok": true, "sql": fmt.Sprintf("SELECT * FROM (\n%s\n) AS dbx_sort ORDER BY %d %s", sql, index+1, direction)}, true, nil
	case "/api/query/build-explain-sql":
		sql, err := cleanSQL(str(options, "sql"), str(options, "databaseType"))
		if err != nil || !isSimpleSelect(sql) {
			return map[string]any{"ok": false, "reason": "unsafe"}, true, nil
		}
		prefix := "EXPLAIN "
		switch str(options, "databaseType") {
		case "sqlite":
			prefix = "EXPLAIN QUERY PLAN "
		case "postgres":
			if boolValue(options, "analyze") {
				prefix = "EXPLAIN (ANALYZE, FORMAT JSON) "
			} else {
				prefix = "EXPLAIN (FORMAT JSON) "
			}
		case "clickhouse":
			if boolValue(options, "analyze") || (str(options, "format") != "" && str(options, "format") != "text" && str(options, "format") != "standard") {
				return map[string]any{"ok": false, "reason": "unsupported"}, true, nil
			}
		case "mysql":
			if str(options, "format") == "json" {
				prefix = "EXPLAIN FORMAT=JSON "
			}
		default:
			return map[string]any{"ok": false, "reason": "unsupported"}, true, nil
		}
		return map[string]any{"ok": true, "sql": prefix + sql}, true, nil
	}
	return nil, false, nil
}
