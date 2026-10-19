package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hasnatsajid/mcp-db-server/internal/guard"
)

// Schema is one allowed schema.
type Schema struct {
	Name    string `json:"name"`
	Comment string `json:"comment,omitempty"`
	Usage   bool   `json:"usage" jsonschema:"whether the connected role has USAGE on the schema"`
}

// Table is one table-like relation in a schema.
type Table struct {
	Name         string `json:"name"`
	Kind         string `json:"kind" jsonschema:"table, partitioned table, view, materialized view or foreign table"`
	RowsEstimate *int64 `json:"rows_estimate,omitempty" jsonschema:"planner row estimate; absent if the table was never analyzed"`
	Comment      string `json:"comment,omitempty"`
}

// TableDetail describes a table's structure.
type TableDetail struct {
	Schema      string       `json:"schema"`
	Name        string       `json:"name"`
	Kind        string       `json:"kind"`
	Comment     string       `json:"comment,omitempty"`
	Columns     []Column     `json:"columns"`
	PrimaryKey  []string     `json:"primary_key,omitempty"`
	Constraints []Constraint `json:"constraints"`
	Indexes     []Index      `json:"indexes"`
}

// Column is one table column.
type Column struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Nullable bool    `json:"nullable"`
	Default  *string `json:"default,omitempty"`
	Comment  string  `json:"comment,omitempty"`
}

// Constraint is a primary key, foreign key, unique, check or exclusion
// constraint.
type Constraint struct {
	Name       string     `json:"name"`
	Type       string     `json:"type"`
	Columns    []string   `json:"columns,omitempty"`
	Definition string     `json:"definition"`
	References *Reference `json:"references,omitempty"`
}

// Reference is the target of a foreign key.
type Reference struct {
	Schema  string   `json:"schema"`
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
}

// Index is one index on a table.
type Index struct {
	Name       string `json:"name"`
	Unique     bool   `json:"unique"`
	Primary    bool   `json:"primary"`
	Definition string `json:"definition"`
}

const relKindCase = `CASE c.relkind
	WHEN 'r' THEN 'table' WHEN 'p' THEN 'partitioned table' WHEN 'v' THEN 'view'
	WHEN 'm' THEN 'materialized view' WHEN 'f' THEN 'foreign table' END`

// ListSchemas returns the allow-listed schemas that exist.
func (e *Executor) ListSchemas(ctx context.Context) ([]Schema, error) {
	out := []Schema{}
	err := e.readOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT n.nspname, coalesce(obj_description(n.oid, 'pg_namespace'), ''),
			       has_schema_privilege(n.oid, 'USAGE')
			FROM pg_namespace n
			WHERE n.nspname = ANY($1)
			ORDER BY n.nspname`, e.policy.AllowSchemas)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Schema, error) {
			var s Schema
			return s, r.Scan(&s.Name, &s.Comment, &s.Usage)
		})
		return err
	})
	return out, err
}

// ListTables returns the tables, views and materialized views in schema,
// minus denied tables. Partitions are omitted; their parent is listed.
func (e *Executor) ListTables(ctx context.Context, schema string) ([]Table, error) {
	if err := e.policy.CheckSchema(schema); err != nil {
		return nil, err
	}
	out := []Table{}
	err := e.readOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT c.relname, `+relKindCase+`,
			       CASE WHEN c.reltuples < 0 THEN NULL ELSE c.reltuples::bigint END,
			       coalesce(obj_description(c.oid, 'pg_class'), '')
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm', 'f') AND NOT c.relispartition
			ORDER BY c.relname`, schema)
		if err != nil {
			return err
		}
		tables, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Table, error) {
			var t Table
			return t, r.Scan(&t.Name, &t.Kind, &t.RowsEstimate, &t.Comment)
		})
		if err != nil {
			return err
		}
		for _, t := range tables {
			if e.policy.RelationAllowed(guard.Relation{Schema: schema, Name: t.Name}) {
				out = append(out, t)
			}
		}
		return nil
	})
	return out, err
}

// DescribeTable returns columns, constraints and indexes of schema.table.
func (e *Executor) DescribeTable(ctx context.Context, schema, table string) (TableDetail, error) {
	rel := guard.Relation{Schema: schema, Name: table}
	if err := e.policy.CheckRelations([]guard.Relation{rel}); err != nil {
		return TableDetail{}, err
	}
	d := TableDetail{Schema: schema, Name: table, Columns: []Column{}, Constraints: []Constraint{}, Indexes: []Index{}}
	err := e.readOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var oid uint32
		err := tx.QueryRow(ctx, `
			SELECT c.oid, `+relKindCase+`, coalesce(obj_description(c.oid, 'pg_class'), '')
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r', 'p', 'v', 'm', 'f')`,
			schema, table).Scan(&oid, &d.Kind, &d.Comment)
		if err == pgx.ErrNoRows {
			return fmt.Errorf("table %s: %w", rel, ErrNotFound)
		}
		if err != nil {
			return err
		}

		rows, err := tx.Query(ctx, `
			SELECT a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
			       pg_get_expr(d.adbin, d.adrelid), coalesce(col_description(a.attrelid, a.attnum), '')
			FROM pg_attribute a
			LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
			WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
			ORDER BY a.attnum`, oid)
		if err != nil {
			return err
		}
		if d.Columns, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Column, error) {
			var c Column
			return c, r.Scan(&c.Name, &c.Type, &c.Nullable, &c.Default, &c.Comment)
		}); err != nil {
			return fmt.Errorf("columns: %w", err)
		}

		rows, err = tx.Query(ctx, `
			SELECT con.conname, con.contype::text, pg_get_constraintdef(con.oid),
			       ARRAY(SELECT a.attname FROM unnest(con.conkey) WITH ORDINALITY k(num, ord)
			             JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.num
			             ORDER BY k.ord),
			       coalesce(fn.nspname, ''), coalesce(fc.relname, ''),
			       ARRAY(SELECT a.attname FROM unnest(con.confkey) WITH ORDINALITY k(num, ord)
			             JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.num
			             ORDER BY k.ord)
			FROM pg_constraint con
			LEFT JOIN pg_class fc ON fc.oid = con.confrelid
			LEFT JOIN pg_namespace fn ON fn.oid = fc.relnamespace
			WHERE con.conrelid = $1
			ORDER BY con.contype, con.conname`, oid)
		if err != nil {
			return err
		}
		if d.Constraints, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Constraint, error) {
			var c Constraint
			var typ string
			var ref Reference
			if err := r.Scan(&c.Name, &typ, &c.Definition, &c.Columns, &ref.Schema, &ref.Table, &ref.Columns); err != nil {
				return c, err
			}
			c.Type = constraintType(typ)
			if typ == "f" {
				c.References = &ref
			}
			return c, nil
		}); err != nil {
			return fmt.Errorf("constraints: %w", err)
		}
		for _, c := range d.Constraints {
			if c.Type == "primary key" {
				d.PrimaryKey = c.Columns
			}
		}

		rows, err = tx.Query(ctx, `
			SELECT ic.relname, i.indisunique, i.indisprimary, pg_get_indexdef(i.indexrelid)
			FROM pg_index i
			JOIN pg_class ic ON ic.oid = i.indexrelid
			WHERE i.indrelid = $1
			ORDER BY ic.relname`, oid)
		if err != nil {
			return err
		}
		if d.Indexes, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Index, error) {
			var ix Index
			return ix, r.Scan(&ix.Name, &ix.Unique, &ix.Primary, &ix.Definition)
		}); err != nil {
			return fmt.Errorf("indexes: %w", err)
		}
		return nil
	})
	return d, err
}

func constraintType(contype string) string {
	switch contype {
	case "p":
		return "primary key"
	case "f":
		return "foreign key"
	case "u":
		return "unique"
	case "c":
		return "check"
	case "x":
		return "exclusion"
	case "n":
		return "not null"
	case "t":
		return "trigger"
	}
	return contype
}
