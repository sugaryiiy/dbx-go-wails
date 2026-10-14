package database

import (
	"reflect"
	"testing"
)

func TestMySQLRoutineStatementBoundaries(t *testing.T) {
	for _, fixture := range []struct{ name, source string }{
		{"procedure", "CREATE PROCEDURE report() BEGIN SELECT 1; SELECT 2; END"},
		{"definer and comments", "/* source */ CREATE DEFINER='root'@'localhost' PROCEDURE `report`() BEGIN SELECT 'END; BEGIN'; -- END;\n# BEGIN;\nSELECT 2; END"},
		{"nested blocks and labels", "CREATE PROCEDURE report() outer_block: BEGIN inner_block: BEGIN SELECT 1; END inner_block; SELECT 2; END outer_block"},
		{"conditionals", "CREATE PROCEDURE report(IN n INT) BEGIN IF n > 0 THEN SELECT IF(n > 1, 'a;', 'b;'); ELSEIF n = 0 THEN SELECT 0; ELSE SELECT -1; END /* suffix */ IF; SELECT 2; END"},
		{"loops", "CREATE PROCEDURE report() BEGIN loop_label: LOOP WHILE 1 DO SELECT 1; END WHILE; REPEAT SELECT 2; UNTIL 1 END REPEAT; LEAVE loop_label; END LOOP loop_label; SELECT 3; END"},
		{"case statement", "CREATE PROCEDURE report(IN n INT) BEGIN CASE n WHEN 1 THEN SELECT 1; ELSE SELECT 2; END CASE; SELECT 3; END"},
		{"case expressions", "CREATE PROCEDURE report() BEGIN SELECT CASE WHEN 1 THEN CASE WHEN 2 THEN 'a;' ELSE 'b;' END ELSE 'c;' END, IF(1, CASE WHEN 1 THEN 2 ELSE 3 END, 4); SELECT 5; END"},
		{"handler", "CREATE PROCEDURE report() BEGIN DECLARE CONTINUE HANDLER FOR SQLEXCEPTION BEGIN SELECT 1; END; SIGNAL SQLSTATE '45000'; END"},
		{"handler conditional", "CREATE PROCEDURE report() BEGIN DECLARE CONTINUE HANDLER FOR SQLEXCEPTION IF 1 THEN SET @x = 1; END IF; SIGNAL SQLSTATE '45000'; END"},
		{"handler case", "CREATE PROCEDURE report() BEGIN DECLARE CONTINUE HANDLER FOR SQLEXCEPTION CASE WHEN 1 THEN SET @x = 1; END CASE; SIGNAL SQLSTATE '45000'; END"},
		{"qualified keyword identifiers", "CREATE PROCEDURE report() BEGIN SELECT t.begin, t.end, t.case, t /* qualifier */ . /* identifier */ end FROM entries t; SELECT 2; END"},
		{"keyword variable names", "CREATE PROCEDURE report(IN end INT) BEGIN DECLARE begin INT; SET begin=end; SELECT begin, end; END"},
		{"keyword variable in case", "CREATE PROCEDURE report(IN end INT) BEGIN SELECT CASE WHEN end > 0 THEN end ELSE 0 END, IF(1, CASE WHEN 1 THEN end + 1 ELSE 0 END, 0); END"},
		{"keyword routine name", "CREATE PROCEDURE begin() BEGIN SELECT 1; END"},
		{"root conditional", "CREATE PROCEDURE report(IN n INT) IF (n > 0) THEN SELECT 1; ELSE SELECT 2; END IF"},
		{"root loop", "CREATE PROCEDURE report() label1: LOOP SELECT 1; LEAVE label1; END LOOP label1"},
		{"root case statement", "CREATE PROCEDURE report(IN n INT) CASE n WHEN 1 THEN SELECT 1; ELSE SELECT 2; END CASE"},
		{"root show", "CREATE PROCEDURE report() SHOW TABLES"},
		{"root analyze", "CREATE PROCEDURE report() ANALYZE TABLE entries"},
		{"function", "CREATE FUNCTION twice(n INT) RETURNS INT DETERMINISTIC BEGIN RETURN n * 2; END"},
		{"function return character set", "CREATE FUNCTION report() RETURNS VARCHAR(255) CHARACTER SET utf8mb4 DETERMINISTIC BEGIN RETURN 'x;'; END"},
		{"function return set", "CREATE FUNCTION report() RETURNS SET('a','b') DETERMINISTIC BEGIN RETURN 'a'; END"},
		{"simple function", "CREATE FUNCTION twice(n INT) RETURNS INT DETERMINISTIC RETURN IF(n > 0, n * 2, 0)"},
		{"function case", "CREATE FUNCTION twice(n INT) RETURNS INT DETERMINISTIC RETURN CASE WHEN n > 0 THEN n * 2 ELSE 0 END"},
		{"trigger", "CREATE TRIGGER changed BEFORE INSERT ON entries FOR EACH ROW BEGIN SET NEW.id = 1; SET NEW.value = 'a;'; END"},
		{"simple trigger", "CREATE TRIGGER changed BEFORE INSERT ON entries FOR EACH ROW SET NEW.id = 1"},
		{"event", "CREATE EVENT report ON SCHEDULE EVERY 1 DAY DISABLE DO BEGIN SELECT 1; SELECT 2; END"},
		{"simple event", "CREATE EVENT report ON SCHEDULE EVERY 1 DAY DISABLE DO SELECT 1"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			for _, dialect := range []string{"mysql", "mariadb"} {
				parts, err := SplitStatementsForDialect(fixture.source+"; SELECT 9;", dialect)
				want := []string{fixture.source, "SELECT 9"}
				if err != nil || !reflect.DeepEqual(parts, want) {
					t.Fatalf("%s routine was split incorrectly: %#v, %v", dialect, parts, err)
				}
				if parts, err := SplitStatementsForDialect(fixture.source, dialect); err != nil || !reflect.DeepEqual(parts, []string{fixture.source}) {
					t.Fatalf("%s routine without trailing separator: %#v, %v", dialect, parts, err)
				}
				if ordinaryRead(fixture.source, "mysql") || mainVerb(fixture.source, "mysql") != "CREATE" || returnsRows(fixture.source, "mysql") {
					t.Fatal("routine DDL must retain its write classification and use ExecContext")
				}
			}
		})
	}
}

func TestMySQLRoutineRejectsIncompleteBodies(t *testing.T) {
	for _, source := range []string{
		"CREATE PROCEDURE report() BEGIN SELECT 1;",
		"CREATE PROCEDURE report() BEGIN BEGIN SELECT 1; END;",
		"CREATE PROCEDURE report() BEGIN IF 1 THEN SELECT 1; END IF;",
		"CREATE PROCEDURE report() BEGIN SELECT CASE WHEN 1 THEN 2; END",
		"CREATE /* a */ /* b */ /* c */ TRIGGER t AFTER INSERT ON x BEGIN SELECT 1; DELETE FROM y; END",
		"CREATE PROCEDURE report BEGIN SELECT 1; DELETE FROM y; END",
		"CREATE EVENT report ON SCHEDULE EVERY 1 DAY BEGIN SELECT 1; DELETE FROM y; END",
	} {
		if _, err := SplitStatementsForDialect(source, "mysql"); err == nil {
			t.Fatalf("accepted incomplete body: %s", source)
		}
	}
}

func TestMySQLRoutineMCPGuards(t *testing.T) {
	for _, source := range []string{
		"CREATE PROCEDURE report() BEGIN SELECT 1; END",
		"CREATE FUNCTION report() RETURNS INT DETERMINISTIC RETURN 1",
		"CREATE TRIGGER changed BEFORE INSERT ON entries FOR EACH ROW BEGIN SET NEW.id = 1; END",
		"CREATE EVENT report ON SCHEDULE EVERY 1 DAY DISABLE DO BEGIN SELECT 1; END",
	} {
		if _, err := ValidateMCPSQL(source, "mysql", false, true); err == nil {
			t.Fatalf("MCP routine restriction was bypassed: %s", source)
		}
	}
}
