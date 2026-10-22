// Package telemetry owns analytics-specific instrumentation. Shared middleware
// remains the sole owner of HTTP metrics and access logs.
package telemetry

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/duynhlab/analytics-service/internal/core/domain"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const scope = "github.com/duynhlab/analytics-service/internal/telemetry"
const snapshotTTL = 65 * time.Second

var sources = [...]string{"order", "checkout", "payment"}
var states = [...]string{"healthy", "stale", "unavailable", "unknown"}

type heartbeatReader interface {
	Heartbeats(context.Context) ([]domain.Heartbeat, error)
}

type snapshot struct {
	observed    time.Time
	lastSuccess time.Time
	stamps      [3]time.Time
	valid       bool
}

type freshnessSampler struct {
	reader       heartbeatReader
	now          func() time.Time
	current      atomic.Pointer[snapshot]
	registration metric.Registration
}

// StartFreshness samples immediately and every 30 seconds, independently of
// requests. The returned shutdown cancels and joins the sampler, then unregisters
// its callback. Call it before closing the database or telemetry provider.
func StartFreshness(ctx context.Context, reader heartbeatReader) (func(context.Context) error, error) {
	s, err := newFreshness(reader, otel.Meter(scope), time.Now)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.run(ctx)
	}()
	var once sync.Once
	var closeErr error
	return func(ctx context.Context) error {
		cancel()
		select {
		case <-done:
			once.Do(func() { closeErr = s.registration.Unregister() })
			return closeErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}, nil
}

func newFreshness(reader heartbeatReader, meter metric.Meter, now func() time.Time) (*freshnessSampler, error) {
	lag, err := meter.Float64ObservableGauge("analytics.source.lag", metric.WithUnit("s"), metric.WithDescription("Current target-observed heartbeat age"))
	if err != nil {
		return nil, err
	}
	state, err := meter.Int64ObservableGauge("analytics.source.state", metric.WithUnit("1"), metric.WithDescription("One-hot source freshness state"))
	if err != nil {
		return nil, err
	}
	last, err := meter.Float64ObservableGauge("analytics.freshness.last_success.time", metric.WithUnit("s"), metric.WithDescription("Unix time of last fully valid heartbeat read"))
	if err != nil {
		return nil, err
	}
	s := &freshnessSampler{reader: reader, now: now}
	s.current.Store(&snapshot{})
	s.registration, err = meter.RegisterCallback(func(_ context.Context, observer metric.Observer) error {
		snap := s.current.Load()
		now := s.now()
		valid := snap.valid && now.Sub(snap.observed) >= 0 && now.Sub(snap.observed) <= snapshotTTL
		for i, source := range sources {
			status := "unknown"
			age := now.Sub(snap.stamps[i])
			attrs := []attribute.KeyValue{attribute.String("analytics.source.name", source)}
			if valid && age >= 0 {
				status = "healthy"
				if age > 300*time.Second {
					status = "unavailable"
				} else if age > 120*time.Second {
					status = "stale"
				}
				observer.ObserveFloat64(lag, age.Seconds(), metric.WithAttributes(attrs...))
			}
			for _, candidate := range states {
				value := int64(0)
				if candidate == status {
					value = 1
				}
				observer.ObserveInt64(state, value, metric.WithAttributes(attrs[0], attribute.String("analytics.source.state", candidate)))
			}
		}
		if !snap.lastSuccess.IsZero() {
			observer.ObserveFloat64(last, float64(snap.lastSuccess.Unix())+float64(snap.lastSuccess.Nanosecond())/1e9)
		}
		return nil
	}, lag, state, last)
	return s, err
}

func (s *freshnessSampler) run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		s.sample(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *freshnessSampler) sample(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := s.reader.Heartbeats(ctx)
	now := s.now()
	next := &snapshot{observed: now, lastSuccess: s.current.Load().lastSuccess}
	if err == nil && ctx.Err() == nil {
		next.stamps, err = heartbeatStamps(rows, now)
		if err == nil {
			next.valid = true
			next.lastSuccess = now
		}
	}
	s.current.Store(next)
}

// A batch is atomic: a malformed result cannot certify only part of a read.
func heartbeatStamps(rows []domain.Heartbeat, now time.Time) ([3]time.Time, error) {
	var stamps [3]time.Time
	if len(rows) != len(sources) {
		return stamps, errors.New("invalid heartbeat batch")
	}
	for _, row := range rows {
		index, ok := sourceIndex(row.Name)
		if !ok || !stamps[index].IsZero() || row.At == nil || row.At.IsZero() || row.At.After(now) {
			return stamps, errors.New("invalid heartbeat batch")
		}
		stamps[index] = *row.At
	}
	return stamps, nil
}

// sourceIndex maps a source name to its fixed position; the switch keeps the
// bound explicit for the three-element arrays above.
func sourceIndex(name string) (int, bool) {
	switch name {
	case sources[0]:
		return 0, true
	case sources[1]:
		return 1, true
	case sources[2]:
		return 2, true
	}
	return 0, false
}
