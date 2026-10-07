// Package database provides the Go query engine used by both Wails and HTTP.
package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

var ErrUnsupported = errors.New("not implemented by the Go database engine")

type Config struct {
	// Raw retains engine-specific connection options for native non-SQL clients.
	Raw                 map[string]any   `json:"-"`
	ID                  string           `json:"id"`
	Name                string           `json:"name"`
	Type                string           `json:"db_type"`
	Host                string           `json:"host"`
	Port                int              `json:"port"`
	Username            string           `json:"username"`
	Password            string           `json:"password"`
	Database            string           `json:"database"`
	DefaultSchema       string           `json:"default_schema"`
	ConnectTimeout      int              `json:"connect_timeout_secs"`
	QueryTimeout        int              `json:"query_timeout_secs"`
	ReadOnly            bool             `json:"read_only"`
	Production          bool             `json:"is_production"`
	ProductionDatabases []string         `json:"production_databases"`
	SSL                 bool             `json:"ssl"`
	CACert              string           `json:"ca_cert_path"`
	ClientCert          string           `json:"client_cert_path"`
	ClientKey           string           `json:"client_key_path"`
	ConnectionString    string           `json:"connection_string"`
	URLParams           string           `json:"url_params"`
	InitScript          string           `json:"init_script"`
	DriverProfile       string           `json:"driver_profile"`
	Transports          []map[string]any `json:"transport_layers"`
	Attached            []struct {
		Name string `json:"name"`
		Path string `json:"path"`
	} `json:"attached_databases"`
}

type connection struct {
	config        Config
	pools         map[string]*sql.DB
	native        nativeDriver
	life          context.Context
	cancel        context.CancelFunc
	unlockedUntil time.Time
}
type runningQuery struct {
	cancel       context.CancelFunc
	connectionID string
}
type Manager struct {
	mu          sync.RWMutex
	connections map[string]*connection
	running     map[string]runningQuery
	closed      bool
}

func NewManager() *Manager {
	return &Manager{connections: map[string]*connection{}, running: map[string]runningQuery{}}
}
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	for _, q := range m.running {
		q.cancel()
	}
	connections := m.connections
	m.connections = map[string]*connection{}
	m.mu.Unlock()
	var errs []error
	for _, c := range connections {
		if c.cancel != nil {
			c.cancel()
		}
		if c.native != nil {
			errs = append(errs, c.native.Close())
		}
		for _, db := range c.pools {
			errs = append(errs, db.Close())
		}
	}
	return errors.Join(errs...)
}
func str(p map[string]any, names ...string) string {
	for _, n := range names {
		if v, ok := p[n]; ok && v != nil {
			return fmt.Sprint(v)
		}
	}
	return ""
}
func integer(p map[string]any, fallback int, names ...string) int {
	s := str(p, names...)
	if s == "" {
		return fallback
	}
	v, e := strconv.Atoi(s)
	if e != nil {
		return fallback
	}
	return v
}
func boolean(p map[string]any, names ...string) bool {
	b, _ := strconv.ParseBool(str(p, names...))
	return b
}
func decodeConfig(p map[string]any) (Config, error) {
	var c Config
	raw, ok := p["config"]
	if !ok {
		return c, errors.New("config is required")
	}
	b, e := json.Marshal(raw)
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(b, &c)
	c = c.normalized()
	return c, e
}
func unsupported(feature string) error { return fmt.Errorf("%w: %s", ErrUnsupported, feature) }
func (c Config) normalized() Config {
	if c.Type == "mariadb" {
		c.Type = "mysql"
	}
	return c
}
func (c Config) validate() error {
	switch c.Type {
	case "sqlite", "mysql", "postgres", "redis", "mongodb", "clickhouse":
	default:
		return unsupported("database type " + c.Type)
	}
	if c.Type == "sqlite" && hasEnabledTransports(c) {
		return unsupported("SSH transport on local SQLite")
	}
	if err := ValidateNetworkTransports(c); err != nil {
		return err
	}
	if c.Type == "sqlite" && (c.SSL || c.CACert != "" || c.ClientCert != "" || c.ClientKey != "") {
		return unsupported("TLS settings on SQLite")
	}
	if c.ConnectionString != "" || c.URLParams != "" && c.Type != "mongodb" {
		return unsupported("custom connection strings and URL parameters")
	}
	if c.InitScript != "" {
		return unsupported("connection initialization scripts")
	}
	if c.DriverProfile != "" && c.DriverProfile != "native" && c.DriverProfile != c.Type && !(c.Type == "mysql" && c.DriverProfile == "mariadb") {
		return unsupported("custom driver profiles")
	}
	if len(c.Attached) > 0 {
		return unsupported("attached database configuration")
	}
	if c.Type == "sqlite" && sqlitePath(c) == "" {
		return errors.New("SQLite database file path is required")
	}
	return nil
}
func sqlitePath(c Config) string {
	if strings.TrimSpace(c.Host) != "" {
		return c.Host
	}
	if c.Database == ":memory:" || strings.ContainsAny(c.Database, "/\\") || strings.HasSuffix(c.Database, ".sqlite") || strings.HasSuffix(c.Database, ".db") {
		return c.Database
	}
	return ""
}

func openDB(ctx context.Context, c Config, database string) (*sql.DB, error) {
	if e := c.validate(); e != nil {
		return nil, e
	}
	timeout := c.ConnectTimeout
	if timeout <= 0 {
		timeout = 15
	}
	if timeout > 300 {
		timeout = 300
	}
	connectCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	var driver, dsn string
	var db *sql.DB
	var e error
	var dial func(context.Context, string, string) (net.Conn, error)
	if c.Type != "sqlite" {
		dial, e = networkDialer(c)
		if e != nil {
			return nil, redact(e, c)
		}
	}
	switch c.Type {
	case "sqlite":
		driver = "sqlite"
		if sqlitePath(c) == ":memory:" {
			dsn = ":memory:"
		} else {
			p, e := filepath.Abs(sqlitePath(c))
			if e != nil {
				return nil, e
			}
			u := url.URL{Scheme: "file", Path: p}
			dsn = u.String()
		}
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn += sep + "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	case "mysql":
		driver = "mysql"
		port := c.Port
		if port == 0 {
			port = 3306
		}
		cfg := mysql.NewConfig()
		cfg.User = c.Username
		cfg.Passwd = c.Password
		cfg.Net = "tcp"
		cfg.Addr = net.JoinHostPort(c.Host, strconv.Itoa(port))
		cfg.DBName = database
		cfg.Timeout = time.Duration(timeout) * time.Second
		// Database clients preserve zero dates and timezone-free DATETIME text.
		cfg.ParseTime = false
		cfg.InterpolateParams = false
		cfg.MultiStatements = false
		cfg.TLS, e = databaseTLSConfig(c)
		if e != nil {
			return nil, redact(e, c)
		}
		cfg.DialFunc = dial
		connector, err := mysql.NewConnector(cfg)
		if err != nil {
			return nil, redact(err, c)
		}
		db = sql.OpenDB(connector)
	case "postgres":
		driver = "pgx"
		port := c.Port
		if port == 0 {
			port = 5432
		}
		u := url.URL{Scheme: "postgres", User: url.UserPassword(c.Username, c.Password), Host: net.JoinHostPort(c.Host, strconv.Itoa(port)), Path: "/" + database}
		q := u.Query()
		q.Set("sslmode", "disable")
		q.Set("standard_conforming_strings", "on")
		q.Set("connect_timeout", strconv.Itoa(timeout))
		u.RawQuery = q.Encode()
		dsn = u.String()
		pgConfig, err := pgx.ParseConfig(dsn)
		if err != nil {
			return nil, redact(err, c)
		}
		pgConfig.TLSConfig, e = databaseTLSConfig(c)
		if e != nil {
			return nil, redact(e, c)
		}
		pgConfig.Fallbacks = nil
		pgConfig.DialFunc = dial
		// The last SSH host resolves the database hostname, not the desktop DNS.
		if hasEnabledTransports(c) {
			pgConfig.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
		}
		db = stdlib.OpenDB(*pgConfig)
	}
	if db == nil {
		db, e = sql.Open(driver, dsn)
	}
	if e != nil {
		return nil, redact(e, c)
	}
	if c.Type == "sqlite" {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(8)
		db.SetMaxIdleConns(2)
		db.SetConnMaxIdleTime(5 * time.Minute)
	}
	if e = db.PingContext(connectCtx); e != nil {
		db.Close()
		return nil, redact(e, c)
	}
	return db, nil
}

type sanitizedError struct {
	message string
	cause   error
}

func (e sanitizedError) Error() string { return e.message }
func (e sanitizedError) Unwrap() error { return e.cause }
func redact(err error, c Config) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	secrets := []string{c.Password}
	for _, layer := range c.Transports {
		for _, name := range []string{"password", "key_passphrase", "token", "private_key"} {
			if value, ok := layer[name].(string); ok {
				secrets = append(secrets, value)
			}
		}
	}
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	if message == err.Error() {
		return err
	}
	return sanitizedError{message: message, cause: err}
}
func (m *Manager) Connect(ctx context.Context, c Config) error {
	c = c.normalized()
	if c.ID == "" {
		return errors.New("connection id is required")
	}
	if usesNativeDriver(c.Type) {
		return m.connectNative(ctx, c)
	}
	db, e := openDB(ctx, c, c.Database)
	if e != nil {
		return e
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		db.Close()
		return errors.New("database manager is closed")
	}
	old := m.connections[c.ID]
	for _, q := range m.running {
		if q.connectionID == c.ID {
			q.cancel()
		}
	}
	m.connections[c.ID] = &connection{config: c, pools: map[string]*sql.DB{c.Database: db}}
	m.mu.Unlock()
	if old != nil {
		if old.cancel != nil {
			old.cancel()
		}
		if old.native != nil {
			old.native.Close()
		}
		for _, pool := range old.pools {
			pool.Close()
		}
	}

	return nil
}
func (m *Manager) pool(ctx context.Context, id, database string) (*sql.DB, Config, bool, error) {
	m.mu.RLock()
	c := m.connections[id]
	if c == nil {
		m.mu.RUnlock()
		return nil, Config{}, false, errors.New("database connection is not open")
	}
	cfg := c.config
	if c.native != nil {
		m.mu.RUnlock()
		return nil, cfg, false, unsupported("SQL pool operations for " + cfg.Type)
	}
	effectiveDatabase := database
	if cfg.Type == "sqlite" {
		if database != "" && database != "main" && database != cfg.Database {
			m.mu.RUnlock()
			return nil, cfg, false, unsupported("SQLite attached database " + database)
		}
		effectiveDatabase = "main"
	} else if effectiveDatabase == "" {
		effectiveDatabase = cfg.Database
	}
	// SQL may qualify a different database than the selected tree node. Until
	// cross-database SQL analysis is available, any production marker locks
	// writes on the whole connection rather than allowing that bypass.
	locked := cfg.ReadOnly || cfg.Production || len(cfg.ProductionDatabases) > 0
	for _, name := range cfg.ProductionDatabases {
		if name == effectiveDatabase {
			locked = true
		}
	}
	locked = locked && !time.Now().Before(c.unlockedUntil)
	if cfg.Type == "sqlite" || database == "" {
		database = cfg.Database
	}
	db := c.pools[database]
	m.mu.RUnlock()
	if db != nil {
		return db, cfg, locked, nil
	}
	db, e := openDB(ctx, cfg, database)
	if e != nil {
		return nil, cfg, locked, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.connections[id] != c {
		db.Close()
		return nil, cfg, locked, errors.New("connection was closed while connecting")
	}
	if existing := c.pools[database]; existing != nil {
		db.Close()
		db = existing
	} else {
		c.pools[database] = db
	}
	return db, cfg, locked, nil
}
func (m *Manager) Handle(ctx context.Context, path string, p map[string]any) (any, error) {
	if result, handled, err := m.handleNative(ctx, path, p); handled {
		return result, err
	}
	switch path {
	case "/api/connection/test", "/api/connection/test-info":
		c, e := decodeConfig(p)
		if e != nil {
			return nil, e
		}
		if usesNativeDriver(c.Type) {
			return testNative(ctx, c, path == "/api/connection/test-info")
		}
		db, e := openDB(ctx, c, c.Database)
		if e != nil {
			return nil, e
		}
		defer db.Close()
		if path == "/api/connection/test" {
			return "Connection successful", nil
		}
		info, e := databaseInfo(ctx, db, c)
		return map[string]any{"message": "Connection successful", "databaseInfo": info}, e
	case "/api/connection/connect":
		c, e := decodeConfig(p)
		if e != nil {
			return nil, e
		}
		if e = m.Connect(ctx, c); e != nil {
			return nil, e
		}
		return c.ID, nil
	case "/api/connection/is-open":
		m.mu.RLock()
		defer m.mu.RUnlock()
		return m.connections[str(p, "connectionId", "connection_id")] != nil, nil
	case "/api/connection/disconnect":
		id := str(p, "connectionId", "connection_id")
		m.mu.Lock()
		c := m.connections[id]
		delete(m.connections, id)
		for _, q := range m.running {
			if q.connectionID == id {
				q.cancel()
			}
		}
		m.mu.Unlock()
		if c != nil {
			if c.cancel != nil {
				c.cancel()
			}
			if c.native != nil {
				c.native.Close()
			}
			for _, db := range c.pools {
				db.Close()
			}
		}
		return nil, nil
	case "/api/connection/write-unlock", "/api/connection/write-unlock/lock", "/api/connection/write-unlock/state":
		m.mu.Lock()
		defer m.mu.Unlock()
		c := m.connections[str(p, "connectionId", "connection_id")]
		if c == nil {
			return nil, errors.New("database connection is not open")
		}
		if path == "/api/connection/write-unlock" {
			seconds := integer(p, 0, "durationSecs")
			if seconds < 1 || seconds > 3600 {
				return nil, errors.New("unlock duration must be between 1 and 3600 seconds")
			}
			c.unlockedUntil = time.Now().Add(time.Duration(seconds) * time.Second)
		}
		if strings.HasSuffix(path, "/lock") {
			c.unlockedUntil = time.Time{}
			return nil, nil
		}
		remaining := time.Until(c.unlockedUntil).Milliseconds()
		if remaining < 0 {
			remaining = 0
		}
		return map[string]any{"remainingMs": remaining}, nil
	case "/api/connection/identifier-quote", "/api/connection/database-info", "/api/connection/check-health", "/api/connection/prewarm":
		db, c, _, e := m.pool(ctx, str(p, "connectionId", "connection_id"), str(p, "database"))
		if e != nil {
			return nil, e
		}
		switch path {
		case "/api/connection/identifier-quote":
			if c.Type == "mysql" {
				return "`", nil
			}
			return "\"", nil
		case "/api/connection/database-info":
			return databaseInfo(ctx, db, c)
		default:
			return nil, redact(db.PingContext(ctx), c)
		}
	case "/api/query/cancel":
		m.mu.RLock()
		q, ok := m.running[str(p, "executionId")]
		m.mu.RUnlock()
		if ok {
			q.cancel()
		}
		return ok, nil
	case "/api/query/close-session", "/api/query/close-client-session":
		// Results are bounded and materialized; this engine never retains a cursor.
		return false, nil
	case "/api/query/execute", "/api/query/execute-multi", "/api/query/execute-batch", "/api/query/execute-in-transaction", "/api/query/execute-script":
		return m.execute(ctx, path, p)
	default:
		if strings.HasPrefix(path, "/api/schema/") {
			return m.schema(ctx, path, p)
		}
		return nil, unsupported(path)
	}
}
func databaseInfo(ctx context.Context, db *sql.DB, c Config) (map[string]any, error) {
	info := map[string]any{"currentDatabase": c.Database, "driverName": "Go database/sql", "unquotedIdentifierCase": "mixed", "quotedIdentifierCase": "mixed"}
	var version string
	var e error
	switch c.Type {
	case "sqlite":
		info["productName"] = "SQLite"
		info["currentDatabase"] = "main"
		e = db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&version)
	case "mysql":
		info["productName"] = "MySQL"
		e = db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version)
	case "postgres":
		info["productName"] = "PostgreSQL"
		info["unquotedIdentifierCase"] = "lower"
		e = db.QueryRowContext(ctx, "SHOW server_version").Scan(&version)
	}
	if e != nil {
		return nil, redact(e, c)
	}
	info["productVersion"] = version
	return info, nil
}

func hasEnabledTransports(c Config) bool {
	for _, layer := range c.Transports {
		if layer["enabled"] != false {
			return true
		}
	}
	return false
}
