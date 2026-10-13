package server

import (
	"strings"
	"testing"
)

func TestSchemaDiffMappedTargetAndRollback(t *testing.T) {
	source := map[string]any{"name": "id", "data_type": "integer", "is_nullable": false, "is_primary_key": false, "column_default": nil, "comment": nil}
	target := map[string]any{"name": "id", "data_type": "bigint", "is_nullable": true, "is_primary_key": false, "column_default": nil, "comment": nil}
	prepared, err := prepareSchemaTools(map[string]any{"databaseType": "postgres", "targetSchema": "dst", "enableRollback": true, "sourceTables": []map[string]any{{"name": "users", "table_type": "TABLE"}}, "targetTables": []map[string]any{{"name": "members", "table_type": "TABLE"}}, "tableMappings": []map[string]any{{"sourceTable": "users", "targetTable": "members"}}, "sourceDetails": []map[string]any{{"name": "users", "columns": []map[string]any{source}}}, "targetDetails": []map[string]any{{"name": "members", "columns": []map[string]any{target}}}})
	if err != nil {
		t.Fatal(err)
	}
	sql := str(prepared, "syncSql")
	if !strings.Contains(sql, `ALTER TABLE "dst"."members" ALTER COLUMN "id" TYPE integer`) || strings.Contains(sql, "users") {
		t.Fatal(sql)
	}
	if prepared["rollbackCompleteness"] != "incomplete" {
		t.Fatal(prepared)
	}
	if !strings.Contains(str(prepared, "rollbackSyncSql"), "TYPE bigint") {
		t.Fatal(prepared)
	}
}
func TestSchemaMySQLDDLNeverClaimsAtomicRollback(t *testing.T) {
	plan, err := generateSchemaPlan(map[string]any{"databaseType": "mysql", "enableRollback": true, "diffs": []map[string]any{{"type": "added", "name": "x", "columns": []map[string]any{{"type": "added", "name": "id", "source": map[string]any{"name": "id", "data_type": "INT", "is_nullable": true}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if plan["rollbackCompleteness"] != "incomplete" || !strings.Contains(str(plan, "rollbackSyncSql"), "DROP TABLE") {
		t.Fatal(plan)
	}
}
func TestSchemaDDLRewritesOnlyObjectHeader(t *testing.T) {
	sql, err := schemaDDLTarget(`CREATE TABLE "old"."source" (name TEXT DEFAULT 'old.source')`, "postgres", "new", "target")
	if err != nil {
		t.Fatal(err)
	}
	if sql != `CREATE TABLE "new"."target" (name TEXT DEFAULT 'old.source')` {
		t.Fatal(sql)
	}
}
func TestAdvancedStructureRejectsInjectedTypeAndPartitionBounds(t *testing.T) {
	if _, err := advancedStructure(map[string]any{"databaseType": "postgres", "tableName": "x", "columns": []map[string]any{{"name": "id", "dataType": "INT); DROP TABLE x; --"}}}, true); err == nil {
		t.Fatal("Accepted injected type")
	}
	if _, err := advancedPartitionBound(map[string]any{"kind": "list", "values": []string{"1); DROP TABLE x; --"}}); err == nil {
		t.Fatal("Accepted injected bound")
	}
}

func TestSchemaSynchronizationDropsForeignKeysBeforeParentsAndCreatesBeforeConstraints(t *testing.T) {
	col := map[string]any{"name": "id", "data_type": "integer", "is_nullable": true}
	fk := map[string]any{"name": "child_parent", "column": "id", "ref_column": "id", "ref_table": "parent"}
	plan, err := generateSchemaPlan(map[string]any{"databaseType": "postgres", "targetSchema": "dst", "diffs": []map[string]any{
		{"name": "parent", "type": "removed"},
		{"name": "child", "type": "modified", "foreignKeys": []map[string]any{{"name": "child_parent", "type": "removed", "target": fk}}},
		{"name": "new_child", "type": "added", "columns": []map[string]any{{"name": "id", "type": "added", "source": col}}, "foreignKeys": []map[string]any{{"name": "new_fk", "type": "added", "source": map[string]any{"name": "new_fk", "column": "id", "ref_column": "id", "ref_table": "new_parent"}}}},
		{"name": "new_parent", "type": "added", "columns": []map[string]any{{"name": "id", "type": "added", "source": col}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	sql := str(plan, "syncSql")
	dropFK := strings.Index(sql, `ALTER TABLE "dst"."child" DROP CONSTRAINT`)
	dropParent := strings.Index(sql, `DROP TABLE "dst"."parent"`)
	createParent := strings.Index(sql, `CREATE TABLE "dst"."new_parent"`)
	createChild := strings.Index(sql, `CREATE TABLE "dst"."new_child"`)
	addFK := strings.Index(sql, `ALTER TABLE "dst"."new_child" ADD CONSTRAINT`)
	if dropFK < 0 || dropFK > dropParent || createParent > createChild || createChild > addFK || !strings.Contains(sql, `REFERENCES "dst"."new_parent"`) {
		t.Fatal(sql)
	}
}

func TestSchemaNativeExtraMetadataAndOverloadedRoutineNamespaces(t *testing.T) {
	prepared, err := prepareSchemaTools(map[string]any{"databaseType": "postgres", "targetSchema": "dst", "sourceSequences": []map[string]any{{"name": "ids", "data_type": "bigint", "start_value": "7", "min_value": "1", "max_value": "99", "increment": "2", "cycle": true}}, "sourceOwners": []map[string]any{{"object_name": "items", "object_type": "TABLE", "owner": "dbx"}}, "sourceFunctions": []map[string]any{
		{"name": "twice", "function_type": "FUNCTION", "arguments": "integer", "definition": `CREATE FUNCTION twice(integer) RETURNS integer LANGUAGE SQL AS $$ SELECT $1 * 2 $$`},
		{"name": "twice", "function_type": "FUNCTION", "arguments": "bigint", "definition": `CREATE FUNCTION twice(bigint) RETURNS bigint LANGUAGE SQL AS $$ SELECT $1 * 2 $$`},
	}})
	if err != nil {
		t.Fatal(err)
	}
	sql := str(prepared, "syncSql")
	if !strings.Contains(sql, `CREATE SEQUENCE "dst"."ids" AS bigint START WITH 7 INCREMENT BY 2 MINVALUE 1 MAXVALUE 99 CYCLE`) || !strings.Contains(sql, `ALTER TABLE "dst"."items" OWNER TO "dbx"`) || strings.Count(sql, `CREATE FUNCTION "dst"."twice"`) != 2 {
		t.Fatal(sql)
	}
}

func TestSchemaDDLDefersOnlyForeignKeyConstraintsPreservingLiteralText(t *testing.T) {
	ddl, constraints, err := schemaAddedTableForeignKeys("postgres", "dst", "child", `CREATE TABLE "dst"."child" (id integer, description text DEFAULT 'FOREIGN KEY REFERENCES parent', CONSTRAINT fk FOREIGN KEY(id) REFERENCES parent(id))`, []map[string]any{{"source": map[string]any{"name": "fk", "column": "id", "ref_column": "id", "ref_table": "parent"}}})
	if err != nil || len(constraints) != 1 || !strings.Contains(ddl, "'FOREIGN KEY REFERENCES parent'") || strings.Contains(ddl, "CONSTRAINT fk") {
		t.Fatal(ddl, constraints, err)
	}
}
