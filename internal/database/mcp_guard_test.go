package database

import "testing"

func TestMCPGuardSafeStatements(t *testing.T) {
	for _, tc := range []struct {
		sql, kind string
		ro, ddl   bool
	}{
		{"SELECT count(*), replace(name,'x','y') FROM people", "sqlite", true, false},
		{"WITH a AS (SELECT 1 AS id) SELECT * FROM a", "sqlite", true, false},
		{"UPDATE people SET name='value' WHERE id=3", "sqlite", false, false},
		{"DELETE FROM people WHERE id=3 AND name='name'", "sqlite", false, false},
		{"INSERT INTO main.people(id,name) VALUES (3,'name')", "sqlite", false, false},
		{"CREATE TABLE IF NOT EXISTS main.sample(id INTEGER)", "sqlite", false, true},
		{"SELECT count(*) FROM `people` # safe comment\n WHERE id=1", "mariadb", true, false},
	} {
		if _, err := ValidateMCPSQL(tc.sql, tc.kind, tc.ro, tc.ddl); err != nil {
			t.Errorf("safe %s: %v", tc.sql, err)
		}
	}
}
func TestMCPGuardHighRiskAndBypasses(t *testing.T) {
	for _, sql := range []string{
		"DELETE FROM people WHERE TRUE=TRUE", `DELETE FROM people WHERE "never_a_real_column"='never_a_real_column'`, "DELETE FROM people", "UPDATE people SET name='all'", "DELETE FROM people WHERE 1=1", "DELETE FROM people WHERE id=id", "DELETE FROM people WHERE id=1 OR 1=1", "DELETE FROM people WHERE name=upper(name)", "WITH gone AS (DELETE FROM people RETURNING *) SELECT * FROM gone", "SELECT load_extension('x')", "SELECT \"load_extension\"('x')", "SELECT user_func()", "SELECT pg_read_file('x')", "SELECT anything.abs(1)", "SELECT 1 INTO OUTFILE 'x'", "ATTACH DATABASE 'x' AS other", "PRAGMA query_only=OFF", "SELECT 1; SET SESSION a=1", "CREATE EXTENSION x", "CREATE VIRTUAL TABLE x USING fts5(name)", "CREATE SERVER x", "VACUUM INTO 'x'", "SELECT $$escaped$$",
	} {
		if _, err := ValidateMCPSQL(sql, "sqlite", false, false); err == nil {
			t.Errorf("unsafe SQL accepted: %s", sql)
		}
	}
}
func TestMCPGuardReadonlyWrites(t *testing.T) {
	for _, sql := range []string{"INSERT INTO t VALUES(1)", "UPDATE t SET a=2 WHERE a=1", "DELETE FROM t WHERE a=1", "CREATE TABLE t(id INT)", "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d"} {
		if _, err := ValidateMCPSQL(sql, "sqlite", true, true); err == nil {
			t.Errorf("readonly SQL accepted: %s", sql)
		}
	}
}

func TestMCPGuardEventsRemainUnavailableWithHighRiskPermission(t *testing.T) {
	for _, statement := range []string{
		"CREATE EVENT reset_people ON SCHEDULE EVERY 1 DAY DO DELETE FROM people",
		"CREATE DEFINER='app'@'localhost' EVENT reset_people ON SCHEDULE EVERY 1 DAY DO UPDATE people SET name='reset'",
		"ALTER EVENT reset_people ENABLE",
		"DROP EVENT IF EXISTS reset_people",
	} {
		if _, err := ValidateMCPSQL(statement, "mysql", false, true); err == nil {
			t.Errorf("asynchronous event DDL was accepted: %s", statement)
		}
	}
	for _, statement := range []string{
		"CREATE TABLE event(event INTEGER)",
		"ALTER TABLE event ADD event_time DATETIME",
		"DROP TABLE event",
	} {
		if _, err := ValidateMCPSQL(statement, "mysql", false, true); err != nil {
			t.Errorf("ordinary DDL using an event identifier: %v", err)
		}
	}
}
