package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func usesServiceDriver(kind string) bool {
	return kind == "elasticsearch" || kind == "consul" || kind == "nacos" || kind == "meilisearch" || kind == "influxdb"
}
func init() {
	for _, kind := range []string{"elasticsearch", "consul", "nacos", "meilisearch", "influxdb"} {
		nativeOpeners[kind] = openService
	}
}
func validateServiceConfig(c Config) error {
	if c.DriverProfile != "" && c.DriverProfile != "native" && c.DriverProfile != c.Type {
		return unsupported("service driver profile " + c.DriverProfile)
	}
	if c.InitScript != "" || len(c.Attached) > 0 {
		return unsupported("service initialization scripts or attachments")
	}
	if c.Type == "influxdb" {
		if err := validateInfluxConfig(c); err != nil {
			return err
		}
	}
	return ValidateNetworkTransports(c)
}
func mapValue(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	if result, ok := value.(map[string]any); ok {
		return result
	}
	return map[string]any{}
}

type serviceDriver struct {
	config       Config
	external     map[string]any
	base         *url.URL
	http         *http.Client
	transport    *http.Transport
	life         context.Context
	cancel       context.CancelFunc
	authMu       sync.Mutex
	nacosToken   string
	redactToken  atomic.Value
	tokenExpires time.Time
}
type serviceResponse struct {
	body   any
	raw    []byte
	header http.Header
	status int
}
type serviceHTTPError struct {
	status  int
	message string
}

func (e *serviceHTTPError) Error() string { return e.message }
func openService(ctx context.Context, c Config) (nativeDriver, error) {
	if err := validateServiceConfig(c); err != nil {
		return nil, err
	}
	if c.Type == "influxdb" && influxVersion(c) == "2" {
		// Keep the credential sent on the wire and used for error redaction equal.
		c.Password = strings.TrimSpace(c.Password)
	}
	external := mapValue(c.Raw["external_config"])
	endpoint := str(external, "serverAddr", "server_addr", "baseUrl", "base_url")
	if endpoint == "" {
		endpoint = c.ConnectionString
	}
	if endpoint == "" {
		endpoint = c.Host
	}
	if !strings.Contains(endpoint, "://") {
		scheme := "http"
		if c.SSL {
			scheme = "https"
		}
		port := c.Port
		if port == 0 {
			port = map[string]int{"elasticsearch": 9200, "consul": 8500, "nacos": 8848, "meilisearch": 7700, "influxdb": 8086}[c.Type]
		}
		endpoint = scheme + "://" + net.JoinHostPort(endpoint, strconv.Itoa(port))
	}
	base, err := url.Parse(endpoint)
	if err != nil || base.Hostname() == "" || base.Scheme != "http" && base.Scheme != "https" {
		return nil, errors.New("service endpoint must be a valid HTTP or HTTPS address")
	}
	if base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("service endpoint cannot contain credentials, query or fragment")
	}
	if c.Type == "nacos" {
		contextPath := str(external, "contextPath", "context_path")
		if contextPath == "" && len(external) == 0 {
			contextPath = "/nacos"
		}
		if contextPath != "" && contextPath != "/" && strings.TrimSuffix(base.Path, "/") == "" {
			base.Path = "/" + strings.Trim(contextPath, "/")
		}
	}
	c.Host = base.Hostname()
	c.SSL = base.Scheme == "https"
	dial, err := networkDialer(c)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := databaseTLSConfig(c)
	if err != nil {
		return nil, err
	}
	if tlsConfig != nil && boolean(external, "tlsSkipVerify", "tls_skip_verify") {
		tlsConfig.InsecureSkipVerify = true
	}
	transport := &http.Transport{DialContext: dial, TLSClientConfig: tlsConfig, MaxIdleConnsPerHost: 8, IdleConnTimeout: 60 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 300 * time.Second}
	life, cancel := context.WithCancel(context.Background())
	driver := &serviceDriver{config: c, external: external, base: base, transport: transport, http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, life: life, cancel: cancel}
	if err = driver.Ping(ctx); err != nil {
		driver.Close()
		return nil, driver.redact(err)
	}
	return driver, nil
}
func (d *serviceDriver) Close() error { d.cancel(); d.transport.CloseIdleConnections(); return nil }
func (d *serviceDriver) redact(err error) error {
	if err == nil {
		return nil
	}
	message := redact(err, d.config).Error()
	token, _ := d.redactToken.Load().(string)
	for _, value := range []string{str(d.external, "token", "apiKey", "api_key"), str(mapValue(d.external["auth"]), "token", "password", "value"), token} {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[REDACTED]")
		}
	}
	return sanitizedError{message: message, cause: err}
}
func (d *serviceDriver) request(ctx context.Context, method, endpoint string, query url.Values, body any) (serviceResponse, error) {
	if d.config.Type == "nacos" {
		if err := d.ensureNacosAuth(ctx); err != nil {
			return serviceResponse{}, err
		}
		d.authMu.Lock()
		token := d.nacosToken
		d.authMu.Unlock()
		if token != "" {
			if query == nil {
				query = url.Values{}
			}
			query.Set("accessToken", token)
		}
	}
	return d.perform(ctx, method, endpoint, query, body)
}
func (d *serviceDriver) perform(parent context.Context, method, endpoint string, query url.Values, body any) (serviceResponse, error) {
	if !strings.HasPrefix(endpoint, "/") || strings.HasPrefix(endpoint, "//") || strings.Contains(endpoint, "://") {
		return serviceResponse{}, errors.New("service API endpoint must be relative")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host != "" || parsed.Fragment != "" {
		return serviceResponse{}, errors.New("invalid service API endpoint")
	}
	for _, part := range strings.Split(parsed.Path, "/") {
		if part == ".." {
			return serviceResponse{}, errors.New("service path traversal is unavailable")
		}
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := context.AfterFunc(d.life, cancel)
	defer stop()
	target, err := url.Parse(strings.TrimSuffix(d.base.String(), "/") + endpoint)
	if err != nil {
		return serviceResponse{}, err
	}
	q := target.Query()
	for k, values := range query {
		for _, value := range values {
			q.Add(k, value)
		}
	}
	if d.config.Type == "consul" {
		for key, fields := range map[string][]string{"dc": {"datacenter"}, "ns": {"namespace"}, "partition": {"partition"}} {
			if value := str(d.external, fields...); value != "" {
				q.Set(key, value)
			}
		}
		if method == http.MethodGet {
			switch str(d.external, "consistency") {
			case "stale", "consistent":
				q.Set(str(d.external, "consistency"), "")
			}
		}
	}
	target.RawQuery = q.Encode()
	var data []byte
	contentType := "application/json"
	switch value := body.(type) {
	case nil:
	case []byte:
		data = value
		contentType = "application/octet-stream"
	case url.Values:
		data = []byte(value.Encode())
		contentType = "application/x-www-form-urlencoded"
	default:
		data, err = json.Marshal(body)
		if err != nil {
			return serviceResponse{}, err
		}
	}
	if len(data) > 16<<20 {
		return serviceResponse{}, errors.New("service request exceeds 16 MiB")
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(data))
	if err != nil {
		return serviceResponse{}, err
	}
	if body != nil {
		request.Header.Set("Content-Type", contentType)
	}
	if d.config.Type == "consul" {
		if token := str(d.external, "token"); token != "" {
			request.Header.Set("X-Consul-Token", token)
		}
	} else if d.config.Type == "meilisearch" {
		token := str(d.external, "apiKey", "api_key", "token")
		if token == "" {
			token = d.config.Password
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
	} else if d.config.Type == "influxdb" && influxVersion(d.config) == "2" {
		request.Header.Set("Authorization", "Token "+strings.TrimSpace(d.config.Password))
	} else if d.config.Type != "nacos" {
		auth := mapValue(d.external["auth"])
		if token := str(auth, "token"); token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		} else if d.config.Username != "" {
			request.SetBasicAuth(d.config.Username, d.config.Password)
		}
	}
	response, err := d.http.Do(request)
	if err != nil {
		return serviceResponse{}, d.redact(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (48<<20)+1))
	if err != nil {
		return serviceResponse{}, d.redact(err)
	}
	if len(raw) > 48<<20 {
		return serviceResponse{}, errors.New("service response exceeds 48 MiB")
	}
	result := serviceResponse{raw: raw, header: response.Header, status: response.StatusCode}
	if len(raw) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&result.body); err != nil {
			result.body = string(raw)
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := string(raw)
		if len(detail) > 4096 {
			detail = detail[:4096]
		}
		return result, d.redact(&serviceHTTPError{status: response.StatusCode, message: fmt.Sprintf("%s HTTP %d: %s", d.config.Type, response.StatusCode, detail)})
	}
	return result, nil
}
func (d *serviceDriver) Ping(ctx context.Context) error {
	if d.config.Type == "influxdb" {
		_, err := d.influxDatabases(ctx)
		return err
	}
	endpoint := map[string]string{"elasticsearch": "/", "consul": "/v1/status/leader", "nacos": "/v1/console/namespaces", "meilisearch": "/health"}[d.config.Type]
	_, err := d.request(ctx, http.MethodGet, endpoint, nil, nil)
	return err
}
func (d *serviceDriver) Info(ctx context.Context) (map[string]any, error) {
	if d.config.Type == "influxdb" {
		return d.influxInfo(ctx)
	}
	endpoint := map[string]string{"elasticsearch": "/", "consul": "/v1/agent/self", "nacos": "/v1/console/server/state", "meilisearch": "/version"}[d.config.Type]
	response, err := d.request(ctx, http.MethodGet, endpoint, nil, nil)
	if err != nil {
		return nil, err
	}
	body := mapValue(response.body)
	version := str(body, "version", "pkgVersion")
	if d.config.Type == "elasticsearch" {
		version = str(mapValue(body["version"]), "number")
	}
	if d.config.Type == "consul" {
		version = str(mapValue(body["Config"]), "Version")
	}
	return map[string]any{"productName": d.config.Type, "productVersion": version, "driverName": "Go HTTP", "currentDatabase": d.config.Database}, nil
}
func (d *serviceDriver) Handle(ctx context.Context, path string, p map[string]any, locked bool) (any, error) {
	switch d.config.Type {
	case "elasticsearch":
		return d.handleElastic(ctx, path, p, locked)
	case "consul":
		return d.handleConsul(ctx, path, p, locked)
	case "nacos":
		return d.handleNacos(ctx, path, p, locked)
	case "meilisearch":
		return d.handleMeilisearch(ctx, path, p, locked)
	case "influxdb":
		return d.handleInflux(ctx, path, p, locked)
	}
	return nil, unsupported(path)
}
