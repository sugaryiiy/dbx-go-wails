package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Partial, expression and invalid unique indexes cannot identify arbitrary rows
// or serve as foreign-key targets. INCLUDE columns do not belong to the key.
func referenceableIndex(r record) bool {
	if !valueBool(r["is_unique"]) || valueString(r["filter"]) != "" {
		return false
	}
	if valid, ok := r["is_valid"]; ok && !valueBool(valid) {
		return false
	}
	if immediate, ok := r["is_immediate"]; ok && !valueBool(immediate) {
		return false
	}
	expressions, _ := r["key_is_expression"].([]bool)
	for _, expression := range expressions {
		if expression {
			return false
		}
	}
	cols, ok := r["columns"].([]string)
	if !ok || len(cols) == 0 {
		return false
	}
	for _, col := range cols {
		if col == "" {
			return false
		}
	}
	return true
}

// Deparsing with a fixed search_path makes references to user types, functions,
// relations and operator classes schema-qualified. A read-only snapshot also
// prevents a pool user's transient search_path from affecting generated DDL.
func postgresCatalogSnapshot(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "SET LOCAL search_path = pg_catalog"); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func postgresIndexes(ctx context.Context, db *sql.DB, schema, table string) ([]record, error) {
	tx, err := postgresCatalogSnapshot(ctx, db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	raw, err := records(ctx, tx, `SELECT ci.relname AS name, i.indisunique AND i.indisvalid AS is_unique,
 i.indisprimary AS is_primary, i.indisvalid AS is_valid, i.indimmediate AS is_immediate,
 am.amname AS index_type, pg_get_expr(i.indpred,i.indrelid) AS filter,
 obj_description(ci.oid,'pg_class') AS comment, pos.n > i.indnkeyatts AS included,
 COALESCE(a.attname::text,pg_get_indexdef(i.indexrelid,pos.n,false)) AS column_name,
 i.indkey[pos.n-1] = 0 AS is_expression, i.indoption[pos.n-1] AS key_option,
 CASE WHEN NOT op.opcdefault THEN quote_ident(opn.nspname)||'.'||quote_ident(op.opcname) END AS opclass,
 EXISTS(SELECT 1 FROM pg_constraint con WHERE con.conindid=i.indexrelid AND con.conrelid=i.indrelid AND con.contype IN ('p','u','x')) AS constraint_backed
 FROM pg_index i JOIN pg_class t ON t.oid=i.indrelid JOIN pg_namespace ns ON ns.oid=t.relnamespace
 JOIN pg_class ci ON ci.oid=i.indexrelid JOIN pg_am am ON am.oid=ci.relam
 CROSS JOIN LATERAL generate_series(1,i.indnatts) AS pos(n)
 LEFT JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum=i.indkey[pos.n-1] AND a.attnum>0
 LEFT JOIN pg_opclass op ON op.oid=i.indclass[pos.n-1] LEFT JOIN pg_namespace opn ON opn.oid=op.opcnamespace
 WHERE ns.nspname=$1 AND t.relname=$2 ORDER BY ci.relname,pos.n`, schema, table)
	if err != nil {
		return nil, err
	}
	out := []record{}
	byName := map[string]record{}
	for _, r := range raw {
		name := valueString(r["name"])
		item := byName[name]
		if item == nil {
			item = record{"name": name, "columns": []string{}, "is_unique": valueBool(r["is_unique"]), "is_primary": valueBool(r["is_primary"]), "is_valid": valueBool(r["is_valid"]), "is_immediate": valueBool(r["is_immediate"]), "index_type": r["index_type"], "filter": r["filter"], "comment": r["comment"], "included_columns": []string{}, "key_is_expression": []bool{}, "key_options": []int64{}, "column_opclasses": []any{}, "constraint_backed": valueBool(r["constraint_backed"])}
			byName[name] = item
			out = append(out, item)
		}
		col := valueString(r["column_name"])
		if valueBool(r["included"]) {
			item["included_columns"] = append(item["included_columns"].([]string), col)
			continue
		}
		item["columns"] = append(item["columns"].([]string), col)
		item["key_is_expression"] = append(item["key_is_expression"].([]bool), valueBool(r["is_expression"]))
		item["key_options"] = append(item["key_options"].([]int64), valueInt(r["key_option"]))
		item["column_opclasses"] = append(item["column_opclasses"].([]any), r["opclass"])
	}
	return out, tx.Commit()
}

func pgIdent(name string) string             { return quoteIdentifier(name, "postgres") }
func pgQualified(schema, name string) string { return pgIdent(schema) + "." + pgIdent(name) }
func pgLiteral(value string) string {
	// E strings do not depend on standard_conforming_strings in the destination.
	return "E'" + strings.ReplaceAll(strings.ReplaceAll(value, "\\", "\\\\"), "'", "''") + "'"
}

func pgOptions(raw any) (string, error) {
	if raw == nil {
		return "", nil
	}
	var values []string
	if err := json.Unmarshal([]byte(valueString(raw)), &values); err != nil {
		return "", err
	}
	options := make([]string, 0, len(values))
	for _, value := range values {
		name, val, ok := strings.Cut(value, "=")
		if !ok || name == "" {
			return "", fmt.Errorf("invalid PostgreSQL catalog option %q", value)
		}
		// reloptions may include toast.autovacuum_*; quote each identifier separately.
		parts := strings.Split(name, ".")
		for i := range parts {
			parts[i] = pgIdent(parts[i])
		}
		options = append(options, strings.Join(parts, ".")+" = "+pgLiteral(val))
	}
	return strings.Join(options, ", "), nil
}

const pgDDLRelationSQL = `SELECT c.oid::bigint AS oid,c.relkind::text AS kind,c.relpersistence::text AS persistence,
 c.relispartition,c.reloftype<>0 AS typed,c.relrowsecurity,c.relforcerowsecurity,
 c.relreplident::text AS replica_identity,am.amname AS access_method,ts.spcname AS tablespace,
 array_to_json(c.reloptions)::text AS options,obj_description(c.oid,'pg_class') AS comment,
 EXISTS(SELECT 1 FROM pg_inherits WHERE inhrelid=c.oid OR inhparent=c.oid) AS inherited,
 EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=c.oid AND NOT tgisinternal) AS triggers,
 EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=c.oid AND tgisinternal AND tgenabled<>'O') AS altered_internal_triggers,
 EXISTS(SELECT 1 FROM pg_rewrite WHERE ev_class=c.oid AND rulename<>'_RETURN') AS rules,
 EXISTS(SELECT 1 FROM pg_policy WHERE polrelid=c.oid) AS policies,
 EXISTS(SELECT 1 FROM pg_depend WHERE classid='pg_class'::regclass AND objid=c.oid AND deptype='e') AS extension_member
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_am am ON am.oid=c.relam
 LEFT JOIN pg_tablespace ts ON ts.oid=c.reltablespace WHERE n.nspname=$1 AND c.relname=$2`

func postgresObjectDDL(ctx context.Context, db *sql.DB, schema, table, requestedType string) (string, error) {
	if table == "" {
		return "", errors.New("table is required")
	}
	tx, err := postgresCatalogSnapshot(ctx, db)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil {
		return "", err
	}
	if version < 120000 {
		return "", unsupported("PostgreSQL DDL extraction requires PostgreSQL 12 or later")
	}
	rels, err := records(ctx, tx, pgDDLRelationSQL, schema, table)
	if err != nil {
		return "", err
	}
	if len(rels) == 0 {
		return "", sql.ErrNoRows
	}
	rel := rels[0]
	kind := valueString(rel["kind"])
	objectType := map[string]string{"r": "TABLE", "v": "VIEW", "m": "MATERIALIZED VIEW"}[kind]
	if objectType == "" {
		return "", unsupported("PostgreSQL DDL for partitioned, foreign or non-table relations")
	}
	requestedType = strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(requestedType)), "_", " ")
	if requestedType != "" && requestedType != objectType {
		return "", fmt.Errorf("requested %s but PostgreSQL relation is %s", requestedType, objectType)
	}
	for _, flag := range []string{"relispartition", "typed", "inherited", "relrowsecurity", "relforcerowsecurity", "policies", "triggers", "altered_internal_triggers", "rules", "extension_member"} {
		if valueBool(rel[flag]) {
			return "", unsupported("PostgreSQL DDL with " + flag)
		}
	}
	if valueString(rel["persistence"]) == "t" {
		return "", unsupported("PostgreSQL temporary relation DDL")
	}
	if method := valueString(rel["access_method"]); method != "" && method != "heap" {
		return "", unsupported("PostgreSQL non-heap table access methods")
	}
	oid := rel["oid"]
	cols, err := records(ctx, tx, `SELECT a.attnum,a.attname AS name,format_type(a.atttypid,a.atttypmod) AS data_type,
 a.attnotnull,a.attidentity::text AS identity,a.attgenerated::text AS generated,pg_get_expr(d.adbin,d.adrelid) AS expression,
 CASE WHEN a.attcollation<>0 AND a.attcollation<>ty.typcollation THEN quote_ident(cn.nspname)||'.'||quote_ident(co.collname) END AS collation,
 a.attstorage::text AS storage,ty.typstorage::text AS default_storage,to_jsonb(a)->>'attcompression' AS compression,
 array_to_json(a.attoptions)::text AS options,a.attstattarget AS statistics,col_description(a.attrelid,a.attnum) AS comment
 FROM pg_attribute a JOIN pg_type ty ON ty.oid=a.atttypid LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
 LEFT JOIN pg_collation co ON co.oid=a.attcollation LEFT JOIN pg_namespace cn ON cn.oid=co.collnamespace
 WHERE a.attrelid=$1 AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, oid)
	if err != nil {
		return "", err
	}
	sequences, err := records(ctx, tx, `SELECT dep.refobjsubid AS attnum,dep.deptype::text AS dependency,
 ns.nspname AS schema,s.relname AS name,s.relpersistence::text AS persistence,format_type(q.seqtypid,NULL) AS data_type,
 q.seqstart,q.seqincrement,q.seqmin,q.seqmax,q.seqcache,q.seqcycle,obj_description(s.oid,'pg_class') AS comment
 FROM pg_depend dep JOIN pg_class s ON s.oid=dep.objid AND s.relkind='S' JOIN pg_namespace ns ON ns.oid=s.relnamespace
 JOIN pg_sequence q ON q.seqrelid=s.oid WHERE dep.refclassid='pg_class'::regclass AND dep.classid='pg_class'::regclass
 AND dep.refobjid=$1 AND dep.deptype IN ('a','i') ORDER BY dep.refobjsubid,s.oid`, oid)
	if err != nil {
		return "", err
	}
	indexes, err := records(ctx, tx, `SELECT c.relname AS name,pg_get_indexdef(i.indexrelid) AS definition,
 i.indisvalid,i.indisready,i.indisreplident,array_to_json(c.reloptions)::text AS options,
 ts.spcname AS tablespace,obj_description(c.oid,'pg_class') AS comment,
 EXISTS(SELECT 1 FROM pg_constraint con WHERE con.conindid=i.indexrelid AND con.conrelid=i.indrelid AND con.contype IN ('p','u','x')) AS constraint_backed
 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid LEFT JOIN pg_tablespace ts ON ts.oid=c.reltablespace
 WHERE i.indrelid=$1 ORDER BY c.relname`, oid)
	if err != nil {
		return "", err
	}
	for _, index := range indexes {
		if !valueBool(index["indisvalid"]) || !valueBool(index["indisready"]) {
			return "", unsupported("PostgreSQL DDL with invalid or unfinished indexes")
		}
	}
	constraints, err := records(ctx, tx, `SELECT conname AS name,contype::text AS kind,pg_get_constraintdef(oid,false) AS definition,
 obj_description(oid,'pg_constraint') AS comment FROM pg_constraint WHERE conrelid=$1 ORDER BY CASE contype WHEN 'p' THEN 0 WHEN 'u' THEN 1 WHEN 'x' THEN 2 WHEN 'c' THEN 3 ELSE 4 END,conname`, oid)
	if err != nil {
		return "", err
	}
	for _, constraint := range constraints {
		if !strings.Contains("pufcxn", valueString(constraint["kind"])) {
			return "", unsupported("PostgreSQL constraint type " + valueString(constraint["kind"]))
		}
	}
	qualified := pgQualified(schema, table)
	statements := []string{"-- Structural DDL; existing schemas and referenced objects are required.\n-- Does not include data, ownership, grants, sequence current values or materialized-view contents."}
	sequenceForIdentity := map[int64]record{}
	for _, sequence := range sequences {
		if valueString(sequence["dependency"]) == "i" {
			sequenceForIdentity[valueInt(sequence["attnum"])] = sequence
			continue
		}
		prefix := "CREATE "
		if valueString(sequence["persistence"]) == "u" {
			prefix += "UNLOGGED "
		}
		statements = append(statements, prefix+"SEQUENCE "+pgQualified(valueString(sequence["schema"]), valueString(sequence["name"]))+" AS "+valueString(sequence["data_type"])+" "+pgSequenceOptions(sequence)+";")
	}
	options, err := pgOptions(rel["options"])
	if err != nil {
		return "", err
	}
	create := "CREATE "
	if valueString(rel["persistence"]) == "u" {
		create += "UNLOGGED "
	}
	create += objectType + " " + qualified
	if kind == "r" {
		defs := []string{}
		for _, col := range cols {
			def := pgIdent(valueString(col["name"])) + " " + valueString(col["data_type"])
			if collation := valueString(col["collation"]); collation != "" {
				def += " COLLATE " + collation
			}
			switch identity := valueString(col["identity"]); identity {
			case "a", "d":
				mode := "ALWAYS"
				if identity == "d" {
					mode = "BY DEFAULT"
				}
				sequence := sequenceForIdentity[valueInt(col["attnum"])]
				if sequence == nil {
					return "", errors.New("PostgreSQL identity column is missing its sequence")
				}
				def += " GENERATED " + mode + " AS IDENTITY (SEQUENCE NAME " + pgQualified(valueString(sequence["schema"]), valueString(sequence["name"])) + " " + pgSequenceOptions(sequence) + ")"
			case "":
				switch generated := valueString(col["generated"]); generated {
				case "s", "v":
					storage := "STORED"
					if generated == "v" {
						storage = "VIRTUAL"
					}
					def += " GENERATED ALWAYS AS (" + valueString(col["expression"]) + ") " + storage
				case "":
					if col["expression"] != nil {
						def += " DEFAULT " + valueString(col["expression"])
					}
				default:
					return "", unsupported("PostgreSQL generated column mode " + generated)
				}
			default:
				return "", unsupported("PostgreSQL identity column mode " + identity)
			}
			if valueBool(col["attnotnull"]) {
				def += " NOT NULL"
			}
			defs = append(defs, def)
		}
		create += " (\n  " + strings.Join(defs, ",\n  ") + "\n)"
	} else {
		names := make([]string, len(cols))
		for i, col := range cols {
			names[i] = pgIdent(valueString(col["name"]))
		}
		if len(names) > 0 {
			create += " (" + strings.Join(names, ", ") + ")"
		}
	}
	if options != "" {
		create += " WITH (" + options + ")"
	}
	if tablespace := valueString(rel["tablespace"]); tablespace != "" && kind != "v" {
		create += " TABLESPACE " + pgIdent(tablespace)
	}
	if kind != "r" {
		var definition string
		if err = tx.QueryRowContext(ctx, "SELECT pg_get_viewdef($1::oid,false)", oid).Scan(&definition); err != nil {
			return "", err
		}
		create += " AS\n" + strings.TrimSuffix(strings.TrimSpace(definition), ";")
		if kind == "m" {
			create += "\nWITH NO DATA"
		}
	}
	statements = append(statements, create+";")
	// ALTER TABLE preserves NOT VALID, DEFERRABLE, composite key ordering and
	// exclusion constraints verbatim from the server's canonical deparser.
	for _, constraint := range constraints {
		if valueString(constraint["kind"]) == "f" {
			continue
		}
		if valueString(constraint["kind"]) == "n" {
			return "", unsupported("PostgreSQL named NOT NULL constraints")
		}
		statements = append(statements, "ALTER TABLE "+qualified+" ADD CONSTRAINT "+pgIdent(valueString(constraint["name"]))+" "+valueString(constraint["definition"])+";")
		pgAppendComment(&statements, "CONSTRAINT "+pgIdent(valueString(constraint["name"]))+" ON "+qualified, constraint["comment"])
	}
	for _, index := range indexes {
		if !valueBool(index["constraint_backed"]) {
			statements = append(statements, valueString(index["definition"])+";")
		} else {
			idxOptions, e := pgOptions(index["options"])
			if e != nil {
				return "", e
			}
			if idxOptions != "" {
				statements = append(statements, "ALTER INDEX "+pgQualified(schema, valueString(index["name"]))+" SET ("+idxOptions+");")
			}
			if ts := valueString(index["tablespace"]); ts != "" {
				statements = append(statements, "ALTER INDEX "+pgQualified(schema, valueString(index["name"]))+" SET TABLESPACE "+pgIdent(ts)+";")
			}
		}
		pgAppendComment(&statements, "INDEX "+pgQualified(schema, valueString(index["name"])), index["comment"])
	}
	// A self-referencing foreign key may depend on a standalone unique index.
	// Restore those indexes before restoring foreign keys.
	for _, constraint := range constraints {
		if valueString(constraint["kind"]) == "f" {
			statements = append(statements, "ALTER TABLE "+qualified+" ADD CONSTRAINT "+pgIdent(valueString(constraint["name"]))+" "+valueString(constraint["definition"])+";")
			pgAppendComment(&statements, "CONSTRAINT "+pgIdent(valueString(constraint["name"]))+" ON "+qualified, constraint["comment"])
		}
	}
	if kind == "r" {
		switch valueString(rel["replica_identity"]) {
		case "f":
			statements = append(statements, "ALTER TABLE "+qualified+" REPLICA IDENTITY FULL;")
		case "n":
			statements = append(statements, "ALTER TABLE "+qualified+" REPLICA IDENTITY NOTHING;")
		case "i":
			for _, index := range indexes {
				if valueBool(index["indisreplident"]) {
					statements = append(statements, "ALTER TABLE "+qualified+" REPLICA IDENTITY USING INDEX "+pgIdent(valueString(index["name"]))+";")
				}
			}
		}
	}
	for _, col := range cols {
		target := qualified + " ALTER COLUMN " + pgIdent(valueString(col["name"]))
		if kind == "r" || kind == "m" {
			alterKind := "TABLE"
			if kind == "m" {
				alterKind = "MATERIALIZED VIEW"
			}
			if storage := valueString(col["storage"]); storage != valueString(col["default_storage"]) {
				mode := map[string]string{"p": "PLAIN", "e": "EXTERNAL", "m": "MAIN", "x": "EXTENDED"}[storage]
				if mode == "" {
					return "", unsupported("PostgreSQL column storage " + storage)
				}
				statements = append(statements, "ALTER "+alterKind+" "+target+" SET STORAGE "+mode+";")
			}
			if compression := valueString(col["compression"]); compression != "" {
				method := map[string]string{"p": "pglz", "l": "lz4"}[compression]
				if method == "" {
					return "", unsupported("PostgreSQL compression " + compression)
				}
				statements = append(statements, "ALTER "+alterKind+" "+target+" SET COMPRESSION "+method+";")
			}
			colOptions, e := pgOptions(col["options"])
			if e != nil {
				return "", e
			}
			if colOptions != "" {
				statements = append(statements, "ALTER "+alterKind+" "+target+" SET ("+colOptions+");")
			}
			// PostgreSQL 17+ represents the default target as NULL; older
			// versions use -1. Zero explicitly disables statistics collection.
			if stats := valueInt(col["statistics"]); col["statistics"] != nil && stats >= 0 {
				statements = append(statements, fmt.Sprintf("ALTER %s %s SET STATISTICS %d;", alterKind, target, stats))
			}
		} else if col["expression"] != nil {
			statements = append(statements, "ALTER VIEW "+target+" SET DEFAULT "+valueString(col["expression"])+";")
		}
		pgAppendComment(&statements, "COLUMN "+qualified+"."+pgIdent(valueString(col["name"])), col["comment"])
	}
	for _, sequence := range sequences {
		seqName := pgQualified(valueString(sequence["schema"]), valueString(sequence["name"]))
		if valueString(sequence["dependency"]) == "a" {
			for _, col := range cols {
				if valueInt(col["attnum"]) == valueInt(sequence["attnum"]) {
					statements = append(statements, "ALTER SEQUENCE "+seqName+" OWNED BY "+qualified+"."+pgIdent(valueString(col["name"]))+";")
				}
			}
		}
		pgAppendComment(&statements, "SEQUENCE "+seqName, sequence["comment"])
	}
	pgAppendComment(&statements, objectType+" "+qualified, rel["comment"])
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return strings.Join(statements, "\n\n") + "\n", nil
}

func pgSequenceOptions(sequence record) string {
	cycle := "NO CYCLE"
	if valueBool(sequence["seqcycle"]) {
		cycle = "CYCLE"
	}
	return fmt.Sprintf("START WITH %s INCREMENT BY %s MINVALUE %s MAXVALUE %s CACHE %s %s", valueString(sequence["seqstart"]), valueString(sequence["seqincrement"]), valueString(sequence["seqmin"]), valueString(sequence["seqmax"]), valueString(sequence["seqcache"]), cycle)
}
func pgAppendComment(statements *[]string, target string, comment any) {
	if comment != nil {
		*statements = append(*statements, "COMMENT ON "+target+" IS "+pgLiteral(valueString(comment))+";")
	}
}
