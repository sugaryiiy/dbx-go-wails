package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type aiJob struct {
	events  []map[string]any
	done    bool
	bytes   int
	updated time.Time
}

func (a *aiRuntime) appendJob(id string, event map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	job := a.jobs[id]
	if job == nil {
		return errors.New("AI job does not exist")
	}
	b, _ := json.Marshal(event)
	if job.bytes+len(b) > maxAIResponseBytes || len(job.events) >= 20000 {
		return errors.New("AI event buffer exceeds size limit")
	}
	copy := cloneMap(event)
	copy["seq"] = len(job.events) + 1
	job.events = append(job.events, copy)
	job.bytes += len(b)
	job.updated = time.Now()
	return nil
}
func (s *Server) handleAIJobs(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	if r.URL.Path == "/api/ai/stream-events" {
		if r.Method != "GET" {
			writeError(w, 405, "Method not allowed")
			return true
		}
		id := aiSessionID(p)
		after := integer(p["after_seq"], 0)
		s.aiRuntime.mu.Lock()
		job := s.aiRuntime.jobs[id]
		if job == nil {
			s.aiRuntime.mu.Unlock()
			writeError(w, 404, "AI stream job does not exist")
			return true
		}
		events := []map[string]any{}
		for i, event := range job.events {
			if i+1 > after {
				events = append(events, cloneMap(event))
				if len(events) >= 256 {
					break
				}
			}
		}
		result := map[string]any{"events": events, "done": job.done, "lastSeq": len(job.events)}
		s.aiRuntime.mu.Unlock()
		writeJSON(w, 200, result)
		return true
	}
	if (r.URL.Path != "/api/ai/stream" && r.URL.Path != "/api/ai/agent-stream") || p["background"] != true {
		return false
	}
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return true
	}
	if !s.desktop {
		writeError(w, 403, "Background AI streams are available only in Wails desktop")
		return true
	}
	id := aiSessionID(p)
	request := cloneMap(appObject(p["request"]))
	if _, err := aiBody(request, false); err != nil {
		writeError(w, 400, err.Error())
		return true
	}
	if _, err := aiEndpoint(appObject(request["config"]), false, false); err != nil {
		writeError(w, 400, err.Error())
		return true
	}
	agent := r.URL.Path == "/api/ai/agent-stream"
	if agent && (str(p, "connectionId") == "" || str(p, "database") == "") {
		writeError(w, 400, "Agent requires a connection and database")
		return true
	}
	ctx, finish, err := s.aiRuntime.register(context.Background(), id)
	if err != nil {
		writeError(w, 400, err.Error())
		return true
	}
	s.aiRuntime.mu.Lock()
	for key, job := range s.aiRuntime.jobs {
		if job.done && time.Since(job.updated) > 10*time.Minute {
			delete(s.aiRuntime.jobs, key)
		}
	}
	s.aiRuntime.jobs[id] = &aiJob{events: []map[string]any{}, updated: time.Now()}
	s.aiRuntime.mu.Unlock()
	payload := cloneMap(p)
	go func() {
		defer finish()
		var err error
		if agent {
			err = s.aiAgentLoop(ctx, request, payload, func(v any) error { return s.aiRuntime.appendJob(id, appObject(v)) })
		} else {
			err = s.aiStream(ctx, request, func(delta, reason string) error {
				event := map[string]any{"session_id": id, "delta": delta, "done": false}
				if reason != "" {
					event["reasoning_delta"] = reason
				}
				return s.aiRuntime.appendJob(id, event)
			})
		}
		if err != nil {
			message := aiSafeError(appObject(request["config"]), err.Error())
			if agent {
				_ = s.aiRuntime.appendJob(id, map[string]any{"type": "error", "message": message})
			} else {
				_ = s.aiRuntime.appendJob(id, map[string]any{"session_id": id, "delta": "", "done": true, "error": message})
			}
		} else if !agent {
			_ = s.aiRuntime.appendJob(id, map[string]any{"session_id": id, "delta": "", "done": true})
		}
		s.aiRuntime.mu.Lock()
		job := s.aiRuntime.jobs[id]
		job.done = true
		job.updated = time.Now()
		s.aiRuntime.mu.Unlock()
	}()
	writeJSON(w, 200, map[string]any{"sessionId": id, "started": true})
	return true
}
