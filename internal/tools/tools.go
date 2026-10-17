// Package tools defines the MCP tools the server exposes.
package tools

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
