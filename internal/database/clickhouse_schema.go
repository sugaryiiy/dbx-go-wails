package database

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

func (h *clickHouse) metadata(ctx context.Context, database, sql string, params url.Values) ([]record, error) {
	r, err := h.query(ctx, database, sql, true, 100000, 0, 16<<20, params)
	if err != nil {
		return nil, err
	}
	if r.Truncated {
		return nil, errors.New("ClickHouse metadata exceeds the result limit; narrow the selection")
	}
	out := make([]record, 0, len(r.Rows))
	for _, row := range r.Rows {
		item := record{}
		for i, name := range r.Columns {
			item[name] = row[i]
		}
		out = append(out, item)
	}
	return out, nil
}
func (h *clickHouse) schema(ctx context.Context, path string, p map[string]any) (any, error) {
	database := str(p, "database")
	if database == "" {
		database = h.config.Database
	}
	if database == "" {
		database = "default"
	}
	table := str(p, "table", "name")
	params := url.Values{"param_db": {database}, "param_table": {table}}
	switch strings.TrimPrefix(path, "/api/schema/") {
	case "databases", "database-metadata":
		return h.metadata(ctx, database, "SELECT name FROM system.databases ORDER BY name", nil)
	case "schemas":
		return []string{database}, nil
	case "tables", "objects", "completion-objects":
		list, err := h.metadata(ctx, database, "SELECT name, multiIf(engine = 'MaterializedView', 'MATERIALIZED_VIEW', endsWith(engine, 'View'), 'VIEW', 'TABLE') AS table_type, comment FROM system.tables WHERE database = {db:String} AND is_temporary = 0 ORDER BY name", params)
		if err != nil {
			return nil, err
		}
		list, err = filterTables(list, p)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(path, "objects") {
			for _, item := range list {
				item["object_type"] = item["table_type"]
				item["schema"] = database
			}
		}
		return list, nil
	case "columns":
		if table == "" {
			return nil, errors.New("table is required")
		}
		list, err := h.metadata(ctx, database, "SELECT name, type AS data_type, default_kind, default_expression, comment, is_in_primary_key FROM system.columns WHERE database = {db:String} AND table = {table:String} ORDER BY position", params)
		for _, item := range list {
			item["is_nullable"] = strings.Contains(valueString(item["data_type"]), "Nullable(")
			item["is_primary_key"] = valueBool(item["is_in_primary_key"])
			item["is_unique"] = false // A ClickHouse primary key is a sparse index, not a uniqueness constraint.
			item["column_default"] = nil
			item["extra"] = nil
			if valueString(item["default_kind"]) != "" {
				item["column_default"] = item["default_expression"]
				item["extra"] = item["default_kind"]
			}
			delete(item, "default_kind")
			delete(item, "default_expression")
			delete(item, "is_in_primary_key")
		}
		return list, err
	case "all-columns":
		tables, err := h.schema(ctx, "/api/schema/tables", p)
		if err != nil {
			return nil, err
		}
		all := []record{}
		for _, item := range tables.([]record) {
			cp := map[string]any{"database": database, "table": item["name"]}
			columns, err := h.schema(ctx, "/api/schema/columns", cp)
			if err != nil {
				return nil, err
			}
			all = append(all, record{"table_name": item["name"], "columns": columns})
		}
		return all, nil
	case "indexes":
		if table == "" {
			return nil, errors.New("table is required")
		}
		list, err := h.metadata(ctx, database, "SELECT name, expr, type_full FROM system.data_skipping_indices WHERE database = {db:String} AND table = {table:String} ORDER BY name", params)
		if err != nil {
			return nil, err
		}
		out := []record{}
		primary, err := h.metadata(ctx, database, "SELECT primary_key FROM system.tables WHERE database = {db:String} AND name = {table:String}", params)
		if err != nil {
			return nil, err
		}
		if len(primary) > 0 && valueString(primary[0]["primary_key"]) != "" {
			out = append(out, record{"name": "PRIMARY", "columns": []string{valueString(primary[0]["primary_key"])}, "is_unique": false, "is_primary": true, "index_type": "PRIMARY", "key_is_expression": []bool{true}, "constraint_backed": false})
		}
		for _, item := range list {
			out = append(out, record{"name": item["name"], "columns": []string{valueString(item["expr"])}, "is_unique": false, "is_primary": false, "index_type": item["type_full"], "key_is_expression": []bool{true}, "constraint_backed": false})
		}
		return out, nil
	case "foreign-keys", "reference-keys", "triggers":
		return []record{}, nil
	case "reference-key-columns":
		return []string{}, nil
	case "ddl", "object-source":
		if table == "" {
			return nil, errors.New("table is required")
		}
		// Identifier parameters use ClickHouse's own escaping rules rather than SQL interpolation.
		rows, err := h.metadata(ctx, database, "SHOW CREATE TABLE {db:Identifier}.{table:Identifier}", params)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, errors.New("ClickHouse object does not exist")
		}
		ddl := valueString(rows[0]["statement"])
		if strings.HasSuffix(path, "object-source") {
			return record{"name": table, "schema": database, "object_type": str(p, "object_type"), "source": ddl, "editable": false}, nil
		}
		return ddl, nil
	case "data-types":
		return []string{"String", "FixedString", "Bool", "UInt8", "UInt16", "UInt32", "UInt64", "Int8", "Int16", "Int32", "Int64", "Float32", "Float64", "Decimal", "Date", "Date32", "DateTime", "DateTime64", "UUID", "IPv4", "IPv6", "Array", "Tuple", "Map", "Nullable", "LowCardinality"}, nil
	default:
		return nil, unsupported(path)
	}
}
