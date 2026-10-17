// Package audit records every tool call as an append-only JSON line: who
// called which tool with which (redacted) arguments, outcome, rows and latency.
package audit

import "time"

// Entry is one audited tool call. Query parameter values are never recorded,
// only their count, so audit logs cannot leak data.
type Entry struct {
	Time       time.Time     `json:"time"`
	Client     string        `json:"client"`
	Tool       string        `json:"tool"`
	SQL        string        `json:"sql,omitempty"`
	ParamCount int           `json:"param_count"`
	Rows       int           `json:"rows"`
	Duration   time.Duration `json:"duration_ns"`
	Error      string        `json:"error,omitempty"`
}
