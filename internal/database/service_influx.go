package database

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

func influxVersion(c Config) string {
	version := strings.ToLower(strings.TrimSpace(str(mapValue(c.Raw["external_config"]), "version")))
	if version == "" {
		return "1"
	}
	return strings.TrimPrefix(version, "v")
}
func validateInfluxConfig(c Config) error {
	switch influxVersion(c) {
	case "1":
	case "2":
		if strings.TrimSpace(str(mapValue(c.Raw["external_config"]), "org", "organization")) == "" {
			return errors.New("InfluxDB 2.x organization is required")
		}
		if strings.TrimSpace(c.Password) == "" {
			return errors.New("InfluxDB 2.x token is required")
		}
	default:
		return unsupported("InfluxDB version " + influxVersion(c))
	}
	params, err := url.ParseQuery(c.URLParams)
	if err != nil {
		return errors.New("invalid InfluxDB URL parameters")
	}
	for key := range params {
		if key != "epoch" && key != "pretty" {
			return unsupported("InfluxDB URL parameter " + key)
		}
	}
	return nil
}
func (d *serviceDriver) influxOrg() string {
	return strings.TrimSpace(str(d.external, "org", "organization"))
}
func (d *serviceDriver) influxInfo(ctx context.Context) (map[string]any, error) {
	if err := d.Ping(ctx); err != nil {
		return nil, err
	}
	info := map[string]any{"productName": "InfluxDB", "driverName": "Go HTTP", "currentDatabase": d.config.Database}
	endpoint := "/ping"
	if influxVersion(d.config) == "2" {
		endpoint = "/health"
	}
	// Older compatible gateways may omit health/version endpoints. The
	// authenticated database probe above still determines connection success.
	if response, err := d.request(ctx, http.MethodGet, endpoint, nil, nil); err == nil {
		version := str(mapValue(response.body), "version")
		if version == "" {
			version = response.header.Get("X-Influxdb-Version")
		}
		if version != "" {
			info["productVersion"] = version
		}
	}
	return info, ctx.Err()
}
func (d *serviceDriver) influxDatabases(ctx context.Context) ([]map[string]any, error) {
	if influxVersion(d.config) == "2" {
		result := []map[string]any{}
		for offset := 0; offset < 10000; offset += 100 {
			response, err := d.request(ctx, http.MethodGet, "/api/v2/buckets", url.Values{"org": {d.influxOrg()}, "limit": {"100"}, "offset": {strconv.Itoa(offset)}}, nil)
			if err != nil {
				return nil, err
			}
			body, ok := response.body.(map[string]any)
			if !ok {
				return nil, errors.New("invalid InfluxDB bucket response")
			}
			buckets, ok := body["buckets"].([]any)
			if !ok {
				return nil, errors.New("invalid InfluxDB bucket list")
			}
			for _, bucket := range buckets {
				name := str(mapValue(bucket), "name")
				if name == "" {
					return nil, errors.New("InfluxDB bucket has no name")
				}
				result = append(result, map[string]any{"name": name})
			}
			if len(buckets) < 100 {
				return result, nil
			}
		}
		return nil, errors.New("InfluxDB bucket list exceeds 10000 entries")
	}
	result, err := d.influxQuery(ctx, "", "SHOW DATABASES", 10000)
	if err != nil && d.config.Database != "" {
		if _, probeErr := d.influxQuery(ctx, d.config.Database, "SHOW MEASUREMENTS", 1); probeErr == nil {
			return []map[string]any{{"name": d.config.Database}}, nil
		}
	}
	if err != nil {
		return nil, err
	}
	if result.Truncated {
		return nil, errors.New("InfluxDB database list exceeds 10000 entries")
	}
	return influxNames(result, "name", false), nil
}
func influxNames(result QueryResult, column string, tables bool) []map[string]any {
	index := -1
	for i, name := range result.Columns {
		if name == column {
			index = i
		}
	}
	entries := []map[string]any{}
	seen := map[string]bool{}
	for _, row := range result.Rows {
		if index < 0 || index >= len(row) || row[index] == nil {
			continue
		}
		name := fmt.Sprint(row[index])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		entry := map[string]any{"name": name}
		if tables {
			entry["table_type"] = "TABLE"
		}
		entries = append(entries, entry)
	}
	return entries
}
func (d *serviceDriver) influxQuery(ctx context.Context, database, source string, maxRows int) (QueryResult, error) {
	start := time.Now()
	result := emptyResult()
	if influxVersion(d.config) == "2" && !influxQLSource(source) {
		body := map[string]any{"query": source, "type": "flux", "dialect": map[string]any{"header": true, "annotations": []string{"datatype", "group", "default"}}}
		response, err := d.request(ctx, http.MethodPost, "/api/v2/query", url.Values{"org": {d.influxOrg()}}, body)
		if err != nil {
			return result, err
		}
		o, preview := ParseInfluxPreview(source)
		result, err = parseInfluxCSVData(string(response.raw), maxRows, preview)
		if preview {
			if o.Count && err == nil && len(result.Rows) == 0 {
				result.Columns, result.Rows = []string{"cnt"}, [][]any{{int64(0)}}
				result.AffectedRows = 1
			}
		}
		result.ExecutionTimeMS = time.Since(start).Milliseconds()
		return result, d.redact(err)
	}
	query, _ := url.ParseQuery(d.config.URLParams)
	query.Set("q", source)
	if database != "" {
		query.Set("db", database)
	}
	method := http.MethodPost
	if influxQLReadOnly(source) {
		method = http.MethodGet
	}
	response, err := d.request(ctx, method, "/query", query, nil)
	if err != nil {
		return result, err
	}
	var envelope struct {
		Error   string `json:"error"`
		Results []struct {
			Error  string `json:"error"`
			Series []struct {
				Columns []string          `json:"columns"`
				Values  [][]any           `json:"values"`
				Tags    map[string]string `json:"tags"`
			} `json:"series"`
		} `json:"results"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(response.raw)))
	decoder.UseNumber()
	if err = decoder.Decode(&envelope); err != nil || envelope.Results == nil && envelope.Error == "" {
		return result, errors.New("invalid InfluxDB query response")
	}
	if envelope.Error != "" {
		return result, d.redact(errors.New(envelope.Error))
	}
	for _, statement := range envelope.Results {
		if statement.Error != "" {
			return result, d.redact(errors.New(statement.Error))
		}
		for _, series := range statement.Series {
			columns := append([]string{}, series.Columns...)
			tags := []string{}
			for name := range series.Tags {
				tags = append(tags, name)
			}
			sort.Strings(tags)
			for _, name := range tags {
				found := false
				for _, column := range columns {
					if column == name {
						found = true
					}
				}
				if !found {
					columns = append(columns, name)
				}
			}
			for _, row := range series.Values {
				if len(row) != len(series.Columns) {
					return result, errors.New("invalid InfluxDB series row")
				}
				values := make([]any, len(columns))
				copy(values, row)
				for i, name := range columns[len(series.Columns):] {
					values[len(series.Columns)+i] = series.Tags[name]
				}
				appendInfluxRow(&result, columns, values, maxRows)
			}
			// Empty series still supply the result's column names.
			mergeInfluxColumns(&result, columns)
		}
	}
	result.AffectedRows = int64(len(result.Rows))
	result.ExecutionTimeMS = time.Since(start).Milliseconds()
	return result, nil
}
func mergeInfluxColumns(result *QueryResult, columns []string) []int {
	indices := make([]int, len(columns))
	for i, name := range columns {
		index := -1
		for j, existing := range result.Columns {
			if existing == name {
				index = j
				break
			}
		}
		if index < 0 {
			index = len(result.Columns)
			result.Columns = append(result.Columns, name)
			for j := range result.Rows {
				result.Rows[j] = append(result.Rows[j], nil)
			}
		}
		indices[i] = index
	}
	return indices
}
func appendInfluxRow(result *QueryResult, columns []string, values []any, maxRows int) {
	indices := mergeInfluxColumns(result, columns)
	if len(result.Rows) >= maxRows {
		result.Truncated = true
		return
	}
	row := make([]any, len(result.Columns))
	for i, index := range indices {
		if i < len(values) {
			value := values[i]
			if n, ok := value.(json.Number); ok {
				value = clickHouseValue(n, "")
			}
			row[index] = value
		}
	}
	result.Rows = append(result.Rows, row)
}
func parseInfluxCSV(source string, maxRows int) (QueryResult, error) {
	return parseInfluxCSVData(source, maxRows, false)
}
func parseInfluxCSVData(source string, maxRows int, hideTransportColumns bool) (QueryResult, error) {
	result := emptyResult()
	reader := csv.NewReader(strings.NewReader(source))
	reader.FieldsPerRecord = -1
	var header, defaults []string
	headerPending := true
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return result, errors.New("invalid InfluxDB Flux CSV response")
		}
		if len(record) == 0 {
			continue
		}
		if strings.HasPrefix(record[0], "#") {
			if record[0] == "#datatype" {
				headerPending = true
				defaults = nil
			}
			if record[0] == "#default" {
				defaults = append([]string{}, record...)
			}
			continue
		}
		if headerPending {
			isError := record[0] == "error" || len(record) > 1 && record[0] == "" && record[1] == "error"
			if !isError && (len(record) < 3 || record[0] != "" || record[1] != "result" || record[2] != "table") {
				return result, errors.New("invalid InfluxDB Flux CSV header")
			}
			header = append([]string{}, record...)
			if !isError {
				offset := 1
				if hideTransportColumns {
					offset = 3
				}
				mergeInfluxColumns(&result, header[offset:])
			}
			headerPending = false
			continue
		}
		if len(record) != len(header) {
			return result, errors.New("invalid InfluxDB Flux row width")
		}
		if len(header) > 0 && (header[0] == "error" || len(header) > 1 && header[1] == "error") {
			index := 0
			if header[0] == "" {
				index = 1
			}
			return result, fmt.Errorf("InfluxDB Flux error: %s", record[index])
		}
		offset := 0
		if len(header) > 0 && header[0] == "" {
			offset = 1
			if hideTransportColumns {
				offset = 3
			}
		}
		values := make([]any, len(header)-offset)
		for i := offset; i < len(record); i++ {
			value := record[i]
			if value == "" && i < len(defaults) {
				value = defaults[i]
			}
			if value != "" {
				values[i-offset] = value
			}
		}
		appendInfluxRow(&result, header[offset:], values, maxRows)
	}
	result.AffectedRows = int64(len(result.Rows))
	return result, nil
}
func influxQLReadOnly(source string) bool {
	// Reject ambiguous backslash quoting rather than letting the SQL scanner
	// and InfluxQL disagree about a statement boundary or an INTO target.
	if strings.Contains(source, "\\") {
		return false
	}
	statements, err := scanSQL(source, "influxdb")
	if err != nil || len(statements) == 0 {
		return false
	}
	for _, statement := range statements {
		if len(statement.words) == 0 || statement.words[0] != "SHOW" && statement.words[0] != "SELECT" {
			return false
		}
		for _, word := range statement.words {
			if word == "INTO" {
				return false
			}
		}
	}
	return true
}
func influxIdentifier(name string) string {
	return `"` + strings.NewReplacer("\\", "\\\\", `"`, `\"`, "\r", `\r`, "\n", `\n`).Replace(name) + `"`
}
func fluxString(value string) string {
	// Flux interpolates ${...} inside double quoted strings; JSON quoting alone
	// does not protect measurement/bucket/column names from that interpretation.
	var quoted strings.Builder
	quoted.WriteByte('"')
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '\\':
			quoted.WriteString(`\\`)
		case '"':
			quoted.WriteString(`\"`)
		case '\n':
			quoted.WriteString(`\n`)
		case '\r':
			quoted.WriteString(`\r`)
		case '\t':
			quoted.WriteString(`\t`)
		case '$':
			if i+1 < len(value) && value[i+1] == '{' {
				quoted.WriteByte('\\')
			}
			quoted.WriteByte('$')
		default:
			if value[i] < 32 {
				fmt.Fprintf(&quoted, `\x%02x`, value[i])
			} else {
				quoted.WriteByte(value[i])
			}
		}
	}
	quoted.WriteByte('"')
	return quoted.String()
}
func influxQLSource(source string) bool {
	statements, err := scanSQL(source, "influxdb")
	if err != nil || len(statements) == 0 || len(statements[0].words) == 0 {
		return false
	}
	switch statements[0].words[0] {
	case "SELECT", "SHOW", "CREATE", "DROP", "DELETE", "ALTER", "GRANT", "REVOKE":
		return true
	}
	return false
}
func (d *serviceDriver) handleInflux(ctx context.Context, path string, p map[string]any, locked bool) (any, error) {
	database := str(p, "database")
	if database == "" {
		database = d.config.Database
	}
	table := str(p, "table", "name")
	v2 := influxVersion(d.config) == "2"
	switch path {
	case "/api/schema/databases", "/api/schema/database-metadata":
		return d.influxDatabases(ctx)
	case "/api/schema/schemas":
		return []string{}, nil
	case "/api/schema/tables", "/api/schema/objects", "/api/schema/completion-objects":
		query := "SHOW MEASUREMENTS"
		column := "name"
		if v2 {
			query = `import "influxdata/influxdb/schema"` + "\n" + "schema.measurements(bucket: " + fluxString(database) + ", start: 0)"
			column = "_value"
		}
		result, err := d.influxQuery(ctx, database, query, 10000)
		if err != nil {
			return nil, err
		}
		if result.Truncated {
			return nil, errors.New("InfluxDB measurement list exceeds 10000 entries")
		}
		return influxNames(result, column, true), nil
	case "/api/schema/columns":
		return d.influxColumns(ctx, database, table)
	case "/api/query/execute", "/api/query/execute-multi":
		source := str(p, "sql")
		if strings.TrimSpace(source) == "" {
			return nil, errors.New("query is empty")
		}
		_, verifiedPreview := ParseInfluxPreview(source)
		if locked && !verifiedPreview && !influxQLReadOnly(source) {
			return nil, errors.New("writes are locked; this InfluxDB query cannot be verified as read-only")
		}
		maxRows := integer(p, 10000, "maxRows")
		if maxRows <= 0 {
			maxRows = 10000
		}
		if maxRows > 100000 {
			return nil, errors.New("maxRows must not exceed 100000")
		}
		result, err := d.influxQuery(ctx, database, source, maxRows)
		if path == "/api/query/execute-multi" {
			return []QueryResult{result}, err
		}
		return result, err
	}
	return nil, unsupported(path)
}
func (d *serviceDriver) influxColumns(ctx context.Context, database, table string) ([]map[string]any, error) {
	if database == "" || table == "" {
		return nil, errors.New("InfluxDB database and measurement are required")
	}
	columns := []map[string]any{{"name": "time", "data_type": "timestamp", "is_nullable": false, "is_primary_key": true, "extra": "influxdb:time"}}
	indices := map[string]int{"time": 0}
	namesByKind := map[string][]string{}
	for _, kind := range []string{"tag", "field"} {
		query := "SHOW " + strings.ToUpper(kind) + " KEYS FROM " + influxIdentifier(table)
		nameColumn := kind + "Key"
		if influxVersion(d.config) == "2" {
			fn := "measurementTagKeys"
			if kind == "field" {
				fn = "measurementFieldKeys"
			}
			query = `import "influxdata/influxdb/schema"` + "\n" + "schema." + fn + "(bucket: " + fluxString(database) + ", measurement: " + fluxString(table) + ", start: 0)"
			nameColumn = "_value"
		}
		result, err := d.influxQuery(ctx, database, query, 10000)
		if err != nil {
			return nil, err
		}
		if result.Truncated {
			return nil, errors.New("InfluxDB column list exceeds 10000 entries")
		}
		for _, entry := range influxNames(result, nameColumn, false) {
			name := str(entry, "name")
			if influxVersion(d.config) == "2" && (name == "_start" || name == "_stop" || name == "_time" || name == "_value" || name == "_field" || name == "_measurement") {
				continue
			}
			namesByKind[kind] = append(namesByKind[kind], name)
			if index, exists := indices[name]; exists {
				columns[index]["extra"] = "influxdb:ambiguous"
				columns[index]["is_primary_key"] = false
				continue
			}
			typ := "string"
			if kind == "field" {
				typ = "field"
			}
			indices[name] = len(columns)
			columns = append(columns, map[string]any{"name": name, "data_type": typ, "is_nullable": true, "is_primary_key": kind == "tag", "extra": "influxdb:" + kind})
		}
	}
	if influxVersion(d.config) == "2" {
		// Flux pivot cannot place a field into a column already occupied by a
		// tag. Keep both values with deterministic, collision-free aliases.
		columns = []map[string]any{{"name": "time", "data_type": "timestamp", "is_nullable": false, "is_primary_key": true, "extra": "influxdb:time"}}
		used := map[string]bool{"time": true}
		fields := map[string]bool{}
		for _, names := range namesByKind {
			for _, name := range names {
				used[name] = true
			}
		}
		for _, name := range namesByKind["field"] {
			fields[name] = true
		}
		for _, kind := range []string{"tag", "field"} {
			for _, name := range namesByKind[kind] {
				alias, extra := name, "influxdb:"+kind
				if name == "time" || kind == "tag" && fields[name] {
					alias = name + " [" + kind + "]"
					for used[alias] {
						alias += "_"
					}
					used[alias] = true
					extra += ":" + name
				}
				typ := "string"
				if kind == "field" {
					typ = "field"
				}
				columns = append(columns, map[string]any{"name": alias, "data_type": typ, "is_nullable": true, "is_primary_key": kind == "tag", "extra": extra})
			}
		}
	}
	return columns, nil
}
