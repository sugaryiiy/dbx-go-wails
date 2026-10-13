package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/sugaryiiy/dbx-go-wails/internal/database"
)

func (s *Server) tunnelProfiles() ([]map[string]any, error) {
	raw, ok := s.snapshot().Settings["tunnel-profiles"]
	if !ok {
		return []map[string]any{}, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, errors.New("Invalid saved tunnel profiles")
	}
	var profiles []map[string]any
	if json.Unmarshal(data, &profiles) != nil {
		return nil, errors.New("Invalid saved tunnel profiles")
	}
	if profiles == nil {
		profiles = []map[string]any{}
	}
	return profiles, nil
}

// Resolve from the durable profile store for both desktop and MCP callers.
// A reference replaces all fields except its per-connection id/enabled state.
func (s *Server) resolveConnectionTransports(config map[string]any) (map[string]any, error) {
	config = cloneMap(config)
	raw, ok := config["transport_layers"]
	if !ok || raw == nil {
		return config, nil
	}
	layers, ok := raw.([]any)
	if !ok {
		return nil, errors.New("transport_layers must be an array")
	}
	profiles, err := s.tunnelProfiles()
	if err != nil {
		return nil, err
	}
	byID := map[string]map[string]any{}
	for _, p := range profiles {
		byID[str(p, "id")] = p
	}
	result := make([]any, 0, len(layers))
	for _, rawLayer := range layers {
		layer, ok := rawLayer.(map[string]any)
		if !ok {
			return nil, errors.New("Invalid transport layer")
		}
		if enabled, exists := layer["enabled"]; exists && enabled != nil {
			if _, ok := enabled.(bool); !ok {
				return nil, errors.New("Transport enabled must be a boolean")
			}
		}
		ref := str(layer, "profile_id")
		if ref != "" && layer["enabled"] != false {
			profile, found := byID[ref]
			if !found {
				return nil, errors.New("Referenced tunnel profile does not exist")
			}
			next := cloneMap(profile)
			next["id"] = layer["id"]
			next["enabled"] = layer["enabled"] != false
			delete(next, "profile_id")
			layer = next
		}
		result = append(result, layer)
	}
	config["transport_layers"] = result
	return config, nil
}

func (s *Server) prepareTransportPayload(route string, payload map[string]any) error {
	switch route {
	case "/api/connection/connect", "/api/connection/test", "/api/connection/test-info", "/api/connection/test-ssh-tunnel":
		config, ok := payload["config"].(map[string]any)
		if !ok {
			return errors.New("config is required")
		}
		resolved, err := s.resolveConnectionTransports(config)
		if err != nil {
			return err
		}
		payload["config"] = resolved
	}
	return nil
}

func (s *Server) handleTunnels(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	route := r.URL.Path
	if !strings.HasPrefix(route, "/api/tunnel-profiles/") && route != "/api/connection/test-ssh-tunnel" && route != "/api/ssh/config-hosts" && route != "/api/ssh/prompts/pending" {
		return false
	}
	var result any
	var err error
	switch {
	case route == "/api/tunnel-profiles/list" && r.Method == "GET":
		result, err = s.tunnelProfiles()
	case route == "/api/tunnel-profiles/save" && r.Method == "POST":
		profiles, ok := p["profiles"].([]any)
		if !ok || len(profiles) > 128 {
			err = errors.New("profiles must be an array with at most 128 entries")
			break
		}
		ids := map[string]bool{}
		for _, raw := range profiles {
			profile, ok := raw.(map[string]any)
			if !ok {
				err = errors.New("Invalid tunnel profile")
				break
			}
			id := str(profile, "id")
			if id == "" || ids[id] || str(profile, "profile_id") != "" {
				err = errors.New("Tunnel profiles require unique ids and cannot reference other profiles")
				break
			}
			ids[id] = true
			// Validate the draft even when currently disabled. No network or key reads.
			check := cloneMap(profile)
			check["enabled"] = true
			if err = database.ValidateNetworkTransports(database.Config{Type: "postgres", Transports: []map[string]any{check}}); err != nil {
				break
			}
		}
		if err == nil {
			err = s.update(func(st *diskState) error { st.Settings["tunnel-profiles"] = profiles; return nil })
			if err == nil {
				s.revokeMCPRequests()
			}
		}
	case route == "/api/tunnel-profiles/test" && r.Method == "POST":
		result, err = database.TestSSHTunnel(r.Context(), p)
	case route == "/api/connection/test-ssh-tunnel" && r.Method == "POST":
		if err = s.prepareTransportPayload(route, p); err != nil {
			break
		}
		var config database.Config
		data, e := json.Marshal(p["config"])
		if e != nil {
			err = e
			break
		}
		if err = json.Unmarshal(data, &config); err != nil {
			break
		}
		result, err = database.TestSSHTransports(r.Context(), config)
	case (route == "/api/ssh/config-hosts" || route == "/api/ssh/prompts/pending") && r.Method == "GET":
		// Go uses explicit SSH fields and known_hosts/pins. No interactive prompt
		// or implicit OpenSSH ProxyCommand execution is available.
		result = []any{}
	default:
		writeError(w, 405, "Method not allowed")
		return true
	}
	if err != nil {
		writeError(w, 400, s.redactError(err.Error(), p))
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
