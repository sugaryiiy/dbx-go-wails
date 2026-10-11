package database

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (d *serviceDriver) ensureNacosAuth(ctx context.Context) error {
	d.authMu.Lock()
	defer d.authMu.Unlock()
	auth := mapValue(d.external["auth"])
	kind := str(auth, "kind")
	if kind == "none" {
		return nil
	}
	if kind == "token" {
		d.nacosToken = str(auth, "token")
		d.redactToken.Store(d.nacosToken)
		return nil
	}
	username := str(auth, "username")
	password := str(auth, "password")
	if username == "" {
		username = d.config.Username
		password = d.config.Password
	}
	if username == "" {
		return nil
	}
	if d.nacosToken != "" && time.Now().Before(d.tokenExpires) {
		return nil
	}
	var last error
	for _, endpoint := range []string{"/v1/auth/users/login", "/v1/auth/login", "/v3/auth/user/login"} {
		response, err := d.perform(ctx, http.MethodPost, endpoint, nil, url.Values{"username": {username}, "password": {password}})
		if err != nil {
			last = err
			var status *serviceHTTPError
			if errors.As(err, &status) && (status.status == 404 || status.status == 405) {
				continue
			}
			return err
		}
		body := mapValue(response.body)
		if nested, ok := body["data"].(map[string]any); ok {
			body = nested
		}
		token := str(body, "accessToken", "access_token")
		if token == "" {
			return errors.New("Nacos authentication response has no access token")
		}
		seconds := integer(body, 18000, "tokenTtl", "expires_in")
		if seconds < 1 {
			seconds = 1
		}
		d.nacosToken = token
		d.redactToken.Store(token)
		d.tokenExpires = time.Now().Add(time.Duration(seconds)*time.Second - time.Second)
		return nil
	}
	return last
}
func nacosBody(value any) (any, error) {
	object := mapValue(value)
	if code, exists := object["code"]; exists {
		number := valueInt(code)
		if number != 0 && number != 200 {
			return nil, errors.New("Nacos API rejected request: " + str(object, "message", "msg"))
		}
		if data, exists := object["data"]; exists {
			return data, nil
		}
	}
	return value, nil
}
func nacosParams(value map[string]any) url.Values {
	query := url.Values{}
	fields := map[string]string{"namespace": "tenant", "namespaceId": "namespaceId", "namespaceName": "namespaceName", "namespaceDesc": "namespaceDesc", "dataId": "dataId", "group": "group", "appName": "appName", "configType": "type", "content": "content", "desc": "desc", "tags": "config_tags", "casMd5": "casMd5", "serviceName": "serviceName", "groupName": "groupName", "ip": "ip", "port": "port", "clusterName": "clusterName", "healthy": "healthy", "enabled": "enabled", "ephemeral": "ephemeral", "weight": "weight", "protectThreshold": "protectThreshold", "pageNo": "pageNo", "pageSize": "pageSize", "search": "search", "username": "username", "password": "password", "newPassword": "newPassword", "role": "role"}
	for source, target := range fields {
		if raw, exists := value[source]; exists && raw != nil {
			query.Set(target, valueString(raw))
		}
	}
	for _, key := range []string{"metadata", "selector"} {
		if value[key] != nil {
			data, _ := json.Marshal(value[key])
			query.Set(key, string(data))
		}
	}
	return query
}
func (d *serviceDriver) handleNacos(ctx context.Context, path string, p map[string]any, locked bool) (any, error) {
	name := strings.TrimPrefix(path, "/api/nacos/")
	payload := mapValue(p["req"])
	if len(payload) == 0 {
		payload = mapValue(p["query"])
	}
	if len(payload) == 0 {
		payload = mapValue(p["key"])
	}
	if len(payload) == 0 {
		payload = mapValue(p["binding"])
	}
	query := nacosParams(payload)
	if query.Get("tenant") == "" {
		query.Set("tenant", str(d.external, "namespace"))
	}
	page := integer(payload, 1, "pageNo")
	size := integer(payload, 100, "pageSize")
	if page < 1 || size < 1 || size > 1000 {
		return nil, errors.New("Nacos page must be positive and pageSize 1–1000")
	}
	endpoint := ""
	method := http.MethodGet
	var body any
	switch name {
	case "test-connection":
		if boolean(p, "forceRefresh") {
			d.authMu.Lock()
			d.nacosToken = ""
			d.redactToken.Store("")
			d.tokenExpires = time.Time{}
			d.authMu.Unlock()
		}
		info, err := d.Info(ctx)
		if err != nil {
			return nil, err
		}
		capabilities := map[string]any{"supportsConfigManagement": false, "supportsConfigHistory": false, "supportsServiceManagement": false, "supportsInstanceUpdate": false, "supportsRawApi": false}
		for capability, probe := range map[string]string{"supportsConfigManagement": "/v1/cs/configs", "supportsConfigHistory": "/v1/cs/history/configs", "supportsServiceManagement": "/v1/ns/catalog/services"} {
			params := url.Values{"search": {"accurate"}, "pageNo": {"1"}, "pageSize": {"1"}, "tenant": {str(d.external, "namespace")}, "namespaceId": {str(d.external, "namespace")}}
			response, probeErr := d.request(ctx, http.MethodGet, probe, params, nil)
			if probeErr == nil {
				_, probeErr = nacosBody(response.body)
			}
			capabilities[capability] = probeErr == nil
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		// The V1 naming API shares the server implementation used by services.
		capabilities["supportsInstanceUpdate"] = capabilities["supportsServiceManagement"]
		return map[string]any{"serverAddr": d.base.String(), "displayServerAddr": d.base.String(), "namespace": str(d.external, "namespace"), "serverVersion": info["productVersion"], "auth": str(mapValue(d.external["auth"]), "kind"), "capabilities": capabilities, "raw": info}, nil
	case "namespaces/list":
		response, err := d.request(ctx, http.MethodGet, "/v1/console/namespaces", nil, nil)
		if err != nil {
			return nil, err
		}
		return nacosBody(response.body)
	case "namespaces/create", "namespaces/update", "namespaces/delete":
		if locked {
			return nil, errors.New("writes are locked; unlock writes before changing Nacos namespaces")
		}
		endpoint = "/v1/console/namespaces"
		method = http.MethodPost
		if name == "namespaces/update" {
			method = http.MethodPut
		}
		if name == "namespaces/delete" {
			method = http.MethodDelete
			query = url.Values{"namespaceId": {str(p, "namespaceId")}}
		}
		if method != http.MethodDelete {
			body = query
			query = nil
		}
	case "configs/list":
		endpoint = "/v1/cs/configs"
		query.Set("search", "accurate")
		if str(payload, "search") != "" {
			query.Set("search", str(payload, "search"))
		}
		query.Set("pageNo", strconv.Itoa(page))
		query.Set("pageSize", strconv.Itoa(size))
		response, err := d.request(ctx, method, endpoint, query, nil)
		if err != nil {
			return nil, err
		}
		value, err := nacosBody(response.body)
		if err != nil {
			return nil, err
		}
		object := mapValue(value)
		return map[string]any{"pageNo": page, "pageSize": size, "totalCount": object["totalCount"], "items": object["pageItems"]}, nil
	case "configs/get":
		response, err := d.request(ctx, http.MethodGet, "/v1/cs/configs", query, nil)
		if err != nil {
			return nil, err
		}
		sum := md5.Sum(response.raw)
		return map[string]any{"namespace": query.Get("tenant"), "dataId": query.Get("dataId"), "group": query.Get("group"), "content": string(response.raw), "md5": hex.EncodeToString(sum[:])}, nil
	case "configs/publish", "configs/delete":
		if locked {
			return nil, errors.New("writes are locked; unlock writes before changing Nacos configs")
		}
		endpoint = "/v1/cs/configs"
		method = http.MethodPost
		if name == "configs/delete" {
			method = http.MethodDelete
		} else {
			body = query
			query = nil
		}
	case "configs/history/list", "configs/history/get":
		endpoint = "/v1/cs/history/configs"
		if name == "configs/history/list" {
			query.Set("search", "accurate")
			query.Set("pageNo", strconv.Itoa(page))
			query.Set("pageSize", strconv.Itoa(size))
		} else {
			query.Set("nid", str(payload, "nid", "historyId"))
		}
	case "services/list":
		query.Set("namespaceId", query.Get("tenant"))
		query.Del("tenant")
		query.Set("pageNo", strconv.Itoa(page))
		query.Set("pageSize", strconv.Itoa(size))
		response, err := d.request(ctx, http.MethodGet, "/v1/ns/catalog/services", query, nil)
		if err != nil {
			return nil, err
		}
		value, err := nacosBody(response.body)
		if err != nil {
			return nil, err
		}
		object := mapValue(value)
		items := []map[string]any{}
		for _, entry := range anySlice(object["serviceList"]) {
			service := mapValue(entry)
			service["serviceName"] = service["name"]
			items = append(items, service)
		}
		return map[string]any{"pageNo": page, "pageSize": size, "totalCount": object["count"], "items": items}, nil
	case "services/get", "services/create", "services/update", "services/delete":
		endpoint = "/v1/ns/service"
		query.Set("namespaceId", query.Get("tenant"))
		query.Del("tenant")
		if name != "services/get" {
			if locked {
				return nil, errors.New("writes are locked; unlock writes before changing Nacos services")
			}
			method = map[string]string{"services/create": http.MethodPost, "services/update": http.MethodPut, "services/delete": http.MethodDelete}[name]
		}
	case "instances/list":
		endpoint = "/v1/ns/instance/list"
		query.Set("namespaceId", query.Get("tenant"))
		query.Del("tenant")
	case "instances/register", "instances/update", "instances/deregister":
		if locked {
			return nil, errors.New("writes are locked; unlock writes before changing Nacos instances")
		}
		endpoint = "/v1/ns/instance"
		query.Set("namespaceId", query.Get("tenant"))
		query.Del("tenant")
		method = map[string]string{"instances/register": http.MethodPost, "instances/update": http.MethodPut, "instances/deregister": http.MethodDelete}[name]
	case "users/list", "users/create", "users/update", "users/delete", "roles/list", "roles/assign", "roles/remove":
		if strings.HasPrefix(name, "users/") {
			endpoint = "/v1/auth/users"
		} else {
			endpoint = "/v1/auth/roles"
		}
		if strings.HasSuffix(name, "/list") {
			query.Set("pageNo", strconv.Itoa(page))
			query.Set("pageSize", strconv.Itoa(size))
		} else {
			if locked {
				return nil, errors.New("writes are locked; unlock writes before changing Nacos access control")
			}
			switch name {
			case "users/create", "roles/assign":
				method = http.MethodPost
			case "users/update":
				method = http.MethodPut
			default:
				method = http.MethodDelete
			}
			if name == "users/delete" {
				query.Set("username", str(p, "username"))
			}
			if method != http.MethodDelete {
				body = query
				query = nil
			}
		}
	default:
		return nil, unsupported(path)
	}
	response, err := d.request(ctx, method, endpoint, query, body)
	if err != nil {
		return nil, err
	}
	value, err := nacosBody(response.body)
	if err != nil {
		return nil, err
	}
	if method != http.MethodGet {
		if value == false || value == "false" {
			return nil, errors.New("Nacos declined the mutation")
		}
		return value, nil
	}
	if name == "instances/list" {
		return mapValue(value)["hosts"], nil
	}
	if name == "configs/history/list" {
		object := mapValue(value)
		return map[string]any{"pageNo": page, "pageSize": size, "totalCount": object["totalCount"], "items": object["pageItems"]}, nil
	}
	return value, nil
}
