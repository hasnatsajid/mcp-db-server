package config

import (
	"slices"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
		check   func(t *testing.T, c Config)
	}{
		{
			name: "defaults",
			env:  map[string]string{},
			check: func(t *testing.T, c Config) {
				if c.Transport != TransportStdio || c.MaxRows != 500 || c.QueryTimeout != 5*time.Second {
					t.Errorf("unexpected defaults: %+v", c)
				}
				if !slices.Equal(c.AllowSchemas, []string{"public"}) {
					t.Errorf("AllowSchemas = %v", c.AllowSchemas)
				}
			},
		},
		{
			name: "lists are trimmed",
			env:  map[string]string{"ALLOW_SCHEMAS": " public , sales ,", "DENY_TABLES": "public.users"},
			check: func(t *testing.T, c Config) {
				if !slices.Equal(c.AllowSchemas, []string{"public", "sales"}) || !slices.Equal(c.DenyTables, []string{"public.users"}) {
					t.Errorf("got allow=%v deny=%v", c.AllowSchemas, c.DenyTables)
				}
			},
		},
		{name: "http transport", env: map[string]string{"MCP_TRANSPORT": "http"}},
		{name: "bad transport", env: map[string]string{"MCP_TRANSPORT": "grpc"}, wantErr: true},
		{name: "row limit too high", env: map[string]string{"MAX_ROWS": "1000000"}, wantErr: true},
		{name: "zero timeout", env: map[string]string{"QUERY_TIMEOUT": "0s"}, wantErr: true},
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
