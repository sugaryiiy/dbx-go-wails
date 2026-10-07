package database

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// clickHouse uses the HTTP protocol only. Each request has a private query ID;
// neither server sessions nor client-controlled server settings are retained.
type clickHouse struct {
	config    Config
	base      url.URL
	client    *http.Client
	transport *http.Transport
	mu        sync.Mutex
	closed    bool
	running   map[string]context.CancelFunc
}

func openClickHouse(ctx context.Context, c Config) (nativeDriver, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(c.Host) == "" || strings.ContainsAny(c.Host, "/?#@\\\r\n") {
		return nil, errors.New("ClickHouse requires a plain HTTP server hostname")
	}
	for _, key := range []string{"protocol", "clickhouse_protocol"} {
		if v := str(c.Raw, key); v != "" && v != "http" && !(v == "https" && c.SSL) {
			return nil, unsupported("ClickHouse supports only HTTP/HTTPS; enable TLS for HTTPS")
		}
	}
	port := c.Port
	if port == 0 {
		port = 8123
		if c.SSL {
			port = 8443
		}
	}
	if port < 1 || port > 65535 {
		return nil, errors.New("ClickHouse port must be between 1 and 65535")
	}
	tlsConfig, err := databaseTLSConfig(c)
	if err != nil {
		return nil, err
	}
	dial, err := networkDialer(c)
	if err != nil {
		return nil, err
	}
	timeout := c.ConnectTimeout
	if timeout <= 0 {
		timeout = 15
	}
	if timeout > 300 {
		timeout = 300
	}
	boundedDial := func(ctx context.Context, network, address string) (net.Conn, error) {
		dialCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
		return dial(dialCtx, network, address)
	}
	transport := &http.Transport{DialContext: boundedDial, TLSClientConfig: tlsConfig, TLSHandshakeTimeout: time.Duration(timeout) * time.Second, IdleConnTimeout: 90 * time.Second, MaxIdleConnsPerHost: 4, MaxConnsPerHost: 8, DisableCompression: true}
	scheme := "http"
	if c.SSL {
		scheme = "https"
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("ClickHouse redirects are not supported")
	}}
	h := &clickHouse{config: c, base: url.URL{Scheme: scheme, Host: net.JoinHostPort(strings.Trim(c.Host, "[]"), strconv.Itoa(port)), Path: "/"}, client: client, transport: transport, running: map[string]context.CancelFunc{}}
	pingCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	if err := h.Ping(pingCtx); err != nil {
		h.Close()
		return nil, redact(err, c)
	}
	return h, nil
}
func (h *clickHouse) Close() error {
	h.mu.Lock()
	h.closed = true
	for _, cancel := range h.running {
		cancel()
	}
	h.mu.Unlock()
	h.transport.CloseIdleConnections()
	return nil
}
func (h *clickHouse) Ping(ctx context.Context) error {
	_, err := h.query(ctx, h.config.Database, "SELECT 1", true, 1, 0, 1024, nil)
	return err
}
func (h *clickHouse) Info(ctx context.Context) (map[string]any, error) {
	r, err := h.query(ctx, h.config.Database, "SELECT version(), currentDatabase()", true, 1, 0, 1024, nil)
	if err != nil {
		return nil, err
	}
	if len(r.Rows) != 1 || len(r.Rows[0]) != 2 {
		return nil, errors.New("invalid ClickHouse server information")
	}
	return map[string]any{"productName": "ClickHouse", "productVersion": r.Rows[0][0], "currentDatabase": r.Rows[0][1], "driverName": "Go ClickHouse HTTP", "unquotedIdentifierCase": "mixed", "quotedIdentifierCase": "mixed"}, nil
}
func (h *clickHouse) Handle(ctx context.Context, path string, p map[string]any, locked bool) (any, error) {
	if strings.HasPrefix(path, "/api/schema/") {
		return h.schema(ctx, path, p)
	}
	switch path {
	case "/api/connection/identifier-quote":
		return "`", nil
	case "/api/connection/database-info":
		return h.Info(ctx)
	case "/api/connection/check-health", "/api/connection/prewarm":
		return nil, h.Ping(ctx)
	case "/api/query/execute", "/api/query/execute-multi", "/api/query/execute-script", "/api/query/execute-batch":
		return h.execute(ctx, path, p, locked)
	case "/api/query/execute-in-transaction":
		return nil, unsupported("ClickHouse atomic transactions")
	}
	return nil, unsupported(path)
}
func (h *clickHouse) execute(ctx context.Context, path string, p map[string]any, locked bool) (any, error) {
	if (boolean(p, "useTransaction") || boolean(p, "preserveExplicitTransaction")) || str(p, "resultSessionId") != "" {
		return nil, unsupported("ClickHouse transactions and retained result cursors")
	}
	if mode := str(p, "executionMode"); mode != "" && mode != "simple" {
		return nil, unsupported("ClickHouse execution mode " + mode)
	}
	if str(p, "schema") != "" && str(p, "schema") != str(p, "database") && str(p, "schema") != h.config.Database {
		return nil, unsupported("ClickHouse has databases, not independent schemas")
	}
	sources := []string{str(p, "sql")}
	if raw, ok := p["statements"]; ok {
		sources = nil
		switch v := raw.(type) {
		case []string:
			sources = v
		case []any:
			for _, item := range v {
				s, ok := item.(string)
				if !ok {
					return nil, errors.New("statements must contain strings")
				}
				sources = append(sources, s)
			}
		default:
			return nil, errors.New("statements must be an array")
		}
	}
	protected := locked || boolean(p, "readOnly")
	statements := []string{}
	for _, source := range sources {
		parts, err := clickHouseStatements(source, protected)
		if err != nil {
			return nil, err
		}
		statements = append(statements, parts...)
	}
	if len(statements) == 0 || len(statements) > 100 {
		return nil, errors.New("ClickHouse requires 1–100 SQL statements")
	}
	if path == "/api/query/execute" && len(statements) != 1 {
		return nil, errors.New("execute requires exactly one SQL statement")
	}
	// Atomic batch is not provided by ClickHouse; the caller must explicitly use
	// sequential script/multi execution and accept partial writes on failure.
	if path == "/api/query/execute-batch" {
		return nil, unsupported("ClickHouse atomic batches; use execute-multi or execute-script for sequential statements")
	}
	timeout := integer(p, h.config.QueryTimeout, "timeoutSecs")
	if timeout < 0 || timeout > 86400 {
		return nil, errors.New("timeoutSecs must be between 0 and 86400")
	}
	if timeout == 0 {
		timeout = 300
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	rows := integer(p, 10000, "maxRows")
	if rows <= 0 {
		rows = 10000
	}
	if rows > 100000 {
		rows = 100000
	}
	if page := integer(p, 0, "pageSize"); page > 0 && page < rows {
		rows = page
	}
	offset := integer(p, 0, "rowOffset")
	if offset < 0 || offset > 10000000 {
		return nil, errors.New("rowOffset must be between 0 and 10000000")
	}
	bytes := integer(p, 16<<20, "maxResultBytes")
	if bytes <= 0 || bytes > 64<<20 {
		bytes = 16 << 20
	}
	database := str(p, "database")
	if database == "" {
		database = h.config.Database
	}
	results := make([]QueryResult, 0, len(statements))
	aggregate := emptyResult()
	start := time.Now()
	for i, s := range statements {
		if offset > 0 && !returnsRows(s, "clickhouse") {
			return nil, errors.New("rowOffset requires a query returning rows")
		}
		r, err := h.query(ctx, database, s, protected, rows, offset, bytes, nil)
		r.StatementIndex = i
		if err != nil {
			if !boolean(p, "continueOnError") {
				return nil, redact(err, h.config)
			}
			r = emptyResult()
			r.StatementIndex = i
			r.ExecutionError = true
			r.Error = map[string]any{"message": redact(err, h.config).Error(), "kind": "database"}
		}
		results = append(results, r)
		aggregate.AffectedRows += r.AffectedRows
	}
	aggregate.ExecutionTimeMS = time.Since(start).Milliseconds()
	if path == "/api/query/execute-multi" {
		return results, nil
	}
	if path == "/api/query/execute" {
		return results[0], nil
	}
	return aggregate, nil
}

func (h *clickHouse) request(ctx context.Context, statement string, params url.Values) (*http.Response, error) {
	endpoint := h.base
	endpoint.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(statement))
	if err != nil {
		return nil, err
	}
	username := h.config.Username
	if username == "" {
		username = "default"
	}
	req.SetBasicAuth(username, h.config.Password)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	return h.client.Do(req)
}
func (h *clickHouse) kill(queryID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// IDs are generated locally, not interpolated from user input.
	params := url.Values{"param_id": {queryID}, "wait_end_of_query": {"1"}}
	response, err := h.request(ctx, "KILL QUERY WHERE query_id = {id:String} SYNC", params)
	if err == nil {
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		response.Body.Close()
	}
}
func (h *clickHouse) query(ctx context.Context, database, statement string, protected bool, maxRows, offset, maxBytes int, extra url.Values) (QueryResult, error) {
	result := emptyResult()
	start := time.Now()
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return result, err
	}
	id := "dbx-" + hex.EncodeToString(idBytes)
	ctx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		cancel()
		return result, errors.New("ClickHouse connection is closed")
	}
	h.running[id] = cancel
	h.mu.Unlock()
	done := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			h.kill(id)
		case <-done:
		}
	}()
	defer func() {
		close(done)
		<-watchDone
		cancel()
		h.mu.Lock()
		delete(h.running, id)
		h.mu.Unlock()
	}()
	params := url.Values{"query_id": {id}, "default_format": {"JSONCompact"}, "output_format_json_quote_64bit_integers": {"1"}, "output_format_json_quote_decimals": {"1"}, "output_format_json_quote_denormals": {"1"}, "max_result_rows": {strconv.Itoa(maxRows + offset + 1)}, "result_overflow_mode": {"break"}, "cancel_http_readonly_queries_on_client_close": {"1"}}
	if database != "" {
		params.Set("database", database)
	}
	if protected {
		params.Set("readonly", "1")
	}
	if deadline, ok := ctx.Deadline(); ok {
		seconds := int(time.Until(deadline).Seconds()) + 1
		if seconds < 1 {
			seconds = 1
		}
		params.Set("max_execution_time", strconv.Itoa(seconds))
	}
	for key, values := range extra {
		params[key] = values
	}
	response, err := h.request(ctx, statement, params)
	if err != nil {
		return result, redact(err, h.config)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
		return result, redact(fmt.Errorf("ClickHouse HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body))), h.config)
	}
	if response.Header.Get("X-ClickHouse-Exception-Code") != "" {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
		return result, redact(fmt.Errorf("ClickHouse query failed: %s", strings.TrimSpace(string(body))), h.config)
	}
	result, err = decodeClickHouse(response.Body, maxRows, offset, maxBytes)
	if result.Truncated {
		cancel()
	}
	result.ExecutionTimeMS = time.Since(start).Milliseconds()
	if err == nil && response.Header.Get("X-ClickHouse-Summary") != "" {
		var summary struct {
			WrittenRows string `json:"written_rows"`
		}
		if json.Unmarshal([]byte(response.Header.Get("X-ClickHouse-Summary")), &summary) == nil {
			result.AffectedRows, _ = strconv.ParseInt(summary.WrittenRows, 10, 64)
		}
	}
	return result, redact(err, h.config)
}

func decodeClickHouse(body io.Reader, maxRows, offset, maxBytes int) (QueryResult, error) {
	result := emptyResult()
	// Bound both JSON allocation and network consumption, including a single
	// oversized value. Metadata has a separate 1 MiB allowance.
	reader := &io.LimitedReader{R: body, N: int64(maxBytes) + (1 << 20)}
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	token, err := decoder.Token()
	if err == io.EOF {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if token != json.Delim('{') {
		return result, errors.New("invalid ClickHouse JSON response")
	}
	totalBytes, seen := 0, 0
	for decoder.More() {
		name, err := decoder.Token()
		if err != nil {
			return result, err
		}
		switch name {
		case "meta":
			var meta []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			}
			if err := decoder.Decode(&meta); err != nil {
				return result, err
			}
			for _, column := range meta {
				result.Columns = append(result.Columns, column.Name)
				result.ColumnTypes = append(result.ColumnTypes, column.Type)
			}
		case "data":
			token, err := decoder.Token()
			if err != nil || token != json.Delim('[') {
				return result, errors.New("invalid ClickHouse row data")
			}
			for decoder.More() {
				var row []any
				if err := decoder.Decode(&row); err != nil {
					if reader.N == 0 {
						return result, errors.New("ClickHouse result value exceeds the response byte limit")
					}
					return result, err
				}
				if len(row) != len(result.Columns) {
					return result, errors.New("invalid ClickHouse row width")
				}
				if seen < offset {
					seen++
					continue
				}
				if len(result.Rows) >= maxRows {
					result.Truncated = true
					return result, nil
				}
				encoded, _ := json.Marshal(row)
				if totalBytes+len(encoded) > maxBytes {
					result.Truncated = true
					return result, nil
				}
				totalBytes += len(encoded)
				for i, value := range row {
					row[i] = clickHouseValue(value, result.ColumnTypes[i])
				}
				result.Rows = append(result.Rows, row)
			}
			if _, err := decoder.Token(); err != nil {
				return result, err
			}
		case "exception":
			var message string
			if err := decoder.Decode(&message); err != nil {
				return result, err
			}
			return result, errors.New("ClickHouse query failed: " + message)
		default:
			var ignored json.RawMessage
			if err := decoder.Decode(&ignored); err != nil {
				return result, err
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return result, err
	}
	// ClickHouse can append an exception after sending HTTP 200. A successful
	// result requires clean EOF, so a late server error cannot masquerade as data.
	if _, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return result, err
		}
		return result, errors.New("unexpected trailing ClickHouse response")
	}
	return result, nil
}
func clickHouseValue(value any, typ string) any {
	switch v := value.(type) {
	case json.Number:
		if strings.Contains(typ, "Decimal") {
			return v.String()
		}
		if n, err := strconv.ParseInt(v.String(), 10, 64); err == nil {
			return normalizeValue(n, typ)
		}
		if n, err := strconv.ParseUint(v.String(), 10, 64); err == nil {
			return normalizeValue(n, typ)
		}
		if strings.ContainsAny(v.String(), ".eE") {
			if n, err := strconv.ParseFloat(v.String(), 64); err == nil {
				return normalizeValue(n, typ)
			}
		}
		return v.String()
	case []any:
		for i, item := range v {
			v[i] = clickHouseValue(item, typ)
		}
		return v
	case map[string]any:
		for key, item := range v {
			v[key] = clickHouseValue(item, typ)
		}
		return v
	default:
		return v
	}
}
