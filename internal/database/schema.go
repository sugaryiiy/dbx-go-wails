package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type record = map[string]any

func records(ctx context.Context, q queryRunner, statement string, args ...any) ([]record, error) {
	rows, e := q.QueryContext(ctx, statement, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	columns, e := rows.Columns()
	if e != nil {
		return nil, e
	}
	out := []record{}
	for rows.Next() {
		v := make([]any, len(columns))
		ptr := make([]any, len(v))
		for i := range v {
			ptr[i] = &v[i]
		}
		if e = rows.Scan(ptr...); e != nil {
			return nil, e
		}
		r := record{}
		for i, k := range columns {
			if bytes, ok := v[i].([]byte); ok {
				r[k] = string(bytes)
			} else {
				r[k] = v[i]
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func valueString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func valueInt(v any) int64 { n, _ := strconv.ParseInt(valueString(v), 10, 64); return n }
func valueBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	s := strings.ToLower(valueString(v))
	return s == "1" || s == "true" || s == "yes"
}
func (m *Manager) schema(ctx context.Context, path string, p map[string]any) (any, error) {
	db, c, _, e := m.pool(ctx, str(p, "connection_id", "connectionId"), str(p, "database"))
	if e != nil {
		return nil, e
	}
	schema := str(p, "schema")
	database := str(p, "database")
	table := str(p, "table")
	if database == "" {
		database = c.Database
	}
	if schema == "" {
		if c.Type == "sqlite" {
			schema = "main"
		} else if c.Type == "mysql" {
			schema = database
		} else if c.DefaultSchema != "" {
			schema = c.DefaultSchema
		} else {
			schema = "public"
		}
	}
	if c.Type == "sqlite" && schema != "main" && schema != "temp" {
		return nil, unsupported("SQLite attached schemas")
	}
	var result any
	if result, handled, err := schemaAdvanced(ctx, db, c, database, schema, table, path, p); handled {
		return result, redact(err, c)
	}
	switch strings.TrimPrefix(path, "/api/schema/") {
	case "databases", "database-metadata":
		switch c.Type {
		case "sqlite":
			result, e = records(ctx, db, "SELECT name FROM pragma_database_list ORDER BY seq")
		case "mysql":
			result, e = records(ctx, db, "SELECT schema_name AS name, default_character_set_name AS default_charset, default_collation_name AS default_collation FROM information_schema.schemata ORDER BY schema_name")
		case "postgres":
			result, e = records(ctx, db, "SELECT datname AS name FROM pg_database WHERE NOT datistemplate ORDER BY datname")
		}
	case "schemas":
		switch c.Type {
		case "sqlite":
			result = []string{"main"}
		case "mysql":
			result = []string{database}
		case "postgres":
			var schemas []record
			schemas, e = records(ctx, db, "SELECT schema_name AS name FROM information_schema.schemata WHERE schema_name NOT LIKE 'pg_%' AND schema_name <> 'information_schema' ORDER BY schema_name")
			names := make([]string, 0, len(schemas))
			for _, item := range schemas {
				names = append(names, valueString(item["name"]))
			}
			result = names
		}
	case "objects", "completion-objects":
		result, e = nativeObjects(ctx, db, c.Type, database, schema, p)
	case "tables":
		var list []record
		switch c.Type {
		case "sqlite":
			list, e = records(ctx, db, "SELECT name, upper(type) AS table_type FROM "+quoteIdentifier(schema, c.Type)+".sqlite_schema WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY type,name")
		case "mysql":
			list, e = records(ctx, db, "SELECT table_name AS name, CASE table_type WHEN 'BASE TABLE' THEN 'TABLE' ELSE table_type END AS table_type, table_comment AS comment FROM information_schema.tables WHERE table_schema=? ORDER BY table_type,table_name", database)
		case "postgres":
			list, e = records(ctx, db, "SELECT c.relname AS name, CASE c.relkind WHEN 'v' THEN 'VIEW' WHEN 'm' THEN 'MATERIALIZED_VIEW' ELSE 'TABLE' END AS table_type, obj_description(c.oid) AS comment FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','p','v','m','f') ORDER BY c.relkind,c.relname", schema)
		}
		if e != nil {
			break
		}
		list, e = filterTables(list, p)
		if e != nil {
			break
		}
		result = list
	case "columns":
		result, e = columns(ctx, db, c.Type, database, schema, table)
	case "completion-assistant":
		if c.Type != "postgres" {
			return nil, unsupported("completion assistant for " + c.Type)
		}
		result, e = postgresCompletion(ctx, db, database, schema, p)
	case "table-comment":
		switch c.Type {
		case "postgres":
			var comment sql.NullString
			e = db.QueryRowContext(ctx, "SELECT obj_description(c.oid,'pg_class') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2", schema, table).Scan(&comment)
			if e == nil && comment.Valid {
				result = comment.String
			}
		default:
			return nil, unsupported("table comment for " + c.Type)
		}
	case "all-columns":
		var tableRecords any
		tableRecords, e = m.schema(ctx, "/api/schema/tables", p)
		if e != nil {
			break
		}
		all := []record{}
		for _, t := range tableRecords.([]record) {
			var cols []record
			cols, e = columns(ctx, db, c.Type, database, schema, valueString(t["name"]))
			if e != nil {
				break
			}
			all = append(all, record{"table_name": t["name"], "columns": cols})
		}
		result = all
	case "indexes":
		result, e = indexes(ctx, db, c.Type, database, schema, table)
	case "foreign-keys":
		result, e = foreignKeys(ctx, db, c.Type, database, schema, table)
	case "reference-key-columns", "reference-keys":
		var idx []record
		idx, e = indexes(ctx, db, c.Type, database, schema, table)
		keys := []record{}
		first := []string{}
		for _, r := range idx {
			if referenceableIndex(r) {
				cols := r["columns"].([]string)
				keys = append(keys, record{"columns": cols})
				if len(first) == 0 || valueBool(r["is_primary"]) {
					first = cols
				}
			}
		}
		if strings.HasSuffix(path, "/reference-keys") {
			result = keys
		} else {
			result = first
		}
	case "ddl", "object-source":
		var ddl string
		objectType := strings.ToUpper(str(p, "object_type"))
		if c.Type == "postgres" && (objectType == "FUNCTION" || objectType == "PROCEDURE" || objectType == "TRIGGER" || objectType == "SEQUENCE") {
			ddl, e = postgresCatalogObjectSource(ctx, db, schema, table, objectType, p)
		} else {
			ddl, e = objectDDL(ctx, db, c.Type, database, schema, table, objectType)
		}
		if strings.HasSuffix(path, "object-source") {
			result = record{"name": table, "schema": schema, "object_type": str(p, "object_type"), "source": ddl, "editable": true}
		} else {
			result = ddl
		}
	case "event-info":
		if c.Type != "mysql" {
			return nil, unsupported("scheduled events for " + c.Type)
		}
		result, e = mysqlEventInfo(ctx, db, database, table)
	case "triggers":
		if c.Type == "sqlite" {
			var list []record
			list, e = records(ctx, db, "SELECT name, sql AS statement FROM "+quoteIdentifier(schema, c.Type)+".sqlite_schema WHERE type='trigger' AND tbl_name=? ORDER BY name", table)
			for _, r := range list {
				r["event"] = ""
				r["timing"] = ""
				r["enabled"] = true
			}
			result = list
		} else if c.Type == "mysql" {
			result, e = records(ctx, db, "SELECT trigger_name AS name, event_manipulation AS event, action_timing AS timing, action_statement AS statement FROM information_schema.triggers WHERE trigger_schema=? AND event_object_table=? ORDER BY trigger_name", database, table)
		} else {
			result, e = records(ctx, db, "SELECT t.tgname AS name, pg_get_triggerdef(t.oid) AS statement, '' AS event, '' AS timing, t.tgenabled <> 'D' AS enabled FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND NOT t.tgisinternal ORDER BY t.tgname", schema, table)
		}
	case "data-types":
		switch c.Type {
		case "sqlite":
			result = []string{"INTEGER", "REAL", "TEXT", "BLOB", "NUMERIC"}
		case "mysql":
			result = []string{"BIGINT", "INT", "SMALLINT", "TINYINT", "DECIMAL", "DOUBLE", "FLOAT", "VARCHAR", "TEXT", "LONGTEXT", "BLOB", "DATETIME", "TIMESTAMP", "DATE", "TIME", "JSON", "BOOLEAN"}
		case "postgres":
			result = []string{"bigint", "integer", "smallint", "numeric", "real", "double precision", "text", "varchar", "boolean", "date", "timestamp", "timestamptz", "bytea", "uuid", "json", "jsonb"}
		}
	case "partitions", "subpartitions", "functions", "sequences", "rules", "owners", "extensions", "available-extensions", "event-triggers", "invalid-indexes":
		if c.Type == "sqlite" {
			result = []record{}
		} else {
			return nil, unsupported(path)
		}
	case "constraints":
		return nil, unsupported("constraint introspection")
	case "table-partition-status":
		if c.Type == "sqlite" {
			result = record{"isPartitionedParent": false, "isPartition": false}
		} else {
			return nil, unsupported(path)
		}
	default:
		return nil, unsupported(path)
	}
	return result, redact(e, c)
}
func filterTables(list []record, p map[string]any) ([]record, error) {
	if str(p, "table_name_filter") != "" {
		return nil, unsupported("structured table name filters")
	}
	filter := strings.ToLower(str(p, "filter"))
	types := strings.ToUpper(str(p, "object_types"))
	if types == "" {
		types = strings.ToUpper(str(p, "object_type"))
	}
	allowed := map[string]bool{}
	for _, v := range strings.Split(types, ",") {
		v = strings.TrimSpace(v)
		if v != "" {
			allowed[v] = true
		}
	}
	filtered := []record{}
	for _, r := range list {
		if filter != "" && !strings.Contains(strings.ToLower(valueString(r["name"])), filter) {
			continue
		}
		if len(allowed) > 0 && !allowed[valueString(r["table_type"])] {
			continue
		}
		filtered = append(filtered, r)
	}
	offset := integer(p, 0, "offset")
	limit := integer(p, 0, "limit")
	if offset < 0 || limit < 0 {
		return nil, errors.New("limit and offset cannot be negative")
	}
	if offset >= len(filtered) {
		return []record{}, nil
	}
	filtered = filtered[offset:]
	if limit > 0 && limit < len(filtered) {
		filtered = filtered[:limit]
	}
	return filtered, nil
}
func columns(ctx context.Context, db *sql.DB, kind, database, schema, table string) ([]record, error) {
	if table == "" {
		return nil, errors.New("table is required")
	}
	var out []record
	var e error
	switch kind {
	case "sqlite":
		raw, err := records(ctx, db, "SELECT * FROM pragma_table_xinfo(?, ?) ORDER BY cid", table, schema)
		if err != nil {
			return nil, err
		}
		out = []record{}
		for _, r := range raw {
			pk := valueInt(r["pk"]) > 0
			extra := any(nil)
			if valueInt(r["hidden"]) >= 2 {
				extra = "GENERATED"
			}
			out = append(out, record{"name": r["name"], "data_type": r["type"], "is_nullable": valueInt(r["notnull"]) == 0 && !pk, "column_default": r["dflt_value"], "is_primary_key": pk, "extra": extra})
		}
	case "mysql":
		out, e = records(ctx, db, "SELECT column_name AS name, column_type AS data_type, is_nullable='YES' AS is_nullable, column_default, column_key='PRI' AS is_primary_key, column_key='UNI' AS is_unique, extra, column_comment AS comment, numeric_precision, numeric_scale, character_maximum_length, character_set_name AS character_set, collation_name AS collation FROM information_schema.columns WHERE table_schema=? AND table_name=? ORDER BY ordinal_position", database, table)
	case "postgres":
		out, e = postgresColumns(ctx, db, schema, table)
	}
	for _, r := range out {
		r["is_nullable"] = valueBool(r["is_nullable"])
		r["is_primary_key"] = valueBool(r["is_primary_key"])
		if _, ok := r["is_unique"]; ok {
			r["is_unique"] = valueBool(r["is_unique"])
		}
	}
	return out, e
}
func indexes(ctx context.Context, db *sql.DB, kind, database, schema, table string) ([]record, error) {
	out := []record{}
	switch kind {
	case "sqlite":
		raw, e := records(ctx, db, "SELECT * FROM pragma_index_list(?, ?) ORDER BY seq", table, schema)
		if e != nil {
			return nil, e
		}
		for _, r := range raw {
			info, e := records(ctx, db, "SELECT name FROM pragma_index_info(?, ?) ORDER BY seqno", r["name"], schema)
			if e != nil {
				return nil, e
			}
			names := []string{}
			for _, v := range info {
				names = append(names, valueString(v["name"]))
			}
			out = append(out, record{"name": r["name"], "columns": names, "is_unique": valueInt(r["unique"]) == 1, "is_primary": r["origin"] == "pk", "constraint_backed": r["origin"] != "c"})
		}
		// INTEGER PRIMARY KEY aliases rowid and has no entry in index_list.
		raw, e = records(ctx, db, "SELECT name FROM pragma_table_info(?, ?) WHERE pk > 0 ORDER BY pk", table, schema)
		if e != nil {
			return nil, e
		}
		hasPrimary := false
		for _, r := range out {
			hasPrimary = hasPrimary || valueBool(r["is_primary"])
		}
		if len(raw) > 0 && !hasPrimary {
			names := []string{}
			for _, r := range raw {
				names = append(names, valueString(r["name"]))
			}
			out = append(out, record{"name": "PRIMARY", "columns": names, "is_unique": true, "is_primary": true, "constraint_backed": true})
		}
	case "mysql":
		raw, e := records(ctx, db, "SELECT index_name AS name, column_name, non_unique, index_type FROM information_schema.statistics WHERE table_schema=? AND table_name=? ORDER BY index_name,seq_in_index", database, table)
		if e != nil {
			return nil, e
		}
		byName := map[string]record{}
		for _, r := range raw {
			name := valueString(r["name"])
			item := byName[name]
			if item == nil {
				item = record{"name": name, "columns": []string{}, "is_unique": valueInt(r["non_unique"]) == 0, "is_primary": name == "PRIMARY", "index_type": r["index_type"]}
				byName[name] = item
				out = append(out, item)
			}
			item["columns"] = append(item["columns"].([]string), valueString(r["column_name"]))
		}
	case "postgres":
		return postgresIndexes(ctx, db, schema, table)
	}
	return out, nil
}
func foreignKeys(ctx context.Context, db *sql.DB, kind, database, schema, table string) ([]record, error) {
	switch kind {
	case "sqlite":
		raw, e := records(ctx, db, "SELECT * FROM pragma_foreign_key_list(?, ?) ORDER BY id, seq", table, schema)
		if e != nil {
			return nil, e
		}
		out := []record{}
		for _, r := range raw {
			out = append(out, record{"name": fmt.Sprintf("fk_%s_%v", table, r["id"]), "column": r["from"], "ref_schema": schema, "ref_table": r["table"], "ref_column": valueString(r["to"]), "on_update": r["on_update"], "on_delete": r["on_delete"]})
		}
		return out, nil
	case "mysql":
		return records(ctx, db, "SELECT k.constraint_name AS name, k.column_name AS `column`, k.referenced_table_schema AS ref_schema, k.referenced_table_name AS ref_table, k.referenced_column_name AS ref_column, r.update_rule AS on_update, r.delete_rule AS on_delete FROM information_schema.key_column_usage k JOIN information_schema.referential_constraints r ON r.constraint_schema=k.constraint_schema AND r.constraint_name=k.constraint_name AND r.table_name=k.table_name WHERE k.table_schema=? AND k.table_name=? AND k.referenced_table_name IS NOT NULL ORDER BY k.constraint_name,k.ordinal_position", database, table)
	case "postgres":
		return records(ctx, db, `SELECT c.conname AS name, a.attname AS "column", rn.nspname AS ref_schema, rt.relname AS ref_table, ra.attname AS ref_column,
 (SELECT k FROM generate_subscripts(c.conkey,1) AS k WHERE c.conkey[k]=a.attnum) AS key_position,
 CASE c.confupdtype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT' WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END AS on_update,
 CASE c.confdeltype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT' WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END AS on_delete
 FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_namespace n ON n.oid=t.relnamespace
 JOIN pg_class rt ON rt.oid=c.confrelid JOIN pg_namespace rn ON rn.oid=rt.relnamespace
 JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum=ANY(c.conkey)
 JOIN pg_attribute ra ON ra.attrelid=rt.oid AND ra.attnum=c.confkey[(SELECT k FROM generate_subscripts(c.conkey,1) AS k WHERE c.conkey[k]=a.attnum)]
 WHERE c.contype='f' AND n.nspname=$1 AND t.relname=$2 ORDER BY c.conname,key_position`, schema, table)
	}
	return nil, unsupported("foreign key introspection")
}
func objectDDL(ctx context.Context, db *sql.DB, kind, database, schema, table, objectType string) (string, error) {
	switch kind {
	case "sqlite":
		var ddl sql.NullString
		statement := "SELECT sql FROM " + quoteIdentifier(schema, kind) + ".sqlite_schema WHERE name=?"
		args := []any{table}
		switch strings.ToUpper(strings.TrimSpace(objectType)) {
		case "":
			statement += " AND type IN ('table','view')"
		case "TABLE", "VIEW", "TRIGGER", "INDEX":
			statement += " AND type=?"
			args = append(args, strings.ToLower(strings.TrimSpace(objectType)))
		default:
			return "", unsupported("SQLite source for " + objectType)
		}
		e := db.QueryRowContext(ctx, statement, args...).Scan(&ddl)
		return ddl.String, e
	case "mysql":
		return mysqlCatalogObjectSource(ctx, db, database, table, objectType)
	case "postgres":
		return postgresObjectDDL(ctx, db, schema, table, objectType)
	}
	return "", unsupported("DDL extraction")
}
