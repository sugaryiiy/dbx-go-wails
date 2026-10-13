package mcpbridge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RunHTTPStdio connects to the running application and serves its tools on stdio.
// It never opens the application's encrypted store or bypasses its process lock.
func RunHTTPStdio(ctx context.Context, endpoint, token string) error {
	remote, err := ConnectHTTP(ctx, endpoint, token)
	if err != nil {
		return err
	}
	defer remote.Close()
	local, err := proxyServer(ctx, remote)
	if err != nil {
		return err
	}
	return local.Run(ctx, &mcp.StdioTransport{MaxLineLength: 1 << 20})
}

// ConnectHTTP creates an authenticated SDK client. Tokens never appear in URLs,
// logs, command arguments, redirects, or requests to a different endpoint.
func ConnectHTTP(ctx context.Context, endpoint, token string) (*mcp.ClientSession, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("MCP URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	loopback := strings.EqualFold(u.Hostname(), "localhost")
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("MCP URL requires HTTPS, except for a local loopback HTTP endpoint")
	}
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("DBX_MCP_TOKEN must contain the application's MCP access token")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 10 * time.Second
	client := &http.Client{
		Transport:     bearerTransport{base: transport, token: token},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	remote := mcp.NewClient(&mcp.Implementation{Name: "dbx-go-stdio-proxy", Version: Version}, nil)
	session, err := remote.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: u.String(), HTTPClient: client, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("Connect to DBX MCP: %w", err)
	}
	go func() {
		_ = session.Wait()
		transport.CloseIdleConnections()
	}()
	return session, nil
}

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(copy)
}

func proxyServer(ctx context.Context, remote *mcp.ClientSession) (*mcp.Server, error) {
	server := newServer()
	for tool, err := range remote.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("List DBX MCP tools: %w", err)
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Invalid input schema for MCP tool %q", tool.Name)
		}
		copy := *tool
		copy.InputSchema = strictSchema(schema)
		// SDK validates input; the remote server retains permission enforcement.
		// Preserve the complete CallToolResult, including remote IsError values.
		registerTool(server, &copy, func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
			return remote.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: args})
		})
	}
	return server, nil
}
