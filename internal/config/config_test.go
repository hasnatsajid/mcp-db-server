package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
)

// base holds the minimum valid environment. Values are placeholders.
var base = map[string]string{"DATABASE_URL": "postgres://placeholder"}

func with(kv ...string) map[string]string {
	env := maps.Clone(base)
	for i := 0; i < len(kv); i += 2 {
		env[kv[i]] = kv[i+1]
	}
	return env
}

func TestLoad(t *testing.T) {
	t.Parallel()
	token := strings.Repeat("t", minTokenLen)
	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
		check   func(t *testing.T, c Config)
	}{
		{
			name: "defaults",
			env:  base,
			check: func(t *testing.T, c Config) {
				if c.Transport != TransportStdio || c.MaxRows != 500 || c.QueryTimeout != 5*time.Second || c.LogFormat != "json" {
					t.Errorf("unexpected defaults: %v", c)
				}
				if !slices.Equal(c.AllowSchemas, []string{"public"}) {
					t.Errorf("AllowSchemas = %v", c.AllowSchemas)
				}
			},
		},
		{
			name: "lists are trimmed",
			env:  with("ALLOW_SCHEMAS", " public , sales ,", "DENY_TABLES", "public.users"),
			check: func(t *testing.T, c Config) {
				if !slices.Equal(c.AllowSchemas, []string{"public", "sales"}) || !slices.Equal(c.DenyTables, []string{"public.users"}) {
					t.Errorf("got allow=%v deny=%v", c.AllowSchemas, c.DenyTables)
				}
			},
		},
		{name: "http transport with token", env: with("MCP_TRANSPORT", "http", "HTTP_AUTH_TOKEN", token)},
		{name: "http transport without token", env: with("MCP_TRANSPORT", "http"), wantErr: true},
		{name: "http transport short token", env: with("MCP_TRANSPORT", "http", "HTTP_AUTH_TOKEN", "short"), wantErr: true},
		{name: "missing database url", env: map[string]string{}, wantErr: true},
		{name: "bad transport", env: with("MCP_TRANSPORT", "grpc"), wantErr: true},
		{name: "row limit too high", env: with("MAX_ROWS", "1000000"), wantErr: true},
		{name: "zero timeout", env: with("QUERY_TIMEOUT", "0s"), wantErr: true},
		{name: "bad shutdown timeout", env: with("SHUTDOWN_TIMEOUT", "soon"), wantErr: true},
		{name: "unqualified deny entry", env: with("DENY_TABLES", "users"), wantErr: true},
		{name: "empty allow list", env: with("ALLOW_SCHEMAS", " , "), wantErr: true},
		{name: "text log format", env: with("LOG_FORMAT", "text")},
		{name: "bad log format", env: with("LOG_FORMAT", "xml"), wantErr: true},
		{name: "bad log level", env: with("LOG_LEVEL", "loud"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Load(func(k string) string { return tt.env[k] })
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestStringRedactsSecrets(t *testing.T) {
	t.Parallel()
	const dbSecret, tokenSecret = "db-secret-placeholder", "token-secret-placeholder-0123456789"
	cfg, err := Load(func(k string) string {
		return map[string]string{
			"DATABASE_URL":    "postgres://u:" + dbSecret + "@db.invalid/x",
			"MCP_TRANSPORT":   "http",
			"HTTP_AUTH_TOKEN": tokenSecret,
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{cfg.String(), fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), cfg.LogValue().String()} {
		if strings.Contains(out, dbSecret) || strings.Contains(out, tokenSecret) || strings.Contains(out, "db.invalid") {
			t.Errorf("config output leaks a secret: %s", out)
		}
	}
}
