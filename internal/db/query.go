package db

import (
	"context"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/hasnatsajid/mcp-db-server/internal/guard"
)

// ResultColumn names and types one column of a query result.
type ResultColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Result is a capped query result.
type Result struct {
	Columns   []ResultColumn `json:"columns"`
	Rows      [][]any        `json:"rows"`
	RowCount  int            `json:"row_count"`
	Truncated bool           `json:"truncated" jsonschema:"true if more rows existed than the limit; refine the query or aggregate"`
}

// Query validates sql, checks the relations it reads against the policy,
// and runs it with at most limit rows (clamped to the policy maximum).
func (e *Executor) Query(ctx context.Context, sql string, params []any, limit int) (Result, error) {
	clean, err := guard.ValidateSelect(sql)
	if err != nil {
		return Result{}, err
	}
	limit = e.policy.ClampLimit(limit)
	// Wrapping caps the rows the server produces, not just the rows we read.
	// The newline keeps a trailing -- comment from swallowing the parenthesis.
	wrapped := fmt.Sprintf("SELECT * FROM (\n%s\n) AS mcp_query LIMIT %d", clean, limit+1)
	args := normalizeParams(params)

	res := Result{Columns: []ResultColumn{}, Rows: [][]any{}}
	err = e.readOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := e.checkPlan(ctx, tx, wrapped, args); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, wrapped, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		tm := tx.Conn().TypeMap()
		for _, fd := range rows.FieldDescriptions() {
			typ := strconv.FormatUint(uint64(fd.DataTypeOID), 10)
			if t, ok := tm.TypeForOID(fd.DataTypeOID); ok {
				typ = t.Name
			}
			res.Columns = append(res.Columns, ResultColumn{Name: fd.Name, Type: typ})
		}
		for rows.Next() {
			if len(res.Rows) == limit {
				res.Truncated = true
				break
			}
			vals, err := rows.Values()
			if err != nil {
				return err
			}
			for i, v := range vals {
				vals[i] = jsonValue(v)
			}
			res.Rows = append(res.Rows, vals)
		}
		rows.Close()
		return rows.Err()
	})
	if err != nil {
		return Result{}, err
	}
	res.RowCount = len(res.Rows)
	return res, nil
}

// Explain validates sql, checks the relations it reads and returns its plan
// as decoded EXPLAIN (FORMAT JSON) output. With analyze the statement is
// executed (still read-only and under the timeout) to report actual timings.
func (e *Executor) Explain(ctx context.Context, sql string, params []any, analyze bool) (any, error) {
	clean, err := guard.ValidateSelect(sql)
	if err != nil {
		return nil, err
	}
	args := normalizeParams(params)
	var plan any
	err = e.readOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
		raw, err := e.checkPlan(ctx, tx, clean, args)
		if err != nil {
			return err
		}
		if analyze {
			if err := tx.QueryRow(ctx, "EXPLAIN (ANALYZE, VERBOSE, BUFFERS, FORMAT JSON) "+clean, args...).Scan(&raw); err != nil {
				return err
			}
		}
		return json.Unmarshal(raw, &plan)
	})
	return plan, err
}

// checkPlan plans sql without executing it and rejects it if any relation
// in the plan, or any partitioned ancestor of one, is not allowed. It
// returns the raw EXPLAIN (VERBOSE, FORMAT JSON) output.
func (e *Executor) checkPlan(ctx context.Context, tx pgx.Tx, sql string, args []any) ([]byte, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, "EXPLAIN (VERBOSE, FORMAT JSON) "+sql, args...).Scan(&raw); err != nil {
		return nil, err
	}
	rels, err := guard.PlanRelations(raw)
	if err != nil {
		return nil, err
	}
	if len(rels) > 0 {
		// Queries on a partitioned table plan as scans of its partitions; a
		// denied parent must also deny its partitions.
		schemas, names := make([]string, len(rels)), make([]string, len(rels))
		for i, r := range rels {
			schemas[i], names[i] = r.Schema, r.Name
		}
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT n.nspname, c.relname
			FROM unnest($1::text[], $2::text[]) AS r(s, t)
			CROSS JOIN LATERAL pg_partition_ancestors(format('%I.%I', r.s, r.t)::regclass) AS a
			JOIN pg_class c ON c.oid = a.relid
			JOIN pg_namespace n ON n.oid = c.relnamespace`, schemas, names)
		if err != nil {
			return nil, err
		}
		ancestors, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (guard.Relation, error) {
			var rel guard.Relation
			return rel, r.Scan(&rel.Schema, &rel.Name)
		})
		if err != nil {
			return nil, err
		}
		rels = append(rels, ancestors...)
	}
	return raw, e.policy.CheckRelations(rels)
}

// normalizeParams converts JSON-decoded parameters to values pgx encodes
// predictably: integral numbers become int64 so they bind to integer columns.
func normalizeParams(params []any) []any {
	args := make([]any, len(params))
	for i, p := range params {
		switch v := p.(type) {
		case float64:
			if v == math.Trunc(v) && math.Abs(v) < 1<<53 {
				args[i] = int64(v)
				continue
			}
		case json.Number:
			if n, err := v.Int64(); err == nil {
				args[i] = n
				continue
			}
			if f, err := v.Float64(); err == nil {
				args[i] = f
				continue
			}
		}
		args[i] = p
	}
	return args
}

// jsonValue converts a decoded column value into something that marshals to
// readable JSON.
func jsonValue(v any) any {
	switch x := v.(type) {
	case nil, bool, string, int8, int16, int32, int64, int, uint32, uint64:
		return x
	case float32:
		return jsonFloat(float64(x))
	case float64:
		return jsonFloat(x)
	case [16]byte: // uuid
		return fmt.Sprintf("%x-%x-%x-%x-%x", x[0:4], x[4:6], x[6:8], x[8:10], x[10:16])
	case []byte: // bytea, shown as Postgres prints it
		return `\x` + hex.EncodeToString(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = jsonValue(e)
		}
		return out
	case map[string]any: // json, jsonb
		return x
	case json.Marshaler:
		if _, err := x.MarshalJSON(); err == nil {
			return x
		}
	case fmt.Stringer:
		return x.String()
	}
	if vr, ok := v.(driver.Valuer); ok {
		if dv, err := vr.Value(); err == nil {
			return jsonValue(dv)
		}
	}
	if _, err := json.Marshal(v); err == nil {
		return v
	}
	return fmt.Sprint(v)
}

// jsonFloat returns NaN and infinities as strings, which JSON cannot encode
// as numbers.
func jsonFloat(f float64) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return f
}
