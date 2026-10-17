// Command mcp-db-server is an MCP server exposing safe, read-only Postgres
// introspection and query tools over stdio or HTTP.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hasnatsajid/mcp-db-server/internal/config"
	"github.com/hasnatsajid/mcp-db-server/internal/tools"
)

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
	// stdout carries the MCP protocol in stdio mode, so logs always go to stderr.
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("starting", "transport", cfg.Transport, "tools", tools.All, "max_rows", cfg.MaxRows)
	switch cfg.Transport {
	case config.TransportHTTP:
		err = serveHTTP(ctx, log, cfg)
	default:
		err = serveStdio(ctx, log)
	}
	if err != nil {
		return err
	}
	log.Info("stopped")
	return nil
}

// serveStdio reads newline-delimited JSON-RPC messages until stdin closes or
// the context is cancelled. TODO: hand messages to the MCP SDK.
func serveStdio(ctx context.Context, log *slog.Logger) error {
	lines := make(chan string)
	scanErr := make(chan error, 1)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
		scanErr <- sc.Err()
	}()

	for {
		select {
		case <-ctx.Done():
			log.Info("signal received, shutting down")
			return nil
		case line, ok := <-lines:
			if !ok {
				log.Info("stdin closed, shutting down")
				return <-scanErr
			}
			log.Debug("received message", "bytes", len(line))
		}
	}
}

func serveHTTP(ctx context.Context, log *slog.Logger, cfg config.Config) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not implemented yet", http.StatusNotImplemented)
	})
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
