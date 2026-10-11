package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func catalogStrings(value any) []string {
	out := []string{}
	if value == nil {
		return out
	}
	_ = json.Unmarshal([]byte(valueString(value)), &out)
	return out
}
func schemaAdvanced(ctx context.Context, db *sql.DB, c Config, database, schema, table, path string, p map[string]any) (any, bool, error) {
	part := strings.TrimPrefix(path, "/api/schema/")
	switch part {
	case "constraints", "functions", "sequences", "rules", "owners", "table-owner", "extensions", "available-extensions", "event-triggers", "invalid-indexes", "partitions", "subpartitions", "table-partition-status", "table-partitioning":
	default:
		return nil, false, nil
	}
	if c.Type == "postgres" {
		result, err := postgresAdvanced(ctx, db, schema, table, part, p)
		return result, true, err
	}
	if c.Type == "mysql" {
		result, err := mysqlAdvanced(ctx, db, database, table, part, p)
		return result, true, err
	}
	if c.Type == "sqlite" && part == "constraints" {
		result, err := sqliteConstraints(ctx, db, schema, table)
		return result, true, err
	}
	if c.Type == "sqlite" && (part == "table-owner" || part == "table-partitioning") {
		if part == "table-owner" {
			return nil, true, nil
		}
		return record{"isPartitioned": false, "isPartition": false, "keyColumns": []string{}, "partitions": []record{}}, true, nil
	}
	return nil, false, nil
}
func postgresAdvanced(ctx context.Context, db *sql.DB, schema, table, part string, p map[string]any) (any, error) {
	version, err := postgresVersion(ctx, db)
	if err != nil {
		return nil, err
	}
	switch part {
	case "constraints":
		raw, err := records(ctx, db, `SELECT con.conname AS name,CASE con.contype WHEN 'p' THEN 'PRIMARY KEY' WHEN 'u' THEN 'UNIQUE' WHEN 'f' THEN 'FOREIGN KEY' WHEN 'c' THEN 'CHECK' WHEN 'x' THEN 'EXCLUDE' WHEN 'n' THEN 'NOT NULL' ELSE con.contype::text END AS constraint_type,
 pg_get_constraintdef(con.oid,true) AS definition,
 `+postgresConstraintColumnsSQL("conkey", "conrelid")+` AS columns,
 rn.nspname AS ref_schema,rc.relname AS ref_table,
 `+postgresConstraintColumnsSQL("confkey", "confrelid")+` AS ref_columns,
 CASE con.confmatchtype WHEN 'f' THEN 'FULL' WHEN 'p' THEN 'PARTIAL' WHEN 's' THEN 'SIMPLE' END AS match_type,
 CASE con.confupdtype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT' WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END AS on_update,
 CASE con.confdeltype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT' WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END AS on_delete,
 con.condeferrable AS deferrable,con.condeferred AS initially_deferred,true AS enabled,con.convalidated AS valid
 FROM pg_constraint con JOIN pg_class t ON t.oid=con.conrelid JOIN pg_namespace n ON n.oid=t.relnamespace LEFT JOIN pg_class rc ON rc.oid=con.confrelid LEFT JOIN pg_namespace rn ON rn.oid=rc.relnamespace
 WHERE n.nspname=$1 AND t.relname=$2 ORDER BY con.conname`, schema, table)
		if err != nil {
			return nil, err
		}
		for _, r := range raw {
			r["columns"] = catalogStrings(r["columns"])
			r["ref_columns"] = catalogStrings(r["ref_columns"])
		}
		return raw, nil
	case "functions":
		kind, condition := "CASE p.prokind WHEN 'p' THEN 'PROCEDURE' ELSE 'FUNCTION' END", "p.prokind IN ('f','p','w')"
		if version < 110000 {
			kind, condition = "'FUNCTION'::text", "NOT p.proisagg"
		}
		raw, err := records(ctx, db, `SELECT p.proname AS name,`+kind+` AS function_type,pg_get_function_result(p.oid) AS data_type,pg_get_functiondef(p.oid) AS definition,pg_get_function_identity_arguments(p.oid) AS arguments FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$1 AND `+condition+` ORDER BY p.proname,pg_get_function_identity_arguments(p.oid)`, schema)
		if err != nil {
			return nil, err
		}
		for _, r := range raw {
			definition := valueString(r["definition"])
			for _, kind := range []string{"FUNCTION", "PROCEDURE"} {
				for _, prefix := range []string{"CREATE OR REPLACE ", "CREATE "} {
					for _, namespace := range []string{quoteIdentifier(schema, "postgres"), schema} {
						definition = strings.Replace(definition, prefix+kind+" "+namespace+".", prefix+kind+" ", 1)
					}
				}
			}
			r["definition"] = definition
			if r["data_type"] == nil {
				r["data_type"] = ""
			}
		}
		return raw, nil
	case "sequences":
		if version < 100000 {
			raw, err := records(ctx, db, "SELECT c.relname AS name FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind='S' ORDER BY c.relname", schema)
			if err != nil {
				return nil, err
			}
			for _, sequence := range raw {
				options, err := postgresLegacySequenceOptions(ctx, db, schema, valueString(sequence["name"]))
				if err != nil {
					return nil, err
				}
				sequence["data_type"] = "bigint"
				for output, input := range map[string]string{"start_value": "seqstart", "min_value": "seqmin", "max_value": "seqmax", "increment": "seqincrement"} {
					sequence[output] = valueString(options[input])
				}
				sequence["cycle"] = options["seqcycle"]
				if boolean(p, "with_last_values", "withLastValues") {
					sequence["last_value"] = valueString(options["last_value"])
				}
			}
			return raw, nil
		}
		raw, err := records(ctx, db, `SELECT c.relname AS name,format_type(s.seqtypid,NULL) AS data_type,s.seqstart::text AS start_value,s.seqmin::text AS min_value,s.seqmax::text AS max_value,s.seqincrement::text AS increment,s.seqcycle AS cycle FROM pg_sequence s JOIN pg_class c ON c.oid=s.seqrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 ORDER BY c.relname`, schema)
		if err != nil {
			return nil, err
		}
		if boolean(p, "with_last_values", "withLastValues") {
			values, e := records(ctx, db, "SELECT sequencename AS name,last_value::text AS last_value FROM pg_sequences WHERE schemaname=$1", schema)
			if e != nil {
				return nil, e
			}
			byName := map[string]any{}
			for _, v := range values {
				byName[valueString(v["name"])] = v["last_value"]
			}
			for _, r := range raw {
				if value := byName[valueString(r["name"])]; value != nil {
					r["last_value"] = value
				}
			}
		}
		return raw, nil
	case "rules":
		return records(ctx, db, "SELECT rulename AS name,tablename AS table_name,definition FROM pg_rules WHERE schemaname=$1 ORDER BY tablename,rulename", schema)
	case "owners":
		return records(ctx, db, `SELECT c.relname AS object_name,CASE c.relkind WHEN 'v' THEN 'VIEW' WHEN 'm' THEN 'MATERIALIZED VIEW' WHEN 'S' THEN 'SEQUENCE' WHEN 'f' THEN 'FOREIGN TABLE' ELSE 'TABLE' END AS object_type,pg_get_userbyid(c.relowner) AS owner FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','p','v','m','S','f') ORDER BY c.relname`, schema)
	case "table-owner":
		raw, err := records(ctx, db, "SELECT pg_get_userbyid(c.relowner) AS owner FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2", schema, table)
		if err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			return nil, nil
		}
		return raw[0]["owner"], nil
	case "extensions":
		sql := `SELECT e.extname AS name,e.extversion AS version,obj_description(e.oid,'pg_extension') AS comment,n.nspname AS schema FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace`
		args := []any{}
		if str(p, "schema") != "" {
			sql += " WHERE n.nspname=$1"
			args = append(args, schema)
		}
		return records(ctx, db, sql+" ORDER BY n.nspname,e.extname", args...)
	case "available-extensions":
		return records(ctx, db, "SELECT name,default_version AS version,comment,NULL::text AS schema,installed_version FROM pg_available_extensions ORDER BY name")
	case "event-triggers":
		if version < 90300 {
			return []record{}, nil
		}
		raw, err := records(ctx, db, `SELECT e.evtname AS name,e.evtevent AS event,pg_get_userbyid(e.evtowner) AS owner,format('%I.%I()',n.nspname,p.proname) AS function,e.evtenabled::text AS enabled,array_to_json(e.evttags)::text AS tags,obj_description(e.oid,'pg_event_trigger') AS comment FROM pg_event_trigger e JOIN pg_proc p ON p.oid=e.evtfoid JOIN pg_namespace n ON n.oid=p.pronamespace ORDER BY e.evtname`)
		if err != nil {
			return nil, err
		}
		for _, r := range raw {
			tags := catalogStrings(r["tags"])
			if r["tags"] != nil {
				r["tags"] = tags
			}
			source := "CREATE EVENT TRIGGER " + pgIdent(valueString(r["name"])) + " ON " + valueString(r["event"])
			if len(tags) > 0 {
				literals := []string{}
				for _, tag := range tags {
					literals = append(literals, pgLiteral(tag))
				}
				source += " WHEN TAG IN (" + strings.Join(literals, ", ") + ")"
			}
			source += " EXECUTE FUNCTION " + valueString(r["function"]) + ";"
			r["source"] = source
		}
		return raw, nil
	case "invalid-indexes":
		raw, err := records(ctx, db, "SELECT i.relname AS name FROM pg_index x JOIN pg_class t ON t.oid=x.indrelid JOIN pg_namespace n ON n.oid=t.relnamespace JOIN pg_class i ON i.oid=x.indexrelid WHERE n.nspname=$1 AND t.relname=$2 AND NOT x.indisvalid ORDER BY i.relname", schema, table)
		if err != nil {
			return nil, err
		}
		names := []string{}
		for _, r := range raw {
			names = append(names, valueString(r["name"]))
		}
		return names, nil
	case "table-partition-status":
		if version < 100000 {
			return record{"isPartitionedParent": false, "isPartition": false}, nil
		}
		raw, err := records(ctx, db, "SELECT c.relkind='p' AS parent,c.relispartition AS child FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2", schema, table)
		if err != nil {
			return nil, err
		}
		out := record{"isPartitionedParent": false, "isPartition": false}
		if len(raw) > 0 {
			out["isPartitionedParent"] = valueBool(raw[0]["parent"])
			out["isPartition"] = valueBool(raw[0]["child"])
		}
		return out, nil
	case "table-partitioning":
		return postgresPartitioning(ctx, db, schema, table)
	case "partitions", "subpartitions":
		tree, err := postgresPartitioning(ctx, db, schema, table)
		if err != nil {
			return nil, err
		}
		out := []record{}
		for i, node := range tree["partitions"].([]record) {
			out = append(out, record{"name": node["name"], "position": i + 1, "value": node["boundDefinition"], "partition_type": strings.ToUpper(valueString(tree["strategy"])), "partition_key": tree["keyDefinition"]})
		}
		return out, nil
	}
	return nil, unsupported("PostgreSQL schema metadata " + part)
}
func mysqlAdvanced(ctx context.Context, db *sql.DB, database, table, part string, p map[string]any) (any, error) {
	switch part {
	case "functions":
		raw, err := records(ctx, db, "SELECT routine_name AS name,routine_type AS function_type,COALESCE(dtd_identifier,'') AS data_type FROM information_schema.routines WHERE routine_schema=? ORDER BY routine_name,routine_type", database)
		if err != nil {
			return nil, err
		}
		for _, r := range raw {
			kind := valueString(r["function_type"])
			name := quoteIdentifier(database, "mysql") + "." + quoteIdentifier(valueString(r["name"]), "mysql")
			create, err := records(ctx, db, "SHOW CREATE "+kind+" "+name)
			if err != nil {
				return nil, err
			}
			if len(create) == 0 {
				return nil, errors.New("Routine exists but CREATE source is unavailable")
			}
			definition := valueString(create[0]["Create Function"])
			if kind == "PROCEDURE" {
				definition = valueString(create[0]["Create Procedure"])
			}
			if definition == "" {
				return nil, errors.New("Routine definition is hidden by server permissions")
			}
			r["definition"] = definition
			args, err := records(ctx, db, "SELECT parameter_mode,parameter_name,dtd_identifier FROM information_schema.parameters WHERE specific_schema=? AND specific_name=? AND ordinal_position>0 ORDER BY ordinal_position", database, r["name"])
			if err != nil {
				return nil, err
			}
			arguments := []string{}
			for _, arg := range args {
				definition := valueString(arg["parameter_mode"]) + " " + quoteIdentifier(valueString(arg["parameter_name"]), "mysql") + " " + valueString(arg["dtd_identifier"])
				arguments = append(arguments, strings.TrimSpace(definition))
			}
			r["arguments"] = strings.Join(arguments, ", ")
		}
		return raw, nil
	case "constraints":
		return mysqlConstraints(ctx, db, database, table)
	case "partitions", "subpartitions":
		name, pos, description, method, expression := "partition_name", "partition_ordinal_position", "partition_description", "partition_method", "partition_expression"
		if part == "subpartitions" {
			name, pos, method, expression = "subpartition_name", "subpartition_ordinal_position", "subpartition_method", "subpartition_expression"
		}
		return records(ctx, db, fmt.Sprintf("SELECT %s AS name,%s AS position,%s AS value,%s AS partition_type,%s AS partition_key FROM information_schema.partitions WHERE table_schema=? AND table_name=? AND %s IS NOT NULL ORDER BY partition_ordinal_position,subpartition_ordinal_position", name, pos, description, method, expression, name), database, table)
	case "table-partition-status":
		raw, err := records(ctx, db, "SELECT COUNT(*) AS n FROM information_schema.partitions WHERE table_schema=? AND table_name=? AND partition_name IS NOT NULL", database, table)
		if err != nil {
			return nil, err
		}
		return record{"isPartitionedParent": len(raw) > 0 && valueInt(raw[0]["n"]) > 0, "isPartition": false}, nil
	case "table-partitioning":
		return nil, unsupported("PostgreSQL structured partition view on MySQL")
	case "table-owner":
		return nil, nil
	case "invalid-indexes":
		return []string{}, nil
	case "sequences", "rules", "owners", "extensions", "available-extensions", "event-triggers":
		return []record{}, nil
	}
	return nil, unsupported("MySQL schema metadata " + part)
}
func mysqlConstraints(ctx context.Context, db *sql.DB, database, table string) ([]record, error) {
	raw, err := records(ctx, db, `SELECT tc.constraint_name AS name,tc.constraint_type,k.column_name,k.referenced_table_schema AS ref_schema,k.referenced_table_name AS ref_table,k.referenced_column_name AS ref_column,rc.match_option AS match_type,rc.update_rule AS on_update,rc.delete_rule AS on_delete FROM information_schema.table_constraints tc LEFT JOIN information_schema.key_column_usage k ON k.constraint_schema=tc.constraint_schema AND k.table_name=tc.table_name AND k.constraint_name=tc.constraint_name LEFT JOIN information_schema.referential_constraints rc ON rc.constraint_schema=tc.constraint_schema AND rc.table_name=tc.table_name AND rc.constraint_name=tc.constraint_name WHERE tc.table_schema=? AND tc.table_name=? ORDER BY tc.constraint_name,k.ordinal_position`, database, table)
	if err != nil {
		return nil, err
	}
	out := []record{}
	byName := map[string]record{}
	for _, r := range raw {
		name := valueString(r["name"])
		item := byName[name]
		if item == nil {
			item = record{"name": name, "constraint_type": r["constraint_type"], "definition": "", "columns": []string{}, "ref_schema": r["ref_schema"], "ref_table": r["ref_table"], "ref_columns": []string{}, "match_type": r["match_type"], "on_update": r["on_update"], "on_delete": r["on_delete"], "deferrable": false, "initially_deferred": false, "enabled": true, "valid": true}
			byName[name] = item
			out = append(out, item)
		}
		if r["column_name"] != nil {
			item["columns"] = append(item["columns"].([]string), valueString(r["column_name"]))
		}
		if r["ref_column"] != nil {
			item["ref_columns"] = append(item["ref_columns"].([]string), valueString(r["ref_column"]))
		}
	}
	quoteCols := func(cols []string) string {
		quoted := []string{}
		for _, col := range cols {
			quoted = append(quoted, quoteIdentifier(col, "mysql"))
		}
		return "(" + strings.Join(quoted, ", ") + ")"
	}
	for _, item := range out {
		kind := valueString(item["constraint_type"])
		definition := kind + " " + quoteCols(item["columns"].([]string))
		if kind == "FOREIGN KEY" {
			definition += " REFERENCES " + quoteIdentifier(valueString(item["ref_schema"]), "mysql") + "." + quoteIdentifier(valueString(item["ref_table"]), "mysql") + " " + quoteCols(item["ref_columns"].([]string)) + " ON UPDATE " + valueString(item["on_update"]) + " ON DELETE " + valueString(item["on_delete"])
		}
		if kind == "CHECK" {
			check, err := records(ctx, db, "SELECT check_clause FROM information_schema.check_constraints WHERE constraint_schema=? AND constraint_name=?", database, item["name"])
			if err != nil {
				return nil, err
			}
			if len(check) == 0 {
				return nil, errors.New("Check constraint has no catalog definition")
			}
			definition = "CHECK (" + valueString(check[0]["check_clause"]) + ")"
		}
		item["definition"] = definition
	}
	return out, nil
}

// Pg bounds contain literal SQL text. Split nested calls/ROW bounds without
// splitting a quoted comma, and retain the raw catalog string as fallback.
func splitPartitionValues(text string) []string {
	out := []string{}
	start, depth := 0, 0
	quote := byte(0)
	for i := 0; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			if c == quote {
				if i+1 < len(text) && text[i+1] == quote {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if c == '(' {
			depth++
		}
		if c == ')' {
			depth--
		}
		if c == ',' && depth == 0 {
			out = append(out, strings.TrimSpace(text[start:i]))
			start = i + 1
		}
	}
	if strings.TrimSpace(text[start:]) != "" {
		out = append(out, strings.TrimSpace(text[start:]))
	}
	return out
}
func pgPartitionBound(raw string) record {
	raw = strings.TrimSpace(raw)
	if strings.EqualFold(raw, "DEFAULT") {
		return record{"kind": "default"}
	}
	if matches := regexp.MustCompile(`(?is)^FOR VALUES FROM \((.*)\) TO \((.*)\)$`).FindStringSubmatch(raw); len(matches) > 0 {
		return record{"kind": "range", "from": splitPartitionValues(matches[1]), "to": splitPartitionValues(matches[2])}
	}
	if matches := regexp.MustCompile(`(?is)^FOR VALUES IN \((.*)\)$`).FindStringSubmatch(raw); len(matches) > 0 {
		return record{"kind": "list", "values": splitPartitionValues(matches[1])}
	}
	if matches := regexp.MustCompile(`(?i)^FOR VALUES WITH \(modulus ([0-9]+), remainder ([0-9]+)\)$`).FindStringSubmatch(raw); len(matches) > 0 {
		m, _ := strconv.Atoi(matches[1])
		r, _ := strconv.Atoi(matches[2])
		return record{"kind": "hash", "modulus": m, "remainder": r}
	}
	return nil
}
func postgresPartitioning(ctx context.Context, db *sql.DB, schema, table string) (record, error) {
	version, err := postgresVersion(ctx, db)
	if err != nil {
		return nil, err
	}
	if version < 100000 {
		return record{"isPartitioned": false, "isPartition": false, "keyColumns": []string{}, "partitions": []record{}, "serverVersionNum": version}, nil
	}
	raw, err := records(ctx, db, `WITH RECURSIVE tree(oid,parent_oid,depth,path) AS (SELECT c.oid,NULL::oid,0,ARRAY[c.oid] FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND c.relkind IN ('r','p','f') UNION ALL SELECT i.inhrelid,i.inhparent,t.depth+1,t.path||i.inhrelid FROM pg_inherits i JOIN tree t ON t.oid=i.inhparent WHERE NOT i.inhrelid=ANY(t.path) AND t.depth<64)
 SELECT c.oid::bigint AS oid,t.parent_oid::bigint AS parent_oid,n.nspname AS schema,c.relname AS name,c.relkind='p' AS partitioned,c.relispartition AS is_partition,pt.partstrat::text AS strategy,pg_get_partkeydef(c.oid) AS key_definition,pg_get_expr(c.relpartbound,c.oid,true) AS bound,c.reltuples::bigint AS row_estimate,pg_total_relation_size(c.oid)::bigint AS total_bytes,
 COALESCE((SELECT json_agg(a.attname ORDER BY k.ordinality)::text FROM unnest(pt.partattrs) WITH ORDINALITY AS k(attnum,ordinality) JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=k.attnum WHERE k.attnum<>0),'[]') AS key_columns,
 EXISTS(SELECT 1 FROM unnest(pt.partattrs) AS k(attnum) WHERE k.attnum=0) AS key_expression,
 current_setting('server_version_num')::int AS version FROM tree t JOIN pg_class c ON c.oid=t.oid JOIN pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_partitioned_table pt ON pt.partrelid=c.oid ORDER BY t.depth,n.nspname,c.relname`, schema, table)
	if err != nil {
		return nil, err
	}
	out := record{"isPartitioned": false, "isPartition": false, "keyColumns": []string{}, "partitions": []record{}}
	if len(raw) == 0 {
		return out, nil
	}
	root := raw[0]
	out["isPartitioned"] = valueBool(root["partitioned"])
	out["isPartition"] = valueBool(root["is_partition"])
	out["serverVersionNum"] = root["version"]
	strategy := func(v any) string {
		switch valueString(v) {
		case "r":
			return "range"
		case "l":
			return "list"
		case "h":
			return "hash"
		}
		return ""
	}
	if s := strategy(root["strategy"]); s != "" {
		out["strategy"] = s
		out["keyDefinition"] = root["key_definition"]
		out["keyColumns"] = catalogStrings(root["key_columns"])
		if valueBool(root["key_expression"]) {
			expression := valueString(root["key_definition"])
			if i := strings.IndexByte(expression, '('); i >= 0 {
				expression = strings.TrimSuffix(strings.TrimSpace(expression[i+1:]), ")")
			}
			out["keyExpression"] = expression
		}
	}
	if valueBool(root["is_partition"]) {
		parents, e := records(ctx, db, "SELECT n.nspname AS schema,c.relname AS name FROM pg_inherits i JOIN pg_class c ON c.oid=i.inhparent JOIN pg_namespace n ON n.oid=c.relnamespace WHERE i.inhrelid=$1 ORDER BY n.nspname,c.relname LIMIT 1", root["oid"])
		if e != nil {
			return nil, e
		}
		if len(parents) > 0 {
			out["parentSchema"] = parents[0]["schema"]
			out["parentTable"] = parents[0]["name"]
			out["parent"] = valueString(parents[0]["schema"]) + "." + valueString(parents[0]["name"])
		}
		if bound := pgPartitionBound(valueString(root["bound"])); bound != nil {
			out["ownBound"] = bound
		}
	}
	nodes := map[int64]record{}
	children := map[int64][]int64{}
	for _, r := range raw {
		oid := valueInt(r["oid"])
		node := record{"schema": r["schema"], "name": r["name"], "isLeaf": !valueBool(r["partitioned"]), "rowEstimate": r["row_estimate"], "totalBytes": r["total_bytes"], "children": []record{}}
		if bound := valueString(r["bound"]); bound != "" {
			node["boundDefinition"] = bound
			if parsed := pgPartitionBound(bound); parsed != nil {
				node["bound"] = parsed
			}
		}
		if kind := strategy(r["strategy"]); kind != "" {
			node["strategy"] = kind
			node["keyDefinition"] = r["key_definition"]
		}
		nodes[oid] = node
		if r["parent_oid"] != nil {
			children[valueInt(r["parent_oid"])] = append(children[valueInt(r["parent_oid"])], oid)
		}
	}
	var build func(int64, map[int64]bool) []record
	build = func(oid int64, seen map[int64]bool) []record {
		out := []record{}
		for _, child := range children[oid] {
			if seen[child] {
				continue
			}
			seen[child] = true
			node := nodes[child]
			node["children"] = build(child, seen)
			out = append(out, node)
		}
		return out
	}
	partitions := build(valueInt(root["oid"]), map[int64]bool{})
	out["partitions"] = partitions
	for _, node := range partitions {
		if bound, ok := node["bound"].(record); ok && bound["kind"] == "default" {
			out["defaultPartition"] = node["name"]
		}
	}
	return out, nil
}
func sqliteConstraints(ctx context.Context, db *sql.DB, schema, table string) ([]record, error) {
	idx, err := indexes(ctx, db, "sqlite", "", schema, table)
	if err != nil {
		return nil, err
	}
	fks, err := foreignKeys(ctx, db, "sqlite", "", schema, table)
	if err != nil {
		return nil, err
	}
	out := []record{}
	base := func(name, kind, definition string, columns []string) record {
		return record{"name": name, "constraint_type": kind, "definition": definition, "columns": columns, "ref_schema": nil, "ref_table": nil, "ref_columns": []string{}, "match_type": nil, "on_update": nil, "on_delete": nil, "deferrable": false, "initially_deferred": false, "enabled": true, "valid": true}
	}
	for _, i := range idx {
		if !valueBool(i["constraint_backed"]) {
			continue
		}
		kind := "UNIQUE"
		if valueBool(i["is_primary"]) {
			kind = "PRIMARY KEY"
		}
		cols := i["columns"].([]string)
		quoted := []string{}
		for _, c := range cols {
			quoted = append(quoted, quoteIdentifier(c, "sqlite"))
		}
		out = append(out, base(valueString(i["name"]), kind, kind+" ("+strings.Join(quoted, ", ")+")", cols))
	}
	byName := map[string]record{}
	for _, fk := range fks {
		name := valueString(fk["name"])
		item := byName[name]
		if item == nil {
			item = base(name, "FOREIGN KEY", "", []string{})
			item["ref_schema"] = fk["ref_schema"]
			item["ref_table"] = fk["ref_table"]
			item["on_update"] = fk["on_update"]
			item["on_delete"] = fk["on_delete"]
			byName[name] = item
			out = append(out, item)
		}
		item["columns"] = append(item["columns"].([]string), valueString(fk["column"]))
		item["ref_columns"] = append(item["ref_columns"].([]string), valueString(fk["ref_column"]))
	}
	for _, item := range byName {
		item["definition"] = "FOREIGN KEY (" + strings.Join(item["columns"].([]string), ", ") + ") REFERENCES " + quoteIdentifier(valueString(item["ref_table"]), "sqlite") + " (" + strings.Join(item["ref_columns"].([]string), ", ") + ")"
	}
	ddlRows, err := records(ctx, db, "SELECT sql FROM "+quoteIdentifier(schema, "sqlite")+".sqlite_schema WHERE name=? AND type='table'", table)
	if err != nil {
		return nil, err
	}
	if len(ddlRows) > 0 {
		checks := sqliteCheckDefinitions(valueString(ddlRows[0]["sql"]))
		for i, check := range checks {
			name := check["name"]
			if name == "" {
				name = fmt.Sprintf("check_%d", i+1)
			}
			out = append(out, base(name, "CHECK", check["definition"], []string{}))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return valueString(out[i]["name"]) < valueString(out[j]["name"]) })
	return out, nil
}

// SQLite has no CHECK catalog. Read each balanced expression from its actual
// CREATE source while skipping quoted literals and identifier text.
func sqliteCheckDefinitions(ddl string) []map[string]string {
	out := []map[string]string{}
	quote := byte(0)
	lastDelimiter := 0
	for i := 0; i < len(ddl); i++ {
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
		if c == ',' {
			lastDelimiter = i + 1
		}
		if i+5 > len(ddl) || !strings.EqualFold(ddl[i:i+5], "CHECK") {
			continue
		}
		if i > 0 && (ddl[i-1] == '_' || ddl[i-1] >= 'a' && ddl[i-1] <= 'z' || ddl[i-1] >= 'A' && ddl[i-1] <= 'Z') {
			continue
		}
		j := i + 5
		for j < len(ddl) && (ddl[j] == ' ' || ddl[j] == '\n' || ddl[j] == '\t') {
			j++
		}
		if j >= len(ddl) || ddl[j] != '(' {
			continue
		}
		start := i
		depth := 1
		j++
		q := byte(0)
		for j < len(ddl) && depth > 0 {
			c := ddl[j]
			if q != 0 {
				close := q
				if q == '[' {
					close = ']'
				}
				if c == close {
					if j+1 < len(ddl) && ddl[j+1] == close {
						j += 2
						continue
					}
					q = 0
				}
				j++
				continue
			}
			if c == '\'' || c == '"' || c == '`' || c == '[' {
				q = c
			} else if c == '(' {
				depth++
			} else if c == ')' {
				depth--
			}
			j++
		}
		if depth != 0 {
			break
		}
		name := ""
		prefix := ddl[lastDelimiter:start]
		matches := regexp.MustCompile(`(?is)\bCONSTRAINT\s+("(?:[^"]|"")+"|` + "`(?:[^`]|``)+`" + `|\[[^]]+\]|[A-Za-z_][A-Za-z0-9_]*)\s*$`).FindStringSubmatch(prefix)
		if len(matches) > 0 {
			name = strings.Trim(matches[1], "\"`[]")
			name = strings.ReplaceAll(strings.ReplaceAll(name, "\"\"", "\""), "``", "`")
		}
		out = append(out, map[string]string{"name": name, "definition": ddl[start:j]})
		i = j - 1
	}
	return out
}
