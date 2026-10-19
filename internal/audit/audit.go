// Package audit records every tool call as an append-only JSON line: who
// called which tool with which (redacted) arguments, outcome, rows and latency.
package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Entry is one audited tool call. Query parameter values are never recorded,
// only their count, so audit logs cannot leak data.
type Entry struct {
	Time       time.Time     `json:"time"`
	Client     string        `json:"client"`
	Session    string        `json:"session,omitempty"`
	Tool       string        `json:"tool"`
	Target     string        `json:"target,omitempty"` // schema or schema.table for introspection tools
	SQL        string        `json:"sql,omitempty"`
	ParamCount int           `json:"param_count"`
	Rows       int           `json:"rows"`
	Truncated  bool          `json:"truncated,omitempty"`
	Denied     bool          `json:"denied,omitempty"`
	Duration   time.Duration `json:"duration_ns"`
	Error      string        `json:"error,omitempty"`
}

// Log appends entries as JSON lines. It is safe for concurrent use.
type Log struct {
	mu   sync.Mutex
	w    io.Writer
	path string   // set when writing to a file
	f    *os.File // owned file, nil for caller-owned writers
}

// New returns a Log writing to w. The caller owns w.
func New(w io.Writer) *Log {
	return &Log{w: w}
}

// Open returns a Log appending to the file at path, creating it with mode
// 0600 if needed. Writes use O_APPEND, so external rotation (rename, then
// Reopen) loses no entries.
func Open(path string) (*Log, error) {
	f, err := openFile(path)
	if err != nil {
		return nil, err
	}
	return &Log{w: f, path: path, f: f}, nil
}

func openFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	return f, nil
}

// Write appends one entry.
func (l *Log) Write(e Entry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode audit entry: %w", err)
	}
	b = append(b, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.w.Write(b); err != nil {
		return fmt.Errorf("write audit entry: %w", err)
	}
	return nil
}

// Reopen closes and reopens the log file, for use after log rotation (for
// example on SIGHUP). It is a no-op for logs created with New.
func (l *Log) Reopen() error {
	if l.f == nil {
		return nil
	}
	f, err := openFile(l.path)
	if err != nil {
		return err
	}
	l.mu.Lock()
	old := l.f
	l.f, l.w = f, f
	l.mu.Unlock()
	return old.Close()
}

// Close closes the log file, if the Log owns one.
func (l *Log) Close() error {
	if l.f == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}
