package server

import (
	"context"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPRedactsNestedTransportAndTLSMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	fields := map[string]string{
		"host": "bastion.example.private", "user": "ssh-user-private", "username": "nested-db-user",
		"key_path": "~/.ssh/private-key", "known_hosts_path": "~/private/known_hosts", "ssh_agent_sock_path": "~/private/agent.sock",
		"ca_cert_path": "~/private/ca.pem", "client_cert_path": "~/private/client.pem", "client_key_path": "~/private/client-key.pem",
		"password": "transport-password/private", "key_passphrase": "key-passphrase/private",
	}
	layer := map[string]any{"type": "ssh"}
	values := []string{}
	for key, value := range fields {
		layer[key] = value
		values = append(values, value)
		if strings.HasPrefix(value, "~/") {
			values = append(values, filepath.Join(home, value[2:]))
		}
	}
	config := map[string]any{"host": "database.example.private", "username": "database-user-private", "transport_layers": []any{layer}}
	values = append(values, config["host"].(string), config["username"].(string))
	var s Server
	for _, value := range values {
		for _, variant := range []string{value, url.QueryEscape(value), url.PathEscape(value)} {
			if got := s.mcpRedact("connection failed: "+variant, config); got != "connection failed: [REDACTED]" {
				t.Fatalf("metadata was not fully redacted: %q", got)
			}
		}
	}
}

func TestMCPRedactsImplicitSSHPathsAndOverlappingSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	socket := filepath.Join(home, "agent.socket")
	t.Setenv("SSH_AUTH_SOCK", socket)
	config := map[string]any{
		"password": "common", "connection_string": "postgres://name:common@private/database",
		"transport_layers": []any{map[string]any{"type": "ssh", "auth_method": "agent", "password": "common/long-secret", "host": "[::1]"}},
	}
	var s Server
	for _, input := range []string{filepath.Join(home, ".ssh", "known_hosts"), socket, "common/long-secret", "postgres://name:common@private/database", "[::1]", "::1"} {
		for _, variant := range []string{input, url.QueryEscape(input), url.PathEscape(input)} {
			if got := s.mcpRedact(variant, config); got != "[REDACTED]" {
				t.Fatalf("implicit metadata or overlapping secret was not fully redacted: %q", got)
			}
		}
	}
}

func TestMCPSSHFailureRedactsExpandedPathAndAudit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	s := testServer(t, t.TempDir())
	config := map[string]any{
		"id": "allowed", "db_type": "postgres", "host": "database.example.private", "database": "test", "save_password": true,
		"transport_layers": []any{map[string]any{"type": "ssh", "host": "bastion.example.private", "port": 22, "user": "private-user", "auth_method": "key", "key_path": "~/missing-private-key"}},
	}
	if err := s.update(func(st *diskState) error { st.Connections = []map[string]any{config}; return nil }); err != nil {
		t.Fatal(err)
	}
	mcpAllow(t, s)
	// The missing temporary private-key file fails before any network dial.
	_, err := s.MCPBackend().Call(context.Background(), "dbx_execute_query", mcpArgs("SELECT 1"))
	if err == nil || !strings.Contains(err.Error(), "private key") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("expected a redacted local SSH configuration error: %v", err)
	}
	if strings.Contains(err.Error(), home) || strings.Contains(err.Error(), "missing-private-key") {
		t.Fatal("MCP error exposed a configured or expanded local path")
	}
	history := s.snapshot().History
	if len(history) != 1 || str(history[0], "error") != err.Error() {
		t.Fatal("audit did not retain the sanitized error")
	}
}
