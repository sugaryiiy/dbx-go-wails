package server

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

func (s *Server) handleSchemaTools(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	var result any
	var err error
	switch r.URL.Path {
	case "/api/schema-diff/prepare":
		result, err = prepareSchemaTools(p)
	case "/api/schema-diff/generate-sync-plan":
		result, err = generateSchemaPlan(p)
	case "/api/schema-diff/generate-sync-sql":
		var plan map[string]any
		plan, err = generateSchemaPlan(p)
		if err == nil {
			result = plan["syncSql"]
		}
	default:
		return false
	}
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
	} else if err != nil {
		writeError(w, 400, err.Error())
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
func schemaMap(items []map[string]any, key string, fold bool) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	for _, item := range items {
		name := str(item, key)
		if err := compareIdentifier(name); err != nil {
			return nil, err
		}
		if fold {
			name = strings.ToLower(name)
		}
		if _, ok := out[name]; ok {
			return nil, fmt.Errorf("Ambiguous duplicate schema object %q", name)
		}
		out[name] = item
	}
	return out, nil
}
func schemaObjectDiff(source, target []map[string]any, key string, fields []string, fold bool) ([]map[string]any, error) {
	src, err := schemaMap(source, key, fold)
	if err != nil {
		return nil, err
	}
	tgt, err := schemaMap(target, key, fold)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, item := range source {
		name := str(item, key)
		k := name
		if fold {
			k = strings.ToLower(k)
		}
		other, ok := tgt[k]
		if !ok {
			out = append(out, map[string]any{"type": "added", "name": name, "source": item})
			continue
		}
		changes := []string{}
		for _, field := range fields {
			if !compareEqual(item[field], other[field]) {
				changes = append(changes, fmt.Sprintf("%s: %v → %v", field, other[field], item[field]))
			}
		}
		if len(changes) > 0 {
			out = append(out, map[string]any{"type": "modified", "name": name, "source": item, "target": other, "changes": changes})
		}
	}
	for _, item := range target {
		name := str(item, key)
		k := name
		if fold {
			k = strings.ToLower(k)
		}
		if _, ok := src[k]; !ok {
			out = append(out, map[string]any{"type": "removed", "name": name, "target": item})
		}
	}
	return out, nil
}
func schemaColumnDiff(source, target []map[string]any, p map[string]any) ([]map[string]any, error) {
	fields := []string{"data_type", "is_nullable", "column_default", "extra", "is_primary_key"}
	if !boolValue(p, "ignoreComments") {
		fields = append(fields, "comment")
	}
	charset, exists := p["compareCharset"]
	if !exists || charset == true {
		fields = append(fields, "character_set", "collation")
	}
	// Driver integer display widths and ANSI aliases are representations of the
	// same stored type; compare those canonically without rewriting output data.
	normalizedSource, normalizedTarget := []map[string]any{}, []map[string]any{}
	for _, pair := range []struct {
		values []map[string]any
		output *[]map[string]any
	}{{source, &normalizedSource}, {target, &normalizedTarget}} {
		for _, c := range pair.values {
			copy := map[string]any{}
			for k, v := range c {
				copy[k] = v
			}
			copy["data_type"] = schemaCanonicalType(str(c, "data_type"), str(p, "databaseType"))
			*pair.output = append(*pair.output, copy)
		}
	}
	out, err := schemaObjectDiff(normalizedSource, normalizedTarget, "name", fields, boolValue(p, "ignoreColumnNameCase"))
	if err != nil {
		return nil, err
	}
	for _, d := range out {
		for _, c := range source {
			if str(c, "name") == str(d, "name") {
				d["source"] = c
			}
		}
		for _, c := range target {
			if str(c, "name") == str(d, "name") || (boolValue(p, "ignoreColumnNameCase") && strings.EqualFold(str(c, "name"), str(d, "name"))) {
				d["target"] = c
			}
		}
	}
	if boolValue(p, "detectRenames") {
		out = schemaDetectColumnRenames(out, fields)
	}
	fold := boolValue(p, "ignoreColumnNameCase")
	targetPositions := map[string]int{}
	for i, c := range target {
		name := str(c, "name")
		if fold {
			name = strings.ToLower(name)
		}
		targetPositions[name] = i
	}
	for i, c := range source {
		name := str(c, "name")
		key := name
		if fold {
			key = strings.ToLower(key)
		}
		position := any("first")
		if i > 0 {
			position = map[string]any{"after": str(source[i-1], "name")}
		}
		found := false
		for _, d := range out {
			if str(d, "name") == name {
				found = true
				if str(d, "type") == "added" {
					d["addPosition"] = position
				}
				if boolValue(p, "compareColumnOrder") && len(source) == len(target) && str(d, "type") == "modified" && targetPositions[key] != i {
					d["changes"] = append(compareStrings(d["changes"]), fmt.Sprintf("order: %d → %d", targetPositions[key]+1, i+1))
					d["addPosition"] = position
				}
			}
		}
		if !found && boolValue(p, "compareColumnOrder") && len(source) == len(target) {
			if targetIndex, ok := targetPositions[key]; ok && targetIndex != i {
				out = append(out, map[string]any{"type": "modified", "name": name, "source": c, "target": target[targetIndex], "changes": []string{fmt.Sprintf("order: %d → %d", targetIndex+1, i+1)}, "addPosition": position})
			}
		}
	}
	return out, nil
}

func schemaCanonicalType(typ, kind string) string {
	value := strings.ToLower(strings.Join(strings.Fields(typ), " "))
	aliases := map[string]string{"character varying": "varchar", "double precision": "double", "int": "integer", "bool": "boolean"}
	for from, to := range aliases {
		if value == from {
			value = to
		}
		if strings.HasPrefix(value, from+"(") {
			value = to + value[len(from):]
		}
	}
	if kind == "mysql" || kind == "mariadb" {
		value = regexp.MustCompile(`^(tinyint|smallint|mediumint|integer|bigint|year)\([0-9]+\)`).ReplaceAllString(value, "$1")
	}
	return value
}
func schemaDetectColumnRenames(diffs []map[string]any, fields []string) []map[string]any {
	used := map[int]bool{}
	out := []map[string]any{}
	for i, added := range diffs {
		if str(added, "type") != "added" {
			continue
		}
		candidate := -1
		for j, removed := range diffs {
			if used[j] || str(removed, "type") != "removed" {
				continue
			}
			source, target := mapValue(added, "source"), mapValue(removed, "target")
			same := true
			for _, field := range fields {
				if !compareEqual(source[field], target[field]) {
					same = false
				}
			}
			if same {
				if candidate >= 0 {
					candidate = -1
					break
				}
				candidate = j
			}
		}
		if candidate >= 0 {
			removed := diffs[candidate]
			used[i] = true
			used[candidate] = true
			out = append(out, map[string]any{"type": "renamed", "name": added["name"], "source": added["source"], "target": removed["target"], "changes": []string{str(removed, "name") + " → " + str(added, "name")}})
		}
	}
	for i, d := range diffs {
		if !used[i] {
			out = append(out, d)
		}
	}
	return out
}
func prepareSchemaTools(p map[string]any) (map[string]any, error) {
	kind, err := compareKind(str(p, "databaseType"))
	if err != nil {
		return nil, err
	}
	fold := boolValue(p, "ignoreTableNameCase")
	source, target := compareList(p["sourceTables"]), compareList(p["targetTables"])
	sourceDetails, targetDetails := compareList(p["sourceDetails"]), compareList(p["targetDetails"])
	srcTables, err := schemaMap(source, "name", fold)
	if err != nil {
		return nil, err
	}
	tgtTables, err := schemaMap(target, "name", fold)
	if err != nil {
		return nil, err
	}
	srcDetails, err := schemaMap(sourceDetails, "name", fold)
	if err != nil {
		return nil, err
	}
	tgtDetails, err := schemaMap(targetDetails, "name", fold)
	if err != nil {
		return nil, err
	}
	key := func(name string) string {
		if fold {
			return strings.ToLower(name)
		}
		return name
	}
	mappings := map[string]string{}
	usedMappings := map[string]bool{}
	for _, m := range compareList(p["tableMappings"]) {
		a, b := key(str(m, "sourceTable")), key(str(m, "targetTable"))
		if _, ok := srcTables[a]; !ok {
			return nil, fmt.Errorf("Mapped source table %q does not exist", a)
		}
		if _, ok := tgtTables[b]; !ok {
			return nil, fmt.Errorf("Mapped target table %q does not exist", b)
		}
		if _, ok := mappings[a]; ok || usedMappings[b] {
			return nil, errors.New("Table mappings must be one to one")
		}
		mappings[a] = b
		usedMappings[b] = true
	}
	diffs := []map[string]any{}
	used := map[string]bool{}
	for _, table := range source {
		name := str(table, "name")
		srcKey := key(name)
		targetKey := srcKey
		if mapped, ok := mappings[srcKey]; ok {
			targetKey = mapped
		}
		other, exists := tgtTables[targetKey]
		detail := srcDetails[srcKey]
		objectType := "table"
		if strings.Contains(strings.ToUpper(str(table, "table_type")), "VIEW") {
			objectType = "view"
		}
		d := map[string]any{"name": name, "objectType": objectType}
		if exists {
			used[targetKey] = true
			if targetKey != srcKey || str(other, "name") != name {
				d["targetName"] = other["name"]
			}
			d["type"] = "modified"
		} else {
			d["type"] = "added"
		}
		targetDetail := tgtDetails[targetKey]
		hasChanges := false
		if objectType == "view" {
			sourceDDL, targetDDL := str(detail, "ddl"), str(targetDetail, "ddl")
			if sourceDDL == "" {
				return nil, fmt.Errorf("View %s has no source DDL", name)
			}
			d["ddl"] = sourceDDL
			if exists {
				d["targetDdl"] = targetDDL
			}
			hasChanges = !exists || schemaNormalizeDDL(sourceDDL) != schemaNormalizeDDL(targetDDL)
		} else {
			cols, e := schemaColumnDiff(compareList(detail["columns"]), compareList(targetDetail["columns"]), p)
			if e != nil {
				return nil, e
			}
			if len(cols) > 0 {
				d["columns"] = cols
				hasChanges = true
			}
			for _, part := range []struct {
				name   string
				fields []string
			}{{"indexes", []string{"columns", "is_unique", "is_primary", "filter", "index_type", "included_columns", "column_opclasses"}}, {"foreignKeys", []string{"column", "columns", "ref_table", "ref_column", "ref_columns", "ref_schema", "on_update", "on_delete", "match_type", "deferrable", "initially_deferred"}}, {"triggers", []string{"statement", "timing", "event", "enabled"}}} {
				srcItems, tgtItems := compareList(detail[part.name]), compareList(targetDetail[part.name])
				if part.name == "foreignKeys" {
					for _, fk := range srcItems {
						if _, ok := srcTables[key(str(fk, "ref_table"))]; ok {
							fk["ref_schema"] = nil
							if mapped, exists := mappings[key(str(fk, "ref_table"))]; exists {
								fk["ref_table"] = tgtTables[mapped]["name"]
							}
						}
					}
					for _, fk := range tgtItems {
						if _, ok := tgtTables[key(str(fk, "ref_table"))]; ok {
							fk["ref_schema"] = nil
						}
					}
				}
				objects, e := schemaObjectDiff(srcItems, tgtItems, "name", part.fields, false)
				if e != nil {
					return nil, e
				}
				if len(objects) > 0 {
					d[part.name] = objects
					hasChanges = true
				}
			}
			if ddl := str(detail, "ddl"); ddl != "" {
				d["ddl"] = ddl
			}
			if ddl := str(targetDetail, "ddl"); ddl != "" {
				d["targetDdl"] = ddl
			}
			if !boolValue(p, "ignoreComments") && str(table, "comment") != str(other, "comment") {
				d["sourceTableComment"] = table["comment"]
				d["targetTableComment"] = other["comment"]
				hasChanges = true
			}
		}
		if hasChanges || !exists {
			diffs = append(diffs, d)
		}
	}
	for _, table := range target {
		k := key(str(table, "name"))
		if used[k] {
			continue
		}
		detail := tgtDetails[k]
		d := map[string]any{"type": "removed", "name": table["name"], "objectType": "table", "targetDdl": detail["ddl"], "targetTableComment": table["comment"]}
		if strings.Contains(strings.ToUpper(str(table, "table_type")), "VIEW") {
			d["objectType"] = "view"
		}
		for _, part := range []string{"columns", "indexes", "foreignKeys", "triggers"} {
			removed := []map[string]any{}
			for _, item := range compareList(detail[part]) {
				removed = append(removed, map[string]any{"type": "removed", "name": item["name"], "target": item})
			}
			if len(removed) > 0 {
				d[part] = removed
			}
		}
		diffs = append(diffs, d)
	}
	extras := map[string]any{}
	for _, part := range []struct {
		prefix, key string
		fields      []string
	}{{"Functions", "name", []string{"definition", "arguments", "data_type", "function_type"}}, {"Sequences", "name", []string{"dataType", "startValue", "minValue", "maxValue", "increment", "cycle", "lastValue"}}, {"Rules", "name", []string{"tableName", "definition"}}, {"Owners", "objectName", []string{"objectType", "owner"}}} {
		srcExtra := schemaExtraObjects(compareList(p["source"+part.prefix]), part.prefix)
		tgtExtra := schemaExtraObjects(compareList(p["target"+part.prefix]), part.prefix)
		if part.prefix == "Functions" || part.prefix == "Rules" {
			part.key = "identity"
		}
		objects, e := schemaObjectDiff(srcExtra, tgtExtra, part.key, part.fields, false)
		if e != nil {
			return nil, e
		}
		if part.prefix == "Owners" {
			for _, d := range objects {
				d["objectName"] = d["name"]
				delete(d, "name")
			}
		} else if part.key == "identity" {
			for _, d := range objects {
				item := mapValue(d, "source")
				if len(item) == 0 {
					item = mapValue(d, "target")
				}
				d["name"] = item["name"]
			}
		}
		label := strings.ToLower(part.prefix[:1]) + part.prefix[1:]
		label = strings.TrimSuffix(label, "s") + "Diffs"
		extras[label] = objects
	}
	planInput := map[string]any{"databaseType": kind, "targetSchema": p["targetSchema"], "cascadeDelete": p["cascadeDelete"], "enableRollback": p["enableRollback"], "fieldMappings": p["fieldMappings"], "sourceDialect": p["sourceDialect"], "diffs": diffs}
	for k, v := range extras {
		planInput[k] = v
	}
	plan, err := generateSchemaPlan(planInput)
	if err != nil {
		return nil, err
	}
	plan["diffs"] = diffs
	for k, v := range extras {
		plan[k] = v
	}
	plan["dependencyGraph"] = schemaDependencyGraph(sourceDetails)
	return plan, nil
}
func schemaNormalizeDDL(sql string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSuffix(strings.TrimSpace(sql), ";")), " "))
}
func schemaDependencyGraph(details []map[string]any) map[string]any {
	nodes := map[string]map[string]any{}
	for _, d := range details {
		name := str(d, "name")
		nodes[name] = map[string]any{"table_name": name, "depends_on": []string{}, "depended_by": []string{}}
	}
	for _, d := range details {
		name := str(d, "name")
		refs := []string{}
		for _, fk := range compareList(d["foreignKeys"]) {
			refs = append(refs, str(fk, "ref_table"))
		}
		tokens := schemaLex(str(d, "ddl"))
		for i, t := range tokens {
			keyword := strings.ToUpper(t.identifier)
			if keyword != "FROM" && keyword != "JOIN" && keyword != "REFERENCES" {
				continue
			}
			if i+1 < len(tokens) {
				j := i + 1
				for j+2 < len(tokens) && tokens[j+1].symbol == '.' {
					j += 2
				}
				refs = append(refs, tokens[j].identifier)
			}
		}
		seen := map[string]bool{}
		for _, ref := range refs {
			if other, ok := nodes[ref]; ok && ref != name {
				if seen[ref] {
					continue
				}
				seen[ref] = true
				nodes[name]["depends_on"] = append(compareStrings(nodes[name]["depends_on"]), ref)
				other["depended_by"] = append(compareStrings(other["depended_by"]), name)
			}
		}
	}
	return map[string]any{"nodes": nodes, "topological_order": schemaTopologicalOrder(nodes)}
}
func schemaTopologicalOrder(nodes map[string]map[string]any) []string {
	out := []string{}
	visited := map[string]bool{}
	visiting := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if visited[name] || visiting[name] {
			return
		}
		visiting[name] = true
		deps := compareStrings(nodes[name]["depends_on"])
		sort.Strings(deps)
		for _, dep := range deps {
			visit(dep)
		}
		visited[name] = true
		out = append(out, name)
	}
	names := []string{}
	for name := range nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		visit(name)
	}
	return out
}
func schemaMappedColumn(col map[string]any, mappings []map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range col {
		out[k] = v
	}
	typ := str(out, "data_type")
	base := typ
	params := ""
	if i := strings.IndexByte(typ, '('); i >= 0 {
		base = strings.TrimSpace(typ[:i])
		params = typ[i:]
	}
	for _, m := range mappings {
		if strings.EqualFold(str(m, "sourceType"), base) {
			target := str(m, "targetType")
			switch str(m, "paramStrategy") {
			case "strip":
				params = ""
			case "custom":
				params = "(" + str(m, "customParams") + ")"
			}
			out["data_type"] = target + params
			break
		}
	}
	return out
}
func schemaDDLTarget(ddl, kind, schema, name string) (string, error) {
	ddl = schemaRewriteNamespace(ddl, schemaCreateNamespace(ddl), schema, kind)
	target, err := compareTable(kind, schema, name)
	if err != nil {
		return "", err
	}
	pattern := regexp.MustCompile(`(?is)^(\s*CREATE\s+(?:OR\s+REPLACE\s+)?(?:MATERIALIZED\s+)?(?:TABLE|VIEW)\s+(?:IF\s+NOT\s+EXISTS\s+)?)(?:"(?:[^"]|"")+"|` + "`(?:[^`]|``)+`" + `|[\w$]+)(?:\s*\.\s*(?:"(?:[^"]|"")+"|` + "`(?:[^`]|``)+`" + `|[\w$]+))?`)
	loc := pattern.FindStringSubmatchIndex(ddl)
	if loc == nil {
		return "", errors.New("Source DDL is not a recognizable CREATE TABLE or VIEW statement")
	}
	return ddl[:loc[2]] + ddl[loc[2]:loc[3]] + target + ddl[loc[1]:], nil
}
func schemaDiffStatements(p map[string]any) ([]string, error) {
	kind, err := compareKind(str(p, "databaseType"))
	if err != nil {
		return nil, err
	}
	schema := str(p, "targetSchema")
	mappings := compareList(p["fieldMappings"])
	diffs := schemaOrderDiffs(compareList(p["diffs"]))
	statements, err := schemaForeignKeyDrops(kind, schema, diffs)
	if err != nil {
		return nil, err
	}
	deferred := []string{}
	for _, d := range diffs {
		name := str(d, "name")
		if target := str(d, "targetName"); target != "" {
			name = target
		}
		target, err := compareTable(kind, schema, name)
		if err != nil {
			return nil, err
		}
		object := strings.ToUpper(str(d, "objectType"))
		if object == "" {
			object = "TABLE"
		}
		if object != "TABLE" && object != "VIEW" && object != "MATERIALIZED VIEW" {
			return nil, errors.New("Unknown schema object type")
		}
		if sql := str(d, "syncSql"); sql != "" {
			parts, err := cleanSQL(sql, kind)
			if err != nil {
				return nil, err
			}
			statements = append(statements, parts+";")
			continue
		}
		switch str(d, "type") {
		case "removed":
			sql := "DROP " + object + " " + target
			if kind == "postgres" && boolValue(p, "cascadeDelete") {
				sql += " CASCADE"
			}
			statements = append(statements, sql+";")
		case "renamed":
			old, err := compareTable(kind, schema, str(d, "targetName"))
			if err != nil {
				return nil, err
			}
			newName := str(d, "name")
			statements = append(statements, "ALTER "+object+" "+old+" RENAME TO "+quoteSQLName(newName, kind)+";")
		case "added":
			if object != "TABLE" {
				sql, err := schemaDDLTarget(str(d, "ddl"), kind, schema, name)
				if err != nil {
					return nil, err
				}
				sql, err = cleanSQL(sql, kind)
				if err != nil {
					return nil, err
				}
				statements = append(statements, sql+";")
				continue
			}
			if ddl := str(d, "ddl"); ddl != "" && len(mappings) == 0 && (str(p, "sourceDialect") == "" || str(p, "sourceDialect") == kind) {
				sql, err := schemaDDLTarget(ddl, kind, schema, name)
				if err != nil {
					return nil, err
				}
				var foreignKeys []string
				sql, foreignKeys, err = schemaAddedTableForeignKeys(kind, schema, name, sql, compareList(d["foreignKeys"]))
				if err != nil {
					return nil, err
				}
				deferred = append(deferred, foreignKeys...)
				sql, err = cleanSQL(sql, kind)
				if err != nil {
					return nil, err
				}
				statements = append(statements, sql+";") // SQLite/Postgres DDL introspection may exclude ordinary indexes.
				for _, idx := range compareList(d["indexes"]) {
					if kind == "mysql" {
						continue
					}
					sql, err := schemaIndexSQL(kind, schema, name, mapValue(idx, "source"))
					if err != nil {
						return nil, err
					}
					if sql != "" {
						statements = append(statements, sql)
					}
				}
			} else {
				cols, idx, fks := []map[string]any{}, []map[string]any{}, []map[string]any{}
				for _, c := range compareList(d["columns"]) {
					cols = append(cols, schemaMappedColumn(mapValue(c, "source"), mappings))
				}
				for _, i := range compareList(d["indexes"]) {
					idx = append(idx, mapValue(i, "source"))
				}
				for _, f := range compareList(d["foreignKeys"]) {
					fks = append(fks, mapValue(f, "source"))
				}
				inline := fks
				if kind != "sqlite" {
					inline = nil
					_, foreignKeys, err := schemaAddedTableForeignKeys(kind, schema, name, "", compareList(d["foreignKeys"]))
					if err != nil {
						return nil, err
					}
					deferred = append(deferred, foreignKeys...)
				}
				built, err := schemaCreateFromColumns(kind, schema, name, cols, idx, inline)
				if err != nil {
					return nil, err
				}
				statements = append(statements, built...)
			}
		case "modified":
			if object != "TABLE" {
				sql, err := schemaDDLTarget(str(d, "ddl"), kind, schema, name)
				if err != nil {
					return nil, err
				}
				if kind == "sqlite" || object == "MATERIALIZED VIEW" {
					statements = append(statements, "DROP "+object+" "+target+";")
				} else if !strings.Contains(strings.ToUpper(sql), "CREATE OR REPLACE") {
					sql = regexp.MustCompile(`(?i)^\s*CREATE\s+`).ReplaceAllString(sql, "CREATE OR REPLACE ")
				}
				sql, err = cleanSQL(sql, kind)
				if err != nil {
					return nil, err
				}
				statements = append(statements, sql+";")
				continue
			}
			for _, fk := range compareList(d["foreignKeys"]) {
				if str(fk, "type") == "removed" || str(fk, "type") == "modified" {
					if kind == "sqlite" {
						return nil, errors.New("SQLite foreign key changes require a table rebuild")
					}
				}
				if str(fk, "type") != "removed" {
					source := mapValue(fk, "source")
					if str(source, "ref_schema") == "" && str(source, "refSchema") == "" {
						source["ref_schema"] = schema
					}
					definition, err := schemaForeignKeyDefinition(kind, source)
					if err != nil {
						return nil, err
					}
					deferred = append(deferred, "ALTER TABLE "+target+" ADD "+definition+";")
				}
			}
			for _, idx := range compareList(d["indexes"]) {
				if str(idx, "type") == "removed" || str(idx, "type") == "modified" {
					sql, err := schemaDropIndex(kind, schema, name, mapValue(idx, "target"))
					if err != nil {
						return nil, err
					}
					statements = append(statements, sql)
				}
			}
			for _, c := range compareList(d["columns"]) {
				switch str(c, "type") {
				case "removed":
					statements = append(statements, "ALTER TABLE "+target+" DROP COLUMN "+quoteSQLName(str(c, "name"), kind)+";")
				case "added", "modified", "renamed":
					source := schemaMappedColumn(mapValue(c, "source"), mappings)
					original := mapValue(c, "target")
					built, err := schemaAlterColumn(kind, schema, name, source, original)
					if err != nil {
						return nil, err
					}
					if kind == "mysql" && c["addPosition"] != nil && len(built) > 0 {
						position := " FIRST"
						if after := str(compareObject(c["addPosition"]), "after"); after != "" {
							position = " AFTER " + quoteSQLName(after, kind)
						}
						built[len(built)-1] = strings.TrimSuffix(built[len(built)-1], ";") + position + ";"
					}
					statements = append(statements, built...)
				default:
					return nil, errors.New("Invalid column diff type")
				}
			}
			for _, idx := range compareList(d["indexes"]) {
				if str(idx, "type") != "removed" {
					source := mapValue(idx, "source")
					if boolValue(source, "is_primary") {
						if kind == "sqlite" {
							return nil, errors.New("SQLite primary key edits require a table rebuild")
						}
						q := []string{}
						for _, c := range compareStrings(source["columns"]) {
							q = append(q, quoteSQLName(c, kind))
						}
						statements = append(statements, "ALTER TABLE "+target+" ADD PRIMARY KEY ("+strings.Join(q, ", ")+");")
					} else {
						sql, err := schemaIndexSQL(kind, schema, name, source)
						if err != nil {
							return nil, err
						}
						statements = append(statements, sql)
					}
				}
			}
			if value, exists := d["sourceTableComment"]; exists {
				literal := "NULL"
				if value != nil {
					literal = schemaLiteral(fmt.Sprint(value), kind)
				}
				if kind == "postgres" {
					statements = append(statements, "COMMENT ON TABLE "+target+" IS "+literal+";")
				} else if kind == "mysql" {
					statements = append(statements, "ALTER TABLE "+target+" COMMENT="+literal+";")
				}
			}
		default:
			return nil, errors.New("Invalid table diff type")
		}
		for _, trigger := range compareList(d["triggers"]) {
			if str(trigger, "type") == "removed" || str(trigger, "type") == "modified" {
				sql := "DROP TRIGGER " + quoteSQLName(str(trigger, "name"), kind)
				if kind == "postgres" {
					sql += " ON " + target
				}
				statements = append(statements, sql+";")
			}
			if str(trigger, "type") != "removed" {
				sql := str(mapValue(trigger, "source"), "statement")
				if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sql)), "CREATE ") {
					return nil, fmt.Errorf("Trigger %s does not contain reconstructable CREATE TRIGGER source", str(trigger, "name"))
				}
				sql, err = cleanSQL(sql, kind)
				if err != nil {
					return nil, err
				}
				deferred = append(deferred, sql+";")
			}
		}
	}
	statements = append(statements, deferred...)
	for _, part := range []string{"functionDiffs", "sequenceDiffs", "ruleDiffs", "ownerDiffs"} {
		for _, d := range compareList(p[part]) {
			built, err := schemaExtraSQL(kind, schema, part, d)
			if err != nil {
				return nil, err
			}
			statements = append(statements, built...)
		}
	}
	return statements, nil
}
func schemaExtraSQL(kind, schema, part string, d map[string]any) ([]string, error) {
	source, target := mapValue(d, "source"), mapValue(d, "target")
	prefix := strings.ToUpper(part[:1]) + strings.TrimSuffix(part[1:], "Diffs") + "s"
	source = schemaExtraObjects([]map[string]any{source}, prefix)[0]
	target = schemaExtraObjects([]map[string]any{target}, prefix)[0]
	name := str(d, "name")
	if part == "ownerDiffs" {
		name = str(d, "objectName")
	}
	qualified, err := compareTable(kind, schema, name)
	if err != nil {
		return nil, err
	}
	typeName := str(d, "type")
	out := []string{}
	switch part {
	case "ownerDiffs":
		if typeName == "removed" {
			return out, nil
		}
		if kind != "postgres" {
			return nil, errors.New("Object owners require PostgreSQL")
		}
		object := strings.ToUpper(str(source, "objectType"))
		if object != "TABLE" && object != "VIEW" && object != "SEQUENCE" && object != "MATERIALIZED VIEW" {
			return nil, errors.New("Unsupported owner object type")
		}
		owner := str(source, "owner")
		if err = compareIdentifier(owner); err != nil {
			return nil, err
		}
		return []string{"ALTER " + object + " " + qualified + " OWNER TO " + quoteSQLName(owner, kind) + ";"}, nil
	case "functionDiffs":
		info := source
		if typeName == "removed" {
			info = target
		}
		routine := strings.ToUpper(str(info, "function_type"))
		if routine != "PROCEDURE" {
			routine = "FUNCTION"
		}
		if typeName == "removed" || (typeName == "modified" && kind == "mysql") {
			args := str(info, "arguments")
			if err = schemaExpression("NULL"+args, kind); err != nil {
				return nil, err
			}
			suffix := ""
			if kind == "postgres" {
				suffix = "(" + args + ")"
			}
			out = append(out, "DROP "+routine+" "+qualified+suffix+";")
		}
		if typeName != "removed" {
			definition := str(source, "definition")
			if definition == "" {
				return nil, errors.New("Routine source definition is required")
			}
			definition, err = schemaSourceObjectTarget(definition, kind, schema, name, routine)
			if err != nil {
				return nil, err
			}
			if kind == "postgres" && typeName == "modified" && !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(definition)), "CREATE OR REPLACE ") {
				definition = regexp.MustCompile(`(?i)^\s*CREATE\s+`).ReplaceAllString(definition, "CREATE OR REPLACE ")
			}
			sql, err := cleanSQL(definition, kind)
			if err != nil {
				return nil, err
			}
			out = append(out, sql+";")
		}
	case "ruleDiffs":
		if kind != "postgres" {
			return nil, errors.New("Rules require PostgreSQL")
		}
		if typeName == "removed" || typeName == "modified" {
			table, err := compareTable(kind, schema, str(target, "tableName"))
			if err != nil {
				return nil, err
			}
			out = append(out, "DROP RULE "+quoteSQLName(name, kind)+" ON "+table+";")
		}
		if typeName != "removed" {
			sql, err := cleanSQL(str(source, "definition"), kind)
			if err != nil {
				return nil, err
			}
			out = append(out, sql+";")
		}
	case "sequenceDiffs":
		if kind != "postgres" {
			return nil, errors.New("Sequences require PostgreSQL")
		}
		if typeName == "removed" {
			return []string{"DROP SEQUENCE " + qualified + ";"}, nil
		}
		prefix := "CREATE SEQUENCE "
		if typeName == "modified" {
			prefix = "ALTER SEQUENCE "
		}
		sql := prefix + qualified
		if typ := str(source, "dataType"); typ != "" {
			typ = strings.ToLower(typ)
			if typ != "smallint" && typ != "integer" && typ != "bigint" {
				return nil, errors.New("Sequence data type must be smallint, integer or bigint")
			}
			sql += " AS " + typ
		}
		for _, pair := range [][2]string{{"startValue", "START WITH"}, {"increment", "INCREMENT BY"}, {"minValue", "MINVALUE"}, {"maxValue", "MAXVALUE"}} {
			value := str(source, pair[0])
			if value != "" {
				if !regexp.MustCompile(`^-?[0-9]+$`).MatchString(value) {
					return nil, errors.New("Sequence parameters must be integers")
				}
				sql += " " + pair[1] + " " + value
			}
		}
		if boolValue(source, "cycle") {
			sql += " CYCLE"
		} else {
			sql += " NO CYCLE"
		}
		out = append(out, sql+";")
		if last := str(source, "lastValue"); last != "" {
			if !regexp.MustCompile(`^-?[0-9]+$`).MatchString(last) {
				return nil, errors.New("Sequence last value must be an integer")
			}
			out = append(out, "SELECT setval("+schemaLiteral(qualified, kind)+"::regclass, "+last+", true);")
		}
	}
	return out, nil
}
func schemaReverseDiff(d map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range d {
		out[k] = v
	}
	switch str(d, "type") {
	case "added":
		out["type"] = "removed"
	case "removed":
		out["type"] = "added"
	case "renamed":
		out["name"], out["targetName"] = d["targetName"], d["name"]
	}
	out["source"], out["target"] = d["target"], d["source"]
	out["ddl"], out["targetDdl"] = d["targetDdl"], d["ddl"]
	out["sourceTableComment"], out["targetTableComment"] = d["targetTableComment"], d["sourceTableComment"]
	delete(out, "syncSql")
	for _, part := range []string{"columns", "indexes", "foreignKeys", "triggers"} {
		items := []map[string]any{}
		for _, item := range compareList(d[part]) {
			items = append(items, schemaReverseDiff(item))
		}
		if len(items) > 0 {
			out[part] = items
		}
	}
	return out
}
func generateSchemaPlan(p map[string]any) (map[string]any, error) {
	statements, err := schemaDiffStatements(p)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"syncSql": strings.Join(statements, "\n"), "rollbackCompleteness": "complete"}
	if !boolValue(p, "enableRollback") {
		return out, nil
	}
	reverse := map[string]any{}
	for k, v := range p {
		reverse[k] = v
	}
	missing := []map[string]any{}
	for _, part := range []string{"diffs", "functionDiffs", "sequenceDiffs", "ruleDiffs", "ownerDiffs"} {
		items := []map[string]any{}
		original := compareList(p[part])
		for i := len(original) - 1; i >= 0; i-- {
			d := original[i]
			items = append(items, schemaReverseDiff(d))
			if part == "diffs" && (str(d, "type") == "removed" || len(compareList(d["columns"])) > 0) {
				missing = append(missing, map[string]any{"kind": "tableData", "name": d["name"], "table": d["name"], "reason": "DDL rollback cannot recover deleted rows or values lost through column conversion"})
			}
		}
		reverse[part] = items
	}
	rollback, e := schemaDiffStatements(reverse)
	if e != nil {
		missing = append(missing, map[string]any{"kind": "schema", "name": "rollback", "table": nil, "reason": e.Error()})
	} else {
		out["rollbackSyncSql"] = strings.Join(rollback, "\n")
	}
	if str(p, "databaseType") == "mysql" || str(p, "databaseType") == "mariadb" {
		missing = append(missing, map[string]any{"kind": "transaction", "name": "MySQL DDL", "table": nil, "reason": "MySQL DDL implicitly commits; inverse DDL is not an atomic rollback"})
	}
	if len(missing) > 0 {
		out["rollbackCompleteness"] = "incomplete"
		out["missingRollbackObjects"] = missing
	}
	return out, nil
}
