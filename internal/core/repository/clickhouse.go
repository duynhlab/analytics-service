// Package repository reads only the reviewed commerce serving views.
package repository

import (
	"context"
	_ "embed"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/duynhlab/analytics-service/internal/core/domain"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

//go:embed sql/ledger.sql
var ledgerSQL string

//go:embed sql/daily.sql
var dailySQL string

//go:embed sql/captured.sql
var capturedSQL string

//go:embed sql/products.sql
var productsSQL string

//go:embed sql/funnel.sql
var funnelSQL string

//go:embed sql/heartbeats.sql
var heartbeatsSQL string

type ClickHouse struct {
	conn     driver.Conn
	duration metric.Float64Histogram
}

func New(conn driver.Conn) (*ClickHouse, error) {
	duration, err := otel.Meter("github.com/duynhlab/analytics-service").Float64Histogram("analytics.query.duration", metric.WithUnit("s"))
	if err != nil {
		return nil, err
	}
	return &ClickHouse{conn: conn, duration: duration}, nil
}

// Query limits apply independently of the outer five-second request budget.
// Error details never cross this boundary: driver errors may contain SQL/data.
func (r *ClickHouse) query(ctx context.Context, name, sql string, params clickhouse.Parameters, scan func(driver.Rows) error) (err error) {
	start := time.Now()
	defer func() {
		outcome := "success"
		if err != nil {
			outcome = "error"
		}
		r.duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attribute.String("query", name), attribute.String("outcome", outcome)))
	}()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ctx = clickhouse.Context(ctx, clickhouse.WithParameters(params), clickhouse.WithSettings(clickhouse.Settings{
		"max_execution_time": 2, "max_memory_usage": 536870912, "max_threads": 2,
		"max_result_rows": 1000, "result_overflow_mode": "throw", "timeout_overflow_mode": "throw", "join_use_nulls": 1,
	}))
	rows, e := r.conn.Query(ctx, sql)
	if e != nil {
		return domain.ErrUnavailable
	}
	defer func() {
		if rows.Close() != nil {
			err = domain.ErrUnavailable
		}
	}()
	if e = scan(rows); e != nil {
		return domain.ErrUnavailable
	}
	if rows.Close() != nil || rows.Err() != nil || ctx.Err() != nil {
		return domain.ErrUnavailable
	}
	return nil
}
func (r *ClickHouse) Heartbeats(ctx context.Context) ([]domain.Heartbeat, error) {
	result := make([]domain.Heartbeat, 0, 3)
	err := r.query(ctx, "heartbeats", heartbeatsSQL, nil, func(rows driver.Rows) error {
		for rows.Next() {
			var h domain.Heartbeat
			var count uint64
			if err := rows.Scan(&h.Name, &count, &h.At); err != nil {
				return err
			}
			if count != 1 {
				return domain.ErrUnavailable
			}
			result = append(result, h)
		}
		return nil
	})
	return result, err
}
func (r *ClickHouse) Ready(ctx context.Context) error {
	// A real serving-view read checks the connection and grants without certifying freshness.
	return r.query(ctx, "ready", "SELECT emitted_at FROM commerce.order_analytics_cdc_heartbeat_live LIMIT 1", nil, func(rows driver.Rows) error {
		for rows.Next() {
			var at time.Time
			if err := rows.Scan(&at); err != nil {
				return err
			}
		}
		return nil
	})
}
func (r *ClickHouse) Read(ctx context.Context, f domain.Filters) (domain.Facts, error) {
	facts := domain.Facts{}
	from, to := f.Bounds()
	params := clickhouse.Parameters{"currency": f.Currency, "from": from.Format("2006-01-02 15:04:05.000000"), "to": to.Format("2006-01-02 15:04:05.000000")}
	err := r.query(ctx, "daily", "WITH "+ledgerSQL+dailySQL, params, func(rows driver.Rows) error {
		for rows.Next() {
			var d domain.Daily
			var settled, refunded string
			var invalid uint64
			if e := rows.Scan(&d.Date, &settled, &refunded, &invalid); e != nil {
				return e
			}
			if invalid != 0 {
				return domain.ErrUnavailable
			}
			d.Settled = domain.Money(settled)
			d.Refunded = domain.Money(refunded)
			facts.Daily = append(facts.Daily, d)
		}
		return nil
	})
	if err != nil {
		return facts, err
	}
	err = r.query(ctx, "captured", "WITH "+ledgerSQL+capturedSQL+"SELECT toString(count()) FROM captured", params, func(rows driver.Rows) error {
		if !rows.Next() {
			return domain.ErrUnavailable
		}
		var n string
		if e := rows.Scan(&n); e != nil {
			return e
		}
		var e error
		facts.CapturedOrders, e = domain.Count(n)
		return e
	})
	if err != nil {
		return facts, err
	}
	err = r.query(ctx, "funnel", "WITH "+ledgerSQL+funnelSQL, params, func(rows driver.Rows) error {
		if !rows.Next() {
			return domain.ErrUnavailable
		}
		var ns [4]string
		var invalid uint64
		if e := rows.Scan(&ns[0], &ns[1], &ns[2], &ns[3], &invalid); e != nil {
			return e
		}
		if invalid != 0 {
			return domain.ErrUnavailable
		}
		values := [4]*uint64{&facts.Funnel.Started, &facts.Funnel.Created, &facts.Funnel.Captured, &facts.Funnel.Completed}
		for i, n := range ns {
			v, e := domain.Count(n)
			if e != nil {
				return e
			}
			*values[i] = v
		}
		return nil
	})
	if err != nil {
		return facts, err
	}
	err = r.query(ctx, "products", "WITH "+ledgerSQL+capturedSQL+productsSQL, params, func(rows driver.Rows) error {
		for rows.Next() {
			var p domain.Product
			var quantity, orders, subtotal string
			if e := rows.Scan(&p.ID, &p.Name, &quantity, &orders, &subtotal); e != nil {
				return e
			}
			var e error
			p.Quantity, e = domain.Count(quantity)
			if e != nil {
				return e
			}
			p.Orders, e = domain.Count(orders)
			if e != nil {
				return e
			}
			p.Subtotal = domain.Money(subtotal)
			facts.Products = append(facts.Products, p)
		}
		return nil
	})
	return facts, err
}
