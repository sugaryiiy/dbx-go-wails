package database

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
)

// Object browsers and sidebar groups share this catalog. Lists contain names
// and identities only: a hidden or expensive routine body must not hide its name.
func nativeObjects(ctx context.Context, db *sql.DB, kind, database, schema string, p map[string]any) ([]record, error) {
	var catalog queryRunner = db
	var snapshot *sql.Tx
	if kind == "postgres" {
		var err error
		snapshot, err = postgresCatalogSnapshot(ctx, db)
		if err != nil {
			return nil, err
		}
		defer snapshot.Rollback()
		catalog = snapshot
	}
	types := strings.ToUpper(str(p, "object_types", "object_type"))
	wanted := map[string]bool{}
	for _, value := range strings.Split(types, ",") {
		if value = strings.TrimSpace(value); value != "" {
			wanted[value] = true
		}
	}
	wants := func(types ...string) bool {
		if len(wanted) == 0 {
			return true
		}
		for _, value := range types {
			if wanted[value] {
				return true
			}
		}
		return false
	}
	list := []record{}
	query := func(statement string, args ...any) error {
		rows, err := records(ctx, catalog, statement, args...)
		if err == nil {
			list = append(list, rows...)
		}
		return err
	}
	var err error
	switch kind {
	case "sqlite":
		err = query("SELECT name,upper(type) AS table_type,? AS schema,CASE WHEN type='trigger' THEN tbl_name END AS parent_name,CASE WHEN type='trigger' THEN sql END AS statement,1 AS enabled FROM "+quoteIdentifier(schema, kind)+".sqlite_schema WHERE type IN ('table','view','trigger') AND name NOT LIKE 'sqlite_%'", schema)
	case "mysql":
		if wants("TABLE", "VIEW") {
			err = query("SELECT table_name AS name,CASE table_type WHEN 'BASE TABLE' THEN 'TABLE' ELSE table_type END AS table_type,table_schema AS `schema`,table_comment AS comment FROM information_schema.tables WHERE table_schema=?", database)
		}
		if err == nil && wants("FUNCTION", "PROCEDURE") {
			err = query("SELECT routine_name AS name,routine_type AS table_type,routine_schema AS `schema`,routine_comment AS comment,created AS created_at,last_altered AS updated_at FROM information_schema.routines WHERE routine_schema=?", database)
		}
		if err == nil && wants("TRIGGER") {
			err = query("SELECT trigger_name AS name,'TRIGGER' AS table_type,trigger_schema AS `schema`,event_object_schema AS parent_schema,event_object_table AS parent_name,event_manipulation AS event,action_timing AS timing,action_statement AS statement,created AS created_at,1 AS enabled FROM information_schema.triggers WHERE trigger_schema=?", database)
		}
		if err == nil && wants("EVENT") {
			err = query("SELECT event_name AS name,'EVENT' AS table_type,event_schema AS `schema`,event_comment AS comment,created AS created_at,last_altered AS updated_at FROM information_schema.events WHERE event_schema=?", database)
		}
	case "postgres":
		if wants("TABLE", "VIEW", "MATERIALIZED_VIEW", "SEQUENCE") {
			err = query(`SELECT c.relname AS name,CASE c.relkind WHEN 'v' THEN 'VIEW' WHEN 'm' THEN 'MATERIALIZED_VIEW' WHEN 'S' THEN 'SEQUENCE' ELSE 'TABLE' END AS table_type,n.nspname AS schema,obj_description(c.oid,'pg_class') AS comment FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','p','v','m','f','S')`, schema)
		}
		if err == nil && wants("FUNCTION", "PROCEDURE") {
			var routineKind, condition string
			routineKind, condition, err = postgresRoutineCatalogSQL(ctx, catalog)
			if err == nil {
				err = query(`SELECT p.proname AS name,`+routineKind+` AS table_type,n.nspname AS schema,pg_get_function_identity_arguments(p.oid) AS signature,obj_description(p.oid,'pg_proc') AS comment FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$1 AND `+condition, schema)
			}
		}
		if err == nil && wants("TRIGGER") {
			err = query(`SELECT t.tgname AS name,'TRIGGER'::text AS table_type,n.nspname AS schema,n.nspname AS parent_schema,c.relname AS parent_name,t.tgenabled<>'D' AS enabled,pg_get_triggerdef(t.oid) AS statement,obj_description(t.oid,'pg_trigger') AS comment FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND NOT t.tgisinternal`, schema)
		}
	default:
		return nil, unsupported("object catalog for " + kind)
	}
	if err != nil {
		return nil, err
	}
	if snapshot != nil {
		if err := snapshot.Commit(); err != nil {
			return nil, err
		}
	}
	for _, object := range list {
		object["object_type"] = object["table_type"]
		if valueString(object["object_type"]) == "TRIGGER" {
			object["parent_schema"] = object["schema"]
			object["trigger"] = record{"name": object["name"], "event": valueString(object["event"]), "timing": valueString(object["timing"]), "statement": object["statement"], "enabled": valueBool(object["enabled"]), "comment": object["comment"]}
		}
	}
	// Sorting the entire combined catalog before applying offset/limit makes
	// mixed-kind paging stable, including overloads and per-table trigger names.
	sort.SliceStable(list, func(i, j int) bool {
		for _, key := range []string{"name", "object_type", "signature", "parent_name"} {
			left, right := valueString(list[i][key]), valueString(list[j][key])
			if left != right {
				return left < right
			}
		}
		return false
	})
	return filterTables(list, p)
}

// PostgreSQL-derived servers can advertise 9.2 while providing procedures.
// Probe the actual catalog rather than inferring routine kinds from the version.
func postgresRoutineCatalogSQL(ctx context.Context, db queryRunner) (string, string, error) {
	rows, err := records(ctx, db, `SELECT EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='pg_catalog.pg_proc'::regclass AND attname='prokind' AND NOT attisdropped) AS has_kind`)
	if err != nil {
		return "", "", err
	}
	if valueBool(rows[0]["has_kind"]) {
		return "CASE p.prokind WHEN 'p' THEN 'PROCEDURE' ELSE 'FUNCTION' END", "p.prokind IN ('f','p','w')", nil
	}
	return "'FUNCTION'::text", "NOT p.proisagg", nil
}

func mysqlCatalogObjectSource(ctx context.Context, db *sql.DB, database, name, objectType string) (string, error) {
	objectType = strings.ToUpper(strings.TrimSpace(objectType))
	if objectType == "" {
		objectType = "TABLE"
	}
	switch objectType {
	case "TABLE", "VIEW", "FUNCTION", "PROCEDURE", "TRIGGER", "EVENT":
	default:
		return "", unsupported("MySQL source for " + objectType)
	}
	rows, err := records(ctx, db, "SHOW CREATE "+objectType+" "+quoteIdentifier(database, "mysql")+"."+quoteIdentifier(name, "mysql"))
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", sql.ErrNoRows
	}
	for key, value := range rows[0] {
		upper := strings.ToUpper(key)
		if upper == "CREATE "+objectType || (objectType == "TABLE" && upper == "CREATE VIEW") || (objectType == "TRIGGER" && upper == "SQL ORIGINAL STATEMENT") {
			if source := valueString(value); source != "" {
				return source, nil
			}
		}
	}
	return "", errors.New("Object exists but its CREATE source is hidden by server permissions")
}

func mysqlEventInfo(ctx context.Context, db *sql.DB, database, name string) (record, error) {
	rows, err := records(ctx, db, "SELECT event_name AS name,event_schema AS `schema`,definer AS definer,time_zone AS time_zone,event_type AS event_type,execute_at AS execute_at,interval_value AS interval_value,interval_field AS interval_field,starts AS starts,ends AS ends,status AS status,on_completion AS on_completion,event_comment AS comment,event_body AS event_body,event_definition AS event_definition,created AS created_at,last_altered AS updated_at,last_executed AS last_executed FROM information_schema.events WHERE event_schema=? AND event_name=?", database, name)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return rows[0], nil
}

func postgresCatalogObjectSource(ctx context.Context, db *sql.DB, schema, name, objectType string, p map[string]any) (string, error) {
	tx, err := postgresCatalogSnapshot(ctx, db)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var rows []record
	switch objectType {
	case "FUNCTION", "PROCEDURE":
		kind, condition, e := postgresRoutineCatalogSQL(ctx, tx)
		if e != nil {
			return "", e
		}
		statement := `SELECT pg_get_functiondef(p.oid) AS source FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$1 AND p.proname=$2 AND (` + kind + `)=$3 AND ` + condition
		args := []any{schema, name, objectType}
		if signature, supplied := p["signature"]; supplied {
			statement += " AND pg_get_function_identity_arguments(p.oid)=$4"
			args = append(args, valueString(signature))
		}
		rows, err = records(ctx, tx, statement, args...)
	case "TRIGGER":
		statement := `SELECT pg_get_triggerdef(t.oid,true) AS source FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND t.tgname=$2 AND NOT t.tgisinternal`
		args := []any{schema, name}
		if relation := str(p, "relation_name"); relation != "" {
			statement += " AND c.relname=$3"
			args = append(args, relation)
		}
		rows, err = records(ctx, tx, statement, args...)
	case "SEQUENCE":
		version, e := postgresVersion(ctx, tx)
		if e != nil {
			return "", e
		}
		var options record
		asType := ""
		if version < 100000 {
			options, err = postgresLegacySequenceOptions(ctx, tx, schema, name)
		} else {
			rows, err = records(ctx, tx, `SELECT format_type(seqtypid,NULL) AS data_type,seqstart,seqincrement,seqmin,seqmax,seqcache,seqcycle FROM pg_sequence WHERE seqrelid=$1::regclass`, pgQualified(schema, name))
			if err == nil && len(rows) > 0 {
				options = rows[0]
				asType = " AS " + valueString(options["data_type"])
			}
		}
		if err != nil {
			return "", err
		}
		if options == nil {
			return "", sql.ErrNoRows
		}
		return "CREATE SEQUENCE " + pgQualified(schema, name) + asType + " " + pgSequenceOptions(options) + ";", tx.Commit()
	default:
		return "", unsupported("PostgreSQL source for " + objectType)
	}
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", sql.ErrNoRows
	}
	if len(rows) != 1 {
		return "", errors.New("Object name is ambiguous; provide its routine signature or trigger relation name")
	}
	return valueString(rows[0]["source"]), tx.Commit()
}
