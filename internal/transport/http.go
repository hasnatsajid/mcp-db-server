// Package transport builds the HTTP handler for the Streamable HTTP
// transport. The stdio transport needs no setup beyond the MCP SDK.
package transport

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxBodyBytes bounds request bodies; tool arguments are small.
const maxBodyBytes = 1 << 20

// HTTPHandler serves srv over Streamable HTTP at /mcp, behind bearer-token
// authentication and cross-origin protection, plus an unauthenticated
// GET /healthz.
func HTTPHandler(srv *mcp.Server, token string, log *slog.Logger) http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
		Logger:              log,
		SessionTimeout:      30 * time.Minute,
		MaxRequestBodyBytes: maxBodyBytes,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/mcp", RequireBearer(token, log, http.NewCrossOriginProtection().Handler(mcpHandler)))
	return mux
}

// RequireBearer rejects requests whose Authorization header is not
// "Bearer <token>". The comparison is constant-time, and neither the
// expected nor the presented token is ever logged.
func RequireBearer(token string, log *slog.Logger, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if token == "" || subtle.ConstantTimeCompare(got, want) != 1 {
			log.Warn("unauthorized request", "method", r.Method, "path", r.URL.Path)
			w.Header().Set("WWW-Authenticate", `Bearer realm="mcp-db-server"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
