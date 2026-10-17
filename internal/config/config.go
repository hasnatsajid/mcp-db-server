// Package config loads server configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Transport selects how MCP messages are exchanged.
type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
)

// Config is the parsed and validated runtime configuration.
type Config struct {
	Transport       Transport
	HTTPAddr        string
	DatabaseURL     string
	MaxRows         int
	QueryTimeout    time.Duration
	AllowSchemas    []string
	DenyTables      []string
	AuditLogPath    string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
}

// Load reads configuration using getenv (normally os.Getenv).
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Transport:    Transport(orDefault(getenv("MCP_TRANSPORT"), string(TransportStdio))),
		HTTPAddr:     orDefault(getenv("HTTP_ADDR"), "127.0.0.1:8090"),
		DatabaseURL:  getenv("DATABASE_URL"),
		AllowSchemas: splitList(orDefault(getenv("ALLOW_SCHEMAS"), "public")),
		DenyTables:   splitList(getenv("DENY_TABLES")),
		AuditLogPath: getenv("AUDIT_LOG_PATH"),
	}

	var errs []error
	var err error
	if cfg.Transport != TransportStdio && cfg.Transport != TransportHTTP {
		errs = append(errs, fmt.Errorf("MCP_TRANSPORT must be stdio or http, got %q", cfg.Transport))
	}
	if cfg.MaxRows, err = strconv.Atoi(orDefault(getenv("MAX_ROWS"), "500")); err != nil || cfg.MaxRows < 1 || cfg.MaxRows > 10000 {
		errs = append(errs, errors.New("MAX_ROWS must be an integer between 1 and 10000"))
	}
	if cfg.QueryTimeout, err = time.ParseDuration(orDefault(getenv("QUERY_TIMEOUT"), "5s")); err != nil || cfg.QueryTimeout <= 0 {
		errs = append(errs, errors.New("QUERY_TIMEOUT must be a positive duration"))
	}
	if cfg.ShutdownTimeout, err = time.ParseDuration(orDefault(getenv("SHUTDOWN_TIMEOUT"), "5s")); err != nil {
		errs = append(errs, fmt.Errorf("SHUTDOWN_TIMEOUT: %w", err))
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(orDefault(getenv("LOG_LEVEL"), "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}
	return cfg, errors.Join(errs...)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
