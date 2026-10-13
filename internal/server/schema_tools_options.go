package server

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

func schemaExtraObjects(items []map[string]any, prefix string) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		copy := map[string]any{}
		for key, value := range item {
			copy[key] = value
		}
		for _, pair := range [][2]string{{"dataType", "data_type"}, {"startValue", "start_value"}, {"minValue", "min_value"}, {"maxValue", "max_value"}, {"lastValue", "last_value"}, {"tableName", "table_name"}, {"objectName", "object_name"}, {"objectType", "object_type"}} {
			if _, exists := copy[pair[0]]; !exists {
				copy[pair[0]] = copy[pair[1]]
			}
		}
		if prefix == "Sequences" {
			for _, key := range []string{"startValue", "minValue", "maxValue", "increment", "lastValue"} {
				if copy[key] != nil {
					copy[key] = fmt.Sprint(copy[key])
				}
			}
		}
		if prefix == "Functions" {
			copy["identity"] = strings.ToUpper(str(copy, "function_type")) + ":" + str(copy, "name") + "(" + strings.Join(strings.Fields(str(copy, "arguments")), " ") + ")"
		}
		if prefix == "Rules" {
			copy["identity"] = str(copy, "tableName") + ":" + str(copy, "name")
		}
		out = append(out, copy)
	}
	return out
}

func schemaDiffName(d map[string]any) string {
	if str(d, "type") != "renamed" && str(d, "targetName") != "" {
		return str(d, "targetName")
	}
	return str(d, "name")
}

func schemaOrderDiffs(diffs []map[string]any) []map[string]any {
	source, target := []map[string]any{}, []map[string]any{}
	for _, d := range diffs {
		srcKeys, tgtKeys := []map[string]any{}, []map[string]any{}
		for _, fk := range compareList(d["foreignKeys"]) {
			if str(fk, "type") != "removed" {
				srcKeys = append(srcKeys, mapValue(fk, "source"))
			}
			if str(fk, "type") != "added" {
				tgtKeys = append(tgtKeys, mapValue(fk, "target"))
			}
		}
		name := schemaDiffName(d)
		source = append(source, map[string]any{"name": name, "foreignKeys": srcKeys, "ddl": d["ddl"]})
		target = append(target, map[string]any{"name": name, "foreignKeys": tgtKeys, "ddl": d["targetDdl"]})
	}
	rank := func(details []map[string]any) map[string]int {
		ranks := map[string]int{}
		for i, name := range compareStrings(schemaDependencyGraph(details)["topological_order"]) {
			ranks[name] = i
		}
		return ranks
	}
	srcRank, tgtRank := rank(source), rank(target)
	out := append([]map[string]any{}, diffs...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		removedA, removedB := str(a, "type") == "removed", str(b, "type") == "removed"
		if removedA != removedB {
			return removedA
		}
		if removedA {
			return tgtRank[schemaDiffName(a)] > tgtRank[schemaDiffName(b)]
		}
		return srcRank[schemaDiffName(a)] < srcRank[schemaDiffName(b)]
	})
	return out
}

func schemaForeignKeyDrops(kind, schema string, diffs []map[string]any) ([]string, error) {
	if kind == "sqlite" {
		return nil, nil // SQLite drops depend on child-first table ordering.
	}
	out := []string{}
	for _, d := range diffs {
		if str(d, "type") != "removed" && str(d, "type") != "modified" {
			continue
		}
		table, err := compareTable(kind, schema, schemaDiffName(d))
		if err != nil {
			return nil, err
		}
		for _, fk := range compareList(d["foreignKeys"]) {
			if str(fk, "type") != "removed" && str(fk, "type") != "modified" {
				continue
			}
			if err := compareIdentifier(str(fk, "name")); err != nil {
				return nil, err
			}
			operation := "DROP CONSTRAINT"
			if kind == "mysql" {
				operation = "DROP FOREIGN KEY"
			}
			out = append(out, "ALTER TABLE "+table+" "+operation+" "+quoteSQLName(str(fk, "name"), kind)+";")
		}
	}
	return out, nil
}

func schemaSourceObjectTarget(source, kind, schema, name, object string) (string, error) {
	target, err := compareTable(kind, schema, name)
	if err != nil {
		return "", err
	}
	part := `(?:"(?:[^"]|"")+"|` + "`(?:[^`]|``)+`" + `|[\p{L}_][\p{L}\p{N}_$]*)`
	pattern := regexp.MustCompile(`(?is)^(\s*CREATE\s+(?:OR\s+REPLACE\s+)?(?:DEFINER\s*=\s*\S+\s+)?` + regexp.QuoteMeta(object) + `\s+)` + part + `(?:\s*\.\s*` + part + `)?`)
	loc := pattern.FindStringSubmatchIndex(source)
	if loc == nil {
		return "", errors.New("Source must contain a recognizable CREATE " + object + " statement")
	}
	return source[:loc[3]] + target + source[loc[1]:], nil
}

func schemaAddedTableForeignKeys(kind, schema, name, ddl string, diffs []map[string]any) (string, []string, error) {
	if kind == "sqlite" || len(diffs) == 0 {
		return ddl, nil, nil
	}
	target, err := compareTable(kind, schema, name)
	if err != nil {
		return "", nil, err
	}
	out := []string{}
	for _, diff := range diffs {
		fk := mapValue(diff, "source")
		if str(fk, "ref_schema") == "" && str(fk, "refSchema") == "" {
			fk["ref_schema"] = schema
		}
		definition, err := schemaForeignKeyDefinition(kind, fk)
		if err != nil {
			return "", nil, err
		}
		out = append(out, "ALTER TABLE "+target+" ADD "+definition+";")
	}
	if ddl == "" {
		return ddl, out, nil
	}
	parts, suffix, err := splitSQLiteDefinition(ddl)
	if err != nil {
		return "", nil, err
	}
	keep := []string{}
	for _, part := range parts {
		tokens := schemaLex(part)
		_, isColumn := sqliteLeadingIdentifier(part)
		foreign := false
		for i := 0; i+1 < len(tokens); i++ {
			if strings.EqualFold(tokens[i].identifier, "FOREIGN") && strings.EqualFold(tokens[i+1].identifier, "KEY") {
				foreign = true
			}
		}
		if !isColumn && foreign {
			continue
		}
		// Inline reference clauses may carry extra column constraints. Keep the
		// source verbatim rather than discard expressions we cannot reconstruct.
		for i, token := range tokens {
			if i > 0 && strings.EqualFold(token.identifier, "REFERENCES") {
				return "", nil, errors.New("Inline foreign keys require explicit table-level foreign key DDL for dependency-safe synchronization")
			}
		}
		keep = append(keep, part)
	}
	ddl = ddl[:strings.IndexByte(ddl, '(')] + "(\n  " + strings.Join(keep, ",\n  ") + "\n)"
	if suffix != "" {
		ddl += " " + suffix
	}
	return ddl, out, nil
}
