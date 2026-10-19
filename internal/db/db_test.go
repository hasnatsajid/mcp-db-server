package db

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hasnatsajid/mcp-db-server/internal/guard"
)

func TestSafeError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		want       string
		wantDenied bool
	}{
		{
			name: "server error keeps message, code and hint",
			err:  fmt.Errorf("query: %w", &pgconn.PgError{Message: `relation "x" does not exist`, Code: "42P01", Hint: "check the name"}),
			want: `relation "x" does not exist (SQLSTATE 42P01); hint: check the name`,
		},
		{
			name:       "permission error counts as denied",
			err:        &pgconn.PgError{Message: "permission denied for table users", Code: "42501"},
			want:       "permission denied for table users (SQLSTATE 42501)",
			wantDenied: true,
		},
		{
			name: "network error drops the address",
			err:  fmt.Errorf("conn to 192.0.2.3:5432: %w", &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 3), Port: 5432}, Err: errors.New("connect: connection refused")}),
			want: "network error: connect: connection refused",
		},
		{name: "timeout", err: fmt.Errorf("x: %w", context.DeadlineExceeded), want: "timed out"},
		{name: "unknown error is generic", err: errors.New("host db.internal unreachable"), want: "database error"},
		{name: "guard error passes through", err: fmt.Errorf("%w: nope", guard.ErrDenied), want: "guard: denied: nope", wantDenied: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SafeError(tt.err)
			if got.Error() != tt.want {
				t.Errorf("SafeError() = %q, want %q", got, tt.want)
			}
			if errors.Is(got, guard.ErrDenied) != tt.wantDenied {
				t.Errorf("errors.Is(ErrDenied) = %v, want %v", !tt.wantDenied, tt.wantDenied)
			}
			if !errors.Is(got, tt.err) && !strings.HasPrefix(tt.want, "guard") {
				t.Error("original error no longer reachable")
			}
		})
	}
	if SafeError(nil) != nil {
		t.Error("SafeError(nil) != nil")
	}
}

func TestNormalizeParams(t *testing.T) {
	t.Parallel()
	got := normalizeParams([]any{float64(3), 2.5, "x", nil, true, float64(1 << 60)})
	want := []any{int64(3), 2.5, "x", nil, true, float64(1 << 60)}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("param %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestJSONValue(t *testing.T) {
	t.Parallel()
	uuid := [16]byte{0xa0, 0xee, 0xbc, 0x99, 0x9c, 0x0b, 0x4e, 0xf8, 0xbb, 0x6d, 0x6b, 0xb9, 0xbd, 0x38, 0x0a, 0x11}
	tests := []struct {
		in   any
		want any
	}{
		{uuid, "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"},
		{[]byte{0xde, 0xad}, `\xdead`},
		{math.NaN(), "NaN"},
		{math.Inf(-1), "-Inf"},
		{1.5, 1.5},
		{int64(7), int64(7)},
	}
	for _, tt := range tests {
		if got := jsonValue(tt.in); got != tt.want {
			t.Errorf("jsonValue(%#v) = %#v, want %#v", tt.in, got, tt.want)
		}
	}
}
