package database

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

func consulMetadata(header http.Header) map[string]any {
	result := map[string]any{"index": nil, "filteredByAcls": nil, "knownLeader": nil, "lastContact": nil, "queryBackend": nil}
	for target, key := range map[string]string{"index": "X-Consul-Index", "lastContact": "X-Consul-LastContact", "queryBackend": "X-Consul-Query-Backend"} {
		if value := header.Get(key); value != "" {
			result[target] = value
		}
	}
	for target, key := range map[string]string{"filteredByAcls": "X-Consul-Results-Filtered-By-ACLs", "knownLeader": "X-Consul-KnownLeader"} {
		if value := header.Get(key); value != "" {
			result[target] = strings.EqualFold(value, "true")
		}
	}
	return result
}
func consulKVValue(value any) ([]byte, error) {
	object := mapValue(value)
	data := str(object, "data")
	switch str(object, "encoding") {
	case "utf8":
		return []byte(data), nil
	case "base64":
		return base64.StdEncoding.DecodeString(data)
	default:
		return nil, errors.New("KV value encoding must be utf8 or base64")
	}
}
func consulDecodeEntry(entry map[string]any) (map[string]any, error) {
	data, err := base64.StdEncoding.DecodeString(str(entry, "Value"))
	if err != nil {
		return nil, err
	}
	encoding := "utf8"
	value := string(data)
	if !utf8.Valid(data) {
		encoding = "base64"
		value = base64.StdEncoding.EncodeToString(data)
	}
	metadata := map[string]any{"createRevision": valueString(entry["CreateIndex"]), "modRevision": valueString(entry["ModifyIndex"]), "flags": valueString(entry["Flags"]), "lockIndex": valueString(entry["LockIndex"]), "session": entry["Session"], "valueSize": len(data)}
	return map[string]any{"found": true, "key": entry["Key"], "value": map[string]any{"encoding": encoding, "data": value}, "metadata": metadata}, nil
}
func (d *serviceDriver) handleConsul(ctx context.Context, path string, p map[string]any, locked bool) (any, error) {
	name := strings.TrimPrefix(path, "/api/consul/")
	endpoint := ""
	method := http.MethodGet
	query := url.Values{}
	var body any
	wrap := false
	escape := url.PathEscape
	options := mapValue(p["options"])
	for _, key := range []string{"filter", "near", "index", "wait"} {
		if value := str(options, key); value != "" {
			query.Set(key, value)
		}
	}
	if name == "list-prefix" {
		prefix := str(p, "prefix")
		query.Set("keys", "")
		query.Set("separator", "/")
		response, err := d.request(ctx, http.MethodGet, "/v1/kv/"+escape(prefix), query, nil)
		var httpErr *serviceHTTPError
		if errors.As(err, &httpErr) && httpErr.status == 404 {
			return map[string]any{"keys": []any{}, "continuation": nil, "revision": response.header.Get("X-Consul-Index")}, nil
		}
		if err != nil {
			return nil, err
		}
		keys := []string{}
		for _, key := range anySlice(response.body) {
			if text, ok := key.(string); ok && text != prefix {
				keys = append(keys, text)
			}
		}
		sort.Strings(keys)
		after := ""
		if cursor := str(p, "continuation"); cursor != "" {
			data, err := base64.RawURLEncoding.DecodeString(cursor)
			if err != nil {
				return nil, errors.New("invalid Consul continuation")
			}
			after = string(data)
		}
		limit := integer(p, 100, "limit")
		if limit < 1 || limit > 500 {
			return nil, errors.New("Consul limit must be 1–500")
		}
		result := []map[string]any{}
		var continuation any
		for _, key := range keys {
			if key <= after {
				continue
			}
			if len(result) == limit {
				continuation = base64.RawURLEncoding.EncodeToString([]byte(valueString(result[len(result)-1]["key"])))
				break
			}
			result = append(result, map[string]any{"key": key})
		}
		return map[string]any{"keys": result, "continuation": continuation, "revision": consulMetadata(response.header)["index"], "filteredByAcls": consulMetadata(response.header)["filteredByAcls"]}, nil
	}
	if name == "get" {
		response, err := d.request(ctx, http.MethodGet, "/v1/kv/"+escape(str(p, "key")), query, nil)
		var httpErr *serviceHTTPError
		if errors.As(err, &httpErr) && httpErr.status == 404 {
			return map[string]any{"found": false, "key": nil, "value": nil, "metadata": nil}, nil
		}
		if err != nil {
			return nil, err
		}
		entries := anySlice(response.body)
		if len(entries) != 1 {
			return nil, errors.New("Consul did not return one key")
		}
		return consulDecodeEntry(mapValue(entries[0]))
	}
	if name == "put" || name == "delete" {
		if locked {
			return nil, errors.New("writes are locked; unlock writes before changing Consul keys")
		}
		key := str(p, "key")
		if key == "" {
			return nil, errors.New("Consul key is required")
		}
		options := mapValue(p["options"])
		cas := str(options, "expectedModRevision")
		creating := str(options, "writeMode") == "create" || str(options, "expectedCreateRevision") == "0"
		if creating {
			cas = "0"
		}
		if cas == "" {
			return nil, errors.New("CONSUL_CAS_REQUIRED: Consul mutations require expectedModRevision or explicit create mode")
		}
		if _, err := strconv.ParseUint(cas, 10, 64); err != nil {
			return nil, errors.New("invalid Consul CAS revision")
		}
		query.Set("cas", cas)
		// CAS protects the revision, while this read also preserves flags and
		// refuses ordinary writes to keys held by a Consul session.
		existing, readErr := d.request(ctx, http.MethodGet, "/v1/kv/"+escape(key), nil, nil)
		var missing *serviceHTTPError
		if readErr != nil && !(errors.As(readErr, &missing) && missing.status == 404) {
			return nil, readErr
		}
		entry := map[string]any{}
		if readErr == nil {
			entries := anySlice(existing.body)
			if len(entries) != 1 {
				return nil, errors.New("Consul did not return one key for mutation validation")
			}
			entry = mapValue(entries[0])
			if str(entry, "Session") != "" {
				return nil, errors.New("CONSUL_KEY_LOCKED: Consul key is held by a session")
			}
			if cas != valueString(entry["ModifyIndex"]) {
				return nil, errors.New("CONSUL_CAS_CONFLICT: Consul key changed before mutation")
			}
		} else if !creating {
			return nil, errors.New("CONSUL_CAS_CONFLICT: Consul key no longer exists")
		}
		flags := str(options, "flags")
		if flags == "" && name == "put" && options["preserveFlags"] != false && entry["Flags"] != nil {
			flags = valueString(entry["Flags"])
		}
		if flags != "" {
			if _, err := strconv.ParseUint(flags, 10, 64); err != nil {
				return nil, errors.New("invalid Consul flags")
			}
			query.Set("flags", flags)
		}
		method = http.MethodPut
		if name == "delete" {
			method = http.MethodDelete
		} else {
			value, err := consulKVValue(p["value"])
			if err != nil {
				return nil, err
			}
			if len(value) > 512<<10 {
				return nil, errors.New("Consul value exceeds 512 KiB")
			}
			body = value
		}
		response, err := d.request(ctx, method, "/v1/kv/"+escape(key), query, body)
		if err != nil {
			return nil, err
		}
		if response.body != true && response.body != "true" {
			return nil, errors.New("CONSUL_CAS_CONFLICT: Consul key changed before mutation")
		}
		if name == "delete" {
			return map[string]any{"deleted": 1, "revision": nil}, nil
		}
		var created any
		if creating {
			created = key
		}
		return map[string]any{"revision": nil, "key": key, "createdKey": created}, nil
	}
	if name == "txn" {
		operations := anySlice(mapValue(p["request"])["operations"])
		if len(operations) < 1 || len(operations) > 64 {
			return nil, errors.New("Consul transaction requires 1–64 operations")
		}
		list := []any{}
		for _, item := range operations {
			operation := mapValue(item)
			verb := str(operation, "verb")
			read := verb == "get" || verb == "get-tree" || strings.HasPrefix(verb, "check-")
			if locked && !read {
				return nil, errors.New("writes are locked; unlock writes before Consul transaction mutations")
			}
			if !strings.Contains("|get|get-tree|check-index|check-not-exists|check-session|set|cas|delete|delete-cas|delete-tree|lock|unlock|", "|"+verb+"|") {
				return nil, errors.New("invalid Consul transaction verb")
			}
			kv := map[string]any{"Verb": verb, "Key": str(operation, "key")}
			if operation["value"] != nil {
				data, err := consulKVValue(operation["value"])
				if err != nil {
					return nil, err
				}
				kv["Value"] = base64.StdEncoding.EncodeToString(data)
			}
			for _, field := range []string{"flags", "index"} {
				if operation[field] != nil {
					number, err := strconv.ParseUint(valueString(operation[field]), 10, 64)
					if err != nil {
						return nil, err
					}
					kv[strings.ToUpper(field[:1])+field[1:]] = number
				}
			}
			if operation["session"] != nil {
				kv["Session"] = operation["session"]
			}
			list = append(list, map[string]any{"KV": kv})
		}
		response, err := d.request(ctx, http.MethodPut, "/v1/txn", query, list)
		if err != nil {
			var conflict *serviceHTTPError
			if errors.As(err, &conflict) && conflict.status == http.StatusConflict {
				failures := []any{}
				for _, entry := range anySlice(mapValue(response.body)["Errors"]) {
					item := mapValue(entry)
					failures = append(failures, map[string]any{"opIndex": item["OpIndex"], "what": item["What"]})
				}
				return map[string]any{"committed": false, "errors": failures, "results": []any{}}, nil
			}
			return nil, err
		}
		result := mapValue(response.body)
		rows := []any{}
		for _, entry := range anySlice(result["Results"]) {
			decoded, err := consulDecodeEntry(mapValue(mapValue(entry)["KV"]))
			if err != nil {
				return nil, err
			}
			metadata := mapValue(decoded["metadata"])
			rows = append(rows, map[string]any{"key": decoded["key"], "value": decoded["value"], "flags": metadata["flags"], "createIndex": metadata["createRevision"], "modifyIndex": metadata["modRevision"], "lockIndex": metadata["lockIndex"], "session": metadata["session"]})
		}
		return map[string]any{"committed": true, "errors": []any{}, "results": rows}, nil
	}
	readEndpoints := map[string]string{"status/leader": "/v1/status/leader", "status/peers": "/v1/status/peers", "agent/self": "/v1/agent/self", "agent/members": "/v1/agent/members", "agent/metrics": "/v1/agent/metrics", "agent/services": "/v1/agent/services", "agent/checks": "/v1/agent/checks", "catalog/datacenters": "/v1/catalog/datacenters", "catalog/nodes": "/v1/catalog/nodes", "catalog/services": "/v1/catalog/services", "sessions": "/v1/session/list", "prepared-query/list": "/v1/query", "event/list": "/v1/event/list", "coordinate/nodes": "/v1/coordinate/nodes", "acl/token/self": "/v1/acl/token/self", "mesh/intentions/list": "/v1/connect/intentions", "mesh/peerings/list": "/v1/peerings"}
	if endpoint = readEndpoints[name]; endpoint != "" {
		wrap = strings.HasPrefix(name, "catalog/") && name != "catalog/datacenters" || name == "sessions"
		if name == "agent/members" {
			query.Set("wan", strconv.FormatBool(boolean(p, "wan")))
			if segment := str(p, "segment"); segment != "" {
				query.Set("segment", segment)
			}
		}
		if name == "event/list" && str(p, "name") != "" {
			query.Set("name", str(p, "name"))
		}
	}
	if name == "capabilities" {
		response, err := d.request(ctx, http.MethodGet, "/v1/agent/self", nil, nil)
		if err != nil {
			return nil, err
		}
		config := mapValue(mapValue(response.body)["Config"])
		capabilities := map[string]any{"version": config["Version"], "datacenter": config["Datacenter"], "nodeName": config["NodeName"], "server": config["Server"], "agent": "supported"}
		probes := map[string]string{"catalog": "/v1/catalog/nodes", "health": "/v1/health/state/any", "sessions": "/v1/session/list", "acl": "/v1/acl/tokens", "configEntries": "/v1/config", "intentions": "/v1/connect/intentions", "peering": "/v1/peerings", "preparedQueries": "/v1/query", "events": "/v1/event/list", "coordinates": "/v1/coordinate/nodes"}
		for capability, api := range probes {
			_, probeErr := d.request(ctx, http.MethodGet, api, nil, nil)
			status := "supported"
			if probeErr != nil {
				status = "unknown"
				var httpErr *serviceHTTPError
				if errors.As(probeErr, &httpErr) {
					if httpErr.status == 403 {
						status = "forbidden"
					} else if httpErr.status == 404 {
						status = "unsupported"
					}
				}
			}
			capabilities[capability] = status
		}
		return capabilities, nil
	}
	if endpoint == "" {
		switch name {
		case "catalog/service-nodes":
			endpoint = "/v1/catalog/service/" + escape(str(p, "name"))
			wrap = true
		case "catalog/node-services":
			endpoint = "/v1/catalog/node/" + escape(str(p, "name"))
			wrap = true
		case "health/node", "health/checks", "health/service", "health/state":
			endpoint = "/v1/" + strings.TrimPrefix(name, "health/")
			endpoint = "/v1/health/" + strings.TrimPrefix(name, "health/") + "/" + escape(str(p, "name"))
			wrap = true
			if name == "health/service" && boolean(p, "passing") {
				query.Set("passing", "")
			}
		case "agent/service":
			endpoint = "/v1/agent/service/" + escape(str(p, "id"))
		case "session":
			endpoint = "/v1/session/info/" + escape(str(p, "id"))
			wrap = true
		case "sessions/node":
			endpoint = "/v1/session/node/" + escape(str(p, "name"))
			wrap = true
		case "prepared-query/read":
			endpoint = "/v1/query/" + escape(str(p, "id"))
		case "prepared-query/execute":
			request := mapValue(p["request"])
			endpoint = "/v1/query/" + escape(str(request, "id", "name")) + "/execute"
		case "prepared-query/explain":
			endpoint = "/v1/query/" + escape(str(p, "id")) + "/explain"
		case "agent/service/register", "agent/check/register":
			method = http.MethodPut
			endpoint = "/v1/" + name
			registration := mapValue(p["registration"])
			wire := map[string]any{}
			for k, v := range registration {
				wire[strings.ToUpper(k[:1])+k[1:]] = v
			}
			if id := registration["id"]; id != nil {
				wire["ID"] = id
				delete(wire, "Id")
			}
			body = wire
		case "agent/service/deregister", "agent/check/deregister":
			method = http.MethodPut
			endpoint = "/v1/" + name + "/" + escape(str(p, "id"))
		case "agent/service/maintenance":
			method = http.MethodPut
			endpoint = "/v1/" + name + "/" + escape(str(p, "id"))
			query.Set("enable", strconv.FormatBool(boolean(p, "enable")))
			query.Set("reason", str(p, "reason"))
		case "agent/check/ttl":
			method = http.MethodPut
			status := str(p, "status")
			if status != "pass" && status != "warn" && status != "fail" {
				return nil, errors.New("invalid check status")
			}
			endpoint = "/v1/agent/check/" + status + "/" + escape(str(p, "id"))
			query.Set("note", str(p, "output"))
		case "session/renew":
			method = http.MethodPut
			endpoint = "/v1/session/renew/" + escape(str(p, "id"))
		case "prepared-query/create":
			method = http.MethodPost
			endpoint = "/v1/query"
			body = p["input"]
		case "prepared-query/update", "prepared-query/delete":
			method = http.MethodPut
			if name == "prepared-query/delete" {
				method = http.MethodDelete
			}
			endpoint = "/v1/query/" + escape(str(p, "id"))
			body = p["input"]
		case "acl/list", "acl/get", "acl/delete":
			kind := str(p, "kind")
			valid := map[string]string{"token": "token", "tokens": "token", "policy": "policy", "policies": "policy", "role": "role", "roles": "role", "auth-method": "auth-method", "authMethods": "auth-method", "binding-rule": "binding-rule", "bindingRules": "binding-rule"}
			kind, ok := valid[kind]
			if !ok {
				return nil, errors.New("invalid Consul ACL kind")
			}
			endpoint = "/v1/acl/" + kind
			if name == "acl/list" {
				endpoint = map[string]string{"token": "/v1/acl/tokens", "policy": "/v1/acl/policies", "role": "/v1/acl/roles", "auth-method": "/v1/acl/auth-methods", "binding-rule": "/v1/acl/binding-rules"}[kind]
			} else {
				endpoint += "/" + escape(str(p, "id"))
				if name == "acl/delete" {
					method = http.MethodDelete
				}
			}
		case "mesh/config/list", "mesh/config/get":
			endpoint = "/v1/config/" + escape(str(p, "kind"))
			if name == "mesh/config/get" {
				endpoint += "/" + escape(str(p, "name"))
			}
		case "mesh/intentions/get":
			endpoint = "/v1/connect/intentions/" + escape(str(p, "id"))
		case "mesh/peerings/get", "mesh/peerings/delete":
			endpoint = "/v1/peering/" + escape(str(p, "name"))
			if name == "mesh/peerings/delete" {
				method = http.MethodDelete
			}
		default:
			return nil, unsupported(path)
		}
	}
	if locked && method != http.MethodGet {
		return nil, errors.New("writes are locked; unlock writes before changing Consul resources")
	}
	response, err := d.request(ctx, method, endpoint, query, body)
	if err != nil {
		return nil, err
	}
	if name == "agent/self" {
		root := mapValue(response.body)
		config := mapValue(root["Config"])
		member := mapValue(root["Member"])
		return map[string]any{"node": config["NodeName"], "address": member["Addr"], "datacenter": config["Datacenter"], "version": config["Version"], "server": config["Server"], "revision": config["Revision"], "segment": config["Segment"]}, nil
	}
	if wrap {
		return map[string]any{"items": response.body, "metadata": consulMetadata(response.header)}, nil
	}
	return response.body, nil
}
