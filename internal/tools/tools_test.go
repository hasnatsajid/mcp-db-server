package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hasnatsajid/mcp-db-server/internal/audit"
	"github.com/hasnatsajid/mcp-db-server/internal/db"
	"github.com/hasnatsajid/mcp-db-server/internal/guard"
)

type fakeStore struct{}

func (fakeStore) ListSchemas(context.Context) ([]db.Schema, error) {
	return []db.Schema{{Name: "public", Usage: true}}, nil
}

func (fakeStore) ListTables(_ context.Context, schema string) ([]db.Table, error) {
	if schema != "public" {
		return nil, fmt.Errorf("%w: schema %q is not in the allow list", guard.ErrDenied, schema)
	}
	return []db.Table{{Name: "orders", Kind: "table"}}, nil
}

func (fakeStore) DescribeTable(_ context.Context, schema, table string) (db.TableDetail, error) {
	return db.TableDetail{
		Schema: schema, Name: table, Kind: "table",
		Columns:     []db.Column{{Name: "id", Type: "bigint"}},
		PrimaryKey:  []string{"id"},
		Constraints: []db.Constraint{},
		Indexes:     []db.Index{},
	}, nil
}

func (fakeStore) Query(_ context.Context, sql string, params []any, limit int) (db.Result, error) {
	if _, err := guard.ValidateSelect(sql); err != nil {
		return db.Result{}, err
	}
	return db.Result{
		Columns:   []db.ResultColumn{{Name: "n", Type: "int4"}},
		Rows:      [][]any{{1}, {2}},
		RowCount:  2,
		Truncated: limit == 2,
	}, nil
}

func (fakeStore) Explain(_ context.Context, sql string, _ []any, analyze bool) (any, error) {
	return []any{map[string]any{"Plan": map[string]any{"Node Type": "Result"}, "analyze": analyze}}, nil
}

type memAudit struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (m *memAudit) Write(e audit.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
	return nil
}

func (m *memAudit) last() audit.Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entries[len(m.entries)-1]
}

func connect(t *testing.T) (*mcp.ClientSession, *memAudit) {
	t.Helper()
	ctx := context.Background()
	aud := &memAudit{}
	srv := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "test"}, nil)
	Register(srv, fakeStore{}, aud, slog.New(slog.NewTextHandler(io.Discard, nil)))

	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cs.Close()
		_ = ss.Wait()
	})
	return cs, aud
}

func TestToolsListed(t *testing.T) {
	t.Parallel()
	cs, _ := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %s is not annotated read-only", tool.Name)
		}
		if tool.OutputSchema == nil {
			t.Errorf("tool %s has no output schema", tool.Name)
		}
	}
	slices.Sort(names)
	want := slices.Sorted(slices.Values(All))
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
}

func TestToolCalls(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		tool       string
		args       map[string]any
		wantErr    bool
		wantInText string
		checkAudit func(t *testing.T, e audit.Entry)
	}{
		{
			name: "list schemas", tool: ListSchemas, args: map[string]any{},
			wantInText: `"public"`,
		},
		{
			name: "list tables", tool: ListTables, args: map[string]any{"schema": "public"},
			wantInText: `"orders"`,
			checkAudit: func(t *testing.T, e audit.Entry) {
				if e.Target != "public" || e.Rows != 1 {
					t.Errorf("audit = %+v", e)
				}
			},
		},
		{
			name: "list tables denied", tool: ListTables, args: map[string]any{"schema": "secret"},
			wantErr: true, wantInText: "not in the allow list",
			checkAudit: func(t *testing.T, e audit.Entry) {
				if !e.Denied || e.Error == "" {
					t.Errorf("denied call not audited as denied: %+v", e)
				}
			},
		},
		{
			name: "list tables missing schema arg", tool: ListTables, args: map[string]any{},
			wantErr: true,
		},
		{
			name: "describe table", tool: DescribeTable, args: map[string]any{"schema": "public", "table": "orders"},
			wantInText: `"primary_key":["id"]`,
			checkAudit: func(t *testing.T, e audit.Entry) {
				if e.Target != "public.orders" {
					t.Errorf("audit target = %q", e.Target)
				}
			},
		},
		{
			name: "query", tool: Query,
			args:       map[string]any{"sql": "SELECT n FROM t WHERE x = $1", "params": []any{"secret-value"}, "limit": 2},
			wantInText: `"truncated":true`,
			checkAudit: func(t *testing.T, e audit.Entry) {
				if e.SQL == "" || e.ParamCount != 1 || e.Rows != 2 || !e.Truncated {
					t.Errorf("audit = %+v", e)
				}
				b, _ := json.Marshal(e)
				if strings.Contains(string(b), "secret-value") {
					t.Errorf("audit entry leaks a parameter value: %s", b)
				}
			},
		},
		{
			name: "query rejected", tool: Query, args: map[string]any{"sql": "DELETE FROM t"},
			wantErr: true, wantInText: "only SELECT",
			checkAudit: func(t *testing.T, e audit.Entry) {
				if !e.Denied || e.SQL != "DELETE FROM t" {
					t.Errorf("audit = %+v", e)
				}
			},
		},
		{
			name: "explain", tool: ExplainQuery, args: map[string]any{"sql": "SELECT 1", "analyze": true},
			wantInText: `"analyze":true`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cs, aud := connect(t)
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tt.tool, Arguments: tt.args})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if res.IsError != tt.wantErr {
				t.Fatalf("IsError = %v, want %v; content: %s", res.IsError, tt.wantErr, text(res))
			}
			if !strings.Contains(text(res), tt.wantInText) {
				t.Errorf("result %s does not contain %s", text(res), tt.wantInText)
			}
			if !tt.wantErr && res.StructuredContent == nil {
				t.Error("missing structured content")
			}
			if tt.checkAudit != nil {
				e := aud.last()
				if e.Tool != tt.tool || e.Client != "test-client" {
					t.Errorf("audit tool/client = %q/%q", e.Tool, e.Client)
				}
				tt.checkAudit(t, e)
			}
		})
	}
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
