package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sugaryiiy/magpiedb/internal/database"
)

// This deliberately recognizes only direct single-target table statements.
// Quoted identifiers stay intact, including embedded dots and escaped quotes.
func sqlFileTable(statement string) map[string]any {
	tokens := schemaLex(statement)
	i := 0
	word := func(expected string) bool {
		if i >= len(tokens) {
			return false
		}
		t := tokens[i]
		if !strings.EqualFold(statement[t.start:t.end], expected) {
			return false
		}
		i++
		return true
	}
	verb := ""
	switch {
	case word("CREATE"):
		verb = "CREATE"
		if !word("TEMP") {
			word("TEMPORARY")
		}
		if !word("TABLE") {
			return nil
		}
		if word("IF") && (!word("NOT") || !word("EXISTS")) {
			return nil
		}
	case word("DROP"):
		verb = "DROP"
		if !word("TABLE") {
			return nil
		}
		if word("IF") && !word("EXISTS") {
			return nil
		}
	case word("ALTER"):
		verb = "ALTER"
		if !word("TABLE") {
			return nil
		}
	case word("INSERT"), word("REPLACE"):
		verb = "INSERT"
		if word("OR") {
			if !word("ABORT") && !word("FAIL") && !word("IGNORE") && !word("REPLACE") && !word("ROLLBACK") {
				return nil
			}
		}
		if !word("INTO") {
			return nil
		}
	case word("UPDATE"):
		verb = "UPDATE"
		if word("OR") {
			if !word("ABORT") && !word("FAIL") && !word("IGNORE") && !word("REPLACE") && !word("ROLLBACK") {
				return nil
			}
		}
	case word("DELETE"):
		verb = "DELETE"
		if !word("FROM") {
			return nil
		}
	default:
		return nil
	}
	if i >= len(tokens) || tokens[i].identifier == "" {
		return nil
	}
	name := tokens[i].identifier
	end := tokens[i].end
	i++
	var namespace any
	if i < len(tokens) && tokens[i].symbol == '.' {
		i++
		if i >= len(tokens) || tokens[i].identifier == "" {
			return nil
		}
		namespace, name, end = name, tokens[i].identifier, tokens[i].end
		i++
		if i < len(tokens) && tokens[i].symbol == '.' {
			return nil
		}
	}
	// SQL engines can accept multi-target DROP/UPDATE/DELETE. Table filtering
	// must never interpret those as a single selected target.
	tail := strings.TrimSpace(statement[end:])
	if strings.HasPrefix(tail, ",") || verb == "DROP" && strings.Contains(tail, ",") {
		return nil
	}
	if verb == "UPDATE" && !word("SET") {
		return nil
	}
	if verb == "DELETE" && i < len(tokens) {
		if !word("WHERE") && !word("ORDER") && !word("LIMIT") && !word("RETURNING") {
			return nil
		}
	}
	return map[string]any{"name": name, "database": namespace}
}
func sqlFileTableKey(table map[string]any) string {
	return str(table, "database") + "\x00" + str(table, "name")
}
func (s *Server) sqlFileSource(ref string) (*importSource, error) {
	if !strings.HasPrefix(ref, "go-sql://") {
		return nil, errors.New("SQL file must be uploaded or selected through the native file picker")
	}
	s.extensions.mu.Lock()
	defer s.extensions.mu.Unlock()
	source := s.extensions.sources[strings.TrimPrefix(ref, "go-sql://")]
	if source == nil || source.sql == "" || time.Since(source.created) > 30*time.Minute {
		return nil, errors.New("SQL preview expired or was released; upload it again")
	}
	return source, nil
}
func (s *Server) previewSQLUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTransferBytes+1<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeError(w, 400, "SQL upload is invalid or exceeds 32 MiB")
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, "Upload a UTF-8 SQL file")
		return
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxTransferBytes+1))
	if err != nil || len(b) > maxTransferBytes {
		writeError(w, 413, "SQL upload exceeds 32 MiB")
		return
	}
	if !utf8.Valid(b) {
		writeError(w, 400, "SQL file must contain UTF-8 text")
		return
	}
	text := strings.TrimPrefix(string(b), "\ufeff")
	if strings.TrimSpace(text) == "" {
		writeError(w, 400, "SQL file is empty")
		return
	}
	id := "sql-" + transferID()
	s.extensions.mu.Lock()
	total := len(b)
	for key, source := range s.extensions.sources {
		if time.Since(source.created) > 30*time.Minute {
			delete(s.extensions.sources, key)
		} else {
			total += len(source.sql)
		}
	}
	if total > 128<<20 || len(s.extensions.sources) >= 32 {
		s.extensions.mu.Unlock()
		writeError(w, 429, "Too many retained upload previews; release unused files")
		return
	}
	s.extensions.sources[id] = &importSource{sql: text, name: filepath.Base(header.Filename), created: time.Now()}
	s.extensions.mu.Unlock()
	preview := []rune(text)
	if len(preview) > 4000 {
		preview = preview[:4000]
	}
	writeJSON(w, 200, map[string]any{"fileName": filepath.Base(header.Filename), "filePath": "go-sql://" + id, "sizeBytes": len(b), "preview": string(preview), "canExecuteWithoutSelectedDatabase": false, "establishesDatabaseContext": false, "cleanupToken": id})
}
func (s *Server) handleSQLFile(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	path := r.URL.Path
	if !strings.HasPrefix(path, "/api/sql-file/") {
		return false
	}
	if strings.HasPrefix(path, "/api/sql-file/progress/") && r.Method == "GET" {
		id := strings.TrimPrefix(path, "/api/sql-file/progress/")
		job, err := s.extensions.job("sql:"+id, id, "execution")
		if err != nil {
			writeError(w, 400, err.Error())
		} else {
			s.streamJob(w, r, job)
		}
		return true
	}
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return true
	}
	switch path {
	case "/api/sql-file/preview":
		s.previewSQLUpload(w, r)
	case "/api/sql-file/preview/release":
		s.extensions.mu.Lock()
		delete(s.extensions.sources, str(p, "cleanupToken"))
		s.extensions.mu.Unlock()
		writeJSON(w, 200, nil)
	case "/api/sql-file/tables":
		source, err := s.sqlFileSource(str(p, "filePath"))
		if err != nil {
			writeError(w, 400, err.Error())
			return true
		}
		parts, err := database.SplitStatementsForDialect(source.sql, "sqlite")
		if err != nil {
			parts, err = database.SplitStatements(source.sql)
		}
		if err != nil {
			writeError(w, 400, err.Error())
			return true
		}
		list := []map[string]any{}
		seen := map[string]bool{}
		for _, part := range parts {
			if table := sqlFileTable(part); table != nil {
				key := sqlFileTableKey(table)
				if !seen[key] {
					list = append(list, table)
					seen[key] = true
				}
			}
		}
		writeJSON(w, 200, list)
	case "/api/sql-file/cancel":
		id := str(p, "executionId")
		s.extensions.mu.Lock()
		job := s.extensions.jobs["sql:"+id]
		s.extensions.mu.Unlock()
		cancelled := false
		if job != nil {
			job.mu.Lock()
			if job.cancel != nil && !job.done {
				job.cancel()
				cancelled = true
			}
			job.mu.Unlock()
		}
		writeJSON(w, 200, cancelled)
	case "/api/sql-file/execute":
		s.startSQLFile(w, r, p)
	default:
		writeError(w, 501, "SQL file endpoint is not implemented: "+path)
	}
	return true
}
func (s *Server) startSQLFile(w http.ResponseWriter, r *http.Request, p map[string]any) {
	request := mapValue(p, "request")
	if boolValue(request, "skipRelationalConstraints") {
		writeError(w, 400, "Constraint bypass must be supplied explicitly as SQL; this executor preserves database integrity checks")
		return
	}
	c, err := s.database.ConnectionConfig(str(request, "connectionId"))
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	refs := compareStrings(p["filePaths"])
	if len(refs) == 0 {
		refs = []string{str(request, "filePath")}
	}
	if len(refs) > 64 {
		writeError(w, 400, "SQL package exceeds 64 files")
		return
	}
	statements := []string{}
	total := 0
	selected := map[string]bool{}
	for _, table := range compareList(request["selectedTables"]) {
		selected[sqlFileTableKey(table)] = true
	}
	for _, ref := range refs {
		source, e := s.sqlFileSource(ref)
		if e != nil {
			writeError(w, 400, e.Error())
			return
		}
		total += len(source.sql)
		if total > maxTransferBytes {
			writeError(w, 400, "SQL execution exceeds 32 MiB")
			return
		}
		parts, e := database.SplitStatementsForDialect(source.sql, c.Type)
		if e != nil {
			writeError(w, 400, e.Error())
			return
		}
		for _, part := range parts {
			if len(selected) > 0 {
				table := sqlFileTable(part)
				if table == nil {
					writeError(w, 400, "Selected-table import requires direct table DDL/DML; execute this script without a table filter")
					return
				}
				if !selected[sqlFileTableKey(table)] {
					continue
				}
			}
			statements = append(statements, part)
		}
	}
	if len(statements) > 100000 {
		writeError(w, 400, "SQL execution exceeds 100000 statements")
		return
	}
	id := str(request, "executionId")
	job, err := s.extensions.job("sql:"+id, id, "execution")
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	cooldown := numberValue(request, "partCooldownMs", 0)
	if cooldown < 0 || cooldown > 10000 {
		writeError(w, 400, "SQL cooldown must be between 0 and 10000 ms")
		return
	}
	job.mu.Lock()
	if job.started {
		job.mu.Unlock()
		writeError(w, 409, "SQL execution ID has already been used")
		return
	}
	job.started = true
	ctx, cancel := context.WithCancel(s.extensions.ctx)
	job.cancel = cancel
	job.mu.Unlock()
	work := func() {
		defer cancel()
		if str(request, "txnSessionId") == "" {
			defer func() {
				_, _ = s.database.Handle(context.Background(), "/api/query/close-client-session", map[string]any{"connectionId": c.ID, "database": request["database"], "clientSessionId": "sql-file-" + id})
			}()
		}
		start := time.Now()
		success, failure := 0, 0
		var affected int64
		progress := map[string]any{"executionId": id, "status": "running", "statementIndex": 0, "successCount": 0, "failureCount": 0, "affectedRows": 0, "elapsedMs": 0, "statementSummary": "", "bytesRead": 0, "totalBytes": total, "phase": "executing"}
		job.update(progress, false)
		for index, statement := range statements {
			payload := map[string]any{"connectionId": c.ID, "database": request["database"], "schema": request["schema"], "sql": statement, "executionId": id, "clientSessionId": "sql-file-" + id}
			var result any
			var executeErr error
			if txn := str(request, "txnSessionId"); txn != "" {
				payload["txnSessionId"] = txn
				result, executeErr = s.database.Handle(ctx, "/api/transaction/execute", payload)
			} else {
				result, executeErr = s.database.Handle(ctx, "/api/query/execute", payload)
			}
			progress["statementIndex"] = index + 1
			summary := []rune(statement)
			if len(summary) > 200 {
				summary = summary[:200]
			}
			progress["statementSummary"] = string(summary)
			if executeErr != nil {
				failure++
				progress["error"] = s.redactError(executeErr.Error(), payload)
			} else {
				success++
				switch value := result.(type) {
				case database.QueryResult:
					affected += value.AffectedRows
				case []database.QueryResult:
					for _, q := range value {
						affected += q.AffectedRows
					}
				}
			}
			progress["successCount"] = success
			progress["failureCount"] = failure
			progress["affectedRows"] = affected
			progress["elapsedMs"] = time.Since(start).Milliseconds()
			job.update(progress, false)
			if executeErr != nil && (!boolValue(request, "continueOnError") || ctx.Err() != nil) {
				progress["status"] = "error"
				if ctx.Err() != nil {
					progress["status"] = "cancelled"
				}
				job.update(progress, true)
				return
			}
			if cooldown > 0 {
				timer := time.NewTimer(time.Duration(cooldown) * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					progress["status"] = "cancelled"
					job.update(progress, true)
					return
				case <-timer.C:
				}
			}
		}
		progress["status"] = "done"
		if str(request, "txnSessionId") == "" && s.database.HasOpenTransaction(c.ID, "sql-file-"+id) {
			progress["status"] = "error"
			progress["error"] = "SQL file ended with an open transaction; uncommitted changes were rolled back"
		}
		if failure > 0 {
			progress["status"] = "error"
		}
		progress["bytesRead"] = total
		progress["elapsedMs"] = time.Since(start).Milliseconds()
		job.update(progress, true)
	}
	if !s.extensions.start(work) {
		cancel()
		job.update(map[string]any{"executionId": id, "status": "cancelled", "error": "Server is closing"}, true)
		writeError(w, 503, "Server is closing")
		return
	}
	writeJSON(w, 200, nil)
}
