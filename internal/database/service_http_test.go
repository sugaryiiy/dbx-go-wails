package database

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func serviceFixture(t *testing.T, kind string, handler http.HandlerFunc, external map[string]any) *serviceDriver {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	if external == nil {
		external = map[string]any{}
	}
	external["serverAddr"] = server.URL
	config := Config{Type: kind, Raw: map[string]any{"external_config": external}}
	value, err := openService(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	driver := value.(*serviceDriver)
	t.Cleanup(func() { driver.Close() })
	return driver
}
func TestServiceNacosPreservesConfigAndRefreshAuthWithoutRace(t *testing.T) {
	content := `{ "name": "原始 JSON", "enabled": true }` + "\n"
	var logins atomic.Int32
	driver := serviceFixture(t, "nacos", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/users/login":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("password") != "secret" {
				t.Error("login password missing")
			}
			logins.Add(1)
			w.Write([]byte(`{"accessToken":"fixture-token","tokenTtl":1}`))
		case "/v1/console/namespaces":
			if r.URL.Query().Get("accessToken") != "fixture-token" {
				t.Error("missing Nacos token")
			}
			w.Write([]byte(`{"code":200,"data":[]}`))
		case "/v1/cs/configs":
			w.Write([]byte(content))
		default:
			w.WriteHeader(500)
			w.Write([]byte("fixture-token secret"))
		}
	}, map[string]any{"auth": map[string]any{"kind": "basic", "username": "admin", "password": "secret"}})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := driver.Handle(context.Background(), "/api/nacos/configs/get", map[string]any{"key": map[string]any{"dataId": "test", "group": "DEFAULT_GROUP"}}, false)
			if err != nil {
				t.Error(err)
				return
			}
			if mapValue(value)["content"] != content {
				t.Error("config content changed")
			}
			_, err = driver.request(context.Background(), http.MethodGet, "/fail", nil, nil)
			if err == nil || strings.Contains(err.Error(), "fixture-token") || strings.Contains(err.Error(), "secret") {
				t.Errorf("error leaked credentials: %v", err)
			}
		}()
	}
	wg.Wait()
	if logins.Load() < 2 {
		t.Fatal("token expiry did not refresh")
	}
}
func TestServiceWritesLockedRejectBeforeHTTPAndConsulCAS(t *testing.T) {
	var requests atomic.Int32
	driver := serviceFixture(t, "consul", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Consul-Token") != "consul-secret" {
			t.Error("missing Consul token")
		}
		if r.URL.Path == "/v1/status/leader" {
			w.Write([]byte(`"localhost:8300"`))
			return
		}
		requests.Add(1)
		if r.Method == http.MethodGet {
			w.Write([]byte(`[{"Key":"x","Value":"eA==","Flags":77,"ModifyIndex":42,"Session":""}]`))
			return
		}
		if r.URL.Query().Get("cas") != "42" || r.URL.Query().Get("flags") != "77" {
			t.Errorf("lost CAS/flags: %s", r.URL)
		}
		w.Write([]byte(`false`))
	}, map[string]any{"token": "consul-secret"})
	params := map[string]any{"key": "x", "value": map[string]any{"encoding": "utf8", "data": "new"}, "options": map[string]any{"expectedModRevision": "42"}}
	if _, err := driver.Handle(context.Background(), "/api/consul/put", params, true); err == nil {
		t.Fatal("locked write succeeded")
	}
	if requests.Load() != 0 {
		t.Fatal("locked write reached server")
	}
	if _, err := driver.Handle(context.Background(), "/api/consul/put", params, false); err == nil || !strings.Contains(err.Error(), "CAS_CONFLICT") {
		t.Fatalf("CAS failure reported success: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatal("CAS validation read missing")
	}
}
func TestServiceElasticReadOnlyRESTAndCancel(t *testing.T) {
	var calls atomic.Int32
	driver := serviceFixture(t, "elasticsearch", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Write([]byte(`{"version":{"number":"8.0"}}`))
			return
		}
		calls.Add(1)
		if r.URL.Path == "/index/_search" {
			w.Write([]byte(`{"hits":{"total":{"value":0},"hits":[]}}`))
			return
		}
		<-r.Context().Done()
	}, nil)
	for _, sql := range []string{"DELETE /index", "POST /index/_delete_by_query\n{}", "GET /index/_refresh"} {
		if _, err := driver.Handle(context.Background(), "/api/query/execute", map[string]any{"sql": sql}, true); err == nil {
			t.Errorf("unsafe REST accepted: %s", sql)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("locked REST request reached server")
	}
	if _, err := driver.Handle(context.Background(), "/api/query/execute", map[string]any{"sql": "POST /index/_search\n{}"}, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := driver.request(ctx, http.MethodGet, "/block", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout not propagated: %v", err)
	}
}
func TestServiceTLSRejectsWrongCAAndAcceptsConfiguredCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "available"})
	}))
	defer server.Close()
	config := Config{Type: "meilisearch", ConnectionString: server.URL, Raw: map[string]any{}}
	if value, err := openService(context.Background(), config); err == nil {
		value.Close()
		t.Fatal("untrusted TLS server accepted")
	}
	path := filepath.Join(t.TempDir(), "server.pem")
	// Export the test certificate in PEM so the production CA loader is exercised.
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	config.CACert = path
	driver, err := openService(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	driver.Close()
}
