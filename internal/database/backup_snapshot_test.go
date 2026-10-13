package database

import (
	"context"
	"testing"
)

func TestBackupSnapshotReadOnlyAndClose(t *testing.T) {
	m, c := testManager(t)
	run(t, m, "CREATE TABLE snapshot_test(value TEXT)")
	run(t, m, "INSERT INTO snapshot_test VALUES('before')")
	c.ReadOnly = true
	if err := m.Connect(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	id, err := m.BeginBackupSnapshot(context.Background(), c.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.ValidateBackupSnapshot(c.ID, "main", id); err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"connectionId": c.ID, "database": "main", "clientSessionId": id, "sql": "SELECT value FROM snapshot_test"}
	result, err := m.Handle(context.Background(), "/api/query/execute", request)
	if err != nil {
		t.Fatal(err)
	}
	if result.(QueryResult).Rows[0][0] != "before" {
		t.Fatal(result)
	}
	request["sql"] = "DELETE FROM snapshot_test"
	if _, err = m.Handle(context.Background(), "/api/query/execute", request); err == nil {
		t.Fatal("snapshot accepted a write")
	}
	if err = m.ValidateBackupSnapshot(c.ID, "other", id); err == nil {
		t.Fatal("snapshot crossed database scope")
	}
	if _, err = m.Handle(context.Background(), "/api/transaction/rollback", map[string]any{"txnSessionId": id}); err != nil {
		t.Fatal(err)
	}
	if err = m.ValidateBackupSnapshot(c.ID, "main", id); err == nil {
		t.Fatal("closed snapshot remained valid")
	}
	if got := run(t, m, "SELECT value FROM snapshot_test").Rows[0][0]; got != "before" {
		t.Fatal(got)
	}
}
