// Package guard is the read-only enforcement layer. It is defence in depth;
// no single layer is trusted on its own:
//
//  1. The server connects as a Postgres role with only SELECT grants.
//  2. Every query runs in BEGIN READ ONLY with statement_timeout set.
//  3. Statements are parsed and rejected unless they are a single SELECT/EXPLAIN.
//  4. Schema allow lists and table deny lists are checked before execution.
//  5. Results are capped at MaxRows.
package guard

import "errors"

// ErrDenied is returned when a statement or object is not permitted.
var ErrDenied = errors.New("guard: denied")

// Policy holds allow/deny rules. Deny always wins over allow.
type Policy struct {
	AllowSchemas []string
	DenyTables   []string // schema-qualified, e.g. "public.users"
	MaxRows      int
}
