package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func pluginTestArchive(t *testing.T, manifest map[string]any, files map[string][]byte, key ed25519.PrivateKey, keyID string) []byte {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	contents := map[string][]byte{"manifest.json": raw}
	for name, data := range files {
		contents[name] = data
	}
	sums := map[string]string{}
	for name, data := range contents {
		digest := sha256.Sum256(data)
		sums[name] = hex.EncodeToString(digest[:])
	}
	checksum, err := json.Marshal(map[string]any{"algorithm": "sha256", "files": sums})
	if err != nil {
		t.Fatal(err)
	}
	contents["checksums.json"] = checksum
	if key != nil {
		signature, _ := json.Marshal(map[string]any{"algorithm": "ed25519", "key_id": keyID, "signature": base64.StdEncoding.EncodeToString(ed25519.Sign(key, checksum))})
		contents["signature.json"] = signature
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for name, data := range contents {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0700)
		writer, err := archive.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
func pluginTestManifest(id, version string, permissions []any) map[string]any {
	return map[string]any{"manifest_version": 1, "id": id, "name": id, "version": version, "publisher": "Test publisher", "engines": map[string]any{"dbx": ">=0.6.0", "host_api": "^1.4"}, "permissions": permissions, "entrypoints": map[string]any{"ui": map[string]any{"root": "ui", "entry": "ui/index.html"}}}
}
func TestPluginPackageTrustChecksumsPathsAndLifecycle(t *testing.T) {
	s := testServer(t, t.TempDir())
	manifest := pluginTestManifest("test-plugin", "1.0.0", []any{"host.events"})
	archive := pluginTestArchive(t, manifest, map[string][]byte{"ui/index.html": []byte("<h1>One</h1>")}, nil, "")
	if _, err := s.installPluginPackage(archive, false, "file", nil); err == nil {
		t.Fatal("Unsigned package installed without consent")
	}
	if _, err := s.installPluginPackage(archive, true, "file", nil); err != nil {
		t.Fatal(err)
	}
	if len(s.pluginList()) != 1 {
		t.Fatal("plugin not listed")
	}
	cookie := loginTest(t, s)
	asset := call(s, "GET", "/api/plugins/test-plugin/ui", nil, cookie, "")
	if asset.Code != 200 || asset.Body.String() != "<h1>One</h1>" {
		t.Fatal(asset.Code, asset.Body)
	}
	if call(s, "GET", "/api/plugins/test-plugin/assets/%2e%2e/state.enc", nil, cookie, "").Code == 200 {
		t.Fatal("asset traversal allowed")
	}
	installed, _ := s.pluginRecord("test-plugin")
	root := s.pluginDir(installed)
	if err := os.WriteFile(filepath.Join(root, "private.txt"), []byte("Private plugin file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../private.txt", filepath.Join(root, "ui", "leak")); err == nil && call(s, "GET", "/api/plugins/test-plugin/ui/leak", nil, cookie, "").Code == 200 {
		t.Fatal("UI asset escaped its UI root")
	}
	manifest["version"] = "1.1.0"
	archive2 := pluginTestArchive(t, manifest, map[string][]byte{"ui/index.html": []byte("<h1>Two</h1>")}, nil, "")
	if _, err := s.installPluginPackage(archive2, true, "file", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rollbackPlugin("test-plugin"); err != nil {
		t.Fatal(err)
	}
	record, err := s.pluginRecord("test-plugin")
	if err != nil || str(appObject(record["manifest"]), "version") != "1.0.0" {
		t.Fatal(record, err)
	}
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	value(t, call(s, "POST", "/api/plugins/trusted-keys/save", map[string]any{"keyId": "test-key", "publicKey": base64.StdEncoding.EncodeToString(public)}, cookie, ""))
	signedManifest := pluginTestManifest("signed-plugin", "1.0.0", []any{})
	signed := pluginTestArchive(t, signedManifest, map[string][]byte{"ui/index.html": []byte("Signed")}, private, "test-key")
	if _, err := s.installPluginPackage(signed, false, "file", nil); err != nil {
		t.Fatal(err)
	}
	reader, _ := zip.NewReader(bytes.NewReader(signed), int64(len(signed)))
	var corrupt bytes.Buffer
	writer := zip.NewWriter(&corrupt)
	for _, file := range reader.File {
		input, _ := file.Open()
		data, _ := io.ReadAll(input)
		input.Close()
		if file.Name == "ui/index.html" {
			data = []byte("Tampered")
		}
		output, _ := writer.CreateHeader(&file.FileHeader)
		output.Write(data)
	}
	writer.Close()
	if _, err := s.installPluginPackage(corrupt.Bytes(), false, "file", nil); err == nil {
		t.Fatal("checksum tampering accepted")
	}
	for _, entry := range []struct {
		name string
		mode os.FileMode
	}{{"../escape", 0600}, {"ui/escape", os.ModeSymlink | 0777}} {
		var data bytes.Buffer
		writer := zip.NewWriter(&data)
		header := &zip.FileHeader{Name: entry.name}
		header.SetMode(entry.mode)
		file, _ := writer.CreateHeader(header)
		file.Write([]byte("../../outside"))
		writer.Close()
		if _, err := s.installPluginPackage(data.Bytes(), true, "file", nil); err == nil {
			t.Fatal("unsafe archive entry accepted", entry.name)
		}
	}
	if err := s.uninstallPlugin("test-plugin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pluginRecord("test-plugin"); err == nil {
		t.Fatal("uninstalled plugin remained discoverable")
	}
}
func TestPluginDataGrantReadOnlyAndRevocation(t *testing.T) {
	s := testServer(t, t.TempDir())
	cookie := loginTest(t, s)
	manifest := pluginTestManifest("data-plugin", "1.0.0", []any{"host.data:read", "host.schema:read"})
	archive := pluginTestArchive(t, manifest, map[string][]byte{"ui/index.html": []byte("Data")}, nil, "")
	if _, err := s.installPluginPackage(archive, true, "file", nil); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"id": "plugin-db", "name": "Plugin DB", "db_type": "sqlite", "host": filepath.Join(t.TempDir(), "plugin.sqlite"), "database": "main"}
	value(t, call(s, "POST", "/api/connection/save", map[string]any{"configs": []any{config}}, cookie, ""))
	value(t, call(s, "POST", "/api/connection/connect", map[string]any{"config": config}, cookie, ""))
	value(t, call(s, "POST", "/api/query/execute", map[string]any{"connectionId": "plugin-db", "database": "main", "sql": "CREATE TABLE nums(id INTEGER)"}, cookie, ""))
	request := map[string]any{"connectionId": "plugin-db", "database": "main", "sql": "SELECT 42", "maxRows": 1}
	if _, err := s.pluginDataQuery(context.Background(), "data-plugin", request); err == nil || !strings.Contains(err.Error(), "ACCESS_NOT_GRANTED") {
		t.Fatal("ungranted query allowed", err)
	}
	value(t, call(s, "POST", "/api/plugin/data/grant", map[string]any{"pluginId": "data-plugin", "connectionId": "plugin-db", "granted": true}, cookie, ""))
	result, err := s.pluginDataQuery(context.Background(), "data-plugin", request)
	if err != nil || len(result.(map[string]any)["rows"].([][]any)) != 1 {
		t.Fatal(result, err)
	}
	request["sql"] = "INSERT INTO nums VALUES(1)"
	if _, err = s.pluginDataQuery(context.Background(), "data-plugin", request); err == nil {
		t.Fatal("plugin wrote to database")
	}
	request["sql"] = "SELECT load_extension('bad')"
	if _, err = s.pluginDataQuery(context.Background(), "data-plugin", request); err == nil {
		t.Fatal("plugin used external function")
	}
	if _, err = s.pluginTableMetadata(context.Background(), "data-plugin", map[string]any{"connectionId": "plugin-db", "database": "main", "table": "nums"}); err != nil {
		t.Fatal(err)
	}
	value(t, call(s, "POST", "/api/plugin/data/grant", map[string]any{"pluginId": "data-plugin", "connectionId": "plugin-db", "granted": false}, cookie, ""))
	request["sql"] = "SELECT 42"
	if _, err = s.pluginDataQuery(context.Background(), "data-plugin", request); err == nil {
		t.Fatal("revoked grant still allowed query")
	}
	value(t, call(s, "POST", "/api/plugin/data/grant", map[string]any{"pluginId": "data-plugin", "connectionId": "plugin-db", "granted": true}, cookie, ""))
	var script strings.Builder
	for n := 0; n < 100; n++ {
		script.WriteString("INSERT INTO nums VALUES(1);")
	}
	value(t, call(s, "POST", "/api/query/execute-multi", map[string]any{"connectionId": "plugin-db", "database": "main", "sql": script.String()}, cookie, ""))
	request["sql"] = "SELECT SUM(a.id+b.id+c.id+d.id+e.id) FROM nums a, nums b, nums c, nums d, nums e"
	done := make(chan error, 1)
	go func() { _, err := s.pluginDataQuery(context.Background(), "data-plugin", request); done <- err }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.pluginRuntime.mu.Lock()
		running := len(s.pluginRuntime.dataOps["data-plugin\x00plugin-db"]) > 0
		s.pluginRuntime.mu.Unlock()
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("query not registered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	value(t, call(s, "POST", "/api/plugin/data/grant", map[string]any{"pluginId": "data-plugin", "connectionId": "plugin-db", "granted": false}, cookie, ""))
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("revoked in-flight query returned data")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("grant revocation did not cancel in-flight query")
	}
}
func buildPluginFixture(t *testing.T) (string, []byte) {
	t.Helper()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("Go tool required for plugin fixture")
	}
	dir := t.TempDir()
	source := `package main
import ("bufio"; "encoding/binary"; "encoding/json"; "io"; "os"; "time")
func main() {
 raw, _ := os.ReadFile("manifest.json")
 var manifest map[string]any
 json.Unmarshal(raw, &manifest)
 backend := manifest["entrypoints"].(map[string]any)["backend"].(map[string]any)
 framed := backend["transport"] == "stdio-framed"
 reader := bufio.NewReader(os.Stdin)
 read := func() (map[string]any, error) {
  var b []byte
  var err error
  if framed {
   header := [5]byte{}
   if _, err = io.ReadFull(reader, header[:]); err != nil { return nil, err }
   b = make([]byte, int(binary.BigEndian.Uint32(header[1:])))
   _, err = io.ReadFull(reader, b)
  } else { b, err = reader.ReadBytes('\n') }
  if err != nil { return nil, err }
  var request map[string]any
  err = json.Unmarshal(b, &request)
  return request, err
 }
 send := func(value any) {
  out, _ := json.Marshal(value)
  if framed {
   header := [5]byte{}
   binary.BigEndian.PutUint32(header[1:], uint32(len(out)))
   os.Stdout.Write(header[:]); os.Stdout.Write(out)
  } else { os.Stdout.Write(append(out, '\n')) }
 }
 var callbackResult any
 for {
  request, err := read()
  if err != nil { return }
  if request["method"] == "crash" { return }
  if request["method"] == "hang" { for { time.Sleep(time.Hour) } }
  id, exists := request["id"]
  if !exists { continue }
  result := request["params"]
  if request["method"] == "plugin/initialize" {
   if manifest["test_initialize_hang"] == true { for { time.Sleep(time.Hour) } }
   if callback, exists := manifest["test_initialize_query"]; exists {
    send(map[string]any{"jsonrpc":"2.0", "id":"initialize-host-query", "method":"host/queryData", "params":callback})
    reply, err := read()
    if err != nil || reply["error"] != nil || reply["id"] != "initialize-host-query" { return }
    callbackResult = reply["result"]
   }
   result = map[string]any{"protocolVersion":1, "plugin":map[string]any{"id":manifest["id"], "version":manifest["version"]}}
  }
  if request["method"] == "callback-result" { result = callbackResult }
  send(map[string]any{"jsonrpc":"2.0", "id":id, "result":result})
 }
}`
	path := filepath.Join(dir, "main.go")
	os.WriteFile(path, []byte(source), 0600)
	output := filepath.Join(dir, "fixture")
	if runtime.GOOS == "windows" {
		output += ".exe"
	}
	command := exec.Command(goTool, "build", "-mod=mod", "-o", output, path)
	command.Env = append(os.Environ(), "GOWORK=off")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	b, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Base(output), b
}
func TestPluginProcessesJSONLFramedEOFTimeoutAndShutdown(t *testing.T) {
	executable, binary := buildPluginFixture(t)
	for _, transport := range []string{"stdio-jsonl", "stdio-framed"} {
		t.Run(transport, func(t *testing.T) {
			s := testServer(t, t.TempDir())
			manifest := pluginTestManifest("rpc-plugin", "1.0.0", []any{"host.events", "host.binary"})
			entries := appObject(manifest["entrypoints"])
			entries["backend"] = map[string]any{"executable": executable, "transport": transport}
			archive := pluginTestArchive(t, manifest, map[string][]byte{"ui/index.html": []byte("RPC"), executable: binary}, nil, "")
			if _, err := s.installPluginPackage(archive, true, "file", nil); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			process, err := s.pluginRuntime.activate(ctx, "rpc-plugin")
			if err != nil {
				t.Fatal(err)
			}
			reply, err := process.invoke(ctx, "echo", map[string]any{"message": "pong"})
			if err != nil || str(appObject(reply), "message") != "pong" {
				t.Fatal(reply, err)
			}
			timeout, stop := context.WithTimeout(context.Background(), 40*time.Millisecond)
			_, err = process.invoke(timeout, "hang", nil)
			stop()
			if err == nil {
				t.Fatal("hanging plugin did not time out")
			}
			select {
			case <-process.done:
			case <-time.After(3 * time.Second):
				t.Fatal("timed-out plugin process not killed")
			}
			process, err = s.pluginRuntime.activate(ctx, "rpc-plugin")
			if err != nil {
				t.Fatal(err)
			}
			_, err = process.invoke(ctx, "crash", nil)
			if err == nil {
				t.Fatal("EOF treated as successful RPC")
			}
			process, err = s.pluginRuntime.activate(ctx, "rpc-plugin")
			if err != nil {
				t.Fatal(err)
			}
			if !process.running() {
				t.Fatal("failed plugin did not restart")
			}
			s.Close()
			select {
			case <-process.done:
			case <-time.After(3 * time.Second):
				t.Fatal("shutdown leaked plugin process")
			}
		})
	}
}

func TestPluginInitializeHostDataCallbackAndShutdown(t *testing.T) {
	executable, binary := buildPluginFixture(t)
	for _, transport := range []string{"stdio-jsonl", "stdio-framed"} {
		t.Run(transport, func(t *testing.T) {
			s := testServer(t, t.TempDir())
			cookie := loginTest(t, s)
			config := map[string]any{"id": "handshake-db", "name": "Handshake DB", "db_type": "sqlite", "host": filepath.Join(t.TempDir(), "handshake.sqlite"), "database": "main"}
			value(t, call(s, "POST", "/api/connection/save", map[string]any{"configs": []any{config}}, cookie, ""))
			value(t, call(s, "POST", "/api/connection/connect", map[string]any{"config": config}, cookie, ""))
			manifest := pluginTestManifest("handshake-plugin", "1.0.0", []any{"host.data:read"})
			appObject(manifest["entrypoints"])["backend"] = map[string]any{"executable": executable, "transport": transport}
			manifest["test_initialize_query"] = map[string]any{"connectionId": "handshake-db", "database": "main", "sql": "SELECT 42", "maxRows": 1}
			archive := pluginTestArchive(t, manifest, map[string][]byte{"ui/index.html": []byte("Handshake"), executable: binary}, nil, "")
			if _, err := s.installPluginPackage(archive, true, "file", nil); err != nil {
				t.Fatal(err)
			}
			value(t, call(s, "POST", "/api/plugin/data/grant", map[string]any{"pluginId": "handshake-plugin", "connectionId": "handshake-db", "granted": true}, cookie, ""))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			process, err := s.pluginRuntime.activate(ctx, "handshake-plugin")
			if err != nil {
				t.Fatal("initialize callback blocked", err)
			}
			result, err := process.invoke(ctx, "callback-result", nil)
			data, _ := json.Marshal(result)
			if err != nil || !strings.Contains(string(data), `"rows":[[42]]`) {
				t.Fatal("initialize callback lost its query result", string(data), err)
			}
		})
	}
	t.Run("shutdown-during-initialize", func(t *testing.T) {
		s := testServer(t, t.TempDir())
		manifest := pluginTestManifest("initializing-plugin", "1.0.0", []any{})
		appObject(manifest["entrypoints"])["backend"] = map[string]any{"executable": executable, "transport": "stdio-jsonl"}
		manifest["test_initialize_hang"] = true
		archive := pluginTestArchive(t, manifest, map[string][]byte{"ui/index.html": []byte("Initializing"), executable: binary}, nil, "")
		if _, err := s.installPluginPackage(archive, true, "file", nil); err != nil {
			t.Fatal(err)
		}
		activation := make(chan error, 1)
		go func() {
			_, err := s.pluginRuntime.activate(context.Background(), "initializing-plugin")
			activation <- err
		}()
		deadline := time.Now().Add(2 * time.Second)
		for {
			s.pluginRuntime.mu.Lock()
			process := s.pluginRuntime.sessions["initializing-plugin"]
			s.pluginRuntime.mu.Unlock()
			if process != nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("initializing process was not registered")
			}
			time.Sleep(time.Millisecond)
		}
		closed := make(chan struct{})
		go func() { s.closePlugins(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Fatal("shutdown waited for the 30 second initialize timeout")
		}
		select {
		case err := <-activation:
			if err == nil {
				t.Fatal("closed runtime completed initialization")
			}
		case <-time.After(time.Second):
			t.Fatal("shutdown left activation running")
		}
	})
}

func TestPluginNotifyAndBinaryCancelBlockedPipe(t *testing.T) {
	for _, route := range []string{"notify", "binary"} {
		t.Run(route, func(t *testing.T) {
			s := testServer(t, t.TempDir())
			cookie := loginTest(t, s)
			manifest := pluginTestManifest("blocked-plugin", "1.0.0", []any{"host.binary"})
			archive := pluginTestArchive(t, manifest, map[string][]byte{"ui/index.html": []byte("Blocked")}, nil, "")
			if _, err := s.installPluginPackage(archive, true, "file", nil); err != nil {
				t.Fatal(err)
			}
			reader, writer := io.Pipe()
			defer reader.Close()
			done := make(chan struct{})
			close(done)
			process := &pluginProcess{id: "blocked-plugin", manifest: manifest, in: writer, framed: true, pending: map[uint64]chan pluginReply{}, done: done, owner: s.pluginRuntime}
			s.pluginRuntime.mu.Lock()
			s.pluginRuntime.sessions["blocked-plugin"] = process
			s.pluginRuntime.mu.Unlock()
			request := map[string]any{"pluginId": "blocked-plugin", "timeoutMs": 30, "method": "notify", "params": "payload", "channel": "blocked", "dataBase64": base64.StdEncoding.EncodeToString([]byte("payload"))}
			response := make(chan int, 1)
			go func() { response <- call(s, "POST", "/api/plugins/"+route, request, cookie, "").Code }()
			select {
			case code := <-response:
				if code != 400 || process.running() {
					t.Fatal("blocked write was not canceled", code)
				}
			case <-time.After(time.Second):
				t.Fatal("plugin HTTP request hung on an unread pipe")
			}
		})
	}
}
