package database

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSQLiteFileURLPreservesAbsolutePaths(t *testing.T) {
	for _, fixture := range []struct {
		name string
		path string
		uri  string
	}{
		{"unix", "/tmp/data with spaces.sqlite", "file:///tmp/data%20with%20spaces.sqlite"},
		{"windows drive", "C:/Users/runner/data with spaces.sqlite", "file:///C:/Users/runner/data%20with%20spaces.sqlite"},
		{"windows share", "//server/share/data.sqlite", "file:////server/share/data.sqlite"},
		{"escaped characters", "C:/data/#100% +&.sqlite", "file:///C:/data/%23100%25%20+&.sqlite"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			u := sqliteFileURL(filepath.FromSlash(fixture.path))
			if got := u.String(); got != fixture.uri {
				t.Fatalf("SQLite file URI = %q, want %q", got, fixture.uri)
			}
			parsed, err := url.Parse(u.String())
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Host != "" || parsed.Scheme != "file" {
				t.Fatalf("SQLite file path became an authority: %#v", parsed)
			}
		})
	}
	if runtime.GOOS != "windows" {
		t.Run("POSIX literal backslash", func(t *testing.T) {
			u := sqliteFileURL(`/tmp/literal\backslash.sqlite`)
			if got := u.String(); got != "file:///tmp/literal%5Cbackslash.sqlite" {
				t.Fatalf("POSIX backslash changed into a separator: %q", got)
			}
		})
	}
}

func TestSQLiteSpecialFilePathsConnectBackupAndRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m := NewManager()
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Errorf("close database manager: %v", err)
		}
	})
	name := "喜鹊 #100% +&.sqlite"
	if runtime.GOOS != "windows" {
		name = `喜鹊 \#100% +&.sqlite`
	}
	cfg := Config{ID: "special-path", Type: "sqlite", Host: filepath.Join(dir, name)}
	if err := m.Connect(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE TABLE sample(value TEXT)",
		"INSERT INTO sample VALUES('before backup')",
	} {
		if _, err := m.ExecuteSQL(ctx, cfg.ID, "main", statement, 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(cfg.Host); err != nil {
		t.Fatalf("SQLite opened a different file: %v", err)
	}
	backup := filepath.Join(dir, "备份 #100% +&.sqlite")
	if err := m.SQLiteBackup(ctx, cfg.ID, backup); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ExecuteSQL(ctx, cfg.ID, "main", "UPDATE sample SET value='after backup'", 1); err != nil {
		t.Fatal(err)
	}
	if err := m.SQLiteRestore(ctx, cfg.ID, backup); err != nil {
		t.Fatal(err)
	}
	result, err := m.ExecuteSQL(ctx, cfg.ID, "main", "SELECT value FROM sample", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0][0] != "before backup" {
		t.Fatalf("backup was not restored: %#v", result.Rows)
	}
}
