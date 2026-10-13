package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sugaryiiy/dbx-go-wails/internal/agent"
)

type agentOperation struct {
	cancel context.CancelFunc
	key    string
}
type agentHTTPState struct {
	mu          sync.Mutex
	operations  map[string]agentOperation
	subscribers map[chan agent.Progress]struct{}
	closed      bool
}

func newAgentHTTPState() *agentHTTPState {
	return &agentHTTPState{operations: map[string]agentOperation{}, subscribers: map[chan agent.Progress]struct{}{}}
}
func (s *Server) agentHTTP() *agentHTTPState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agents == nil {
		s.agents = newAgentHTTPState()
	}
	return s.agents
}
func (s *Server) closeAgentHTTP() {
	state := s.agentHTTP()
	state.mu.Lock()
	defer state.mu.Unlock()
	state.closed = true
	for _, operation := range state.operations {
		operation.cancel()
	}
	for subscriber := range state.subscribers {
		close(subscriber)
		delete(state.subscribers, subscriber)
	}
}
func (state *agentHTTPState) begin(parent context.Context, id, key string) (context.Context, func(), error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil, nil, errors.New("agent manager is closed")
	}
	if _, exists := state.operations[id]; exists {
		return nil, nil, errors.New("agent operation id is already running")
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	state.operations[id] = agentOperation{cancel: cancel, key: key}
	return ctx, func() { cancel(); state.mu.Lock(); delete(state.operations, id); state.mu.Unlock() }, nil
}
func (state *agentHTTPState) cancel(id, key string) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	cancelled := false
	for operationID, operation := range state.operations {
		if id != "" && operationID != id {
			continue
		}
		if key != "" && operation.key != key {
			continue
		}
		operation.cancel()
		cancelled = true
	}
	return cancelled
}
func (state *agentHTTPState) publish(progress agent.Progress) {
	state.mu.Lock()
	defer state.mu.Unlock()
	for subscriber := range state.subscribers {
		select {
		case subscriber <- progress:
		default:
		}
	}
}
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/agents/") {
		return false
	}
	if s.agentStore == nil {
		writeError(w, 503, "Agent store is unavailable")
		return true
	}
	route := strings.TrimSuffix(r.URL.Path, "/")
	get := route == "/api/agents/installed-local" || route == "/api/agents/installed" || route == "/api/agents/storage-usage" || route == "/api/agents/runtime" || route == "/api/agents/java-runtime" || strings.HasPrefix(route, "/api/agents/installed/")
	method := http.MethodPost
	if get {
		method = http.MethodGet
	}
	if route == "/api/agents/download-cache" {
		method = http.MethodDelete
	}
	if route == "/api/agents/java-runtime" && r.Method == http.MethodPost {
		method = http.MethodPost
	}
	if r.Method != method {
		writeError(w, 405, "Method not allowed")
		return true
	}
	var result any
	var err error
	store := s.agentStore
	state := s.agentHTTP()
	switch route {
	case "/api/agents/installed-local":
		result = store.List(nil)
	case "/api/agents/installed":
		var registry *agent.Registry
		registry, err = store.FetchRegistry(r.Context())
		if err != nil {
			registry = nil
			err = nil
		}
		result = store.List(registry)
	case "/api/agents/storage-usage":
		result = store.Usage()
	case "/api/agents/download-cache":
		err = store.ClearCache()
		result = map[string]any{"ok": err == nil}
	case "/api/agents/invalidate-registry-cache":
		store.InvalidateRegistry()
		result = map[string]any{"ok": true}
	case "/api/agents/java-runtime":
		if r.Method == http.MethodGet {
			result = store.JavaConfig()
		} else {
			data, _ := json.Marshal(p["config"])
			var config agent.JavaConfig
			if err = json.Unmarshal(data, &config); err == nil {
				result, err = store.SetJavaConfig(r.Context(), config)
			}
		}
	case "/api/agents/cancel-install":
		result = map[string]any{"ok": true, "cancelled": state.cancel(str(p, "operationId"), str(p, "dbType"))}
	case "/api/agents/cancel-upgrade-all":
		result = map[string]any{"ok": true, "cancelled": state.cancel(str(p, "operationId"), "batch")}
	case "/api/agents/update-blockers":
		candidates := []string{}
		data, _ := json.Marshal(p["dbTypes"])
		err = json.Unmarshal(data, &candidates)
		blockers := []map[string]any{}
		if err == nil {
			keys := store.ActiveKeys()
			labels := agent.Labels()
			for _, key := range keys {
				for _, candidate := range candidates {
					if candidate == key {
						blockers = append(blockers, map[string]any{"db_type": key, "label": labels[key], "connections": []string{key}})
						break
					}
				}
			}
		}
		result = blockers
	case "/api/agents/uninstall":
		err = store.Uninstall(str(p, "dbType"))
		result = map[string]any{"ok": err == nil}
	case "/api/agents/uninstall-jre":
		err = store.UninstallJRE(str(p, "jreKey"))
		result = map[string]any{"ok": err == nil}
	case "/api/agents/install", "/api/agents/reinstall-jre", "/api/agents/upgrade-all":
		id := str(p, "operationId")
		if id == "" {
			id = fmt.Sprintf("go-install-%d", time.Now().UnixNano())
		}
		key := str(p, "dbType")
		if route == "/api/agents/upgrade-all" {
			key = "batch"
		}
		var ctx context.Context
		var finish func()
		ctx, finish, err = state.begin(r.Context(), id, key)
		if err == nil {
			defer finish()
			progress := func(event agent.Progress) { event.OperationID = id; state.publish(event) }
			if route == "/api/agents/install" {
				err = store.Install(ctx, key, progress)
			} else if route == "/api/agents/reinstall-jre" {
				err = store.ReinstallJRE(ctx, str(p, "jreKey"), progress)
			} else {
				var registry *agent.Registry
				registry, err = store.FetchRegistry(ctx)
				if err == nil {
					upgraded := 0
					cancelled := 0
					failed := []map[string]any{}
					for _, driver := range store.List(registry) {
						if driver["update_available"] != true {
							continue
						}
						driverKey, _ := driver["db_type"].(string)
						installErr := store.Install(ctx, driverKey, progress)
						if installErr != nil {
							if ctx.Err() != nil {
								cancelled++
							} else {
								failed = append(failed, map[string]any{"db_type": driverKey, "error": installErr.Error()})
							}
						} else {
							upgraded++
						}
					}
					result = map[string]any{"upgraded": upgraded, "cancelled": cancelled, "failed": failed}
				}
			}
			if err == nil {
				progress(agent.Progress{Step: "done", DBType: key})
			} else if ctx.Err() != nil {
				err = errors.New("Agent download canceled by user.")
			}
		}
		if result == nil {
			result = map[string]any{"ok": err == nil}
		}
	case "/api/agents/runtime":
		result = store.RuntimeSummary()
	case "/api/agents/runtime/stop":
		err = store.StopRuntime(str(p, "runtimeId"))
		result = map[string]any{"ok": err == nil}
	case "/api/agents/runtime/restart":
		err = errors.New("reconnect the database connection to restart its isolated agent runtime")
	default:
		if strings.HasPrefix(route, "/api/agents/installed/") {
			result = store.IsInstalled(strings.TrimPrefix(route, "/api/agents/installed/"))
		} else {
			writeError(w, 501, "This agent capability has not yet been migrated to Go")
			return true
		}
	}
	if err != nil {
		writeError(w, 400, err.Error())
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
func (s *Server) handleAgentTransport(w http.ResponseWriter, r *http.Request) bool {
	route := strings.TrimSuffix(r.URL.Path, "/")
	if strings.HasPrefix(route, "/api/agents/progress/") {
		if r.Method != http.MethodGet {
			writeError(w, 405, "Method not allowed")
			return true
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, 500, "Streaming unavailable")
			return true
		}
		state := s.agentHTTP()
		subscriber := make(chan agent.Progress, 64)
		state.mu.Lock()
		if state.closed {
			state.mu.Unlock()
			writeError(w, 503, "Agent manager closed")
			return true
		}
		state.subscribers[subscriber] = struct{}{}
		state.mu.Unlock()
		defer func() { state.mu.Lock(); delete(state.subscribers, subscriber); state.mu.Unlock() }()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = io.WriteString(w, ": connected\n\n")
		flusher.Flush()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case event, ok := <-subscriber:
				if !ok {
					return true
				}
				data, _ := json.Marshal(event)
				if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
					return true
				}
				flusher.Flush()
			case <-ticker.C:
				if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
					return true
				}
				flusher.Flush()
			case <-r.Context().Done():
				return true
			}
		}
	}
	if route != "/api/agents/import-driver" && route != "/api/agents/import-offline" {
		return false
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "Method not allowed")
		return true
	}
	if s.agentStore == nil {
		writeError(w, 503, "Agent store unavailable")
		return true
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<30)
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, 400, "Expected multipart driver upload")
		return true
	}
	file, err := os.CreateTemp(s.dataDir, ".agent-upload-*")
	if err != nil {
		writeError(w, 500, err.Error())
		return true
	}
	defer os.Remove(file.Name())
	defer file.Close()
	key := ""
	name := ""
	operationID := ""
	uploaded := false
	for {
		part, partErr := reader.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil {
			err = partErr
			break
		}
		switch part.FormName() {
		case "dbType", "operationId":
			data, e := io.ReadAll(io.LimitReader(part, 4097))
			if e != nil || len(data) > 4096 {
				err = errors.New("invalid upload metadata")
			} else if part.FormName() == "dbType" {
				key = string(data)
			} else {
				operationID = string(data)
			}
		case "file":
			if uploaded {
				err = errors.New("only one uploaded file is allowed")
			} else {
				name = part.FileName()
				_, err = io.Copy(file, part)
				uploaded = true
			}
		}
		part.Close()
		if err != nil {
			break
		}
	}
	if err == nil && !uploaded {
		err = errors.New("no driver file uploaded")
	}
	if err == nil {
		err = file.Close()
	}
	var result any
	if err == nil {
		if route == "/api/agents/import-driver" {
			err = s.agentStore.ImportDriver(r.Context(), key, file.Name(), name)
			result = map[string]any{"success": err == nil}
		} else {
			result, err = s.agentStore.ImportPackage(r.Context(), file.Name())
		}
	}
	if err != nil {
		writeError(w, 400, err.Error())
	} else {
		s.agentHTTP().publish(agent.Progress{Step: "done", DBType: key, OperationID: operationID})
		writeJSON(w, 200, result)
	}
	return true
}
