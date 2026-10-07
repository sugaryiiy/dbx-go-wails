package database

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The opt-in fixture is a private JSON Config file. Never use production data:
// each test creates and drops only its own randomly named database.
func mongoIntegrationConfig(t *testing.T) Config {
	t.Helper()
	path := os.Getenv("DBX_TEST_MONGO_CONFIG")
	if path == "" {
		t.Skip("DBX_TEST_MONGO_CONFIG is not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c Config
	if err = json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	c.Database = fmt.Sprintf("dbx_go_test_%d", time.Now().UnixNano())
	return c
}

func TestMongoLiveDocumentsIndexesAndPermissions(t *testing.T) {
	c := mongoIntegrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	native, err := openMongo(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = native.Close() })
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = native.Handle(cleanup, "/api/mongo/drop-database", map[string]any{"database": c.Database}, false)
	})
	call := func(path string, args map[string]any) any {
		t.Helper()
		if args == nil {
			args = map[string]any{}
		}
		args["database"] = c.Database
		args["collection"] = "records"
		value, err := native.Handle(ctx, path, args, false)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return value
	}
	if _, err = native.Info(ctx); err != nil {
		t.Fatal(err)
	}
	call("/api/mongo/create-collection", nil)
	call("/api/mongo/run-command", map[string]any{"commandJson": `{"create":"shell_created"}`})
	call("/api/mongo/run-command", map[string]any{"commandJson": `{"listDatabases":1,"nameOnly":true}`})
	if _, err = native.Handle(ctx, "/api/mongo/run-command", map[string]any{"database": c.Database, "commandJson": `{"create":"must_not_exist"}`}, true); err == nil {
		t.Fatal("run-command bypassed collection creation write lock")
	}
	oid := "507f1f77bcf86cd799439011"
	call("/api/mongo/insert-documents", map[string]any{"docsJson": `[{"_id":{"$oid":"` + oid + `"},"n":1,"long":{"$numberLong":"9223372036854775807"},"decimal":{"$numberDecimal":"1.234567890123456789"},"binary":{"$binary":{"base64":"AAECAw==","subType":"80"}}},{"_id":"` + oid + `","n":2}]`})
	result := call("/api/document-store/find-documents", map[string]any{"limit": 1, "sort": `{"n":1}`}).(mongoDocumentResult)
	if result.Total != 2 || len(result.Documents) != 1 || !result.TotalIsExact {
		t.Fatalf("incorrect find pagination: %#v", result)
	}
	canonical, _ := json.Marshal(result.ExtendedDocuments[0])
	for _, token := range []string{"$oid", "9223372036854775807", "$numberDecimal", "$binary"} {
		if !strings.Contains(string(canonical), token) {
			t.Fatalf("BSON value lost: %s", token)
		}
	}
	call("/api/document-store/update-document", map[string]any{"id": `{"$oid":"` + oid + `"}`, "docJson": string(canonical)})
	call("/api/mongo/update-documents", map[string]any{"filterJson": `{"n":1}`, "updateJson": `{"$inc":{"n":2}}`, "many": false})
	one := call("/api/mongo/find-one", map[string]any{"filter": `{"n":3}`}).(mongoDocumentResult)
	if one.Total != 1 {
		t.Fatal("update did not take effect")
	}
	call("/api/mongo/create-index", map[string]any{"keysJson": `{"n":1}`, "optionsJson": `{"name":"n_unique","unique":true}`})
	indexes := call("/api/mongo/list-index-specs", nil).([]map[string]any)
	if len(indexes) != 2 {
		t.Fatalf("index count: %d", len(indexes))
	}
	if len(call("/api/schema/indexes", nil).([]map[string]any)) != 2 {
		t.Fatal("sidebar indexes missing")
	}
	columns := call("/api/schema/columns", nil).([]map[string]any)
	if len(columns) < 5 {
		t.Fatal("sampled columns missing")
	}
	call("/api/mongo/collection-stats", nil)
	call("/api/mongo/explain-find", map[string]any{"limit": 1, "filter": `{"n":3}`})
	agg := call("/api/mongo/aggregate-documents", map[string]any{"pipelineJson": `[{"$sort":{"n":1}}]`, "maxRows": 1, "optionsJson": `{"maxTimeMS":5000}`}).(mongoDocumentResult)
	if len(agg.Documents) != 1 || agg.TotalIsExact {
		t.Fatal("aggregate truncation not indicated")
	}
	distinct := call("/api/mongo/distinct", map[string]any{"field": "n"}).(mongoDocumentResult)
	if distinct.Total != 2 {
		t.Fatal("incorrect distinct result")
	}
	for _, path := range []string{"/api/mongo/aggregate-documents", "/api/mongo/run-command"} {
		args := map[string]any{"database": c.Database, "collection": "records", "pipelineJson": `[{"$out":"forbidden"}]`, "commandJson": `{"drop":"records"}`}
		if _, err = native.Handle(ctx, path, args, true); err == nil {
			t.Fatalf("unsafe read route accepted: %s", path)
		}
	}
	if _, err = native.Handle(ctx, "/api/document-store/delete-document", map[string]any{"database": c.Database, "collection": "records", "id": oid}, true); err == nil {
		t.Fatal("locked delete allowed")
	}
	call("/api/document-store/delete-document", map[string]any{"id": `__dbx_mongo_string_id__"` + oid + `"`})
	if count := call("/api/mongo/count-documents", nil).(int64); count != 1 {
		t.Fatal("typed string deletion affected wrong document")
	}
	one = call("/api/mongo/find-one", map[string]any{"filter": `{"_id":{"$oid":"` + oid + `"}}`}).(mongoDocumentResult)
	if one.Total != 1 {
		t.Fatal("ObjectID was deleted instead of string")
	}
	call("/api/mongo/find-one-and-update", map[string]any{"filterJson": `{"n":3}`, "updateJson": `{"$set":{"n":4}}`, "optionsJson": `{"returnDocument":"after"}`})
	call("/api/mongo/drop-indexes", map[string]any{"indexesJson": `"n_unique"`, "single": true})
	call("/api/mongo/insert-documents", map[string]any{"docsJson": `[{"_id":{"z":1,"a":2},"long":{"$numberLong":"42"},"double":{"$numberDouble":"42.0"}},{"_id":"[object Object]"}]`})
	compound := call("/api/mongo/find-one", map[string]any{"filter": `{"_id":{"z":1,"a":2}}`}).(mongoDocumentResult)
	wire, _ := json.Marshal(compound.ExtendedDocuments[0])
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	call("/api/document-store/update-document", map[string]any{"id": string(fields["_id"]), "docJson": string(wire)})
	updated := call("/api/mongo/find-one", map[string]any{"filter": `{"_id":{"z":1,"a":2}}`}).(mongoDocumentResult)
	updatedWire, _ := json.Marshal(updated.ExtendedDocuments[0])
	if string(wire) != string(updatedWire) {
		t.Fatalf("unchanged BSON document changed on editor replacement")
	}
	call("/api/document-store/delete-document", map[string]any{"id": string(fields["_id"])})
	if remaining := call("/api/mongo/find-one", map[string]any{"filter": `{"_id":"[object Object]"}`}).(mongoDocumentResult); remaining.Total != 1 {
		t.Fatal("compound ID mutation targeted string ID")
	}
	cancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err = native.Handle(cancelled, "/api/mongo/count-documents", map[string]any{"database": c.Database, "collection": "records"}, false); err == nil {
		t.Fatal("cancelled context ignored")
	}
}

func TestMongoLiveTLSAndSSH(t *testing.T) {
	c := mongoIntegrationConfig(t)
	if !c.SSL {
		t.Skip("fixture does not configure TLS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bad := c
	bad.ConnectTimeout = 1
	bad.CACert = ""
	if native, err := openMongo(ctx, bad); err == nil {
		native.Close()
		t.Fatal("untrusted TLS CA accepted")
	}
	address := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	server := newSSHTestServer(t, func(s *sshTestServer) { s.allow = map[string]bool{address: true} })
	c.Transports = []map[string]any{server.layer()}
	native, err := openMongo(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if err = native.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = native.Info(ctx); err != nil {
		t.Fatal(err)
	}
	if err = native.Close(); err != nil {
		t.Fatal(err)
	}
	server.waitIdle(t)
}
