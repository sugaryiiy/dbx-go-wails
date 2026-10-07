package database

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	redis "github.com/redis/go-redis/v9"
)

const redisPreviewLimit = 64 << 10
const redisRawLimit = 8 << 20
const redisPageLimit = 500

// Each operation owns its client and SELECT state. Closing a request or the
// driver therefore closes its sockets (and SSH chains) without affecting another
// database's SELECT or leaving a blocked read running in the background.
type redisDriver struct {
	config  Config
	options redis.Options
	mu      sync.Mutex
	clients map[*redis.Client]context.CancelFunc
	closed  bool
}

func init() { nativeOpeners["redis"] = openRedis }

func validateRedis(c Config) error {
	mode := str(c.Raw, "redis_connection_mode")
	if mode != "" && mode != "standalone" {
		return unsupported("Redis " + mode + " mode")
	}
	for _, name := range []string{"redis_sentinel_master", "redis_sentinel_nodes", "redis_sentinel_username", "redis_sentinel_password", "redis_cluster_nodes"} {
		if str(c.Raw, name) != "" {
			return unsupported("Redis option " + name)
		}
	}
	if boolean(c.Raw, "redis_sentinel_tls") || boolean(c.Raw, "redis_tls_insecure") {
		return unsupported("Redis Sentinel or insecure TLS")
	}
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("Redis host is required")
	}
	_, err := redisDBIndex(c.Database)
	return err
}

func redisDBIndex(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 65535 {
		return 0, errors.New("Redis database must be an integer between 0 and 65535")
	}
	return n, nil
}

func openRedis(ctx context.Context, c Config) (nativeDriver, error) {
	if err := validateRedis(c); err != nil {
		return nil, err
	}
	db, _ := redisDBIndex(c.Database)
	tlsConfig, err := databaseTLSConfig(c)
	if err != nil {
		return nil, err
	}
	dial, err := networkDialer(c)
	if err != nil {
		return nil, err
	}
	port := c.Port
	if port == 0 {
		port = 6379
	}
	if port < 1 || port > 65535 {
		return nil, errors.New("invalid Redis port")
	}
	timeout := c.ConnectTimeout
	if timeout <= 0 {
		timeout = 15
	}
	if timeout > 300 {
		timeout = 300
	}
	opts := redis.Options{Addr: net.JoinHostPort(strings.Trim(c.Host, "[]"), strconv.Itoa(port)), Username: c.Username, Password: c.Password, DB: db, Protocol: 2, MaxRetries: -1, PoolSize: 1, DialTimeout: time.Duration(timeout) * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, ContextTimeoutEnabled: true, DisableIdentity: true}
	opts.Dialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if tlsConfig != nil {
			secure := tls.Client(conn, tlsConfig.Clone())
			if err = secure.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, err
			}
			conn = secure
		}
		return newRedisBoundedConn(conn), nil
	}
	d := &redisDriver{config: c, options: opts, clients: make(map[*redis.Client]context.CancelFunc)}
	if err = d.Ping(ctx); err != nil {
		d.Close()
		return nil, err
	}
	// Reject a cluster endpoint even when its saved configuration says standalone.
	client, callCtx, done, err := d.client(ctx, db)
	if err != nil {
		d.Close()
		return nil, err
	}
	modeInfo, infoErr := client.Info(callCtx, "cluster").Result()
	done()
	if infoErr == nil && strings.Contains(modeInfo, "cluster_enabled:1") {
		d.Close()
		return nil, unsupported("Redis cluster endpoint")
	}
	return d, nil
}

func (d *redisDriver) client(ctx context.Context, db int) (*redis.Client, context.Context, func(), error) {
	ctx, cancel := context.WithCancel(ctx)
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		cancel()
		return nil, ctx, nil, errors.New("Redis connection is closed")
	}
	opts := d.options
	opts.DB = db
	client := redis.NewClient(&opts)
	d.clients[client] = cancel
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	d.mu.Unlock()
	done := func() {
		stop()
		cancel()
		_ = client.Close()
		d.mu.Lock()
		delete(d.clients, client)
		d.mu.Unlock()
	}
	return client, ctx, done, nil
}
func (d *redisDriver) Close() error {
	d.mu.Lock()
	d.closed = true
	clients := d.clients
	d.clients = make(map[*redis.Client]context.CancelFunc)
	d.mu.Unlock()
	for client, cancel := range clients {
		cancel()
		_ = client.Close()
	}
	return nil
}
func (d *redisDriver) Ping(ctx context.Context) error {
	client, ctx, done, err := d.client(ctx, d.options.DB)
	if err != nil {
		return err
	}
	defer done()
	return client.Ping(ctx).Err()
}
func (d *redisDriver) Info(ctx context.Context) (map[string]any, error) {
	client, ctx, done, err := d.client(ctx, d.options.DB)
	if err != nil {
		return nil, err
	}
	defer done()
	raw, err := client.Info(ctx, "server").Result()
	if err != nil {
		return nil, err
	}
	version := ""
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "redis_version:") {
			version = strings.TrimSpace(strings.TrimPrefix(line, "redis_version:"))
		}
	}
	return map[string]any{"productName": "Redis", "productVersion": version, "currentDatabase": strconv.Itoa(d.options.DB), "driverName": "go-redis"}, nil
}

func redisBlob(value string) map[string]any {
	encoding := "binary"
	if utf8.ValidString(value) {
		encoding = "utf8"
	}
	return map[string]any{"raw_base64": base64.StdEncoding.EncodeToString([]byte(value)), "encoding": encoding}
}
func redisDisplay(value string) string {
	if utf8.ValidString(value) {
		return strings.ReplaceAll(value, "\\", "\\\\")
	}
	var out strings.Builder
	for _, b := range []byte(value) {
		if b == '\\' {
			out.WriteString("\\\\")
		} else if b >= 32 && b <= 126 {
			out.WriteByte(b)
		} else {
			fmt.Fprintf(&out, "\\x%02x", b)
		}
	}
	return out.String()
}
func redisKey(p map[string]any, name string) (string, error) {
	raw, ok := p[name].(string)
	if !ok {
		return "", fmt.Errorf("%s is required and must be base64", name)
	}
	if len(raw) > redisRawLimit*2 {
		return "", errors.New("Redis key exceeds size limit")
	}
	b, err := base64.StdEncoding.Strict().DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("invalid %s base64 encoding", name)
	}
	return string(b), nil
}
func redisRequiredText(p map[string]any, name string) (string, error) {
	s, ok := p[name].(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", name)
	}
	if len(s) > redisRawLimit {
		return "", fmt.Errorf("%s exceeds the 8 MiB limit", name)
	}
	return s, nil
}
func redisInt(p map[string]any, name string, fallback int64) (int64, error) {
	if p[name] == nil {
		return fallback, nil
	}
	n, err := strconv.ParseInt(str(p, name), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return n, nil
}
func redisPageCount(p map[string]any) (int64, error) {
	n, err := redisInt(p, "count", 100)
	if err != nil || n < 1 || n > redisPageLimit {
		return 0, fmt.Errorf("count must be between 1 and %d", redisPageLimit)
	}
	return n, nil
}

func (d *redisDriver) Handle(ctx context.Context, route string, p map[string]any, locked bool) (result any, err error) {
	route = strings.TrimPrefix(route, "/api/redis/")
	reads := map[string]bool{"list-databases": true, "scan-keys": true, "scan-keys-batch": true, "get-value": true, "get-raw-value": true, "get-ttl": true, "load-more": true, "check-json-module": true}
	writes := map[string]bool{"set-string": true, "delete-key": true, "delete-keys": true, "rename-key": true, "hash-set": true, "hash-del": true, "hash-field-update": true, "list-push": true, "list-set": true, "list-remove": true, "set-add": true, "set-remove": true, "zadd": true, "zrem": true, "zset-update": true, "set-ttl": true, "set-expire-at": true, "set-keys-ttl": true, "set-keys-expire-at": true}
	if !reads[route] && !writes[route] && route != "execute-command" {
		return nil, unsupported("Redis " + route)
	}
	if writes[route] && locked {
		return nil, errors.New("writes are locked for this read-only or production connection")
	}
	db, err := redisDBIndex(str(p, "db"))
	if err != nil {
		return nil, err
	}
	if p["db"] == nil {
		db = d.options.DB
	}
	client, ctx, done, err := d.client(ctx, db)
	if err != nil {
		return nil, err
	}
	defer done()
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	switch route {
	case "list-databases":
		return redisListDBs(ctx, client, d.options.DB)
	case "scan-keys", "scan-keys-batch":
		return redisScan(ctx, client, p, route == "scan-keys-batch")
	case "execute-command":
		return redisExecute(ctx, client, p, locked)
	case "check-json-module":
		return false, nil // RedisJSON is deliberately not advertised until its routes are migrated.
	case "delete-keys", "set-keys-ttl", "set-keys-expire-at":
		return redisBatchWrite(ctx, client, route, p)
	}
	key, err := redisKey(p, "keyRaw")
	if err != nil {
		return nil, err
	}
	switch route {
	case "get-value":
		return redisGetValue(ctx, client, key)
	case "get-ttl":
		return client.Do(ctx, "TTL", key).Int64()
	case "get-raw-value":
		val, e := client.GetRange(ctx, key, 0, redisRawLimit).Result()
		if e != nil {
			return nil, e
		}
		if len(val) > redisRawLimit {
			return nil, errors.New("Redis raw value exceeds the 8 MiB download limit")
		}
		return redisBlob(val), nil
	case "load-more":
		return redisCollection(ctx, client, key, str(p, "keyType"), p, false)
	default:
		return redisWrite(ctx, client, route, key, p)
	}
}

func redisListDBs(ctx context.Context, client *redis.Client, selected int) ([]map[string]any, error) {
	counts := map[int]int64{}
	raw, err := client.Info(ctx, "keyspace").Result()
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(raw, "\n") {
		name, fields, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || !strings.HasPrefix(name, "db") {
			continue
		}
		db, e := strconv.Atoi(strings.TrimPrefix(name, "db"))
		if e != nil {
			continue
		}
		for _, field := range strings.Split(fields, ",") {
			if strings.HasPrefix(field, "keys=") {
				n, e := strconv.ParseInt(strings.TrimPrefix(field, "keys="), 10, 64)
				if e == nil {
					counts[db] = n
				}
			}
		}
	}
	// Restricted ACL accounts can browse their configured/non-empty databases
	// without granting CONFIG access. Never fabricate an assumed DB count.
	settings, e := client.ConfigGet(ctx, "databases").Result()
	if e == nil {
		n, _ := strconv.Atoi(settings["databases"])
		if n > 65536 {
			return nil, errors.New("Redis database count exceeds supported limit")
		}
		for db := 0; db < n; db++ {
			if _, ok := counts[db]; !ok {
				counts[db] = 0
			}
		}
	}
	if _, ok := counts[selected]; !ok {
		counts[selected] = 0
	}
	indices := make([]int, 0, len(counts))
	for db := range counts {
		indices = append(indices, db)
	}
	sort.Ints(indices)
	result := make([]map[string]any, 0, len(indices))
	for _, db := range indices {
		result = append(result, map[string]any{"db": db, "keys": counts[db]})
	}
	return result, nil
}

func redisScan(ctx context.Context, client *redis.Client, p map[string]any, batch bool) (any, error) {
	cursor, err := redisInt(p, "cursor", 0)
	if err != nil || cursor < 0 {
		return nil, errors.New("cursor must be a nonnegative integer")
	}
	count, err := redisInt(p, "count", 1000)
	if err != nil || count < 1 || count > 10000 {
		return nil, errors.New("Redis scan count must be between 1 and 10000")
	}
	iterations := int64(1)
	if batch {
		iterations, err = redisInt(p, "maxIterations", 1)
		if err != nil || iterations < 1 || iterations > 1000 {
			return nil, errors.New("maxIterations must be between 1 and 1000")
		}
	}
	pattern := str(p, "pattern")
	if pattern == "" {
		pattern = "*"
	}
	include := p["includeTypes"] != false
	keys := []map[string]any{}
	seen := map[string]bool{}
	cur := uint64(cursor)
	for i := int64(0); i < iterations; i++ {
		values, next, e := client.Scan(ctx, cur, pattern, count).Result()
		if e != nil {
			return nil, e
		}
		cur = next
		// TYPE and TTL travel in bounded pipelines; a 1000-key page should not
		// require 2000 network round trips over a remote SSH connection.
		for start := 0; start < len(values); start += 500 {
			end := start + 500
			if end > len(values) {
				end = len(values)
			}
			batch := values[start:end]
			kinds := make([]*redis.StatusCmd, len(batch))
			ttls := make([]*redis.Cmd, len(batch))
			if include {
				_, err := client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
					for index, key := range batch {
						kinds[index] = pipe.Type(ctx, key)
						ttls[index] = pipe.Do(ctx, "TTL", key)
					}
					return nil
				})
				if err != nil {
					return nil, err
				}
			}
			for index, key := range batch {
				if seen[key] {
					continue
				}
				seen[key] = true
				item := map[string]any{"key_display": redisDisplay(key), "key_raw": base64.StdEncoding.EncodeToString([]byte(key))}
				if include {
					item["key_type"] = kinds[index].Val()
					ttl, err := ttls[index].Int64()
					if err != nil {
						return nil, err
					}
					item["ttl"] = ttl
				}
				keys = append(keys, item)
			}
		}
		// SCAN COUNT is a hint, so never truncate its returned batch and lose keys.
		if cur == 0 || len(keys) >= int(count) {
			break
		}
	}
	total, err := client.DBSize(ctx).Result()
	if err != nil {
		return nil, err
	}
	return map[string]any{"cursor": cur, "keys": keys, "total_keys": total}, nil
}

func redisGetValue(ctx context.Context, client *redis.Client, key string) (any, error) {
	kind, err := client.Type(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if kind == "none" {
		return nil, errors.New("Redis key no longer exists")
	}
	ttl, err := client.Do(ctx, "TTL", key).Int64()
	if err != nil {
		return nil, err
	}
	var data any
	if kind == "string" {
		val, e := client.GetRange(ctx, key, 0, redisPreviewLimit).Result()
		if e != nil {
			return nil, e
		}
		total, e := client.StrLen(ctx, key).Result()
		if e != nil {
			return nil, e
		}
		truncated := len(val) > redisPreviewLimit
		if truncated {
			val = val[:redisPreviewLimit]
		}
		data = map[string]any{"kind": "string", "content": redisBlob(val), "total_bytes": total, "truncated": truncated}
	} else if kind == "list" || kind == "hash" || kind == "set" || kind == "zset" {
		data, err = redisCollection(ctx, client, key, kind, map[string]any{}, true)
		if err != nil {
			return nil, err
		}
	} else {
		data = map[string]any{"kind": "unknown", "redis_type": kind}
	}
	return map[string]any{"key_display": redisDisplay(key), "key_raw": base64.StdEncoding.EncodeToString([]byte(key)), "ttl": ttl, "redis_type": kind, "data": data}, nil
}

func redisCollection(ctx context.Context, client *redis.Client, key, kind string, p map[string]any, initial bool) (any, error) {
	count, err := redisPageCount(p)
	if err != nil {
		return nil, err
	}
	cursor, err := redisInt(p, "cursor", 0)
	if err != nil || cursor < 0 {
		return nil, errors.New("cursor must be a nonnegative integer")
	}
	if kind != "list" && kind != "hash" && kind != "set" && kind != "zset" {
		return nil, unsupported("Redis collection type " + kind)
	}
	filter := str(p, "filter")
	if filter != "" {
		return nil, unsupported("Redis collection value filtering")
	}
	items := []map[string]any{}
	var total int64
	var next uint64
	switch kind {
	case "list":
		total, err = client.LLen(ctx, key).Result()
		if err != nil {
			return nil, err
		}
		vals, e := client.LRange(ctx, key, cursor, cursor+count-1).Result()
		if e != nil {
			return nil, e
		}
		for i, v := range vals {
			items = append(items, map[string]any{"index": cursor + int64(i), "value": redisBlob(v)})
		}
		if cursor+int64(len(vals)) < total {
			next = uint64(cursor) + uint64(len(vals))
		}
	case "zset":
		total, err = client.ZCard(ctx, key).Result()
		if err != nil {
			return nil, err
		}
		var vals []redis.Z
		if str(p, "sortDirection") == "desc" {
			vals, err = client.ZRevRangeWithScores(ctx, key, cursor, cursor+count-1).Result()
		} else {
			vals, err = client.ZRangeWithScores(ctx, key, cursor, cursor+count-1).Result()
		}
		if err != nil {
			return nil, err
		}
		for _, v := range vals {
			items = append(items, map[string]any{"score": redisScoreText(v.Score), "member": redisBlob(fmt.Sprint(v.Member))})
		}
		if cursor+int64(len(vals)) < total {
			next = uint64(cursor) + uint64(len(vals))
		}
	case "hash":
		total, err = client.HLen(ctx, key).Result()
		if err != nil {
			return nil, err
		}
		vals, n, e := client.HScan(ctx, key, uint64(cursor), filter, count).Result()
		if e != nil {
			return nil, e
		}
		next = n
		for i := 0; i+1 < len(vals); i += 2 {
			items = append(items, map[string]any{"field": redisBlob(vals[i]), "value": redisBlob(vals[i+1])})
		}
	case "set":
		total, err = client.SCard(ctx, key).Result()
		if err != nil {
			return nil, err
		}
		vals, n, e := client.SScan(ctx, key, uint64(cursor), filter, count).Result()
		if e != nil {
			return nil, e
		}
		next = n
		for _, v := range vals {
			items = append(items, map[string]any{"member": redisBlob(v)})
		}
	}
	result := map[string]any{"kind": kind, "items": items}
	if next > 0 {
		result["scan_cursor"] = next
	}
	if initial {
		result["total"] = total
	}
	return result, nil
}
