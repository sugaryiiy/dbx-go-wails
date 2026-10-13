package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

func advancedGridColumn(o map[string]any) (gridColumn, error) {
	name := str(o, "columnName")
	if err := compareIdentifier(name); err != nil {
		return gridColumn{}, err
	}
	metadata := mapValue(o, "columnInfo")
	b, _ := json.Marshal(metadata)
	var col gridColumn
	_ = json.Unmarshal(b, &col)
	if col.Name == "" {
		col.Name = name
	}
	return col, nil
}
func advancedWhere(value string) string {
	return regexp.MustCompile(`(?i)^\s*WHERE\s+`).ReplaceAllString(strings.TrimSpace(value), "")
}
func advancedGridCondition(o map[string]any) (any, error) {
	kind, err := compareKind(str(o, "databaseType"))
	if err != nil {
		return nil, err
	}
	col, err := advancedGridColumn(o)
	if err != nil {
		return nil, err
	}
	b := gridBuilder{options: gridOptions{Kind: kind}, metadata: map[string]gridColumn{col.Name: col}}
	column := quoteSQLName(str(o, "columnName"), kind)
	mode := str(o, "mode")
	value := o["value"]
	literal := func(value any) (string, error) { return b.literal(value, col) }
	switch mode {
	case "is-null":
		return column + " IS NULL", nil
	case "is-not-null":
		return column + " IS NOT NULL", nil
	case "is-blank":
		return "(" + column + " IS NULL OR " + column + " = '')", nil
	case "is-not-blank":
		return "(" + column + " IS NOT NULL AND " + column + " <> '')", nil
	case "equals", "not-equals":
		if value == nil {
			suffix := " IS NULL"
			if mode == "not-equals" {
				suffix = " IS NOT NULL"
			}
			return column + suffix, nil
		}
		v, err := literal(value)
		if err != nil {
			return nil, err
		}
		op := " = "
		if mode == "not-equals" {
			op = " <> "
		}
		return column + op + v, nil
	case "less-than", "less-than-or-equal", "greater-than", "greater-than-or-equal":
		v, err := literal(value)
		if err != nil {
			return nil, err
		}
		op := map[string]string{"less-than": "<", "less-than-or-equal": "<=", "greater-than": ">", "greater-than-or-equal": ">="}[mode]
		return column + " " + op + " " + v, nil
	case "like", "not-like", "begins-with", "ends-with":
		text := fmt.Sprint(value)
		if value == nil {
			text = ""
		}
		if mode != "begins-with" {
			text = "%" + text
		}
		if mode != "ends-with" {
			text += "%"
		}
		v, err := b.literal(text, gridColumn{Type: "text"})
		if err != nil {
			return nil, err
		}
		likeColumn := column
		if !gridTextType(col.Type) {
			cast := "TEXT"
			if kind == "mysql" {
				cast = "CHAR"
			}
			likeColumn = "CAST(" + column + " AS " + cast + ")"
		}
		op := " LIKE "
		if mode == "not-like" {
			op = " NOT LIKE "
		}
		return likeColumn + op + v, nil
	case "in", "not-in":
		values, ok := o["values"].([]any)
		if !ok {
			data, _ := json.Marshal(o["values"])
			_ = json.Unmarshal(data, &values)
		}
		if len(values) == 0 {
			return nil, nil
		}
		literals := []string{}
		hasNull := false
		seen := map[string]bool{}
		for _, v := range values {
			if v == nil {
				hasNull = true
				continue
			}
			l, err := literal(v)
			if err != nil {
				return nil, err
			}
			if !seen[l] {
				literals = append(literals, l)
				seen[l] = true
			}
		}
		if len(literals) == 0 {
			if hasNull {
				suffix := " IS NULL"
				if mode == "not-in" {
					suffix = " IS NOT NULL"
				}
				return column + suffix, nil
			}
			return nil, nil
		}
		op := " IN "
		if mode == "not-in" {
			op = " NOT IN "
		}
		predicate := column + op + "(" + strings.Join(literals, ", ") + ")"
		if mode == "not-in" {
			return "(" + column + " IS NOT NULL AND " + predicate + ")", nil
		}
		if hasNull {
			return "(" + column + " IS NULL OR " + predicate + ")", nil
		}
		return predicate, nil
	case "between", "not-between":
		end, ok := o["endValue"]
		if !ok || value == nil || end == nil {
			return nil, nil
		}
		startLiteral, err := literal(value)
		if err != nil {
			return nil, err
		}
		endLiteral, err := literal(end)
		if err != nil {
			return nil, err
		}
		op := " BETWEEN "
		if mode == "not-between" {
			op = " NOT BETWEEN "
		}
		return column + op + startLiteral + " AND " + endLiteral, nil
	}
	return nil, errors.New("Unknown grid filter mode")
}
func (s *Server) handleAdvancedGrid(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	switch r.URL.Path {
	case "/api/query/build-data-grid-context-filter-condition", "/api/query/build-data-grid-column-value-filter-condition", "/api/query/build-data-grid-column-values-filter-condition", "/api/query/build-data-grid-column-distinct-values-sql", "/api/query/build-data-grid-count-sql", "/api/query/build-data-grid-conditional-update-sql", "/api/query/build-database-search-sql", "/api/query/build-search-result-where", "/api/query/build-hive-table-properties-sql":
	default:
		return false
	}
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return true
	}
	result, err := advancedGridSQL(r.URL.Path, mapValue(p, "options"))
	if err != nil {
		writeError(w, 400, err.Error())
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
func advancedGridSQL(path string, o map[string]any) (any, error) {
	if str(o, "databaseType") == "influxdb" && path == "/api/query/build-data-grid-count-sql" {
		result, _, err := buildInfluxSQL(path, o)
		return result, err
	}
	if str(o, "databaseType") == "influxdb" {
		switch path {
		case "/api/query/build-data-grid-context-filter-condition", "/api/query/build-data-grid-column-value-filter-condition", "/api/query/build-data-grid-column-values-filter-condition":
			// These endpoints produce predicates, which the preview builder parses
			// into Flux. Use standard doubled-quote SQL literals for that grammar.
			copy := make(map[string]any, len(o))
			for k, v := range o {
				copy[k] = v
			}
			copy["databaseType"] = "sqlite"
			return advancedGridSQL(path, copy)
		}
	}
	if path == "/api/query/build-hive-table-properties-sql" {
		kind := str(o, "databaseType")
		if kind != "hive" && kind != "argo" && kind != "transwarp" {
			return nil, errors.New("Table properties require a Hive-compatible database")
		}
		table, err := compareTable("mysql", str(o, "schema"), str(o, "tableName"))
		if err != nil {
			return nil, err
		}
		property := str(o, "propertyName")
		if property == "" {
			return "SHOW TBLPROPERTIES " + table + ";", nil
		}
		return "SHOW TBLPROPERTIES " + table + " (" + schemaLiteral(property, "sqlite") + ");", nil
	}
	kind, err := compareKind(str(o, "databaseType"))
	if err != nil {
		return nil, err
	}
	if quote := str(o, "identifierQuote"); quote != "" && quote != string('"') && !(kind == "mysql" && quote == "`") {
		return nil, errors.New("Identifier quote does not match the selected dialect")
	}
	switch path {
	case "/api/query/build-data-grid-context-filter-condition":
		return advancedGridCondition(o)
	case "/api/query/build-data-grid-column-value-filter-condition":
		raw := strings.TrimSpace(str(o, "rawValue"))
		if raw == "" {
			return nil, nil
		}
		col, err := advancedGridColumn(o)
		if err != nil {
			return nil, err
		}
		var value any = raw
		if strings.EqualFold(raw, "null") {
			value = nil
		} else if strings.Contains(strings.ToLower(col.Type), "bool") {
			if v, e := strconv.ParseBool(raw); e == nil {
				value = v
			}
		} else if !gridTextType(col.Type) {
			if v, e := strconv.ParseFloat(raw, 64); e == nil && v <= 9007199254740991 && v >= -9007199254740991 {
				value = v
			}
		}
		o["mode"], o["value"] = "equals", value
		return advancedGridCondition(o)
	case "/api/query/build-data-grid-column-values-filter-condition":
		o["mode"] = "in"
		return advancedGridCondition(o)
	case "/api/query/build-data-grid-conditional-update-sql":
		if kind != "mysql" {
			return nil, nil
		}
		meta := mapValue(o, "tableMeta")
		name := str(o, "columnName")
		columns := compareList(meta["columns"])
		var col gridColumn
		found := false
		for _, c := range columns {
			if strings.EqualFold(str(c, "name"), name) {
				data, _ := json.Marshal(c)
				_ = json.Unmarshal(data, &col)
				found = true
				break
			}
		}
		if !found || col.Primary {
			return nil, nil
		}
		for _, key := range compareStrings(meta["primaryKeys"]) {
			if strings.EqualFold(key, name) {
				return nil, nil
			}
		}
		b := gridBuilder{options: gridOptions{Kind: kind}}
		if b.generated(col) {
			return nil, nil
		}
		where := advancedWhere(str(o, "whereInput"))
		if where == "" {
			return nil, nil
		}
		table, err := compareTable(kind, str(meta, "schema"), str(meta, "tableName"))
		if err != nil {
			return nil, err
		}
		value, err := b.literal(o["value"], col)
		if err != nil {
			return nil, err
		}
		sql := "UPDATE " + table + " SET " + quoteSQLName(col.Name, kind) + " = " + value + " WHERE (" + where + ");"
		if _, err = cleanSQL(sql, kind); err != nil {
			return nil, errors.New("Update predicate must belong to one statement")
		}
		return sql, nil
	case "/api/query/build-data-grid-column-distinct-values-sql", "/api/query/build-data-grid-count-sql":
		table, err := compareTable(kind, str(o, "schema"), str(o, "tableName"))
		if err != nil {
			return nil, err
		}
		if str(o, "catalog") != "" {
			return nil, errors.New("Catalog-qualified table selection is unavailable for this dialect")
		}
		predicates := []string{}
		if where := advancedWhere(str(o, "whereInput")); where != "" {
			predicates = append(predicates, "("+where+")")
		}
		selection := "COUNT(*) AS cnt"
		hint := ""
		if value := str(o, "countHint"); value != "" {
			if !strings.HasPrefix(value, "/*+") || !strings.HasSuffix(value, "*/") || strings.Contains(value, ";") {
				return nil, errors.New("Count hint must be a single optimizer comment")
			}
			hint = value + " "
		}
		suffix := ""
		if strings.Contains(path, "distinct-values") {
			col, err := advancedGridColumn(o)
			if err != nil {
				return nil, err
			}
			column := quoteSQLName(str(o, "columnName"), kind)
			selection = column + " AS dbx_value"
			if boolValue(o, "includeCounts") {
				selection += ", COUNT(*) AS dbx_count"
			}
			if boolValue(o, "excludeNulls") {
				predicates = append(predicates, column+" IS NOT NULL")
			}
			if search := str(o, "searchValue"); search != "" {
				like := column
				if !gridTextType(col.Type) {
					cast := "TEXT"
					if kind == "mysql" {
						cast = "CHAR"
					}
					like = "CAST(" + column + " AS " + cast + ")"
				}
				predicates = append(predicates, like+" LIKE "+schemaLiteral("%"+search+"%", kind))
			}
			suffix = " GROUP BY " + column + " ORDER BY "
			if boolValue(o, "includeCounts") {
				suffix += "dbx_count DESC, "
			}
			limit := numberValue(o, "limit", 100)
			if limit < 1 || limit > 10000 {
				return nil, errors.New("Distinct-value limit must be between 1 and 10000")
			}
			suffix += fmt.Sprintf("dbx_value LIMIT %d", limit)
		}
		where := ""
		if len(predicates) > 0 {
			where = " WHERE " + strings.Join(predicates, " AND ")
		}
		sql := "SELECT " + hint + selection + " FROM " + table + where + suffix
		if _, err = cleanSQL(sql, kind); err != nil {
			return nil, errors.New("Filter must belong to one SQL statement")
		}
		return sql, nil
	case "/api/query/build-database-search-sql":
		term := strings.TrimSpace(str(o, "term"))
		if term == "" {
			return nil, nil
		}
		table, err := compareTable(kind, str(o, "schema"), str(o, "tableName"))
		if err != nil {
			return nil, err
		}
		predicates, names := []string{}, []string{}
		numeric := regexp.MustCompile(`^[+-]?(?:\d+|\d+\.\d+|\.\d+)$`).MatchString(term)
		for _, col := range compareList(o["columns"]) {
			typ := strings.ToLower(str(col, "data_type"))
			name := str(col, "name")
			if err = compareIdentifier(name); err != nil {
				return nil, err
			}
			if strings.Contains(typ, "blob") || strings.Contains(typ, "binary") || typ == "bytea" {
				continue
			}
			isText := false
			for _, t := range []string{"char", "text", "clob", "uuid", "enum"} {
				if strings.Contains(typ, t) {
					isText = true
				}
			}
			if isText {
				names = append(names, name)
				predicates = append(predicates, "LOWER("+quoteSQLName(name, kind)+") LIKE "+schemaLiteral("%"+strings.ToLower(term)+"%", kind))
			} else if numeric {
				isNumber := false
				for _, t := range []string{"int", "serial", "number", "numeric", "decimal", "float", "double", "real", "money"} {
					if strings.Contains(typ, t) {
						isNumber = true
					}
				}
				if isNumber {
					names = append(names, name)
					predicates = append(predicates, quoteSQLName(name, kind)+" = "+term)
				}
			}
		}
		if len(predicates) == 0 {
			return nil, nil
		}
		limit := numberValue(o, "limit", 100)
		if limit < 1 || limit > 10000 {
			return nil, errors.New("Search limit must be between 1 and 10000")
		}
		return map[string]any{"sql": fmt.Sprintf("SELECT * FROM %s WHERE (%s) LIMIT %d", table, strings.Join(predicates, " OR "), limit), "searchableColumns": names}, nil
	case "/api/query/build-search-result-where":
		resultColumns := compareStrings(o["resultColumns"])
		rows := compareRows([]any{o["row"]})
		if len(rows) == 0 {
			return nil, errors.New("Search result row is required")
		}
		row := rows[0]
		metadata := map[string]gridColumn{}
		keys := []string{}
		for _, c := range compareList(o["columns"]) {
			data, _ := json.Marshal(c)
			var col gridColumn
			_ = json.Unmarshal(data, &col)
			metadata[strings.ToLower(col.Name)] = col
			if col.Primary {
				keys = append(keys, strings.ToLower(col.Name))
			}
		}
		if len(keys) == 0 {
			for _, c := range compareStrings(o["matchedColumns"]) {
				keys = append(keys, strings.ToLower(c))
			}
		}
		if len(keys) == 0 {
			for _, c := range resultColumns {
				keys = append(keys, strings.ToLower(c))
			}
		}
		b := gridBuilder{options: gridOptions{Kind: kind}, metadata: map[string]gridColumn{}}
		terms := []string{}
		for _, key := range keys {
			col, ok := metadata[key]
			if !ok {
				continue
			}
			index := -1
			for i, c := range resultColumns {
				if strings.EqualFold(c, col.Name) {
					index = i
					break
				}
			}
			if index < 0 || index >= len(row) {
				return nil, errors.New("Search result is missing a key value")
			}
			b.metadata[col.Name] = col
			term, err := b.predicate(col.Name, row[index], false)
			if err != nil {
				return nil, err
			}
			terms = append(terms, term)
		}
		if len(terms) == 0 {
			return nil, errors.New("Search result has no identifiable columns")
		}
		return strings.Join(terms, " AND "), nil
	}
	return nil, errors.New("Unknown grid SQL builder")
}
