// Command mcp-db-server is an MCP server exposing safe, read-only Postgres
// introspection and query tools over stdio or HTTP.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hasnatsajid/mcp-db-server/internal/audit"
	"github.com/hasnatsajid/mcp-db-server/internal/config"
	"github.com/hasnatsajid/mcp-db-server/internal/db"
	"github.com/hasnatsajid/mcp-db-server/internal/guard"
	"github.com/hasnatsajid/mcp-db-server/internal/tools"
	"github.com/hasnatsajid/mcp-db-server/internal/transport"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	log := newLogger(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	aud, err := openAudit(cfg.AuditLogPath)
	if err != nil {
		return err
	}
	defer func() {
		if err := aud.Close(); err != nil {
			log.Error("close audit log", "error", err)
		}
	}()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	policy := guard.Policy{AllowSchemas: cfg.AllowSchemas, DenyTables: cfg.DenyTables, MaxRows: cfg.MaxRows}
	srv := mcp.NewServer(&mcp.Implementation{Name: "mcp-db-server", Version: version}, nil)
	tools.Register(srv, db.New(pool, policy, cfg.QueryTimeout), aud, log)

	// Reopen the audit log on SIGHUP so external rotation works.
	var wg sync.WaitGroup
	defer wg.Wait()
	hupCtx, stopHUP := context.WithCancel(ctx)
	defer stopHUP()
	wg.Go(func() { reopenOnHUP(hupCtx, log, aud) })

	log.Info("starting", "version", version, "transport", cfg.Transport, "tools", tools.All,
		"max_rows", cfg.MaxRows, "query_timeout", cfg.QueryTimeout, "allow_schemas", cfg.AllowSchemas)
	switch cfg.Transport {
	case config.TransportHTTP:
		err = serveHTTP(ctx, log, cfg, srv)
	default:
		err = serveStdio(ctx, log, srv)
	}
	if err != nil {
		return err
	}
	log.Info("stopped")
	return nil
}

func newLogger(cfg config.Config) *slog.Logger {
	// stdout carries the MCP protocol in stdio mode, so logs always go to stderr.
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}

// openAudit appends to path, or writes to stderr when path is empty.
func openAudit(path string) (*audit.Log, error) {
	if path == "" {
		return audit.New(os.Stderr), nil
	}
	return audit.Open(path)
}

func reopenOnHUP(ctx context.Context, log *slog.Logger, aud *audit.Log) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			if err := aud.Reopen(); err != nil {
				log.Error("reopen audit log", "error", err)
			} else {
				log.Info("audit log reopened")
			}
		}
	}
}

// serveStdio serves one MCP session over stdin/stdout until the client
// disconnects or a signal arrives.
func serveStdio(ctx context.Context, log *slog.Logger, srv *mcp.Server) error {
	err := srv.Run(ctx, &mcp.StdioTransport{})
	switch {
	case err == nil, errors.Is(err, io.EOF):
		log.Info("client disconnected, shutting down")
		return nil
	case ctx.Err() != nil:
		log.Info("signal received, shutting down")
		return nil
	}
	return fmt.Errorf("stdio session: %w", err)
}

// serveHTTP serves Streamable HTTP until a signal arrives, then stops
// accepting connections and waits up to cfg.ShutdownTimeout for in-flight
// requests before closing the rest.
func serveHTTP(ctx context.Context, log *slog.Logger, cfg config.Config, srv *mcp.Server) error {
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           transport.HTTPHandler(srv, cfg.HTTPAuthToken, log),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", cfg.HTTPAddr)
		errCh <- httpSrv.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		log.Info("signal received, shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	err := httpSrv.Shutdown(shutdownCtx)
	if errors.Is(err, context.DeadlineExceeded) {
		// Long-lived SSE streams don't finish on their own.
		log.Warn("shutdown timeout reached, closing remaining connections")
		err = httpSrv.Close()
	}
	if serveErr := <-errCh; !errors.Is(serveErr, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", serveErr)
	}
	if err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	return nil
}
