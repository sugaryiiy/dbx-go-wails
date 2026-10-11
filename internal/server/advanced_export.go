package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func (s *Server) handleAdvancedExport(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	switch r.URL.Path {
	case "/api/query/build-export-insert-statements", "/api/query/build-export-sql-insert", "/api/query/build-database-sql-export":
	default:
		return false
	}
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return true
	}
	o := mapValue(p, "options")
	var result any
	var err error
	if strings.Contains(r.URL.Path, "database-sql-export") {
		result, err = s.advancedDatabaseExport(r, o)
	} else {
		var statements []string
		statements, err = advancedExportInserts(o)
		if strings.HasSuffix(r.URL.Path, "sql-insert") {
			result = strings.Join(statements, "\n")
		} else {
			result = statements
		}
	}
	if err != nil {
		writeError(w, 400, err.Error())
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
func advancedExportInserts(o map[string]any) ([]string, error) {
	kind, err := compareKind(str(o, "databaseType"))
	if err != nil {
		return nil, err
	}
	if str(o, "insertDialect") == "standard" {
		kind = "sqlite"
	}
	if dialect := str(o, "insertDialect"); dialect != "" && dialect != "source" && dialect != "standard" {
		return nil, errors.New("Unknown INSERT output dialect")
	}
	table := ""
	if name := str(o, "tableName"); name != "" {
		table, err = compareTable(kind, str(o, "schema"), name)
	} else {
		table, err = exportQualifiedName(str(o, "qualifiedTableName"), kind)
	}
	if err != nil {
		return nil, err
	}
	columns := compareStrings(o["columns"])
	types := exportNullableStrings(o["columnTypes"])
	extras := exportNullableStrings(o["columnExtras"])
	rows := compareRows(o["rows"])
	excluded := map[string]bool{}
	for _, c := range compareStrings(o["excludeColumns"]) {
		excluded[strings.ToLower(c)] = true
	}
	indices := []int{}
	quoted := []string{}
	metadata := map[int]gridColumn{}
	spatial := map[int]any{}
	for _, c := range compareList(o["spatialColumns"]) {
		index := numberValue(c, "column_index", -1)
		if index < 0 || index >= len(columns) {
			return nil, errors.New("Spatial column index is outside exported columns")
		}
		spatial[index] = c["srid"]
	}
	spatialValues := compareRows(o["spatialValues"])
	b := gridBuilder{options: gridOptions{Kind: kind}}
	for i, c := range columns {
		if err = compareIdentifier(c); err != nil {
			return nil, err
		}
		col := gridColumn{Name: c}
		if i < len(types) {
			col.Type = types[i]
		}
		if i < len(extras) {
			extra := extras[i]
			col.Extra = &extra
		}
		metadata[i] = col
		if excluded[strings.ToLower(c)] || b.generated(col) {
			continue
		}
		indices = append(indices, i)
		quoted = append(quoted, quoteSQLName(c, kind))
	}
	batch := numberValue(o, "batchSize", 100)
	if batch < 1 || batch > 10000 {
		return nil, errors.New("INSERT batch size must be between 1 and 10000")
	}
	if len(rows) > 1000000 {
		return nil, errors.New("Too many rows for an in-memory SQL export")
	}
	out := []string{}
	for start := 0; start < len(rows); start += batch {
		end := start + batch
		if end > len(rows) {
			end = len(rows)
		}
		if len(indices) == 0 {
			for range rows[start:end] {
				sql := "INSERT INTO " + table + " DEFAULT VALUES;"
				if kind == "mysql" {
					sql = "INSERT INTO " + table + " () VALUES ();"
				}
				out = append(out, sql)
			}
			continue
		}
		values := []string{}
		for rowOffset, row := range rows[start:end] {
			if len(row) != len(columns) {
				return nil, errors.New("Export row width must match columns")
			}
			cells := []string{}
			for _, i := range indices {
				value := row[i]
				col := metadata[i]
				if srid, declared := spatial[i]; declared || exportSpatialType(col.Type) {
					if start+rowOffset < len(spatialValues) && i < len(spatialValues[start+rowOffset]) && spatialValues[start+rowOffset][i] != nil {
						srid = spatialValues[start+rowOffset][i]
					}
					literal, err := exportSpatialLiteral(value, kind, col.Type, srid)
					if err != nil {
						return nil, fmt.Errorf("Export column %s: %w", columns[i], err)
					}
					cells = append(cells, literal)
					continue
				}
				if _, ok := value.([]any); ok && !strings.Contains(strings.ToLower(col.Type), "json") && kind == "postgres" && strings.HasSuffix(strings.TrimSpace(col.Type), "[]") {
					array, err := advancedPostgresArray(value.([]any), b)
					if err != nil {
						return nil, err
					}
					if !schemaTypePattern.MatchString(col.Type) {
						return nil, errors.New("Invalid PostgreSQL array column type")
					}
					cells = append(cells, array+"::"+col.Type)
					continue
				}
				if value != nil {
					if _, ok := value.(map[string]any); ok && col.Type == "" {
						col.Type = "json"
					}
					if _, ok := value.([]any); ok && col.Type == "" {
						col.Type = "json"
					}
				}
				literal, err := b.literal(value, col)
				if err != nil {
					return nil, fmt.Errorf("Export column %s: %w", columns[i], err)
				}
				cells = append(cells, literal)
			}
			values = append(values, "("+strings.Join(cells, ", ")+")")
		}
		overriding := ""
		if kind == "postgres" {
			for _, i := range indices {
				if metadata[i].Extra != nil && strings.Contains(strings.ToLower(*metadata[i].Extra), "identity") {
					overriding = " OVERRIDING SYSTEM VALUE"
				}
			}
		}
		out = append(out, "INSERT INTO "+table+" ("+strings.Join(quoted, ", ")+")"+overriding+" VALUES "+strings.Join(values, ", ")+";")
	}
	return out, nil
}

// The entire input must be a qualified identifier. The DDL lexer deliberately
// ignores literals and comments, so it cannot validate an untrusted name.
var exportNamePart = `(?:"(?:[^"]|"")+"|` + "`(?:[^`]|``)+`" + `|\[(?:[^\]]|\]\])+\]|[\p{L}_][\p{L}\p{N}_$]*)`
var exportQualifiedPattern = regexp.MustCompile(`^\s*` + exportNamePart + `(?:\s*\.\s*` + exportNamePart + `){0,2}\s*$`)

func exportQualifiedName(name, kind string) (string, error) {
	if !exportQualifiedPattern.MatchString(name) {
		return "", errors.New("A valid tableName or qualifiedTableName is required")
	}
	parts := []string{}
	for _, token := range schemaLex(name) {
		if token.identifier != "" {
			if err := compareIdentifier(token.identifier); err != nil {
				return "", err
			}
			parts = append(parts, quoteSQLName(token.identifier, kind))
		}
	}
	return strings.Join(parts, "."), nil
}

func exportSpatialType(typ string) bool {
	typ = strings.ToLower(strings.TrimSpace(strings.SplitN(typ, "(", 2)[0]))
	switch typ {
	case "geometry", "geography", "point", "linestring", "polygon", "multipoint", "multilinestring", "multipolygon", "geometrycollection":
		return true
	}
	return false
}

func exportSpatialLiteral(value any, kind, typ string, srid any) (string, error) {
	if value == nil {
		return "NULL", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", errors.New("Spatial export requires WKB hex or WKT text")
	}
	if strings.HasPrefix(text, "DBX_WKB:") {
		parts := strings.SplitN(text, ":", 3)
		if len(parts) != 3 {
			return "", errors.New("Invalid spatial WKB marker")
		}
		srid, text = parts[1], parts[2]
	}
	sridValue := uint64(0)
	if srid != nil {
		var err error
		sridValue, err = strconv.ParseUint(fmt.Sprint(srid), 10, 32)
		if err != nil {
			return "", errors.New("Spatial SRID must be an unsigned 32-bit integer")
		}
	}
	hex := strings.TrimPrefix(strings.TrimPrefix(text, "0x"), "\\x")
	wkb := len(hex) >= 10 && len(hex)%2 == 0 && regexp.MustCompile(`(?i)^[0-9a-f]+$`).MatchString(hex)
	if kind == "mysql" {
		if wkb {
			return fmt.Sprintf("ST_GeomFromWKB(X'%s', %d)", hex, sridValue), nil
		}
		return fmt.Sprintf("ST_GeomFromText(%s, %d)", schemaLiteral(text, kind), sridValue), nil
	}
	if kind == "postgres" {
		sql := ""
		if wkb {
			sql = fmt.Sprintf("ST_GeomFromWKB(decode('%s','hex'), %d)", hex, sridValue)
		} else {
			sql = fmt.Sprintf("ST_GeomFromText(%s, %d)", schemaLiteral(text, kind), sridValue)
		}
		if strings.HasPrefix(strings.ToLower(typ), "geography") {
			sql += "::geography"
		}
		return sql, nil
	}
	return "", errors.New("Spatial SQL export is unavailable for this dialect")
}
func advancedPostgresArray(values []any, b gridBuilder) (string, error) {
	out := []string{}
	for _, value := range values {
		if nested, ok := value.([]any); ok {
			sql, err := advancedPostgresArray(nested, b)
			if err != nil {
				return "", err
			}
			out = append(out, sql)
		} else {
			sql, err := b.literal(value, gridColumn{Type: "text"})
			if err != nil {
				return "", err
			}
			out = append(out, sql)
		}
	}
	return "ARRAY[" + strings.Join(out, ", ") + "]", nil
}
func exportComment(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " ")
}
func (s *Server) advancedDatabaseExport(r *http.Request, o map[string]any) (string, error) {
	tables := compareList(o["tables"])
	if id := str(o, "connectionId"); id != "" && len(tables) > 1 {
		details := []map[string]any{}
		for _, table := range tables {
			if str(table, "tableName") == "" {
				continue
			}
			schema := str(table, "schema")
			if schema == "" {
				schema = str(o, "schema")
			}
			fks, err := s.compareMetadata(r.Context(), id, str(o, "database"), schema, str(table, "tableName"), "foreign-keys")
			if err != nil {
				return "", err
			}
			details = append(details, map[string]any{"name": table["tableName"], "foreignKeys": fks})
		}
		order := compareStrings(schemaDependencyGraph(details)["topological_order"])
		ordered := []map[string]any{}
		used := map[int]bool{}
		for _, name := range order {
			for i, table := range tables {
				if str(table, "tableName") == name {
					ordered = append(ordered, table)
					used[i] = true
				}
			}
		}
		for i, t := range tables {
			if !used[i] {
				ordered = append(ordered, t)
			}
		}
		tables = ordered
	}
	exportedAt := str(o, "exportedAt")
	if exportedAt == "" {
		exportedAt = time.Now().UTC().Format(time.RFC3339)
	}
	limit := numberValue(o, "rowLimitPerTable", 10000)
	lines := []string{"-- Database export", "-- Database: " + exportComment(str(o, "databaseName")), "-- Exported at: " + exportComment(exportedAt), fmt.Sprintf("-- Row limit per table: %d", limit), ""}
	for _, table := range tables {
		display := exportComment(str(table, "displayName"))
		if ddl := str(table, "ddl"); ddl != "" {
			lines = append(lines, "-- Structure for "+display, strings.TrimSuffix(strings.TrimSpace(ddl), ";")+";", "")
		}
		rows := compareRows(table["rows"])
		lines = append(lines, "-- Data for "+display, fmt.Sprintf("-- Exported rows: %d", len(rows)))
		if boolValue(table, "truncated") {
			lines = append(lines, fmt.Sprintf("-- WARNING: partial data truncated at %d rows", limit))
		}
		table["batchSize"] = numberValue(o, "insertBatchSize", 100)
		table["insertDialect"] = o["insertDialect"]
		inserts, err := advancedExportInserts(table)
		if err != nil {
			return "", err
		}
		if len(inserts) == 0 {
			lines = append(lines, "-- No rows")
		} else {
			lines = append(lines, inserts...)
		}
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n"), nil
}

// Keep nullable metadata positions; json decoding into []string otherwise
// discards the whole array when one driver type/extra is null.
func exportNullableStrings(value any) []string {
	raw, _ := json.Marshal(value)
	var values []any
	_ = json.Unmarshal(raw, &values)
	out := make([]string, len(values))
	for i, v := range values {
		if v != nil {
			out[i] = fmt.Sprint(v)
		}
	}
	return out
}
