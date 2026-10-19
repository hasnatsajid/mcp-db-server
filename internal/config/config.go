// Package config loads server configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
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

// minTokenLen is the shortest accepted HTTP_AUTH_TOKEN.
const minTokenLen = 32

// Config is the parsed and validated runtime configuration. It holds
// secrets: print it only via String, which redacts them.
type Config struct {
	Transport       Transport
	HTTPAddr        string
	HTTPAuthToken   string
	DatabaseURL     string
	MaxRows         int
	QueryTimeout    time.Duration
	AllowSchemas    []string
	DenyTables      []string
	AuditLogPath    string
	LogLevel        slog.Level
	LogFormat       string // json or text
	ShutdownTimeout time.Duration
}

// String describes the config with secrets redacted.
func (c Config) String() string {
	return fmt.Sprintf("{Transport:%s HTTPAddr:%s HTTPAuthToken:%s DatabaseURL:%s MaxRows:%d QueryTimeout:%s AllowSchemas:%v DenyTables:%v AuditLogPath:%s LogLevel:%s LogFormat:%s ShutdownTimeout:%s}",
		c.Transport, c.HTTPAddr, redact(c.HTTPAuthToken), redact(c.DatabaseURL), c.MaxRows, c.QueryTimeout,
		c.AllowSchemas, c.DenyTables, c.AuditLogPath, c.LogLevel, c.LogFormat, c.ShutdownTimeout)
}

// LogValue keeps secrets out of structured logs.
func (c Config) LogValue() slog.Value { return slog.StringValue(c.String()) }

func redact(s string) string {
	if s == "" {
		return "<unset>"
	}
	return "<redacted>"
}

// Load reads configuration using getenv (normally os.Getenv).
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Transport:     Transport(orDefault(getenv("MCP_TRANSPORT"), string(TransportStdio))),
		HTTPAddr:      orDefault(getenv("HTTP_ADDR"), "127.0.0.1:8090"),
		HTTPAuthToken: getenv("HTTP_AUTH_TOKEN"),
		DatabaseURL:   getenv("DATABASE_URL"),
		LogFormat:     orDefault(getenv("LOG_FORMAT"), "json"),
		AllowSchemas:  splitList(orDefault(getenv("ALLOW_SCHEMAS"), "public")),
		DenyTables:    splitList(getenv("DENY_TABLES")),
		AuditLogPath:  getenv("AUDIT_LOG_PATH"),
	}

	var errs []error
	var err error
	if cfg.Transport != TransportStdio && cfg.Transport != TransportHTTP {
		errs = append(errs, fmt.Errorf("MCP_TRANSPORT must be stdio or http, got %q", cfg.Transport))
	}
	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if cfg.Transport == TransportHTTP && len(cfg.HTTPAuthToken) < minTokenLen {
		errs = append(errs, fmt.Errorf("HTTP_AUTH_TOKEN of at least %d characters is required for the http transport", minTokenLen))
	}
	if len(cfg.AllowSchemas) == 0 {
		errs = append(errs, errors.New("ALLOW_SCHEMAS must list at least one schema"))
	}
	for _, t := range cfg.DenyTables {
		if s, n, ok := strings.Cut(t, "."); !ok || s == "" || n == "" {
			errs = append(errs, fmt.Errorf("DENY_TABLES entry %q must be schema-qualified (schema.table)", t))
		}
	}
	if !slices.Contains([]string{"json", "text"}, cfg.LogFormat) {
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be json or text, got %q", cfg.LogFormat))
	}
	if cfg.MaxRows, err = strconv.Atoi(orDefault(getenv("MAX_ROWS"), "500")); err != nil || cfg.MaxRows < 1 || cfg.MaxRows > 10000 {
		errs = append(errs, errors.New("MAX_ROWS must be an integer between 1 and 10000"))
	}
	if cfg.QueryTimeout, err = time.ParseDuration(orDefault(getenv("QUERY_TIMEOUT"), "5s")); err != nil || cfg.QueryTimeout <= 0 {
		errs = append(errs, errors.New("QUERY_TIMEOUT must be a positive duration"))
	}
	if cfg.ShutdownTimeout, err = time.ParseDuration(orDefault(getenv("SHUTDOWN_TIMEOUT"), "5s")); err != nil || cfg.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("SHUTDOWN_TIMEOUT must be a positive duration"))
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
