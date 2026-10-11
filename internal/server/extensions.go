package server

import (
	"net/http"
	"strings"
)

// Extension handlers are implemented separately from application persistence.
func (s *Server) handleExtension(w http.ResponseWriter, r *http.Request, payload map[string]any) bool {
	if s.handlePlugins(w, r, payload) {
		return true
	}
	if s.handleSQLFile(w, r, payload) {
		return true
	}
	if s.handleAgents(w, r, payload) {
		return true
	}
	if s.handleAI(w, r, payload) || s.handleAppCache(w, r, payload) || s.handlePrompts(w, r, payload) || s.handleUserSkills(w, r, payload) {
		return true
	}
	if s.handleAdvancedSQL(w, r, payload) || s.handleSchemaTools(w, r, payload) || s.handleDataCompare(w, r, payload) || s.handleDocs(w, r, payload) {
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/api/import/") || strings.HasPrefix(r.URL.Path, "/api/export/") {
		return s.handleFileTransfer(w, r, payload)
	}
	if s.handleGridSQL(w, r, payload) {
		return true
	}
	if result, handled, err := buildSQL(r.URL.Path, payload); handled {
		if r.Method != "POST" {
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
	return false
}
