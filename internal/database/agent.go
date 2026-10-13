package database

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sugaryiiy/dbx-go-wails/internal/agent"
)

func (m *Manager) SetAgentStore(store *agent.Store) { m.mu.Lock(); m.agentStore = store; m.mu.Unlock() }
func (m *Manager) openNative(ctx context.Context, c Config) (nativeDriver, error) {
	if !agent.UsesDatabase(c.Type) {
		return openNative(ctx, c)
	}
	if err := validateAgentConfig(c); err != nil {
		return nil, err
	}
	m.mu.RLock()
	store := m.agentStore
	m.mu.RUnlock()
	if store == nil {
		return nil, unsupported("agent store is not configured")
	}
	seconds := c.ConnectTimeout
	if seconds <= 0 {
		seconds = 15
	}
	if seconds > 300 {
		seconds = 300
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	key, ok := agentPackageKey(c)
	if !ok {
		return nil, unsupported("driver profile " + c.DriverProfile)
	}
	params := agentConnectParams(c)
	session, err := store.Open(ctx, key, params)
	if err != nil {
		return nil, redact(err, c)
	}
	return &agentDriver{store: store, session: session, config: c, sessionGate: make(chan struct{}, 1), sessions: map[string]*agent.Session{}, cursors: map[string]agentCursor{}}, nil
}
func (m *Manager) testNative(ctx context.Context, c Config, withInfo bool) (any, error) {
	driver, err := m.openNative(ctx, c)
	if err != nil {
		return nil, err
	}
	defer driver.Close()
	if !withInfo {
		return "Connection successful", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	info, err := driver.Info(ctx)
	return map[string]any{"message": "Connection successful", "databaseInfo": info}, redact(err, c)
}
func validateAgentConfig(c Config) error {
	if _, ok := agentPackageKey(c); !ok {
		return unsupported("database type " + c.Type)
	}
	if hasEnabledTransports(c) {
		return unsupported("SSH transport for agent database connections")
	}
	if len(c.Attached) > 0 {
		return unsupported("agent attached database configuration")
	}
	if strings.TrimSpace(c.InitScript) != "" {
		return unsupported("agent connection initialization scripts")
	}
	if c.Type == "mq" {
		auth := str(mapValue(mapValue(c.Raw["external_config"])["auth"]), "kind")
		if auth != "" && auth != "none" && auth != "basic" {
			return unsupported("MQ agent authentication " + auth)
		}
		if c.CACert != "" || c.ClientCert != "" || c.ClientKey != "" {
			return unsupported("MQ direct certificate fields; configure the installed agent TLS properties")
		}
	}
	return nil
}
func agentConnectParams(c Config) map[string]any {
	if c.Type == "mq" {
		return map[string]any{"connection": mqAgentConnectParams(c)}
	}
	params := map[string]any{}
	for k, v := range c.Raw {
		params[k] = v
	}
	port := c.Port
	if port == 0 {
		port = agent.DefaultPort(c.Type)
	}
	for k, v := range map[string]any{"host": c.Host, "port": port, "database": c.Database, "username": c.Username, "password": c.Password, "connection_string": c.ConnectionString, "url_params": c.URLParams, "ssl": c.SSL, "ca_cert_path": c.CACert, "client_cert_path": c.ClientCert, "client_key_path": c.ClientKey, "connect_timeout_secs": c.ConnectTimeout, "driver_profile": c.DriverProfile, "database_type": c.Type, "sessionRole": "workload"} {
		params[k] = v
	}
	if c.ConnectTimeout <= 0 {
		params["connect_timeout_secs"] = 15
	}
	// Preserve local H2 database file semantics and its legacy driver aliases.
	if c.Type == "h2" && c.Port == 0 && c.Host != "" && c.ConnectionString == "" {
		file := c.Host
		file = strings.TrimSuffix(strings.TrimSuffix(file, ".mv.db"), ".h2.db")
		params["connection_string"] = "jdbc:h2:file:" + file + ";AUTO_SERVER=TRUE"
		params["database"] = "file:" + file
		params["host"] = ""
		params["port"] = 0
	}
	if c.Type == "etcd" {
		if value := str(c.Raw, "etcd_endpoints"); value == "" {
			params["etcd_endpoints"] = c.Host
		}
	}
	if c.Type == "zookeeper" {
		params["zookeeper_connect_string"] = c.Host
	}
	if c.Type == "oracle" && (boolean(c.Raw, "sysdba") || strings.EqualFold(c.Username, "sys")) {
		params["sysdba"] = true
		if c.Database != "" && !strings.HasPrefix(strings.ToUpper(c.Database), "SYSDBA:") {
			params["database"] = "SYSDBA:" + c.Database
		}
	}
	return params
}

type agentDriver struct {
	store       *agent.Store
	session     *agent.Session
	config      Config
	sessionGate chan struct{}
	sessions    map[string]*agent.Session
	cursors     map[string]agentCursor
	closed      bool
}

func (d *agentDriver) Ping(ctx context.Context) error {
	if d.config.Type == "mq" {
		return d.session.Call(ctx, "test_connection", agentConnectParams(d.config), nil)
	}
	method := "validate_connection"
	if d.session.Runtime.Handshake.Supports("multi_session") {
		method = "validate_session"
	}
	return d.session.Call(ctx, method, map[string]any{}, nil)
}
func (d *agentDriver) Info(ctx context.Context) (map[string]any, error) {
	if d.config.Type == "mq" {
		info, err := d.mqCluster(ctx)
		return map[string]any{"productName": mqKind(d.config), "productVersion": info["serverVersion"], "driverName": "Standalone Agent", "mq": info}, err
	}
	var info map[string]any
	err := d.session.Call(ctx, "connection_info", map[string]any{}, &info)
	if nested, ok := info["databaseInfo"].(map[string]any); ok {
		return nested, err
	}
	return info, err
}
func (d *agentDriver) IdentifierQuote(ctx context.Context) (string, error) {
	var info map[string]any
	err := d.session.Call(ctx, "connection_info", map[string]any{}, &info)
	if err != nil {
		return "", err
	}
	if quote, ok := info["identifierQuote"].(string); ok && quote != "" {
		return quote, nil
	}
	return "", unsupported("agent identifier quote")
}
func (d *agentDriver) Handle(ctx context.Context, path string, p map[string]any, locked bool) (any, error) {
	if strings.HasPrefix(path, "/api/mq/") {
		return d.handleMQ(ctx, path, p, locked)
	}
	params := map[string]any{}
	for k, v := range p {
		params[k] = v
	}
	if str(params, "database") == "" {
		params["database"] = d.config.Database
	}
	if str(params, "schema") == "" {
		params["schema"] = d.config.DefaultSchema
	}
	for _, name := range []string{"limit", "offset", "maxRows", "pageSize", "fetchSize", "rowOffset", "timeoutSecs"} {
		if _, ok := params[name]; ok {
			params[name] = integer(params, 0, name)
		}
	}
	if path == "/api/query/close-client-session" {
		return d.closeClientSession(str(params, "database"), str(params, "clientSessionId"))
	}
	if path == "/api/query/close-session" {
		return d.closeCursor(ctx, str(params, "sessionId"))
	}
	session := d.session
	if strings.HasPrefix(path, "/api/query/") || strings.HasPrefix(path, "/api/schema/") {
		var err error
		session, err = d.sessionFor(ctx, path, params)
		if err != nil {
			return nil, err
		}
	}
	methods := map[string]string{"databases": "list_databases", "database-metadata": "list_databases", "schemas": "list_schemas", "tables": "list_tables", "objects": "list_objects", "completion-objects": "list_objects", "columns": "get_columns", "indexes": "list_indexes", "foreign-keys": "list_foreign_keys", "triggers": "list_triggers", "constraints": "list_constraints", "partitions": "list_partitions", "subpartitions": "list_subpartitions", "table-partition-status": "get_table_partition_status", "table-partitioning": "get_table_partitioning", "ddl": "get_table_ddl", "data-types": "list_data_types", "object-source": "get_object_source", "completion-assistant": "completion_assistant_search_v1"}
	if strings.HasPrefix(path, "/api/schema/") {
		method, ok := methods[strings.TrimPrefix(path, "/api/schema/")]
		if !ok {
			return nil, unsupported(path)
		}
		if method == "get_object_source" {
			params["name"] = str(p, "table", "name")
		}
		var result any
		err := session.Call(ctx, method, params, &result)
		return result, err
	}
	switch path {
	case "/api/query/execute", "/api/query/execute-multi", "/api/query/execute-batch", "/api/query/execute-script", "/api/query/execute-in-transaction":
		return d.execute(ctx, path, params, session, locked)
	case "/api/query/get-explain-info":
		if _, err := ValidateMCPSQL(str(p, "sql"), "postgres", true, false); err != nil {
			return nil, err
		}
		var result any
		err := session.Call(ctx, "get_explain_info", params, &result)
		if info, ok := result.(map[string]any); ok {
			if plan, ok := info["plan"].(string); ok {
				return plan, err
			}
		}
		return result, err
	case "/api/query/begin-manual-transaction", "/api/query/commit-manual-transaction", "/api/query/rollback-manual-transaction":
		if locked && path != "/api/query/rollback-manual-transaction" {
			return nil, errors.New("writes are locked; unlock writes before opening an agent transaction")
		}
		if !session.Runtime.Handshake.Supports("transaction") {
			return nil, unsupported("agent manual transactions")
		}
		method := strings.TrimPrefix(path, "/api/query/")
		method = strings.ReplaceAll(method, "-", "_")
		var result any
		err := session.Call(ctx, method, params, &result)
		return result, err
	}
	if strings.HasPrefix(path, "/api/etcd/") || strings.HasPrefix(path, "/api/zookeeper/") {
		return d.handleKV(ctx, path, params, locked)
	}
	return nil, unsupported(path)
}
func (d *agentDriver) execute(ctx context.Context, path string, p map[string]any, session *agent.Session, locked bool) (any, error) {
	mode := str(p, "executionMode")
	if mode != "" && mode != "simple" {
		return nil, unsupported("agent execution mode " + mode)
	}
	if sessionID := str(p, "resultSessionId"); sessionID != "" {
		if path != "/api/query/execute" {
			return nil, errors.New("cursor fetch requires single execute route")
		}
		p["sessionId"] = sessionID
		if locked && !d.cursorIsReadOnly(sessionID) {
			return nil, errors.New("writes are locked; close this cursor and run a verified read-only query")
		}
		var result map[string]any
		err := session.Call(ctx, "fetch_query_page", p, &result)
		return result, err
	}
	dialect := "postgres"
	if d.config.Type == "doris" || d.config.Type == "starrocks" || d.config.Type == "oceanbase" {
		dialect = "mysql"
	}
	statements := []string{}
	if raw, ok := p["statements"]; ok {
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var list []string
		if err = json.Unmarshal(b, &list); err != nil {
			return nil, errors.New("statements must be strings")
		}
		for _, source := range list {
			parts, err := SplitStatementsForDialect(source, dialect)
			if err != nil {
				return nil, err
			}
			statements = append(statements, parts...)
		}
	} else {
		var err error
		statements, err = SplitStatementsForDialect(str(p, "sql"), dialect)
		if err != nil {
			return nil, err
		}
	}
	if len(statements) == 0 || len(statements) > 1000 {
		return nil, errors.New("agent query requires 1–1000 statements")
	}
	if path == "/api/query/execute" && len(statements) != 1 {
		return nil, errors.New("execute requires one statement")
	}
	for _, statement := range statements {
		if locked {
			if strings.ContainsAny(statement, "[]") {
				return nil, errors.New("bracket identifiers are unavailable while writes are locked")
			}
			if _, err := ValidateMCPSQL(statement, dialect, true, false); err != nil {
				return nil, errors.New("writes are locked; the agent accepts only verified read-only SQL until explicitly unlocked")
			}
		}
	}
	maxRows := integer(p, 10000, "maxRows")
	if maxRows < 1 || maxRows > 100000 {
		return nil, errors.New("maxRows must be between 1 and 100000")
	}
	p["maxRows"] = maxRows
	if path == "/api/query/execute" {
		method := "execute_query"
		if integer(p, 0, "pageSize") > 0 && session.Runtime.Handshake.Supports("paged_query") {
			if integer(p, 0, "pageSize") > 10000 {
				return nil, errors.New("pageSize exceeds 10000")
			}
			method = "execute_query_page"
		}
		var result map[string]any
		err := session.Call(ctx, method, p, &result)
		if err == nil {
			readonly := false
			if !strings.ContainsAny(statements[0], "[]") {
				_, guardErr := ValidateMCPSQL(statements[0], dialect, true, false)
				readonly = guardErr == nil
			}
			d.rememberCursor(session, result, readonly)
		}
		return result, err
	}
	if path == "/api/query/execute-in-transaction" || boolean(p, "useTransaction") {
		if !session.Runtime.Handshake.Supports("transaction") {
			return nil, unsupported("agent transactions")
		}
		p["statements"] = statements
		var result map[string]any
		err := session.Call(ctx, "execute_transaction", p, &result)
		return result, err
	}
	if path == "/api/query/execute-multi" {
		results := []map[string]any{}
		for index, statement := range statements {
			p["sql"] = statement
			var result map[string]any
			if err := session.Call(ctx, "execute_query", p, &result); err != nil {
				return nil, err
			}
			result["statement_index"] = index
			results = append(results, result)
		}
		return results, nil
	}
	p["statements"] = statements
	var result map[string]any
	err := session.Call(ctx, "execute_batch", p, &result)
	return result, err
}
func (d *agentDriver) handleKV(ctx context.Context, path string, p map[string]any, locked bool) (any, error) {
	name := strings.TrimPrefix(strings.TrimPrefix(path, "/api/etcd/"), "/api/zookeeper/")
	if name == "supports-ttl" {
		return d.session.Runtime.Handshake.Supports("kv_ttl"), nil
	}
	methods := map[string]string{"list-prefix": "kv_list_prefix", "get": "kv_get", "put": "kv_put", "delete": "kv_delete", "rename": "kv_rename", "history": "kv_history", "status": "kv_status", "compact": "etcd_compact", "defrag": "etcd_defrag", "watch/start": "etcd_watch_start", "watch/poll": "etcd_watch_poll", "watch/stop": "etcd_watch_stop", "lease/list": "etcd_lease_list"}
	read := name == "list-prefix" || name == "get" || name == "history" || name == "status" || strings.HasPrefix(name, "watch/") || name == "lease/list"
	if request, ok := p["request"].(map[string]any); ok {
		for k, v := range request {
			p[k] = v
		}
	}
	method, ok := methods[name]
	if name == "lease/call" || name == "auth/call" {
		operation := str(p, "operation")
		if params, ok := p["params"].(map[string]any); ok {
			for k, v := range params {
				p[k] = v
			}
		}
		allowed := map[string]bool{"lease_get": true, "lease_grant": true, "lease_keepalive_once": true, "lease_revoke": true, "auth_user_list": true, "auth_user_get": true, "auth_user_add": true, "auth_user_delete": true, "auth_user_change_password": true, "auth_user_grant_role": true, "auth_user_revoke_role": true, "auth_role_list": true, "auth_role_get": true, "auth_role_add": true, "auth_role_delete": true, "auth_role_grant_permission": true, "auth_role_revoke_permission": true}
		operation = strings.TrimPrefix(operation, "etcd_")
		if !allowed[operation] {
			return nil, unsupported("etcd operation " + operation)
		}
		method = "etcd_" + operation
		ok = true
		read = strings.HasSuffix(operation, "_list") || strings.HasSuffix(operation, "_get")
	}
	if !ok {
		return nil, unsupported(path)
	}
	if locked && !read {
		return nil, errors.New("writes are locked; unlock writes before modifying keys or etcd administration")
	}
	var result any
	err := d.session.Call(ctx, method, p, &result)
	return result, err
}
