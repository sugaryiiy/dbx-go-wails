// Package server implements the transport shared by the Wails asset server and Web mode.
package server

import (
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sugaryiiy/dbx-go-wails/internal/database"
)

type Options struct {
	DataDir  string
	Password string
	Desktop  bool
	Assets   fs.FS
	// Stdio MCP uses the data store exclusively without restoring a listener.
	DisableMCPListener bool
}
type Server struct {
	database     *database.Manager
	storeLock    *os.File
	closeOnce    sync.Once
	closeErr     error
	dataDir      string
	desktop      bool
	assets       fs.FS
	passwordHash [32]byte
	mu           sync.Mutex
	state        diskState
	aead         cipher.AEAD
	sessions     map[string]time.Time
	failedLogins int
	lockedUntil  time.Time
	extensions   *extensionState
	sampleMu     sync.Mutex
	mcpState     *mcpRuntime
}

func New(opts Options) (*Server, error) {
	if !opts.Desktop && opts.Password == "" {
		return nil, errors.New("Web mode requires a password")
	}
	dir := opts.DataDir
	if dir == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(root, "DBX-Go-Wails")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	s := &Server{database: database.NewManager(), dataDir: absolute, desktop: opts.Desktop, assets: opts.Assets, passwordHash: sha256.Sum256([]byte(opts.Password)), sessions: map[string]time.Time{}, extensions: newExtensionState()}
	if err = s.openStore(); err != nil {
		_ = s.Close()
		return nil, err
	}
	if err = s.initMCP(opts.DisableMCPListener); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.closeMCP()
		s.extensions.close()
		s.closeErr = s.database.Close()
		if s.storeLock != nil {
			_ = unlockStore(s.storeLock)
			if err := s.storeLock.Close(); s.closeErr == nil {
				s.closeErr = err
			}
		}
	})
	return s.closeErr
}

// Desktop mode is for Wails' in-process asset handler only, never an HTTP listener.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	if !s.desktop && r.URL.Path == "/mcp" {
		s.serveMCP(w, r, false)
		return
	}
	if s.desktop && r.RemoteAddr != "" {
		writeError(w, 403, "Desktop API is available only inside Wails")
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		s.serveAsset(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !validOrigin(r, s.desktop) {
		writeError(w, 403, "Cross-origin API requests are not allowed")
		return
	}
	if r.Method != "GET" && r.Method != "POST" && r.Method != "PUT" && r.Method != "DELETE" {
		writeError(w, 405, "Method not allowed")
		return
	}
	route := strings.TrimSuffix(r.URL.Path, "/")
	if route == "/api/auth/check" && r.Method == "GET" {
		writeJSON(w, 200, map[string]any{"required": !s.desktop, "authenticated": s.authenticated(r), "setup_required": false})
		return
	}
	if route != "/api/auth/login" && !s.authenticated(r) {
		writeError(w, 401, "Authentication required")
		return
	}
	payload := map[string]any{}
	for key, values := range r.URL.Query() {
		if len(values) > 0 {
			payload[key] = values[0]
		}
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		limit := integer(s.snapshot().Settings["sql-file-upload-max-bytes"], maxTransferBytes)
		r.Body = http.MaxBytesReader(w, r.Body, int64(limit))
	} else if r.Method == "POST" || r.Method == "PUT" {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
		decoder := json.NewDecoder(r.Body)
		var body map[string]any
		if err := decoder.Decode(&body); err != nil && err != io.EOF {
			writeError(w, 400, "Invalid or oversized JSON request")
			return
		}
		for key, value := range body {
			payload[key] = value
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			writeError(w, 400, "Expected one JSON object")
			return
		}
	}
	if route == "/api/auth/login" {
		if r.Method != "POST" {
			writeError(w, 405, "Method not allowed")
			return
		}
		s.login(w, r, payload)
		return
	}
	if route == "/api/auth/logout" && r.Method == "POST" {
		s.logout(w, r)
		return
	}
	// Execution and connection mutations never accept a GET navigation.
	if (strings.HasPrefix(route, "/api/query/") || (strings.HasPrefix(route, "/api/connection/") && route != "/api/connection/list")) && r.Method != http.MethodPost {
		writeError(w, 405, "This operation requires POST")
		return
	}
	if s.handleMCPSettings(w, r, payload) {
		return
	}
	if s.handleApplication(w, r, payload) {
		return
	}
	if s.handleExtension(w, r, payload) {
		return
	}
	result, err := s.database.Handle(r.Context(), route, payload)
	if err != nil {
		if errors.Is(err, database.ErrUnsupported) {
			writeError(w, 501, "This endpoint or database capability has not yet been migrated to Go: "+route)
		} else {
			writeError(w, 400, s.redactError(err.Error(), payload))
		}
		return
	}
	writeJSON(w, 200, result)
}
func validOrigin(r *http.Request, desktop bool) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.User != nil {
		return false
	}
	if desktop {
		return origin == "wails://wails" || (u.Host == "wails.localhost" && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "wails"))
	}
	expectedScheme := "http"
	if r.TLS != nil {
		expectedScheme = "https"
	}
	return u.Host == r.Host && u.Scheme == expectedScheme
}
func (s *Server) authenticated(r *http.Request) bool {
	if s.desktop {
		return true
	}
	cookie, err := r.Cookie("dbx_session")
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry, ok := s.sessions[cookie.Value]
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		delete(s.sessions, cookie.Value)
		return false
	}
	return true
}
func (s *Server) login(w http.ResponseWriter, r *http.Request, p map[string]any) {
	if s.desktop {
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Now().Before(s.lockedUntil) {
		writeError(w, 429, "Too many login attempts; retry in one minute")
		return
	}
	given := sha256.Sum256([]byte(str(p, "password")))
	if subtle.ConstantTimeCompare(given[:], s.passwordHash[:]) != 1 {
		s.failedLogins++
		if s.failedLogins >= 5 {
			s.lockedUntil = time.Now().Add(time.Minute)
			s.failedLogins = 0
		}
		writeError(w, 401, "Invalid password")
		return
	}
	s.failedLogins = 0
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		writeError(w, 500, "Cannot create session")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	for key, expiry := range s.sessions {
		if time.Now().After(expiry) {
			delete(s.sessions, key)
		}
	}
	if len(s.sessions) >= 256 {
		writeError(w, 429, "Too many active sessions")
		return
	}
	s.sessions[token] = time.Now().Add(24 * time.Hour)
	http.SetCookie(w, &http.Cookie{Name: "dbx_session", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 86400})
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("dbx_session"); err == nil {
		s.mu.Lock()
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "dbx_session", Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		writeError(w, 405, "Method not allowed")
		return
	}
	if s.assets == nil {
		writeError(w, 404, "Frontend assets are not installed")
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if _, err := fs.Stat(s.assets, name); err != nil {
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		name = "index.html"
	}
	data, err := fs.ReadFile(s.assets, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if strings.HasSuffix(name, ".html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	} else if strings.HasSuffix(name, ".js") {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	} else if strings.HasSuffix(name, ".css") {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}
	http.ServeContent(w, r, name, time.Time{}, strings.NewReader(string(data)))
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"version": 1, "code": "DBX-LEGACY-0001", "messageKey": "backendErrors.legacy", "detail": message, "source": "go", "messageParams": map[string]any{}, "operationOutcome": "unknown"})
}
func str(p map[string]any, key string) string { v, _ := p[key].(string); return v }
func cloneMap(p map[string]any) map[string]any {
	b, _ := json.Marshal(p)
	var result map[string]any
	_ = json.Unmarshal(b, &result)
	return result
}
func (s *Server) redactError(message string, payload map[string]any) string {
	redact := func(p map[string]any) {
		walkSecrets(p, func(_ map[string]any, _ string, value string) {
			if value != "" {
				message = strings.ReplaceAll(message, value, "[REDACTED]")
				message = strings.ReplaceAll(message, url.QueryEscape(value), "[REDACTED]")
				message = strings.ReplaceAll(message, url.PathEscape(value), "[REDACTED]")
			}
		})
	}
	redact(payload)
	s.mu.Lock()
	for _, c := range s.state.Connections {
		redact(c)
	}
	s.mu.Unlock()
	if len(message) > 2048 {
		message = message[:2048]
	}
	return message
}

var _ http.Handler = (*Server)(nil)
