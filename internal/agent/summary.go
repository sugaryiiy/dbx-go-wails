package agent

import (
	"errors"
	"fmt"
)

func (s *Store) RuntimeSummary() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	runtimes := []map[string]any{}
	running := 0
	labels := Labels()
	for session, key := range s.active {
		status := "running"
		if session.Runtime.Failed() {
			status = "stopped"
		} else {
			running++
		}
		mode := "legacy"
		if session.Runtime.Handshake.Supports("multi_session") {
			mode = "v2_isolated"
		}
		runtimes = append(runtimes, map[string]any{"id": session.ID, "driver_key": key, "label": labels[key], "kind": "agent", "source": "go", "status": status, "pid": session.Runtime.cmd.Process.Pid, "memory_bytes": nil, "cpu_percent": nil, "uptime_seconds": nil, "version": s.state.Drivers[key].Version, "last_error": nil, "can_stop": status == "running", "can_restart": false, "control_unavailable_reason": "reconnect the database connection to restart", "protocol_mode": mode, "active_sessions": 1})
	}
	health := "healthy"
	if running < len(runtimes) {
		health = "degraded"
	}
	return map[string]any{"running_count": running, "total_memory_bytes": 0, "last_error": nil, "health": health, "runtimes": runtimes}
}
func (s *Store) StopRuntime(id string) error {
	s.mu.Lock()
	var target *Session
	for session := range s.active {
		if session.ID == id {
			target = session
			break
		}
	}
	s.mu.Unlock()
	if target == nil {
		return errors.New("agent runtime does not exist")
	}
	if err := target.Close(); err != nil {
		return fmt.Errorf("stop agent runtime: %w", err)
	}
	return nil
}
