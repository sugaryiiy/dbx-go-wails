package database

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh/knownhosts"
)

// These opt-in tests operate only on uniquely named tables they create. Supply
// a private JSON array of Config values for disposable PostgreSQL/MySQL servers
// with TLS and a trusted CA, through DBX_TEST_DATABASE_CONFIGS. Credentials are
// never logged. No production database or local SSH configuration is required.
func TestNetworkDatabaseIntegration(t *testing.T) {
	path := os.Getenv("DBX_TEST_DATABASE_CONFIGS")
	if path == "" {
		t.Skip("set DBX_TEST_DATABASE_CONFIGS to run live PostgreSQL/MySQL transport tests")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read opt-in database fixture configuration:", err)
	}
	var configs []Config
	if err := json.Unmarshal(data, &configs); err != nil {
		t.Fatal("invalid opt-in database fixture JSON")
	}
	if len(configs) == 0 {
		t.Fatal("opt-in database fixture configuration is empty")
	}
	for i, original := range configs {
		original = original.normalized()
		if original.Type != "mysql" && original.Type != "postgres" {
			t.Fatal("live network fixture must be MySQL or PostgreSQL")
		}
		if !original.SSL || original.CACert == "" {
			t.Fatal("live network fixtures must configure verified TLS with a CA")
		}
		original.ConnectTimeout = 5
		original.QueryTimeout = 10
		original.ReadOnly = false
		original.Production = false
		original.ProductionDatabases = nil
		original.Transports = nil
		t.Run(fmt.Sprintf("%s_%d", original.Type, i), func(t *testing.T) {
			for _, mode := range []string{"direct_tls", "ssh_one_hop", "ssh_two_hops"} {
				t.Run(mode, func(t *testing.T) {
					c := original
					c.ID = "network-integration-" + mode
					var hops []*sshTestServer
					if mode != "direct_tls" {
						target := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
						last := newSSHTestServer(t, func(s *sshTestServer) { s.allow = map[string]bool{target: true} })
						hops = append(hops, last)
						// Exercise known_hosts, rather than bypassing that path with a pin.
						layer := networkIntegrationKnownHost(t, last)
						c.Transports = []map[string]any{layer}
						if mode == "ssh_two_hops" {
							first := newSSHTestServer(t, func(s *sshTestServer) { s.allow = map[string]bool{last.listener.Addr().String(): true} })
							hops = append(hops, first)
							c.Transports = []map[string]any{first.layer(), layer}
						}
					}
					networkIntegrationLifecycle(t, c)
					for _, hop := range hops {
						hop.waitIdle(t)
					}
				})
			}
			t.Run("reject_wrong_ca", func(t *testing.T) {
				c := original
				c.ID = "network-bad-ca"
				c.CACert = newDatabaseTLSFixture(t).caPath
				m := NewManager()
				defer m.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				err := m.Connect(ctx, c)
				var certErr *tls.CertificateVerificationError
				if err == nil || !errors.As(err, &certErr) {
					t.Fatalf("wrong CA did not fail certificate verification: %v", redact(err, c))
				}
			})
			t.Run("reject_wrong_hostname", func(t *testing.T) {
				c := original
				c.ID = "network-bad-host"
				// The fixture cert has localhost / 127.0.0.1 SANs. IPv6 loopback
				// remains reachable through this transparent relay, but does not
				// match those SANs. TLS still runs end to end with the real engine.
				address := networkIntegrationRelay(t, net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))
				c.Host, _, _ = net.SplitHostPort(address)
				_, port, _ := net.SplitHostPort(address)
				c.Port, _ = strconv.Atoi(port)
				m := NewManager()
				defer m.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				err := m.Connect(ctx, c)
				var certErr x509.HostnameError
				if err == nil || !errors.As(err, &certErr) {
					t.Fatalf("wrong hostname did not fail certificate verification: %v", redact(err, c))
				}
			})
		})
	}
}

func networkIntegrationKnownHost(t *testing.T, server *sshTestServer) map[string]any {
	t.Helper()
	layer := server.layer()
	delete(layer, "host_key_fingerprint")
	path := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{server.listener.Addr().String()}, server.signer.PublicKey()) + "\n"
	if err := os.WriteFile(path, []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	layer["known_hosts_path"] = path
	return layer
}

func networkIntegrationLifecycle(t *testing.T, c Config) {
	t.Helper()
	m := NewManager()
	t.Cleanup(func() { m.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Connect(ctx, c); err != nil {
		t.Fatalf("connect: %v", redact(err, c))
	}
	query := func(statement string) QueryResult {
		t.Helper()
		r, err := m.ExecuteSQL(ctx, c.ID, c.Database, statement, 100)
		if err != nil {
			t.Fatalf("execute fixture SQL: %v", redact(err, c))
		}
		return r
	}
	if c.Type == "postgres" {
		r := query("SELECT ssl, version, cipher FROM pg_stat_ssl WHERE pid = pg_backend_pid()")
		if len(r.Rows) != 1 || len(r.Rows[0]) != 3 || r.Rows[0][0] != true || fmt.Sprint(r.Rows[0][1]) == "" || fmt.Sprint(r.Rows[0][2]) == "" {
			t.Fatal("PostgreSQL session is not using TLS")
		}
	} else {
		r := query("SHOW SESSION STATUS LIKE 'Ssl_cipher'")
		if len(r.Rows) != 1 || len(r.Rows[0]) != 2 || fmt.Sprint(r.Rows[0][1]) == "" {
			t.Fatal("MySQL session is not using TLS")
		}
	}
	table := "dbx_network_it_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	quoted := quoteIdentifier(table, c.Type)
	query("CREATE TABLE " + quoted + " (id INTEGER PRIMARY KEY, value VARCHAR(100))")
	t.Cleanup(func() {
		cleanup := NewManager()
		defer cleanup.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := cleanup.Connect(cleanupCtx, c); err != nil {
			t.Errorf("fixture cleanup connect: %v", redact(err, c))
			return
		}
		if _, err := cleanup.ExecuteSQL(cleanupCtx, c.ID, c.Database, "DROP TABLE IF EXISTS "+quoted, 1); err != nil {
			t.Errorf("fixture cleanup: %v", redact(err, c))
		}
	})
	networkIntegrationMetadataContracts(t, ctx, m, c, table)
	if r := query("INSERT INTO " + quoted + " VALUES (1,'initial')"); r.AffectedRows != 1 {
		t.Fatal("insert affected-row count")
	}
	if r := query("UPDATE " + quoted + " SET value='updated' WHERE id=1"); r.AffectedRows != 1 {
		t.Fatal("update affected-row count")
	}
	r := query("SELECT value FROM " + quoted + " WHERE id=1")
	if len(r.Rows) != 1 || r.Rows[0][0] != "updated" {
		t.Fatal("CRUD result differs")
	}
	_, err := m.Handle(ctx, "/api/query/execute-in-transaction", map[string]any{"connectionId": c.ID, "database": c.Database, "statements": []string{"INSERT INTO " + quoted + " VALUES (2,'rollback')", "INSERT INTO " + quoted + " VALUES (1,'duplicate')"}})
	if err == nil {
		t.Fatal("failed atomic batch unexpectedly committed")
	}
	if r := query("SELECT id FROM " + quoted + " WHERE id=2"); len(r.Rows) != 0 {
		t.Fatal("transaction partially committed")
	}
	readOnly := c
	readOnly.ReadOnly = true
	if err := m.Connect(ctx, readOnly); err != nil {
		t.Fatalf("read-only reconnect: %v", redact(err, c))
	}
	if _, err := m.ExecuteSQL(ctx, c.ID, c.Database, "DELETE FROM "+quoted, 1); err == nil {
		t.Fatal("read-only connection accepted DELETE")
	}
	// SELECT passes the lexical read-only guard, so the server transaction
	// must enforce this row-lock rejection as well.
	if _, err := m.ExecuteSQL(ctx, c.ID, c.Database, "SELECT * FROM "+quoted+" FOR UPDATE", 1); err == nil {
		t.Fatal("server read-only transaction accepted FOR UPDATE")
	}
	query("SELECT id FROM " + quoted)
	if err := m.Connect(ctx, c); err != nil {
		t.Fatalf("writable reconnect: %v", redact(err, c))
	}
	query("INSERT INTO " + quoted + " VALUES (2,'reconnected')")
	if r := query("DELETE FROM " + quoted + " WHERE id=2"); r.AffectedRows != 1 {
		t.Fatal("delete affected-row count")
	}
	slow := "SELECT pg_sleep(5)"
	if c.Type == "mysql" {
		slow = "SELECT SLEEP(5)"
	}
	deadlineCtx, deadlineCancel := context.WithTimeout(ctx, 150*time.Millisecond)
	started := time.Now()
	_, err = m.ExecuteSQL(deadlineCtx, c.ID, c.Database, slow, 1)
	deadlineCancel()
	if err == nil || time.Since(started) > 3*time.Second {
		t.Fatalf("query deadline failed: %v", redact(err, c))
	}
	query("SELECT 1")
	networkIntegrationCancel(t, m, c, slow, false)
	query("SELECT 2")
	if err := m.Connect(ctx, c); err != nil {
		t.Fatalf("reconnect after cancellation: %v", redact(err, c))
	}
	query("SELECT value FROM " + quoted + " WHERE id=1")
	networkIntegrationCancel(t, m, c, slow, true)
	if _, err := m.ExecuteSQL(ctx, c.ID, c.Database, "SELECT 3", 1); err == nil {
		t.Fatal("closed manager accepted a query")
	}
	if err := m.Connect(ctx, c); err == nil {
		t.Fatal("closed manager accepted reconnect")
	}
}

func networkIntegrationCancel(t *testing.T, m *Manager, c Config, slow string, closeManager bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	executionID := "network-cancel"
	done := make(chan error, 1)
	go func() {
		_, err := m.Handle(ctx, "/api/query/execute", map[string]any{"connectionId": c.ID, "database": c.Database, "sql": slow, "executionId": executionID})
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		m.mu.RLock()
		_, running := m.running[executionID]
		m.mu.RUnlock()
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancellable query did not start")
		}
		time.Sleep(time.Millisecond)
	}
	// Let the real query reach the server before requesting cancellation.
	time.Sleep(40 * time.Millisecond)
	if closeManager {
		closed := make(chan error, 1)
		go func() { closed <- m.Close() }()
		select {
		case err := <-closed:
			if err != nil {
				t.Fatalf("close: %v", redact(err, c))
			}
		case <-time.After(3 * time.Second):
			t.Fatal("manager close blocked on a running query")
		}
	} else {
		found, err := m.Handle(ctx, "/api/query/cancel", map[string]any{"executionId": executionID})
		if err != nil || found != true {
			t.Fatalf("explicit query cancellation failed: %v", redact(err, c))
		}
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled query succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("query cancellation did not return")
	}
}

func networkIntegrationRelay(t *testing.T, target string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 loopback is unavailable for hostname-mismatch integration test")
	}
	var mu sync.Mutex
	open := map[net.Conn]bool{}
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			local, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			open[local] = true
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { local.Close(); mu.Lock(); delete(open, local); mu.Unlock() }()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				remote, err := (&net.Dialer{}).DialContext(ctx, "tcp", target)
				cancel()
				if err != nil {
					return
				}
				defer remote.Close()
				copied := make(chan struct{})
				go func() { io.Copy(remote, local); remote.Close(); close(copied) }()
				io.Copy(local, remote)
				local.Close()
				<-copied
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-done
		mu.Lock()
		for conn := range open {
			conn.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return listener.Addr().String()
}

// Use the same snake_case request keys as http.ts, and decode serialized values
// into the frontend's wire shapes. Merely checking nonempty Go records missed
// the former schemas [{name:...}] versus string[] mismatch.
func networkIntegrationMetadataContracts(t *testing.T, ctx context.Context, m *Manager, c Config, table string) {
	t.Helper()
	schema := c.Database
	if c.Type == "postgres" {
		schema = c.DefaultSchema
		if schema == "" {
			schema = "public"
		}
	}
	request := map[string]any{"connection_id": c.ID, "database": c.Database, "schema": schema, "table": table}
	call := func(path string, p map[string]any) any {
		t.Helper()
		value, err := m.Handle(ctx, path, p)
		if err != nil {
			t.Fatalf("metadata %s: %v", path, redact(err, c))
		}
		return value
	}
	decode := func(value any, target any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal("metadata could not be serialized")
		}
		if err := json.Unmarshal(data, target); err != nil {
			t.Fatalf("metadata wire contract: %v", err)
		}
	}
	var schemas []string
	decode(call("/api/schema/schemas", map[string]any{"connection_id": c.ID, "database": c.Database}), &schemas)
	hasSchema, hasPublic := false, false
	for _, name := range schemas {
		if name == schema {
			hasSchema = true
		}
		if name == "public" {
			hasPublic = true
		}
	}
	if !hasSchema || c.Type == "postgres" && !hasPublic {
		t.Fatal("schema names must include the fixture schema and PostgreSQL public")
	}
	var tables []struct {
		Name string `json:"name"`
		Type string `json:"table_type"`
	}
	decode(call("/api/schema/tables", map[string]any{"connection_id": c.ID, "database": c.Database, "schema": schema, "filter": table, "object_types": "TABLE"}), &tables)
	found := false
	for _, entry := range tables {
		if entry.Name == table && entry.Type == "TABLE" {
			found = true
		}
	}
	if !found {
		t.Fatal("tables route did not return the fixture's TableInfo shape")
	}
	var objects []struct {
		Name   string `json:"name"`
		Type   string `json:"object_type"`
		Schema string `json:"schema"`
	}
	decode(call("/api/schema/objects", map[string]any{"connection_id": c.ID, "database": c.Database, "schema": schema, "filter": table, "object_types": "TABLE"}), &objects)
	found = false
	for _, entry := range objects {
		if entry.Name == table && entry.Type == "TABLE" && entry.Schema == schema {
			found = true
		}
	}
	if !found {
		t.Fatal("objects route did not return the fixture's ObjectInfo shape")
	}
	var columns []struct {
		Name     string  `json:"name"`
		DataType string  `json:"data_type"`
		Nullable *bool   `json:"is_nullable"`
		Primary  *bool   `json:"is_primary_key"`
		Default  *string `json:"column_default"`
		Extra    *string `json:"extra"`
	}
	decode(call("/api/schema/columns", request), &columns)
	if len(columns) != 2 || columns[0].Name != "id" || columns[1].Name != "value" {
		t.Fatal("column names or order differ from ColumnInfo contract")
	}
	for i, column := range columns {
		if column.DataType == "" || column.Nullable == nil || column.Primary == nil {
			t.Fatal("ColumnInfo missing string data_type or boolean flags")
		}
		if *column.Primary != (i == 0) || *column.Nullable != (i == 1) {
			t.Fatal("ColumnInfo primary/nullable flags differ")
		}
	}
	var ddl string
	decode(call("/api/schema/ddl", request), &ddl)
	if !strings.Contains(strings.ToUpper(ddl), "CREATE TABLE") || !strings.Contains(ddl, table) || !strings.Contains(strings.ToUpper(ddl), "PRIMARY KEY") {
		t.Fatal("table DDL route did not return a complete string definition")
	}
	view := table + "_view"
	viewSQL := "CREATE VIEW " + quoteIdentifier(view, c.Type) + " AS SELECT id, value FROM " + quoteIdentifier(table, c.Type)
	if _, err := m.ExecuteSQL(ctx, c.ID, c.Database, viewSQL, 1); err != nil {
		t.Fatalf("create view fixture: %v", redact(err, c))
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := m.ExecuteSQL(cleanupCtx, c.ID, c.Database, "DROP VIEW IF EXISTS "+quoteIdentifier(view, c.Type), 1); err != nil {
			t.Errorf("view fixture cleanup: %v", redact(err, c))
		}
	}()
	var source struct {
		Name     string `json:"name"`
		Type     string `json:"object_type"`
		Schema   string `json:"schema"`
		Source   string `json:"source"`
		Editable *bool  `json:"editable"`
	}
	decode(call("/api/schema/object-source", map[string]any{"connection_id": c.ID, "database": c.Database, "schema": schema, "table": view, "object_type": "VIEW"}), &source)
	if source.Name != view || source.Type != "VIEW" || source.Schema != schema || source.Editable == nil || !*source.Editable || !strings.Contains(strings.ToUpper(source.Source), "VIEW") || !strings.Contains(source.Source, table) {
		t.Fatal("view source route did not return the ObjectSource contract")
	}
}
