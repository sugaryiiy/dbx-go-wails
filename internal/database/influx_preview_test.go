package database

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInfluxMetadataPreservesCollidingTagAndFieldNames(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/buckets" {
			fmt.Fprint(w, `{"buckets":[]}`)
			return
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("query envelope")
			return
		}
		fmt.Fprint(w, "#datatype,string,long,string\n#default,_result,,\n,result,table,_value\n")
		if strings.Contains(str(body, "query"), "measurementTagKeys") {
			fmt.Fprint(w, ",,0,dev_name\n,,0,dev_name [tag]\n,,0,time\n")
		} else {
			fmt.Fprint(w, ",,0,dev_name\n,,0,value\n,,0,time\n")
		}
	}))
	defer backend.Close()
	value, err := openService(context.Background(), Config{Type: "influxdb", Host: backend.URL, Password: "token", Raw: map[string]any{"external_config": map[string]any{"version": "2", "org": "org"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer value.Close()
	columns, err := value.(*serviceDriver).influxColumns(context.Background(), "db", "machine_data")
	if err != nil {
		t.Fatal(err)
	}
	tagRenames, fieldRenames := map[string]string{}, map[string]string{}
	seen := map[string]bool{}
	for _, col := range columns {
		name, extra := str(col, "name"), str(col, "extra")
		if seen[name] {
			t.Fatal("duplicate display column", name)
		}
		seen[name] = true
		if strings.HasPrefix(extra, "influxdb:tag:") {
			tagRenames[strings.TrimPrefix(extra, "influxdb:tag:")] = name
		}
		if strings.HasPrefix(extra, "influxdb:field:") {
			fieldRenames[strings.TrimPrefix(extra, "influxdb:field:")] = name
		}
	}
	if tagRenames["dev_name"] != "dev_name [tag]_" || tagRenames["time"] != "time [tag]" || fieldRenames["time"] != "time [field]" || !seen["dev_name"] {
		t.Fatal(columns)
	}
	source, err := BuildInfluxPreview(InfluxPreviewOptions{Version: "2", Database: "db", Table: "machine_data", Limit: 100, TagRenames: tagRenames, FieldRenames: fieldRenames})
	if err != nil || !strings.Contains(source, `rename(fn: (column) => if column == "dev_name" then "dev_name [tag]_" else column)`) || !strings.Contains(source, `r._field == "time" then "time [field]"`) {
		t.Fatal(source, err)
	}
	if _, ok := ParseInfluxPreview(source); !ok {
		t.Fatal("safe aliases not canonical")
	}
}

func TestInfluxPreviewSafeNamesFilterSortAndCanonicalValidation(t *testing.T) {
	o := InfluxPreviewOptions{Version: "2", Database: `db & ${http.post()}`, Table: "机台\"\\\n${x}",
		Columns: []string{"time", "host", "result", "table"}, Limit: 20, Offset: 40, DefaultTime: true}
	source, err := BuildInfluxPreview(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`range(start: -5m)`, `pivot(rowKey: ["_time"]`, `group(columns: [])`, `sort(columns: ["time"], desc: true)`, `limit(n: 20, offset: 40)`, `\${http.post()}`, `keep(columns: ["time", "host", "result", "table"])`} {
		if !strings.Contains(source, want) {
			t.Errorf("missing %s in %s", want, source)
		}
	}
	if _, ok := ParseInfluxPreview(source); !ok {
		t.Fatal("generated query not verified")
	}
	for _, unsafe := range []string{source + "\n |> to(bucket: \"other\")", strings.Replace(source, "limit(n: 20", "limit(n: 21", 1)} {
		if _, ok := ParseInfluxPreview(unsafe); ok {
			t.Fatal("modified query trusted")
		}
	}
	o.Where = `WHERE ("host" = 'a''b' OR "host" IS NULL) AND time > now() - 1h AND "value" >= 2.5`
	o.Order = `"value" DESC, "time" DESC`
	source, err = BuildInfluxPreview(o)
	if err != nil || !strings.Contains(source, `date.sub(d: 1h, from: now())`) || !strings.Contains(source, `r["host"] == "a'b"`) || !strings.Contains(source, `sort(columns: ["value", "time"], desc: true)`) {
		t.Fatal(source, err)
	}
	if _, ok := ParseInfluxPreview(source); !ok {
		t.Fatal("filtered query not verified")
	}
	for _, filter := range []string{`1=1; DROP MEASUREMENT cpu`, `host = 'x' |> to(bucket: "other")`, `time > http.post()`, `host = 'x' // malicious`, `host = 'x' OR true`, `host = '${to(bucket: "x")}' ; DELETE FROM cpu`} {
		o.Where = filter
		if _, err := BuildInfluxPreview(o); err == nil {
			t.Errorf("unsafe filter accepted: %s", filter)
		}
		// Even a client-created descriptor cannot authorize a malicious program.
		raw, _ := json.Marshal(o)
		forged := influxPreviewPrefix + base64.RawURLEncoding.EncodeToString(raw) + "\nfrom(bucket: \"db\") |> to(bucket: \"other\")"
		if _, ok := ParseInfluxPreview(forged); ok {
			t.Fatal("forged descriptor trusted")
		}
	}
}

func TestInfluxPreviewV1AndExplicitUnsupportedExpressions(t *testing.T) {
	source, err := BuildInfluxPreview(InfluxPreviewOptions{Version: "1", Table: `cpu"a`, Limit: 10, Offset: 20, DefaultTime: true})
	if err != nil || source != `SELECT * FROM "cpu\"a" WHERE time > now() - 5m ORDER BY time DESC LIMIT 10 OFFSET 20` {
		t.Fatal(source, err)
	}
	for _, o := range []InfluxPreviewOptions{
		{Version: "1", Table: "cpu", Count: true},
		{Version: "2", Database: "db", Table: "cpu", Where: "host LIKE '%x%'"},
		{Version: "2", Database: "db", Table: "cpu", Order: "host ASC, time DESC"},
		{Version: "2", Database: "db", Table: "cpu", Limit: -1},
		{Version: "2", Database: "db", Table: "cpu", Offset: -1},
	} {
		if _, err := BuildInfluxPreview(o); err == nil {
			t.Fatalf("unsupported options accepted: %+v", o)
		}
	}
}

func TestInfluxPreviewCSVKeepsRealColumnsNamedResultAndTable(t *testing.T) {
	source := ",result,table,time,result,table\n,,0,2026-10-09T00:00:00Z,user-result,user-table\n"
	result, err := parseInfluxCSVData(source, 10, true)
	if err != nil || strings.Join(result.Columns, ",") != "time,result,table" || result.Rows[0][1] != "user-result" {
		t.Fatal(result, err)
	}
}
