package server

import (
	"strings"
	"testing"
)

func TestDocsAnnotationPrecedenceAndSafeStandaloneExport(t *testing.T) {
	snapshot := map[string]any{"formatVersion": 1, "project": map[string]any{"name": "<script>alert(1)</script>", "databaseType": "postgres"}, "tables": []map[string]any{{"schema": "public", "name": "users", "kind": "TABLE", "note": "Database note", "noteSource": "DATABASE", "columns": []map[string]any{{"name": "id", "data_type": "integer", "comment": "DB column note"}}, "columnNotes": map[string]any{}}}, "warnings": []any{}}
	annotations := map[string]any{"formatVersion": 1, "groups": []map[string]any{{"id": "g", "name": "Group", "hue": 240}}, "tables": map[string]any{"public.users": map[string]any{"note": "Local note", "group": "g", "columns": map[string]any{"ID": map[string]any{"note": "Local column note"}}}, "public.deleted": map[string]any{"note": "Orphan"}}}
	if err := validateDocsAnnotations(annotations); err != nil {
		t.Fatal(err)
	}
	applied := applyDocsAnnotations(snapshot, annotations, "postgres")
	table := compareList(applied["tables"])[0]
	if table["note"] != "Local note" || table["shadowedNote"] != "Database note" || table["groupId"] != "g" {
		t.Fatal(table)
	}
	if str(mapValue(table, "columnNotes")["id"].(map[string]any), "shadowed") != "DB column note" {
		t.Fatal(table)
	}
	html, err := exportDocsHTML(applied, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "<script>alert(1)</script>") || !strings.Contains(html, "Local column note") || !strings.Contains(html, "orphanedNotes") {
		t.Fatal(html)
	}
}
func TestDocsRejectsInvalidNotes(t *testing.T) {
	for _, notes := range []map[string]any{{"formatVersion": 2}, {"formatVersion": 1, "unknown": true}, {"formatVersion": 1, "groups": []map[string]any{{"id": "g", "name": "G", "hue": 999}}}} {
		if err := validateDocsAnnotations(notes); err == nil {
			t.Fatal("Accepted invalid notes", notes)
		}
	}
}
