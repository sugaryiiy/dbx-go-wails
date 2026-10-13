package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type docsAnnotation struct {
	FormatVersion int                      `json:"formatVersion"`
	Project       *docsProjectNote         `json:"project,omitempty"`
	Groups        []docsGroupNote          `json:"groups,omitempty"`
	Tables        map[string]docsTableNote `json:"tables,omitempty"`
}
type docsProjectNote struct {
	Name *string `json:"name,omitempty"`
	Note *string `json:"note,omitempty"`
}
type docsGroupNote struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	Hue  int     `json:"hue"`
	Note *string `json:"note,omitempty"`
}
type docsTableNote struct {
	Group   *string                   `json:"group,omitempty"`
	Note    *string                   `json:"note,omitempty"`
	Columns map[string]docsColumnNote `json:"columns,omitempty"`
}
type docsColumnNote struct {
	Note string `json:"note"`
}

func parseDocsAnnotation(raw any) (docsAnnotation, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return docsAnnotation{}, err
	}
	return decodeDocsAnnotation(b)
}
func decodeDocsAnnotation(b []byte) (docsAnnotation, error) {
	var note docsAnnotation
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&note) != nil {
		return note, errors.New("Invalid documentation annotation format")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return note, errors.New("Expected one annotation object")
	}
	if note.FormatVersion != 1 {
		return note, errors.New("Unsupported annotation formatVersion; expected 1")
	}
	for _, group := range note.Groups {
		if group.ID == "" || group.Name == "" || group.Hue < 0 || group.Hue > 359 {
			return note, errors.New("Invalid annotation group")
		}
	}
	return note, nil
}
func (s *Server) annotationPath(config map[string]any) (string, error) {
	id := str(config, "id")
	if !validAppID(id) {
		return "", errors.New("Invalid connection id")
	}
	explicit := strings.TrimSpace(str(config, "docs_notes_path"))
	if explicit == "" {
		explicit = strings.TrimSpace(str(config, "docsNotesPath"))
	}
	if explicit != "" {
		if !s.desktop {
			return "", errors.New("Custom documentation annotation paths are available only in Wails desktop")
		}
		path, err := filepath.Abs(explicit)
		if err != nil {
			return "", errors.New("Invalid annotation file path")
		}
		if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return "", errors.New("Annotation path must be a regular file")
		}
		return path, nil
	}
	return filepath.Join(s.dataDir, "docs-notes", id+".json"), nil
}
func saveDocsAnnotation(path string, note docsAnnotation) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errors.New("Cannot create annotation directory")
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("Annotation path must be a regular file")
	}
	b, err := json.MarshalIndent(note, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".dbx-notes-*")
	if err != nil {
		return errors.New("Cannot save documentation annotations")
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		return errors.New("Cannot publish documentation annotations")
	}
	return nil
}
func (s *Server) handleDocsAnnotations(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	route := strings.TrimSuffix(r.URL.Path, "/")
	if !strings.HasPrefix(route, "/api/docs/annotations/") {
		return false
	}
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return true
	}
	var config map[string]any
	for _, c := range s.snapshot().Connections {
		if str(c, "id") == str(p, "connectionId") {
			config = c
			break
		}
	}
	if config == nil {
		writeError(w, 400, "Connection does not exist")
		return true
	}
	var result any
	var err error
	switch route {
	case "/api/docs/annotations/load":
		var path string
		path, err = s.annotationPath(config)
		if err == nil {
			if e := regularFile(path); errors.Is(e, os.ErrNotExist) {
				break
			} else if e != nil {
				err = errors.New("Annotation path must be a regular file")
				break
			}
			f, e := os.Open(path)
			if e != nil {
				err = errors.New("Cannot read documentation annotations")
				break
			}
			b, e := io.ReadAll(io.LimitReader(f, 4<<20+1))
			f.Close()
			if e != nil || len(b) > 4<<20 {
				err = errors.New("Documentation annotations exceed size limit")
				break
			}
			result, err = decodeDocsAnnotation(b)
		}
	case "/api/docs/annotations/save":
		var note docsAnnotation
		note, err = parseDocsAnnotation(p["annotations"])
		if err == nil {
			var path string
			path, err = s.annotationPath(config)
			if err == nil {
				err = saveDocsAnnotation(path, note)
			}
		}
	case "/api/docs/annotations/apply":
		var note docsAnnotation
		note, err = parseDocsAnnotation(p["annotations"])
		if err == nil {
			snapshot, ok := p["snapshot"].(map[string]any)
			if !ok || integer(snapshot["formatVersion"], 0) != 1 {
				err = errors.New("Invalid schema snapshot")
			} else {
				result = applyDocsAnnotation(snapshot, note, str(config, "db_type"))
			}
		}
	default:
		return false
	}
	if err != nil {
		writeError(w, 400, err.Error())
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
func docsFold(kind, text string) string {
	switch kind {
	case "oracle":
		return strings.ToUpper(text)
	case "clickhouse", "mongodb":
		return text
	default:
		return strings.ToLower(text)
	}
}
func applyDocsAnnotation(raw map[string]any, note docsAnnotation, kind string) map[string]any {
	snapshot := cloneMap(raw)
	project := appObject(snapshot["project"])
	if note.Project != nil {
		if note.Project.Name != nil && strings.TrimSpace(*note.Project.Name) != "" {
			project["name"] = *note.Project.Name
		}
		if note.Project.Note != nil {
			project["note"] = *note.Project.Note
		}
	}
	snapshot["project"] = project
	groups := []docsGroupNote{}
	known := map[string]bool{}
	for _, group := range note.Groups {
		if !known[group.ID] {
			known[group.ID] = true
			groups = append(groups, group)
		}
	}
	snapshot["groups"] = groups
	liveTables, liveColumns := map[string]bool{}, map[string]bool{}
	tables := appItems(snapshot["tables"])
	for _, table := range tables {
		key := str(table, "name")
		if schema := str(table, "schema"); schema != "" {
			key = schema + "." + key
		}
		key = docsFold(kind, key)
		liveTables[key] = true
		columns := appItems(table["columns"])
		for _, column := range columns {
			liveColumns[key+"."+docsFold(kind, str(column, "name"))] = true
		}
		ann, ok := note.Tables[key]
		if !ok {
			continue
		}
		if ann.Note != nil && strings.TrimSpace(*ann.Note) != "" {
			if str(table, "noteSource") == "DATABASE" {
				table["shadowedNote"] = table["note"]
			}
			table["note"] = *ann.Note
			table["noteSource"] = "LOCAL"
		}
		table["groupId"] = nil
		if ann.Group != nil && known[*ann.Group] {
			table["groupId"] = *ann.Group
		}
		columnNotes := appObject(table["columnNotes"])
		for _, column := range columns {
			for name, n := range ann.Columns {
				if docsFold(kind, name) == docsFold(kind, str(column, "name")) && strings.TrimSpace(n.Note) != "" {
					var shadow any
					if comment := str(column, "comment"); strings.TrimSpace(comment) != "" {
						shadow = comment
					}
					columnNotes[str(column, "name")] = map[string]any{"note": n.Note, "source": "LOCAL", "shadowed": shadow}
					break
				}
			}
		}
		table["columnNotes"] = columnNotes
	}
	snapshot["tables"] = tables
	orphans := 0
	for key, ann := range note.Tables {
		key = docsFold(kind, key)
		if !liveTables[key] {
			orphans++
			continue
		}
		for column := range ann.Columns {
			if !liveColumns[key+"."+docsFold(kind, column)] {
				orphans++
			}
		}
	}
	if orphans > 0 {
		warnings := []any{}
		if previous, ok := snapshot["warnings"].([]any); ok {
			warnings = previous
		}
		warnings = append(warnings, map[string]any{"kind": "orphanedNotes", "count": orphans})
		snapshot["warnings"] = warnings
	}
	return snapshot
}
