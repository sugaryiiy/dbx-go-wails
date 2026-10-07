package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tunnelDraft() map[string]any {
	return map[string]any{"id": "bastion", "name": "Shared bastion", "type": "ssh", "host": "ssh.example.test", "port": 22, "user": "tester", "auth_method": "password", "password": "tunnel/password?secret", "key_passphrase": "key-secret-value"}
}

func TestTunnelProfilesValidationAndEncryptedPersistence(t *testing.T) {
	dir := t.TempDir()
	s := testServer(t, dir)
	cookie := loginTest(t, s)
	profile := tunnelDraft()
	payload := map[string]any{"profiles": []any{profile}}
	if w := call(s, "POST", "/api/tunnel-profiles/save", payload, nil, ""); w.Code != 401 {
		t.Fatal("unauthenticated profile save allowed")
	}
	value(t, call(s, "POST", "/api/tunnel-profiles/save", payload, cookie, ""))
	for _, mutate := range []func(map[string]any){
		func(p map[string]any) { p["id"] = "" },
		func(p map[string]any) { p["profile_id"] = "another" },
		func(p map[string]any) { p["proxy_command"] = "nc %h %p" },
		func(p map[string]any) { p["type"] = "proxy" },
		func(p map[string]any) { p["host"] = ""; p["enabled"] = false },
		func(p map[string]any) { p["host_key_fingerprint"] = "invalid" },
	} {
		bad := cloneMap(profile)
		mutate(bad)
		w := call(s, "POST", "/api/tunnel-profiles/save", map[string]any{"profiles": []any{bad}}, cookie, "")
		if w.Code != 400 || strings.Contains(w.Body.String(), str(profile, "password")) {
			t.Fatalf("invalid profile response: %d %s", w.Code, w.Body)
		}
	}
	if w := call(s, "POST", "/api/tunnel-profiles/save", map[string]any{"profiles": []any{profile, profile}}, cookie, ""); w.Code != 400 {
		t.Fatal("duplicate profile ids allowed")
	}
	data, err := os.ReadFile(filepath.Join(dir, "state.enc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{str(profile, "password"), str(profile, "key_passphrase"), str(profile, "host")} {
		if strings.Contains(string(data), secret) {
			t.Fatal("profile stored in plaintext")
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = testServer(t, dir)
	cookie = loginTest(t, s)
	profiles := value(t, call(s, "GET", "/api/tunnel-profiles/list", nil, cookie, "")).([]any)
	if len(profiles) != 1 || profiles[0].(map[string]any)["password"] != profile["password"] {
		t.Fatal("profile persistence or validation atomicity failed")
	}
}

func TestTunnelResolutionAndMCPUseLatestProfile(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	profile := tunnelDraft()
	config := map[string]any{"id": "allowed", "db_type": "postgres", "database": "dbx", "save_password": true,
		"transport_layers": []any{map[string]any{"id": "hop-one", "enabled": true, "profile_id": "bastion", "host": "stale-host", "password": "stale-password"}}}
	value(t, call(s, "POST", "/api/connection/save", map[string]any{"configs": []any{config}}, cookie, ""))
	mcpAllow(t, s)
	p, err := s.LoadMCPPolicy()
	if err != nil {
		t.Fatal(err)
	}
	for _, password := range []string{"first-password", "updated-password"} {
		profile["password"] = password
		value(t, call(s, "POST", "/api/tunnel-profiles/save", map[string]any{"profiles": []any{profile}}, cookie, ""))
		request := map[string]any{"config": config}
		if err := s.prepareTransportPayload("/api/connection/connect", request); err != nil {
			t.Fatal(err)
		}
		scope, err := s.mcpResolve(p, map[string]any{"connection_id": "allowed"}, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, resolved := range []map[string]any{request["config"].(map[string]any), scope.config} {
			layer := resolved["transport_layers"].([]any)[0].(map[string]any)
			if layer["id"] != "hop-one" || layer["enabled"] != true || layer["profile_id"] != nil || layer["host"] != profile["host"] || layer["password"] != password {
				t.Fatal("shared profile was not fully resolved with latest credentials")
			}
		}
	}
	if config["transport_layers"].([]any)[0].(map[string]any)["profile_id"] != "bastion" {
		t.Fatal("profile resolution mutated caller config")
	}
	for _, enabled := range []any{"false", 0, "true"} {
		invalid := cloneMap(config)
		invalid["transport_layers"].([]any)[0].(map[string]any)["enabled"] = enabled
		if _, err := s.resolveConnectionTransports(invalid); err == nil {
			t.Fatal("malformed enabled value silently activated a shared tunnel")
		}
	}
	value(t, call(s, "POST", "/api/tunnel-profiles/save", map[string]any{"profiles": []any{}}, cookie, ""))
	if _, err := s.resolveConnectionTransports(config); err == nil {
		t.Fatal("missing shared profile allowed")
	}
	if _, err := s.mcpResolve(p, map[string]any{"connection_id": "allowed"}, true); err == nil {
		t.Fatal("MCP used a deleted shared profile")
	}
	config["transport_layers"].([]any)[0].(map[string]any)["enabled"] = false
	if _, err := s.resolveConnectionTransports(config); err != nil {
		t.Fatal("disabled missing profile blocked connection", err)
	}
	config["transport_layers"] = "invalid"
	if _, err := s.resolveConnectionTransports(config); err == nil {
		t.Fatal("malformed transport array accepted")
	}
}

func TestTunnelConnectionCredentialPolicyAndRedaction(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	profile := tunnelDraft()
	config := map[string]any{"id": "transient", "db_type": "postgres", "password": "database-secret", "save_password": false, "transport_layers": []any{profile}}
	value(t, call(s, "POST", "/api/connection/save", map[string]any{"configs": []any{config}}, cookie, ""))
	saved := value(t, call(s, "GET", "/api/connection/list", nil, cookie, "")).([]any)[0].(map[string]any)
	layer := saved["transport_layers"].([]any)[0].(map[string]any)
	if saved["password"] != "" || layer["password"] != "" || layer["key_passphrase"] != "" || layer["host"] != profile["host"] {
		t.Fatal("transient SSH credentials were retained or nonsecret fields lost")
	}
	message := "failure: " + str(profile, "password") + " " + url.QueryEscape(str(profile, "password")) + " " + str(profile, "key_passphrase")
	redacted := s.redactError(message, config)
	if strings.Contains(redacted, str(profile, "password")) || strings.Contains(redacted, str(profile, "key_passphrase")) || strings.Contains(redacted, url.QueryEscape(str(profile, "password"))) {
		t.Fatal("transport credentials exposed in error")
	}
}

// Opt in with a private JSON array of disposable database configurations.
func TestMCPLiveTLSConnections(t *testing.T) {
	path := os.Getenv("DBX_TEST_DATABASE_CONFIGS")
	if path == "" {
		t.Skip("set DBX_TEST_DATABASE_CONFIGS for disposable live databases")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read live configuration file")
	}
	var configs []map[string]any
	if json.Unmarshal(data, &configs) != nil || len(configs) == 0 {
		t.Fatal("invalid live configuration file")
	}
	for _, config := range configs {
		t.Run(str(config, "db_type"), func(t *testing.T) {
			s := testServer(t, t.TempDir())
			cookie := loginTest(t, s)
			config = cloneMap(config)
			config["id"] = "allowed"
			config["save_password"] = true
			value(t, call(s, "POST", "/api/connection/save", map[string]any{"configs": []any{config}}, cookie, ""))
			mcpAllow(t, s)
			res := mcpCall(t, s, "dbx_execute_query", mcpArgs("SELECT 42 AS answer")).(map[string]any)
			if fmt.Sprint(res["rows"]) != "[[42]]" {
				t.Fatal("unexpected live query result")
			}
			if _, err := s.MCPBackend().Call(context.Background(), "dbx_execute_query", mcpArgs("CREATE TABLE mcp_should_reject(id INT)")); err == nil {
				t.Fatal("MCP readonly policy allowed network write")
			}
			config["save_password"] = false
			value(t, call(s, "POST", "/api/connection/save", map[string]any{"configs": []any{config}}, cookie, ""))
			if _, err := s.MCPBackend().Call(context.Background(), "dbx_execute_query", mcpArgs("SELECT 42")); err == nil || !strings.Contains(err.Error(), "saved credentials") {
				t.Fatal("MCP reconnected without saved credentials")
			}
		})
	}
}
