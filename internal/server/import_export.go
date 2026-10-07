package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

func (s *Server) handleFileTransfer(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	path := r.URL.Path
	if path == "/api/import/preview" && r.Method == "POST" {
		s.previewUpload(w, r)
		return true
	}
	if path == "/api/import/source/release" && r.Method == "POST" {
		s.extensions.mu.Lock()
		_, exists := s.extensions.sources[str(p, "sourceRef")]
		delete(s.extensions.sources, str(p, "sourceRef"))
		s.extensions.mu.Unlock()
		writeJSON(w, 200, map[string]any{"released": exists})
		return true
	}
	if path == "/api/import/execute" && r.Method == "POST" {
		s.startImport(w, r, mapValue(p, "request"))
		return true
	}
	if path == "/api/export/query-result" && r.Method == "POST" {
		s.startExport(w, r, mapValue(p, "request"))
		return true
	}
	for _, spec := range []struct{ prefix, kind string }{{"/api/import/progress/", "import"}, {"/api/export/query-result/progress/", "export"}} {
		if strings.HasPrefix(path, spec.prefix) && r.Method == "GET" {
			id := strings.TrimPrefix(path, spec.prefix)
			j, err := s.extensions.job(spec.kind+":"+id, id, spec.kind)
			if err != nil {
				writeError(w, 400, err.Error())
			} else {
				s.streamJob(w, r, j)
			}
			return true
		}
	}
	if strings.HasPrefix(path, "/api/export/query-result/download/") && r.Method == "GET" {
		id := strings.TrimPrefix(path, "/api/export/query-result/download/")
		s.extensions.mu.Lock()
		j := s.extensions.jobs["export:"+id]
		s.extensions.mu.Unlock()
		if j == nil {
			writeError(w, 404, "Export not found")
			return true
		}
		j.mu.Lock()
		defer j.mu.Unlock()
		if !j.done || j.progress["status"] != "done" {
			writeError(w, 409, "Export is not complete")
			return true
		}
		w.Header().Set("Content-Type", j.contentType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": j.filename}))
		w.Header().Set("Content-Length", strconv.Itoa(len(j.data)))
		w.WriteHeader(200)
		_, _ = w.Write(j.data)
		return true
	}
	if (path == "/api/import/cancel" || path == "/api/export/query-result/cancel") && r.Method == "POST" {
		kind := "export"
		if path == "/api/import/cancel" {
			kind = "import"
		}
		s.extensions.mu.Lock()
		j := s.extensions.jobs[kind+":"+str(p, kind+"Id")]
		s.extensions.mu.Unlock()
		if j == nil {
			writeError(w, 404, "Transfer not found")
			return true
		}
		j.mu.Lock()
		cancel := j.cancel
		done := j.done
		j.mu.Unlock()
		if cancel != nil && !done {
			cancel()
		}
		writeJSON(w, 200, nil)
		return true
	}
	return false
}

func (s *Server) previewUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTransferBytes)
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		writeError(w, 400, "Invalid upload or file exceeds 32 MiB")
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, "Upload a CSV, TSV, or XLSX file")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxTransferBytes+1))
	if err != nil || len(data) > maxTransferBytes {
		writeError(w, 413, "Upload exceeds 32 MiB")
		return
	}
	opts := map[string]any{}
	if raw := r.FormValue("parseOptions"); raw != "" {
		if json.Unmarshal([]byte(raw), &opts) != nil {
			writeError(w, 400, "Invalid parse options")
			return
		}
	}
	format := r.FormValue("sourceFormat")
	if format == "" {
		format = strings.TrimPrefix(strings.ToLower(filepath.Ext(header.Filename)), ".")
	}
	columns, rows, sheets, err := parseTransferFile(data, format, opts)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	id := transferID()
	fingerprint := sha256.Sum256(data)
	s.extensions.mu.Lock()
	for key, source := range s.extensions.sources {
		if time.Since(source.created) > 30*time.Minute {
			delete(s.extensions.sources, key)
		}
	}
	if len(s.extensions.sources) >= 16 {
		s.extensions.mu.Unlock()
		writeError(w, 429, "Release previous previews before uploading another file")
		return
	}
	s.extensions.sources[id] = &importSource{columns: columns, rows: rows, created: time.Now()}
	s.extensions.mu.Unlock()
	limit := 100
	if len(rows) < limit {
		limit = len(rows)
	}
	writeJSON(w, 200, map[string]any{"fileName": filepath.Base(header.Filename), "filePath": id, "sourceRef": id, "fileType": format, "sizeBytes": len(data), "columns": columns, "rows": rows[:limit], "totalRows": len(rows), "totalRowsExact": true, "sourceFingerprint": hex.EncodeToString(fingerprint[:]), "effectiveEncoding": "utf8", "sheets": sheets})
}

func parseTransferFile(data []byte, format string, opts map[string]any) ([]string, [][]any, []string, error) {
	if encoding := str(opts, "encoding"); encoding != "" && encoding != "auto" && encoding != "utf8" {
		return nil, nil, nil, errors.New("Only UTF-8 imports are currently supported")
	}
	var records [][]string
	var sheets []string
	if format == "xlsx" || format == "excel" {
		book, err := excelize.OpenReader(bytes.NewReader(data), excelize.Options{UnzipSizeLimit: 64 << 20, UnzipXMLSizeLimit: 16 << 20})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("Invalid XLSX workbook: %w", err)
		}
		defer book.Close()
		sheets = book.GetSheetList()
		if len(sheets) == 0 {
			return nil, nil, nil, errors.New("Workbook has no sheets")
		}
		name := str(opts, "sheetName")
		if name == "" {
			index := numberValue(opts, "sheetIndex", 0)
			if index < 0 || index >= len(sheets) {
				return nil, nil, nil, errors.New("Invalid worksheet index")
			}
			name = sheets[index]
		}
		iterator, err := book.Rows(name)
		if err != nil {
			return nil, nil, nil, err
		}
		defer iterator.Close()
		for iterator.Next() {
			cells, err := iterator.Columns(excelize.Options{RawCellValue: true})
			if err != nil {
				return nil, nil, nil, err
			}
			records = append(records, cells)
			if len(records) > maxTransferRows+1 {
				return nil, nil, nil, errors.New("Import exceeds 100,000 rows")
			}
		}
		if err := iterator.Error(); err != nil {
			return nil, nil, nil, err
		}
	} else if format == "csv" || format == "tsv" || format == "delimited" {
		if !utf8.Valid(data) {
			return nil, nil, nil, errors.New("File must use UTF-8 encoding")
		}
		reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\ufeff")))
		reader.FieldsPerRecord = -1
		if format == "tsv" {
			reader.Comma = '\t'
		}
		if delimiter := str(opts, "delimiter"); delimiter != "" {
			runes := []rune(delimiter)
			if len(runes) != 1 {
				return nil, nil, nil, errors.New("Delimiter must be one character")
			}
			reader.Comma = runes[0]
		}
		for {
			record, err := reader.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, nil, nil, fmt.Errorf("Invalid delimited file: %w", err)
			}
			records = append(records, record)
			if len(records) > maxTransferRows+1 {
				return nil, nil, nil, errors.New("Import exceeds 100,000 rows")
			}
		}
	} else {
		return nil, nil, nil, errors.New("Only CSV, TSV and XLSX imports are implemented")
	}
	if len(records) == 0 {
		return nil, nil, nil, errors.New("The file is empty")
	}
	start := 0
	hasHeader := true
	if v, ok := opts["hasHeader"].(bool); ok {
		hasHeader = v
	}
	var columns []string
	if hasHeader {
		titleRow := numberValue(opts, "titleRow", 1) - 1
		if titleRow < 0 || titleRow >= len(records) {
			return nil, nil, nil, errors.New("Invalid header row")
		}
		columns = records[titleRow]
		start = titleRow + 1
	} else {
		for i := range records[0] {
			columns = append(columns, fmt.Sprintf("Column%d", i+1))
		}
	}
	seen := map[string]bool{}
	for _, column := range columns {
		if column == "" || seen[column] {
			return nil, nil, nil, errors.New("Column headers must be non-empty and unique")
		}
		seen[column] = true
	}
	if len(columns) == 0 || len(columns) > 2000 {
		return nil, nil, nil, errors.New("Invalid number of columns")
	}
	start = numberValue(opts, "dataStartRow", start+1) - 1
	end := numberValue(opts, "lastDataRow", len(records))
	if start < 0 || start > len(records) || end < start || end > len(records) {
		return nil, nil, nil, errors.New("Invalid import row range")
	}
	nullLiteral := "\\N"
	if v, ok := opts["nullLiteral"].(string); ok {
		nullLiteral = v
	}
	rows := make([][]any, 0, end-start)
	for _, record := range records[start:end] {
		if len(record) > len(columns) {
			return nil, nil, nil, errors.New("A row has more fields than the header")
		}
		row := make([]any, len(columns))
		for i := range row {
			// XLSX omits trailing empty cells; preserve their empty-string value
			// unless the import explicitly maps empty cells to SQL NULL.
			cell := ""
			if i < len(record) {
				cell = record[i]
			}
			if boolValue(opts, "trimValues") {
				cell = strings.TrimSpace(cell)
			}
			if (nullLiteral != "" && cell == nullLiteral) || (cell == "" && boolValue(opts, "emptyStringAsNull")) {
				row[i] = nil
			} else {
				row[i] = cell
			}
		}
		rows = append(rows, row)
	}
	return columns, rows, sheets, nil
}

func (s *Server) startImport(w http.ResponseWriter, r *http.Request, p map[string]any) {
	if boolValue(p, "createTable") || boolValue(p, "skipDuplicateRows") || (str(p, "conflictPolicy") != "" && str(p, "conflictPolicy") != "error") {
		writeError(w, 501, "Create-table and conflict policies have not yet been migrated")
		return
	}
	s.extensions.mu.Lock()
	source := s.extensions.sources[str(p, "sourceRef")]
	s.extensions.mu.Unlock()
	if source == nil {
		writeError(w, 400, "Upload and preview the source file before importing")
		return
	}
	mappings, ok := p["mappings"].([]any)
	if !ok || len(mappings) == 0 {
		writeError(w, 400, "Select import column mappings")
		return
	}
	var columns []string
	var indices []int
	seen := map[string]bool{}
	for _, value := range mappings {
		mapping, ok := value.(map[string]any)
		if !ok {
			writeError(w, 400, "Invalid mapping")
			return
		}
		target := str(mapping, "targetColumn")
		if target == "" || seen[target] {
			writeError(w, 400, "Target columns must be unique")
			return
		}
		seen[target] = true
		index := -1
		for i, name := range source.columns {
			if name == str(mapping, "sourceColumn") {
				index = i
				break
			}
		}
		if index < 0 {
			writeError(w, 400, "Source column not found")
			return
		}
		columns = append(columns, target)
		indices = append(indices, index)
	}
	rows := make([][]any, len(source.rows))
	for i, row := range source.rows {
		rows[i] = make([]any, len(indices))
		for j, index := range indices {
			rows[i][j] = row[index]
		}
	}
	id := str(p, "importId")
	job, err := s.extensions.job("import:"+id, id, "import")
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithCancel(s.extensions.ctx)
	job.mu.Lock()
	if job.started {
		job.mu.Unlock()
		cancel()
		writeError(w, 409, "Import ID has already been used")
		return
	}
	job.started = true
	job.cancel = cancel
	job.mu.Unlock()
	if !s.extensions.start(func() {
		defer cancel()
		started := time.Now()
		count, err := s.database.ImportRows(ctx, p, columns, rows)
		progress := map[string]any{"status": "done", "phase": "done", "totalRows": len(rows), "rowsImported": count, "elapsedMs": time.Since(started).Milliseconds()}
		if err != nil {
			progress["status"] = "error"
			progress["error"] = s.redactError(err.Error(), p)
			if ctx.Err() != nil {
				progress["status"] = "cancelled"
			}
		}
		job.update(progress, true)
		if !boolValue(p, "retainSource") {
			s.extensions.mu.Lock()
			delete(s.extensions.sources, str(p, "sourceRef"))
			s.extensions.mu.Unlock()
		}
	}) {
		cancel()
		writeError(w, 503, "Application is closing")
		return
	}
	writeJSON(w, 200, map[string]any{"importId": id})
}

type exportResult struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	Truncated bool     `json:"truncated"`
}

func (s *Server) startExport(w http.ResponseWriter, r *http.Request, p map[string]any) {
	format := str(p, "format")
	if format != "csv" && format != "xlsx" && format != "json" && format != "txt" {
		writeError(w, 501, "Only CSV, XLSX, JSON and TXT exports are implemented")
		return
	}
	if setup, ok := p["setupSql"].([]any); ok && len(setup) > 0 {
		writeError(w, 501, "Exports with setup SQL are not yet supported")
		return
	}
	if cols, ok := p["selectedColumns"].([]any); ok && len(cols) > 0 {
		writeError(w, 501, "Selected-column export is not yet supported")
		return
	}
	id := str(p, "exportId")
	job, err := s.extensions.job("export:"+id, id, "export")
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithCancel(s.extensions.ctx)
	job.mu.Lock()
	if job.started {
		job.mu.Unlock()
		cancel()
		writeError(w, 409, "Export ID has already been used")
		return
	}
	job.started = true
	job.cancel = cancel
	job.mu.Unlock()
	if !s.extensions.start(func() {
		defer cancel()
		limit := numberValue(p, "rowLimit", maxTransferRows)
		if limit <= 0 || limit > maxTransferRows {
			limit = maxTransferRows
		}
		args := map[string]any{"connectionId": p["connectionId"], "database": p["database"], "schema": p["schema"], "sql": p["sql"], "maxRows": limit, "timeoutSecs": p["timeoutSecs"], "readOnly": true}
		result, err := s.database.Handle(ctx, "/api/query/execute", args)
		var qr exportResult
		if err == nil {
			data, e := json.Marshal(result)
			err = e
			if e == nil {
				decoder := json.NewDecoder(bytes.NewReader(data))
				decoder.UseNumber()
				err = decoder.Decode(&qr)
			}
		}
		if err == nil && qr.Truncated && numberValue(p, "rowLimit", 0) <= 0 {
			err = errors.New("Export exceeds 100,000 rows; set an explicit row limit or narrow the query")
		}
		var data []byte
		var contentType string
		if err == nil {
			data, contentType, err = encodeTransfer(qr, format, p)
		}
		if err == nil && len(data) > maxTransferBytes {
			err = errors.New("Export exceeds 32 MiB; narrow the query")
		}
		if err != nil {
			status := "error"
			if ctx.Err() != nil {
				status = "cancelled"
			}
			job.update(map[string]any{"status": status, "errorMessage": s.redactError(err.Error(), p)}, true)
			return
		}
		if ctx.Err() != nil {
			job.update(map[string]any{"status": "cancelled"}, true)
			return
		}
		name := filepath.Base(strings.ReplaceAll(str(p, "filePath"), "\\", "/"))
		if name == "" || name == "." {
			name = "query-result." + format
		}
		job.mu.Lock()
		job.data = data
		job.contentType = contentType
		job.filename = name
		job.mu.Unlock()
		job.update(map[string]any{"status": "done", "rowsExported": len(qr.Rows), "totalRows": len(qr.Rows)}, true)
	}) {
		cancel()
		writeError(w, 503, "Application is closing")
		return
	}
	writeJSON(w, 200, map[string]any{"exportId": id})
}
func encodeTransfer(q exportResult, format string, p map[string]any) ([]byte, string, error) {
	nullLiteral := "\\N"
	if v, ok := p["nullLiteral"].(string); ok {
		nullLiteral = v
	}
	if format == "json" {
		objects := make([]map[string]any, len(q.Rows))
		for i, row := range q.Rows {
			objects[i] = map[string]any{}
			for j, col := range q.Columns {
				if j < len(row) {
					objects[i][col] = row[j]
				}
			}
		}
		data, err := json.MarshalIndent(objects, "", "  ")
		return data, "application/json; charset=utf-8", err
	}
	if format == "xlsx" {
		book := excelize.NewFile()
		defer book.Close()
		for j, col := range q.Columns {
			cell, _ := excelize.CoordinatesToCellName(j+1, 1)
			if err := book.SetCellStr("Sheet1", cell, col); err != nil {
				return nil, "", err
			}
		}
		for i, row := range q.Rows {
			for j, value := range row {
				cell, _ := excelize.CoordinatesToCellName(j+1, i+2)
				var err error
				if value == nil {
					err = book.SetCellStr("Sheet1", cell, nullLiteral)
				} else {
					switch v := value.(type) {
					case string:
						err = book.SetCellStr("Sheet1", cell, v)
					case json.Number:
						err = book.SetCellStr("Sheet1", cell, string(v))
					default:
						err = book.SetCellValue("Sheet1", cell, value)
					}
				}
				if err != nil {
					return nil, "", err
				}
			}
		}
		buf, err := book.WriteToBuffer()
		if err != nil {
			return nil, "", err
		}
		return buf.Bytes(), "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", nil
	}
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	if format == "txt" {
		writer.Comma = '\t'
	}
	if err := writer.Write(q.Columns); err != nil {
		return nil, "", err
	}
	for _, row := range q.Rows {
		record := make([]string, len(q.Columns))
		for i := range record {
			if i < len(row) && row[i] != nil {
				record[i] = fmt.Sprint(row[i])
			} else {
				record[i] = nullLiteral
			}
		}
		if err := writer.Write(record); err != nil {
			return nil, "", err
		}
	}
	writer.Flush()
	contentType := "text/csv; charset=utf-8"
	if format == "txt" {
		contentType = "text/plain; charset=utf-8"
	}
	return buffer.Bytes(), contentType, writer.Error()
}
