package guard

import (
	"errors"
	"slices"
	"testing"
)

func TestValidateSelect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		sql     string
		want    string // cleaned SQL; ignored when wantErr
		wantErr bool
	}{
		{name: "simple select", sql: "SELECT 1", want: "SELECT 1"},
		{name: "trailing semicolon", sql: "select * from orders;  ", want: "select * from orders"},
		{name: "trailing semicolon and comment", sql: "SELECT 1; -- done; really", want: "SELECT 1"},
		{name: "trailing block comment", sql: "SELECT 1 /* a; b */", want: "SELECT 1 /* a; b */"},
		{name: "cte", sql: "WITH t AS (SELECT 1 AS x) SELECT x FROM t", want: "WITH t AS (SELECT 1 AS x) SELECT x FROM t"},
		{name: "values", sql: "VALUES (1), (2)", want: "VALUES (1), (2)"},
		{name: "table", sql: "TABLE orders", want: "TABLE orders"},
		{name: "parenthesised union", sql: "(SELECT 1) UNION (SELECT 2)", want: "(SELECT 1) UNION (SELECT 2)"},
		{name: "params", sql: "SELECT * FROM t WHERE id = $1 AND x = $2", want: "SELECT * FROM t WHERE id = $1 AND x = $2"},
		{name: "keyword inside string", sql: "SELECT 'delete; drop table x' AS s", want: "SELECT 'delete; drop table x' AS s"},
		{name: "keyword inside quoted ident", sql: `SELECT "update" FROM t`, want: `SELECT "update" FROM t`},
		{name: "keyword in comment", sql: "SELECT 1 -- insert into\n", want: "SELECT 1 -- insert into"},
		{name: "doubled quote", sql: "SELECT 'it''s; fine'", want: "SELECT 'it''s; fine'"},
		{name: "escape string", sql: `SELECT E'a\'; delete'`, want: `SELECT E'a\'; delete'`},
		{name: "dollar quoted", sql: "SELECT $$; insert $$, $q$ ; $q$", want: "SELECT $$; insert $$, $q$ ; $q$"},
		{name: "nested comment", sql: "SELECT /* a /* b */ ; */ 1", want: "SELECT /* a /* b */ ; */ 1"},
		{name: "for in non-locking position", sql: "SELECT substring('abc' FROM 1 FOR 2)", want: "SELECT substring('abc' FROM 1 FOR 2)"},
		{name: "allowed function", sql: "SELECT count(*), now() FROM t", want: "SELECT count(*), now() FROM t"},

		{name: "empty", sql: "  ", wantErr: true},
		{name: "only comment", sql: "-- nothing", wantErr: true},
		{name: "insert", sql: "INSERT INTO t VALUES (1)", wantErr: true},
		{name: "update", sql: "UPDATE t SET x = 1", wantErr: true},
		{name: "drop", sql: "DROP TABLE t", wantErr: true},
		{name: "explain", sql: "EXPLAIN ANALYZE DELETE FROM t", wantErr: true},
		{name: "multiple statements", sql: "SELECT 1; DROP TABLE t", wantErr: true},
		{name: "multiple selects", sql: "SELECT 1; SELECT 2", wantErr: true},
		{name: "statement hidden after comment", sql: "SELECT 1; /* x */ DELETE FROM t", wantErr: true},
		{name: "data-modifying cte", sql: "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", wantErr: true},
		{name: "select into", sql: "SELECT * INTO new_t FROM t", wantErr: true},
		{name: "for update", sql: "SELECT * FROM t FOR UPDATE", wantErr: true},
		{name: "for share", sql: "SELECT * FROM t FOR SHARE", wantErr: true},
		{name: "for no key update", sql: "SELECT * FROM t for no key update", wantErr: true},
		{name: "set_config", sql: "SELECT set_config('statement_timeout', '0', false)", wantErr: true},
		{name: "qualified set_config", sql: "SELECT pg_catalog.set_config('a', 'b', true)", wantErr: true},
		{name: "quoted set_config", sql: `SELECT "set_config"('a', 'b', true)`, wantErr: true},
		{name: "set_config with comment before paren", sql: "SELECT set_config /* x */ ('a','b',true)", wantErr: true},
		{name: "query_to_xml", sql: "SELECT query_to_xml('select * from users', true, true, '')", wantErr: true},
		{name: "pg_read_file", sql: "SELECT pg_read_file('/etc/passwd')", wantErr: true},
		{name: "dblink", sql: "SELECT * FROM dblink('x', 'y') AS t(a int)", wantErr: true},
		{name: "lo_export", sql: "SELECT lo_export(1, '/tmp/x')", wantErr: true},
		{name: "pg_sleep", sql: "SELECT pg_sleep(100)", wantErr: true},
		{name: "advisory lock", sql: "SELECT pg_advisory_lock(1)", wantErr: true},
		{name: "unicode escape ident", sql: `SELECT U&"\0073et_config"('a','b',true)`, wantErr: true},
		{name: "unterminated string", sql: "SELECT 'abc", wantErr: true},
		{name: "unterminated comment", sql: "SELECT 1 /* abc", wantErr: true},
		{name: "unterminated dollar", sql: "SELECT $x$ abc", wantErr: true},
		{name: "comment hides nothing", sql: "/* SELECT */ DELETE FROM t", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateSelect(tt.sql)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ValidateSelect(%q) = %q, want error", tt.sql, got)
				}
				if !errors.Is(err, ErrDenied) {
					t.Fatalf("error %v does not wrap ErrDenied", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateSelect(%q) error: %v", tt.sql, err)
			}
			if got != tt.want {
				t.Errorf("ValidateSelect(%q) = %q, want %q", tt.sql, got, tt.want)
			}
		})
	}
}

func TestPolicy(t *testing.T) {
	t.Parallel()
	p := Policy{AllowSchemas: []string{"public", "sales"}, DenyTables: []string{"public.users"}, MaxRows: 100}
	tests := []struct {
		rel  Relation
		want bool
	}{
		{Relation{"public", "orders"}, true},
		{Relation{"sales", "users"}, true},
		{Relation{"public", "users"}, false},
		{Relation{"pg_catalog", "pg_authid"}, false},
		{Relation{"Public", "orders"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.rel.String(), func(t *testing.T) {
			t.Parallel()
			if got := p.RelationAllowed(tt.rel); got != tt.want {
				t.Errorf("RelationAllowed(%s) = %v, want %v", tt.rel, got, tt.want)
			}
			err := p.CheckRelations([]Relation{{"public", "orders"}, tt.rel})
			if (err == nil) != tt.want {
				t.Errorf("CheckRelations error = %v, want allowed=%v", err, tt.want)
			}
			if err != nil && !errors.Is(err, ErrDenied) {
				t.Errorf("error %v does not wrap ErrDenied", err)
			}
		})
	}
}

func TestClampLimit(t *testing.T) {
	t.Parallel()
	p := Policy{MaxRows: 50}
	for req, want := range map[int]int{0: 50, -1: 50, 10: 10, 50: 50, 51: 50} {
		if got := p.ClampLimit(req); got != want {
			t.Errorf("ClampLimit(%d) = %d, want %d", req, got, want)
		}
	}
}

func TestPlanRelations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		plan    string
		want    []Relation
		wantErr bool
	}{
		{
			name: "no relations",
			plan: `[{"Plan":{"Node Type":"Result"}}]`,
		},
		{
			name: "nested join with duplicate",
			plan: `[{"Plan":{"Node Type":"Hash Join","Plans":[
				{"Node Type":"Seq Scan","Relation Name":"orders","Schema":"public","Alias":"o"},
				{"Node Type":"Hash","Plans":[
					{"Node Type":"Index Scan","Relation Name":"users","Schema":"public","Alias":"u"},
					{"Node Type":"Seq Scan","Relation Name":"orders","Schema":"public","Alias":"o2"}
				]}
			]}}]`,
			want: []Relation{{"public", "orders"}, {"public", "users"}},
		},
		{
			name:    "missing schema fails closed",
			plan:    `[{"Plan":{"Node Type":"Seq Scan","Relation Name":"orders"}}]`,
			wantErr: true,
		},
		{name: "invalid json", plan: `{`, wantErr: true},
		{name: "missing plan", plan: `[{}]`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := PlanRelations([]byte(tt.plan))
			if (err != nil) != tt.wantErr {
				t.Fatalf("PlanRelations() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("PlanRelations() = %v, want %v", got, tt.want)
			}
		})
	}
}

func FuzzValidateSelect(f *testing.F) {
	for _, s := range []string{"SELECT 1", "SELECT 'a'';' ; x", "SELECT $$x$$", "/* /* */ */ SELECT", "SELECT E'\\''"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		got, err := ValidateSelect(sql)
		if err != nil {
			return
		}
		// Whatever passes must still pass once cleaned (wrapping relies on it).
		if _, err := ValidateSelect(got); err != nil {
			t.Fatalf("cleaned SQL %q no longer validates: %v", got, err)
		}
	})
}
