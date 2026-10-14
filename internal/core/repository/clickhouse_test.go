package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

type queryConn struct {
	driver.Conn
	rows driver.Rows
}

func (c queryConn) Query(context.Context, string, ...any) (driver.Rows, error) { return c.rows, nil }

type lateErrorRows struct {
	driver.Rows
	closed bool
}

func (r *lateErrorRows) Close() error {
	r.closed = true
	return errors.New("late server exception with sensitive details")
}
func (r *lateErrorRows) Err() error { return nil }
func TestLateStreamErrorFailsClosed(t *testing.T) {
	rows := &lateErrorRows{}
	repo, err := New(queryConn{rows: rows})
	if err != nil {
		t.Fatal(err)
	}
	err = repo.query(context.Background(), "test", "SELECT", nil, func(driver.Rows) error { return nil })
	if err == nil || err.Error() != "analytics unavailable" || !rows.closed {
		t.Fatal("late error ignored or leaked", err)
	}
}
