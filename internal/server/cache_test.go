package server

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCacheReadsAreIsolatedAndDoNotRewriteState(t *testing.T) {
	dir := t.TempDir()
	s := testServer(t, dir)
	if err := s.update(func(st *diskState) error {
		st.Settings["schema.cache"] = map[string]any{"schema": map[string]any{"tables": []any{map[string]any{"name": "before"}}}}
		st.Settings["tab.cache"] = map[string]any{"cached": map[string]any{"payloadBase64": "eA==", "lastAccessedAt": 1, "createdAt": 1}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.enc")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	schema := s.schemaCacheEntry("schema").(map[string]any)
	schema["tables"].([]any)[0].(map[string]any)["name"] = "after"
	entry := s.tabCacheEntry("cached")
	access := cacheAccessTime(entry["lastAccessedAt"])
	entry["payloadBase64"] = "after"
	metadata := s.tabCacheMetadata()
	if _, exists := metadata[0]["payloadBase64"]; exists {
		t.Fatal("metadata included cache payload")
	}
	metadata[0]["createdAt"] = 2
	if got := s.schemaCacheEntry("schema").(map[string]any)["tables"].([]any)[0].(map[string]any)["name"]; got != "before" {
		t.Fatal("schema read exposed live state")
	}
	if got := s.tabCacheEntry("cached")["payloadBase64"]; got != "eA==" {
		t.Fatal("cache read exposed live state")
	}
	if got := cacheAccessTime(s.tabCacheMetadata()[0]["createdAt"]); got != 1 {
		t.Fatal("metadata read exposed live state")
	}
	if s.tabCacheEntry("missing") != nil || s.schemaCacheEntry("missing") != nil {
		t.Fatal("missing cache returned an entry")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("cache read rewrote encrypted state", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again := testServer(t, dir)
	if got := cacheAccessTime(again.tabCacheMetadata()[0]["lastAccessedAt"]); got < access {
		t.Fatal("cache recency was not flushed on Close")
	}
	if again.tabCacheEntry("cached")["payloadBase64"] != "eA==" {
		t.Fatal("cache payload missing after restart")
	}
}

func TestSchemaTabCachesPersistPruneAndOwner(t *testing.T) {
	dir := t.TempDir()
	s := testServer(t, dir)
	cookie := loginTest(t, s)
	value(t, call(s, "POST", "/api/schema/cache", map[string]any{"cacheKey": "conn::tables", "payload": map[string]any{"tables": []any{"a"}}}, cookie, ""))
	for _, key := range []string{"live", "orphan"} {
		value(t, call(s, "POST", "/api/tab-runtime-cache", map[string]any{"key": key, "payloadBase64": base64.StdEncoding.EncodeToString([]byte(key)), "rowCount": 1, "columnCount": 1, "ownerId": "conn"}, cookie, ""))
	}
	if call(s, "POST", "/api/tab-runtime-cache", map[string]any{"key": "bad", "payloadBase64": "@", "rowCount": 1, "columnCount": 1}, cookie, "").Code == 200 {
		t.Fatal("bad base64 accepted")
	}
	result := value(t, call(s, "POST", "/api/tab-runtime-cache/prune", map[string]any{"liveKeys": []any{"live"}, "maxBytes": 0, "orphanGraceMs": 0}, cookie, "")).(map[string]any)
	if result["deletedEntries"] != float64(1) || result["remainingEntries"] != float64(1) {
		t.Fatal(result)
	}
	s.Close()
	again := testServer(t, dir)
	cookie = loginTest(t, again)
	if value(t, call(again, "GET", "/api/schema/cache?cache_key=conn::tables", nil, cookie, "")) == nil {
		t.Fatal("schema cache missing")
	}
	entry := value(t, call(again, "GET", "/api/tab-runtime-cache?key=live", nil, cookie, "")).(map[string]any)
	if entry["payloadBase64"] != base64.StdEncoding.EncodeToString([]byte("live")) {
		t.Fatal(entry)
	}
	deleted := value(t, call(again, "DELETE", "/api/tab-runtime-cache/owner?owner_id=conn", nil, cookie, ""))
	if deleted != float64(1) {
		t.Fatal(deleted)
	}
}
func TestUserSkillFrontmatterAndSymlinkContainment(t *testing.T) {
	root := t.TempDir()
	body := "---\nname: sql-review\ndescription: |\n  Team rules\n  Second line\n---\nRead SQL carefully."
	dir := filepath.Join(root, "team")
	os.Mkdir(dir, 0700)
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0600)
	listing := listSkillRoot(root, "c")
	if listing.Status != "ok" || len(listing.Skills) != 1 || !strings.Contains(listing.Skills[0].Description, "Second line") {
		t.Fatal(listing)
	}
	result := readSkillIDs([]any{listing.Skills[0].ID, listing.Skills[0].ID, "c-missing"}, map[string]string{"c": root})
	if len(result["skills"].([]userSkill)) != 1 || len(result["failures"].([]map[string]any)) != 1 {
		t.Fatal(result)
	}
	outside := filepath.Join(t.TempDir(), "SKILL.md")
	os.WriteFile(outside, []byte(body), 0600)
	os.Remove(filepath.Join(dir, "SKILL.md"))
	if err := os.Symlink(outside, filepath.Join(dir, "SKILL.md")); err == nil {
		if len(listSkillRoot(root, "c").Skills) != 0 {
			t.Fatal("escaping skill symlink listed")
		}
		result = readSkillIDs([]any{listing.Skills[0].ID}, map[string]string{"c": root})
		if result["failures"].([]map[string]any)[0]["reason"] != "not_found" {
			t.Fatal(result)
		}
	}
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	if call(s, "POST", "/api/ai/skills/list", map[string]any{"customRootEnabled": true, "customRoot": root}, cookie, "").Code != http.StatusForbidden {
		t.Fatal("Web exposed filesystem skills")
	}
}
func TestDocumentationAnnotationsRoundtripStrictnessAndOverrides(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	value(t, call(s, "POST", "/api/connection/save", map[string]any{"configs": []any{map[string]any{"id": "docs-notes", "name": "Docs", "db_type": "postgres"}}}, cookie, ""))
	note := map[string]any{"formatVersion": 1, "tables": map[string]any{"public.orders": map[string]any{"note": "Local table", "columns": map[string]any{"ID": map[string]any{"note": "Local ID"}, "gone": map[string]any{"note": "Old"}}}}}
	value(t, call(s, "POST", "/api/docs/annotations/save", map[string]any{"connectionId": "docs-notes", "annotations": note}, cookie, ""))
	loaded := value(t, call(s, "POST", "/api/docs/annotations/load", map[string]any{"connectionId": "docs-notes"}, cookie, ""))
	if loaded == nil {
		t.Fatal("notes missing")
	}
	snapshot := map[string]any{"formatVersion": 1, "project": map[string]any{"name": "Project"}, "tables": []any{map[string]any{"schema": "public", "name": "orders", "note": "DB table", "noteSource": "DATABASE", "columns": []any{map[string]any{"name": "id", "comment": "DB ID"}}, "columnNotes": map[string]any{}}}, "warnings": []any{}}
	applied := value(t, call(s, "POST", "/api/docs/annotations/apply", map[string]any{"connectionId": "docs-notes", "snapshot": snapshot, "annotations": note}, cookie, "")).(map[string]any)
	table := applied["tables"].([]any)[0].(map[string]any)
	if table["note"] != "Local table" || table["shadowedNote"] != "DB table" {
		t.Fatal(table)
	}
	if table["columnNotes"].(map[string]any)["id"].(map[string]any)["shadowed"] != "DB ID" {
		t.Fatal(table)
	}
	if len(applied["warnings"].([]any)) != 1 {
		t.Fatal(applied)
	}
	note["tabels"] = map[string]any{}
	if call(s, "POST", "/api/docs/annotations/save", map[string]any{"connectionId": "docs-notes", "annotations": note}, cookie, "").Code == 200 {
		t.Fatal("typo silently discarded")
	}
}
