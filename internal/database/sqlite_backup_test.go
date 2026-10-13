package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteOnlineBackupRestoreAndPermissions(t *testing.T) {
	m, c := testManager(t)
	run(t, m, "CREATE TABLE entries(id INTEGER PRIMARY KEY,value TEXT)")
	run(t, m, "INSERT INTO entries VALUES(1,'kept')")
	backup := filepath.Join(t.TempDir(), "backup.sqlite")
	if e := m.SQLiteBackup(context.Background(), c.ID, backup); e != nil {
		t.Fatal(e)
	}
	valid, e := IsSQLiteFile(backup)
	if e != nil || !valid {
		t.Fatal(valid, e)
	}
	run(t, m, "DELETE FROM entries")
	if e = m.SQLiteRestore(context.Background(), c.ID, backup); e != nil {
		t.Fatal(e)
	}
	if run(t, m, "SELECT value FROM entries").Rows[0][0] != "kept" {
		t.Fatal("restore lost snapshot")
	}
	if e = m.SQLiteBackup(context.Background(), c.ID, c.Host); e == nil {
		t.Fatal("backup overwrote source")
	}
	bad := filepath.Join(t.TempDir(), "bad.sqlite")
	os.WriteFile(bad, []byte("invalid database"), 0600)
	if e = m.SQLiteRestore(context.Background(), c.ID, bad); e == nil {
		t.Fatal("restored invalid file")
	}
	c.Production = true
	if e = m.Connect(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	if e = m.SQLiteRestore(context.Background(), c.ID, backup); e == nil {
		t.Fatal("restore bypassed production lock")
	}
	if e = m.SQLiteBackup(context.Background(), c.ID, filepath.Join(t.TempDir(), "protected-backup.sqlite")); e != nil {
		t.Fatal("read backup blocked", e)
	}
}

func TestSQLiteRestoreRejectsCorruptPagesAndClosesTransactions(t *testing.T) {
	m, c := testManager(t)
	run(t, m, "CREATE TABLE entries(id INTEGER PRIMARY KEY,value TEXT)")
	run(t, m, "INSERT INTO entries VALUES(1,'original')")
	backup := filepath.Join(t.TempDir(), "snapshot.sqlite")
	if err := m.SQLiteBackup(context.Background(), c.ID, backup); err != nil {
		t.Fatal(err)
	}
	corrupt, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	for i := 100; i < len(corrupt); i++ {
		corrupt[i] = 0xff
	}
	bad := filepath.Join(t.TempDir(), "corrupt.sqlite")
	if err = os.WriteFile(bad, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if err = m.SQLiteRestore(context.Background(), c.ID, bad); err == nil {
		t.Fatal("header-only source bypassed integrity check")
	}
	if got := run(t, m, "SELECT value FROM entries").Rows[0][0]; got != "original" {
		t.Fatal(got)
	}
	_, err = m.Handle(context.Background(), "/api/query/execute-multi", map[string]any{"connectionId": c.ID, "clientSessionId": "editor", "sql": "BEGIN; UPDATE entries SET value='uncommitted'"})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.SQLiteRestore(context.Background(), c.ID, backup); err != nil {
		t.Fatal(err)
	}
	if m.HasOpenTransaction(c.ID, "editor") {
		t.Fatal("restore left old transaction open")
	}
	if got := run(t, m, "SELECT value FROM entries").Rows[0][0]; got != "original" {
		t.Fatal(got)
	}
}

func TestSQLiteRestoreCancelledCopyPreservesTarget(t *testing.T) {
	m, c := testManager(t)
	run(t, m, "CREATE TABLE entries(value TEXT)")
	run(t, m, "INSERT INTO entries VALUES('snapshot')")
	backup := filepath.Join(t.TempDir(), "backup.sqlite")
	if err := m.SQLiteBackup(context.Background(), c.ID, backup); err != nil {
		t.Fatal(err)
	}
	run(t, m, "UPDATE entries SET value='target'")
	// An external write lock forces the online restore to wait for its commit.
	db, err := sql.Open("sqlite", c.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err = m.SQLiteRestore(ctx, c.ID, backup); err == nil {
		t.Fatal("restore ignored cancellation")
	}
	if _, err = conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if got := run(t, m, "SELECT value FROM entries").Rows[0][0]; got != "target" {
		t.Fatal("cancelled restore changed target", got)
	}
}
