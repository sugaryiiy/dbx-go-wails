package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/sugaryiiy/dbx-go-wails/internal/database"
)

// splitSQLiteDefinition separates top-level column/constraint definitions,
// preserving quoted defaults, generated expressions and table CHECK clauses.
func splitSQLiteDefinition(ddl string) ([]string, string, error) {
	start := strings.IndexByte(ddl, '(')
	if start < 0 {
		return nil, "", errors.New("SQLite table has no reconstructable CREATE TABLE body")
	}
	depth := 1
	quote := byte(0)
	segment := start + 1
	parts := []string{}
	for i := start + 1; i < len(ddl); i++ {
		c := ddl[i]
		if quote != 0 {
			close := quote
			if quote == '[' {
				close = ']'
			}
			if c == close {
				if i+1 < len(ddl) && ddl[i+1] == close {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' || c == '[' {
			quote = c
			continue
		}
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(ddl[segment:i]))
				return parts, strings.TrimSuffix(strings.TrimSpace(ddl[i+1:]), ";"), nil
			}
		case ',':
			if depth == 1 {
				parts = append(parts, strings.TrimSpace(ddl[segment:i]))
				segment = i + 1
			}
		}
	}
	return nil, "", errors.New("Unterminated SQLite table definition")
}
func sqliteLeadingIdentifier(definition string) (string, bool) {
	definition = strings.TrimSpace(definition)
	if definition == "" {
		return "", false
	}
	if regexp.MustCompile(`(?i)^(?:CONSTRAINT\b|PRIMARY\s+KEY\b|FOREIGN\s+KEY\b|UNIQUE\s*\(|CHECK\s*\()`).MatchString(definition) {
		return "", false
	}
	if definition[0] == '"' || definition[0] == '`' || definition[0] == '[' {
		quote := definition[0]
		close := quote
		if quote == '[' {
			close = ']'
		}
		out := ""
		for i := 1; i < len(definition); i++ {
			if definition[i] == close {
				if i+1 < len(definition) && definition[i+1] == close {
					out += string(close)
					i++
					continue
				}
				return out, true
			}
			out += string(definition[i])
		}
		return "", false
	}
	for i, c := range definition {
		if c == ' ' || c == '\t' || c == '\n' {
			return definition[:i], true
		}
	}
	return definition, true
}
func sqliteColumnUnchanged(col, original map[string]any) bool {
	meta := editableColumnMetadata(col)
	for _, key := range []string{"name", "data_type", "is_nullable", "column_default", "is_primary_key"} {
		if key == "is_nullable" || key == "is_primary_key" {
			if boolValue(meta, key) != boolValue(original, key) {
				return false
			}
			continue
		}
		if str(meta, key) != str(original, key) {
			return false
		}
	}
	if schemaColumnAutomatic(meta) != schemaColumnAutomatic(original) {
		return false
	}
	return true
}
func (s *Server) buildSQLitePreview(ctx context.Context, p map[string]any) (map[string]any, error) {
	id, db := str(p, "connectionId"), str(p, "database")
	opts := mapValue(p, "options")
	if str(opts, "databaseType") != "sqlite" {
		return nil, errors.New("SQLite structure preview requires SQLite")
	}
	schema, table := str(opts, "schema"), str(opts, "tableName")
	if schema == "" {
		schema = "main"
	}
	if schema != "main" && schema != "temp" {
		return nil, errors.New("SQLite schema must be main or temp")
	}
	provider, ok := any(s.database).(interface {
		SQLiteSchemaRevision(context.Context, string, string, string, string) (string, error)
	})
	if !ok {
		return nil, errors.New("SQLite revision API is unavailable")
	}
	revision, err := provider.SQLiteSchemaRevision(ctx, id, db, schema, table)
	if err != nil {
		return nil, err
	}
	ddlValue, err := s.compareMetadata(ctx, id, db, schema, table, "ddl")
	if err != nil {
		return nil, err
	}
	ddl := fmt.Sprint(ddlValue)
	parts, suffix, err := splitSQLiteDefinition(ddl)
	if err != nil {
		return nil, err
	}
	originalDefinitions := map[string]string{}
	constraints := []string{}
	for _, part := range parts {
		if name, ok := sqliteLeadingIdentifier(part); ok {
			originalDefinitions[name] = part
		} else {
			constraints = append(constraints, part)
		}
	}
	originalCols, err := s.compareMetadata(ctx, id, db, schema, table, "columns")
	if err != nil {
		return nil, err
	}
	columns := compareList(opts["columns"])
	originalMap := map[string]map[string]any{}
	for _, c := range compareList(originalCols) {
		originalMap[str(c, "name")] = c
	}
	seenOriginal := map[string]bool{}
	active := []map[string]any{}
	droppedOrRenamed := false
	defs := []string{}
	insertCols, selectCols := []string{}, []string{}
	warnings := []string{}
	for _, col := range columns {
		original := mapValue(col, "original")
		oldName := str(original, "name")
		if oldName != "" {
			live, exists := originalMap[oldName]
			if !exists {
				return nil, fmt.Errorf("Original column %s no longer exists", oldName)
			}
			original = live
			seenOriginal[oldName] = true
		}
		if boolValue(col, "markedForDrop") {
			if oldName != "" {
				droppedOrRenamed = true
			}
			continue
		}
		name := str(col, "name")
		if oldName != "" && name != oldName {
			droppedOrRenamed = true
		}
		meta := editableColumnMetadata(col)
		active = append(active, meta)
		raw := originalDefinitions[oldName]
		if oldName != "" && sqliteColumnUnchanged(col, original) {
			defs = append(defs, raw)
		} else {
			if oldName != "" && regexp.MustCompile(`(?i)\b(CHECK|UNIQUE|REFERENCES|GENERATED|COLLATE)\b`).MatchString(raw) {
				return nil, fmt.Errorf("Column %s has a constraint or generated expression that must be preserved in explicit DDL", oldName)
			}
			definition, err := schemaColumnDefinition("sqlite", meta, false)
			if err != nil {
				return nil, err
			}
			defs = append(defs, definition)
		}
		if oldName != "" && !regexp.MustCompile(`(?i)\bGENERATED\b|\bAS\s*\(`).MatchString(raw) {
			insertCols = append(insertCols, quoteSQLName(name, "sqlite"))
			selectCols = append(selectCols, quoteSQLName(oldName, "sqlite"))
		}
	}
	for name := range originalMap {
		if !seenOriginal[name] {
			return nil, fmt.Errorf("Column %s is missing from the edit snapshot; reload all columns before rebuilding", name)
		}
	}
	if len(defs) == 0 {
		return nil, errors.New("SQLite table must retain at least one column")
	}
	fkChanged := false
	for _, fk := range compareList(opts["foreignKeys"]) {
		original := mapValue(fk, "original")
		if len(original) == 0 || boolValue(fk, "markedForDrop") || str(fk, "name") != str(original, "name") || str(fk, "column") != str(original, "column") || str(fk, "refTable") != str(original, "ref_table") || str(fk, "refColumn") != str(original, "ref_column") || str(fk, "onUpdate") != str(original, "on_update") || str(fk, "onDelete") != str(original, "on_delete") {
			fkChanged = true
		}
	}
	for _, constraint := range constraints {
		upper := strings.ToUpper(constraint)
		if strings.Contains(upper, "PRIMARY KEY") {
			continue
		}
		if strings.Contains(upper, "FOREIGN KEY") && fkChanged {
			continue
		}
		if droppedOrRenamed {
			return nil, errors.New("Renaming or dropping columns in a table with table constraints requires explicit DDL so dependent expressions cannot be lost")
		}
		defs = append(defs, constraint)
	}
	if fkChanged {
		for _, fk := range compareList(opts["foreignKeys"]) {
			if boolValue(fk, "markedForDrop") {
				continue
			}
			definition, err := schemaForeignKeyDefinition("sqlite", fk)
			if err != nil {
				return nil, err
			}
			defs = append(defs, definition)
		}
	}
	primary := []string{}
	hasInlinePrimary := false
	for _, definition := range defs {
		if strings.Contains(strings.ToUpper(definition), "PRIMARY KEY") {
			hasInlinePrimary = true
		}
	}
	for _, c := range active {
		if boolValue(c, "is_primary_key") {
			primary = append(primary, quoteSQLName(str(c, "name"), "sqlite"))
		}
	}
	for _, idx := range compareList(opts["indexes"]) {
		if boolValue(idx, "isPrimary") && !boolValue(idx, "markedForDrop") {
			primary = []string{}
			for _, c := range compareStrings(idx["columns"]) {
				primary = append(primary, quoteSQLName(c, "sqlite"))
			}
		}
	}
	if !hasInlinePrimary && len(primary) > 0 {
		defs = append(defs, "PRIMARY KEY ("+strings.Join(primary, ", ")+")")
	}
	random := make([]byte, 6)
	if _, err = rand.Read(random); err != nil {
		return nil, err
	}
	tempName := "__dbx_rebuild_" + hex.EncodeToString(random)
	temp, _ := compareTable("sqlite", schema, tempName)
	target, _ := compareTable("sqlite", schema, table)
	create := "CREATE TABLE " + temp + " (\n  " + strings.Join(defs, ",\n  ") + "\n)"
	if suffix != "" {
		create += " " + suffix
	}
	statements := []string{create + ";"}
	if len(insertCols) > 0 {
		statements = append(statements, "INSERT INTO "+temp+" ("+strings.Join(insertCols, ", ")+") SELECT "+strings.Join(selectCols, ", ")+" FROM "+target+";")
	} else {
		return nil, errors.New("Rebuilding without any retained writable columns requires explicit data migration")
	}
	statements = append(statements, "DROP TABLE "+target+";", "ALTER TABLE "+temp+" RENAME TO "+quoteSQLName(table, "sqlite")+";")
	if regexp.MustCompile(`(?i)\bAUTOINCREMENT\b`).MatchString(ddl) {
		counter, err := s.database.ExecuteSQL(ctx, id, db, "SELECT seq FROM "+quoteSQLName(schema, "sqlite")+".sqlite_sequence WHERE name="+schemaLiteral(table, "sqlite"), 2)
		if err != nil {
			return nil, err
		}
		if len(counter.Rows) > 0 && len(counter.Rows[0]) > 0 {
			value := fmt.Sprint(counter.Rows[0][0])
			if !regexp.MustCompile(`^[0-9]+$`).MatchString(value) {
				return nil, errors.New("Invalid SQLite AUTOINCREMENT counter")
			}
			statements = append(statements, "UPDATE "+quoteSQLName(schema, "sqlite")+".sqlite_sequence SET seq=MAX(seq,"+value+") WHERE name="+schemaLiteral(table, "sqlite")+";")
		}
	}
	indexSources, err := s.database.ExecuteSQL(ctx, id, db, "SELECT name,sql FROM "+quoteSQLName(schema, "sqlite")+".sqlite_schema WHERE type='index' AND tbl_name="+schemaLiteral(table, "sqlite")+" AND sql IS NOT NULL ORDER BY name", 10000)
	if err != nil {
		return nil, err
	}
	if indexSources.Truncated {
		return nil, errors.New("Too many dependent indexes to safely rebuild this table")
	}
	rawIndexes := map[string]string{}
	for _, row := range indexSources.Rows {
		if len(row) == 2 {
			rawIndexes[fmt.Sprint(row[0])] = fmt.Sprint(row[1])
		}
	}
	for _, idx := range compareList(opts["indexes"]) {
		if boolValue(idx, "markedForDrop") || boolValue(idx, "isPrimary") {
			continue
		}
		original := mapValue(idx, "original")
		originalName := str(original, "name")
		if str(idx, "name") == originalName && compareEqual(idx["columns"], original["columns"]) && (boolValue(idx, "isUnique") == boolValue(original, "is_unique")) && str(idx, "filter") == str(original, "filter") && rawIndexes[originalName] != "" {
			statements = append(statements, strings.TrimSuffix(strings.TrimSpace(rawIndexes[originalName]), ";")+";")
			continue
		}
		sql, err := schemaIndexSQL("sqlite", schema, table, idx)
		if err != nil {
			return nil, err
		}
		if sql != "" {
			statements = append(statements, sql)
		}
	}
	liveIndexes, err := s.compareMetadata(ctx, id, db, schema, table, "indexes")
	if err != nil {
		return nil, err
	}
	coveredIndexes := map[string]bool{}
	for _, idx := range compareList(opts["indexes"]) {
		original := mapValue(idx, "original")
		coveredIndexes[str(original, "name")] = true
		coveredIndexes[str(idx, "name")] = true
	}
	for _, idx := range compareList(liveIndexes) {
		if boolValue(idx, "is_primary") || strings.HasPrefix(str(idx, "name"), "sqlite_autoindex_") || coveredIndexes[str(idx, "name")] {
			continue
		}
		sql := rawIndexes[str(idx, "name")]
		if sql == "" {
			return nil, fmt.Errorf("Index %s has no reconstructable source", str(idx, "name"))
		}
		if sql != "" {
			statements = append(statements, strings.TrimSuffix(strings.TrimSpace(sql), ";")+";")
		}
	}
	liveTriggers, err := s.compareMetadata(ctx, id, db, schema, table, "triggers")
	if err != nil {
		return nil, err
	}
	editedTriggers := map[string]map[string]any{}
	for _, trigger := range compareList(opts["triggers"]) {
		original := mapValue(trigger, "original")
		key := str(original, "name")
		if key == "" {
			key = str(trigger, "name")
		}
		editedTriggers[key] = trigger
	}
	for _, trigger := range compareList(liveTriggers) {
		name := str(trigger, "name")
		if edited, exists := editedTriggers[name]; exists {
			delete(editedTriggers, name)
			if boolValue(edited, "markedForDrop") {
				continue
			}
			trigger = edited
		}
		sql := str(trigger, "statement")
		if sql == "" {
			return nil, errors.New("Cannot preserve trigger without source")
		}
		sql, err = cleanSQL(sql, "sqlite")
		if err != nil {
			return nil, err
		}
		statements = append(statements, sql+";")
	}
	for _, trigger := range editedTriggers {
		if boolValue(trigger, "markedForDrop") {
			continue
		}
		sql, err := cleanSQL(str(trigger, "statement"), "sqlite")
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(strings.ToUpper(sql), "CREATE TRIGGER") {
			return nil, errors.New("SQLite trigger source must contain CREATE TRIGGER")
		}
		statements = append(statements, sql+";")
	}
	if droppedOrRenamed && len(compareList(liveTriggers)) > 0 {
		return nil, errors.New("Tables with triggers need explicit dependent trigger edits when columns are renamed or removed")
	}
	if droppedOrRenamed {
		objects, err := s.compareMetadata(ctx, id, db, schema, "", "tables")
		if err != nil {
			return nil, err
		}
		for _, object := range compareList(objects) {
			if !strings.Contains(strings.ToUpper(str(object, "table_type")), "VIEW") {
				continue
			}
			source, err := s.compareMetadata(ctx, id, db, schema, str(object, "name"), "ddl")
			if err != nil {
				return nil, err
			}
			for _, token := range schemaLex(fmt.Sprint(source)) {
				if strings.EqualFold(token.identifier, table) {
					return nil, fmt.Errorf("View %s depends on this table; edit its source explicitly before renaming or removing columns", str(object, "name"))
				}
			}
		}
	}
	if str(opts, "tableComment") != "" {
		warnings = append(warnings, "SQLite comments are retained in local documentation only")
	}
	return map[string]any{"statements": statements, "warnings": warnings, "schemaRevision": revision}, nil
}
func (s *Server) handleSQLiteStructure(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	if r.URL.Path != "/api/query/preview-sqlite-table-structure-change" && r.URL.Path != "/api/query/apply-sqlite-table-structure-change" {
		return false
	}
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return true
	}
	preview, err := s.buildSQLitePreview(r.Context(), p)
	var result any = preview
	if err == nil && strings.Contains(r.URL.Path, "/apply-") {
		expected := str(p, "schemaRevision")
		if expected == "" || expected != str(preview, "schemaRevision") {
			err = errors.New("SQLite schema changed since preview; reload and preview again")
		} else {
			provider, ok := any(s.database).(interface {
				SQLiteRebuild(context.Context, string, string, string, string, string, []string) (database.QueryResult, error)
			})
			if !ok {
				err = errors.New("SQLite rebuild API is unavailable")
			} else {
				opts := mapValue(p, "options")
				schema := str(opts, "schema")
				if schema == "" {
					schema = "main"
				}
				result, err = provider.SQLiteRebuild(r.Context(), str(p, "connectionId"), str(p, "database"), schema, str(opts, "tableName"), expected, compareStrings(preview["statements"]))
			}
		}
	}
	if err != nil {
		writeError(w, 400, err.Error())
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
