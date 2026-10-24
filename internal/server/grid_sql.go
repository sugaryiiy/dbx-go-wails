package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
)

type gridColumn struct {
	Name     string  `json:"name"`
	Type     string  `json:"data_type"`
	Nullable bool    `json:"is_nullable"`
	Primary  bool    `json:"is_primary_key"`
	Default  *string `json:"column_default"`
	Extra    *string `json:"extra"`
}
type gridTable struct {
	Catalog     string       `json:"catalog"`
	Database    string       `json:"database"`
	Schema      string       `json:"schema"`
	Name        string       `json:"tableName"`
	PrimaryKeys []string     `json:"primaryKeys"`
	Columns     []gridColumn `json:"columns"`
}
type gridChange struct {
	Index int
	Value any
}

func (c *gridChange) UnmarshalJSON(data []byte) error {
	var pair []json.RawMessage
	if err := json.Unmarshal(data, &pair); err != nil {
		return err
	}
	if len(pair) != 2 {
		return errors.New("Each cell change must contain column index and value")
	}
	if err := json.Unmarshal(pair[0], &c.Index); err != nil {
		return err
	}
	return json.Unmarshal(pair[1], &c.Value)
}

type gridDirtyRow struct {
	Index   int
	Changes []gridChange
}

func (row *gridDirtyRow) UnmarshalJSON(data []byte) error {
	var pair []json.RawMessage
	if err := json.Unmarshal(data, &pair); err != nil {
		return err
	}
	if len(pair) != 2 {
		return errors.New("Each dirty row must contain row index and changes")
	}
	if err := json.Unmarshal(pair[0], &row.Index); err != nil {
		return err
	}
	return json.Unmarshal(pair[1], &row.Changes)
}

type gridOptions struct {
	Kind               string         `json:"databaseType"`
	IdentifierQuote    string         `json:"identifierQuote"`
	Table              gridTable      `json:"tableMeta"`
	Columns            []string       `json:"columns"`
	SourceColumns      *[]*string     `json:"sourceColumns"`
	Rows               [][]any        `json:"rows"`
	DirtyRows          []gridDirtyRow `json:"dirtyRows"`
	DeletedRows        []int          `json:"deletedRows"`
	NewRows            [][]any        `json:"newRows"`
	IncludeDatabase    bool           `json:"includeDatabaseName"`
	ExcludePrimaryKeys bool           `json:"excludePrimaryKeys"`
	IncludeComputed    bool           `json:"includeComputedColumns"`
	InsertMode         string         `json:"insertMode"`
}
type gridPreparation struct {
	ValidationError string          `json:"validationError,omitempty"`
	Statements      []string        `json:"statements"`
	Rollback        []string        `json:"rollbackStatements"`
	Schema          string          `json:"executionSchema,omitempty"`
	KeylessGuards   []gridSaveGuard `json:"keylessGuards,omitempty"`
	MaxAffectedRows []*int          `json:"maxAffectedRowsPerStatement,omitempty"`
}
type gridSaveGuard struct {
	SQL            string `json:"sql"`
	MaxMatchedRows int    `json:"maxMatchedRows"`
	Message        string `json:"message"`
}
type gridBuilder struct {
	options  gridOptions
	table    string
	columns  []string
	metadata map[string]gridColumn
	indexes  map[string]int
	keys     []string
}

func (s *Server) handleGridSQL(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	switch r.URL.Path {
	case "/api/query/prepare-data-grid-save", "/api/query/build-data-grid-copy-update-statements", "/api/query/build-data-grid-copy-insert-statement":
	default:
		return false
	}
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return true
	}
	raw, ok := p["options"].(map[string]any)
	if !ok {
		writeError(w, 400, "Data grid options must be an object")
		return true
	}
	data, err := json.Marshal(raw)
	if err != nil {
		writeError(w, 400, "Invalid data grid options")
		return true
	}
	var opts gridOptions
	if err = json.Unmarshal(data, &opts); err != nil {
		writeError(w, 400, "Invalid data grid option structure")
		return true
	}
	builder, err := newGridBuilder(opts)
	if err == nil {
		profile := str(p, "driverProfile")
		if profile != "" && profile != "native" && profile != builder.options.Kind && !(builder.options.Kind == "mysql" && profile == "mariadb") {
			err = errors.New("This driver profile has not been migrated for data grid writes")
		}
	}
	var result any
	if r.URL.Path == "/api/query/prepare-data-grid-save" {
		prepared := gridPreparation{Statements: []string{}, Rollback: []string{}}
		if err == nil {
			prepared, err = builder.prepare()
		}
		if err != nil {
			prepared = gridPreparation{ValidationError: err.Error(), Statements: []string{}, Rollback: []string{}}
		}
		writeJSON(w, 200, prepared)
		return true
	}
	if err == nil {
		if r.URL.Path == "/api/query/build-data-grid-copy-update-statements" {
			result, err = builder.copyUpdates()
		} else {
			result, err = builder.copyInserts()
		}
	}
	if err != nil {
		writeError(w, 400, err.Error())
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
func newGridBuilder(o gridOptions) (*gridBuilder, error) {
	if o.Kind == "mariadb" {
		o.Kind = "mysql"
	}
	switch o.Kind {
	case "sqlite", "mysql", "postgres":
	default:
		return nil, errors.New("Data grid writes are supported only for SQLite, MySQL and PostgreSQL")
	}
	expectedQuote := "\""
	if o.Kind == "mysql" {
		expectedQuote = "`"
	}
	if o.IdentifierQuote != "" && o.IdentifierQuote != expectedQuote {
		return nil, errors.New("Unsupported identifier quote for this database")
	}
	if o.Table.Name == "" || strings.ContainsRune(o.Table.Name, 0) {
		return nil, errors.New("A valid target table is required")
	}
	if o.Table.Catalog != "" {
		return nil, errors.New("Catalog-qualified data grid writes have not been migrated")
	}
	b := &gridBuilder{options: o, metadata: map[string]gridColumn{}, indexes: map[string]int{}, keys: o.Table.PrimaryKeys}
	b.table = quoteSQLName(o.Table.Name, o.Kind)
	schema := o.Table.Schema
	if o.Kind == "mysql" {
		schema = ""
		if o.IncludeDatabase {
			schema = o.Table.Database
			if schema == "" {
				schema = o.Table.Schema
			}
		}
	}
	if strings.ContainsRune(schema, 0) {
		return nil, errors.New("Invalid schema identifier")
	}
	if schema != "" {
		b.table = quoteSQLName(schema, o.Kind) + "." + b.table
	}
	if len(o.Columns) == 0 || len(o.Columns) > 4096 {
		return nil, errors.New("Data grid columns are required (maximum 4096)")
	}
	if len(o.Table.Columns) == 0 {
		return nil, errors.New("Table column metadata is required before saving")
	}
	for _, col := range o.Table.Columns {
		if col.Name == "" || strings.ContainsRune(col.Name, 0) {
			return nil, errors.New("Invalid table column metadata")
		}
		if _, exists := b.metadata[col.Name]; exists {
			return nil, errors.New("Duplicate table column metadata")
		}
		b.metadata[col.Name] = col
	}
	b.columns = make([]string, len(o.Columns))
	if o.SourceColumns != nil && len(*o.SourceColumns) != len(o.Columns) {
		return nil, errors.New("sourceColumns must match the result columns")
	}
	for i, col := range o.Columns {
		if o.SourceColumns != nil {
			source := (*o.SourceColumns)[i]
			if source == nil || *source == "" {
				continue
			}
			col = *source
		}
		if _, exists := b.metadata[col]; !exists {
			return nil, fmt.Errorf("Result column %q is not a known table column", col)
		}
		if _, duplicate := b.indexes[col]; duplicate {
			return nil, fmt.Errorf("Result maps more than once to column %q", col)
		}
		b.columns[i] = col
		b.indexes[col] = i
	}
	seenKeys := map[string]bool{}
	for _, key := range b.keys {
		if _, exists := b.metadata[key]; !exists || seenKeys[key] {
			return nil, errors.New("Invalid primary key metadata")
		}
		seenKeys[key] = true
	}
	if len(o.Rows)+len(o.NewRows) > 100000 {
		return nil, errors.New("Too many grid rows")
	}
	for _, row := range append(append([][]any{}, o.Rows...), o.NewRows...) {
		if len(row) != len(o.Columns) {
			return nil, errors.New("Row width must match result columns")
		}
	}
	return b, nil
}
func (b *gridBuilder) completeRow() bool {
	for name, col := range b.metadata {
		if b.generated(col) {
			continue
		}
		if _, ok := b.indexes[name]; !ok {
			return false
		}
	}
	return true
}
func (b *gridBuilder) generated(col gridColumn) bool {
	extra := ""
	if col.Extra != nil {
		extra = strings.ToLower(*col.Extra)
	}
	return (strings.Contains(extra, "generated") && !strings.Contains(extra, "identity")) || b.options.Kind == "postgres" && strings.EqualFold(col.Type, "tsvector")
}
func (b *gridBuilder) automatic(col gridColumn) bool {
	extra := ""
	if col.Extra != nil {
		extra = strings.ToLower(*col.Extra)
	}
	return strings.Contains(extra, "auto_increment") || strings.Contains(extra, "autoincrement") || strings.Contains(extra, "identity") || strings.Contains(extra, "serial") || col.Default != nil && strings.Contains(strings.ToLower(*col.Default), "nextval(") || b.options.Kind == "sqlite" && len(b.keys) == 1 && b.keys[0] == col.Name && strings.EqualFold(strings.TrimSpace(col.Type), "INTEGER")
}
func gridTextType(kind string) bool {
	kind = strings.ToLower(kind)
	return kind == "" || strings.Contains(kind, "char") || strings.Contains(kind, "text") || strings.Contains(kind, "clob") || strings.Contains(kind, "json") || strings.Contains(kind, "enum") || strings.Contains(kind, "set") || strings.Contains(kind, "uuid")
}
func (b *gridBuilder) saveValue(value any, col gridColumn) any {
	if value == "" && col.Nullable && !gridTextType(col.Type) {
		return nil
	}
	return value
}
func (b *gridBuilder) literal(value any, col gridColumn) (string, error) {
	if value == nil {
		return "NULL", nil
	}
	switch v := value.(type) {
	case bool:
		if v {
			return "TRUE", nil
		}
		return "FALSE", nil
	case float64:
		if math.IsInf(v, 0) || math.IsNaN(v) || math.Abs(v) > 9007199254740991 && v == math.Trunc(v) {
			return "", errors.New("Large integers must be supplied as strings to preserve precision")
		}
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	case string:
		typ := strings.ToLower(col.Type)
		if strings.Contains(typ, "blob") || strings.Contains(typ, "binary") || typ == "bytea" || b.options.Kind == "mysql" && (typ == "bit" || strings.HasPrefix(typ, "bit(")) {
			if !strings.HasPrefix(v, "0x") {
				return "", errors.New("Binary values must use 0x hexadecimal notation")
			}
			if _, err := hex.DecodeString(v[2:]); err != nil {
				return "", errors.New("Invalid hexadecimal binary value")
			}
			if b.options.Kind == "postgres" {
				return "decode('" + v[2:] + "','hex')", nil
			}
			return "X'" + v[2:] + "'", nil
		}
		if b.options.Kind == "mysql" {
			if v == "" {
				return "''", nil
			}
			return "CONVERT(X'" + hex.EncodeToString([]byte(v)) + "' USING utf8mb4)", nil
		}
		if b.options.Kind == "postgres" {
			if strings.ContainsRune(v, 0) {
				return "", errors.New("PostgreSQL text cannot contain NUL")
			}
			return "E'" + strings.ReplaceAll(strings.ReplaceAll(v, "\\", "\\\\"), "'", "''") + "'", nil
		}
		if strings.ContainsRune(v, 0) {
			return "CAST(X'" + hex.EncodeToString([]byte(v)) + "' AS TEXT)", nil
		}
		return "'" + strings.ReplaceAll(v, "'", "''") + "'", nil
	case []any, map[string]any:
		if !strings.Contains(strings.ToLower(col.Type), "json") {
			return "", errors.New("Array or object values require a JSON column; other structured types have not been migrated")
		}
		data, err := json.Marshal(v)
		if err != nil {
			return "", errors.New("Invalid JSON column value")
		}
		return b.literal(string(data), col)
	default:
		return "", errors.New("Unsupported cell value type")
	}
}
func (b *gridBuilder) predicate(name string, value any, keyless bool) (string, error) {
	col := b.metadata[name]
	ident := quoteSQLName(name, b.options.Kind)
	if value == nil {
		return ident + " IS NULL", nil
	}
	literal, err := b.literal(value, col)
	if err != nil {
		return "", err
	}
	if keyless && b.options.Kind == "mysql" && strings.EqualFold(strings.TrimSpace(col.Type), "json") {
		// MySQL normalizes JSON on assignment. Compare parsed JSON so a new
		// compact value can still identify the row in compensating SQL. Root
		// extraction is also available in MariaDB, unlike CAST(... AS JSON).
		return "JSON_EXTRACT(" + ident + ", '$') = JSON_EXTRACT(" + literal + ", '$')", nil
	}
	if keyless && b.options.Kind == "mysql" && gridTextType(col.Type) {
		ident = "BINARY " + ident
	}
	if b.options.Kind == "postgres" && strings.EqualFold(col.Type, "json") {
		ident += "::text"
		literal += "::text"
	}
	if b.options.Kind == "postgres" && strings.EqualFold(col.Type, "jsonb") {
		literal += "::jsonb"
	}
	return ident + " = " + literal, nil
}
func (b *gridBuilder) where(row []any) (string, error) {
	keyless := len(b.keys) == 0
	names := b.keys
	if keyless {
		// The result contains the original values, including the old value of
		// each edited cell. Use all mapped columns when no key is available.
		names = b.columns
	}
	parts := []string{}
	for _, name := range names {
		if name == "" {
			continue
		}
		idx, ok := b.indexes[name]
		if !ok {
			return "", fmt.Errorf("Result is missing primary key column %q", name)
		}
		if !keyless && row[idx] == nil {
			return "", fmt.Errorf("Primary key column %q has no value", name)
		}
		part, err := b.predicate(name, row[idx], keyless)
		if err != nil {
			return "", err
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "", errors.New("Refusing an UPDATE or DELETE without a row predicate")
	}
	return strings.Join(parts, " AND "), nil
}

func (b *gridBuilder) insert(row []any, restore bool, excludePK bool) (string, error) {
	columns, values := []string{}, []string{}
	for i, name := range b.columns {
		if name == "" {
			continue
		}
		col := b.metadata[name]
		if b.generated(col) {
			continue
		}
		isPK := false
		for _, key := range b.keys {
			isPK = isPK || key == name
		}
		if excludePK && isPK {
			continue
		}
		value := row[i]
		if b.automatic(col) && (value == nil || value == "") {
			continue
		}
		if !restore && value == nil {
			if !col.Nullable && col.Default == nil && !b.automatic(col) && b.options.Kind != "mysql" {
				return "", fmt.Errorf("Column %q cannot be NULL", name)
			}
			continue
		}
		value = b.saveValue(value, col)
		if value == nil && !col.Nullable && !b.automatic(col) {
			return "", fmt.Errorf("Column %q cannot be NULL", name)
		}
		literal, err := b.literal(value, col)
		if err != nil {
			return "", err
		}
		columns = append(columns, quoteSQLName(name, b.options.Kind))
		values = append(values, literal)
	}
	if len(columns) == 0 {
		if b.options.Kind == "mysql" {
			return "INSERT INTO " + b.table + " () VALUES ();", nil
		}
		return "INSERT INTO " + b.table + " DEFAULT VALUES;", nil
	}
	return "INSERT INTO " + b.table + " (" + strings.Join(columns, ", ") + ") VALUES (" + strings.Join(values, ", ") + ");", nil
}
// keylessLocator returns the physical row-locator column for a dialect that
// exposes one, or "" when a keyless mutation cannot address a single
// physical row. Only PostgreSQL qualifies: ctid names exactly one tuple.
// SQLite is deliberately excluded (a WITHOUT ROWID table has no rowid to
// select, and its guard flow is covered by tests), as is MySQL, which
// exposes no row locator at all.
func (b *gridBuilder) keylessLocator() string {
	if len(b.keys) != 0 || b.options.Kind != "postgres" || !b.completeRow() {
		return ""
	}
	return "ctid"
}

// targetSingleRow rewrites a complete all-values predicate so the mutation
// lands on exactly one of the rows it matches. When several stored rows are
// identical in every column, they are interchangeable: updating or deleting
// any one of them produces the same visible outcome, so refusing the save
// (the guard's answer) only strands the user. Targeting one physical row
// keeps the change to a single row, which MaxAffectedRows already enforces.
func (b *gridBuilder) targetSingleRow(predicate string) string {
	locator := b.keylessLocator()
	if locator == "" {
		return predicate
	}
	return locator + " IN (SELECT " + locator + " FROM " + b.table + " WHERE (" + predicate + ") LIMIT 1)"
}

func (b *gridBuilder) prepare() (gridPreparation, error) {
	out := gridPreparation{Statements: []string{}, Rollback: []string{}, Schema: b.options.Table.Schema}
	reverse := []string{}
	reversible := true
	touched := map[int]bool{}
	guarded := map[string]bool{}
	appendStatement := func(statement string, keylessMutation bool) {
		out.Statements = append(out.Statements, statement)
		var limit *int
		if keylessMutation && len(b.keys) == 0 {
			one := 1
			limit = &one
		}
		out.MaxAffectedRows = append(out.MaxAffectedRows, limit)
	}
	guardPredicate := func(predicate string) {
		if len(b.keys) != 0 || guarded[predicate] {
			return
		}
		guarded[predicate] = true
		// Count the exact predicate sent by the mutation: the loaded page
		// alone cannot tell whether another matching row exists in the table.
		out.KeylessGuards = append(out.KeylessGuards, gridSaveGuard{
			SQL:            "SELECT COUNT(*) AS dbx_keyless_row_matches FROM " + b.table + " WHERE (" + predicate + ")",
			MaxMatchedRows: 1,
			Message:        "Cannot safely update or delete this row: the table has no primary key and more than one row matches the original values. Add a primary key or unique index before editing.",
		})
	}

	for _, dirty := range b.options.DirtyRows {
		if dirty.Index < 0 || dirty.Index >= len(b.options.Rows) || touched[dirty.Index] {
			return out, errors.New("Invalid or duplicate changed row index")
		}
		touched[dirty.Index] = true
		before := b.options.Rows[dirty.Index]
		after := append([]any{}, before...)
		predicate, err := b.where(before)
		if err != nil {
			return out, err
		}
		sets, undoSets := []string{}, []string{}
		changedColumns := map[int]bool{}
		for _, change := range dirty.Changes {
			i := change.Index
			if i < 0 || i >= len(b.columns) || b.columns[i] == "" || changedColumns[i] {
				return out, errors.New("Invalid, unmapped or duplicate changed column index")
			}
			changedColumns[i] = true
			name := b.columns[i]
			col := b.metadata[name]
			if b.generated(col) {
				return out, fmt.Errorf("Generated column %q cannot be edited", name)
			}
			newValue := b.saveValue(change.Value, col)
			isPK := false
			for _, key := range b.keys {
				isPK = isPK || key == name
			}
			if newValue == nil && (!col.Nullable || isPK) {
				return out, fmt.Errorf("Column %q cannot be NULL", name)
			}
			literal, err := b.literal(newValue, col)
			if err != nil {
				return out, err
			}
			previous, err := b.literal(before[i], col)
			if err != nil {
				return out, err
			}
			sets = append(sets, quoteSQLName(name, b.options.Kind)+" = "+literal)
			undoSets = append(undoSets, quoteSQLName(name, b.options.Kind)+" = "+previous)
			after[i] = newValue
		}
		if len(sets) == 0 {
			continue
		}
		if b.keylessLocator() == "" {
			guardPredicate(predicate)
		}
		appendStatement("UPDATE "+b.table+" SET "+strings.Join(sets, ", ")+" WHERE "+b.targetSingleRow(predicate)+";", true)
		undoPredicate, err := b.where(after)
		if err != nil {
			return out, err
		}
		for _, change := range dirty.Changes {
			part, err := b.predicate(b.columns[change.Index], after[change.Index], true)
			if err != nil {
				return out, err
			}
			undoPredicate += " AND " + part
		}
		reverse = append(reverse, "UPDATE "+b.table+" SET "+strings.Join(undoSets, ", ")+" WHERE "+b.targetSingleRow(undoPredicate)+";")
	}
	for _, index := range b.options.DeletedRows {
		if index < 0 || index >= len(b.options.Rows) || touched[index] {
			return out, errors.New("Invalid, duplicate or concurrently edited deleted row index")
		}
		touched[index] = true
		row := b.options.Rows[index]
		predicate, err := b.where(row)
		if err != nil {
			return out, err
		}
		if b.keylessLocator() == "" {
			guardPredicate(predicate)
		}
		appendStatement("DELETE FROM "+b.table+" WHERE "+b.targetSingleRow(predicate)+";", true)
		if b.completeRow() {
			undo, err := b.insert(row, true, false)
			if err != nil {
				return out, err
			}
			reverse = append(reverse, undo)
		} else {
			reversible = false
		}
	}
	for _, row := range b.options.NewRows {
		statement, err := b.insert(row, false, false)
		if err != nil {
			return out, err
		}
		appendStatement(statement, false)
		// Without known primary keys an auto-generated or default-filled row cannot
		// be identified safely for a later compensating DELETE.
		if len(b.keys) == 0 {
			reversible = false
			continue
		}
		unknownKey := false
		for _, key := range b.keys {
			if idx, ok := b.indexes[key]; !ok || row[idx] == nil || (b.automatic(b.metadata[key]) && row[idx] == "") {
				unknownKey = true
			}
		}
		if unknownKey {
			reversible = false
			continue
		}
		predicate, err := b.where(row)
		if err != nil {
			reversible = false
			continue
		}
		for i, name := range b.columns {
			if name == "" || row[i] == nil || b.generated(b.metadata[name]) || (b.automatic(b.metadata[name]) && row[i] == "") {
				continue
			}
			part, err := b.predicate(name, b.saveValue(row[i], b.metadata[name]), true)
			if err != nil {
				return out, err
			}
			predicate += " AND " + part
		}
		reverse = append(reverse, "DELETE FROM "+b.table+" WHERE "+predicate+";")
	}
	if reversible {
		for i := len(reverse) - 1; i >= 0; i-- {
			out.Rollback = append(out.Rollback, reverse[i])
		}
	}
	return out, nil
}
func (b *gridBuilder) copyUpdates() ([]string, error) {
	results := []string{}
	for _, row := range b.options.Rows {
		predicate, err := b.where(row)
		if err != nil {
			return nil, err
		}
		sets := []string{}
		for i, name := range b.columns {
			if name == "" || b.generated(b.metadata[name]) {
				continue
			}
			primary := false
			for _, key := range b.keys {
				primary = primary || key == name
			}
			if primary {
				continue
			}
			literal, err := b.literal(row[i], b.metadata[name])
			if err != nil {
				return nil, err
			}
			sets = append(sets, quoteSQLName(name, b.options.Kind)+" = "+literal)
		}
		if len(sets) > 0 {
			results = append(results, "UPDATE "+b.table+" SET "+strings.Join(sets, ", ")+" WHERE "+predicate+";")
		}
	}
	return results, nil
}
func (b *gridBuilder) copyInserts() (any, error) {
	if b.options.IncludeComputed {
		return nil, errors.New("Copying computed columns into INSERT has not been migrated")
	}
	if b.options.InsertMode != "" && b.options.InsertMode != "merged" && b.options.InsertMode != "row-by-row" {
		return nil, errors.New("Unknown INSERT mode")
	}
	if len(b.options.Rows) == 0 {
		return nil, nil
	}
	// Individual INSERTs preserve per-row default/generated-column behavior.
	statements := []string{}
	for _, row := range b.options.Rows {
		statement, err := b.insert(row, true, b.options.ExcludePrimaryKeys)
		if err != nil {
			return nil, err
		}
		statements = append(statements, statement)
	}
	if b.options.InsertMode != "row-by-row" && len(statements) > 1 {
		prefix, tuples := "", []string{}
		mergeable := true
		for _, statement := range statements {
			marker := strings.Index(statement, " VALUES (")
			if marker < 0 {
				mergeable = false
				break
			}
			current := statement[:marker+8]
			if prefix != "" && prefix != current {
				mergeable = false
				break
			}
			prefix = current
			tuples = append(tuples, strings.TrimSuffix(statement[marker+8:], ";"))
		}
		if mergeable {
			return prefix + strings.Join(tuples, ", ") + ";", nil
		}
	}
	return strings.Join(statements, "\n"), nil
}
