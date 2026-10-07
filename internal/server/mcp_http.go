package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sugaryiiy/dbx-go-wails/internal/mcpbridge"
)

type MCPHTTPSettings struct {
	Enabled        bool     `json:"enabled"`
	Host           string   `json:"host"`
	Port           int      `json:"port"`
	Path           string   `json:"path"`
	AllowRemote    bool     `json:"allowRemote"`
	AllowedHosts   []string `json:"allowedHosts"`
	AllowedOrigins []string `json:"allowedOrigins"`
}
type MCPWebSettings struct {
	Enabled        bool     `json:"enabled"`
	AllowedHosts   []string `json:"allowedHosts"`
	AllowedOrigins []string `json:"allowedOrigins"`
}
type mcpRuntime struct {
	mu              sync.Mutex
	requests        sync.WaitGroup
	settings        MCPHTTPSettings
	web             MCPWebSettings
	token, webToken string
	handler         http.Handler
	httpServer      *http.Server
	lastError       string
	logs            []string
	closed          bool
	authCtx         context.Context
	authCancel      context.CancelFunc
}

func defaultMCPHTTPSettings() MCPHTTPSettings {
	return MCPHTTPSettings{Host: "127.0.0.1", Port: 5225, Path: "/mcp", AllowedHosts: []string{}, AllowedOrigins: []string{}}
}
func readMCPSetting(settings map[string]any, key string, destination any) error {
	if value, ok := settings[key]; ok {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, destination); err != nil {
			return fmt.Errorf("Invalid saved MCP setting: %s", key)
		}
	}
	return nil
}
func newMCPToken() (string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes[:]), nil
}
func validMCPToken(token string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == 32
}
func validateMCPLists(hosts, origins []string) error {
	if len(hosts) > 32 || len(origins) > 32 {
		return errors.New("At most 32 allowed hosts and origins may be configured")
	}
	for _, host := range hosts {
		u, err := url.Parse("http://" + host)
		if err != nil || host == "" || u.Host != host || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(host, "*\\ \t\r\n") {
			return errors.New("Allowed hosts must be exact hostnames or host:port authorities, without wildcards")
		}
	}
	for _, origin := range origins {
		u, err := url.Parse(origin)
		if err != nil || u.User != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(origin, "*\\ \t\r\n") {
			return errors.New("Allowed origins must be exact HTTP(S) origins without paths or wildcards")
		}
	}
	return nil
}
func validateMCPDesktopSettings(settings MCPHTTPSettings) error {
	if settings.AllowRemote {
		return errors.New("The Go desktop MCP listener currently supports loopback only; use authenticated Web MCP behind HTTPS for remote access")
	}
	ip := net.ParseIP(settings.Host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("Desktop MCP host must be a loopback IP address such as 127.0.0.1 or ::1")
	}
	if settings.Port < 1 || settings.Port > 65535 {
		return errors.New("MCP port must be between 1 and 65535")
	}
	if !strings.HasPrefix(settings.Path, "/") || settings.Path == "/" || strings.ContainsAny(settings.Path, "?#\\% \t\r\n") || strings.Contains(settings.Path, "//") || strings.Contains(settings.Path, "..") {
		return errors.New("MCP path must be an absolute URL path without query, fragment or dot segments")
	}
	return validateMCPLists(settings.AllowedHosts, settings.AllowedOrigins)
}
func (s *Server) initMCP(disabled bool) error {
	state := s.snapshot()
	m := &mcpRuntime{settings: defaultMCPHTTPSettings(), web: MCPWebSettings{AllowedHosts: []string{}, AllowedOrigins: []string{}}, logs: []string{}}
	m.authCtx, m.authCancel = context.WithCancel(context.Background())
	s.mcpState = m
	if err := readMCPSetting(state.Settings, "mcp-http-server-settings", &m.settings); err != nil {
		return err
	}
	if err := readMCPSetting(state.Settings, "mcp-http-settings", &m.web); err != nil {
		return err
	}
	m.token = str(state.Settings, "mcp-http-server-token")
	m.webToken = str(state.Settings, "mcp-http-token")
	if err := validateMCPDesktopSettings(m.settings); err != nil {
		return err
	}
	if err := validateMCPLists(m.web.AllowedHosts, m.web.AllowedOrigins); err != nil {
		return err
	}
	if m.token != "" && !validMCPToken(m.token) || m.webToken != "" && !validMCPToken(m.webToken) {
		return errors.New("Invalid saved MCP token; restore the Go application state")
	}
	m.handler = mcpbridge.HTTPHandler(s.MCPBackend())
	if m.web.Enabled && m.webToken == "" {
		return errors.New("Enabled Web MCP has no saved access token")
	}
	if m.settings.Enabled && s.desktop && !disabled {
		if m.token == "" {
			return errors.New("Enabled MCP listener has no saved access token")
		}
		if err := s.requireMCPPolicy(); err != nil {
			m.lastError = err.Error()
			return nil
		}
		listener, err := net.Listen("tcp", net.JoinHostPort(m.settings.Host, strconv.Itoa(m.settings.Port)))
		if err != nil {
			m.lastError = "Cannot listen on the configured MCP address: " + err.Error()
			return nil
		}
		s.startMCPListenerLocked(listener)
	}
	return nil
}
func (s *Server) requireMCPPolicy() error {
	policy, err := s.LoadMCPPolicy()
	if err != nil {
		return err
	}
	if !policy.Configured {
		return errors.New("Save an MCP access policy before enabling the service")
	}
	return nil
}
func (m *mcpRuntime) logLocked(message string) {
	m.logs = append(m.logs, time.Now().UTC().Format(time.RFC3339)+" "+message)
	if len(m.logs) > 20 {
		m.logs = m.logs[len(m.logs)-20:]
	}
}
func (m *mcpRuntime) invalidateLocked() {
	m.authCancel()
	m.authCtx, m.authCancel = context.WithCancel(context.Background())
}

// Policy writes revoke in-flight HTTP work after the new policy is durable.
// Do not hold the application store lock while acquiring this runtime lock.
func (s *Server) revokeMCPRequests() {
	if m := s.mcpState; m != nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		if !m.closed {
			m.invalidateLocked()
		}
	}
}
func (s *Server) startMCPListenerLocked(listener net.Listener) {
	m := s.mcpState
	host := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.serveMCP(w, r, true) }), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	m.httpServer = host
	m.lastError = ""
	m.logLocked("MCP listener started at " + listener.Addr().String() + m.settings.Path)
	go func() {
		err := host.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.httpServer == host {
				m.lastError = "MCP listener stopped unexpectedly"
				m.httpServer = nil
				m.logLocked(m.lastError)
			}
		}
	}()
}
func (s *Server) closeMCP() {
	m := s.mcpState
	if m == nil {
		return
	}
	m.mu.Lock()
	m.closed = true
	m.authCancel()
	host := m.httpServer
	m.httpServer = nil
	m.mu.Unlock()
	if host != nil {
		_ = host.Close()
	}
	// Queries use independent database managers and write their audit on exit.
	// Finish those writes before Server.Close releases the encrypted store lock.
	m.requests.Wait()
}
func (s *Server) desktopMCPStatusLocked() map[string]any {
	m := s.mcpState
	var endpoint, token, lastError any
	if m.httpServer != nil {
		endpoint = "http://" + net.JoinHostPort(m.settings.Host, strconv.Itoa(m.settings.Port)) + m.settings.Path
	}
	if m.token != "" {
		token = m.token
	}
	if m.lastError != "" {
		lastError = m.lastError
	}
	return map[string]any{"enabled": m.settings.Enabled, "running": m.httpServer != nil, "endpoint": endpoint, "accessToken": token, "lastError": lastError, "recentLogs": slices.Clone(m.logs)}
}
func (s *Server) webMCPStatusLocked() map[string]any {
	m := s.mcpState
	var token, source any
	if m.webToken != "" {
		token, source = m.webToken, "managed"
	}
	return map[string]any{"enabled": m.web.Enabled, "endpointPath": "/mcp", "accessToken": token, "tokenSource": source, "allowedHosts": slices.Clone(m.web.AllowedHosts), "allowedOrigins": slices.Clone(m.web.AllowedOrigins), "deploymentManaged": false, "managementAvailable": true}
}
func (s *Server) saveMCPDesktop(settings MCPHTTPSettings) (map[string]any, error) {
	if !s.desktop {
		return nil, errors.New("Use Web MCP settings for this server")
	}
	if err := validateMCPDesktopSettings(settings); err != nil {
		return nil, err
	}
	if settings.AllowedHosts == nil {
		settings.AllowedHosts = []string{}
	}
	if settings.AllowedOrigins == nil {
		settings.AllowedOrigins = []string{}
	}
	if settings.Enabled {
		if err := s.requireMCPPolicy(); err != nil {
			return nil, err
		}
	}
	m := s.mcpState
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("Application is closing")
	}
	var candidate net.Listener
	sameAddress := m.settings.Host == settings.Host && m.settings.Port == settings.Port
	if settings.Enabled && (m.httpServer == nil || !sameAddress) {
		var err error
		candidate, err = net.Listen("tcp", net.JoinHostPort(settings.Host, strconv.Itoa(settings.Port)))
		if err != nil {
			return nil, fmt.Errorf("Cannot start MCP listener: %w", err)
		}
	}
	token := m.token
	if token == "" {
		var err error
		token, err = newMCPToken()
		if err != nil {
			if candidate != nil {
				_ = candidate.Close()
			}
			return nil, err
		}
	}
	if err := s.update(func(state *diskState) error {
		state.Settings["mcp-http-server-settings"] = settings
		state.Settings["mcp-http-server-token"] = token
		return nil
	}); err != nil {
		if candidate != nil {
			_ = candidate.Close()
		}
		return nil, err
	}
	m.settings, m.token = settings, token
	m.invalidateLocked()
	if m.httpServer != nil && (!settings.Enabled || !sameAddress) {
		_ = m.httpServer.Close()
		m.httpServer = nil
		m.logLocked("MCP listener stopped")
	}
	if candidate != nil {
		s.startMCPListenerLocked(candidate)
	}
	m.lastError = ""
	return s.desktopMCPStatusLocked(), nil
}
func (s *Server) saveMCPWeb(settings MCPWebSettings) (map[string]any, error) {
	if s.desktop {
		return nil, errors.New("Use desktop MCP settings in Wails")
	}
	if err := validateMCPLists(settings.AllowedHosts, settings.AllowedOrigins); err != nil {
		return nil, err
	}
	if settings.AllowedHosts == nil {
		settings.AllowedHosts = []string{}
	}
	if settings.AllowedOrigins == nil {
		settings.AllowedOrigins = []string{}
	}
	if settings.Enabled {
		if err := s.requireMCPPolicy(); err != nil {
			return nil, err
		}
	}
	m := s.mcpState
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("Application is closing")
	}
	token := m.webToken
	if token == "" {
		var err error
		token, err = newMCPToken()
		if err != nil {
			return nil, err
		}
	}
	if err := s.update(func(state *diskState) error {
		state.Settings["mcp-http-settings"] = settings
		state.Settings["mcp-http-token"] = token
		return nil
	}); err != nil {
		return nil, err
	}
	m.web, m.webToken = settings, token
	m.invalidateLocked()
	return s.webMCPStatusLocked(), nil
}
func (s *Server) rotateMCPToken(desktop bool) (map[string]any, error) {
	if desktop != s.desktop {
		return nil, errors.New("MCP transport does not match this application")
	}
	token, err := newMCPToken()
	if err != nil {
		return nil, err
	}
	m := s.mcpState
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("Application is closing")
	}
	key := "mcp-http-token"
	if desktop {
		key = "mcp-http-server-token"
	}
	if err = s.update(func(state *diskState) error { state.Settings[key] = token; return nil }); err != nil {
		return nil, err
	}
	m.invalidateLocked()
	if desktop {
		m.token = token
		return s.desktopMCPStatusLocked(), nil
	}
	m.webToken = token
	return s.webMCPStatusLocked(), nil
}
func (s *Server) handleMCPSettings(w http.ResponseWriter, r *http.Request, payload map[string]any) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/app-settings/mcp-") {
		return false
	}
	key := strings.TrimPrefix(r.URL.Path, "/api/app-settings/")
	known := []string{"mcp-policy", "mcp-http-server", "mcp-http-server/status", "mcp-http-server/rotate-token", "mcp-http-status", "mcp-http", "mcp-http/rotate-token"}
	if !slices.Contains(known, key) {
		return false
	}
	if r.Method != "GET" && r.Header.Get("X-DBX-MCP-Settings") != "1" && key != "mcp-policy" {
		writeError(w, 403, "MCP settings require an explicit management request")
		return true
	}
	var result any
	var err error
	switch {
	case key == "mcp-policy" && r.Method == "GET":
		result, err = s.LoadMCPPolicy()
	case key == "mcp-policy" && r.Method == "PUT":
		var policy MCPPolicy
		err = decodeMCPPayload(payload, &policy)
		if err == nil {
			err = s.SaveMCPPolicy(policy)
		}
	case key == "mcp-http-server" && r.Method == "GET":
		s.mcpState.mu.Lock()
		result = s.mcpState.settings
		s.mcpState.mu.Unlock()
	case key == "mcp-http-server/status" && r.Method == "GET":
		s.mcpState.mu.Lock()
		result = s.desktopMCPStatusLocked()
		s.mcpState.mu.Unlock()
	case key == "mcp-http-server" && r.Method == "PUT":
		var settings MCPHTTPSettings
		err = decodeMCPPayload(payload, &settings)
		if err == nil {
			result, err = s.saveMCPDesktop(settings)
		}
	case key == "mcp-http-server/rotate-token" && r.Method == "POST":
		result, err = s.rotateMCPToken(true)
	case key == "mcp-http-status" && r.Method == "GET":
		s.mcpState.mu.Lock()
		result = s.webMCPStatusLocked()
		s.mcpState.mu.Unlock()
	case key == "mcp-http" && r.Method == "PUT":
		var settings MCPWebSettings
		err = decodeMCPPayload(payload, &settings)
		if err == nil {
			result, err = s.saveMCPWeb(settings)
		}
	case key == "mcp-http/rotate-token" && r.Method == "POST":
		result, err = s.rotateMCPToken(false)
	default:
		writeError(w, 405, "Method not allowed")
		return true
	}
	if err != nil {
		writeError(w, 400, err.Error())
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
func decodeMCPPayload(payload map[string]any, target any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return errors.New("Invalid MCP settings")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return fmt.Errorf("Invalid MCP settings: %w", err)
	}
	return nil
}
func mcpHostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(host)
	if len(allowed) > 0 {
		for _, item := range allowed {
			if strings.EqualFold(host, item) {
				return true
			}
		}
		return false
	}
	u, err := url.Parse("http://" + host)
	if err != nil || u.User != nil || u.Host != host || u.Path != "" || u.RawQuery != "" {
		return false
	}
	name := u.Hostname()
	if name == "localhost" {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}
func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request, desktop bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	m := s.mcpState
	m.mu.Lock()
	enabled, token, hosts, origins, path := m.web.Enabled, m.webToken, slices.Clone(m.web.AllowedHosts), slices.Clone(m.web.AllowedOrigins), "/mcp"
	if desktop {
		enabled, token, hosts, origins, path = m.settings.Enabled, m.token, slices.Clone(m.settings.AllowedHosts), slices.Clone(m.settings.AllowedOrigins), m.settings.Path
	}
	closed, generation, handler := m.closed, m.authCtx, m.handler
	if !closed {
		m.requests.Add(1)
		defer m.requests.Done()
	}
	m.mu.Unlock()
	if closed || !enabled || r.URL.Path != path {
		http.NotFound(w, r)
		return
	}
	if !mcpHostAllowed(r.Host, hosts) {
		writeError(w, 403, "MCP Host is not allowed")
		return
	}
	origin := r.Header.Get("Origin")
	if origin != "" {
		allowed := slices.Contains(origins, origin)
		if len(origins) == 0 {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			allowed = origin == scheme+"://"+r.Host
		}
		if !allowed {
			writeError(w, 403, "MCP Origin is not allowed")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id,Mcp-Protocol-Version")
	}
	if r.Method == "OPTIONS" {
		if origin == "" {
			writeError(w, 403, "Origin is required")
			return
		}
		w.Header().Set("Access-Control-Allow-Methods", "POST,GET,DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type,Mcp-Protocol-Version,Mcp-Session-Id,Mcp-Method,Mcp-Name,Last-Event-Id")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="DBX MCP"`)
		writeError(w, 401, "MCP bearer token is required")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(generation, cancel)
	defer func() { stop(); cancel() }()
	if generation.Err() != nil {
		cancel()
	}
	r = r.WithContext(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	handler.ServeHTTP(w, r)
}
