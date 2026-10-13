package desktopfiles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPickerGrantsUTF8VersionAndConflict(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "query.sql")
	if err := os.WriteFile(path, []byte("\ufeffSELECT '中文';"), 0600); err != nil {
		t.Fatal(err)
	}
	g := New()
	if _, err := g.Read(path, 0); err == nil {
		t.Fatal("unselected file readable")
	}
	if err := g.Grant(path, false); err != nil {
		t.Fatal(err)
	}
	s, err := g.Read(path, 0)
	if err != nil || s.Content != "SELECT '中文';" || s.Version.ContentHash == "" {
		t.Fatal(s, err)
	}
	if err = os.WriteFile(path, []byte("SELECT 'outside edit';"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := g.Write(path, "SELECT 'new';", WriteOptions{ExpectedContentHash: s.Version.ContentHash})
	if err != nil || r.Kind != "conflict" {
		t.Fatal(r, err)
	}
	s, err = g.Read(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	r, err = g.Write(path, "SELECT 'saved';", WriteOptions{ExpectedContentHash: s.Version.ContentHash})
	if err != nil || r.Kind != "written" {
		t.Fatal(r, err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	r, err = g.Write(path, "SELECT 'recreate';", WriteOptions{ExpectedContentHash: r.Version.ContentHash})
	if err != nil || r.Kind != "missing" {
		t.Fatal(r, err)
	}
	r, err = g.Write(path, "SELECT 'recreate';", WriteOptions{ExpectedMissing: true})
	if err != nil || r.Kind != "written" {
		t.Fatal(r, err)
	}
}
func TestFolderOperationsCannotEscapeOrOverwrite(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	g := New()
	if err := g.Grant(root, true); err != nil {
		t.Fatal(err)
	}
	file, err := g.Create(root, root, "a.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.Create(root, root, "../escape.sql"); err == nil {
		t.Fatal("created outside root")
	}
	if _, err = g.Create(root, root, "a.sql"); err == nil {
		t.Fatal("overwrote existing file")
	}
	if err = os.Symlink(outside, filepath.Join(root, "escape")); err == nil {
		if _, err = g.Create(root, filepath.Join(root, "escape"), "b.sql"); err == nil {
			t.Fatal("followed symlink outside selected root")
		}
	}
	if _, err = g.Create(root, root, "b.sql"); err != nil {
		t.Fatal(err)
	}
	if _, err = g.Rename(root, file, "b.sql"); err == nil {
		t.Fatal("rename clobbered existing file")
	}
	file, err = g.Rename(root, file, "c.sql")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := g.List(root, "*.sql")
	if err != nil || len(entries) != 2 {
		t.Fatal(entries, err)
	}
	if err = g.Delete(root, root); err == nil {
		t.Fatal("deleted selected root")
	}
	if err = g.Delete(root, file); err != nil {
		t.Fatal(err)
	}
}
