package database

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMongoExtendedJSONRoundTrip(t *testing.T) {
	input := `{"_id":{"$oid":"507f1f77bcf86cd799439011"},"long":{"$numberLong":"9223372036854775807"},"smallLong":{"$numberLong":"42"},"decimal":{"$numberDecimal":"1234.567890123456789"},"binary":{"$binary":{"base64":"AAECAw==","subType":"80"}},"when":{"$date":{"$numberLong":"1234567890123"}},"nested":[{"$numberInt":"7"},true,null]}`
	doc, err := mongoDocument(input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := mongoResult([]any{doc}, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result.ExtendedDocuments[0])
	if err != nil {
		t.Fatal(err)
	}
	again, err := mongoDocument(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc, again) { // Canonical JSON maps may reorder fields, BSON types must match.
		for _, item := range doc {
			if !reflect.DeepEqual(item.Value, mongoGet(again, item.Key)) {
				t.Fatalf("BSON type/value changed for %s: %T -> %T", item.Key, item.Value, mongoGet(again, item.Key))
			}
		}
	}
	display := result.Documents[0].(map[string]any)
	if display["smallLong"] != int64(42) {
		t.Fatalf("safe number not displayed numerically: %v", display["smallLong"])
	}
	if _, ok := display["long"].(map[string]any); !ok {
		t.Fatal("unsafe int64 lost canonical wrapper")
	}
	if _, ok := display["_id"].(map[string]any); !ok {
		t.Fatal("ObjectID lost canonical wrapper")
	}
}

func TestMongoCompoundIDWireOrder(t *testing.T) {
	doc, err := mongoDocument(`{"_id":{"z":1,"a":2},"smallLong":{"$numberLong":"42"},"double":{"$numberDouble":"42.0"}}`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := mongoResult([]any{doc}, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{result.Documents[0], result.ExtendedDocuments[0]} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		id, err := mongoID(string(fields["_id"]))
		if err != nil || !reflect.DeepEqual(id, mongoGet(doc, "_id")) {
			t.Fatalf("compound ID field order/type changed: %s", fields["_id"])
		}
	}
	encoded, _ := json.Marshal(result.ExtendedDocuments[0])
	again, err := mongoDocument(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mongoGet(again, "smallLong").(int64); !ok {
		t.Fatal("safe int64 type changed")
	}
	if _, ok := mongoGet(again, "double").(float64); !ok {
		t.Fatal("integral double type changed")
	}
}

func TestMongoRejectsAmbiguousJSONAndWritePipelines(t *testing.T) {
	for _, input := range []string{`[]`, `{"x":1,"x":2}`, `{"x":{"a":1,"a":2}}`, `{"x":1} {}`, `{"x":NaN}`} {
		if _, err := mongoDocument(input); err == nil {
			t.Fatalf("accepted ambiguous JSON %s", input)
		}
	}
	for _, input := range []string{`{"$where":"return true"}`, `{"$lookup":{"pipeline":[{"$merge":"x"}]}}`, `{"$facet":{"x":[{"$out":"x"}]}}`, `{"x":{"$function":{"body":"return 1","args":[],"lang":"js"}}}`, `{"x":{"$code":"return 1"}}`, `{"$changeStream":{}}`} {
		doc, err := mongoDocument(input)
		if err != nil {
			t.Fatal(err)
		}
		if err = mongoSafeRead(doc); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("dangerous query accepted %s: %v", input, err)
		}
	}
	for _, input := range []string{`{"$match":{"name":"$out"}}`, `{"$project":{"value":{"$literal":"$where"}}}`} {
		doc, err := mongoDocument(input)
		if err != nil {
			t.Fatal(err)
		}
		if err = mongoSafeRead(doc); err != nil {
			t.Fatalf("safe query rejected: %v", err)
		}
	}
}

func TestMongoConfigurationAndTypedIDs(t *testing.T) {
	base := Config{Type: "mongodb", Host: "localhost", Username: "alice", Password: "secret", URLParams: "authSource=admin&authMechanism=SCRAM-SHA-256&directConnection=true"}
	opts, err := mongoClientOptions(base)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Auth.AuthSource != "admin" || opts.Auth.AuthMechanism != "SCRAM-SHA-256" || !*opts.Direct || *opts.RetryWrites {
		t.Fatal("MongoDB auth/direct options not preserved")
	}
	for _, params := range []string{"replicaSet=rs0", "tlsAllowInvalidCertificates=true", "authMechanism=MONGODB-OIDC", "directConnection=false", "authSource=admin&authSource=test", "retryWrites=true"} {
		c := base
		c.URLParams = params
		if _, err = mongoClientOptions(c); err == nil {
			t.Fatalf("unsupported config accepted: %s", params)
		}
	}
	c := base
	c.Host = "one,two"
	if _, err = mongoClientOptions(c); err == nil {
		t.Fatal("multiple hosts accepted")
	}
	c = base
	c.ConnectionString = "mongodb://localhost"
	if _, err = mongoClientOptions(c); err == nil {
		t.Fatal("URI accepted")
	}
	stringID, err := mongoID(`__dbx_mongo_string_id__"507f1f77bcf86cd799439011"`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stringID.(string); !ok {
		t.Fatal("typed string ID became ObjectID")
	}
	oid, err := mongoID(`{"$oid":"507f1f77bcf86cd799439011"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := oid.(bson.ObjectID); !ok {
		t.Fatal("ObjectID became string")
	}
	long, err := mongoID(`{"$numberLong":"9223372036854775807"}`)
	if err != nil {
		t.Fatal(err)
	}
	if long != int64(9223372036854775807) {
		t.Fatal("int64 ID lost precision")
	}
	if value, err := mongoID("1.25"); err != nil || value != float64(1.25) {
		t.Fatal("numeric double ID became string")
	}
	if value, err := mongoID("false"); err != nil || value != false {
		t.Fatal("boolean ID became string")
	}
}

func TestMongoWriteLockCoversEveryMutation(t *testing.T) {
	d := &mongoDriver{}
	for _, path := range []string{"/api/document-store/insert-document", "/api/document-store/update-document", "/api/document-store/delete-document", "/api/mongo/create-database", "/api/mongo/drop-database", "/api/mongo/create-collection", "/api/mongo/drop-collection", "/api/mongo/rename-collection", "/api/mongo/create-index", "/api/mongo/drop-indexes", "/api/mongo/insert-documents", "/api/mongo/update-documents", "/api/mongo/delete-documents", "/api/mongo/replace-document", "/api/mongo/find-one-and-update", "/api/mongo/find-one-and-replace", "/api/mongo/find-one-and-delete", "/api/mongo/bulk-write", "/api/mongo/create-user", "/api/mongo/clone-collection"} {
		_, err := d.Handle(context.Background(), path, nil, true)
		if err == nil || !strings.Contains(err.Error(), "locked") {
			t.Errorf("unprotected mutation %s: %v", path, err)
		}
	}
	for _, value := range []any{-1, 10001, "bad", 1.5} {
		if _, err := mongoRowLimit(map[string]any{"limit": value}, "limit"); err == nil {
			t.Fatalf("invalid limit accepted %v", value)
		}
	}
}
