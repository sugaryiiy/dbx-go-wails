package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Opt in with a private JSON array pointing only to disposable databases.
func TestNativeEnginesHTTP(t *testing.T) {
	path := os.Getenv("DBX_TEST_NATIVE_CONFIGS")
	if path == "" {
		t.Skip("set DBX_TEST_NATIVE_CONFIGS to disposable database configurations")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read native test configuration")
	}
	var configs []map[string]any
	if json.Unmarshal(data, &configs) != nil || len(configs) == 0 {
		t.Fatal("invalid native test configurations")
	}
	for _, config := range configs {
		t.Run(str(config, "db_type"), func(t *testing.T) {
			config = cloneMap(config)
			config["id"], config["save_password"], config["read_only"] = "allowed", true, true
			dir := t.TempDir()
			s := testServer(t, dir)
			cookie := loginTest(t, s)
			value(t, call(s, "POST", "/api/connection/save", map[string]any{"configs": []any{config}}, cookie, ""))
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s = testServer(t, dir)
			cookie = loginTest(t, s)
			saved := value(t, call(s, "GET", "/api/connection/list", nil, cookie, "")).([]any)[0].(map[string]any)
			for _, field := range []string{"ssl", "ca_cert_path", "url_params", "redis_connection_mode"} {
				if fmt.Sprint(saved[field]) != fmt.Sprint(config[field]) {
					t.Fatalf("saved %s changed", field)
				}
			}
			if value(t, call(s, "POST", "/api/connection/connect", map[string]any{"config": saved}, cookie, "")) != "allowed" {
				t.Fatal("wrong connection id")
			}
			post := func(path string, args map[string]any) any {
				t.Helper()
				args["connectionId"] = "allowed"
				return value(t, call(s, "POST", path, args, cookie, ""))
			}
			post("/api/connection/database-info", map[string]any{})
			post("/api/connection/check-health", map[string]any{})
			mcpAllow(t, s)
			if _, err := s.MCPBackend().Call(context.Background(), "dbx_execute_query", mcpArgs("SELECT 1")); err == nil || !strings.Contains(err.Error(), "not been migrated to Go MCP") {
				t.Fatalf("native engine crossed MCP boundary: %v", err)
			}
			name := fmt.Sprintf("dbx_http_%d", time.Now().UnixNano())
			var writePath, cleanupPath string
			var write, cleanup, read map[string]any
			var readPath string
			switch str(config, "db_type") {
			case "redis":
				key := base64.StdEncoding.EncodeToString([]byte(name))
				writePath, write = "/api/redis/set-string", map[string]any{"keyRaw": key, "value": "HTTP中文", "db": "0", "ttl": 60}
				readPath, read = "/api/redis/get-value", map[string]any{"keyRaw": key, "db": "0"}
				cleanupPath, cleanup = "/api/redis/delete-key", map[string]any{"keyRaw": key, "db": "0"}
				post("/api/redis/list-databases", map[string]any{})
			case "mongodb":
				writePath, write = "/api/document-store/insert-document", map[string]any{"database": name, "collection": "records", "docJson": `{"_id":"http","number":{"$numberLong":"9223372036854775807"}}`}
				readPath, read = "/api/document-store/find-documents", map[string]any{"database": name, "collection": "records", "limit": 10}
				cleanupPath, cleanup = "/api/mongo/drop-database", map[string]any{"database": name}
				post("/api/document-store/list-databases", map[string]any{})
			case "clickhouse":
				writePath, write = "/api/query/execute", map[string]any{"database": config["database"], "sql": "CREATE TABLE `" + name + "` (id UInt64) ENGINE=Memory"}
				readPath, read = "/api/query/execute", map[string]any{"database": config["database"]}
				cleanupPath, cleanup = "/api/query/execute", map[string]any{"database": config["database"], "sql": "DROP TABLE `" + name + "`"}
				post("/api/schema/databases", map[string]any{})
			default:
				t.Fatal("unsupported native test engine")
			}
			write["connectionId"] = "allowed"
			if got := call(s, "POST", writePath, write, cookie, ""); got.Code == 200 {
				t.Fatal("protected write succeeded")
			}
			post("/api/connection/write-unlock", map[string]any{"durationSecs": 60})
			post(writePath, write)
			defer func() {
				post("/api/connection/write-unlock", map[string]any{"durationSecs": 60})
				post(cleanupPath, cleanup)
				post("/api/connection/disconnect", map[string]any{})
			}()
			if str(config, "db_type") == "clickhouse" {
				post("/api/query/execute", map[string]any{"database": config["database"], "sql": "INSERT INTO `" + name + "` VALUES (18446744073709551615)"})
				read["sql"] = post("/api/query/build-table-select-sql", map[string]any{"options": map[string]any{"databaseType": "clickhouse", "database": config["database"], "includeDatabaseName": true, "tableName": name, "limit": 10}})
				post("/api/schema/columns", map[string]any{"database": config["database"], "table": name})
			}
			post("/api/connection/write-unlock/lock", map[string]any{})
			result := post(readPath, read)
			encoded, _ := json.Marshal(result)
			want := map[string]string{"redis": base64.StdEncoding.EncodeToString([]byte("HTTP中文")), "mongodb": "9223372036854775807", "clickhouse": "18446744073709551615"}[str(config, "db_type")]
			if !strings.Contains(string(encoded), want) {
				t.Fatalf("native result lost expected value: %s", encoded)
			}
			if got := call(s, "POST", writePath, write, cookie, ""); got.Code == 200 {
				t.Fatal("relocked write succeeded")
			}
		})
	}
}
