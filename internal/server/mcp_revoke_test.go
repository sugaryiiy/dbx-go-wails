package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type revocationBearerTransport struct{ token string }

func (transport revocationBearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("Authorization", "Bearer "+transport.token)
	return http.DefaultTransport.RoundTrip(copy)
}

// Exercise the real SQLite driver, auth generation and official SDK together.
// The SDK negotiates each version and supplies that version's protocol headers
// and metadata, including clients whose old HTTP protocol detaches cancellation.
func TestMCPHTTPRevocationCancelsDatabaseQuery(t *testing.T) {
	for _, protocol := range []string{"2025-03-26", "2026-07-28"} {
		for _, action := range []string{"rotate", "disable", "policy", "tunnel-profile"} {
			t.Run(protocol+"/"+action, func(t *testing.T) {
				s, _ := mcpFixture(t)
				mcpAllow(t, s)
				status, err := s.saveMCPWeb(MCPWebSettings{Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				host := httptest.NewServer(s)
				defer host.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				client, err := mcp.NewClient(&mcp.Implementation{Name: "revocation-test", Version: "1"}, nil).Connect(ctx,
					&mcp.StreamableClientTransport{Endpoint: host.URL + "/mcp", DisableStandaloneSSE: true, MaxRetries: -1, HTTPClient: &http.Client{Transport: revocationBearerTransport{token: status["accessToken"].(string)}}},
					&mcp.ClientSessionOptions{ProtocolVersion: protocol})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { cancel(); client.Close() }()
				joins := make([]string, 30)
				for i := range joins {
					joins[i] = fmt.Sprintf("people p%d", i)
				}
				type outcome struct {
					result *mcp.CallToolResult
					err    error
				}
				done := make(chan outcome, 1)
				go func() {
					result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "dbx_execute_query", Arguments: mcpArgs("SELECT count(*) FROM " + strings.Join(joins, " CROSS JOIN "))})
					done <- outcome{result, err}
				}()
				select {
				case result := <-done:
					t.Fatalf("Large SQLite query finished before revocation: result=%#v error=%v", result.result, result.err)
				case <-time.After(150 * time.Millisecond):
				}
				switch action {
				case "rotate":
					_, err = s.rotateMCPToken(false)
				case "disable":
					_, err = s.saveMCPWeb(MCPWebSettings{})
				case "policy":
					err = s.SaveMCPPolicy(defaultMCPPolicy())
				case "tunnel-profile":
					cookie := loginTest(t, s)
					value(t, call(s, "POST", "/api/tunnel-profiles/save", map[string]any{"profiles": []any{tunnelDraft()}}, cookie, ""))
				}
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-done:
					// Revocation may close the HTTP response before a tool-error
					// body reaches the client. Driver termination and audit state
					// are the observable guarantees, not one wire representation.
				case <-time.After(3 * time.Second):
					t.Fatal("Revoked query kept running")
				}
				// Cancellation may reach the client before the server's deferred
				// audit write. Wait a bounded time for the real failed-query audit.
				deadline := time.NewTimer(3 * time.Second)
				defer deadline.Stop()
				poll := time.NewTicker(10 * time.Millisecond)
				defer poll.Stop()
				for {
					history := s.snapshot().History
					if len(history) > 0 {
						if history[0]["success"] != false || history[0]["mcp_tool_name"] != "dbx_execute_query" {
							t.Fatalf("Canceled query was not audited as failure: %#v", history[0])
						}
						break
					}
					select {
					case <-poll.C:
					case <-deadline.C:
						t.Fatal("Canceled query did not finish its audit")
					}
				}
			})
		}
	}
}
