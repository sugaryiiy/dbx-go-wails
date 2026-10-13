package database

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

func jsonObject(source string) (map[string]any, error) {
	if strings.TrimSpace(source) == "" {
		return map[string]any{}, nil
	}
	var object map[string]any
	decoder := json.NewDecoder(strings.NewReader(source))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, errors.New("expected a JSON object")
	}
	return object, nil
}
func parseElasticRequest(source string) (string, string, any, error) {
	lines := strings.SplitN(strings.TrimSpace(source), "\n", 2)
	head := strings.Fields(lines[0])
	if len(head) == 0 {
		return "", "", nil, errors.New("query is empty")
	}
	method := http.MethodGet
	endpoint := head[0]
	switch strings.ToUpper(head[0]) {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD":
		if len(head) != 2 {
			return "", "", nil, errors.New("REST query requires METHOD /path")
		}
		method = strings.ToUpper(head[0])
		endpoint = head[1]
	default:
		if len(head) != 1 {
			return "", "", nil, errors.New("expected SQL or a REST request")
		}
	}
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	var body any
	if len(lines) == 2 && strings.TrimSpace(lines[1]) != "" {
		value, err := jsonObject(lines[1])
		if err != nil {
			return "", "", nil, err
		}
		body = value
	}
	return method, endpoint, body, nil
}
func elasticReadRequest(method, endpoint string, body any) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	path := strings.TrimSuffix(u.Path, "/")
	if method == http.MethodHead {
		return path != "" && !strings.Contains(path, "/_")
	}
	if method != http.MethodGet && method != http.MethodPost {
		return false
	}
	if path == "/" && method == http.MethodGet {
		return true
	}
	for _, suffix := range []string{"/_search", "/_msearch", "/_count", "/_field_caps", "/_validate/query", "/_sql/translate"} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	if path == "/_sql" {
		query := str(mapValue(body), "query")
		_, err := ValidateMCPSQL(query, "postgres", true, false)
		return err == nil
	}
	if method != http.MethodGet {
		return false
	}
	if strings.HasPrefix(path, "/_cat/") || strings.HasPrefix(path, "/_nodes") || path == "/_cluster/health" || path == "/_cluster/state" {
		return true
	}
	for _, suffix := range []string{"/_mapping", "/_settings", "/_stats", "/_alias", "/_aliases"} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return strings.Contains(path, "/_doc/") && !strings.HasSuffix(path, "/_doc/")
}
func (d *serviceDriver) handleElastic(ctx context.Context, path string, p map[string]any, locked bool) (any, error) {
	index := str(p, "collection", "index", "table")
	indexPath := "/" + url.PathEscape(index)
	query := url.Values{}
	if routing := str(p, "routing"); routing != "" {
		query.Set("routing", routing)
	}
	switch path {
	case "/api/document-store/list-databases":
		if err := d.Ping(ctx); err != nil {
			return nil, err
		}
		return []string{"default"}, nil
	case "/api/document-store/list-collections", "/api/schema/tables", "/api/schema/objects":
		response, err := d.request(ctx, http.MethodGet, "/_aliases", nil, nil)
		if err != nil {
			return nil, err
		}
		entries := mapValue(response.body)
		names := []string{}
		for name := range entries {
			names = append(names, name)
		}
		sort.Strings(names)
		result := []map[string]any{}
		for _, name := range names {
			aliases := []string{}
			for alias := range mapValue(mapValue(entries[name])["aliases"]) {
				aliases = append(aliases, alias)
			}
			sort.Strings(aliases)
			result = append(result, map[string]any{"name": name, "id": name, "kind": "index", "aliases": aliases, "table_type": "TABLE"})
		}
		return result, nil
	case "/api/schema/databases":
		if err := d.Ping(ctx); err != nil {
			return nil, err
		}
		return []map[string]any{{"name": "default"}}, nil
	case "/api/schema/schemas":
		if err := d.Ping(ctx); err != nil {
			return nil, err
		}
		return []string{"default"}, nil
	case "/api/document-store/elasticsearch/index-metadata":
		kind := str(p, "kind")
		if kind != "mapping" && kind != "settings" && kind != "stats" {
			return nil, errors.New("invalid Elasticsearch metadata kind")
		}
		response, err := d.request(ctx, http.MethodGet, indexPath+"/_"+kind, query, nil)
		return response.body, err
	case "/api/document-store/find-documents":
		if index == "" {
			return nil, errors.New("index is required")
		}
		body, err := jsonObject(str(p, "filter"))
		if err != nil {
			return nil, err
		}
		if len(body) == 0 {
			body = map[string]any{"match_all": map[string]any{}}
		}
		search := map[string]any{"query": body, "from": integer(p, 0, "skip"), "size": integer(p, 100, "limit"), "track_total_hits": true}
		if search["size"].(int) < 1 || search["size"].(int) > 10000 || search["from"].(int) < 0 {
			return nil, errors.New("search limit must be 1–10000 and skip nonnegative")
		}
		if source := str(p, "sort"); source != "" {
			var sortValue any
			if json.Unmarshal([]byte(source), &sortValue) != nil {
				return nil, errors.New("invalid sort JSON")
			}
			search["sort"] = sortValue
		}
		if source := str(p, "projection"); source != "" {
			var projection any
			if json.Unmarshal([]byte(source), &projection) != nil {
				return nil, errors.New("invalid projection JSON")
			}
			search["_source"] = projection
		}
		response, err := d.request(ctx, http.MethodPost, indexPath+"/_search", query, search)
		if err != nil {
			return nil, err
		}
		hits := mapValue(mapValue(response.body)["hits"])
		documents := []any{}
		for _, hit := range anySlice(hits["hits"]) {
			row := mapValue(hit)
			doc := mapValue(row["_source"])
			doc["_id"] = row["_id"]
			doc["_index"] = row["_index"]
			documents = append(documents, doc)
		}
		total := hits["total"]
		exact := true
		if metadata, ok := total.(map[string]any); ok {
			total = metadata["value"]
			exact = metadata["relation"] == "eq"
		}
		return map[string]any{"documents": documents, "total": total, "total_is_exact": exact}, nil
	case "/api/document-store/count-documents", "/api/document-store/elasticsearch-count-documents":
		body, err := jsonObject(str(p, "filter"))
		if err != nil {
			return nil, err
		}
		if len(body) == 0 {
			body = map[string]any{"match_all": map[string]any{}}
		}
		response, err := d.request(ctx, http.MethodPost, indexPath+"/_count", query, map[string]any{"query": body})
		return mapValue(response.body)["count"], err
	case "/api/document-store/insert-document", "/api/document-store/update-document", "/api/document-store/delete-document", "/api/document-store/elasticsearch/documents/delete-all":
		if locked {
			return nil, errors.New("writes are locked; unlock writes before changing Elasticsearch documents")
		}
		if index == "" {
			return nil, errors.New("index is required")
		}
		endpoint := indexPath + "/_doc"
		method := http.MethodPost
		var body any
		if path == "/api/document-store/elasticsearch/documents/delete-all" {
			endpoint = indexPath + "/_delete_by_query"
			body = map[string]any{"query": map[string]any{"match_all": map[string]any{}}}
		} else if path == "/api/document-store/delete-document" {
			method = http.MethodDelete
			endpoint += "/" + url.PathEscape(str(p, "id"))
		} else {
			doc, err := jsonObject(str(p, "docJson"))
			if err != nil {
				return nil, err
			}
			if path == "/api/document-store/update-document" {
				endpoint = indexPath + "/_update/" + url.PathEscape(str(p, "id"))
				body = map[string]any{"doc": doc}
			} else {
				body = doc
			}
		}
		query.Set("refresh", "wait_for")
		response, err := d.request(ctx, method, endpoint, query, body)
		if err != nil {
			return nil, err
		}
		result := mapValue(response.body)
		if path == "/api/document-store/insert-document" {
			return result["_id"], nil
		}
		if path == "/api/document-store/elasticsearch/documents/delete-all" {
			return result, nil
		}
		if result["result"] == "not_found" || result["result"] == "noop" {
			return 0, nil
		}
		return 1, nil
	case "/api/query/execute":
		started := time.Now()
		source := str(p, "sql")
		method := ""
		endpoint := ""
		var body any
		var err error
		if firstWord(source, "postgres") == "SELECT" || firstWord(source, "postgres") == "WITH" {
			if locked {
				if _, err = ValidateMCPSQL(source, "postgres", true, false); err != nil {
					return nil, err
				}
			}
			method = http.MethodPost
			endpoint = "/_sql"
			body = map[string]any{"query": source, "fetch_size": integer(p, 1000, "maxRows")}
		} else {
			method, endpoint, body, err = parseElasticRequest(source)
			if err != nil {
				return nil, err
			}
			if locked && !elasticReadRequest(method, endpoint, body) {
				return nil, errors.New("writes are locked; this Elasticsearch API is not verified read-only")
			}
		}
		response, err := d.request(ctx, method, endpoint, nil, body)
		if err != nil {
			return nil, err
		}
		result := emptyResult()
		result.ExecutionTimeMS = time.Since(started).Milliseconds()
		object := mapValue(response.body)
		if columns := anySlice(object["columns"]); len(columns) > 0 {
			for _, column := range columns {
				metadata := mapValue(column)
				result.Columns = append(result.Columns, str(metadata, "name"))
				result.ColumnTypes = append(result.ColumnTypes, str(metadata, "type"))
			}
			for _, row := range anySlice(object["rows"]) {
				result.Rows = append(result.Rows, anySlice(row))
			}
			if cursor := str(object, "cursor"); cursor != "" {
				_, _ = d.request(ctx, http.MethodPost, "/_sql/close", nil, map[string]any{"cursor": cursor})
				result.Truncated = true
				result.HasMore = false
			}
		} else {
			result.Columns = []string{"response"}
			result.Rows = [][]any{{response.body}}
		}
		data, _ := json.Marshal(result)
		var output map[string]any
		_ = json.Unmarshal(data, &output)
		output["elasticsearch_raw_body"] = string(response.raw)
		return output, nil
	}
	return nil, unsupported(path)
}
func anySlice(value any) []any {
	if list, ok := value.([]any); ok {
		return list
	}
	return []any{}
}
func (d *serviceDriver) handleMeilisearch(ctx context.Context, path string, p map[string]any, locked bool) (any, error) {
	index := str(p, "collection", "index", "table")
	base := "/indexes/" + url.PathEscape(index)
	switch path {
	case "/api/document-store/list-databases":
		if err := d.Ping(ctx); err != nil {
			return nil, err
		}
		return []string{"default"}, nil
	case "/api/document-store/list-collections":
		response, err := d.request(ctx, http.MethodGet, "/indexes", url.Values{"limit": {"10000"}}, nil)
		if err != nil {
			return nil, err
		}
		result := []map[string]any{}
		for _, entry := range anySlice(mapValue(response.body)["results"]) {
			uid := str(mapValue(entry), "uid")
			result = append(result, map[string]any{"name": uid, "id": uid, "kind": "index"})
		}
		return result, nil
	case "/api/document-store/find-documents":
		body := map[string]any{"offset": integer(p, 0, "skip"), "limit": integer(p, 100, "limit")}
		if body["limit"].(int) < 1 || body["limit"].(int) > 10000 || body["offset"].(int) < 0 {
			return nil, errors.New("invalid search pagination")
		}
		if filter := str(p, "filter"); filter != "" {
			parsed, err := jsonObject(filter)
			if err != nil {
				body["filter"] = filter
			} else {
				for k, v := range parsed {
					body[k] = v
				}
			}
		}
		response, err := d.request(ctx, http.MethodPost, base+"/search", nil, body)
		if err != nil {
			return nil, err
		}
		object := mapValue(response.body)
		total := object["totalHits"]
		exact := true
		if total == nil {
			total = object["estimatedTotalHits"]
			exact = false
		}
		return map[string]any{"documents": object["hits"], "total": total, "total_is_exact": exact}, nil
	case "/api/document-store/count-documents":
		response, err := d.request(ctx, http.MethodGet, base+"/stats", nil, nil)
		return mapValue(response.body)["numberOfDocuments"], err
	case "/api/document-store/insert-document", "/api/document-store/update-document", "/api/document-store/delete-document":
		if locked {
			return nil, errors.New("writes are locked; unlock writes before changing Meilisearch documents")
		}
		return nil, unsupported("Meilisearch asynchronous task confirmation")
	}
	return nil, unsupported(path)
}
