package server

import (
	"crypto/sha256"
	"encoding/hex"
	"gopkg.in/yaml.v3"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const maxUserSkillBytes = 1 << 20
const userSkillHeadBytes = 64 << 10

type userSkill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content,omitempty"`
}
type userSkillListing struct {
	Status string      `json:"status"`
	Skills []userSkill `json:"skills"`
}

func skillID(prefix, name string) string {
	digest := sha256.Sum256([]byte(name))
	return prefix + "-" + hex.EncodeToString(digest[:8])
}
func canonicalSkillRoot(root string) string {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return ""
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return ""
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return ""
	}
	return canonical
}
func insideSkillRoot(root, path string) bool {
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return false
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
func skillPath(root, name string) string {
	dir, err := filepath.EvalSymlinks(filepath.Join(root, name))
	if err != nil || !insideSkillRoot(root, dir) {
		return ""
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}
	file, err := filepath.EvalSymlinks(filepath.Join(dir, "SKILL.md"))
	if err != nil || !insideSkillRoot(root, file) {
		return ""
	}
	return file
}
func skillFrontmatter(text string) (string, string, bool) {
	text = strings.TrimPrefix(text, "\ufeff")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r \t") != "---" {
		return "", "", false
	}
	end := -1
	size := 0
	for i := 1; i < len(lines); i++ {
		size += len(lines[i]) + 1
		if size > userSkillHeadBytes {
			return "", "", false
		}
		if strings.TrimRight(lines[i], "\r \t") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return "", "", false
	}
	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &meta) != nil {
		return "", "", false
	}
	name, desc := strings.TrimSpace(meta.Name), strings.TrimSpace(meta.Description)
	return name, desc, name != "" && desc != ""
}
func readSkillFile(path, id string, full bool) (userSkill, string) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return userSkill{}, "not_found"
	}
	if info.Size() > maxUserSkillBytes {
		return userSkill{}, "oversized"
	}
	file, err := os.Open(path)
	if err != nil {
		return userSkill{}, "unreadable"
	}
	defer file.Close()
	limit := int64(userSkillHeadBytes)
	if full {
		limit = maxUserSkillBytes + 1
	}
	b, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return userSkill{}, "unreadable"
	}
	if len(b) > maxUserSkillBytes {
		return userSkill{}, "oversized"
	}
	if !full && info.Size() > int64(len(b)) {
		for !utf8.Valid(b) && len(b) > 0 && len(b) > userSkillHeadBytes-4 {
			b = b[:len(b)-1]
		}
	}
	if !utf8.Valid(b) {
		return userSkill{}, "not_utf8"
	}
	name, desc, ok := skillFrontmatter(string(b))
	if !ok {
		return userSkill{}, "invalid_frontmatter"
	}
	skill := userSkill{ID: id, Name: name, Description: desc}
	if full {
		skill.Content = string(b)
	}
	return skill, ""
}
func listSkillRoot(root, prefix string) userSkillListing {
	out := userSkillListing{Status: "missing", Skills: []userSkill{}}
	canonical := canonicalSkillRoot(root)
	if canonical == "" {
		if info, err := os.Stat(root); err == nil && !info.IsDir() {
			out.Status = "invalid"
		}
		return out
	}
	out.Status = "ok"
	entries, err := os.ReadDir(canonical)
	if err != nil {
		out.Status = "invalid"
		return out
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		path := skillPath(canonical, name)
		if path == "" {
			continue
		}
		skill, reason := readSkillFile(path, skillID(prefix, name), false)
		if reason == "" {
			out.Skills = append(out.Skills, skill)
		}
	}
	sort.Slice(out.Skills, func(i, j int) bool {
		a, b := strings.ToLower(out.Skills[i].Name), strings.ToLower(out.Skills[j].Name)
		if a == b {
			return out.Skills[i].ID < out.Skills[j].ID
		}
		return a < b
	})
	return out
}
func readSkillIDs(ids []any, roots map[string]string) map[string]any {
	skills := []userSkill{}
	failures := []map[string]any{}
	seen := map[string]bool{}
	for _, raw := range ids {
		id, ok := raw.(string)
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		prefix, _, ok := strings.Cut(id, "-")
		reason := "not_found"
		var skill userSkill
		if ok && (prefix == "d" || prefix == "c") {
			root := roots[prefix]
			if root == "" {
				reason = "root_unavailable"
			} else if entries, err := os.ReadDir(root); err == nil {
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".") || skillID(prefix, entry.Name()) != id {
						continue
					}
					if path := skillPath(root, entry.Name()); path != "" {
						skill, reason = readSkillFile(path, id, true)
					}
					break
				}
			}
		}
		if reason == "" {
			skills = append(skills, skill)
		} else {
			failures = append(failures, map[string]any{"id": id, "reason": reason})
		}
	}
	return map[string]any{"skills": skills, "failures": failures}
}
func (s *Server) handleUserSkills(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	route := strings.TrimSuffix(r.URL.Path, "/")
	if route != "/api/ai/skills/list" && route != "/api/ai/skills/read" {
		return false
	}
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return true
	}
	if !s.desktop {
		writeError(w, 403, "User-level filesystem skills are available only in Wails desktop")
		return true
	}
	home, _ := os.UserHomeDir()
	defaultRoot := filepath.Join(home, ".agents", "skills")
	custom := str(p, "customRoot")
	enabled := p["customRootEnabled"] == true
	var result any
	if strings.HasSuffix(route, "list") {
		var customListing any
		if enabled && strings.TrimSpace(custom) != "" {
			customListing = listSkillRoot(custom, "c")
		}
		result = map[string]any{"defaultRoot": listSkillRoot(defaultRoot, "d"), "customRoot": customListing}
	} else {
		roots := map[string]string{"d": canonicalSkillRoot(defaultRoot)}
		if enabled {
			roots["c"] = canonicalSkillRoot(custom)
		}
		ids, ok := p["ids"].([]any)
		if !ok || len(ids) > 100 {
			writeError(w, 400, "ids must contain at most 100 skill ids")
			return true
		}
		result = readSkillIDs(ids, roots)
	}
	writeJSON(w, 200, result)
	return true
}
