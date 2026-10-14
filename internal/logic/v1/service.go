// Package v1 composes read-only commerce metrics and enforces freshness.
package v1

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/duynhlab/analytics-service/internal/core/domain"
	"github.com/duynhlab/pkg/obsx"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type Reader interface {
	Heartbeats(context.Context) ([]domain.Heartbeat, error)
	Read(context.Context, domain.Filters) (domain.Facts, error)
	Ready(context.Context) error
}
type Service struct {
	reader       Reader
	now          func() time.Time
	lag          metric.Float64Histogram
	observations metric.Int64Counter
}

func New(reader Reader, now func() time.Time) (*Service, error) {
	lag, err := otel.Meter("github.com/duynhlab/analytics-service").Float64Histogram("analytics.source.lag", metric.WithUnit("s"))
	if err != nil {
		return nil, fmt.Errorf("create lag metric: %w", err)
	}
	observations, err := otel.Meter("github.com/duynhlab/analytics-service").Int64Counter("analytics.source.observations")
	if err != nil {
		return nil, fmt.Errorf("create freshness metric: %w", err)
	}
	return &Service{reader: reader, now: now, lag: lag, observations: observations}, nil
}
func (s *Service) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return s.reader.Ready(ctx)
}
func (s *Service) Overview(ctx context.Context, filters domain.Filters) (response domain.Overview, retErr error) {
	if err := filters.Validate(); err != nil {
		return domain.Overview{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ctx, span := obsx.StartSpan(ctx, "github.com/duynhlab/analytics-service/internal/logic/v1", "analytics.overview")
	defer func() {
		if retErr != nil {
			obsx.RecordError(ctx, domain.ErrUnavailable)
		}
		span.End()
	}()
	if ctx.Err() != nil {
		return domain.Overview{}, domain.ErrUnavailable
	}
	heartbeats, err := s.reader.Heartbeats(ctx)
	if err != nil {
		s.observeFreshness(ctx, domain.Meta{})
		return domain.Overview{}, domain.ErrUnavailable
	}
	meta, err := freshness(heartbeats, s.now())
	s.observeFreshness(ctx, meta)
	if err != nil {
		return domain.Overview{}, err
	}
	if ctx.Err() != nil {
		return domain.Overview{}, domain.ErrUnavailable
	}
	facts, err := s.reader.Read(ctx, filters)
	if err != nil {
		return domain.Overview{}, domain.ErrUnavailable
	}
	// Retain the original observed heartbeats: a later heartbeat must not certify
	// data queries that finished before it was observed.
	meta, err = freshness(heartbeats, s.now())
	if err != nil || ctx.Err() != nil {
		return domain.Overview{}, domain.ErrUnavailable
	}
	result, err := assemble(filters, facts)
	if err != nil {
		return domain.Overview{}, err
	}
	result.Meta = meta
	return result, nil
}
func freshness(rows []domain.Heartbeat, now time.Time) (domain.Meta, error) {
	meta := domain.Meta{GeneratedAt: now.UTC(), Sources: make([]domain.Source, 0, 3)}
	if len(rows) != 3 {
		return meta, domain.ErrUnavailable
	}
	byName := make(map[string]*time.Time, 3)
	for _, row := range rows {
		if row.Name != "order" && row.Name != "checkout" && row.Name != "payment" {
			return meta, domain.ErrUnavailable
		}
		if _, ok := byName[row.Name]; ok {
			return meta, domain.ErrUnavailable
		}
		byName[row.Name] = row.At
	}
	unavailable := false
	for _, name := range []string{"order", "checkout", "payment"} {
		source := domain.Source{Name: name, State: "unavailable"}
		at := byName[name]
		if at == nil || at.IsZero() {
			unavailable = true
			meta.Sources = append(meta.Sources, source)
			continue
		}
		stamp := at.UTC()
		age := now.Sub(stamp)
		lag := age.Seconds()
		source.SyncedAt = &stamp
		source.LagSeconds = &lag
		switch {
		case age < 0 || age > 5*time.Minute:
			unavailable = true
		case age > 2*time.Minute:
			source.State = "stale"
			meta.Stale = true
		default:
			source.State = "healthy"
		}
		meta.Sources = append(meta.Sources, source)
		if meta.DataThrough == nil || stamp.Before(*meta.DataThrough) {
			meta.DataThrough = &stamp
		}
	}
	if unavailable {
		return meta, domain.ErrUnavailable
	}
	return meta, nil
}
func (s *Service) observeFreshness(ctx context.Context, meta domain.Meta) {
	for _, name := range []string{"order", "checkout", "payment"} {
		state := "unavailable"
		for _, source := range meta.Sources {
			if source.Name == name {
				state = source.State
				if source.LagSeconds != nil && *source.LagSeconds >= 0 {
					s.lag.Record(ctx, *source.LagSeconds, metric.WithAttributes(attribute.String("source", name)))
				}
			}
		}
		s.observations.Add(ctx, 1, metric.WithAttributes(attribute.String("source", name), attribute.String("state", state)))
	}
}

func assemble(filters domain.Filters, facts domain.Facts) (domain.Overview, error) {
	result := domain.Overview{Filters: filters, Funnel: facts.Funnel, Products: facts.Products, Trend: []domain.Daily{}}
	if result.Products == nil {
		result.Products = []domain.Product{}
	}
	for _, n := range []uint64{facts.CapturedOrders, facts.Funnel.Started, facts.Funnel.Created, facts.Funnel.Captured, facts.Funnel.Completed} {
		if n > domain.MaxSafeCount {
			return result, domain.ErrUnavailable
		}
	}
	if len(facts.Products) > 10 {
		return result, domain.ErrUnavailable
	}
	for _, p := range facts.Products {
		if _, err := domain.Integer(p.Subtotal); err != nil || p.Quantity > domain.MaxSafeCount || p.Orders > domain.MaxSafeCount {
			return result, domain.ErrUnavailable
		}
	}
	days := make(map[string]domain.Daily, len(facts.Daily))
	for _, row := range facts.Daily {
		if _, exists := days[row.Date]; exists {
			return result, domain.ErrUnavailable
		}
		if row.Date < filters.From || row.Date >= filters.To {
			return result, domain.ErrUnavailable
		}
		if _, err := time.Parse(time.DateOnly, row.Date); err != nil {
			return result, domain.ErrUnavailable
		}
		days[row.Date] = row
	}
	settled, refunded := new(big.Int), new(big.Int)
	from, to := filters.Bounds()
	for date := from; date.Before(to); date = date.AddDate(0, 0, 1) {
		key := date.Format(time.DateOnly)
		row, ok := days[key]
		if !ok {
			row = domain.Daily{Date: key, Settled: "0", Refunded: "0"}
		}
		a, e1 := domain.Integer(row.Settled)
		b, e2 := domain.Integer(row.Refunded)
		if e1 != nil || e2 != nil || b.Sign() < 0 {
			return result, domain.ErrUnavailable
		}
		settled.Add(settled, a)
		refunded.Add(refunded, b)
		row.Net = domain.Money(new(big.Int).Sub(a, b).String())
		result.Trend = append(result.Trend, row)
	}
	result.KPI = domain.KPI{Settled: domain.Money(settled.String()), Refunded: domain.Money(refunded.String()), Net: domain.Money(new(big.Int).Sub(settled, refunded).String()), CapturedOrders: facts.CapturedOrders}
	if facts.CapturedOrders > 0 {
		denominator := new(big.Int).SetUint64(facts.CapturedOrders)
		quotient, remainder := new(big.Int), new(big.Int)
		quotient.QuoRem(settled, denominator, remainder)
		if new(big.Int).Lsh(new(big.Int).Abs(remainder), 1).Cmp(denominator) >= 0 {
			quotient.Add(quotient, big.NewInt(int64(settled.Sign())))
		}
		average := domain.Money(quotient.String())
		result.KPI.Average = &average
	}
	return result, nil
}
