package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/docs/") {
		return false
	}
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return true
	}
	var result any
	var err error
	switch r.URL.Path {
	case "/api/docs/snapshot":
		result, err = s.collectDocs(r.Context(), p)
	case "/api/docs/export":
		err = validateDocsAnnotations(mapValue(p, "annotations"))
		if err == nil {
			snapshot := mapValue(p, "snapshot")
			kind := str(mapValue(snapshot, "project"), "databaseType")
			snapshot = applyDocsAnnotations(snapshot, mapValue(p, "annotations"), kind)
			var content string
			content, err = exportDocsHTML(snapshot, str(p, "lang"))
			result = map[string]any{"content": content}
		}
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
func docsNullable(value any) any {
	if value == nil || fmt.Sprint(value) == "" {
		return nil
	}
	return value
}
func docsKey(kind, schema, name string) string {
	fold := func(v string) string {
		if kind == "clickhouse" || kind == "mongodb" {
			return v
		}
		if kind == "oracle" {
			return strings.ToUpper(v)
		}
		return strings.ToLower(v)
	}
	if schema != "" {
		return fold(schema) + "." + fold(name)
	}
	return fold(name)
}
func docsNormalizeColumn(c map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range c {
		out[k] = v
	}
	for _, key := range []string{"column_default", "extra", "comment", "numeric_precision", "numeric_scale", "character_maximum_length"} {
		if _, ok := out[key]; !ok {
			out[key] = nil
		}
	}
	return out
}
func docsNormalizeIndex(c map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range c {
		out[k] = v
	}
	for _, key := range []string{"filter", "index_type", "included_columns", "comment"} {
		if _, ok := out[key]; !ok {
			out[key] = nil
		}
	}
	return out
}
func (s *Server) collectDocs(ctx context.Context, p map[string]any) (map[string]any, error) {
	id, db := str(p, "connectionId"), str(p, "database")
	cfg, err := s.compareConnection(id)
	if err != nil {
		return nil, err
	}
	schemas := compareStrings(p["schemas"])
	if len(schemas) == 0 {
		metadata, e := s.compareMetadata(ctx, id, db, "", "", "schemas")
		if e != nil {
			return nil, e
		}
		schemas = compareStrings(metadata)
	}
	if len(schemas) == 0 {
		schemas = []string{""}
	}
	tables := []map[string]any{}
	warnings := []map[string]any{}
	selected := compareStrings(p["tables"])
	relationships := []map[string]any{}
	enums := []map[string]any{}
	enumKeys := map[string]bool{}
	for _, schema := range schemas {
		metadata, e := s.compareMetadata(ctx, id, db, schema, "", "tables")
		if e != nil {
			return nil, e
		}
		for _, table := range compareList(metadata) {
			name := str(table, "name")
			if len(selected) > 0 {
				match := false
				for _, chosen := range selected {
					if chosen == name || chosen == schema+"."+name {
						match = true
						break
					}
				}
				if !match {
					continue
				}
			}
			columns, e := s.compareMetadata(ctx, id, db, schema, name, "columns")
			if e != nil {
				warnings = append(warnings, map[string]any{"kind": "tableSkipped", "table": schema + "." + name, "reason": e.Error()})
				continue
			}
			indexes, e := s.compareMetadata(ctx, id, db, schema, name, "indexes")
			if e != nil {
				warnings = append(warnings, map[string]any{"kind": "tableSkipped", "table": schema + "." + name, "reason": e.Error()})
				continue
			}
			fks, e := s.compareMetadata(ctx, id, db, schema, name, "foreign-keys")
			if e != nil {
				warnings = append(warnings, map[string]any{"kind": "tableSkipped", "table": schema + "." + name, "reason": e.Error()})
				continue
			}
			kind := str(table, "table_type")
			if kind == "" {
				kind = "TABLE"
			}
			viewDefinition := any(nil)
			if kind != "TABLE" {
				ddl, e := s.compareMetadata(ctx, id, db, schema, name, "ddl")
				if e != nil {
					warnings = append(warnings, map[string]any{"kind": "tableSkipped", "table": schema + "." + name, "reason": e.Error()})
					continue
				}
				viewDefinition = ddl
			}
			columnNotes := map[string]any{}
			normalizedCols := []map[string]any{}
			for _, c := range compareList(columns) {
				c = docsNormalizeColumn(c)
				normalizedCols = append(normalizedCols, c)
				if comment := str(c, "comment"); comment != "" {
					columnNotes[str(c, "name")] = map[string]any{"note": comment, "source": "DATABASE", "shadowed": nil}
				}
				if values := compareStrings(c["enum_values"]); len(values) > 0 {
					enumName := name + "_" + str(c, "name")
					key := schema + "." + enumName
					if !enumKeys[key] {
						enums = append(enums, map[string]any{"schema": docsNullable(schema), "name": enumName, "values": values, "note": nil, "synthesized": true})
						enumKeys[key] = true
					}
				}
			}
			normalizedIndexes := []map[string]any{}
			for _, idx := range compareList(indexes) {
				normalizedIndexes = append(normalizedIndexes, docsNormalizeIndex(idx))
			}
			note, noteSource := docsNullable(table["comment"]), "NONE"
			if note != nil {
				noteSource = "DATABASE"
			}
			normalizedFK := compareList(fks)
			docTable := map[string]any{"schema": docsNullable(schema), "name": name, "kind": kind, "columns": normalizedCols, "indexes": normalizedIndexes, "foreignKeys": normalizedFK, "groupId": nil, "note": note, "noteSource": noteSource, "shadowedNote": nil, "columnNotes": columnNotes, "estimatedRows": nil, "viewDefinition": viewDefinition}
			tables = append(tables, docTable)
			for _, fk := range normalizedFK {
				refSchema := str(fk, "ref_schema")
				if refSchema == "" {
					refSchema = schema
				}
				cardinality := "MANY_TO_ONE"
				for _, idx := range normalizedIndexes {
					cols := compareStrings(idx["columns"])
					if len(cols) == 1 && cols[0] == str(fk, "column") && (boolValue(idx, "is_unique") || boolValue(idx, "is_primary")) {
						cardinality = "ONE_TO_ONE"
					}
				}
				relationships = append(relationships, map[string]any{"id": docsKey(cfg.Type, schema, name) + "." + str(fk, "name") + "." + str(fk, "column"), "name": docsNullable(fk["name"]), "from": map[string]any{"schema": docsNullable(schema), "table": name, "column": fk["column"]}, "to": map[string]any{"schema": docsNullable(refSchema), "table": fk["ref_table"], "column": fk["ref_column"]}, "cardinality": cardinality, "onUpdate": docsNullable(fk["on_update"]), "onDelete": docsNullable(fk["on_delete"])})
			}
		}
	}
	projectName := str(p, "projectName")
	if projectName == "" {
		projectName = cfg.Name
	}
	if projectName == "" {
		projectName = db
	}
	if cfg.Type == "sqlite" {
		warnings = append(warnings, map[string]any{"kind": "commentsUnsupported", "engine": "sqlite"})
	}
	snapshot := map[string]any{"formatVersion": 1, "project": map[string]any{"name": projectName, "databaseType": cfg.Type, "database": docsNullable(db), "schemas": schemas, "generatedAt": time.Now().UTC().Format(time.RFC3339), "note": nil}, "tables": tables, "relationships": relationships, "groups": []map[string]any{}, "enums": enums, "warnings": warnings}
	return snapshot, nil
}
func validateDocsAnnotations(p map[string]any) error { _, err := parseDocsAnnotation(p); return err }
func applyDocsAnnotations(snapshot, annotations map[string]any, kind string) map[string]any {
	note, err := parseDocsAnnotation(annotations)
	if err != nil {
		return snapshot
	}
	return applyDocsAnnotation(snapshot, note, kind)
}

const docsHTMLTemplate = `<!doctype html><html lang="{{.Lang}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Project.name}} · Database documentation</title><style>
:root{font-family:system-ui,sans-serif;color:#182230;background:#f2f5f9}body{max-width:1200px;margin:auto;padding:32px}header{margin-bottom:24px}h1{font-size:30px;margin:0 0 8px}.muted{color:#617084}input{width:100%;box-sizing:border-box;padding:12px;border:1px solid #ccd4df;border-radius:8px;background:white;margin:12px 0 24px}.table-card{background:white;border:1px solid #dae1eb;border-radius:12px;padding:24px;margin:0 0 20px;break-inside:avoid}h2{font-size:20px;margin:0 0 10px}.note{white-space:pre-wrap;line-height:1.6;background:#f6f8fc;padding:12px;border-radius:8px}table{width:100%;border-collapse:collapse;font-size:14px;margin:16px 0}th,td{text-align:left;vertical-align:top;border-bottom:1px solid #e4e9f0;padding:10px}th{background:#f7f9fc;color:#5b6a7d}code,pre{font-family:ui-monospace,monospace}pre{white-space:pre-wrap;background:#f6f8fc;padding:12px;overflow:auto}.tag{font-size:12px;background:#eaf0fb;color:#315582;border-radius:5px;padding:3px 7px}.relations{padding:12px 24px;background:white;border-radius:12px}.warning{color:#8a4b0a}details{margin-top:12px}footer{font-size:12px;color:#617084;margin-top:28px}@media print{body{padding:0;background:white}input{display:none}.table-card{border-radius:0}details>*{display:block}}
</style></head><body><header><h1>{{.Project.name}}</h1><div class="muted">{{.Project.databaseType}} · {{.Project.database}} · {{.Project.generatedAt}}</div>{{if .Project.note}}<p class="note">{{.Project.note}}</p>{{end}}</header><input id="search" type="search" aria-label="Search tables and columns" placeholder="Search tables, columns and notes…">
{{range .Groups}}<div class="note"><strong>{{.name}}</strong>{{if .note}} · {{.note}}{{end}}</div>{{end}}
{{range .Tables}}<article class="table-card"><h2>{{if .schema}}{{.schema}}.{{end}}{{.name}} <span class="tag">{{.kind}}</span></h2>{{if .note}}<p class="note">{{.note}}</p>{{end}}<table><thead><tr><th>Column</th><th>Type</th><th>Nullable</th><th>Default</th><th>Description</th></tr></thead><tbody>{{$notes:=.columnNotes}}{{range .columns}}<tr><td><code>{{.name}}</code>{{if .is_primary_key}} <span class="tag">PK</span>{{end}}</td><td><code>{{.data_type}}</code></td><td>{{if .is_nullable}}YES{{else}}NO{{end}}</td><td><code>{{.column_default}}</code></td><td>{{with index $notes .name}}{{.note}}{{end}}</td></tr>{{end}}</tbody></table>{{if .indexes}}<details><summary>Indexes</summary><table><tr><th>Name</th><th>Columns</th><th>Unique</th></tr>{{range .indexes}}<tr><td>{{.name}}</td><td>{{join .columns ", "}}</td><td>{{.is_unique}}</td></tr>{{end}}</table></details>{{end}}{{if .viewDefinition}}<details><summary>View definition</summary><pre>{{.viewDefinition}}</pre></details>{{end}}</article>{{end}}
{{if .Relationships}}<section class="relations"><h2>Relationships</h2>{{range .Relationships}}<p><code>{{if .from.schema}}{{.from.schema}}.{{end}}{{.from.table}}.{{.from.column}}</code> → <code>{{if .to.schema}}{{.to.schema}}.{{end}}{{.to.table}}.{{.to.column}}</code> <span class="tag">{{.cardinality}}</span></p>{{end}}</section>{{end}}
{{if .Enums}}<section class="relations"><h2>Enums</h2>{{range .Enums}}<p><strong>{{.name}}</strong>: {{join .values ", "}}</p>{{end}}</section>{{end}}
{{range .Warnings}}<p class="warning">{{.kind}}{{if .table}} · {{.table}}{{end}}{{if .reason}} · {{.reason}}{{end}}{{if .count}} · {{.count}}{{end}}</p>{{end}}<footer>Standalone documentation generated by Database Workbench. All data is embedded in this file.</footer><script>document.getElementById('search').addEventListener('input',function(){const term=this.value.toLocaleLowerCase();document.querySelectorAll('.table-card').forEach(card=>card.hidden=!card.textContent.toLocaleLowerCase().includes(term));});</script></body></html>`

func exportDocsHTML(snapshot map[string]any, lang string) (string, error) {
	if numberValue(snapshot, "formatVersion", 0) != 1 {
		return "", errors.New("Snapshot formatVersion must be 1")
	}
	if lang == "" {
		lang = "en"
	}
	tmpl, err := template.New("docs").Funcs(template.FuncMap{"join": func(v any, sep string) string { return strings.Join(compareStrings(v), sep) }}).Parse(docsHTMLTemplate)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	err = tmpl.Execute(&buf, map[string]any{"Lang": lang, "Project": snapshot["project"], "Tables": snapshot["tables"], "Relationships": snapshot["relationships"], "Groups": compareList(snapshot["groups"]), "Enums": snapshot["enums"], "Warnings": snapshot["warnings"]})
	return buf.String(), err
}
