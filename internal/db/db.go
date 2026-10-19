// Package db executes introspection and query calls against Postgres. Every
// call runs in its own READ ONLY transaction with statement and lock
// timeouts, and is always rolled back.
package db

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hasnatsajid/mcp-db-server/internal/guard"
)

// ErrNotFound is returned when a schema or table does not exist.
var ErrNotFound = errors.New("not found")

// Connect opens a connection pool and verifies it with a ping. Errors never
// include the connection string, host or address.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// The parse error can echo parts of the URL; don't pass it on.
		return nil, errors.New("DATABASE_URL is not a valid connection string")
	}
	cfg.MaxConns = 4
	cfg.ConnConfig.RuntimeParams["application_name"] = "mcp-db-server"
	// A second guard behind BEGIN READ ONLY: even a stray autocommit
	// statement on this connection cannot write.
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	// Unnamed prepared statements: arbitrary queries must not fill a cache,
	// and the extended protocol rejects multiple statements per call.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", SafeError(err))
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", SafeError(err))
	}
	return pool, nil
}

// SafeError reduces a pgx error to something safe to log or show a client:
// server errors keep their message, code and hint; connection errors lose the
// host and address they usually carry.
func SafeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, guard.ErrDenied) || errors.Is(err, ErrNotFound) {
		return err
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		msg := fmt.Sprintf("%s (SQLSTATE %s)", pgErr.Message, pgErr.Code)
		if pgErr.Hint != "" {
			msg += "; hint: " + pgErr.Hint
		}
		// The role's grants refused it: the same outcome as a policy denial.
		return &dbError{msg: msg, err: err, denied: pgErr.Code == "42501"}
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &dbError{msg: "timed out", err: err}
	case errors.Is(err, context.Canceled):
		return &dbError{msg: "canceled", err: err}
	}
	if opErr, ok := errors.AsType[*net.OpError](err); ok && opErr.Err != nil {
		return &dbError{msg: "network error: " + opErr.Err.Error(), err: err}
	}
	if _, ok := errors.AsType[*pgconn.ConnectError](err); ok {
		return &dbError{msg: "could not connect to database", err: err}
	}
	return &dbError{msg: "database error", err: err}
}

// dbError carries a redacted message while keeping the original error
// reachable for errors.Is/As. A permission error also matches
// guard.ErrDenied.
type dbError struct {
	msg    string
	err    error
	denied bool
}

func (e *dbError) Error() string        { return e.msg }
func (e *dbError) Unwrap() error        { return e.err }
func (e *dbError) Is(target error) bool { return e.denied && target == guard.ErrDenied }

// Executor runs guarded, read-only calls.
type Executor struct {
	pool    *pgxpool.Pool
	policy  guard.Policy
	timeout time.Duration
}

// New returns an Executor. timeout bounds every statement.
func New(pool *pgxpool.Pool, policy guard.Policy, timeout time.Duration) *Executor {
	return &Executor{pool: pool, policy: policy, timeout: timeout}
}

// readOnly runs fn in a READ ONLY transaction with statement_timeout,
// lock_timeout and standard_conforming_strings set locally. The transaction
// is always rolled back.
func (e *Executor) readOnly(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, e.timeout+2*time.Second)
	defer cancel()

	tx, err := e.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return SafeError(err)
	}
	defer func() {
		rbCtx, rbCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer rbCancel()
		_ = tx.Rollback(rbCtx) // nothing to keep; a failed rollback closes the conn
	}()

	ms := strconv.FormatInt(e.timeout.Milliseconds(), 10)
	if _, err := tx.Exec(ctx, `SELECT
		set_config('statement_timeout', $1, true),
		set_config('lock_timeout', $1, true),
		set_config('idle_in_transaction_session_timeout', $1, true),
		set_config('standard_conforming_strings', 'on', true)`, ms); err != nil {
		return SafeError(err)
	}
	return SafeError(fn(ctx, tx))
}
