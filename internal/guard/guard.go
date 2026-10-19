// Package guard is the read-only enforcement layer. It is defence in depth;
// no single layer is trusted on its own:
//
//  1. The server connects as a Postgres role with only SELECT grants.
//  2. Every query runs in BEGIN READ ONLY with statement_timeout set.
//  3. Statements are lexed and rejected unless they are a single SELECT.
//  4. Schema allow lists and table deny lists are checked against the
//     relations in the query plan before execution.
//  5. Results are capped at MaxRows.
package guard

import (
	"errors"
	"fmt"
	"slices"
)

// ErrDenied is returned when a statement or object is not permitted.
var ErrDenied = errors.New("guard: denied")

// Policy holds allow/deny rules. Deny always wins over allow.
type Policy struct {
	AllowSchemas []string
	DenyTables   []string // schema-qualified, e.g. "public.users"
	MaxRows      int
}

// Relation is a schema-qualified table, view or other relation.
type Relation struct {
	Schema string
	Name   string
}

func (r Relation) String() string { return r.Schema + "." + r.Name }

// SchemaAllowed reports whether objects in schema may be read.
func (p Policy) SchemaAllowed(schema string) bool {
	return slices.Contains(p.AllowSchemas, schema)
}

// RelationAllowed reports whether r may be read: its schema is allowed and
// it is not on the deny list.
func (p Policy) RelationAllowed(r Relation) bool {
	return p.SchemaAllowed(r.Schema) && !slices.Contains(p.DenyTables, r.String())
}

// CheckSchema returns an ErrDenied-wrapping error if schema is not allowed.
func (p Policy) CheckSchema(schema string) error {
	if !p.SchemaAllowed(schema) {
		return fmt.Errorf("%w: schema %q is not in the allow list", ErrDenied, schema)
	}
	return nil
}

// CheckRelations returns an ErrDenied-wrapping error naming the first
// relation that may not be read.
func (p Policy) CheckRelations(rels []Relation) error {
	for _, r := range rels {
		if !p.SchemaAllowed(r.Schema) {
			return fmt.Errorf("%w: query reads %s, schema %q is not in the allow list", ErrDenied, r, r.Schema)
		}
		if !p.RelationAllowed(r) {
			return fmt.Errorf("%w: query reads %s, which is on the deny list", ErrDenied, r)
		}
	}
	return nil
}

// ClampLimit returns the row limit to apply for a requested limit: the
// policy maximum when requested is zero or larger than the maximum.
func (p Policy) ClampLimit(requested int) int {
	if requested <= 0 || requested > p.MaxRows {
		return p.MaxRows
	}
	return requested
}
