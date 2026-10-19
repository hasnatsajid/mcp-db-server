package transport

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testToken = "test-token-0123456789-0123456789-abcdef"

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "ping"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct {
		OK bool `json:"ok"`
	}, error) {
		return nil, struct {
			OK bool `json:"ok"`
		}{true}, nil
	})
	ts := httptest.NewServer(HTTPHandler(srv, testToken, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(ts.Close)
	return ts
}

func TestRequireBearer(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	tests := []struct {
		name   string
		path   string
		auth   string
		method string
		want   int
	}{
		{name: "healthz needs no auth", path: "/healthz", method: http.MethodGet, want: http.StatusOK},
		{name: "no token", path: "/mcp", method: http.MethodPost, want: http.StatusUnauthorized},
		{name: "wrong token", path: "/mcp", method: http.MethodPost, auth: "Bearer nope", want: http.StatusUnauthorized},
		{name: "wrong scheme", path: "/mcp", method: http.MethodPost, auth: "Basic " + testToken, want: http.StatusUnauthorized},
		{name: "token prefix", path: "/mcp", method: http.MethodPost, auth: "Bearer " + testToken[:10], want: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req, _ := http.NewRequest(tt.method, ts.URL+tt.path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestMCPOverHTTP(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   ts.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{testToken}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ping"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
}
