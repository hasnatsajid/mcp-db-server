// Package tools defines the MCP tools the server exposes and registers them
// on an MCP server, auditing every call.
package tools

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hasnatsajid/mcp-db-server/internal/audit"
	"github.com/hasnatsajid/mcp-db-server/internal/db"
	"github.com/hasnatsajid/mcp-db-server/internal/guard"
)

// Names of the tools exposed over MCP.
const (
	ListSchemas   = "list_schemas"
	ListTables    = "list_tables"
	DescribeTable = "describe_table"
	Query         = "query"
	ExplainQuery  = "explain_query"
)

// All lists every tool in registration order.
var All = []string{ListSchemas, ListTables, DescribeTable, Query, ExplainQuery}

// Store is the database surface the tools need. *db.Executor implements it.
type Store interface {
	ListSchemas(ctx context.Context) ([]db.Schema, error)
	ListTables(ctx context.Context, schema string) ([]db.Table, error)
	DescribeTable(ctx context.Context, schema, table string) (db.TableDetail, error)
	Query(ctx context.Context, sql string, params []any, limit int) (db.Result, error)
	Explain(ctx context.Context, sql string, params []any, analyze bool) (any, error)
}

// Auditor records tool calls. *audit.Log implements it.
type Auditor interface {
	Write(audit.Entry) error
}

// Inputs and outputs. Field docs become the JSON schema the client sees.

type ListSchemasIn struct{}

type ListSchemasOut struct {
	Schemas []db.Schema `json:"schemas"`
}

type ListTablesIn struct {
	Schema string `json:"schema" jsonschema:"schema name, from list_schemas"`
}

type ListTablesOut struct {
	Schema string     `json:"schema"`
	Tables []db.Table `json:"tables"`
}

type DescribeTableIn struct {
	Schema string `json:"schema" jsonschema:"schema name"`
	Table  string `json:"table" jsonschema:"table or view name, without schema"`
}

type QueryIn struct {
	SQL    string `json:"sql" jsonschema:"a single read-only SELECT (or WITH ... SELECT); use $1, $2 ... placeholders for values"`
	Params []any  `json:"params,omitempty" jsonschema:"values for $1, $2 ... placeholders, in order"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum rows to return; defaults to and is capped by the server's MAX_ROWS"`
}

type ExplainIn struct {
	SQL     string `json:"sql" jsonschema:"a single read-only SELECT to explain"`
	Params  []any  `json:"params,omitempty" jsonschema:"values for $1, $2 ... placeholders, in order"`
	Analyze bool   `json:"analyze,omitempty" jsonschema:"run the query to report actual timings and buffers (EXPLAIN ANALYZE); default false"`
}

type ExplainOut struct {
	Plan any `json:"plan" jsonschema:"EXPLAIN (FORMAT JSON) output"`
}

// Register adds every tool to srv. Each call is written to aud; audit write
// failures are logged to log and do not fail the call.
func Register(srv *mcp.Server, store Store, aud Auditor, log *slog.Logger) {
	r := registrar{aud: aud, log: log}

	addTool(r, srv, &mcp.Tool{
		Name:        ListSchemas,
		Description: "List the database schemas this server allows reading from.",
	}, func(ctx context.Context, _ ListSchemasIn, e *audit.Entry) (ListSchemasOut, error) {
		s, err := store.ListSchemas(ctx)
		e.Rows = len(s)
		return ListSchemasOut{Schemas: s}, err
	})

	addTool(r, srv, &mcp.Tool{
		Name:        ListTables,
		Description: "List tables, views and materialized views in a schema, with planner row estimates.",
	}, func(ctx context.Context, in ListTablesIn, e *audit.Entry) (ListTablesOut, error) {
		e.Target = in.Schema
		t, err := store.ListTables(ctx, in.Schema)
		e.Rows = len(t)
		return ListTablesOut{Schema: in.Schema, Tables: t}, err
	})

	addTool(r, srv, &mcp.Tool{
		Name:        DescribeTable,
		Description: "Describe a table or view: columns with types, nullability and defaults, primary key, foreign keys and other constraints, and indexes.",
	}, func(ctx context.Context, in DescribeTableIn, e *audit.Entry) (db.TableDetail, error) {
		e.Target = in.Schema + "." + in.Table
		d, err := store.DescribeTable(ctx, in.Schema, in.Table)
		e.Rows = len(d.Columns)
		return d, err
	})

	addTool(r, srv, &mcp.Tool{
		Name: Query,
		Description: "Run one read-only SELECT and return rows as JSON. Runs in a read-only transaction with a timeout; " +
			"results are capped and marked truncated when more rows exist. Pass values as params with $1, $2 placeholders " +
			"instead of inlining them. Writes, DDL, multiple statements, locking clauses and denied tables are rejected.",
	}, func(ctx context.Context, in QueryIn, e *audit.Entry) (db.Result, error) {
		e.SQL, e.ParamCount = in.SQL, len(in.Params)
		res, err := store.Query(ctx, in.SQL, in.Params, in.Limit)
		e.Rows, e.Truncated = res.RowCount, res.Truncated
		return res, err
	})

	addTool(r, srv, &mcp.Tool{
		Name: ExplainQuery,
		Description: "Show the query plan for one read-only SELECT as EXPLAIN (FORMAT JSON). The query is not executed unless " +
			"analyze is true, in which case it runs read-only under the server timeout.",
	}, func(ctx context.Context, in ExplainIn, e *audit.Entry) (ExplainOut, error) {
		e.SQL, e.ParamCount = in.SQL, len(in.Params)
		plan, err := store.Explain(ctx, in.SQL, in.Params, in.Analyze)
		return ExplainOut{Plan: plan}, err
	})
}

type registrar struct {
	aud Auditor
	log *slog.Logger
}

// addTool registers a read-only tool whose handler fills in an audit entry.
// The entry is written once the handler returns, whatever the outcome.
func addTool[In, Out any](r registrar, srv *mcp.Server, t *mcp.Tool, h func(context.Context, In, *audit.Entry) (Out, error)) {
	t.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(false)}
	mcp.AddTool(srv, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		e := audit.Entry{Time: time.Now().UTC(), Tool: t.Name}
		if ss := req.Session; ss != nil {
			e.Session = ss.ID()
			if p := ss.InitializeParams(); p != nil && p.ClientInfo != nil {
				e.Client = p.ClientInfo.Name
			}
		}
		out, err := h(ctx, in, &e)
		e.Duration = time.Since(e.Time)
		if err != nil {
			e.Error = err.Error()
			e.Denied = errors.Is(err, guard.ErrDenied)
		}
		if werr := r.aud.Write(e); werr != nil {
			r.log.Error("audit write failed", "tool", t.Name, "error", werr)
		}
		if err != nil {
			var zero Out
			return nil, zero, err
		}
		return nil, out, nil
	})
}
