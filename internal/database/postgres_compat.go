package database

import (
	"context"
	"fmt"
	"strings"
)

func postgresVersion(ctx context.Context, q queryRunner) (int, error) {
	rows, err := records(ctx, q, "SELECT current_setting('server_version_num')::int AS version")
	if err != nil {
		return 0, err
	}
	if len(rows) != 1 {
		return 0, fmt.Errorf("PostgreSQL server version is unavailable")
	}
	return int(valueInt(rows[0]["version"])), nil
}

// INCLUDE columns appeared in PostgreSQL 11. Before that every index attribute
// is a key, including expression attributes whose indkey entry is zero.
func postgresIndexKeyCountSQL(version int) string {
	if version < 110000 {
		return "i.indnatts"
	}
	return "i.indnkeyatts"
}

func postgresColumns(ctx context.Context, q queryRunner, schema, table string) ([]record, error) {
	version, err := postgresVersion(ctx, q)
	if err != nil {
		return nil, err
	}
	extra := "NULL::text"
	if version >= 120000 {
		extra = "CASE WHEN a.attidentity <> '' THEN 'IDENTITY' WHEN a.attgenerated <> '' THEN 'GENERATED' ELSE NULL END"
	} else if version >= 100000 {
		extra = "CASE WHEN a.attidentity <> '' THEN 'IDENTITY' ELSE NULL END"
	}
	return records(ctx, q, `SELECT a.attname AS name, format_type(a.atttypid,a.atttypmod) AS data_type,
 NOT a.attnotnull AS is_nullable, pg_get_expr(d.adbin,d.adrelid) AS column_default,
 EXISTS(SELECT 1 FROM pg_index i WHERE i.indrelid=a.attrelid AND i.indisprimary
 AND a.attnum=ANY(ARRAY(SELECT i.indkey[k] FROM generate_series(0,`+postgresIndexKeyCountSQL(version)+`-1) AS k))) AS is_primary_key,
 `+extra+` AS extra, col_description(a.attrelid,a.attnum) AS comment
 FROM pg_attribute a JOIN pg_class t ON t.oid=a.attrelid JOIN pg_namespace n ON n.oid=t.relnamespace
 LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
 WHERE n.nspname=$1 AND t.relname=$2 AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, schema, table)
}

// Correlated subqueries work on 9.2; LATERAL and WITH ORDINALITY require newer
// servers. Preserve constraint key order instead of sorting attribute numbers.
func postgresConstraintColumnsSQL(key, relation string) string {
	return `array_to_json(ARRAY(SELECT a.attname::text FROM generate_subscripts(con.` + key + `,1) AS k(pos)
 JOIN pg_attribute a ON a.attrelid=con.` + relation + ` AND a.attnum=con.` + key + `[k.pos] ORDER BY k.pos))::text`
}

func postgresDDLRelationQuery(version int) string {
	query := pgDDLRelationSQL
	if version < 100000 {
		query = strings.ReplaceAll(query, "c.relispartition", "false AS relispartition")
	}
	if version < 90500 {
		query = strings.ReplaceAll(query, "c.relrowsecurity", "false AS relrowsecurity")
		query = strings.ReplaceAll(query, "c.relforcerowsecurity", "false AS relforcerowsecurity")
		query = strings.ReplaceAll(query, "EXISTS(SELECT 1 FROM pg_policy WHERE polrelid=c.oid)", "false")
	}
	if version < 90400 {
		query = strings.ReplaceAll(query, "c.relreplident::text", "'d'::text")
	}
	if version < 120000 {
		query = strings.ReplaceAll(query, "c.reloftype<>0 AS typed", "c.reloftype<>0 AS typed,c.relhasoids AS has_oids")
	}
	return query
}

// Pre-10 sequence parameters live in the sequence relation itself.
func postgresLegacySequenceOptions(ctx context.Context, q queryRunner, schema, name string) (record, error) {
	rows, err := records(ctx, q, `SELECT start_value AS seqstart,increment_by AS seqincrement,
 min_value AS seqmin,max_value AS seqmax,cache_value AS seqcache,is_cycled AS seqcycle,last_value
 FROM `+pgQualified(schema, name))
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, fmt.Errorf("PostgreSQL sequence parameters are unavailable")
	}
	return rows[0], nil
}
