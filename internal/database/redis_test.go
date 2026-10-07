package database

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"
)

func TestRedisCommandBoundary(t *testing.T) {
	args, err := redisCommandArgs(`SET "a b" "\x00\xff\n"`)
	if err != nil || !reflect.DeepEqual(args, []string{"SET", "a b", "\x00\xff\n"}) {
		t.Fatalf("parser: %q %v", args, err)
	}
	for _, command := range []string{`GET "broken`, "PING\nFLUSHALL", `GET \xgg`, ""} {
		if _, err := redisCommandArgs(command); err == nil {
			t.Errorf("accepted malformed command %q", command)
		}
	}
	for _, command := range []string{"FLUSHALL", "FLUSHDB", "CONFIG GET *", "SELECT 1", "AUTH secret", "CLIENT PAUSE 9999", "ACL SETUSER root on nopass +@all", "EVAL 'return 1' 0", "EVAL_RO 'return 1' 0", "FCALL x 0", "KEYS *", "BLPOP x 0", "SUBSCRIBE x", "MONITOR", "RESTORE k 0 x", "MIGRATE localhost 6379 x 0 0", "MODULE LOAD bad", "SORT x STORE y", "XREADGROUP GROUP g c STREAMS x >"} {
		args, err := redisCommandArgs(command)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := redisCommandSafety(args); err == nil {
			t.Errorf("dangerous command allowed: %s", args[0])
		}
	}
	for _, command := range []string{"LRANGE k 0 -1", "ZRANGE k 0 999999", "ZRANGE k 0 10 BYSCORE", "GETRANGE k 0 -1", "SCAN 0 COUNT 1000000"} {
		args, _ := redisCommandArgs(command)
		if _, err := redisCommandSafety(args); err == nil {
			t.Errorf("unbounded read accepted: %s", command)
		}
	}
	for _, command := range []string{"GET k", "SCAN 0 COUNT 200 MATCH a*", "ZRANGE k 0 199 WITHSCORES", "HGET k field", "INFO server"} {
		args, _ := redisCommandArgs(command)
		if safety, err := redisCommandSafety(args); err != nil || safety != "allowed" {
			t.Errorf("read %s: %s %v", command, safety, err)
		}
	}
	for _, command := range []string{"GETEX k EX 5", "GETDEL k", "HSET k f v", "SET k v", "PERSIST k", "DEL k"} {
		args, _ := redisCommandArgs(command)
		if safety, err := redisCommandSafety(args); err != nil || safety == "allowed" {
			t.Errorf("write classified as read: %s: %s %v", command, safety, err)
		}
	}
}

func TestRedisConfigAndBlobBoundary(t *testing.T) {
	for _, raw := range []map[string]any{{"redis_connection_mode": "cluster"}, {"redis_connection_mode": "sentinel"}, {"redis_cluster_nodes": "host:6379"}, {"redis_sentinel_password": "secret"}, {"redis_tls_insecure": true}} {
		if err := validateRedis(Config{Host: "localhost", Raw: raw}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("unsupported mode accepted: %v", err)
		}
	}
	for _, db := range []string{"-1", "65536", "1.1", "--scan"} {
		if _, err := redisDBIndex(db); err == nil {
			t.Errorf("database accepted: %s", db)
		}
	}
	value := "\x00\xff\\hello"
	blob := redisBlob(value)
	bytes, err := base64.StdEncoding.DecodeString(blob["raw_base64"].(string))
	if err != nil || string(bytes) != value || blob["encoding"] != "binary" {
		t.Fatal("binary blob lost bytes")
	}
	if redisDisplay(value) != `\x00\xff\\hello` {
		t.Errorf("display: %q", redisDisplay(value))
	}
	if _, err := redisKey(map[string]any{"keyRaw": "!!!!"}, "keyRaw"); err == nil {
		t.Fatal("invalid base64 accepted")
	}
}

func TestRedisWireBoundsBeforeAllocation(t *testing.T) {
	for _, response := range []string{"$1000000000\r\n", "*100001\r\n", strings.Repeat("*1\r\n", 18) + "+OK\r\n"} {
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { defer close(done); defer server.Close(); _, _ = io.WriteString(server, response) }()
		limited := newRedisBoundedConn(client)
		buf := make([]byte, 100)
		_, err := limited.Read(buf)
		client.Close()
		<-done
		if err == nil {
			t.Fatal("oversized frame accepted")
		}
	}
	response := "*3\r\n$3\r\na\x00b\r\n:42\r\n$-1\r\n"
	client, server := net.Pipe()
	go func() { defer server.Close(); _, _ = io.WriteString(server, response) }()
	defer client.Close()
	bytes, err := io.ReadAll(newRedisBoundedConn(client))
	if err != nil || string(bytes) != response {
		t.Fatalf("RESP changed: %q %v", bytes, err)
	}
}

func TestRedisCancelAndCloseInterruptSockets(t *testing.T) {
	for _, closeDriver := range []bool{false, true} {
		t.Run(fmt.Sprint(closeDriver), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			accepted := make(chan net.Conn, 1)
			go func() {
				c, e := listener.Accept()
				if e == nil {
					accepted <- c
				}
			}()
			d := &redisDriver{options: redis.Options{Addr: listener.Addr().String(), Protocol: 2, MaxRetries: -1, ReadTimeout: time.Minute, WriteTimeout: time.Minute, DisableIdentity: true}, clients: make(map[*redis.Client]context.CancelFunc)}
			defer d.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := d.Handle(ctx, "/api/redis/execute-command", map[string]any{"command": "PING"}, false)
				result <- err
			}()
			var socket net.Conn
			select {
			case socket = <-accepted:
				defer socket.Close()
			case <-time.After(time.Second):
				t.Fatal("did not dial")
			}
			// Wait for the request to be written so this exercises a blocked read.
			buf := make([]byte, 1024)
			_ = socket.SetReadDeadline(time.Now().Add(time.Second))
			if _, err = socket.Read(buf); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			if closeDriver {
				d.Close()
			} else {
				cancel()
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("canceled request succeeded")
				}
				if time.Since(start) > time.Second {
					t.Fatal("cancel was slow")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancel did not close Redis socket")
			}
			d.mu.Lock()
			count := len(d.clients)
			d.mu.Unlock()
			if count != 0 {
				t.Fatalf("%d clients leaked", count)
			}
		})
	}
}

func redisLiveConfig(t *testing.T) Config {
	t.Helper()
	path := os.Getenv("DBX_TEST_REDIS_CONFIG")
	if path == "" {
		t.Skip("DBX_TEST_REDIS_CONFIG not configured")
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c Config
	if err = json.Unmarshal(bytes, &c); err != nil {
		t.Fatal(err)
	}
	return c
}
func TestRedisLiveCRUDAndSafety(t *testing.T) {
	config := redisLiveConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	connectCtx, connectCancel := context.WithCancel(ctx)
	native, err := openRedis(connectCtx, config)
	if err != nil {
		t.Fatal(err)
	}
	connectCancel()
	defer native.Close()
	d := native.(*redisDriver)
	if err = d.Ping(ctx); err != nil {
		t.Fatalf("connect context leaked into lifetime: %v", err)
	}
	prefix := "dbx-test-" + strconv.FormatInt(time.Now().UnixNano(), 10) + ":"
	key := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(prefix + s)) }
	touched := []string{"string", "binary\xff", "hash", "list", "set", "zset", "destination", "large", "same"}
	defer func() {
		for _, db := range []int{0, 1} {
			for _, name := range touched {
				_, _ = d.Handle(context.Background(), "/api/redis/delete-key", map[string]any{"db": db, "keyRaw": key(name)}, false)
			}
		}
	}()
	call := func(route string, p map[string]any) any {
		t.Helper()
		v, e := d.Handle(ctx, "/api/redis/"+route, p, false)
		if e != nil {
			t.Fatalf("%s: %v", route, e)
		}
		return v
	}
	value := func(name string) map[string]any {
		return call("get-value", map[string]any{"keyRaw": key(name)}).(map[string]any)
	}
	blobText := func(v any) string {
		t.Helper()
		raw := v.(map[string]any)["raw_base64"].(string)
		bytes, e := base64.StdEncoding.DecodeString(raw)
		if e != nil {
			t.Fatal(e)
		}
		return string(bytes)
	}
	info, e := d.Info(ctx)
	if e != nil || info["productName"] != "Redis" || info["productVersion"] == "" {
		t.Fatalf("info shape: %v %v", info, e)
	}
	dbs := call("list-databases", map[string]any{}).([]map[string]any)
	if len(dbs) < 2 {
		t.Fatal("expected configured databases")
	}
	call("set-string", map[string]any{"keyRaw": key("string"), "value": "hello", "ttl": 90})
	call("set-string", map[string]any{"keyRaw": key("string"), "value": "changed"})
	ttl := call("get-ttl", map[string]any{"keyRaw": key("string")}).(int64)
	if ttl < 80 || ttl > 90 {
		t.Fatalf("TTL not preserved: %d", ttl)
	}
	if blobText(value("string")["data"].(map[string]any)["content"]) != "changed" {
		t.Fatal("string value mismatch")
	}
	command := `SET "` + prefix + `binary\xff" "\x00\xffhello\\"`
	call("execute-command", map[string]any{"command": command})
	if got := blobText(call("get-raw-value", map[string]any{"keyRaw": key("binary\xff")})); got != "\x00\xffhello\\" {
		t.Fatalf("binary value mismatch: %q", got)
	}
	scanned := call("scan-keys-batch", map[string]any{"pattern": prefix + "*", "count": 1000, "maxIterations": 6}).(map[string]any)
	found := false
	for _, item := range scanned["keys"].([]map[string]any) {
		if item["key_raw"] == key("binary\xff") {
			found = true
		}
	}
	if !found {
		t.Fatal("binary key missing from scan")
	}
	for _, field := range []string{"a", "b"} {
		call("hash-set", map[string]any{"keyRaw": key("hash"), "field": field, "value": field})
	}
	if _, e = d.Handle(ctx, "/api/redis/hash-field-update", map[string]any{"keyRaw": key("hash"), "oldField": "a", "newField": "b", "value": "lost"}, false); e == nil {
		t.Fatal("hash destination collision accepted")
	}
	call("hash-field-update", map[string]any{"keyRaw": key("hash"), "oldField": "a", "newField": "c", "value": "updated"})
	hash := value("hash")["data"].(map[string]any)
	if hash["total"] != int64(2) {
		t.Fatal("hash count incorrect")
	}
	for _, text := range []string{"__DELETED_PLACEHOLDER__", "remove", "last"} {
		call("list-push", map[string]any{"keyRaw": key("list"), "value": text})
	}
	call("list-remove", map[string]any{"keyRaw": key("list"), "index": 1})
	list := value("list")["data"].(map[string]any)["items"].([]map[string]any)
	if len(list) != 2 || blobText(list[0]["value"]) != "__DELETED_PLACEHOLDER__" || blobText(list[1]["value"]) != "last" {
		t.Fatal("list removal corrupted other element")
	}
	call("set-add", map[string]any{"keyRaw": key("set"), "member": "member", "ttl": 90})
	if value("set")["data"].(map[string]any)["total"] != int64(1) {
		t.Fatal("set insertion missing")
	}
	call("zadd", map[string]any{"keyRaw": key("zset"), "member": "a", "score": 1.5, "ttl": 90})
	if call("zset-update", map[string]any{"keyRaw": key("zset"), "originalMember": "a", "expectedScore": "1.5", "member": "b", "score": "2.25"}).(bool) {
		t.Fatal("atomic zset update incorrectly reported ACL fallback")
	}
	if _, e = d.Handle(ctx, "/api/redis/zset-update", map[string]any{"keyRaw": key("zset"), "originalMember": "missing", "expectedScore": "0", "member": "bad", "score": "1"}, false); e == nil {
		t.Fatal("missing sorted-set source accepted")
	}
	call("zadd", map[string]any{"keyRaw": key("zset"), "member": "infinite", "score": "inf"})
	call("zset-update", map[string]any{"keyRaw": key("zset"), "originalMember": "infinite", "expectedScore": "inf", "member": "infinite", "score": "-inf"})
	if _, e = d.Handle(ctx, "/api/redis/zset-update", map[string]any{"keyRaw": key("zset"), "originalMember": "infinite", "expectedScore": "inf", "member": "infinite", "score": "0"}, false); e == nil {
		t.Fatal("opposite-infinity score bypassed stale check")
	}

	if _, e = d.Handle(ctx, "/api/redis/zset-update", map[string]any{"keyRaw": key("zset"), "originalMember": "b", "expectedScore": "1.5", "member": "b", "score": "3"}, false); e == nil {
		t.Fatal("stale zset update accepted")
	}
	expiry := call("set-keys-ttl", map[string]any{"keyRaws": []string{key("string"), key("missing")}, "ttl": -1}).(map[string]any)
	if expiry["applied"] != 1 || !reflect.DeepEqual(expiry["missing_key_raws"], []string{key("missing")}) {
		t.Fatalf("expiry result: %v", expiry)
	}
	call("set-string", map[string]any{"keyRaw": key("large"), "value": strings.Repeat("x", redisPreviewLimit+3)})
	large := value("large")["data"].(map[string]any)
	if large["truncated"] != true || len(blobText(large["content"])) != redisPreviewLimit || large["total_bytes"] != int64(redisPreviewLimit+3) {
		t.Fatal("preview boundary incorrect")
	}
	// Each operation's DB selection remains isolated even when run concurrently.
	var wg sync.WaitGroup
	for db := 0; db < 2; db++ {
		db := db
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				_, e := d.Handle(ctx, "/api/redis/set-string", map[string]any{"db": db, "keyRaw": key("same"), "value": strconv.Itoa(db)}, false)
				if e != nil {
					t.Error(e)
				}
			}
		}()
	}
	wg.Wait()
	for db := 0; db < 2; db++ {
		if got := blobText(call("get-raw-value", map[string]any{"db": db, "keyRaw": key("same")})); got != strconv.Itoa(db) {
			t.Fatal("SELECT leaked between databases")
		}
	}
	for _, route := range []string{"set-string", "delete-key", "delete-keys", "rename-key", "hash-set", "hash-del", "hash-field-update", "list-push", "list-set", "list-remove", "set-add", "set-remove", "zadd", "zrem", "zset-update", "set-ttl", "set-expire-at", "set-keys-ttl", "set-keys-expire-at"} {
		if _, e = d.Handle(ctx, "/api/redis/"+route, map[string]any{"keyRaw": key("string"), "value": "bad"}, true); e == nil || !strings.Contains(e.Error(), "locked") {
			t.Errorf("readonly route %s: %v", route, e)
		}
	}
	for _, command := range []string{"SET " + prefix + "string bad", "GETEX " + prefix + "string EX 5", "DEL " + prefix + "string"} {
		if _, e = d.Handle(ctx, "/api/redis/execute-command", map[string]any{"command": command, "skipSafetyCheck": true}, true); e == nil {
			t.Fatalf("readonly bypass: %s", command)
		}
	}
	for _, command := range []string{"FLUSHALL", "EVAL 'return 1' 0", "SELECT 1"} {
		if _, e = d.Handle(ctx, "/api/redis/execute-command", map[string]any{"command": command, "skipSafetyCheck": true}, false); !errors.Is(e, ErrUnsupported) {
			t.Fatalf("console bypass: %s %v", command, e)
		}
	}
	if _, e = d.Handle(ctx, "/api/redis/execute-command", map[string]any{"command": "GET " + prefix + "string"}, true); e != nil {
		t.Fatal(e)
	}
}

func TestRedisLiveTLSAuthAndSSH(t *testing.T) {
	config := redisLiveConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bad := config
	bad.Password += "invalid"
	if d, err := openRedis(ctx, bad); err == nil {
		d.Close()
		t.Fatal("wrong password accepted")
	}
	if config.SSL {
		bad = config
		bad.CACert = ""
		if d, err := openRedis(ctx, bad); err == nil {
			d.Close()
			t.Fatal("untrusted TLS certificate accepted")
		}
	}
	addr := net.JoinHostPort(strings.Trim(config.Host, "[]"), strconv.Itoa(config.Port))
	server := newSSHTestServer(t, func(s *sshTestServer) { s.allow = map[string]bool{addr: true} })
	config.Transports = []map[string]any{server.layer()}
	d, err := openRedis(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Handle(ctx, "/api/redis/execute-command", map[string]any{"command": "PING"}, true); err != nil {
		t.Fatal(err)
	}
	d.Close()
	server.waitIdle(t)
	if err = d.Ping(ctx); err == nil {
		t.Fatal("closed Redis handle accepted a new operation")
	}
}

func TestRedisLiveACLPreflightAndFieldTTL(t *testing.T) {
	config := redisLiveConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	native, err := openRedis(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	d := native.(*redisDriver)
	admin, ctx, done, err := d.client(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	key := "dbx-acl-test-" + suffix
	user := "dbx_test_" + suffix
	defer admin.Del(ctx, key)
	if err = admin.HSet(ctx, key, "field", "original").Err(); err != nil {
		t.Fatal(err)
	}
	ttlCmd := admin.Do(ctx, "HEXPIRE", key, 90, "FIELDS", 1, "field")
	if ttlCmd.Err() != nil {
		t.Fatal(ttlCmd.Err())
	}
	raw := base64.StdEncoding.EncodeToString([]byte(key))
	if _, err = d.Handle(ctx, "/api/redis/hash-set", map[string]any{"keyRaw": raw, "field": "field", "value": "updated"}, false); err != nil {
		t.Fatal(err)
	}
	ttlValues, err := admin.Do(ctx, "HTTL", key, "FIELDS", 1, "field").Slice()
	if err != nil || len(ttlValues) != 1 {
		t.Fatalf("field TTL response: %v", err)
	}
	ttl, _ := strconv.ParseInt(fmt.Sprint(ttlValues[0]), 10, 64)
	if ttl < 80 || ttl > 90 {
		t.Fatal("hash field TTL was lost")
	}
	if err = admin.Del(ctx, key).Err(); err != nil {
		t.Fatal(err)
	}
	// Lack of EXPIRE permission must fail before SADD, since Redis Lua errors
	// alone do not roll back writes already executed by a script.
	err = admin.Do(ctx, "ACL", "SETUSER", user, "reset", "on", ">"+config.Password, "~"+key, "+ping", "+info", "+eval", "+sadd", "+scard", "+exists").Err()
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Do(ctx, "ACL", "DELUSER", user)
	limited := config
	limited.Username = user
	restricted, err := openRedis(ctx, limited)
	if err != nil {
		t.Fatal(err)
	}
	defer restricted.Close()
	if _, err = restricted.Handle(ctx, "/api/redis/set-add", map[string]any{"keyRaw": raw, "member": "must-not-be-written", "ttl": 90}, false); err == nil {
		t.Fatal("missing EXPIRE permission accepted")
	}
	exists, err := admin.Exists(ctx, key).Result()
	if err != nil || exists != 0 {
		t.Fatalf("failed ACL operation partially wrote key: %d %v", exists, err)
	}
}

func TestRedisSavedClusterConfigFailsBeforeDial(t *testing.T) {
	for _, mode := range []string{"cluster", "sentinel"} {
		var config Config
		if err := json.Unmarshal([]byte(`{"db_type":"redis","host":"127.0.0.1","port":1,"redis_connection_mode":"`+mode+`"}`), &config); err != nil {
			t.Fatal(err)
		}
		config = config.normalized()
		if str(config.Raw, "redis_connection_mode") != mode {
			t.Fatal("saved Redis topology was normalized away")
		}
		driver, err := openNative(context.Background(), config)
		if driver != nil {
			driver.Close()
			t.Fatal("unsupported topology opened")
		}
		if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), mode) {
			t.Fatalf("unsupported topology did not fail explicitly before network: %v", err)
		}
	}
}
