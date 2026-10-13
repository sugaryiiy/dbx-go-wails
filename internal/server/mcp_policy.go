package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// MCPPolicy preserves the frontend policy contract. A nil allowlist means all;
// an empty, non-nil allowlist means none. Unconfigured policy always denies.
type MCPPolicy struct {
	ReadOnly             bool                  `json:"readOnly"`
	AllowDangerousSQL    bool                  `json:"allowDangerousSql"`
	AllowedConnectionIDs []string              `json:"allowedConnectionIds"`
	AllowedGroupIDs      []string              `json:"allowedGroupIds"`
	AllowedToolNames     []string              `json:"allowedToolNames"`
	ConnectionPolicies   []MCPConnectionPolicy `json:"connectionPolicies"`
	GroupPolicies        []MCPGroupPolicy      `json:"groupPolicies"`
	Configured           bool                  `json:"configured"`
	QueryTimeoutSecs     *int                  `json:"queryTimeoutSecs"`
}
type MCPConnectionPolicy struct {
	ConnectionID               string              `json:"connectionId"`
	ReadOnly                   bool                `json:"readOnly"`
	AllowDangerousSQL          bool                `json:"allowDangerousSql"`
	ExecutionModeConfigured    bool                `json:"executionModeConfigured"`
	ExecutionModePolicyVersion *int                `json:"executionModePolicyVersion"`
	DatabaseScope              string              `json:"databaseScope"`
	AllowedDatabases           []string            `json:"allowedDatabases"`
	DatabasePolicies           []MCPDatabasePolicy `json:"databasePolicies"`
	AllowSalesforceDML         bool                `json:"allowSalesforceDml"`
}
type MCPDatabasePolicy struct {
	DatabaseName      string `json:"databaseName"`
	ReadOnly          bool   `json:"readOnly"`
	AllowDangerousSQL bool   `json:"allowDangerousSql"`
}
type MCPGroupPolicy struct {
	GroupID           string `json:"groupId"`
	ReadOnly          bool   `json:"readOnly"`
	AllowDangerousSQL bool   `json:"allowDangerousSql"`
}

func defaultMCPPolicy() MCPPolicy {
	return MCPPolicy{ReadOnly: true, AllowedConnectionIDs: []string{}, AllowedGroupIDs: []string{}, ConnectionPolicies: []MCPConnectionPolicy{}, GroupPolicies: []MCPGroupPolicy{}}
}
func strictMCPJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(v)
}
func (p *MCPPolicy) UnmarshalJSON(b []byte) error {
	type plain MCPPolicy
	v := plain(defaultMCPPolicy())
	if err := strictMCPJSON(b, &v); err != nil {
		return err
	}
	*p = MCPPolicy(v)
	return nil
}
func (p *MCPConnectionPolicy) UnmarshalJSON(b []byte) error {
	type plain MCPConnectionPolicy
	v := plain{ReadOnly: true, ExecutionModeConfigured: true, DatabaseScope: "all", AllowedDatabases: []string{}, DatabasePolicies: []MCPDatabasePolicy{}}
	if err := strictMCPJSON(b, &v); err != nil {
		return err
	}
	*p = MCPConnectionPolicy(v)
	return nil
}
func (p *MCPDatabasePolicy) UnmarshalJSON(b []byte) error {
	type plain MCPDatabasePolicy
	v := plain{ReadOnly: true}
	if err := strictMCPJSON(b, &v); err != nil {
		return err
	}
	*p = MCPDatabasePolicy(v)
	return nil
}
func (p *MCPGroupPolicy) UnmarshalJSON(b []byte) error {
	type plain MCPGroupPolicy
	v := plain{ReadOnly: true}
	if err := strictMCPJSON(b, &v); err != nil {
		return err
	}
	*p = MCPGroupPolicy(v)
	return nil
}
func (s *Server) LoadMCPPolicy() (MCPPolicy, error) {
	raw, ok := s.snapshot().Settings["mcp-policy"]
	if !ok {
		return defaultMCPPolicy(), nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return defaultMCPPolicy(), err
	}
	var p MCPPolicy
	if err = json.Unmarshal(b, &p); err != nil {
		return defaultMCPPolicy(), errors.New("MCP policy is invalid; save it again in Settings")
	}
	if err = s.validateMCPPolicy(p); err != nil {
		return defaultMCPPolicy(), err
	}
	return p, nil
}
func (s *Server) SaveMCPPolicy(p MCPPolicy) error {
	p.Configured = true
	if err := s.validateMCPPolicy(p); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var v any
	if err = json.Unmarshal(b, &v); err != nil {
		return err
	}
	if err = s.update(func(st *diskState) error { st.Settings["mcp-policy"] = v; return nil }); err != nil {
		return err
	}
	s.revokeMCPRequests()
	return nil
}
func validMCPList(values []string) bool {
	seen := map[string]bool{}
	for _, v := range values {
		if strings.TrimSpace(v) == "" || v != strings.TrimSpace(v) || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}
func (s *Server) validateMCPPolicy(p MCPPolicy) error {
	if !validMCPList(p.AllowedConnectionIDs) || !validMCPList(p.AllowedGroupIDs) || !validMCPList(p.AllowedToolNames) {
		return errors.New("MCP allowlists require unique, non-empty values")
	}
	if p.QueryTimeoutSecs != nil && (*p.QueryTimeoutSecs < 1 || *p.QueryTimeoutSecs > 86400) {
		return errors.New("Go MCP query timeout must be 1–86400 seconds; unlimited queries are not supported")
	}
	known := map[string]bool{}
	for _, t := range mcpToolDefinitions() {
		known[t.Name] = true
	}
	for _, name := range p.AllowedToolNames {
		if !known[name] {
			return fmt.Errorf("MCP tool is not implemented in Go: %s", name)
		}
	}
	conns := map[string]map[string]any{}
	for _, c := range s.snapshot().Connections {
		conns[str(c, "id")] = c
	}
	seen := map[string]bool{}
	for _, r := range p.ConnectionPolicies {
		if strings.TrimSpace(r.ConnectionID) == "" || seen[r.ConnectionID] {
			return errors.New("MCP connection policies require unique connection IDs")
		}
		seen[r.ConnectionID] = true
		if r.AllowSalesforceDML {
			return errors.New("Salesforce DML has not been migrated to Go MCP")
		}
		if r.ExecutionModePolicyVersion != nil && *r.ExecutionModePolicyVersion != 1 {
			return errors.New("Unsupported MCP execution policy version")
		}
		if r.DatabaseScope != "all" && r.DatabaseScope != "none" && r.DatabaseScope != "selected" {
			return errors.New("MCP databaseScope must be all, selected or none")
		}
		if !validMCPList(r.AllowedDatabases) {
			return errors.New("MCP database allowlist requires unique, non-empty names")
		}
		if r.DatabaseScope != "selected" && (len(r.AllowedDatabases) > 0 || len(r.DatabasePolicies) > 0) {
			return errors.New("MCP database rules require databaseScope=selected")
		}
		if r.DatabaseScope == "selected" {
			if c := conns[r.ConnectionID]; c != nil && str(c, "db_type") != "sqlite" {
				return errors.New("Selected-database MCP policies currently require SQLite; cross-database SQL isolation for network databases is not implemented")
			}
		}
		dbs := map[string]bool{}
		for _, d := range r.DatabasePolicies {
			if !mcpContains(r.AllowedDatabases, d.DatabaseName) || dbs[d.DatabaseName] {
				return errors.New("MCP database policies must uniquely reference an allowed database")
			}
			dbs[d.DatabaseName] = true
		}
	}
	seen = map[string]bool{}
	for _, g := range p.GroupPolicies {
		if strings.TrimSpace(g.GroupID) == "" || seen[g.GroupID] {
			return errors.New("MCP group policies require unique group IDs")
		}
		seen[g.GroupID] = true
	}
	if len(p.AllowedGroupIDs) > 0 || len(p.GroupPolicies) > 0 {
		if _, err := mcpGroupPaths(s.snapshot().Sidebar); err != nil {
			return err
		}
	}
	return nil
}
func mcpContains(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

type mcpSidebarEntry struct {
	Type          string            `json:"type"`
	ID            string            `json:"id"`
	Children      []mcpSidebarEntry `json:"children"`
	ConnectionIDs []string          `json:"connectionIds"`
}

func mcpGroupPaths(raw any) (map[string][]string, error) {
	result := map[string][]string{}
	if raw == nil {
		return result, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var layout struct {
		Groups []struct {
			ID string `json:"id"`
		} `json:"groups"`
		Order []mcpSidebarEntry `json:"order"`
	}
	if json.Unmarshal(b, &layout) != nil {
		return nil, errors.New("Invalid sidebar layout for MCP group policy")
	}
	groups := map[string]bool{}
	for _, g := range layout.Groups {
		groups[g.ID] = true
	}
	var walk func([]mcpSidebarEntry, []string, int) error
	add := func(id string, path []string) error {
		if _, ok := result[id]; ok {
			return errors.New("Ambiguous duplicate connection in MCP group layout")
		}
		result[id] = append([]string{}, path...)
		return nil
	}
	walk = func(entries []mcpSidebarEntry, path []string, depth int) error {
		if depth > 64 {
			return errors.New("MCP group tree is too deep")
		}
		for _, e := range entries {
			switch e.Type {
			case "connection":
				if err := add(e.ID, path); err != nil {
					return err
				}
			case "group":
				if !groups[e.ID] || mcpContains(path, e.ID) {
					return errors.New("Invalid MCP group tree")
				}
				next := append(append([]string{}, path...), e.ID)
				if e.Children != nil {
					if err := walk(e.Children, next, depth+1); err != nil {
						return err
					}
				} else {
					for _, id := range e.ConnectionIDs {
						if err := add(id, next); err != nil {
							return err
						}
					}
				}
			default:
				return errors.New("Unknown sidebar entry in MCP group policy")
			}
		}
		return nil
	}
	return result, walk(layout.Order, nil, 0)
}
func (p MCPPolicy) allowsTool(name string) bool {
	return p.Configured && (p.AllowedToolNames == nil || mcpContains(p.AllowedToolNames, name))
}
func (p MCPPolicy) allowsConnection(id string, groups []string) bool {
	if !p.Configured {
		return false
	}
	if p.AllowedConnectionIDs == nil || mcpContains(p.AllowedConnectionIDs, id) {
		return true
	}
	for _, g := range groups {
		if mcpContains(p.AllowedGroupIDs, g) {
			return true
		}
	}
	return false
}
func (p MCPPolicy) connectionRule(id string) *MCPConnectionPolicy {
	for i := range p.ConnectionPolicies {
		if p.ConnectionPolicies[i].ConnectionID == id {
			return &p.ConnectionPolicies[i]
		}
	}
	return nil
}
func (p MCPPolicy) allowsDatabase(id, name string) bool {
	r := p.connectionRule(id)
	return r == nil || r.DatabaseScope == "all" || r.DatabaseScope == "selected" && mcpContains(r.AllowedDatabases, name)
}
func (p MCPPolicy) executionPolicy(id, db string, groups []string) (bool, bool) {
	ro, danger := p.ReadOnly, p.AllowDangerousSQL && !p.ReadOnly
	for _, g := range groups {
		for _, r := range p.GroupPolicies {
			if r.GroupID == g {
				ro, danger = r.ReadOnly, !r.ReadOnly && r.AllowDangerousSQL
			}
		}
	}
	if r := p.connectionRule(id); r != nil {
		apply := func(readOnly, allow bool) {
			if r.ExecutionModePolicyVersion != nil && *r.ExecutionModePolicyVersion == 1 {
				ro, danger = readOnly, !readOnly && allow
			} else {
				ro = ro || readOnly
				danger = !ro && danger && allow
			}
		}
		if r.ExecutionModeConfigured {
			apply(r.ReadOnly, r.AllowDangerousSQL)
		}
		for _, d := range r.DatabasePolicies {
			if d.DatabaseName == db {
				apply(d.ReadOnly, d.AllowDangerousSQL)
			}
		}
	}
	return ro, !ro && danger
}
