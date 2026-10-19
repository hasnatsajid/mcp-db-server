//go:build integration

package db

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hasnatsajid/mcp-db-server/internal/guard"
)

// The integration tests need MCP_TEST_DATABASE_URL: a role that can create
// schemas in a disposable database. Fixtures live in schema mcp_it, which is
// dropped and recreated. They deliberately connect with that privileged role:
// the guard layers must hold even without the least-privilege role.

const fixtures = `
DROP SCHEMA IF EXISTS mcp_it CASCADE;
DROP SCHEMA IF EXISTS mcp_it_other CASCADE;
CREATE SCHEMA mcp_it;
CREATE SCHEMA mcp_it_other;
CREATE TABLE mcp_it_other.hidden (id int);
CREATE TABLE mcp_it.customers (id bigint PRIMARY KEY, name text NOT NULL);
CREATE TABLE mcp_it.items (
	id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	customer_id bigint REFERENCES mcp_it.customers(id),
	name text NOT NULL,
	price numeric(10,2) NOT NULL DEFAULT 0,
	created_at timestamptz NOT NULL DEFAULT '2025-01-01T00:00:00Z'
);
CREATE INDEX items_name_idx ON mcp_it.items (name);
COMMENT ON TABLE mcp_it.items IS 'things';
CREATE TABLE mcp_it.secrets (id int, value text);
CREATE VIEW mcp_it.secret_view AS SELECT id FROM mcp_it.secrets;
CREATE TABLE mcp_it.events (id int, at date) PARTITION BY RANGE (at);
CREATE TABLE mcp_it.events_2025 PARTITION OF mcp_it.events FOR VALUES FROM ('2025-01-01') TO ('2026-01-01');
CREATE SEQUENCE mcp_it.seq;
CREATE TABLE mcp_it.types (u uuid, j jsonb, b bytea, iv interval, f float8, arr int[], ip inet);
INSERT INTO mcp_it.customers VALUES (1, 'Ann'), (2, 'Bo');
INSERT INTO mcp_it.items (customer_id, name, price) SELECT 1 + g % 2, 'item ' || g, g FROM generate_series(1, 30) g;
INSERT INTO mcp_it.secrets VALUES (1, 'shh');
INSERT INTO mcp_it.events VALUES (1, '2025-06-01');
INSERT INTO mcp_it.types VALUES ('a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11', '{"k":[1,2]}', '\xdead', '1 day 2 hours', 'NaN', '{1,2}', '192.0.2.1');
ANALYZE mcp_it.items;
`

func setup(t *testing.T) (*Executor, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("MCP_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("MCP_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, fixtures, pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatalf("fixtures: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA mcp_it CASCADE; DROP SCHEMA mcp_it_other CASCADE", pgx.QueryExecModeSimpleProtocol)
	})

	pool, err := Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	policy := guard.Policy{
		AllowSchemas: []string{"mcp_it"},
		DenyTables:   []string{"mcp_it.secrets", "mcp_it.events"},
		MaxRows:      10,
	}
	return New(pool, policy, 500*time.Millisecond), pool
}

func TestQuery(t *testing.T) {
	e, _ := setup(t)
	ctx := context.Background()

	t.Run("rows, columns and truncation", func(t *testing.T) {
		res, err := e.Query(ctx, "SELECT id, name, price FROM mcp_it.items ORDER BY id", nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if res.RowCount != 10 || !res.Truncated {
			t.Errorf("got %d rows, truncated=%v; want 10, true", res.RowCount, res.Truncated)
		}
		want := []ResultColumn{{"id", "int8"}, {"name", "text"}, {"price", "numeric"}}
		if !slices.Equal(res.Columns, want) {
			t.Errorf("columns = %v, want %v", res.Columns, want)
		}
	})

	t.Run("limit and params", func(t *testing.T) {
		res, err := e.Query(ctx, "SELECT name FROM mcp_it.items WHERE customer_id = $1 AND name LIKE $2 ORDER BY id;",
			[]any{float64(1), "item %"}, 3)
		if err != nil {
			t.Fatal(err)
		}
		if res.RowCount != 3 || !res.Truncated || res.Rows[0][0] != "item 2" {
			t.Errorf("got %+v", res)
		}
	})

	t.Run("not truncated when exactly at limit", func(t *testing.T) {
		res, err := e.Query(ctx, "SELECT * FROM mcp_it.customers", nil, 2)
		if err != nil || res.RowCount != 2 || res.Truncated {
			t.Errorf("got %+v, %v", res, err)
		}
	})

	t.Run("trailing comment does not break wrapping", func(t *testing.T) {
		if _, err := e.Query(ctx, "SELECT 1 -- trailing comment", nil, 0); err != nil {
			t.Error(err)
		}
	})

	t.Run("view over allowed tables", func(t *testing.T) {
		if _, err := e.Query(ctx, "WITH c AS (SELECT * FROM mcp_it.customers) SELECT * FROM c JOIN mcp_it.items i ON i.customer_id = c.id", nil, 0); err != nil {
			t.Error(err)
		}
	})

	t.Run("value types marshal to JSON", func(t *testing.T) {
		res, err := e.Query(ctx, "SELECT * FROM mcp_it.types", nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"`, `{"k":[1,2]}`, `"\\xdead"`, `"NaN"`, `[1,2]`, `"192.0.2.1/32"`} {
			if !strings.Contains(string(b), want) {
				t.Errorf("result %s does not contain %s", b, want)
			}
		}
	})
}

func TestQueryRejected(t *testing.T) {
	e, _ := setup(t)
	ctx := context.Background()
	tests := []struct {
		name string
		sql  string
	}{
		{"insert", "INSERT INTO mcp_it.customers VALUES (9, 'x')"},
		{"update", "UPDATE mcp_it.customers SET name = 'x'"},
		{"delete", "DELETE FROM mcp_it.customers"},
		{"ddl", "DROP TABLE mcp_it.customers"},
		{"truncate", "TRUNCATE mcp_it.customers"},
		{"multiple statements", "SELECT 1; DELETE FROM mcp_it.customers"},
		{"data-modifying cte", "WITH d AS (DELETE FROM mcp_it.customers RETURNING *) SELECT * FROM d"},
		{"select into", "SELECT * INTO mcp_it.copy FROM mcp_it.customers"},
		{"for update", "SELECT * FROM mcp_it.customers FOR UPDATE"},
		{"set_config", "SELECT set_config('statement_timeout', '0', false)"},
		{"query_to_xml", "SELECT query_to_xml('SELECT * FROM mcp_it.secrets', true, false, '')"},
		{"pg_read_file", "SELECT pg_read_file('postgresql.conf')"},
		{"denied table", "SELECT * FROM mcp_it.secrets"},
		{"denied table unqualified via search_path", "SELECT * FROM pg_catalog.pg_class, mcp_it.secrets"},
		{"denied table via view", "SELECT * FROM mcp_it.secret_view"},
		{"denied table in subquery", "SELECT 1 WHERE EXISTS (SELECT 1 FROM mcp_it.secrets)"},
		{"denied table in scalar subquery", "SELECT (SELECT value FROM mcp_it.secrets LIMIT 1)"},
		{"denied table in cte", "WITH s AS (SELECT * FROM mcp_it.secrets) SELECT * FROM s"},
		{"denied table in union", "SELECT name FROM mcp_it.customers UNION ALL SELECT value FROM mcp_it.secrets"},
		{"denied partitioned parent", "SELECT * FROM mcp_it.events"},
		{"partition of denied parent", "SELECT * FROM mcp_it.events_2025"},
		{"schema not allowed", "SELECT * FROM mcp_it_other.hidden"},
		{"catalog not allowed", "SELECT rolname FROM pg_catalog.pg_roles"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.Query(ctx, tt.sql, nil, 0)
			if !errors.Is(err, guard.ErrDenied) {
				t.Errorf("Query(%q) error = %v, want ErrDenied", tt.sql, err)
			}
			_, err = e.Explain(ctx, tt.sql, nil, true)
			if !errors.Is(err, guard.ErrDenied) {
				t.Errorf("Explain(%q) error = %v, want ErrDenied", tt.sql, err)
			}
		})
	}
}

// TestReadOnlyTransaction bypasses the validator to show the transaction
// layer alone refuses writes.
func TestReadOnlyTransaction(t *testing.T) {
	e, pool := setup(t)
	ctx := context.Background()
	for _, sql := range []string{
		"INSERT INTO mcp_it.customers VALUES (9, 'x')",
		"CREATE TABLE mcp_it.nope (id int)",
		"SELECT nextval('mcp_it.seq')",
	} {
		err := e.readOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "25006" { // read_only_sql_transaction
			t.Errorf("%q: error = %v, want SQLSTATE 25006", sql, err)
		}
	}
	// nextval passes the validator, so the transaction is what stops it.
	if _, err := e.Query(ctx, "SELECT nextval('mcp_it.seq')", nil, 0); err == nil || !strings.Contains(err.Error(), "25006") {
		t.Errorf("nextval error = %v, want read-only transaction error", err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM mcp_it.customers").Scan(&n); err != nil || n != 2 {
		t.Errorf("customers = %d, %v; data changed", n, err)
	}
}

func TestStatementTimeout(t *testing.T) {
	e, _ := setup(t)
	start := time.Now()
	_, err := e.Query(context.Background(), "SELECT count(*) FROM generate_series(1, 1000000000)", nil, 0)
	if err == nil || !strings.Contains(err.Error(), "57014") { // query_canceled
		t.Errorf("error = %v, want statement timeout (SQLSTATE 57014)", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("timeout took %s", d)
	}
}

func TestExplain(t *testing.T) {
	e, _ := setup(t)
	ctx := context.Background()
	for _, analyze := range []bool{false, true} {
		plan, err := e.Explain(ctx, "SELECT * FROM mcp_it.items WHERE id = $1", []any{float64(3)}, analyze)
		if err != nil {
			t.Fatalf("analyze=%v: %v", analyze, err)
		}
		b, _ := json.Marshal(plan)
		if !strings.Contains(string(b), `"Relation Name":"items"`) {
			t.Errorf("analyze=%v: plan %s does not mention items", analyze, b)
		}
		if got := strings.Contains(string(b), "Actual Rows"); got != analyze {
			t.Errorf("analyze=%v: has actual rows = %v", analyze, got)
		}
	}
}

func TestIntrospection(t *testing.T) {
	e, _ := setup(t)
	ctx := context.Background()

	schemas, err := e.ListSchemas(ctx)
	if err != nil || len(schemas) != 1 || schemas[0].Name != "mcp_it" || !schemas[0].Usage {
		t.Errorf("ListSchemas = %+v, %v", schemas, err)
	}

	tables, err := e.ListTables(ctx, "mcp_it")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tb := range tables {
		names = append(names, tb.Name)
	}
	if want := []string{"customers", "items", "secret_view", "types"}; !slices.Equal(names, want) {
		t.Errorf("ListTables = %v, want %v (no denied tables, no partitions)", names, want)
	}
	if _, err := e.ListTables(ctx, "mcp_it_other"); !errors.Is(err, guard.ErrDenied) {
		t.Errorf("ListTables(disallowed) error = %v", err)
	}

	d, err := e.DescribeTable(ctx, "mcp_it", "items")
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != "table" || d.Comment != "things" || len(d.Columns) != 5 || !slices.Equal(d.PrimaryKey, []string{"id"}) {
		t.Errorf("DescribeTable = %+v", d)
	}
	var fk *Constraint
	for i, c := range d.Constraints {
		if c.Type == "foreign key" {
			fk = &d.Constraints[i]
		}
	}
	if fk == nil || fk.References == nil || fk.References.Table != "customers" || !slices.Equal(fk.Columns, []string{"customer_id"}) {
		t.Errorf("foreign key = %+v", fk)
	}
	if len(d.Indexes) != 2 {
		t.Errorf("indexes = %+v", d.Indexes)
	}
	if c := d.Columns[3]; c.Name != "price" || c.Type != "numeric(10,2)" || c.Nullable || c.Default == nil {
		t.Errorf("price column = %+v", c)
	}

	if _, err := e.DescribeTable(ctx, "mcp_it", "secrets"); !errors.Is(err, guard.ErrDenied) {
		t.Errorf("DescribeTable(denied) error = %v", err)
	}
	if _, err := e.DescribeTable(ctx, "mcp_it", "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DescribeTable(missing) error = %v", err)
	}
}

func TestConnectErrorRedacted(t *testing.T) {
	const secret = "pw-should-not-appear"
	_, err := Connect(context.Background(), "postgres://someone:"+secret+"@127.0.0.1:1/x?connect_timeout=2&sslmode=disable")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, leak := range []string{secret, "127.0.0.1", "someone"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error %q leaks %q", err, leak)
		}
	}
}
