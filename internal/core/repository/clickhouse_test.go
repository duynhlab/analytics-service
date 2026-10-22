package repository

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/duynhlab/analytics-service/internal/core/domain"
)

type queryConn struct {
	driver.Conn
	query func(context.Context) (driver.Rows, error)
}

func (c queryConn) Query(ctx context.Context, _ string, _ ...any) (driver.Rows, error) {
	return c.query(ctx)
}

type testRows struct {
	driver.Rows
	closes              int
	closeErr, streamErr error
	scan                func(...any) error
	next                func() bool
}

func (r *testRows) Close() error { r.closes++; return r.closeErr }
func (r *testRows) Err() error   { return r.streamErr }
func (r *testRows) Next() bool {
	if r.next != nil {
		return r.next()
	}
	return false
}
func (r *testRows) Scan(v ...any) error { return r.scan(v...) }

func TestQueryFailures(t *testing.T) {
	sensitive := errors.New("secret SQL and credentials")
	for _, name := range []string{"success", "query", "scan", "stream", "close", "cancel-before", "cancel-scan"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			rows := &testRows{}
			if name == "stream" {
				rows.streamErr = sensitive
			}
			if name == "close" {
				rows.closeErr = sensitive
			}
			calls := 0
			repo, err := New(queryConn{query: func(qctx context.Context) (driver.Rows, error) {
				calls++
				if d, ok := qctx.Deadline(); !ok || time.Until(d) > 2*time.Second {
					t.Error("missing query budget")
				}
				if name == "query" {
					return nil, sensitive
				}
				return rows, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if name == "cancel-before" {
				cancel()
			}
			err = repo.query(ctx, "test", "SELECT", nil, func(driver.Rows) error {
				if name == "cancel-scan" {
					cancel()
				}
				if name == "scan" {
					return sensitive
				}
				return nil
			})
			if name == "success" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, domain.ErrUnavailable) {
				t.Fatal(err)
			}
			want := 1
			if name == "query" || name == "cancel-before" {
				want = 0
			}
			if rows.closes != want {
				t.Fatalf("closed %d times, want %d", rows.closes, want)
			}
			if name == "cancel-before" && calls != 0 {
				t.Fatal("queried canceled context")
			}
		})
	}
}

func TestReadStopsOnFailure(t *testing.T) {
	for _, failure := range []string{"scan", "stream", "close"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			emitted := false
			rows := &testRows{next: func() bool {
				if emitted {
					return false
				}
				emitted = true
				return true
			}, scan: func(v ...any) error {
				if failure == "scan" {
					return errors.New("sensitive scan error")
				}
				for i, value := range []string{"2026-10-01", "100", "0"} {
					target, ok := v[i].(*string)
					if !ok {
						return errors.New("unexpected scan target")
					}
					*target = value
				}
				return nil
			}}
			if failure == "stream" {
				rows.streamErr = errors.New("stream failed")
			}
			if failure == "close" {
				rows.closeErr = errors.New("close failed")
			}
			repo, err := New(queryConn{query: func(context.Context) (driver.Rows, error) { calls++; return rows, nil }})
			if err != nil {
				t.Fatal(err)
			}
			facts, err := repo.Read(t.Context(), domain.Filters{Currency: "USD", From: "2026-10-01", To: "2026-10-02"})
			if !errors.Is(err, domain.ErrUnavailable) || calls != 1 || len(facts.Daily) != 0 || rows.closes != 1 {
				t.Fatalf("partial result or continued read: %+v %v calls=%d closes=%d", facts, err, calls, rows.closes)
			}
		})
	}
}

func TestQueryDeadline(t *testing.T) {
	for _, budget := range []time.Duration{2 * time.Second, 50 * time.Millisecond} {
		t.Run(budget.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := t.Context()
				if budget < 2*time.Second {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, budget)
					defer cancel()
				}
				started := time.Now()
				repo, err := New(queryConn{query: func(ctx context.Context) (driver.Rows, error) { <-ctx.Done(); return nil, ctx.Err() }})
				if err != nil {
					t.Fatal(err)
				}
				err = repo.query(ctx, "deadline", "SELECT", nil, func(driver.Rows) error { t.Fatal("scanned failed query"); return nil })
				if !errors.Is(err, domain.ErrUnavailable) || time.Since(started) != budget {
					t.Fatal(err, time.Since(started))
				}
			})
		})
	}
}
