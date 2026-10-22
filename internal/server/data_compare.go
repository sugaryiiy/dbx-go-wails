package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/sugaryiiy/magpiedb/internal/database"
)

// compare helpers normalize typed metadata and JSON request objects alike.
func compareObject(value any) map[string]any {
	if m, ok := value.(map[string]any); ok {
		return m
	}
	b, _ := json.Marshal(value)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}
func compareList(value any) []map[string]any {
	b, _ := json.Marshal(value)
	out := []map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}
func compareStrings(value any) []string {
	b, _ := json.Marshal(value)
	out := []string{}
	_ = json.Unmarshal(b, &out)
	return out
}
func compareRows(value any) [][]any {
	b, _ := json.Marshal(value)
	out := [][]any{}
	_ = json.Unmarshal(b, &out)
	return out
}
func compareEqual(a, b any) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(aa) == string(bb)
}
func compareKind(kind string) (string, error) {
	if kind == "" {
		kind = "mysql"
	}
	if kind == "mariadb" {
		kind = "mysql"
	}
	switch kind {
	case "mysql", "postgres", "sqlite":
		return kind, nil
	}
	return "", fmt.Errorf("Data and schema SQL generation is unavailable for dialect %q", kind)
}
func compareIdentifier(name string) error {
	if strings.TrimSpace(name) == "" || strings.ContainsRune(name, 0) {
		return errors.New("A nonempty identifier without NUL is required")
	}
	return nil
}
func compareTable(kind, schema, name string) (string, error) {
	if err := compareIdentifier(name); err != nil {
		return "", err
	}
	if strings.ContainsRune(schema, 0) {
		return "", errors.New("Invalid schema name")
	}
	q := quoteSQLName(name, kind)
	if schema != "" {
		q = quoteSQLName(schema, kind) + "." + q
	}
	return q, nil
}
func (s *Server) compareConnection(id string) (database.Config, error) {
	// The database manager owns live connection configuration. Saved state remains
	// a compatibility fallback for builds predating the public manager accessor.
	if provider, ok := any(s.database).(interface {
		ConnectionConfig(string) (database.Config, error)
	}); ok {
		if cfg, err := provider.ConnectionConfig(id); err == nil {
			return cfg, nil
		}
	}
	for _, c := range s.snapshot().Connections {
		if str(c, "id") == id {
			b, _ := json.Marshal(c)
			var cfg database.Config
			err := json.Unmarshal(b, &cfg)
			return cfg, err
		}
	}
	return database.Config{}, errors.New("Connection configuration not found")
}
func (s *Server) compareMetadata(ctx context.Context, id, db, schema, table, part string) (any, error) {
	return s.database.Handle(ctx, "/api/schema/"+part, map[string]any{"connectionId": id, "database": db, "schema": schema, "table": table})
}
func (s *Server) handleDataCompare(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/data-compare/") {
		return false
	}
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return true
	}
	var result any
	var err error
	switch r.URL.Path {
	case "/api/data-compare/prepare":
		result, err = prepareDataCompare(p)
	case "/api/data-compare/build-sync-plan":
		result, err = buildDataSyncPlan(compareList(p["tables"]))
	case "/api/data-compare/prepare-from-tables", "/api/data-compare/prepare-missing-target":
		result, err = s.prepareDataFromTables(r.Context(), p, strings.HasSuffix(r.URL.Path, "missing-target"))
	default:
		return false
	}
	if err != nil {
		writeError(w, 400, err.Error())
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
func compareDataRows(columns, keys []string, source, target [][]any, allowKeyless bool) (map[string]any, error) {
	index := map[string]int{}
	for i, c := range columns {
		if err := compareIdentifier(c); err != nil {
			return nil, err
		}
		if _, exists := index[c]; exists {
			return nil, errors.New("Duplicate comparison column")
		}
		index[c] = i
	}
	if len(columns) == 0 {
		return nil, errors.New("Comparison columns are required")
	}
	if len(keys) == 0 && !allowKeyless {
		return nil, errors.New("At least one key column is required for data comparison")
	}
	for _, k := range keys {
		if _, ok := index[k]; !ok {
			return nil, fmt.Errorf("Key column %q is absent from comparison columns", k)
		}
	}
	collect := func(rows [][]any, label string) (map[string]map[string]any, []string, error) {
		items := map[string]map[string]any{}
		order := []string{}
		for rowIndex, row := range rows {
			values := map[string]any{}
			for i, c := range columns {
				if i < len(row) {
					values[c] = row[i]
				} else {
					values[c] = nil
				}
			}
			parts := []string{}
			for _, k := range keys {
				b, _ := json.Marshal(values[k])
				parts = append(parts, string(b))
			}
			key := strings.Join(parts, "\x1f")
			if len(keys) == 0 {
				key = strconv.Itoa(rowIndex)
			}
			if _, exists := items[key]; exists {
				return nil, nil, fmt.Errorf("Duplicate %s key for column(s) [%s]: %s", label, strings.Join(keys, ", "), key)
			}
			items[key] = values
			order = append(order, key)
		}
		return items, order, nil
	}
	src, srcOrder, err := collect(source, "source")
	if err != nil {
		return nil, err
	}
	tgt, tgtOrder, err := collect(target, "target")
	if err != nil {
		return nil, err
	}
	keyValues := func(v map[string]any) map[string]any {
		out := map[string]any{}
		for _, k := range keys {
			out[k] = v[k]
		}
		return out
	}
	added := []map[string]any{}
	removed := []map[string]any{}
	modified := []map[string]any{}
	for _, key := range srcOrder {
		sv := src[key]
		tv, ok := tgt[key]
		if !ok {
			added = append(added, map[string]any{"key": key, "keyValues": keyValues(sv), "values": sv})
			continue
		}
		changes := []map[string]any{}
		for _, c := range columns {
			isKey := false
			for _, k := range keys {
				if k == c {
					isKey = true
				}
			}
			if !isKey && !compareEqual(sv[c], tv[c]) {
				changes = append(changes, map[string]any{"column": c, "source": sv[c], "target": tv[c]})
			}
		}
		if len(changes) > 0 {
			modified = append(modified, map[string]any{"key": key, "keyValues": keyValues(sv), "sourceValues": sv, "targetValues": tv, "changes": changes})
		}
	}
	for _, key := range tgtOrder {
		if _, ok := src[key]; !ok {
			removed = append(removed, map[string]any{"key": key, "keyValues": keyValues(tgt[key]), "values": tgt[key]})
		}
	}
	return map[string]any{"added": added, "removed": removed, "modified": modified}, nil
}
func prepareDataCompare(p map[string]any) (map[string]any, error) {
	diff, err := compareDataRows(compareStrings(p["columns"]), compareStrings(p["keyColumns"]), compareRows(p["sourceRows"]), compareRows(p["targetRows"]), false)
	if err != nil {
		return nil, err
	}
	table := map[string]any{}
	for k, v := range p {
		table[k] = v
	}
	table["diff"] = diff
	plan, err := buildDataSyncPlan([]map[string]any{table})
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": diff, "syncStatements": plan["syncStatements"], "syncSql": plan["syncSql"]}, nil
}
func buildDataSyncPlan(tables []map[string]any) (map[string]any, error) {
	statements := []string{}
	inserts, updates, deletes := 0, 0, 0
	for _, t := range tables {
		kind, err := compareKind(str(t, "databaseType"))
		if err != nil {
			return nil, err
		}
		name, err := compareTable(kind, str(t, "schema"), str(t, "tableName"))
		if err != nil {
			return nil, err
		}
		cols := compareStrings(t["columns"])
		keys := compareStrings(t["keyColumns"])
		metadata := map[string]gridColumn{}
		for _, c := range compareList(t["columnInfo"]) {
			b, _ := json.Marshal(c)
			var col gridColumn
			_ = json.Unmarshal(b, &col)
			metadata[col.Name] = col
		}
		b := gridBuilder{options: gridOptions{Kind: kind}, metadata: metadata}
		allowed := map[string]bool{}
		writable := []string{}
		for _, c := range cols {
			if err = compareIdentifier(c); err != nil {
				return nil, err
			}
			if allowed[c] {
				return nil, errors.New("Duplicate synchronization column")
			}
			allowed[c] = true
			if !b.generated(metadata[c]) {
				writable = append(writable, c)
			}
		}
		predicate := func(values map[string]any) (string, error) {
			if len(keys) == 0 {
				return "", errors.New("Update/delete synchronization requires key columns")
			}
			terms := []string{}
			for _, key := range keys {
				if !allowed[key] {
					return "", fmt.Errorf("Unknown key column %q", key)
				}
				v, ok := values[key]
				if !ok {
					return "", fmt.Errorf("Missing key value for %q", key)
				}
				term, err := b.predicate(key, v, false)
				if err != nil {
					return "", err
				}
				terms = append(terms, term)
			}
			return strings.Join(terms, " AND "), nil
		}
		for _, sql := range compareStrings(t["preSyncStatements"]) {
			parts, err := database.SplitStatementsForDialect(sql, kind)
			if err != nil {
				return nil, err
			}
			for _, part := range parts {
				statements = append(statements, part+";")
			}
		}
		diff := compareObject(t["diff"])
		added := compareList(diff["added"])
		inserts += len(added)
		for start := 0; start < len(added); start += 500 {
			end := start + 500
			if end > len(added) {
				end = len(added)
			}
			if len(writable) == 0 {
				for range added[start:end] {
					sql := "INSERT INTO " + name + " DEFAULT VALUES;"
					if kind == "mysql" {
						sql = "INSERT INTO " + name + " () VALUES ();"
					}
					statements = append(statements, sql)
				}
				continue
			}
			values := []string{}
			for _, row := range added[start:end] {
				v := compareObject(row["values"])
				literals := []string{}
				for _, c := range writable {
					literal, err := b.literal(v[c], metadata[c])
					if err != nil {
						return nil, fmt.Errorf("Column %s: %w", c, err)
					}
					literals = append(literals, literal)
				}
				values = append(values, "("+strings.Join(literals, ", ")+")")
			}
			quoted := []string{}
			for _, c := range writable {
				quoted = append(quoted, quoteSQLName(c, kind))
			}
			statements = append(statements, "INSERT INTO "+name+" ("+strings.Join(quoted, ", ")+") VALUES "+strings.Join(values, ", ")+";")
		}
		for _, row := range compareList(diff["modified"]) {
			assign := []string{}
			for _, change := range compareList(row["changes"]) {
				c := str(change, "column")
				if !allowed[c] {
					return nil, fmt.Errorf("Unknown changed column %q", c)
				}
				if b.generated(metadata[c]) {
					continue
				}
				for _, key := range keys {
					if c == key {
						return nil, errors.New("Synchronization cannot change a key column")
					}
				}
				literal, err := b.literal(change["source"], metadata[c])
				if err != nil {
					return nil, err
				}
				assign = append(assign, quoteSQLName(c, kind)+" = "+literal)
			}
			if len(assign) == 0 {
				continue
			}
			where, err := predicate(compareObject(row["keyValues"]))
			if err != nil {
				return nil, err
			}
			statements = append(statements, "UPDATE "+name+" SET "+strings.Join(assign, ", ")+" WHERE "+where+";")
			updates++
		}
		removed := compareList(diff["removed"])
		deletes += len(removed)
		for start := 0; start < len(removed); start += 200 {
			end := start + 200
			if end > len(removed) {
				end = len(removed)
			}
			predicates := []string{}
			for _, row := range removed[start:end] {
				where, err := predicate(compareObject(row["keyValues"]))
				if err != nil {
					return nil, err
				}
				predicates = append(predicates, "("+where+")")
			}
			statements = append(statements, "DELETE FROM "+name+" WHERE "+strings.Join(predicates, " OR ")+";")
		}
	}
	return map[string]any{"insertCount": inserts, "updateCount": updates, "deleteCount": deletes, "statementCount": len(statements), "syncStatements": statements, "syncSql": strings.Join(statements, "\n")}, nil
}
func compareChecksums(columns []string, rows [][]any) map[string]string {
	out := map[string]string{}
	for i, c := range columns {
		h := sha256.New()
		for _, row := range rows {
			var v any
			if i < len(row) {
				v = row[i]
			}
			data, _ := json.Marshal(v)
			h.Write(data)
			h.Write([]byte{0})
		}
		out[c] = hex.EncodeToString(h.Sum(nil))
	}
	return out
}
func (s *Server) prepareDataFromTables(ctx context.Context, p map[string]any, missing bool) (map[string]any, error) {
	srcID, tgtID := str(p, "sourceConnectionId"), str(p, "targetConnectionId")
	srcCfg, err := s.compareConnection(srcID)
	if err != nil {
		return nil, err
	}
	tgtCfg, err := s.compareConnection(tgtID)
	if err != nil {
		return nil, err
	}
	srcKind, err := compareKind(srcCfg.Type)
	if err != nil {
		return nil, err
	}
	tgtKind, err := compareKind(tgtCfg.Type)
	if err != nil {
		return nil, err
	}
	cols := compareStrings(p["columns"])
	keys := compareStrings(p["keyColumns"])
	srcCols := compareStrings(p["sourceColumns"])
	columnInfo := any(nil)
	pre := []string{}
	if missing {
		columnInfo, err = s.compareMetadata(ctx, srcID, str(p, "sourceDatabase"), str(p, "sourceSchema"), str(p, "sourceTable"), "columns")
		if err != nil {
			return nil, err
		}
		for _, c := range compareList(columnInfo) {
			cols = append(cols, str(c, "name"))
		}
		srcCols = cols
		create, err := schemaCreateFromColumns(tgtKind, str(p, "targetSchema"), str(p, "targetTable"), compareList(columnInfo), nil, nil)
		if err != nil {
			return nil, err
		}
		pre = append(pre, create...)
	} else {
		columnInfo, err = s.compareMetadata(ctx, tgtID, str(p, "targetDatabase"), str(p, "targetSchema"), str(p, "targetTable"), "columns")
		if err != nil {
			return nil, err
		}
	}
	if len(srcCols) == 0 {
		srcCols = cols
	}
	if len(srcCols) != len(cols) {
		return nil, errors.New("sourceColumns must match columns")
	}
	if len(keys) == 0 && !missing {
		return nil, errors.New("At least one key column is required")
	}
	threshold := mapValue(p, "degradationThreshold")
	budget := numberValue(threshold, "fullCompareMaxRows", 100000)
	if budget < 1 {
		budget = 1
	}
	if budget > 1000000 {
		budget = 1000000
	}
	batch := numberValue(p, "fetchBatchSize", 1000)
	if batch < 1 {
		batch = 1
	}
	if batch > 10000 {
		batch = 10000
	}
	count := func(id, db, schema, table, kind string) (int, error) {
		name, err := compareTable(kind, schema, table)
		if err != nil {
			return 0, err
		}
		result, err := s.database.ExecuteSQL(ctx, id, db, "SELECT COUNT(*) FROM "+name, 1)
		if err != nil {
			return 0, err
		}
		if len(result.Rows) == 0 || len(result.Rows[0]) == 0 {
			return 0, errors.New("Count query returned no count")
		}
		v := reflect.ValueOf(result.Rows[0][0])
		if v.IsValid() {
			n, err := strconv.Atoi(fmt.Sprint(result.Rows[0][0]))
			if err == nil {
				return n, nil
			}
		}
		return 0, errors.New("Invalid count result")
	}
	srcCount, err := count(srcID, str(p, "sourceDatabase"), str(p, "sourceSchema"), str(p, "sourceTable"), srcKind)
	if err != nil {
		return nil, err
	}
	tgtCount := 0
	if !missing {
		tgtCount, err = count(tgtID, str(p, "targetDatabase"), str(p, "targetSchema"), str(p, "targetTable"), tgtKind)
		if err != nil {
			return nil, err
		}
	}
	fetch := func(id, db, schema, table, kind string, names, orderedKeys []string) ([][]any, error) {
		name, err := compareTable(kind, schema, table)
		if err != nil {
			return nil, err
		}
		quoted := []string{}
		for _, c := range names {
			if err = compareIdentifier(c); err != nil {
				return nil, err
			}
			quoted = append(quoted, quoteSQLName(c, kind))
		}
		order := []string{}
		for _, k := range orderedKeys {
			order = append(order, quoteSQLName(k, kind))
		}
		if len(order) == 0 {
			order = quoted
		}
		base := "SELECT " + strings.Join(quoted, ", ") + " FROM " + name + " ORDER BY " + strings.Join(order, ", ")
		rows := [][]any{}
		for len(rows) < budget {
			limit := batch
			if limit > budget-len(rows) {
				limit = budget - len(rows)
			}
			result, err := s.database.ExecuteSQL(ctx, id, db, fmt.Sprintf("%s LIMIT %d OFFSET %d", base, limit, len(rows)), limit)
			if err != nil {
				return nil, err
			}
			rows = append(rows, result.Rows...)
			if len(result.Rows) < limit {
				break
			}
		}
		return rows, nil
	}
	srcKeys := []string{}
	for _, k := range keys {
		found := false
		for i, c := range cols {
			if c == k {
				srcKeys = append(srcKeys, srcCols[i])
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("Unknown key column %q", k)
		}
	}
	source, err := fetch(srcID, str(p, "sourceDatabase"), str(p, "sourceSchema"), str(p, "sourceTable"), srcKind, srcCols, srcKeys)
	if err != nil {
		return nil, err
	}
	target := [][]any{}
	if !missing {
		target, err = fetch(tgtID, str(p, "targetDatabase"), str(p, "targetSchema"), str(p, "targetTable"), tgtKind, cols, keys)
		if err != nil {
			return nil, err
		}
	}
	diff, err := compareDataRows(cols, keys, source, target, missing)
	if err != nil {
		return nil, err
	}
	plan, err := buildDataSyncPlan([]map[string]any{{"tableName": p["targetTable"], "schema": p["targetSchema"], "columns": cols, "keyColumns": keys, "columnInfo": columnInfo, "diff": diff, "databaseType": tgtKind, "preSyncStatements": pre}})
	if err != nil {
		return nil, err
	}
	truncated := srcCount > budget || tgtCount > budget
	level, method, confidence, rate := "full", "full_compare", 1.0, 1.0
	if missing {
		method = "missing_target_full"
	}
	if truncated {
		level = "sample"
		method = "ordered_prefix"
		confidence = 0.0
		max := srcCount
		if tgtCount > max {
			max = tgtCount
		}
		rate = float64(budget) / float64(max)
	}
	out := map[string]any{"result": diff, "syncStatements": plan["syncStatements"], "syncSql": plan["syncSql"], "preSyncStatements": pre, "sourceRowCount": srcCount, "targetRowCount": tgtCount, "sourceTruncated": srcCount > budget, "targetTruncated": tgtCount > budget, "rowBudget": budget, "degradationLevel": level, "samplingRate": rate, "confidenceScore": confidence, "verificationMethod": method}
	if enabled, ok := p["enableChecksum"]; !ok || enabled == true {
		out["sourceChecksums"] = compareChecksums(cols, source)
		out["targetChecksums"] = compareChecksums(cols, target)
	}
	return out, nil
}
