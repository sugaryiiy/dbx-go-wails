package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Query the catalog on demand so completion works before the sidebar has
// loaded a schema. Bound parameters treat masks and object names literally.
func postgresCompletion(ctx context.Context, db *sql.DB, database, schema string, p map[string]any) (record, error) {
	kinds := []string{"table", "view"}
	if raw, ok := p["object_kinds"]; ok && raw != nil {
		encoded, err := json.Marshal(raw)
		if err != nil || json.Unmarshal(encoded, &kinds) != nil {
			return nil, errors.New("object_kinds must be an array of strings")
		}
	}
	wanted := map[string]bool{}
	for _, kind := range kinds {
		switch kind {
		case "database", "schema", "table", "view", "routine", "procedure", "function", "column", "sequence":
			wanted[kind] = true
		default:
			return nil, fmt.Errorf("invalid completion object kind: %s", kind)
		}
	}
	limit := integer(p, 200, "max_results")
	if limit < 1 {
		return nil, errors.New("max_results must be positive")
	}
	if limit > 2000 {
		limit = 2000
	}
	matchMode := str(p, "match_mode")
	if matchMode != "" && matchMode != "prefix" && matchMode != "contains" {
		return nil, errors.New("invalid completion match mode")
	}
	if parentSchema := str(p, "parent_schema"); parentSchema != "" {
		schema = parentSchema
	}
	args := []any{}
	bind := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	namespace := "n.nspname=" + bind(schema)
	if boolean(p, "global_search") {
		namespace = "left(n.nspname,3)<>'pg_' AND n.nspname<>'information_schema' AND has_schema_privilege(n.oid,'USAGE')"
		// Do not leave an unused parameter in a PostgreSQL prepared statement.
		args = nil
	}
	queries := []string{}
	const emptyFields = "NULL::text AS parent_schema,NULL::text AS parent_name"
	if wanted["table"] || wanted["view"] || wanted["sequence"] {
		relKinds := []string{}
		if wanted["table"] {
			relKinds = append(relKinds, "'r'", "'p'", "'f'")
		}
		if wanted["view"] {
			relKinds = append(relKinds, "'v'", "'m'")
		}
		if wanted["sequence"] {
			relKinds = append(relKinds, "'S'")
		}
		queries = append(queries, `SELECT c.relname::text AS name,
 CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'view' WHEN 'S' THEN 'sequence' ELSE 'table' END AS kind,
 n.nspname::text AS schema,`+emptyFields+`,obj_description(c.oid,'pg_class') AS comment,
 CASE c.relkind WHEN 'v' THEN 'VIEW' WHEN 'm' THEN 'MATERIALIZED_VIEW' WHEN 'S' THEN 'SEQUENCE' ELSE 'TABLE' END AS data_type,
 NULL::text AS signature FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE `+namespace+` AND c.relkind IN (`+strings.Join(relKinds, ",")+`)`)
	}
	if wanted["column"] {
		parent := str(p, "parent_name")
		if parent == "" {
			return nil, errors.New("parent_name is required for column completion")
		}
		queries = append(queries, `SELECT a.attname::text AS name,'column'::text AS kind,n.nspname::text AS schema,
 n.nspname::text AS parent_schema,c.relname::text AS parent_name,col_description(c.oid,a.attnum) AS comment,
 format_type(a.atttypid,a.atttypmod) AS data_type,NULL::text AS signature
 FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE `+namespace+` AND c.relname=`+bind(parent)+` AND a.attnum>0 AND NOT a.attisdropped`)
	}
	if wanted["routine"] || wanted["function"] || wanted["procedure"] {
		version, err := postgresVersion(ctx, db)
		if err != nil {
			return nil, err
		}
		kind, condition := "'function'::text", "NOT p.proisagg"
		if version >= 110000 {
			kind = "CASE p.prokind WHEN 'p' THEN 'procedure' ELSE 'function' END"
			condition = "p.prokind IN ('f','p','w')"
			if !wanted["routine"] {
				if !wanted["function"] {
					condition = "p.prokind='p'"
				} else if !wanted["procedure"] {
					condition = "p.prokind IN ('f','w')"
				}
			}
		} else if wanted["procedure"] && !wanted["routine"] && !wanted["function"] {
			condition = "false"
		}
		queries = append(queries, `SELECT p.proname::text AS name,`+kind+` AS kind,n.nspname::text AS schema,
 `+emptyFields+`,obj_description(p.oid,'pg_proc') AS comment,pg_get_function_result(p.oid) AS data_type,
 pg_get_function_identity_arguments(p.oid) AS signature FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
 WHERE `+namespace+` AND `+condition)
	}
	if wanted["schema"] {
		queries = append(queries, `SELECT n.nspname::text AS name,'schema'::text AS kind,n.nspname::text AS schema,
 `+emptyFields+`,obj_description(n.oid,'pg_namespace') AS comment,NULL::text AS data_type,NULL::text AS signature
 FROM pg_namespace n WHERE left(n.nspname,3)<>'pg_' AND n.nspname<>'information_schema' AND has_schema_privilege(n.oid,'USAGE')`)
	}
	if wanted["database"] {
		queries = append(queries, `SELECT d.datname::text AS name,'database'::text AS kind,NULL::text AS schema,
 `+emptyFields+`,NULL::text AS comment,NULL::text AS data_type,NULL::text AS signature
 FROM pg_database d WHERE NOT d.datistemplate AND has_database_privilege(d.oid,'CONNECT')`)
	}
	if len(queries) == 0 {
		return record{"candidates": []record{}, "incomplete": false, "fallback_used": false}, nil
	}
	query := "SELECT * FROM (" + strings.Join(queries, " UNION ALL ") + ") AS candidates"
	mask := str(p, "mask")
	if mask != "" {
		needle := bind(mask)
		expression := func(field string) string {
			value, search := field, needle
			if !boolean(p, "case_sensitive") {
				value, search = "lower("+field+")", "lower("+needle+"::text)"
			}
			if matchMode == "contains" {
				return "strpos(" + value + "," + search + ")>0"
			}
			return "left(" + value + ",length(" + search + "))=" + search
		}
		query += " WHERE (" + expression("name")
		if boolean(p, "search_in_comments") {
			query += " OR " + expression("COALESCE(comment,'')")
		}
		query += ")"
	}
	// Schema/database-only requests do not use the namespace predicate above.
	// Keep its parameter referenced for every kind combination.
	preferred := "$1"
	if boolean(p, "global_search") {
		preferred = bind(schema)
	}
	query += " ORDER BY CASE WHEN schema=" + preferred + " THEN 0 ELSE 1 END,name,kind,signature LIMIT " + bind(limit+1)
	candidates, err := records(ctx, db, query, args...)
	if err != nil {
		return nil, err
	}
	incomplete := len(candidates) > limit
	if incomplete {
		candidates = candidates[:limit]
	}
	for _, candidate := range candidates {
		candidate["database"] = database
	}
	return record{"candidates": candidates, "incomplete": incomplete, "fallback_used": false}, nil
}
