package server

import (
	"encoding/json"
	"testing"
)

func TestMCPExecutionPolicyOverrideCompatibility(t *testing.T) {
	version := 1
	p := defaultMCPPolicy()
	p.GroupPolicies = []MCPGroupPolicy{{GroupID: "outer", ReadOnly: false, AllowDangerousSQL: true}, {GroupID: "inner", ReadOnly: true}}
	ro, danger := p.executionPolicy("c", "main", []string{"outer", "inner"})
	if !ro || danger {
		t.Fatal("group hierarchy ignored")
	}
	p.ConnectionPolicies = []MCPConnectionPolicy{{ConnectionID: "c", ReadOnly: false, AllowDangerousSQL: true, ExecutionModeConfigured: true, ExecutionModePolicyVersion: &version}}
	ro, danger = p.executionPolicy("c", "main", []string{"outer", "inner"})
	if ro || !danger {
		t.Fatal("explicit v1 connection override ignored")
	}
	p.ConnectionPolicies[0].ExecutionModePolicyVersion = nil
	ro, danger = p.executionPolicy("c", "main", []string{"outer", "inner"})
	if !ro || danger {
		t.Fatal("legacy connection widened group ceiling")
	}
	p.ConnectionPolicies[0].ExecutionModeConfigured = false
	ro, danger = p.executionPolicy("c", "main", []string{"outer"})
	if ro || !danger {
		t.Fatal("scope-only legacy connection changed inherited mode")
	}
	p.ConnectionPolicies[0].ExecutionModePolicyVersion = &version
	p.ConnectionPolicies[0].DatabasePolicies = []MCPDatabasePolicy{{DatabaseName: "main", ReadOnly: true}}
	ro, danger = p.executionPolicy("c", "main", []string{"outer"})
	if !ro || danger {
		t.Fatal("database override ignored")
	}
}
func TestMCPPolicyDurableEmptyAllowlistAndDefault(t *testing.T) {
	dir := t.TempDir()
	s := testServer(t, dir)
	var p MCPPolicy
	if err := json.Unmarshal([]byte(`{"allowedConnectionIds":[],"allowedToolNames":[]}`), &p); err != nil {
		t.Fatal(err)
	}
	if !p.ReadOnly || p.AllowedConnectionIDs == nil || p.AllowedToolNames == nil {
		t.Fatal("JSON defaults widened access")
	}
	if err := s.SaveMCPPolicy(p); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := testServer(t, dir)
	got, err := reopened.LoadMCPPolicy()
	if err != nil || !got.Configured || !got.ReadOnly || got.AllowedConnectionIDs == nil || got.AllowedToolNames == nil {
		t.Fatal("durable policy did not preserve empty allowlists", got, err)
	}
}
func TestMCPCorruptPolicyFailsClosed(t *testing.T) {
	s := testServer(t, t.TempDir())
	if err := s.update(func(st *diskState) error {
		st.Settings["mcp-policy"] = map[string]any{"configured": true, "readOnly": false, "allowedConnectionIds": nil, "unexpectedUnsafeFlag": true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p, err := s.LoadMCPPolicy()
	if err == nil || p.Configured || !p.ReadOnly {
		t.Fatal("malformed policy did not fail closed", p, err)
	}
}
